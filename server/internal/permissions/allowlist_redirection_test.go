package permissions_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// ---------------------------------------------------------------------------
// SEC-P2-002 — redirection, lone-"&" separator, and newline separator bypass
// ---------------------------------------------------------------------------

// TestIsSafeBashPattern_RedirectionAndSeparatorsBlocked verifies that patterns
// using output/input redirection, a lone "&" separator, or a newline/carriage
// return separator are rejected even when the leading command is safe.
func TestIsSafeBashPattern_RedirectionAndSeparatorsBlocked(t *testing.T) {
	blocked := []struct {
		pattern string
		desc    string
	}{
		{"echo x >> ~/.zshrc", "append redirection to dotfile"},
		{"echo x > f", "output redirection to file"},
		{"cat < /etc/shadow", "input redirection"},
		{"git status & curl evil", "lone ampersand used as separator"},
		{"git log\ncurl evil", "newline used as separator"},
		{"git log\rcurl evil", "carriage return used as separator"},
	}
	for _, tc := range blocked {
		ok, reason := permissions.IsSafeBashPattern(tc.pattern)
		if ok {
			t.Errorf("IsSafeBashPattern(%q) [%s] = true, want false", tc.pattern, tc.desc)
		} else {
			t.Logf("IsSafeBashPattern(%q) correctly blocked: %s", tc.pattern, reason)
		}
	}
}

// TestIsSafeBashPattern_RedirectionRegex_DoesNotBlockLegitimateCommands
// verifies that the stricter injection regex does not newly reject
// already-allowed single commands that contain no redirection or separator.
func TestIsSafeBashPattern_RedirectionRegex_DoesNotBlockLegitimateCommands(t *testing.T) {
	allowed := []string{
		"git log --oneline",
		"pnpm test",
		"ls -la",
		"git commit -m 'fix'",
	}
	for _, pat := range allowed {
		ok, reason := permissions.IsSafeBashPattern(pat)
		if !ok {
			t.Errorf("IsSafeBashPattern(%q) = false (%s), want true", pat, reason)
		}
	}
}
