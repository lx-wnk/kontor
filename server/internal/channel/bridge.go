// Package channel implements the dashboard-channel MCP server (stdio).
//
// The bridge is invoked as a subprocess by Claude Code via a temporary MCP config file
// written by the pipeline spawner. It runs alongside the spawned stage agent and provides:
//
//   - dashboard_reply: let the agent send status updates back to the dashboard.
//   - request_permission: forward bulk permission requests to the dashboard server.
//   - set_stage_output: submit this stage's structured result to the dashboard for validation.
//   - HTTP server: receive dashboard → agent messages (forwarded as MCP log notifications).
//   - Discovery file: ~/.claude/dashboard-channel/{parentPid}.json so the dashboard can
//     find the HTTP server's port and ephemeral token.
//
// Call Run to start the bridge; it blocks until the MCP session ends or the context is
// cancelled.
package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/httputil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var dashboardClient = &http.Client{Timeout: 15 * time.Second}

// Run starts the channel bridge. It blocks until the MCP stdio session ends.
func Run(ctx context.Context) error {
	dashboardURL := strings.TrimSuffix(os.Getenv("DASHBOARD_MCP_URL"), "/")
	if dashboardURL == "" {
		dashboardURL = "http://127.0.0.1:13120"
	} else if !isLoopbackURL(dashboardURL) {
		slog.Warn("channel: DASHBOARD_MCP_URL is not a loopback address — ignoring, using default",
			"url", dashboardURL)
		dashboardURL = "http://127.0.0.1:13120"
	}
	mcpToken := os.Getenv(channelconfig.EnvMCPToken)
	stageRunID := os.Getenv("DASHBOARD_STAGE_RUN_ID")
	initialToken, err := generateToken()
	if err != nil {
		return fmt.Errorf("channel: generateToken: %w", err)
	}
	token := newRotatingToken(initialToken)
	parentPid := os.Getppid()

	var sessionPtr atomic.Pointer[mcp.ServerSession]

	httpSrv, httpPort, err := startHTTPServer(dashboardURL, token, &sessionPtr)
	if err != nil {
		return fmt.Errorf("channel: HTTP server: %w", err)
	}

	discPath, err := writeDiscovery(parentPid, httpPort, token.value())
	if err != nil {
		slog.Warn("channel: discovery file write failed", "err", err)
	}

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			if discPath != "" {
				_ = os.Remove(discPath)
			}
			shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutCtx)
		})
	}
	defer cleanup()

	// Cancel context on OS signals so server.Run returns cleanly and deferred cleanup fires.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Rotate the discovery token periodically, re-emitting the 0600 discovery file
	// each time. The dashboard re-reads the file per delivery, so no coordination
	// is needed; a one-rotation grace window covers in-flight deliveries.
	go startTokenRotation(ctx, token, injectTokenRotateInterval(), func(newToken string) error {
		_, werr := writeDiscovery(parentPid, httpPort, newToken)
		return werr
	})

	server := mcp.NewServer(&mcp.Implementation{Name: channelconfig.ServerName, Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: channelInstructions,
	})

	// Capture session on every request so the HTTP handler can send notifications.
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if ss, ok := req.GetSession().(*mcp.ServerSession); ok {
				sessionPtr.Store(ss)
			}
			return next(ctx, method, req)
		}
	})

	registerTools(server, dashboardURL, mcpToken, stageRunID, parentPid)

	slog.Info("channel: MCP stdio server starting", "parentPid", parentPid, "httpPort", httpPort)
	return server.Run(ctx, &mcp.StdioTransport{})
}

