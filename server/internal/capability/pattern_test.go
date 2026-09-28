package capability_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		grant   string
		request string
		want    bool
	}{
		{"empty grant is a wildcard", "", "anything at all", true},
		{"exact matches itself", "git status", "git status", true},
		{"exact does not cover a longer command", "git status", "git status --short", false},
		{"prefix covers the longer command", "git status*", "git status --short", true},
		{"prefix matches the bare prefix too", "git status*", "git status", true},
		{"prefix does not cover a different command", "git status*", "git push", false},
		{"domain matches the host", "domain:docs.example.com", "https://docs.example.com/a", true},
		{"domain matches a subdomain", "domain:example.com", "https://docs.example.com/a", true},
		{"domain rejects a suffix collision", "domain:example.com", "https://evilexample.com/a", false},
		{"domain rejects a different host", "domain:example.com", "https://other.test/a", false},
		{"domain is case-insensitive", "domain:Example.com", "https://docs.example.com/a", true},
		{"domain pattern rejects non-URL", "domain:example.com", "not a url", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capability.Match(tt.grant, tt.request); got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.grant, tt.request, got, tt.want)
			}
		})
	}
}

func TestParsePattern(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty string is valid (wildcard)", "", false},
		{"exact pattern is valid", "git status", false},
		{"prefix pattern is valid", "git status*", false},
		{"valid domain pattern", "domain:example.com", false},
		{"valid domain with subdomain", "domain:docs.example.com", false},
		{"domain with empty remainder is invalid", "domain:", true},
		{"domain with invalid hostname characters", "domain:ex@mple.com", true},
		{"domain hostname with leading dot", "domain:.example.com", true},
		{"domain hostname with trailing dot", "domain:example.com.", true},
		{"domain hostname with leading hyphen", "domain:-example.com", true},
		{"domain hostname with trailing hyphen", "domain:example.com-", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := capability.ParsePattern(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParsePattern(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
