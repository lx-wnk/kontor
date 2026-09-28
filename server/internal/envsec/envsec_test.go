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
