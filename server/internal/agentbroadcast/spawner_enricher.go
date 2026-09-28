// server/internal/agentbroadcast/spawner_enricher.go
package agentbroadcast

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/pathutil"
)

// ClaudeConfigDirEnv is the variable a Claude profile wrapper sets to point a
// session at its own config directory; it is what separates one configured
// spawner from another when both run the same command.
const ClaudeConfigDirEnv = "CLAUDE_CONFIG_DIR"

// Only the two reads this needs, so the repos can be stubbed without standing
// up their full interfaces.
type spawnerLister interface {
	List(ctx context.Context) ([]*ent.Spawner, error)
}

type taskLister interface {
	ListByIDs(ctx context.Context, ids []string) ([]*ent.Task, error)
}

// NewSpawnerEnricher annotates each agent with the spawner it belongs to.
//
// Two sources, most authoritative first:
//
//  1. task — the agent runs a pipeline stage whose task names a spawner.
//  2. env — the CLAUDE_CONFIG_DIR the scanner read out of the process, which
//     identifies the profile the session was started with. It comes off the
//     agent rather than from a second read of the process: it is read once per
//     scan (scanner.ProcessInfo.ClaudeConfigDir), it survives the process
//     exiting so finished cards keep their profile, and it cannot drift onto
//     an unrelated process that reused the PID.
//
// A dashboard-started session needs no third source: spawning applies the
// spawner's own env to the child, so the env source recognises it exactly. Two
// spawners that differ in nothing but command or args are indistinguishable
// this way — attribution then falls to the default one.
//
// Agents that match none keep an empty SpawnerID. The source travels with the
// annotation so the UI can mark a derived attribution as derived.
//
// Best-effort throughout: a missing repo, an unreadable process, or a query
// error leaves the annotation empty instead of failing the scan.
// A nil spawner repo returns a nil enricher, which merger.ChainEnrichers skips
// — matching NewHookEventEnricher, so the no-database path keeps composing away
// without the composition root carrying a per-enricher guard.
func NewSpawnerEnricher(spawners spawnerLister, tasks taskLister) merger.Enricher {
	return newSpawnerEnricher(spawners, tasks, newDirResolver(dirCacheTTL, time.Now))
}

func newSpawnerEnricher(spawners spawnerLister, tasks taskLister, dirs *dirResolver) merger.Enricher {
	if spawners == nil {
		return nil
	}
	return func(ctx context.Context, agents []sdk.Agent) {
		if len(agents) == 0 {
			return
		}
		rows, err := spawners.List(ctx)
		if err != nil {
			slog.Debug("spawner enricher: spawner list failed", "err", err)
			return
		}
		if len(rows) == 0 {
			return
		}

		byID := make(map[string]*ent.Spawner, len(rows))
		for _, s := range rows {
			byID[s.ID] = s
		}
		byConfigDir := indexByConfigDir(rows, dirs)

		taskSpawner := taskSpawnerIDs(ctx, tasks, agents)

		for i := range agents {
			id, source := attribute(&agents[i], taskSpawner, byConfigDir, dirs)
			if id == "" {
				continue
			}
			row, ok := byID[id]
			if !ok {
				continue
			}
			agents[i].SpawnerID = id
			agents[i].SpawnerName = row.Name
			agents[i].SpawnerSource = source
		}
	}
}

func attribute(
	agent *sdk.Agent,
	taskSpawner map[string]string,
	byConfigDir map[string]*ent.Spawner,
	dirs *dirResolver,
) (id, source string) {
	if agent.PipelineTaskID != "" {
		if fromTask := taskSpawner[agent.PipelineTaskID]; fromTask != "" {
			return fromTask, sdk.SpawnerSourceTask
		}
	}
	if !agent.ClaudeConfigDirKnown {
		// The environment was never read, so there is no profile to place this
		// on. Unassigned says that; the default spawner would claim a config dir
		// nothing observed, and the derived marker would present the guess as a
		// reading.
		return "", ""
	}
	dir := strings.TrimSpace(agent.ClaudeConfigDir)
	if dir == "" {
		// Read, and the variable is unset — the session runs on the user's
		// default config dir, which is what a spawner without one targets.
		dir = userDefaultConfigDir()
	}
	if match, ok := byConfigDir[dirs.canonical(dir)]; ok {
		return match.ID, sdk.SpawnerSourceEnv
	}
	return "", ""
}

// indexByConfigDir maps each spawner's config dir to it. A spawner that names no
// config dir targets the user's default one and is indexed under that, so a
// session either matches a declared directory or matches nothing. Two spawners
// can name the same directory (a symlink to the same store); the default one
// wins so the attribution is stable rather than map-order dependent.
func indexByConfigDir(rows []*ent.Spawner, dirs *dirResolver) map[string]*ent.Spawner {
	byDir := make(map[string]*ent.Spawner, len(rows))
	var fallback *ent.Spawner
	for _, s := range rows {
		dir := strings.TrimSpace(s.Env[ClaudeConfigDirEnv])
		if dir == "" {
			if fallback == nil || s.IsDefault {
				fallback = s
			}
			continue
		}
		key := dirs.canonical(dir)
		if prev, ok := byDir[key]; ok && prev.IsDefault {
			continue
		}
		byDir[key] = s
	}
	if fallback != nil {
		if key := dirs.canonical(userDefaultConfigDir()); key != "" {
			if prev, ok := byDir[key]; !ok || !prev.IsDefault {
				byDir[key] = fallback
			}
		}
	}
	return byDir
}

// userDefaultConfigDir is the config dir a session that sets no
// CLAUDE_CONFIG_DIR runs on: always ~/.claude.
//
// Deliberately not the server process's own CLAUDE_CONFIG_DIR. That variable
// says which dir this server was launched under — a different fact, and one
// that already has an owner in parser.AllClaudeConfigDirs (which session trees
// to scan). Reading it here made a server started under ~/.claude-work declare
// that dir to be the user default, so a spawner targeting the default profile
// claimed sessions on the work profile and lost the ones on ~/.claude.
func userDefaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// canonicalDir resolves ~, relative segments, and symlinks so that a spawner
// pointing at ~/.claude and a process reporting the store it links to compare
// equal. Tilde handling goes through pathutil so attribution expands exactly
// what BuildSpawnEnv exported to the process in the first place.
func canonicalDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	dir = pathutil.ExpandLeadingTilde(dir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(dir)
}

func taskSpawnerIDs(ctx context.Context, tasks taskLister, agents []sdk.Agent) map[string]string {
	if tasks == nil {
		return nil
	}
	ids := make([]string, 0, len(agents))
	seen := make(map[string]struct{}, len(agents))
	for i := range agents {
		id := agents[i].PipelineTaskID
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tasks.ListByIDs(ctx, ids)
	if err != nil {
		slog.Debug("spawner enricher: task batch lookup failed", "err", err)
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, t := range rows {
		if t.SpawnerID != nil && *t.SpawnerID != "" {
			out[t.ID] = *t.SpawnerID
		}
	}
	return out
}
