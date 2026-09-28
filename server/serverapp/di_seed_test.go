// Tests for the boot-migration paths in di_seed.go. The deprecated config
// types are referenced intentionally — see the file-level note in di_seed.go.
//
//lint:file-ignore SA1019 Boot-migration tests intentionally exercise deprecated config shapes.

package serverapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/config"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newSpawnerRepoForTest(t *testing.T) repo.SpawnerRepo {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewSpawnerRepo(bundle.Client)
}

func TestMigrateAdapterConfigToSpawners_Empty_NoRows(t *testing.T) {
	t.Setenv("DASHBOARD_SPAWN_COMMAND", "")
	r := newSpawnerRepoForTest(t)

	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), config.Config{}, r))

	spawners, err := r.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, spawners, "no rows should be created for an empty AdapterConfig")
}

func TestMigrateAdapterConfigToSpawners_Ollama(t *testing.T) { //nolint:staticcheck // boot-migration test asserts deprecated config shape
	t.Setenv("DASHBOARD_SPAWN_COMMAND", "")
	r := newSpawnerRepoForTest(t)

	cfg := config.Config{Adapters: config.AdapterConfig{ //nolint:staticcheck // boot-migration test asserts deprecated config shape
		Ollama: config.OllamaConfig{Host: "http://localhost:11434", DefaultModel: "llama3"}, //nolint:staticcheck // boot-migration test asserts deprecated config shape
	}}

	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), cfg, r))

	s, err := r.GetBySlug(t.Context(), importedOllamaSlug)
	require.NoError(t, err)
	require.Equal(t, "Ollama (imported)", s.Name)
	require.Equal(t, "ollama", s.AdapterType)
	require.Equal(t, map[string]string{
		"host":          "http://localhost:11434",
		"default_model": "llama3",
	}, s.AdapterConfig)
	require.False(t, s.BuiltIn)
}

func TestMigrateAdapterConfigToSpawners_Ollama_OnlyHost(t *testing.T) { //nolint:staticcheck // boot-migration test asserts deprecated config shape
	t.Setenv("DASHBOARD_SPAWN_COMMAND", "")
	r := newSpawnerRepoForTest(t)

	cfg := config.Config{Adapters: config.AdapterConfig{ //nolint:staticcheck // boot-migration test asserts deprecated config shape
		Ollama: config.OllamaConfig{Host: "http://localhost:11434"}, //nolint:staticcheck // boot-migration test asserts deprecated config shape
	}}

	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), cfg, r))

	s, err := r.GetBySlug(t.Context(), importedOllamaSlug)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"host": "http://localhost:11434"}, s.AdapterConfig,
		"only non-empty keys should be included")
}

func TestMigrateAdapterConfigToSpawners_OpenAI(t *testing.T) { //nolint:staticcheck // boot-migration test asserts deprecated config shape
	t.Setenv("DASHBOARD_SPAWN_COMMAND", "")
	r := newSpawnerRepoForTest(t)

	cfg := config.Config{Adapters: config.AdapterConfig{ //nolint:staticcheck // boot-migration test asserts deprecated config shape
		OpenAI: config.OpenAIConfig{ //nolint:staticcheck // boot-migration test asserts deprecated config shape
			BaseURL:      "https://api.openai.com/v1",
			APIKeyEnv:    "OPENAI_API_KEY",
			DefaultModel: "gpt-4o",
		},
	}}

	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), cfg, r))

	s, err := r.GetBySlug(t.Context(), importedOpenAISlug)
	require.NoError(t, err)
	require.Equal(t, "OpenAI (imported)", s.Name)
	require.Equal(t, "openai", s.AdapterType)
	require.Equal(t, map[string]string{
		"base_url":      "https://api.openai.com/v1",
		"api_key_env":   "OPENAI_API_KEY",
		"default_model": "gpt-4o",
	}, s.AdapterConfig)
}

func TestMigrateAdapterConfigToSpawners_SpawnCommandEnv(t *testing.T) {
	t.Setenv("DASHBOARD_SPAWN_COMMAND", "/usr/local/bin/my-spawner")
	r := newSpawnerRepoForTest(t)

	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), config.Config{}, r))

	s, err := r.GetBySlug(t.Context(), importedCustomSlug)
	require.NoError(t, err)
	require.Equal(t, "Custom (imported)", s.Name)
	require.Equal(t, "custom", s.AdapterType)
	require.Equal(t, "/usr/local/bin/my-spawner", s.Command)
	require.Empty(t, s.AdapterConfig)
}

func TestMigrateAdapterConfigToSpawners_Idempotent(t *testing.T) {
	t.Setenv("DASHBOARD_SPAWN_COMMAND", "/usr/local/bin/x")
	r := newSpawnerRepoForTest(t)

	cfg := config.Config{Adapters: config.AdapterConfig{ //nolint:staticcheck // boot-migration test asserts deprecated config shape
		Default: "ollama",
		Ollama:  config.OllamaConfig{Host: "h"},    //nolint:staticcheck // boot-migration test asserts deprecated config shape
		OpenAI:  config.OpenAIConfig{BaseURL: "b"}, //nolint:staticcheck // boot-migration test asserts deprecated config shape
	}}

	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), cfg, r))
	require.NoError(t, migrateAdapterConfigToSpawners(t.Context(), cfg, r))

	all, err := r.List(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 3, "expected one row per imported adapter, no duplicates")

	slugs := map[string]struct{}{}
	for _, s := range all {
		slugs[s.Slug] = struct{}{}
	}
	require.Contains(t, slugs, importedOllamaSlug)
	require.Contains(t, slugs, importedOpenAISlug)
	require.Contains(t, slugs, importedCustomSlug)
}

func TestMigrateAdapterConfigToSpawners_NilRepo_NoOp(t *testing.T) {
	t.Parallel()
	require.NoError(t, migrateAdapterConfigToSpawners(context.Background(), config.Config{}, nil))
}

// ensure ent package is still required by an indirect dependency (silences
// unused-import paranoia in case future refactors drop direct uses).
var _ = ent.IsNotFound
