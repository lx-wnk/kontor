// This file performs the one-time boot migration that lifts legacy
// environment variables and adapter-config.json entries into Spawner rows.
// It must reference the deprecated config types by design — they exist
// solely so historical configurations still unmarshal cleanly here.
//
//lint:file-ignore SA1019 Boot-migration code intentionally consumes deprecated config shapes.

package serverapp

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/config"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// repairSpawnerAdapterConfig replaces any row in spawners whose adapter_config
// column is not a valid JSON value with an empty object. Defends against a
// historical bug in this schema where the SQL DEFAULT was emitted as the
// literal string "”{}”" instead of "{}", which then crashed every spawner
// load with an "invalid character '\”" unmarshal error. Idempotent; runs
// before seedSpawners so the subsequent GetBySlug call cannot trip the bug.
func repairSpawnerAdapterConfig(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	res, err := db.ExecContext(ctx,
		`UPDATE spawners SET adapter_config = '{}' WHERE json_valid(adapter_config) = 0`,
	)
	if err != nil {
		return fmt.Errorf("repairSpawnerAdapterConfig: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		slog.Warn("repaired spawner rows with corrupt adapter_config", "rows", n)
	}
	return nil
}

// claudeDefaultSpawnerSlug is the slug of the built-in spawner that exists in
// every dashboard installation. It is the final fallback in the spawner
// resolution chain (task.spawnerId ?? project.defaultSpawnerId ?? claude-default).
const claudeDefaultSpawnerSlug = "claude-default"

// Slugs used by migrateAdapterConfigToSpawners. Stable so re-invocation is idempotent.
const (
	importedOllamaSlug = "imported-ollama"
	importedOpenAISlug = "imported-openai"
	importedCustomSlug = "imported-custom"
)

// seedSpawners inserts the built-in claude-default spawner if it is not already
// present, then guarantees exactly one spawner is flagged is_default. Idempotent:
// once the row exists and a default is set, it is a no-op.
func seedSpawners(ctx context.Context, spawnerRepo repo.SpawnerRepo) error {
	if spawnerRepo == nil {
		return nil
	}
	claudeDefault, err := spawnerRepo.GetBySlug(ctx, claudeDefaultSpawnerSlug)
	switch {
	case err == nil:
		// already present — fall through to the default-flag guard below.
	case ent.IsNotFound(err):
		description := "Built-in Claude CLI spawner — cannot be deleted."
		claudeDefault, err = spawnerRepo.Create(
			ctx,
			"Claude (default)",
			claudeDefaultSpawnerSlug,
			"claude",
			[]string{},
			map[string]string{},
			nil,
			&description,
			"claude",
			map[string]string{},
			true,
		)
		if err != nil {
			return fmt.Errorf("seedSpawners: create claude-default: %w", err)
		}
		slog.Info("seeded built-in spawner", "slug", claudeDefaultSpawnerSlug)
	default:
		return fmt.Errorf("seedSpawners: lookup claude-default: %w", err)
	}

	// Guarantee a default exists. Covers fresh installs (just-created row) and
	// pre-is_default databases (column backfilled to false on auto-migrate, so
	// no row is flagged yet). A user-chosen default is left untouched.
	if _, err := spawnerRepo.GetDefault(ctx); err != nil {
		if !ent.IsNotFound(err) {
			return fmt.Errorf("seedSpawners: lookup default: %w", err)
		}
		if _, _, err := spawnerRepo.SetDefault(ctx, claudeDefault.ID); err != nil {
			return fmt.Errorf("seedSpawners: set claude-default as default: %w", err)
		}
		slog.Info("flagged default spawner", "slug", claudeDefaultSpawnerSlug)
	}
	return nil
}

// migrateAdapterConfigToSpawners imports legacy adapter-config.json entries into
// the spawners table. Each non-claude adapter with a non-zero config becomes a
// new Spawner row (idempotent — uses GetBySlug first). The legacy
// DASHBOARD_SPAWN_COMMAND env var, when set, is migrated to a "custom" row.
//
// Note on the legacy "default" key: the dashboard no longer has a global
// default adapter — selection is per-task / per-project via spawner_id. We
// therefore cannot mechanically migrate cfg.Adapters.Default; instead we log a
// warning prompting the operator to set the new default_spawner_id manually
// on the relevant Project or Task.
func migrateAdapterConfigToSpawners(ctx context.Context, cfg config.Config, spawnerRepo repo.SpawnerRepo) error {
	if spawnerRepo == nil {
		return nil
	}

	// Ollama
	if ollamaConfigured(cfg.Adapters.Ollama) {
		if err := ensureImportedSpawner(
			ctx, spawnerRepo,
			"Ollama (imported)", importedOllamaSlug, "ollama",
			ollamaAdapterConfig(cfg.Adapters.Ollama),
		); err != nil {
			return fmt.Errorf("migrateAdapterConfigToSpawners: ollama: %w", err)
		}
	}

	// OpenAI
	if openAIConfigured(cfg.Adapters.OpenAI) {
		if err := ensureImportedSpawner(
			ctx, spawnerRepo,
			"OpenAI (imported)", importedOpenAISlug, "openai",
			openAIAdapterConfig(cfg.Adapters.OpenAI),
		); err != nil {
			return fmt.Errorf("migrateAdapterConfigToSpawners: openai: %w", err)
		}
	}

	// DASHBOARD_SPAWN_COMMAND — migrate to a custom-adapter row. The row's
	// top-level command column carries the legacy env value; adapter_config
	// stays empty because the custom adapter has no structured config keys.
	if cmd := config.SpawnCommandFromEnv(); cmd != "" { //nolint:staticcheck // boot migration reads deprecated env once
		if err := ensureImportedCustomSpawner(ctx, spawnerRepo, cmd); err != nil {
			return fmt.Errorf("migrateAdapterConfigToSpawners: custom: %w", err)
		}
	}

	// Surface the un-migratable legacy "default" key to the operator.
	if d := cfg.Adapters.Default; d == "ollama" || d == "openai" {
		slog.Warn(
			"adapter-config.json declared a global default adapter, but the dashboard now selects spawners per Task / Project via default_spawner_id — "+
				"the imported row exists but is NOT marked as the default for any task. Set the default_spawner_id on the relevant Project or Task manually after migration.",
			"legacy_default", d,
		)
	}

	return nil
}

func ollamaConfigured(o config.OllamaConfig) bool { //nolint:staticcheck // boot migration over deprecated config shape
	return o.Host != "" || o.DefaultModel != ""
}

func openAIConfigured(o config.OpenAIConfig) bool { //nolint:staticcheck // boot migration over deprecated config shape
	return o.BaseURL != "" || o.APIKeyEnv != "" || o.DefaultModel != ""
}

func ollamaAdapterConfig(o config.OllamaConfig) map[string]string { //nolint:staticcheck // boot migration over deprecated config shape
	m := map[string]string{}
	if o.Host != "" {
		m["host"] = o.Host
	}
	if o.DefaultModel != "" {
		m["default_model"] = o.DefaultModel
	}
	return m
}

func openAIAdapterConfig(o config.OpenAIConfig) map[string]string { //nolint:staticcheck // boot migration over deprecated config shape
	m := map[string]string{}
	if o.BaseURL != "" {
		m["base_url"] = o.BaseURL
	}
	if o.APIKeyEnv != "" {
		m["api_key_env"] = o.APIKeyEnv
	}
	if o.DefaultModel != "" {
		m["default_model"] = o.DefaultModel
	}
	return m
}

// ensureImportedSpawner creates a new spawner row with the given identity unless
// one with the same slug already exists. Idempotent.
func ensureImportedSpawner(
	ctx context.Context,
	spawnerRepo repo.SpawnerRepo,
	name, slug, adapterType string,
	adapterConfig map[string]string,
) error {
	_, err := spawnerRepo.GetBySlug(ctx, slug)
	if err == nil {
		return nil
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("lookup %s: %w", slug, err)
	}
	if _, err := spawnerRepo.Create(
		ctx,
		name, slug,
		"",                  // command — adapter-typed spawners ignore the command column
		[]string{},          // args
		map[string]string{}, // env
		nil,                 // modelOverride
		nil,                 // description
		adapterType,
		adapterConfig,
		false, // builtIn
	); err != nil {
		return fmt.Errorf("create %s: %w", slug, err)
	}
	slog.Info("migrated adapter to spawner row", "slug", slug, "adapter_type", adapterType)
	return nil
}

// ensureImportedCustomSpawner migrates DASHBOARD_SPAWN_COMMAND to a Spawner row
// of adapter_type=custom. The legacy command value is stored in the row's
// command column (the custom adapter exec contract uses it directly).
func ensureImportedCustomSpawner(ctx context.Context, spawnerRepo repo.SpawnerRepo, command string) error {
	_, err := spawnerRepo.GetBySlug(ctx, importedCustomSlug)
	if err == nil {
		return nil
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("lookup %s: %w", importedCustomSlug, err)
	}
	if _, err := spawnerRepo.Create(
		ctx,
		"Custom (imported)", importedCustomSlug,
		command,
		[]string{},
		map[string]string{},
		nil, nil,
		"custom",
		map[string]string{},
		false,
	); err != nil {
		return fmt.Errorf("create %s: %w", importedCustomSlug, err)
	}
	slog.Info("migrated adapter to spawner row", "slug", importedCustomSlug, "adapter_type", "custom")
	return nil
}
