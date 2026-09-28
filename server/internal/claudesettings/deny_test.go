package claudesettings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/claudesettings"
)

func TestMatchesTheDocumentedRuleShapes(t *testing.T) {
	cases := []struct {
		rule, tool, arg string
		want            bool
	}{
		{"Bash", "Bash", "ls", true},
		{"Bash", "Write", "ls", false},
		{"Bash(rm:*)", "Bash", "rm -rf /", true},
		{"Bash(rm:*)", "Bash", "ls -la", false},
		{"Read(./.env)", "Read", "./.env", true},
		{"Read(./.env)", "Read", "./.envrc", false},
		{"WebFetch(domain:evil.example)", "WebFetch", "https://evil.example/x", true},
		{"WebFetch(domain:evil.example)", "WebFetch", "https://ok.example/x", false},
		// Case is Claude Code's own tool-name spelling; be forgiving on it.
		{"bash(rm:*)", "Bash", "rm x", true},
	}
	for _, c := range cases {
		rules := claudesettings.ParseDenyRules([]string{c.rule})
		if got := rules[0].Matches(c.tool, c.arg); got != c.want {
			t.Errorf("%q.Matches(%q, %q) = %v, want %v", c.rule, c.tool, c.arg, got, c.want)
		}
	}
}

// A shape this matcher does not implement must suppress the Allow button rather
// than be read as "no rule applies". The matcher never grants; it only declines
// to offer, so erring towards a match costs a terminal prompt and nothing else.
func TestAnUnparsedShapeStillMatchesItsTool(t *testing.T) {
	rules := claudesettings.ParseDenyRules([]string{"Read(./secrets/**)"})
	if !rules[0].Matches("Read", "./config/app.yaml") {
		t.Fatal("a glob rule was read as inapplicable; the dashboard would offer Allow under an unparsed restriction")
	}
	if rules[0].Matches("Bash", "./secrets/x") {
		t.Fatal("the rule leaked to another tool")
	}
}

func TestFirstMatchNamesTheRule(t *testing.T) {
	rules := claudesettings.ParseDenyRules([]string{"Write(/etc/*)", "Bash(rm:*)"})
	got := claudesettings.FirstMatch(rules, "Bash", "rm -rf /")
	if got == nil {
		t.Fatal("no match")
	}
	if got.Raw != "Bash(rm:*)" {
		t.Fatalf("Raw = %q, want the rule text verbatim so the card can name it", got.Raw)
	}
	if claudesettings.FirstMatch(rules, "Bash", "ls") != nil {
		t.Fatal("matched a call no rule covers")
	}
}

func TestParseDenyRulesSkipsBlanks(t *testing.T) {
	if got := claudesettings.ParseDenyRules([]string{"", "  ", "Bash"}); len(got) != 1 {
		t.Fatalf("got %d rules, want 1", len(got))
	}
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReaderUnionsUserAndProjectRules(t *testing.T) {
	cfg, project := t.TempDir(), t.TempDir()
	writeSettings(t, filepath.Join(cfg, "settings.json"), `{"permissions":{"deny":["Read(./.env)"]}}`)
	writeSettings(t, filepath.Join(project, ".claude", "settings.json"), `{"permissions":{"deny":["Bash(rm:*)"]}}`)
	writeSettings(t, filepath.Join(project, ".claude", "settings.local.json"), `{"permissions":{"deny":["Write"]}}`)

	rules := claudesettings.NewReader(cfg).DenyRules(project)

	var raw []string
	for _, r := range rules {
		raw = append(raw, r.Raw)
	}
	want := []string{"Read(./.env)", "Bash(rm:*)", "Write"}
	if len(raw) != len(want) {
		t.Fatalf("rules = %v, want %v — a deny is never relaxed by a narrower scope", raw, want)
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Fatalf("rules = %v, want %v", raw, want)
		}
	}
}

// The bridge asks on every held call. A file that has not changed must not be
// re-parsed, and one that HAS changed must not be served from the cache — a
// rule the user deleted has to stop applying.
func TestReaderRefreshesWhenTheFileChanges(t *testing.T) {
	cfg := t.TempDir()
	path := filepath.Join(cfg, "settings.json")
	writeSettings(t, path, `{"permissions":{"deny":["Bash(rm:*)"]}}`)
	r := claudesettings.NewReader(cfg)

	if len(r.DenyRules("")) != 1 {
		t.Fatal("first read found no rule")
	}
	writeSettings(t, path, `{"permissions":{"deny":[]}}`)
	// Size differs, so the change is visible even where mtime has 1s precision.
	if got := r.DenyRules(""); len(got) != 0 {
		t.Fatalf("still serving %v after the rule was removed", got)
	}
}

func TestReaderIsQuietOnMissingAndBrokenFiles(t *testing.T) {
	cfg := t.TempDir()
	r := claudesettings.NewReader(cfg, filepath.Join(cfg, "nope"))
	if got := r.DenyRules(""); got != nil {
		t.Fatalf("got %v for an absent settings file, want none", got)
	}

	writeSettings(t, filepath.Join(cfg, "settings.json"), `{not json`)
	if got := r.DenyRules(""); got != nil {
		t.Fatalf("got %v for unparsable settings, want none — refusing to parse is not a reason to start blocking", got)
	}
}

// A relative cwd cannot be joined into a trustworthy path, and the value comes
// from a hook payload. It must contribute nothing rather than resolve against
// the server's own working directory.
func TestReaderIgnoresARelativeCwd(t *testing.T) {
	cfg := t.TempDir()
	writeSettings(t, filepath.Join(cfg, "settings.json"), `{"permissions":{"deny":["Bash"]}}`)
	if got := claudesettings.NewReader(cfg).DenyRules("../.."); len(got) != 1 {
		t.Fatalf("got %d rules, want only the user-level one", len(got))
	}
}

func TestNilReaderYieldsNoRules(t *testing.T) {
	var r *claudesettings.Reader
	if got := r.DenyRules("/tmp"); got != nil {
		t.Fatalf("got %v from a nil reader", got)
	}
}
