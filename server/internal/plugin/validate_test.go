package plugin_test

import (
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/plugin"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
		why  string
	}{
		{"github-oauth", true, "canonical form"},
		{"my-plugin", true, "common plugin name"},
		{"abc", true, "short alphanumeric"},
		{"a1", true, "single char plus digit"},
		{"a-b-c-1", true, "multiple hyphens"},
		{"a0", true, "alphanumeric"},
		{"a", true, "single character"},
		{"a1-b2", true, "digits and hyphens"},
		{"-leading-hyphen", false, "must start alphanumeric"},
		{"Upper", false, "no uppercase"},
		{"My-Plugin", false, "no uppercase in middle"},
		{"UPPER", false, "all uppercase"},
		{"../traversal", false, "path traversal attempt"},
		{"-bad", false, "leading hyphen"},
		{"has space", false, "no spaces"},
		{"has_underscore", false, "no underscores"},
		{"", false, "empty"},
		// Behaviour change: pluginIDRe had no length bound, the canonical slug
		// rule caps at 64 characters. A 65-character id was accepted before.
		{strings.Repeat("a", 64), true, "at the cap"},
		{strings.Repeat("a", 65), false, "over the cap — previously accepted"},
	}
	for _, tt := range tests {
		if got := plugin.ValidID(tt.id); got != tt.want {
			t.Errorf("ValidID(%q) = %v, want %v (%s)", tt.id, got, tt.want, tt.why)
		}
	}
}
