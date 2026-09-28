package merger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/askq"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

// questionProbeTimeout bounds the loopback HTTP round-trip so a slow or wedged
// pty broker never stalls a scan tick.
const questionProbeTimeout = 250 * time.Millisecond

// captureTmuxPane is a seam so tests can supply rendered pane rows without a
// real tmux. It returns the visible pane content (already rendered by tmux, so
// no VT emulation is needed) split into rows. The exec is bounded by
// questionProbeTimeout so a wedged tmux never stalls the scan hot path.
var captureTmuxPane = func(socket, pane string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), questionProbeTimeout)
	defer cancel()
	args := []string{}
	if socket != "" {
		args = append(args, "-S", socket)
	}
	args = append(args, "capture-pane", "-p", "-t", pane)
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return nil, err
	}
	return strings.Split(string(out), "\n"), nil
}

// RealScreenProbe is the production ScreenProbeFn. It detects whichever
// AskUserQuestion screen — the modal itself or its review/submit screen — is
// currently open for pid, over whichever injection path the session uses: the
// pty broker's HTTP endpoints, or, for tmux sessions, a `tmux capture-pane`
// snapshot run through the same detectors. Fail-soft by design: any missing
// file, unreachable broker, or decode error yields nil rather than an error,
// since this runs on the hot scan path and must never fail agent building.
func RealScreenProbe(pid int) *sdk.PendingScreen {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	// Match SendAnswerKeys' transport precedence (tmux before pty) so, in the
	// rare case a session exposes both discovery files, detection and delivery
	// target the same channel.
	if s := probeTmuxScreen(home, pid); s != nil {
		return s
	}
	return probePtyScreen(home, pid)
}

// probePtyScreen queries the pty broker for pid's open screen, or nil.
//
// It prefers GET /screen (modal + review/submit screen) and falls back to the
// older GET /question (modal only) when the broker 404s it — a broker process
// spawned before /screen existed outlives a server upgrade, and losing modal
// detection for those sessions would be a regression.
func probePtyScreen(home string, pid int) *sdk.PendingScreen {
	data, err := os.ReadFile(channelconfig.DiscoveryPtyFile(home, pid))
	if err != nil {
		return nil
	}
	var disc struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &disc) != nil || disc.Port == 0 || disc.Token == "" {
		return nil
	}

	var screen sdk.PendingScreen
	status, ok := getBrokerJSON(disc.Port, disc.Token, "/screen", &screen)
	if ok {
		return &screen
	}
	if status != http.StatusNotFound {
		return nil
	}

	var q sdk.DetectedQuestion
	if _, ok := getBrokerJSON(disc.Port, disc.Token, "/question", &q); !ok {
		return nil
	}
	return &sdk.PendingScreen{Question: &q}
}

// getBrokerJSON performs one authorized loopback GET against the broker and
// decodes a 200 body into out. It reports the HTTP status (0 when the request
// never completed) and whether out was populated — 204 (no screen open) is a
// successful request but not a populated result.
func getBrokerJSON(port int, token, path string, out any) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), questionProbeTimeout)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, false
	}
	if json.NewDecoder(resp.Body).Decode(out) != nil {
		return resp.StatusCode, false
	}
	return resp.StatusCode, true
}

// probeTmuxScreen detects an open screen from a tmux session's rendered pane,
// or nil. tmux already renders the pane, so the rows go straight to the
// detectors without VT emulation — and one capture serves both.
func probeTmuxScreen(home string, pid int) *sdk.PendingScreen {
	data, err := os.ReadFile(channelconfig.DiscoveryFile(home, pid))
	if err != nil {
		return nil
	}
	var disc struct {
		TmuxPane   string `json:"tmuxPane"`
		TmuxSocket string `json:"tmuxSocket"`
	}
	if json.Unmarshal(data, &disc) != nil || disc.TmuxPane == "" {
		return nil
	}
	rows, err := captureTmuxPane(disc.TmuxSocket, disc.TmuxPane)
	if err != nil {
		return nil
	}
	return askq.DetectScreen(rows)
}
