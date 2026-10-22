package serverapp

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppHomeRoot(t *testing.T) {
	absHome := t.TempDir()

	cases := []struct {
		name string
		home string
		want string
	}{
		{name: "absolute home", home: absHome, want: filepath.Join(absHome, ".claude", "kontor", "apps")},
		{name: "relative home", home: "relative/home", want: ""},
		{name: "empty home", home: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.home)
			require.Equal(t, tc.want, appHomeRoot())
		})
	}
}
