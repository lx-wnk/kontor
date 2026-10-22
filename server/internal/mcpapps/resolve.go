package mcpapps

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

type RunApplications struct {
	Servers        map[string]json.RawMessage
	Allow          []string
	Deny           []string
	CatalogueTools map[string]bool
	// ArgDeny holds the presets' `tool(param:value)` rules. Claude Code honours
	// those only as --disallowedTools values, so they stay out of Deny, which
	// also feeds settings.json.
	ArgDeny []string
}

type MissingSecretError struct{ Server, EnvName string }

func (e *MissingSecretError) Error() string {
	return fmt.Sprintf("application %q is attached but its secret %s is not set", e.Server, e.EnvName)
}

type MissingServerError struct{ Server string }

func (e *MissingServerError) Error() string {
	return fmt.Sprintf("application %q is attached but has no server definition", e.Server)
}

// SpawnerNotAllowedError reports an explicitly attached application whose allowed
// spawners exclude the stage's spawner. Spawner is the spawner's name, or
// UnresolvedSpawner when the stage has none.
type SpawnerNotAllowedError struct{ Server, Spawner, Stage string }

func (e *SpawnerNotAllowedError) Error() string {
	return fmt.Sprintf("application %q is not allowed for spawner %q (stage %s)", e.Server, e.Spawner, e.Stage)
}

const UnresolvedSpawner = "(unresolved)"

type Resolver struct {
	Apps         repo.MCPApplicationRepo
	Secrets      repo.ApplicationSecretRepo
	Grants       repo.GrantRepo
	Capabilities repo.CapabilityRepo
	HomeRoot     string
}

// ResolveRun decides what one run gets. It calls capability.Decide directly and
// not memory.Gate.Authorize: rendering an allow list is not a use of the tool,
// and Authorize books rate-limit usage.
//
// An application with a non-empty AllowedSpawners list reaches only those
// spawners: an explicit attach to any other fails the run, an attach-all
// application is skipped. A nil spawner matches no list.
func (r Resolver) ResolveRun(ctx context.Context, task *ent.Task, spawner *ent.Spawner, stage string) (RunApplications, error) {
	out := RunApplications{Servers: map[string]json.RawMessage{}, CatalogueTools: map[string]bool{}}

	apps, err := r.Apps.List(ctx)
	if err != nil {
		return RunApplications{}, fmt.Errorf("mcpapps.ResolveRun: %w", err)
	}

	attached := make(map[string]bool, len(task.Applications))
	for _, id := range task.Applications {
		attached[id] = true
	}
	contexts := RunContexts(task)

	for _, app := range apps {
		explicit := attached[app.ResourceID]
		if !explicit && !app.AttachAll {
			continue
		}
		if !spawnerAllowed(app, spawner) {
			name := UnresolvedSpawner
			if spawner != nil {
				name = spawner.Name
			}
			if explicit {
				return RunApplications{}, &SpawnerNotAllowedError{Server: app.ServerName, Spawner: name, Stage: stage}
			}
			slog.Info("mcpapps: application skipped, spawner not allowed",
				"server", app.ServerName, "task", task.ID, "stage", stage, "spawner", name)
			continue
		}
		if IsEmptyEntry(app.Entry) {
			if explicit {
				return RunApplications{}, &MissingServerError{Server: app.ServerName}
			}
			continue
		}
		values, err := r.Secrets.Values(ctx, app.ResourceID)
		if err != nil {
			return RunApplications{}, fmt.Errorf("mcpapps.ResolveRun: %s: %w", app.ServerName, err)
		}
		for _, name := range app.RequiredEnv {
			if _, ok := values[name]; !ok {
				return RunApplications{}, &MissingSecretError{Server: app.ServerName, EnvName: name}
			}
		}
		preset, matched, err := matchedPreset(app)
		if err != nil {
			return RunApplications{}, err
		}
		if matched {
			values, err = WithIsolatedHome(values, preset, r.HomeRoot, app.ResourceID)
			if err != nil {
				return RunApplications{}, fmt.Errorf("mcpapps.ResolveRun: %s: %w", app.ServerName, err)
			}
		}
		merged, err := WithEnv(app.Entry, values)
		if err != nil {
			return RunApplications{}, err
		}
		out.Servers[app.ServerName] = merged
		if matched {
			out.ArgDeny = append(out.ArgDeny, preset.ArgDenyRules(app.ServerName)...)
		}

		for _, tool := range app.Catalogue {
			name := CapabilityName(app.ServerName, tool.Name)
			out.CatalogueTools[name] = true
			decision, err := r.decide(ctx, name, contexts)
			if err != nil {
				return RunApplications{}, err
			}
			switch decision.Effect {
			case capability.EffectAllow:
				out.Allow = append(out.Allow, name)
			case capability.EffectDeny:
				out.Deny = append(out.Deny, name)
			}
		}
	}
	return out, nil
}

func spawnerAllowed(app *ent.MCPApplication, spawner *ent.Spawner) bool {
	if len(app.AllowedSpawners) == 0 {
		return true
	}
	return spawner != nil && slices.Contains(app.AllowedSpawners, spawner.ID)
}

// matchedPreset finds the preset matching app's server entry.
func matchedPreset(app *ent.MCPApplication) (Preset, bool, error) {
	entry, err := ParseEntry(app.Entry)
	if err != nil {
		return Preset{}, false, fmt.Errorf("mcpapps.ResolveRun: %s: %w", app.ServerName, err)
	}
	preset, ok := FindPreset(entry)
	return preset, ok, nil
}

func (r Resolver) decide(ctx context.Context, capName string, contexts []capability.Context) (capability.Decision, error) {
	return Decide(ctx, r.Grants, r.Capabilities, capName, contexts)
}

// Decide resolves one capability against the given contexts, reading the
// capability's class (for the default-effect fallback) and its live grants.
// Exported so a caller outside Resolver — the pipeline enricher labels a
// pending permission with the same resolution a run's allow list uses — can
// reuse it without constructing a Resolver.
func Decide(ctx context.Context, grants repo.GrantRepo, caps repo.CapabilityRepo, capName string, contexts []capability.Context) (capability.Decision, error) {
	var view capability.CapabilityView
	if row, err := caps.Get(ctx, capName); err == nil {
		view = capability.CapabilityView{Name: row.Name, Class: row.Class, EnforceableBy: row.EnforceableBy}
	}
	rows, err := grants.ListForCapability(ctx, capName)
	if err != nil {
		return capability.Decision{}, fmt.Errorf("mcpapps: grants for %s: %w", capName, err)
	}
	return capability.Decide(
		capability.Request{Capability: capName, Contexts: contexts},
		repo.GrantViewsFromRows(rows),
		view,
	), nil
}

// RunContexts mirrors the chain the memory push uses: task, the routine that
// created it, the project scope — which the memory gate keys by working
// directory (stage_handlers.go) — and global.
func RunContexts(task *ent.Task) []capability.Context {
	out := []capability.Context{{Kind: repo.GrantContextTask, Ref: task.ID}}
	if task.RoutineID != nil {
		out = append(out, memory.RoutineContext(*task.RoutineID)...)
	}
	if task.Cwd != "" {
		out = append(out, capability.Context{Kind: repo.GrantContextProject, Ref: task.Cwd})
	}
	return append(out, capability.Context{Kind: repo.GrantContextGlobal})
}
