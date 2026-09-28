// Package config loads and validates server configuration.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/lx-wnk/kontor/server/internal/worktree"
)

// Config holds bootstrap and secret configuration. Operational config now lives
// in the DB-backed settings registry; these keys are the only ones still read
// from the environment. Keys match environment variable names after stripping
// the DASHBOARD_ prefix and lowercasing.
type Config struct {
	Host      string `koanf:"host"`
	Port      int    `koanf:"port"`
	JWTSecret string `koanf:"jwt_secret"`
	DBPath    string `koanf:"db_path"`
	PluginDir string `koanf:"plugin_dir"`
	// ProviderDir is an optional directory of user provider descriptors merged
	// over the built-ins. Set via DASHBOARD_PROVIDER_DIR.
	ProviderDir  string `koanf:"provider_dir"`
	WorktreeRoot string `koanf:"worktree_root"`
	HooksSecret  string `koanf:"hooks_secret"`
	MCPToken     string `koanf:"mcp_token"`
	// RemotesEnabled allows binding to a non-loopback address. Set via DASHBOARD_REMOTES_ENABLED=true.
	// Must be explicitly opted in because the dashboard exposes sensitive Claude session data.
	RemotesEnabled bool `koanf:"remotes_enabled"`
	// AuthPluginSecret is the shared secret between core and auth plugins.
	// Set via DASHBOARD_AUTH_PLUGIN_SECRET. When set, enables POST /api/auth/session
	// so an external auth plugin can establish sessions after completing OAuth.
	AuthPluginSecret string        `koanf:"auth_plugin_secret"`
	Adapters         AdapterConfig `koanf:"adapters"`
	// RestartMode controls how POST /api/admin/restart relaunches the server:
	// "reexec" (default) replaces the process image in place (no supervisor needed);
	// "exit" exits 0 so an external supervisor (systemd/launchd/wrapper) restarts it.
	RestartMode string `koanf:"restart_mode"`
}

// Defaults returns a Config populated with safe defaults.
func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Host:         "127.0.0.1",
		Port:         13120,
		DBPath:       keepExisting(filepath.Join(home, ".claude", "kontor-tasks.db"), filepath.Join(home, ".claude", "dashboard-tasks.db")),
		WorktreeRoot: keepExisting(filepath.Join(home, worktree.DefaultRootDirName), filepath.Join(home, worktree.RenamedRootDirName)),
		RestartMode:  "reexec",
	}
}

// keepExisting returns preferred unless it is absent and legacy is present, in
// which case the installation keeps using what it already has. Nothing is
// copied or moved: a database can be large and a worktree root holds checkouts
// that running agents are working in, so relocating either behind the
// operator's back would be the more dangerous of the two options.
func keepExisting(preferred, legacy string) string {
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}
	if _, err := os.Stat(legacy); err == nil {
		slog.Info("using the pre-rename path because it exists", "path", legacy, "new", preferred)
		return legacy
	}
	return preferred
}

// EnvPrefix is the prefix every configuration variable carries. legacyEnvPrefix
// is the name it had before the project was renamed; it is still read so an
// installation whose shell profile or .env file predates the rename keeps its
// configuration.
const (
	EnvPrefix       = "KONTOR_"
	legacyEnvPrefix = "DASHBOARD_"
)

// warnIfOnlyLegacyEnvPrefix says once, at boot, that the configuration came in
// under the old prefix — the only signal an operator gets that a future release
// dropping it will change their setup.
func warnIfOnlyLegacyEnvPrefix() {
	legacy, current := 0, 0
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, EnvPrefix):
			current++
		case strings.HasPrefix(kv, legacyEnvPrefix):
			legacy++
		}
	}
	if legacy > 0 && current == 0 {
		slog.Warn("configuration read from the old environment prefix",
			"old", legacyEnvPrefix, "new", EnvPrefix, "variables", legacy)
	}
}

