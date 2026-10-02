package agents

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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
	buf := bufferArg(t, calls[0])
	require.True(t, strings.HasPrefix(buf, "kontor-%5-"), "buffer %q", buf)
	require.Contains(t, calls[0], "-")
	require.Len(t, stdinCalls, 1)
	require.Equal(t, "hello world", stdinCalls[0])

	// Call 2: paste-buffer with bracketed paste (-p) and delete (-d)
	require.Contains(t, calls[1], "paste-buffer")
	require.Contains(t, calls[1], "-p")
	require.Contains(t, calls[1], "-d")
	require.Equal(t, buf, bufferArg(t, calls[1]), "paste must read the buffer this call loaded")

	// Call 3: send-keys Enter
	require.Equal(t, "Enter", calls[2][len(calls[2])-1])
}

func bufferArg(t *testing.T, args []string) string {
	t.Helper()
	i := slices.Index(args, "-b")
	require.GreaterOrEqual(t, i, 0, "no -b in %v", args)
	require.Less(t, i+1, len(args), "-b without a name in %v", args)
	return args[i+1]
}

func TestSendKeysToTmux_ConcurrentSendsToOnePaneUseDistinctBuffers(t *testing.T) {
	var buffers []string
	origRun, origStdin, origLook := tmuxRunner, tmuxStdinRunner, tmuxLookPath
	t.Cleanup(func() {
		tmuxRunner = origRun
		tmuxStdinRunner = origStdin
		tmuxLookPath = origLook
	})
	tmuxLookPath = func() (string, error) { return "/usr/bin/tmux", nil }
	tmuxRunner = func(_ context.Context, _ ...string) error { return nil }
	tmuxStdinRunner = func(_ context.Context, _ io.Reader, args ...string) error {
		buffers = append(buffers, bufferArg(t, args))
		return nil
	}

	require.NoError(t, sendKeysToTmux(context.Background(), "", "%5", "first"))
	require.NoError(t, sendKeysToTmux(context.Background(), "", "%5", "second"))
	require.Len(t, buffers, 2)
	require.NotEqual(t, buffers[0], buffers[1])
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

func postMessage(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/agents/123/message", strings.NewReader(body))
	req.SetPathValue("pid", "123")
	rec := httptest.NewRecorder()
	NewSpawnHandler(nil).Message(rec, req)
	return rec
}

func TestMessage_OversizedBodyIs413(t *testing.T) {
	rec := postMessage(t, `{"message":"`+strings.Repeat("a", 64*1024)+`"}`)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{"error":"message exceeds 64 KB limit"}`, rec.Body.String())
}

func TestMessage_MalformedBodyIs400(t *testing.T) {
	rec := postMessage(t, `{"message":`)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, `{"error":"missing message"}`, rec.Body.String())
}
