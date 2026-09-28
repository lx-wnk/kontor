// Package envsec holds the canonical set of server-secret environment
// variable names that must never be forwarded to a spawned Claude agent or
// plugin process. Consumed by the interactive spawner, the pipeline
// spawner, and the plugin registry so the deny-set is declared once.
package envsec

// DeniedSecretEnvKeys are pure server secrets — consumed only by
// secretbox.go (plugin master key), the JWT signer, config.go's auth
// bypass, and the hooks HMAC. No spawned agent or plugin has any
// legitimate use for them.
var DeniedSecretEnvKeys = buildDeniedSecretEnvKeys()

// secretSuffixes name the secrets themselves. The prefixes are spelled out
// here rather than imported from config to keep this package free of
// dependencies; both are listed because the server reads configuration under
// either, so denying only one would let the same secret through under the
// other name.
var (
	secretSuffixes = []string{
		"SECRET_KEY",
		"JWT_SECRET",
		"AUTH_PLUGIN_SECRET",
		"HOOKS_SECRET",
	}
	secretPrefixes = []string{"DASHBOARD_", "KONTOR_"}
)

func buildDeniedSecretEnvKeys() map[string]struct{} {
	m := make(map[string]struct{}, len(secretSuffixes)*len(secretPrefixes))
	for _, prefix := range secretPrefixes {
		for _, suffix := range secretSuffixes {
			m[prefix+suffix] = struct{}{}
		}
	}
	return m
}

// InheritedSessionEnvKeys are the vars that bind a process to the specific
// Claude Code session that launched it. A server started from inside a
// Claude Code session must not forward these to the agents it spawns from
// its own os.Environ() — doing so makes the spawned session resume as a
// child of the parent session instead of writing its own transcript.
// Configuration and auth vars (CLAUDE_CONFIG_DIR, CLAUDE_CODE_OAUTH_TOKEN,
// ...) carry no such binding and are not in this set.
var InheritedSessionEnvKeys = map[string]struct{}{
	"CLAUDECODE":                    {},
	"CLAUDE_CODE_CHILD_SESSION":     {},
	"CLAUDE_CODE_SESSION_ID":        {},
	"CLAUDE_CODE_BRIDGE_SESSION_ID": {},
	"CLAUDE_CODE_MESSAGING_SOCKET":  {},
	"CLAUDE_CODE_MESSAGING_TOKEN":   {},
	"CLAUDE_CODE_SESSION_ATTENDED":  {},
	"CLAUDE_CODE_ENTRYPOINT":        {},
	"CLAUDE_CODE_EXECPATH":          {},
	"CLAUDE_PID":                    {},
	"CLAUDE_EFFORT":                 {},
}
