package serverapp

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

// TestDispatchHeadless_GuiPathNeverHijacked asserts DispatchHeadless leaves the
// GUI startup path alone for argv shapes that name no headless subcommand,
// including the "-psn_..." argument macOS passes to a Finder-launched .app.
func TestDispatchHeadless_GuiPathNeverHijacked(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"nil args", nil},
		{"empty args", []string{}},
		{"macOS Finder launch arg", []string{"-psn_0_12345"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handled, err := DispatchHeadless(context.Background(), tc.args)
			if handled {
				t.Errorf("handled = true, want false for args %#v", tc.args)
			}
			if err != nil {
				t.Errorf("err = %v, want nil for args %#v", err, tc.args)
			}
		})
	}
}

// TestDispatchHeadless_PtyHostRecognised proves the pty-host subcommand name is
// recognised without needing to spawn a real pty child.
func TestDispatchHeadless_PtyHostRecognised(t *testing.T) {
	cases := [][]string{
		{channelconfig.SubcommandPtyHost},
		{channelconfig.SubcommandPtyHost, "--"},
	}

	for _, args := range cases {
		handled, err := DispatchHeadless(context.Background(), args)
		if !handled {
			t.Fatalf("handled = false, want true for args %#v", args)
		}
		if err == nil {
			t.Fatalf("err = nil, want a 'no command given' error for args %#v", args)
		}
		if !strings.Contains(err.Error(), "no command given") {
			t.Errorf("err = %q, want it to mention 'no command given'", err.Error())
		}
	}
}

// TestHeadlessSubcommands_WireNames pins the names as literals rather than via
// the constants, because the names are a wire contract: they are baked into
// MCP configs already written to disk and into the argv the spawner builds, so
// changing a constant's value silently breaks running sessions. Comparing the
// full set also means adding a constant without teaching DispatchHeadless
// about it fails here.
//
// It does NOT prove that every re-exec site is covered — that would need a
// source scan, not a unit test. Reviewing a new `<self> <name>` exec against
// this list stays a human step.
func TestHeadlessSubcommands_WireNames(t *testing.T) {
	got := append([]string(nil), HeadlessSubcommands()...)
	sort.Strings(got)

	want := []string{"channel", "pty-host"}
	if !slices.Equal(got, want) {
		t.Fatalf("HeadlessSubcommands() = %v, want exactly %v", got, want)
	}

	if channelconfig.SubcommandChannel != "channel" {
		t.Errorf("SubcommandChannel = %q, want %q", channelconfig.SubcommandChannel, "channel")
	}
	if channelconfig.SubcommandPtyHost != "pty-host" {
		t.Errorf("SubcommandPtyHost = %q, want %q", channelconfig.SubcommandPtyHost, "pty-host")
	}
}
