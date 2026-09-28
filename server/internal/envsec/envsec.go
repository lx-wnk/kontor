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
