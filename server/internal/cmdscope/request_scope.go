package cmdscope

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// SanitizeProjectCwd validates a client-supplied cwd used only to read
// <cwd>/.claude/commands and <cwd>/.claude/skills. It returns "" (disabling the
// project layer) unless the path is absolute and free of traversal segments —
// the only client-provided path this package reads, kept tightly bounded.
func SanitizeProjectCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	// Reject traversal in the RAW input: filepath.Clean would silently collapse
	// "/a/../../etc" to "/etc", so the check must happen before cleaning.
	if slices.Contains(strings.Split(cwd, string(filepath.Separator)), "..") {
		return ""
	}
	cleaned := filepath.Clean(cwd)
	if !filepath.IsAbs(cleaned) {
		return ""
	}
	return cleaned
}

// DefaultSpawnerSlug is the deployment-required fallback spawner. Must match the
// seed slug used elsewhere (services.claudeDefaultSpawnerSlug / di_seed.go).
const DefaultSpawnerSlug = "claude-default"

// SpawnerGetter is the narrow read surface of the spawner repo this package
// needs. Declared structurally so cmdscope does not import the repo package.
type SpawnerGetter interface {
	GetByID(ctx context.Context, id string) (*ent.Spawner, error)
	GetBySlug(ctx context.Context, slug string) (*ent.Spawner, error)
	// GetDefault returns the row flagged is_default. NotFound when none is set,
	// in which case resolution falls through to the slug backstop below.
	GetDefault(ctx context.Context) (*ent.Spawner, error)
}

// AgentsFn returns the current live agents (e.g. merger.GetAgents). Used to
// resolve a session's config dir + cwd for per-session scope.
type AgentsFn func(ctx context.Context) ([]sdk.Agent, error)

// ResolveRequestScope selects the enumeration scope for an HTTP request.
//
// Precedence — the result is ALWAYS spawner- or session-scoped, never an
// unfiltered global read, unless even the default spawner is unavailable:
//
//  1. sessionID matches a live agent → that session's detected config dir + cwd
//  2. spawnerID names a spawner row  → that spawner's config dir (+ request cwd)
//  3. is_default spawner → its config dir (+ request cwd)
//  4. claude-default slug backstop → its config dir (+ request cwd)
//  5. process default (~/.claude or CLAUDE_CONFIG_DIR)
//
// cwd should already be sanitized by the caller (absolute, no traversal); it is
// only used to read <cwd>/.claude/commands and <cwd>/.claude/skills.
func ResolveRequestScope(ctx context.Context, sessionID, spawnerID, cwd string, spawners SpawnerGetter, agents AgentsFn) Scope {
	if sessionID != "" && agents != nil {
		if list, err := agents(ctx); err == nil {
			for _, a := range list {
				if a.SessionID == sessionID {
					s := ResolveSessionScope(a.ClaudeConfigDir, a.CWD)
					s.Source = "session"
					s.Label = "session:" + sessionID
					return s
				}
			}
		}
	}

	if spawners != nil {
		if spawnerID != "" {
			if sp, err := spawners.GetByID(ctx, spawnerID); err == nil {
				s := ResolveSpawnerScope(sp, cwd)
				s.Source = "spawner"
				s.Label = sp.Slug
				return s
			}
		}
		if sp, err := spawners.GetDefault(ctx); err == nil {
			s := ResolveSpawnerScope(sp, cwd)
			s.Source = "default"
			s.Label = sp.Slug
			return s
		}
		if sp, err := spawners.GetBySlug(ctx, DefaultSpawnerSlug); err == nil {
			s := ResolveSpawnerScope(sp, cwd)
			s.Source = "default"
			s.Label = sp.Slug
			return s
		}
	}

	s := DefaultScope(cwd)
	s.Source = "process"
	s.Label = "default"
	return s
}
