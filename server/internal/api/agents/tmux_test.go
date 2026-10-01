package agents

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidTmuxPane(t *testing.T) {
	require.True(t, validTmuxPane("%0"))
	require.True(t, validTmuxPane("%42"))
	require.False(t, validTmuxPane(""))
	require.False(t, validTmuxPane("0"))
	require.False(t, validTmuxPane("%1; rm -rf /"))
	require.False(t, validTmuxPane("session:0.1"))
}

func TestTmuxSendArgs(t *testing.T) {
	text, enter := tmuxSendArgs("/tmp/tmux-501/default", "%3", "/security-review")
	require.Equal(t, []string{"-S", "/tmp/tmux-501/default", "send-keys", "-t", "%3", "-l", "--", "/security-review"}, text)
	require.Equal(t, []string{"-S", "/tmp/tmux-501/default", "send-keys", "-t", "%3", "Enter"}, enter)

	// no socket → no -S
	text2, enter2 := tmuxSendArgs("", "%1", "hi")
	require.Equal(t, []string{"send-keys", "-t", "%1", "-l", "--", "hi"}, text2)
	require.Equal(t, []string{"send-keys", "-t", "%1", "Enter"}, enter2)
}

func TestTmuxBracketedPaste_SendsLoadBufferThenPasteBuffer(t *testing.T) {
	var calls [][]string
	var stdinCalls []string
	origRun, origStdin, origLook := tmuxRunner, tmuxStdinRunner, tmuxLookPath
	t.Cleanup(func() {
		tmuxRunner = origRun
		tmuxStdinRunner = origStdin
		tmuxLookPath = origLook
	})
	tmuxLookPath = func() (string, error) { return "/usr/bin/tmux", nil }
	tmuxRunner = func(_ context.Context, args ...string) error {
		calls = append(calls, args)
		return nil
	}
	tmuxStdinRunner = func(_ context.Context, stdin io.Reader, args ...string) error {
		data, _ := io.ReadAll(stdin)
		stdinCalls = append(stdinCalls, string(data))
		calls = append(calls, args)
		return nil
	}

	err := sendKeysToTmux(context.Background(), "", "%5", "hello world")
	require.NoError(t, err)
	require.Len(t, calls, 3)

	// Call 1: load-buffer with message on stdin (NOT send-keys -l)
	require.Contains(t, calls[0], "load-buffer")
	require.Contains(t, calls[0], "-b")
	require.Contains(t, calls[0], "kontor-%5")
	require.Contains(t, calls[0], "-")
	require.Len(t, stdinCalls, 1)
	require.Equal(t, "hello world", stdinCalls[0])

	// Call 2: paste-buffer with bracketed paste (-p) and delete (-d)
	require.Contains(t, calls[1], "paste-buffer")
	require.Contains(t, calls[1], "-p")
	require.Contains(t, calls[1], "-d")
	require.Contains(t, calls[1], "-b")
	require.Contains(t, calls[1], "kontor-%5")

	// Call 3: send-keys Enter
	require.Equal(t, "Enter", calls[2][len(calls[2])-1])
}

func TestSendKeysToTmux_MultiLinePreservesNewlines(t *testing.T) {
	var stdinData string
	origRun, origStdin, origLook := tmuxRunner, tmuxStdinRunner, tmuxLookPath
	t.Cleanup(func() {
		tmuxRunner = origRun
		tmuxStdinRunner = origStdin
		tmuxLookPath = origLook
	})
	tmuxLookPath = func() (string, error) { return "/usr/bin/tmux", nil }
	tmuxRunner = func(_ context.Context, _ ...string) error { return nil }
	tmuxStdinRunner = func(_ context.Context, stdin io.Reader, _ ...string) error {
		data, _ := io.ReadAll(stdin)
		stdinData = string(data)
		return nil
	}

	err := sendKeysToTmux(context.Background(), "", "%1", "line1\nline2")
	require.NoError(t, err)
	require.Equal(t, "line1\nline2", stdinData, "newlines must be preserved in stdin")
}

func TestSendKeysToTmux_TmuxMissing(t *testing.T) {
	ran := false
	origRun, origStdin, origLook := tmuxRunner, tmuxStdinRunner, tmuxLookPath
	t.Cleanup(func() {
		tmuxRunner = origRun
		tmuxStdinRunner = origStdin
		tmuxLookPath = origLook
	})
	tmuxRunner = func(_ context.Context, _ ...string) error { ran = true; return nil }
	tmuxStdinRunner = func(_ context.Context, _ io.Reader, _ ...string) error { ran = true; return nil }
	tmuxLookPath = func() (string, error) { return "", errors.New("not found") }

	err := sendKeysToTmux(context.Background(), "", "%2", "x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "tmux is required")
	require.False(t, ran, "must not attempt send when tmux is absent")
}

func TestSendKeysToTmux_RejectsBadPane(t *testing.T) {
	called := false
	origRun, origStdin := tmuxRunner, tmuxStdinRunner
	t.Cleanup(func() { tmuxRunner = origRun; tmuxStdinRunner = origStdin })
	tmuxRunner = func(_ context.Context, _ ...string) error { called = true; return nil }
	tmuxStdinRunner = func(_ context.Context, _ io.Reader, _ ...string) error { called = true; return nil }

	err := sendKeysToTmux(context.Background(), "", "$(evil)", "x")
	require.Error(t, err)
	require.False(t, called, "must not exec tmux for an invalid pane")
}