// Load returns a Config merged from defaults → optional JSON file → env vars.
// Env vars carry EnvPrefix and are case-insensitive; legacyEnvPrefix is still
// accepted, see the loop below.
func Load(cfgFile string) (Config, error) {
	// Load a .env file from the working directory into the process environment
	// so both `task dev` (air) and `./bin/agent-dashboard serve` pick it up — the
	// .env uses the same DASHBOARD_ keys, so the env provider below reads them
	// through the identical prefix transform. Non-overriding: an explicit shell
	// export always wins over the file. Absence is fine — the file is optional.
	if err := godotenv.Load(); err == nil {
		slog.Info("loaded configuration from .env file")
	} else if !os.IsNotExist(err) {
		slog.Warn("failed to parse .env file — its values were ignored", "err", err)
	}

	k := koanf.New(".")
	cfg := Defaults()

	// Load defaults as base
	defaults := map[string]any{
		"host":          cfg.Host,
		"port":          cfg.Port,
		"db_path":       cfg.DBPath,
		"worktree_root": cfg.WorktreeRoot,
	}
	if err := k.Load(confmap.Provider(defaults, "."), nil); err != nil {
		return Config{}, fmt.Errorf("config defaults: %w", err)
	}

	// Optional file override
	if cfgFile != "" {
		if err := k.Load(file.Provider(cfgFile), json.Parser()); err != nil {
			return Config{}, fmt.Errorf("config file %s: %w", cfgFile, err)
		}
	}

	// Env vars: KONTOR_HOST → host, KONTOR_JWT_SECRET → jwt_secret.
	//
	// The old prefix is loaded first and the new one after it, so a value set
	// under both wins under the new name. Reading the old prefix is not a
	// courtesy: it lives in shell profiles and .env files this process cannot
	// see, and dropping it would change an existing installation's configuration
	// without anyone touching it.
	for _, prefix := range []string{legacyEnvPrefix, EnvPrefix} {
		if err := k.Load(env.Provider(prefix, ".", func(s string) string {
			return strings.ToLower(strings.TrimPrefix(s, prefix))
		}), nil); err != nil {
			return Config{}, fmt.Errorf("config env %s: %w", prefix, err)
		}
	}
	warnIfOnlyLegacyEnvPrefix()

	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("config unmarshal: %w", err)
	}

	warnOnMovedEnvKeys()

	if cfg.RestartMode != "reexec" && cfg.RestartMode != "exit" {
		if cfg.RestartMode != "" {
			slog.Warn("invalid DASHBOARD_RESTART_MODE — falling back to reexec", "value", cfg.RestartMode)
		}
		cfg.RestartMode = "reexec"
	}

	// Reject operator-set JWT secrets that are too short (< 32 chars).
	// The auto-generated secret is always 64 hex chars so this only fires for short manually-set values.
	if cfg.JWTSecret != "" && len(cfg.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("config: DASHBOARD_JWT_SECRET must be at least 32 characters, got %d", len(cfg.JWTSecret))
	}

	// Reject auth plugin secrets that are too short — a short shared secret offers trivial brute-force surface.
	if cfg.AuthPluginSecret != "" && len(cfg.AuthPluginSecret) < 32 {
		return Config{}, fmt.Errorf("config: DASHBOARD_AUTH_PLUGIN_SECRET must be at least 32 characters, got %d", len(cfg.AuthPluginSecret))
	}

	if cfg.JWTSecret == "" {
		secret, err := randomHex(32)
		if err != nil {
			return Config{}, fmt.Errorf("config: generate jwt secret: %w", err)
		}
		cfg.JWTSecret = secret
		slog.Warn("DASHBOARD_JWT_SECRET not set — generated ephemeral secret; sessions will invalidate on restart")
	}

	// Refuse boot when binding to a non-loopback address unless the operator has
	// explicitly opted in via DASHBOARD_REMOTES_ENABLED=true. The dashboard reads
	// sensitive Claude session data; accidental public exposure is a high-impact mistake.
	loopback := map[string]bool{"127.0.0.1": true, "::1": true, "localhost": true}
	if !loopback[cfg.Host] {
		if !cfg.RemotesEnabled {
			return Config{}, fmt.Errorf(
				"config: DASHBOARD_HOST=%q is a non-loopback address and would expose sensitive Claude session data to the network. "+
					"Set DASHBOARD_REMOTES_ENABLED=true to confirm this is intentional (use a VPN or SSH tunnel), "+
					"or set DASHBOARD_HOST=127.0.0.1 to bind to loopback only",
				cfg.Host,
			)
		}
		slog.Warn("DASHBOARD_HOST is non-loopback — server will expose sensitive Claude session data to the network. Use VPN/SSH tunnel only.", "host", cfg.Host)
	}

	// Auto-generate and persist hooks secret when DASHBOARD_HOOKS_SECRET is not set.
	// This ensures /api/hooks/event is always protected, even on first boot.
	hooksSecret, err := loadOrGenerateHooksSecret(cfg.HooksSecret)
	if err != nil {
		return Config{}, err
	}
	cfg.HooksSecret = hooksSecret

	return cfg, nil
}

// IsLoopback reports whether the configured host is a loopback address.
func (c Config) IsLoopback() bool {
	return c.Host == "127.0.0.1" || c.Host == "::1" || c.Host == "localhost"
}

// Addr returns the bind address string.
func (c Config) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// CallbackURL returns the OAuth redirect URI derived from Host and Port.
// Uses https for non-loopback hosts.
func (c Config) CallbackURL() string {
	scheme := "http"
	if !c.IsLoopback() {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/api/auth/callback", scheme, c.Addr())
}

// warnOnMovedEnvKeys logs a warning for any DASHBOARD_ env var whose value moved
// to the DB-backed settings registry and is therefore no longer read here.
func warnOnMovedEnvKeys() {
	movedKeys := []string{
		"DASHBOARD_AUTH", "DASHBOARD_PROVIDERS_ENABLED", "DASHBOARD_ALLOW_GIT_PUSH",
		"DASHBOARD_ALLOW_GIT_PULL", "DASHBOARD_SPAWNER_ALLOWED_COMMANDS",
		"DASHBOARD_FORCE_WORKTREES", "DASHBOARD_SSE_INTERVAL_MS", "DASHBOARD_SHUTDOWN_TIMEOUT_SECONDS",
		"DASHBOARD_HOOKS_DEBOUNCE_MS", "DASHBOARD_HOOK_EVENTS_PER_SESSION",
		"DASHBOARD_SPAWN_RATE_LIMIT", "DASHBOARD_SPAWN_RATE_WINDOW_MS",
		"DASHBOARD_INJECT_RATE_LIMIT", "DASHBOARD_INJECT_RATE_WINDOW_MS",
		"DASHBOARD_COST_SCAN_INTERVAL_MS", "DASHBOARD_EVAL_SCAN_INTERVAL_MS",
		"DASHBOARD_EVAL_WINDOW_HOURS", "DASHBOARD_EVAL_MIN_SAMPLES",
		"DASHBOARD_EVAL_RATE_DROP_PP", "DASHBOARD_EVAL_STDDEV_K",
	}
	for _, key := range movedKeys {
		if _, ok := os.LookupEnv(key); ok {
			slog.Warn("config: env var is no longer read — manage it via the Settings UI or 'agent-dashboard settings set'", "key", key)
		}
	}
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
