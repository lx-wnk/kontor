package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/lx-wnk/kontor/server/internal/askq"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"golang.org/x/term"
)

// injectSubmitDelay is the pause between writing an injected prompt's text and
// its submitting carriage return. It gives Claude's debounced TUI input time to
// register the pasted text before the Enter, so the prompt submits instead of
// being left with a literal newline appended.
const injectSubmitDelay = 250 * time.Millisecond

// RunPTY launches command under a pseudo-terminal it owns, proxies the current
// terminal to it (so the user interacts normally, full TUI), and serves a
// loopback HTTP /message endpoint that injects text as real keyboard input into
// the child. This is the tmux-free path for live prompt injection: it works on
// macOS and Linux without any external multiplexer because the broker holds the
// pty master. Blocks until the child exits; returns its error.
func RunPTY(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return fmt.Errorf("ptyhost: no command given")
	}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// #nosec G204 -- command is the local operator's CLI argv (hidden `ptyhost` subcommand) or the hardcoded "claude" of `live`; no HTTP route reaches RunPTY and argv is passed without a shell.
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("ptyhost: start: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	hub := newPtyHub(256 * 1024)
	ptyOut := newPtyWriter(ptmx)

	// Keep the pty sized to the real terminal (initial + on resize).
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			_ = pty.InheritSize(os.Stdin, ptmx)
		}
	}()
	winch <- syscall.SIGWINCH
	defer signal.Stop(winch)

	// Put the real terminal in raw mode so keystrokes pass through untouched.
	if oldState, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
		defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()
	}

	// HTTP inject endpoint, keyed by the CHILD pid (the claude process the
	// scanner sees). Mirrors the bridge discovery contract so the dashboard's
	// existing /message delivery works — but writes to the pty, not an MCP log.
	childPid := cmd.Process.Pid
	initialToken, err := generateToken()
	if err != nil {
		return fmt.Errorf("ptyhost: token: %w", err)
	}
	token := newRotatingToken(initialToken)
	srv, port, err := startPtyHTTPServer(ptyOut, hub, token)
	if err != nil {
		return fmt.Errorf("ptyhost: http: %w", err)
	}
	// Foreground sessions proxy output directly to the user's terminal and are
	// interactive/owned by the user; treat them as always-recent (time.Now())
	// rather than tracking output, which the dashboard interprets as live.
	discPath, derr := writePtyDiscovery(childPid, port, token.value(), time.Now())
	if derr != nil {
		slog.Warn("ptyhost: discovery write failed", "err", derr)
	}

	// Rotate the inject token periodically, re-emitting the 0600 discovery file.
	go startTokenRotation(ctx, token, injectTokenRotateInterval(), func(newToken string) error {
		_, werr := writePtyDiscovery(childPid, port, newToken, time.Now())
		return werr
	})
	defer func() {
		if discPath != "" {
			_ = os.Remove(discPath)
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	// Proxy: terminal stdin → child, child output → terminal. stdin goes through
	// the serializing writer too, so a user typing during an injection cannot
	// land inside the injected prompt.
	go func() { _, _ = io.Copy(ptyOut, os.Stdin) }()
	_, _ = io.Copy(io.MultiWriter(os.Stdout, hub), ptmx) // returns when the child exits / pty closes

	return cmd.Wait()
}

// startPtyHTTPServer serves POST /message: the body's `message` is written to
// the pty, then — after injectSubmitDelay, as a SEPARATE write — the submitting
// carriage return, i.e. injected as if typed + Enter. The handler therefore
// blocks for that delay before responding.
func startPtyHTTPServer(ptmx *ptyWriter, hub *ptyHub, token *rotatingToken) (*http.Server, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	srv := &http.Server{Handler: ptyMux(ptmx, hub, token), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, port, nil
}

// ptyMux builds the broker's loopback HTTP handler: POST /message injects
// text into the pty, POST /keys writes raw bytes to the pty verbatim (used to
// answer an AskUserQuestion modal with an exact keystroke sequence — no
// appended CR, no sanitization), GET /health is a liveness probe, GET
// /question detects an open AskUserQuestion modal in the current scrollback,
// GET /screen does the same but also reports the modal's review/submit screen,
// and GET /ws upgrades to a WebSocket that replays scrollback then streams
// pty output to the client while pumping client input (and resize control
// messages) back into the pty. All routes except /health require the
// rotating bearer token.
func ptyMux(ptmx *ptyWriter, hub *ptyHub, token *rotatingToken) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})
	mux.HandleFunc("POST /message", func(w http.ResponseWriter, r *http.Request) {
		if !token.authorize(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Message == "" {
			http.Error(w, `{"error":"missing message"}`, http.StatusBadRequest)
			return
		}
		// Inject the text, then submit with a carriage return written SEPARATELY
		// after a short delay. Claude's TUI debounces pasted input, so a CR
		// coalesced into the same write is absorbed as a literal newline in the
		// prompt (typed-but-not-submitted) instead of triggering submit. Splitting
		// the write mirrors the tmux path, which sends the text then a separate Enter.
		// One job: no other writer can slip between the text and the CR and get
		// its bytes submitted as part of this prompt.
		if err := ptmx.WriteParts(injectSubmitDelay, []byte(payload.Message), []byte("\r")); err != nil {
			http.Error(w, `{"error":"write failed"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /keys", func(w http.ResponseWriter, r *http.Request) {
		if !token.authorize(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		// Written verbatim: no appended CR, no sanitization — the caller
		// (SendAnswerKeys) has already encoded the exact submit sequence.
		if _, err := ptmx.Write(body); err != nil {
			http.Error(w, `{"error":"write failed"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	// GET /question renders the hub's current scrollback through the screen
	// emulator and runs askq.DetectQuestion, so a client can poll for an open
	// AskUserQuestion modal without parsing raw pty bytes itself.
	mux.HandleFunc("GET /question", func(w http.ResponseWriter, r *http.Request) {
		if !token.authorize(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		q := askq.DetectQuestion(renderRows(hub.Snapshot()))
		if q == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(q)
	})

	// GET /screen supersedes GET /question: it reports whichever AskUserQuestion
	// screen is open — the modal itself OR its review/submit screen — from one
	// render. /question is kept as-is (not folded into this envelope) so a broker
	// still running from before this endpoint existed keeps working: the client
	// falls back to it on 404 rather than mis-decoding a changed payload shape.
	mux.HandleFunc("GET /screen", func(w http.ResponseWriter, r *http.Request) {
		if !token.authorize(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		screen := askq.DetectScreen(renderRows(hub.Snapshot()))
		if screen == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(screen)
	})

	// Frame-type contract (enforced by the browser client): a TEXT frame is a
	// control message — currently only {"resize":{cols,rows}} — while a BINARY
	// frame is raw pty input. Raw keystrokes must never be sent as TEXT, else a
	// literal {"resize":...} typed by the user would be swallowed as control.
	mux.HandleFunc("GET /ws", func(w http.ResponseWriter, r *http.Request) {
		if !token.authorize(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}
		defer func() { _ = c.Close(websocket.StatusInternalError, "") }()
		// Forwarded client input (e.g. a large paste) can exceed the 32 KiB
		// default read limit; this listener is loopback + token-gated. Cap at a
		// finite bound so an oversized frame cannot exhaust memory.
		c.SetReadLimit(1 << 20) // 1 MiB
		ctx := r.Context()

		replay, frames, cancel := hub.Subscribe()
		defer cancel()
		if len(replay) > 0 {
			if err := c.Write(ctx, websocket.MessageBinary, replay); err != nil {
				return
			}
		}

		go func() {
			for {
				typ, data, err := c.Read(ctx)
				if err != nil {
					return
				}
				if typ == websocket.MessageText && looksLikeResize(data) {
					applyResize(ptmx.raw, data)
					continue
				}
				if _, err := ptmx.Write(data); err != nil {
					return
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case b, ok := <-frames:
				if !ok {
					return
				}
				if err := c.Write(ctx, websocket.MessageBinary, b); err != nil {
					return
				}
			}
		}
	})

	return mux
}

// resizeMessage is the client→broker control message shape for adjusting the
// pty's terminal size, sent as a text WebSocket frame.
type resizeMessage struct {
	Resize *struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	} `json:"resize"`
}

// looksLikeResize reports whether data is a JSON resize control message
// rather than raw keystrokes to inject into the pty.
func looksLikeResize(data []byte) bool {
	var msg resizeMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return false
	}
	return msg.Resize != nil
}

// applyResize resizes the pty to the dimensions in a resize control message.
// A no-op when ptmx is not a real *os.File (e.g. a test double).
func applyResize(ptmx io.Writer, data []byte) {
	var msg resizeMessage
	if err := json.Unmarshal(data, &msg); err != nil || msg.Resize == nil {
		return
	}
	f, ok := ptmx.(*os.File)
	if !ok {
		return
	}
	_ = pty.Setsize(f, &pty.Winsize{Cols: msg.Resize.Cols, Rows: msg.Resize.Rows})
}

// writePtyDiscovery writes the pty-broker discovery file for a pty-hosted
// session. It writes to {childPid}.pty.json (not {childPid}.json) so it never
// collides with the channel bridge's {parentPid}.json file when both run for
// the same claude process. The dashboard reads BOTH files independently:
//   - {pid}.json  → channel bridge (channelAvailable, tmuxPane)
//   - {pid}.pty.json → pty broker (channelAvailable, ptyInject)
func writePtyDiscovery(childPid, port int, token string, lastOutputAt time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := channelconfig.DiscoveryPtyFile(home, childPid)
	data, _ := json.Marshal(map[string]any{
		"port":         port,
		"token":        token,
		"parentPid":    childPid,
		"cwd":          cwd(),
		"ptyInject":    true,
		"startedAt":    time.Now().UTC().Format(time.RFC3339),
		"lastOutputAt": lastOutputAt.UTC().Format(time.RFC3339),
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
