package materializer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/materializer"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// fixture builds two Claude config dirs and four provider config dirs on disk,
// mirroring the cross product spec §10 asks to be written out literally.
func fixture(t *testing.T) (claude []string, providers []parser.ProviderConfigDir, root string) {
	t.Helper()
	root = t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(p, 0o700))
		return p
	}
	claude = []string{mk(".claude"), mk(".claude-personal")}
	providers = []parser.ProviderConfigDir{
		{Provider: sdk.Provider("codex"), Path: mk(".codex")},
		{Provider: sdk.Provider("gemini"), Path: mk(".gemini")},
		{Provider: sdk.Provider("junie"), Path: mk(".junie")},
		{Provider: sdk.Provider("pi"), Path: mk(".pi")},
	}
	return claude, providers, root
}

func newResolver(t *testing.T) (materializer.Resolver, string) {
	t.Helper()
	claude, providers, root := fixture(t)
	return materializer.Resolver{
		NodeID:             "local",
		ClaudeConfigDirs:   func() []string { return claude },
		ProviderConfigDirs: func() []parser.ProviderConfigDir { return providers },
	}, root
}

func keys(targets []materializer.Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Key())
	}
	return out
}

func TestTargets_GlobalScopeIsTheFullCrossProduct(t *testing.T) {
	r, root := newResolver(t)

	got := r.Targets(repo.GlobalScope())

	require.Equal(t, []string{
		"claude|user|" + filepath.Join(root, ".claude"),
		"claude|user|" + filepath.Join(root, ".claude-personal"),
		"codex|user|" + filepath.Join(root, ".codex"),
		"gemini|user|" + filepath.Join(root, ".gemini"),
		"junie|user|" + filepath.Join(root, ".junie"),
		"pi|user|" + filepath.Join(root, ".pi"),
	}, keys(got))
}

func TestTargets_OnlyClaudeHasASkillFormat(t *testing.T) {
	r, _ := newResolver(t)

	for _, target := range r.Targets(repo.GlobalScope()) {
		if target.Provider == "claude" {
			require.Equal(t, materializer.AdapterClaude, target.Adapter)
			continue
		}
		require.Equal(t, materializer.AdapterNone, target.Adapter,
			"none of the four providers ships a SKILL.md equivalent")
	}
}

func TestTargets_ProjectScopeTargetsTheProjectDirAndStillReportsProviders(t *testing.T) {
	r, root := newResolver(t)
	project := filepath.Join(root, "work", "repo")
	require.NoError(t, os.MkdirAll(project, 0o700))

	got := r.Targets(repo.ProjectScope(project))

	require.Equal(t, []string{
		"claude|project|" + project,
		"codex|user|" + filepath.Join(root, ".codex"),
		"gemini|user|" + filepath.Join(root, ".gemini"),
		"junie|user|" + filepath.Join(root, ".junie"),
		"pi|user|" + filepath.Join(root, ".pi"),
	}, keys(got))
}

func TestTargets_NonExistentDirectoriesAreSkippedNotCreated(t *testing.T) {
	root := t.TempDir()
	present := filepath.Join(root, ".claude")
	require.NoError(t, os.MkdirAll(present, 0o700))

	r := materializer.Resolver{
		NodeID:             "local",
		ClaudeConfigDirs:   func() []string { return []string{present, filepath.Join(root, ".claude-work")} },
		ProviderConfigDirs: func() []parser.ProviderConfigDir { return nil },
	}

	require.Equal(t, []string{"claude|user|" + present}, keys(r.Targets(repo.GlobalScope())),
		"inventing a config directory for a runtime the user has not set up is not this component's business")
}

func TestTargets_ProjectScopeWithAMissingProjectDirYieldsNoClaudeTarget(t *testing.T) {
	r, root := newResolver(t)

	got := r.Targets(repo.ProjectScope(filepath.Join(root, "gone")))

	for _, target := range got {
		require.NotEqual(t, materializer.LayerProject, target.Layer)
	}
}

func TestTargets_ApplicationScopeHasNoPathTemplate(t *testing.T) {
	r, _ := newResolver(t)

	got := r.Targets(repo.ApplicationScope("app-1"))

	for _, target := range got {
		require.Equal(t, materializer.AdapterNone, target.Adapter,
			"spec §3 has templates for user and project only; a third is not guessed")
	}
}
