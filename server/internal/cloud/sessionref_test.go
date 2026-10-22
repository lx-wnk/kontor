package cloud

import (
	"strings"
	"testing"
)

func TestParseSessionRef(t *testing.T) {
	const sess = "session_abcdefgh"
	const cse = "cse_AbC123-_xyz"
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"bare session id", sess, sess, false},
		{"bare cse id", cse, cse, false},
		{"https url", "https://claude.ai/code/" + sess, sess, false},
		{"https url cse", "https://claude.ai/code/" + cse, cse, false},
		{"trailing slash", "https://claude.ai/code/" + sess + "/", sess, false},
		{"query string", "https://claude.ai/code/" + sess + "?x=1", sess, false},
		{"max length id", "session_" + strings.Repeat("a", 128), "session_" + strings.Repeat("a", 128), false},

		{"http scheme", "http://claude.ai/code/x", "", true},
		{"http scheme valid id", "http://claude.ai/code/" + sess, "", true},
		{"other host", "https://evil.com/code/" + sess, "", true},
		{"host suffix trick", "https://claude.ai.evil.com/code/" + sess, "", true},
		{"host prefix trick", "https://evilclaude.ai/code/" + sess, "", true},
		{"subdomain", "https://www.claude.ai/code/" + sess, "", true},
		{"explicit port", "https://claude.ai:8443/code/" + sess, "", true},
		{"userinfo", "https://user@claude.ai/code/" + sess, "", true},
		{"userinfo host trick", "https://claude.ai@evil.com/code/" + sess, "", true},
		{"fragment", "https://claude.ai/code/" + sess + "#frag", "", true},
		{"empty fragment", "https://claude.ai/code/" + sess + "#", "", true},
		{"fragment host trick", "https://evil.com#@claude.ai/code/" + sess, "", true},
		{"extra segment", "https://claude.ai/code/" + sess + "/extra", "", true},
		{"wrong first segment", "https://claude.ai/chat/" + sess, "", true},
		{"missing id", "https://claude.ai/code/", "", true},
		{"path traversal url", "https://claude.ai/code/../" + sess, "", true},
		{"encoded traversal url", "https://claude.ai/code/%2e%2e/" + sess, "", true},
		{"encoded slash", "https://claude.ai/code/session_abc%2Fdefgh", "", true},
		{"path traversal bare", "session_../..", "", true},
		{"id too short", "session_abcdefg", "", true},
		{"id too long", "session_" + strings.Repeat("a", 129), "", true},
		{"unknown prefix", "task_abcdefgh", "", true},
		{"empty", "", "", true},
		{"whitespace padded", " " + sess + " ", "", true},
		{"newline suffix", sess + "\n", "", true},
		{"flag lookalike", "--help", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSessionRef(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSessionRef(%q) = %q, want error", tc.in, got)
				}
				if got != "" {
					t.Fatalf("ParseSessionRef(%q) returned %q alongside an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSessionRef(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseSessionRef(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
