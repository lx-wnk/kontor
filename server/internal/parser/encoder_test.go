package parser_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/require"
)

func TestEncodePath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"home dir", "/home/user/project", "-home-user-project"},
		{"dot claude", "/home/user/.claude", "-home-user--claude"},
		{"underscore", "/home/user/my_project", "-home-user-my-project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, parser.EncodePath(tt.input))
		})
	}
}
