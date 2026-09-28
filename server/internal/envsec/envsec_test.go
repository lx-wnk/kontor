package envsec_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/envsec"
)

// The rename made the server read a second prefix for every configuration
// value, including these four secrets. A deny-set that lists only the old
// prefix would let the same secret through under its new name — the rename
// would reopen a hole that was deliberately closed.
func TestDeniedSecretEnvKeysCoverBothPrefixes(t *testing.T) {
	suffixes := []string{"SECRET_KEY", "JWT_SECRET", "AUTH_PLUGIN_SECRET", "HOOKS_SECRET"}
	for _, prefix := range []string{"DASHBOARD_", "KONTOR_"} {
		for _, suffix := range suffixes {
			key := prefix + suffix
			if _, denied := envsec.DeniedSecretEnvKeys[key]; !denied {
				t.Errorf("%s is not denied — it would reach a spawned agent or a plugin", key)
			}
		}
	}
}

// A spawned agent must not inherit the vars that bind a process to the
// specific Claude Code session that launched the server — that is what made
// a spawned session resume as a child of the parent session instead of
// writing its own transcript. Configuration and auth vars have no such
// binding and must stay out of the set.
func TestInheritedSessionEnvKeys(t *testing.T) {
	sessionBound := []string{
		"CLAUDECODE",
		"CLAUDE_CODE_CHILD_SESSION",
		"CLAUDE_CODE_SESSION_ID",
		"CLAUDE_CODE_BRIDGE_SESSION_ID",
		"CLAUDE_CODE_MESSAGING_SOCKET",
		"CLAUDE_CODE_MESSAGING_TOKEN",
		"CLAUDE_CODE_SESSION_ATTENDED",
		"CLAUDE_CODE_ENTRYPOINT",
		"CLAUDE_CODE_EXECPATH",
		"CLAUDE_PID",
		"CLAUDE_EFFORT",
	}
	for _, key := range sessionBound {
		if _, ok := envsec.InheritedSessionEnvKeys[key]; !ok {
			t.Errorf("%s must be in InheritedSessionEnvKeys", key)
		}
	}

	configAndAuth := []string{
		"CLAUDE_CONFIG_DIR",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS",
	}
	for _, key := range configAndAuth {
		if _, ok := envsec.InheritedSessionEnvKeys[key]; ok {
			t.Errorf("%s is configuration/auth, not session identity — it must not be in InheritedSessionEnvKeys", key)
		}
	}
}
