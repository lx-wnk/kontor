package taskcontrol_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
)

func TestIsAllowAll(t *testing.T) {
	cases := []struct {
		autonomy string
		want     bool
	}{
		{"manual", false},
		{"spec_gated", true},
		{"full", true},
		{"", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		got := taskcontrol.IsAllowAll(tc.autonomy)
		if got != tc.want {
			t.Errorf("IsAllowAll(%q) = %v, want %v", tc.autonomy, got, tc.want)
		}
	}
}

func TestPermissiveAllowList(t *testing.T) {
	list := taskcontrol.PermissiveAllowList(false)
	has := func(tool string) bool {
		for _, v := range list {
			if v == tool {
				return true
			}
		}
		return false
	}
	if !has("Bash") {
		t.Error("PermissiveAllowList must contain Bash")
	}
	if !has("Read") {
		t.Error("PermissiveAllowList must contain Read")
	}
}

func TestValidAutonomyValues(t *testing.T) {
	for _, v := range []string{"manual", "spec_gated", "full"} {
		if _, ok := taskcontrol.ValidAutonomyValues[v]; !ok {
			t.Errorf("ValidAutonomyValues must contain %q", v)
		}
	}
	if _, ok := taskcontrol.ValidAutonomyValues[""]; ok {
		t.Error("ValidAutonomyValues must not contain empty string")
	}
}
