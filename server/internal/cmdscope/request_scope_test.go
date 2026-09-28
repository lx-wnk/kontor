package cmdscope

import (
	"context"
	"errors"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/stretchr/testify/require"
)

type fakeSpawners struct {
	byID   map[string]*ent.Spawner
	bySlug map[string]*ent.Spawner
}

func (f fakeSpawners) GetByID(_ context.Context, id string) (*ent.Spawner, error) {
	if sp, ok := f.byID[id]; ok {
		return sp, nil
	}
	return nil, errors.New("not found")
}

func (f fakeSpawners) GetBySlug(_ context.Context, slug string) (*ent.Spawner, error) {
	if sp, ok := f.bySlug[slug]; ok {
		return sp, nil
	}
	return nil, errors.New("not found")
}

func (f fakeSpawners) GetDefault(_ context.Context) (*ent.Spawner, error) {
	for _, sp := range f.byID {
		if sp.IsDefault {
			return sp, nil
		}
	}
	return nil, errors.New("not found")
}

func agentsReturning(list ...sdk.Agent) AgentsFn {
	return func(_ context.Context) ([]sdk.Agent, error) { return list, nil }
}

func TestResolveRequestScope_SessionWins(t *testing.T) {
	agents := agentsReturning(sdk.Agent{SessionID: "sess-1", CWD: "/proj", ClaudeConfigDir: "/home/u/.claude-work"})
	got := ResolveRequestScope(context.Background(), "sess-1", "ignored-spawner", "/ignored-cwd", fakeSpawners{}, agents)

	require.Equal(t, "session", got.Source)
	require.Equal(t, "session:sess-1", got.Label)
	require.Equal(t, "/home/u/.claude-work", got.ConfigDir)
	require.Equal(t, "/proj", got.ProjectCwd, "session cwd, not the request cwd")
}

func TestResolveRequestScope_UnknownSessionFallsThroughToSpawner(t *testing.T) {
	sp := &ent.Spawner{Slug: "claude-work", Command: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/cfg/work"}, AdapterType: "claude"}
	spawners := fakeSpawners{byID: map[string]*ent.Spawner{"sp-7": sp}}

	got := ResolveRequestScope(context.Background(), "no-such-session", "sp-7", "/cwd", spawners, agentsReturning())
	require.Equal(t, "spawner", got.Source)
	require.Equal(t, "claude-work", got.Label)
	require.Equal(t, "/cfg/work", got.ConfigDir)
	require.Equal(t, "/cwd", got.ProjectCwd)
}

func TestResolveRequestScope_DefaultSpawnerFallback(t *testing.T) {
	def := &ent.Spawner{Slug: DefaultSpawnerSlug, Command: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": "/cfg/default"}, AdapterType: "claude"}
	spawners := fakeSpawners{bySlug: map[string]*ent.Spawner{DefaultSpawnerSlug: def}}

	got := ResolveRequestScope(context.Background(), "", "", "/cwd", spawners, nil)
	require.Equal(t, "default", got.Source)
	require.Equal(t, DefaultSpawnerSlug, got.Label)
	require.Equal(t, "/cfg/default", got.ConfigDir)
}

func TestResolveRequestScope_ProcessFallbackWhenNoSpawners(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/proc/cfg")
	got := ResolveRequestScope(context.Background(), "", "", "/cwd", nil, nil)
	require.Equal(t, "process", got.Source)
	require.Equal(t, "/proc/cfg", got.ConfigDir)
}

func TestSanitizeProjectCwd(t *testing.T) {
	require.Equal(t, "", SanitizeProjectCwd(""))
	require.Equal(t, "", SanitizeProjectCwd("relative/path"), "non-absolute rejected")
	require.Equal(t, "", SanitizeProjectCwd("/a/../../etc"), "traversal rejected")
	require.Equal(t, "/a/b", SanitizeProjectCwd("/a/b/"), "clean absolute kept")
}