func registerTools(server *mcp.Server, dashboardURL, mcpToken, stageRunID string, parentPid int) {
	// dashboard_reply tool
	type replyArgs struct {
		Message string `json:"message" jsonschema:"Reply message to display in the dashboard"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        channelconfig.ToolDashboardReply,
		Description: "Send a reply back to the monitoring dashboard. Use when you complete a dashboard instruction or want to report progress.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args replyArgs) (*mcp.CallToolResult, any, error) {
		if args.Message == "" {
			return errResult("message is required"), nil, nil
		}
		if err := postReply(dashboardURL, mcpToken, parentPid, args.Message); err != nil {
			slog.Warn("channel: postReply failed", "err", err)
			return textResult("dashboard unreachable: " + err.Error()), nil, nil
		}
		return textResult("Reply sent to dashboard."), nil, nil
	})

	// request_permission tool
	type permEntry struct {
		Tool    string  `json:"tool"`
		Pattern *string `json:"pattern,omitempty"`
		Reason  *string `json:"reason,omitempty"`
	}
	type reqPermArgs struct {
		Permissions []permEntry `json:"permissions,omitempty"`
		Tool        *string     `json:"tool,omitempty"`
		Pattern     *string     `json:"pattern,omitempty"`
		Reason      *string     `json:"reason,omitempty"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        channelconfig.ToolRequestPermission,
		Description: requestPermDesc,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reqPermArgs) (*mcp.CallToolResult, any, error) {
		if stageRunID == "" {
			return errResult("No stageRunId — task is not orchestrator-managed."), nil, nil
		}

		var entries []permEntry
		switch {
		case len(args.Permissions) > 0:
			entries = args.Permissions
		case args.Tool != nil && *args.Tool != "":
			entries = []permEntry{{Tool: *args.Tool, Pattern: args.Pattern, Reason: args.Reason}}
		default:
			return errResult("request_permission needs `permissions: [...]` or single-tool `tool`+`reason`."), nil, nil
		}

		resp, apiErr := callDashboard(dashboardURL, mcpToken, "POST", "/api/permission-requests/bulk",
			map[string]any{"stageRunId": stageRunID, "entries": entries})
		if apiErr != nil {
			return textResult("Could not reach dashboard: " + apiErr.Error()), nil, nil
		}
		return textResult(resp), nil, nil
	})

	// set_stage_output tool
	type stageOutputArgs struct {
		Output     map[string]any `json:"output"     jsonschema:"The stage result object, in the exact shape your prompt specified."`
		StageRunID *string        `json:"stageRunId,omitempty" jsonschema:"auto-injected from DASHBOARD_STAGE_RUN_ID env"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        channelconfig.ToolSetStageOutput,
		Description: setStageOutputDesc,
	}, func(ctx context.Context, req *mcp.CallToolRequest, args stageOutputArgs) (*mcp.CallToolResult, any, error) {
		sid := stageRunID
		if args.StageRunID != nil && *args.StageRunID != "" {
			sid = *args.StageRunID
		}
		if sid == "" {
			return errResult("No stageRunId — cannot submit stage output. Task is not orchestrator-managed."), nil, nil
		}
		if args.Output == nil {
			return errResult("set_stage_output requires an `output` object."), nil, nil
		}

		_, apiErr := callDashboard(dashboardURL, mcpToken, "POST", "/api/channel-stage-output",
			map[string]any{"stageRunId": sid, "output": args.Output})
		if apiErr != nil {
			return errResult("Stage output rejected: " + apiErr.Error() + ". Fix and call set_stage_output again."), nil, nil
		}
		return textResult("Stage output accepted."), nil, nil
	})
}

// ─── HTTP server ──────────────────────────────────────────────────────────────

func startHTTPServer(
	dashboardURL string,
	token *rotatingToken,
	sess *atomic.Pointer[mcp.ServerSession],
) (*http.Server, int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("listen: %w", err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return nil, 0, fmt.Errorf("unexpected listener address type")
	}
	port := tcpAddr.Port

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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

		// Forward as MCP log notification (best-effort). SEP-2577 deprecates the
		// logging feature outright as of protocol 2026-07-28 — there is no
		// replacement transport for a server-initiated message, so delivering
		// dashboard messages to a connected agent another way is a redesign, not
		// a rename. The SDK keeps this functional for at least twelve months.
		if ss := sess.Load(); ss != nil {
			msgJSON, _ := json.Marshal(payload.Message)
			notifyCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			//nolint:staticcheck // SA1019: see the SEP-2577 note above
			_ = ss.Log(notifyCtx, &mcp.LoggingMessageParams{
				Level:  "info",
				Logger: "dashboard",
				Data:   json.RawMessage(msgJSON),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})

	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", corsOrigin(r, dashboardURL))
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("channel: HTTP server error", "err", err)
		}
	}()
	return srv, port, nil
}

func corsOrigin(r *http.Request, dashboardURL string) string {
	o := r.Header.Get("Origin")
	u, err := url.Parse(o)
	if err == nil && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") {
		return o
	}
	return dashboardURL
}

func isLoopbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// ─── Discovery ────────────────────────────────────────────────────────────────

// tmuxTarget returns the current tmux pane id and server socket path, read from
// the inherited environment, or ("","") when not running inside tmux.
func tmuxTarget() (pane, socket string) {
	return parseTmuxEnv(os.Getenv("TMUX_PANE"), os.Getenv("TMUX"))
}

// parseTmuxEnv extracts the pane id and socket path. $TMUX is
// "<socket>,<server-pid>,<session>"; the socket is the first comma field.
func parseTmuxEnv(tmuxPane, tmuxEnv string) (pane, socket string) {
	pane = strings.TrimSpace(tmuxPane)
	if pane == "" {
		return "", ""
	}
	if tmuxEnv != "" {
		socket = strings.TrimSpace(strings.SplitN(tmuxEnv, ",", 2)[0])
	}
	return pane, socket
}

func writeDiscovery(parentPid, port int, token string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("UserHomeDir: %w", err)
	}
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	path := channelconfig.DiscoveryFile(home, parentPid)
	entry := map[string]any{
		"port":       port,
		"channelPid": os.Getpid(),
		"parentPid":  parentPid,
		"cwd":        cwd(),
		"token":      token,
		"startedAt":  time.Now().UTC().Format(time.RFC3339),
	}
	// When claude runs inside tmux, record the pane + socket so the dashboard can
	// deliver prompts via `tmux send-keys` (real keyboard input) instead of the
	// MCP log channel, which Claude does not act on for an interactive session.
	if pane, socket := tmuxTarget(); pane != "" {
		entry["tmuxPane"] = pane
		if socket != "" {
			entry["tmuxSocket"] = socket
		}
	}
	data, _ := json.Marshal(entry)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		// Stale file from a previous run with the same parent PID — overwrite it.
		if err2 := os.WriteFile(path, data, 0o600); err2 != nil {
			return "", fmt.Errorf("overwrite discovery: %w", err2)
		}
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("create discovery: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("write discovery: %w", err)
	}
	return path, nil
}

func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		slog.Warn("channel: os.Getwd failed", "err", err)
	}
	return d
}

// ─── Dashboard HTTP helpers ───────────────────────────────────────────────────

func callDashboard(baseURL, token, method, path string, body any) (string, error) {
	var br io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return "", fmt.Errorf("marshal: %w", err)
		}
		br = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, baseURL+path, br) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := dashboardClient.Do(req) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		slog.Warn("channel: callDashboard body read error", "err", readErr)
	}
	if !httputil.Is2xx(resp.StatusCode) {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return formatPermResponse(respBody), nil
}

type permItem struct {
	Tool    string  `json:"tool"`
	Pattern *string `json:"pattern"`
}

func formatPermResponse(body []byte) string {
	var v struct {
		AutoResolved []permItem `json:"autoResolved"`
		Pending      []permItem `json:"pending"`
		LoopWarning  *string    `json:"loopWarning"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return string(body)
	}
	fmtList := func(items []permItem) []string {
		names := make([]string, 0, len(items))
		for _, e := range items {
			s := e.Tool
			if e.Pattern != nil && *e.Pattern != "" {
				s += "(" + *e.Pattern + ")"
			}
			names = append(names, s)
		}
		return names
	}
	autoMsg := "no auto-resolves"
	if len(v.AutoResolved) > 0 {
		autoMsg = fmt.Sprintf("%d auto-resolved: %s", len(v.AutoResolved), strings.Join(fmtList(v.AutoResolved), ", "))
	}
	pendMsg := "all granted — proceed"
	if len(v.Pending) > 0 {
		pendMsg = fmt.Sprintf("%d ON HOLD awaiting user: %s", len(v.Pending), strings.Join(fmtList(v.Pending), ", "))
	}
	loopMsg := ""
	if v.LoopWarning != nil && *v.LoopWarning != "" {
		loopMsg = "\n\n⚠️ " + *v.LoopWarning
	}
	return autoMsg + ". " + pendMsg + "." + loopMsg
}

