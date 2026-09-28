package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

//nolint:gosec // G101: a file path, not a credential — the secret lives in the file.
const hooksSecretFile = ".claude/kontor-hooks-secret"

// legacyHooksSecretFile is the pre-rename name. It is read, and kept in use
// when it exists, rather than migrated: a hook script installed before the
// rename has this path baked into it, so writing the secret somewhere else
// would leave that copy unable to authenticate.
const legacyHooksSecretFile = ".claude/dashboard-hooks-secret"

// loadOrGenerateHooksSecret returns the hooks secret from the following sources
// in precedence order:
//  1. The secret already present in cfg.HooksSecret (set via env DASHBOARD_HOOKS_SECRET).
//  2. The persisted secret file at ~/.claude/dashboard-hooks-secret.
//  3. A freshly generated 32-byte hex secret written to that file (first boot).
//
// The secret file is created with 0600 permissions so only the owning user can
// read it. On subsequent boots the file is read and the secret is loaded silently.
func loadOrGenerateHooksSecret(existing string) (string, error) {
	if existing != "" {
		return existing, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home dir for hooks secret: %w", err)
	}

	path := filepath.Join(home, hooksSecretFile)
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		legacy := filepath.Join(home, legacyHooksSecretFile)
		if _, legacyErr := os.Stat(legacy); legacyErr == nil {
			path = legacy
		}
	}

	data, err := os.ReadFile(path)
	if err == nil {
		secret := strings.TrimSpace(string(data))
		if len(secret) >= 32 {
			return secret, nil
		}
		// File exists but content is too short — treat as corrupt and regenerate.
		slog.Warn("hooks secret file has invalid content, regenerating", "path", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("config: read hooks secret file %s: %w", path, err)
	}

	// First boot: generate and persist.
	secret, err := randomHex(32)
	if err != nil {
		return "", fmt.Errorf("config: generate hooks secret: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", fmt.Errorf("config: create dir for hooks secret: %w", err)
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0600); err != nil {
		return "", fmt.Errorf("config: write hooks secret to %s: %w", path, err)
	}

	slog.Info("Generated hooks secret and persisted to file — load it via DASHBOARD_HOOKS_SECRET to use across restarts", "path", path)
	return secret, nil
}
