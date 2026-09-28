package permissions_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// resetExtras clears the extra allow-list after each test so state does not leak.
func resetExtras(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { permissions.SetExtraSafeBashCommands(nil) })
}

// TestExtraBash_UnknownCommandRejectedByDefault verifies baseline: unknown
// command gh* is rejected when no extras are configured.
func TestExtraBash_UnknownCommandRejectedByDefault(t *testing.T) {
	resetExtras(t)
	ok, _ := permissions.IsSafeBashPattern("gh*")
	if ok {
		t.Error("IsSafeBashPattern(\"gh*\") = true before extras set, want false")
	}
}

// TestExtraBash_AllowedAfterSet verifies that IsSafeBashPattern and
// ValidateGrantEntry accept a command added via SetExtraSafeBashCommands.
func TestExtraBash_AllowedAfterSet(t *testing.T) {
	resetExtras(t)
	permissions.SetExtraSafeBashCommands([]string{"gh"})

	ok, reason := permissions.IsSafeBashPattern("gh*")
	if !ok {
		t.Errorf("IsSafeBashPattern(\"gh*\") = false (%s) after extras set, want true", reason)
	}

	if err := permissions.ValidateGrantEntry("Bash", "gh pr create*"); err != nil {
		t.Errorf("ValidateGrantEntry(\"Bash\", \"gh pr create*\") = %v, want nil", err)
	}
}

// TestExtraBash_ExplicitBlockWins verifies that adding an explicitly-blocked
// command (curl) to extras does NOT un-block it.
func TestExtraBash_ExplicitBlockWins(t *testing.T) {
	resetExtras(t)
	permissions.SetExtraSafeBashCommands([]string{"curl"})

	ok, _ := permissions.IsSafeBashPattern("curl*")
	if ok {
		t.Error("IsSafeBashPattern(\"curl*\") = true after adding curl to extras, want false — hardcoded block must win")
	}
}

// TestExtraBash_NilResetsToBaseline verifies that SetExtraSafeBashCommands(nil)
// removes all extras and reverts to baseline behavior.
func TestExtraBash_NilResetsToBaseline(t *testing.T) {
	resetExtras(t)
	permissions.SetExtraSafeBashCommands([]string{"gh"})
	permissions.SetExtraSafeBashCommands(nil)

	ok, _ := permissions.IsSafeBashPattern("gh*")
	if ok {
		t.Error("IsSafeBashPattern(\"gh*\") = true after reset to nil, want false")
	}
}

// TestParseExtraSafeBashCommands_SpaceAndComma verifies that the parsing helper
// splits on spaces and commas and trims/lowercases entries.
func TestParseExtraSafeBashCommands_SpaceAndComma(t *testing.T) {
	got := permissions.ParseExtraSafeBashCommands("gh, du  jq")
	want := map[string]bool{"gh": true, "du": true, "jq": true}
	for _, name := range got {
		if !want[name] {
			t.Errorf("ParseExtraSafeBashCommands: unexpected entry %q", name)
		}
		delete(want, name)
	}
	for name := range want {
		t.Errorf("ParseExtraSafeBashCommands: missing expected entry %q", name)
	}
}

// TestParseExtraSafeBashCommands_Empty verifies that an empty string yields nil/empty.
func TestParseExtraSafeBashCommands_Empty(t *testing.T) {
	got := permissions.ParseExtraSafeBashCommands("")
	if len(got) != 0 {
		t.Errorf("ParseExtraSafeBashCommands(\"\") = %v, want empty", got)
	}
}
