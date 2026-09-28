// Package config provides HTTP handlers for enumerating and editing the Claude
// configuration available within a resolved scope: installed skills, slash
// commands, and context files (CLAUDE.md / AGENTS.md). Reads and writes are
// confined to the scope's enumerated, editable members (see file.go).
//
// Enumeration is scoped per spawner or per live session via cmdscope, so the
// "Config" explorer reflects the capability set a given spawner/session
// actually sees (e.g. claude-work's ~/.claude-work) rather than a fixed global
// view. The only client-supplied path accepted is ?cwd, which is sanitized and
// used solely to read <cwd>/.claude/{commands,skills} and project context files.
package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/cmdscope"
)

// CwdPolicy gates the client-supplied project cwd to the same roots the spawn
// path permits, so the editable set cannot be widened to an arbitrary directory
// on disk. Allow returns nil when cwd is permitted. A nil policy disables the
// check (dev/bypass-auth with no project roots).
type CwdPolicy interface {
	Allow(ctx context.Context, cwd string) error
}

// Handler enumerates skills/commands/memory for a resolved scope.
type Handler struct {
	spawners  cmdscope.SpawnerGetter
	agents    cmdscope.AgentsFn
	cwdPolicy CwdPolicy
}

// NewHandler constructs the config explorer handler. Any dep may be nil; scope
// resolution then falls back to the process-default scope, and a nil cwdPolicy
// leaves the client cwd ungated.
func NewHandler(spawners cmdscope.SpawnerGetter, agents cmdscope.AgentsFn, cwdPolicy CwdPolicy) *Handler {
	return &Handler{spawners: spawners, agents: agents, cwdPolicy: cwdPolicy}
}

// ContextFileEntry describes a single context file (CLAUDE.md / AGENTS.md).
type ContextFileEntry struct {
	Path     string `json:"path"`
	Scope    string `json:"scope"` // "user" | "project"
	Size     int64  `json:"size"`
	MTime    int64  `json:"mtime"` // unix seconds
	Editable bool   `json:"editable"`
}

type skillsResponse struct {
	Skills      []cmdscope.SkillEntry `json:"skills"`
	ScopeSource string                `json:"scopeSource"`
	ScopeLabel  string                `json:"scopeLabel"`
}

type commandsResponse struct {
	Commands           []cmdscope.CommandDetail `json:"commands"`
	EngineVersion      string                   `json:"engineVersion,omitempty"`
	BuiltinsMayBeStale bool                     `json:"builtinsMayBeStale"`
	ScopeSource        string                   `json:"scopeSource"`
	ScopeLabel         string                   `json:"scopeLabel"`
}

// contextFilesResponse keeps the wire tag "memory" so the payload stays
// identical to what /api/config/memory always returned — old and new paths
// must answer identically during the deprecation window.
type contextFilesResponse struct {
	ContextFiles []ContextFileEntry `json:"memory"`
	ScopeSource  string             `json:"scopeSource"`
	ScopeLabel   string             `json:"scopeLabel"`
}

func (h *Handler) resolve(r *http.Request) cmdscope.Scope {
	q := r.URL.Query()
	cwd := cmdscope.SanitizeProjectCwd(q.Get("cwd"))
	// Confine the project layer to roots the spawn path allows; a cwd outside
	// them is dropped so the editable set never reaches arbitrary directories.
	if cwd != "" && h.cwdPolicy != nil {
		if err := h.cwdPolicy.Allow(r.Context(), cwd); err != nil {
			cwd = ""
		}
	}
	return cmdscope.ResolveRequestScope(r.Context(), q.Get("sessionId"), q.Get("spawnerId"), cwd, h.spawners, h.agents)
}

// Skills handles GET /api/config/skills.
func (h *Handler) Skills(w http.ResponseWriter, r *http.Request) {
	scope := h.resolve(r)
	writeJSON(w, skillsResponse{
		Skills:      scope.Skills(),
		ScopeSource: scope.Source,
		ScopeLabel:  scope.Label,
	})
}

// Commands handles GET /api/config/commands.
func (h *Handler) Commands(w http.ResponseWriter, r *http.Request) {
	scope := h.resolve(r)
	// Only Claude scopes have a slash-command surface; skip the version probe
	// (an exec of scope.Command) for unsupported adapters, which enumerate empty.
	var version string
	var ok bool
	if scope.Supported {
		version, ok = cmdscope.ProbeEngineVersion(scope.Command)
	}
	writeJSON(w, commandsResponse{
		Commands:           scope.CommandDetails(),
		EngineVersion:      version,
		BuiltinsMayBeStale: cmdscope.BuiltinsMayBeStale(version, ok),
		ScopeSource:        scope.Source,
		ScopeLabel:         scope.Label,
	})
}

// ContextFiles handles GET /api/config/context-files.
func (h *Handler) ContextFiles(w http.ResponseWriter, r *http.Request) {
	scope := h.resolve(r)
	writeJSON(w, contextFilesResponse{
		ContextFiles: enumerateMemoryFiles(scope),
		ScopeSource:  scope.Source,
		ScopeLabel:   scope.Label,
	})
}

var logLegacyMemoryPathOnce sync.Once

// Memory handles the deprecated GET /api/config/memory. It answers exactly
// like ContextFiles and logs the deprecation once per process so a client
// still on the old path keeps working during the deprecation window.
//
// Deprecated: use ContextFiles. When this method goes, rename the response
// struct's "memory" JSON key to "contextFiles" — it only kept the old name so
// both paths return byte-identical payloads while this one still exists.
func (h *Handler) Memory(w http.ResponseWriter, r *http.Request) {
	logLegacyMemoryPathOnce.Do(func() {
		slog.Info("config: /api/config/memory is deprecated, use /api/config/context-files instead")
	})
	h.ContextFiles(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// enumerateMemoryFiles lists the known memory files for the scope:
//   - user:    <ConfigDir>/CLAUDE.md, <ConfigDir>/AGENTS.md
//   - project: <cwd>/CLAUDE.md, <cwd>/AGENTS.md, <cwd>/.claude/CLAUDE.md
//
// When the scope carries no project cwd, the server's own working directory is
// used for the project scope (preserving the pre-scoping behavior).
func enumerateMemoryFiles(scope cmdscope.Scope) []ContextFileEntry {
	out := []ContextFileEntry{}

	type candidate struct {
		scope string
		path  string
	}
	var candidates []candidate

	if scope.ConfigDir != "" {
		candidates = append(candidates,
			candidate{"user", filepath.Join(scope.ConfigDir, "CLAUDE.md")},
			candidate{"user", filepath.Join(scope.ConfigDir, "AGENTS.md")},
		)
	}

	projectDir := scope.ProjectCwd
	if projectDir == "" {
		if wd, err := os.Getwd(); err == nil {
			projectDir = wd
		}
	}
	if projectDir != "" {
		candidates = append(candidates,
			candidate{"project", filepath.Join(projectDir, "CLAUDE.md")},
			candidate{"project", filepath.Join(projectDir, "AGENTS.md")},
			candidate{"project", filepath.Join(projectDir, ".claude", "CLAUDE.md")},
		)
	}

	for _, c := range candidates {
		info, err := os.Stat(c.path)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, ContextFileEntry{
			Path:     c.path,
			Scope:    c.scope,
			Size:     info.Size(),
			MTime:    info.ModTime().Unix(),
			Editable: cmdscope.IsEditableSource(c.scope),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		return out[i].Path < out[j].Path
	})
	return out
}
