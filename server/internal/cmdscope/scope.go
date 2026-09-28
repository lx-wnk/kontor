// Package cmdscope resolves and enumerates the Claude Code slash commands and
// skills available within a given scope — a spawner (which may point at a
// non-default CLAUDE_CONFIG_DIR) or a live monitored session (which carries the
// config dir detected from its process env).
//
// It is the single canonical source for command/skill enumeration: the HTTP
// handlers delegate here so the built-in list and the frontmatter parsing live
// in exactly one place.
package cmdscope

import (
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pathutil"
)

// Scope is the resolved context a command/skill set is enumerated for.
type Scope struct {
	// ConfigDir is the resolved Claude config root (e.g. /home/u/.claude-work).
	// Never empty when Supported is true.
	ConfigDir string
	// ProjectCwd, when non-empty, enables enumeration of project-local
	// <cwd>/.claude/commands and <cwd>/.claude/skills.
	ProjectCwd string
	// Command is the launcher binary (e.g. "claude") used for the version probe.
	Command string
	// Supported is false for spawners whose adapter is not Claude Code; such
	// spawners have no slash-command / skill concept, so enumeration is empty.
	Supported bool
	// Source records how the scope was selected: "session" | "spawner" |
	// "default" | "process". Set by ResolveRequestScope; empty otherwise.
	Source string
	// Label is a human-readable scope identifier for the UI (e.g. a spawner
	// slug, "session:<id>", or "default"). Set by ResolveRequestScope.
	Label string
}

// defaultConfigDir is the server's configured config root (claude.configDir
// setting, else CLAUDE_CONFIG_DIR, else ~/.claude).
func defaultConfigDir() string {
	return claudeconfig.ConfigDir()
}

// ResolveSpawnerScope builds a Scope from a spawner row and optional project cwd.
//
// ConfigDir precedence: spawner env CLAUDE_CONFIG_DIR (tilde-expanded the way a
// shell would, since exec does not expand it) → process CLAUDE_CONFIG_DIR →
// ~/.claude. Command defaults to "claude" when the spawner declares none.
//
// Supported is true only for the Claude adapter (empty or "claude"); other
// adapter types (ollama/openai/custom) drive non-Claude engines that have no
// slash-command surface, so their scope enumerates nothing.
func ResolveSpawnerScope(sp *ent.Spawner, projectCwd string) Scope {
	if sp != nil && sp.AdapterType != "" && sp.AdapterType != "claude" {
		return Scope{ProjectCwd: projectCwd, Command: sp.Command, Supported: false}
	}

	configDir := ""
	command := "claude"
	if sp != nil {
		if v, ok := sp.Env["CLAUDE_CONFIG_DIR"]; ok && v != "" {
			configDir = pathutil.ExpandLeadingTilde(v)
		}
		if sp.Command != "" {
			command = sp.Command
		}
	}
	if configDir == "" {
		configDir = defaultConfigDir()
	}
	return Scope{ConfigDir: configDir, ProjectCwd: projectCwd, Command: command, Supported: true}
}

// ResolveSessionScope builds a Scope for a live monitored session from its
// detected CLAUDE_CONFIG_DIR (empty → default) and working directory.
func ResolveSessionScope(claudeConfigDir, cwd string) Scope {
	configDir := claudeConfigDir
	if configDir == "" {
		configDir = defaultConfigDir()
	}
	return Scope{ConfigDir: configDir, ProjectCwd: cwd, Command: "claude", Supported: true}
}

// DefaultScope is the process-default scope (~/.claude or CLAUDE_CONFIG_DIR),
// used when no spawner or session context is supplied.
func DefaultScope(projectCwd string) Scope {
	return Scope{ConfigDir: defaultConfigDir(), ProjectCwd: projectCwd, Command: "claude", Supported: true}
}
