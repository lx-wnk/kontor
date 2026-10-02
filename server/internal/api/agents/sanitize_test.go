package agents

import "testing"

func TestSanitizeInjectMessage(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"tab preserved", "a\tb", "a\tb"},
		{"newline preserved", "line1\nline2", "line1\nline2"},
		{"carriage return stripped", "a\rb", "ab"},
		{"crlf becomes lf", "a\r\nb", "a\nb"},
		{"embedded enter injection neutralized", "/help\rrm -rf", "/helprm -rf"},
		{"del stripped", "a\x7fb", "ab"},
		{"c0 controls stripped", "a\x00\x01\x1bb", "ab"},
		{"unicode preserved", "café — 日本語", "café — 日本語"},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeInjectMessage(tc.in); got != tc.want {
				t.Fatalf("sanitizeInjectMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
