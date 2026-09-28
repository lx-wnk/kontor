package merger

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

// writeProbeDiscovery writes a pty discovery file for pid pointing at addr
// (an httptest server's listener address, e.g. "127.0.0.1:54321") with token.
func writeProbeDiscovery(t *testing.T, home string, pid int, port int, token string) {
	t.Helper()
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	data, err := json.Marshal(map[string]any{"port": port, "token": token, "ptyInject": true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(channelconfig.DiscoveryPtyFile(home, pid), data, 0o600))
}

// writeTmuxDiscovery writes a tmux discovery file for pid.
func writeTmuxDiscovery(t *testing.T, home string, pid int, pane, socket string) {
	t.Helper()
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	data, err := json.Marshal(map[string]any{"tmuxPane": pane, "tmuxSocket": socket})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(channelconfig.DiscoveryFile(home, pid), data, 0o600))
}

// stubTmuxCapture swaps the capture seam for the duration of the test.
func stubTmuxCapture(t *testing.T, rows []string) *struct{ socket, pane string } {
	t.Helper()
	got := &struct{ socket, pane string }{}
	orig := captureTmuxPane
	captureTmuxPane = func(socket, pane string) ([]string, error) {
		got.socket, got.pane = socket, pane
		return rows, nil
	}
	t.Cleanup(func() { captureTmuxPane = orig })
	return got
}

func TestRealScreenProbe_ScreenEndpointDecodesQuestion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := sdk.DetectedQuestion{
		Header:   "Pick a color",
		Question: "Pick a color?",
		Options: []sdk.DetectedOption{
			{Index: 1, Label: "Red"},
			{Index: 2, Label: "Green"},
		},
		TypeSomethingIndex: 3,
		ChatAboutIndex:     4,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/screen", r.URL.Path)
		assert.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sdk.PendingScreen{Question: &want})
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeProbeDiscovery(t, home, 4242, port, "secret-token")

	got := RealScreenProbe(4242)
	require.NotNil(t, got)
	require.NotNil(t, got.Question)
	assert.Nil(t, got.Confirm)
	assert.Equal(t, want, *got.Question)
}

func TestRealScreenProbe_ScreenEndpointDecodesConfirm(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := sdk.DetectedConfirm{
		Question: "Ready to submit your answers?",
		Options: []sdk.DetectedOption{
			{Index: 1, Label: "Submit answers"},
			{Index: 2, Label: "Cancel"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sdk.PendingScreen{Confirm: &want})
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeProbeDiscovery(t, home, 4245, port, "secret-token")

	got := RealScreenProbe(4245)
	require.NotNil(t, got)
	require.NotNil(t, got.Confirm)
	assert.Nil(t, got.Question)
	assert.Equal(t, want, *got.Confirm)
}

// A broker spawned before GET /screen existed 404s it; the probe must fall back
// to GET /question rather than lose modal detection for that session.
func TestRealScreenProbe_FallsBackToQuestionWhenScreenUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := sdk.DetectedQuestion{
		Header:             "Pick a color",
		Question:           "Pick a color?",
		Options:            []sdk.DetectedOption{{Index: 1, Label: "Red"}},
		TypeSomethingIndex: 2,
		ChatAboutIndex:     3,
	}

	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/screen" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeProbeDiscovery(t, home, 4246, port, "secret-token")

	got := RealScreenProbe(4246)
	require.NotNil(t, got)
	require.NotNil(t, got.Question)
	assert.Equal(t, want, *got.Question)
	assert.Equal(t, []string{"/screen", "/question"}, paths)
}

func TestRealScreenProbe_204ReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeProbeDiscovery(t, home, 4343, port, "secret-token")

	assert.Nil(t, RealScreenProbe(4343))
	// 204 means "no screen open" — an answer, not an unknown route, so the
	// legacy fallback must NOT fire.
	assert.Equal(t, 1, calls)
}

func TestRealScreenProbe_401ReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	writeProbeDiscovery(t, home, 4444, port, "wrong-token")

	assert.Nil(t, RealScreenProbe(4444))
}

func TestRealScreenProbe_NoDiscoveryFileReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	assert.Nil(t, RealScreenProbe(9999))
}

func TestRealScreenProbe_TmuxCapturePaneDetectsQuestion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pid := 55201
	writeTmuxDiscovery(t, home, pid, "%3", "/tmp/sock")
	captured := stubTmuxCapture(t, []string{
		"Pick a colour",
		"What is your favourite colour?",
		"1. Red",
		"2. Green",
		"3. Blue",
		"4. Type something",
		"5. Chat about this",
	})

	screen := RealScreenProbe(pid)
	require.NotNil(t, screen)
	q := screen.Question
	require.NotNil(t, q)
	assert.False(t, q.MultiSelect)
	assert.Equal(t, "/tmp/sock", captured.socket)
	assert.Equal(t, "%3", captured.pane)
	labels := []string{q.Options[0].Label, q.Options[1].Label, q.Options[2].Label}
	assert.Equal(t, []string{"Red", "Green", "Blue"}, labels)
	assert.Equal(t, 4, q.TypeSomethingIndex)
	assert.Equal(t, 5, q.ChatAboutIndex)
}

func TestRealScreenProbe_TmuxCapturePaneDetectsConfirmScreen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pid := 55203
	writeTmuxDiscovery(t, home, pid, "%4", "/tmp/sock")
	stubTmuxCapture(t, []string{
		"Review your answers",
		"● How should the plan run?",
		"  → Subagent per task",
		"Ready to submit your answers?",
		"❯ 1. Submit answers",
		"  2. Cancel",
	})

	screen := RealScreenProbe(pid)
	require.NotNil(t, screen)
	require.NotNil(t, screen.Confirm)
	assert.Nil(t, screen.Question)
	assert.Equal(t, "Ready to submit your answers?", screen.Confirm.Question)
	assert.Equal(t, "Submit answers", screen.Confirm.Options[0].Label)
}

func TestRealScreenProbe_NoTmuxPaneReturnsNil(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	pid := 55202
	writeTmuxDiscovery(t, home, pid, "", "")

	assert.Nil(t, RealScreenProbe(pid))
}