func postReply(dashboardURL, token string, parentPid int, message string) error {
	_, err := callDashboard(dashboardURL, token, "POST", "/api/channel-reply", map[string]any{
		"parentPid": parentPid,
		"message":   message,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	return err
}

// ─── Misc ─────────────────────────────────────────────────────────────────────

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

const channelInstructions = `Messages from the monitoring dashboard arrive as MCP log notifications (level: info, logger: dashboard).

## How to handle dashboard messages

1. FINISH your current action before addressing the message.
2. ACKNOWLEDGE immediately with dashboard_reply (under 100 chars).
3. ASSESS priority: course correction, hint, question, or directive.
4. REPORT progress after each significant step.
5. REPORT completion with a final dashboard_reply.

## Reply format
Use dashboard_reply for all communication. Keep replies under 200 chars, plain text, no markdown.`

const requestPermDesc = `Request one or more tool permissions. CRITICAL: every missed permission triggers a kill-and-restart — request piecemeal and you burn through restart loops.

Mandatory pattern:
  1. As your FIRST action, enumerate every tool you will need.
  2. Call ONCE with the full permissions: [...] array. Pre-granted entries auto-resolve.
  3. On [PERMISSION GRANTED] resume: re-scan remaining work and bulk-request everything else.

Single-tool form (tool + pattern) is kept for compatibility but strongly discouraged.`

const setStageOutputDesc = `Submit this stage's structured result to the dashboard. Call this as your FINAL action — the dashboard validates the object against the per-stage schema and returns an error on mismatch; you must fix the output and call again. This is the reliable replacement for emitting a ` + "```" + `json block in a message.`
