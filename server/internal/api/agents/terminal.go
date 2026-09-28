package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/coder/websocket"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// TerminalTargetFn resolves the pty-broker port and auth token for the running
// session identified by pid. It is the seam over SpawnManager.TerminalTarget so
// the handler is testable without a real spawned session. Implementations
// return ErrNoTerminal when the session has no pty terminal to attach to.
type TerminalTargetFn func(pid int) (port int, token string, err error)

// TerminalHandler proxies GET /api/agents/{pid}/terminal: it resolves the
// pty-broker target for a live-injectable session and pipes WebSocket frames
// both ways between the browser and the broker. The browser never sees the
// broker's bearer token — it is only used for the server-side broker dial.
type TerminalHandler struct {
	getAgents GetAgentsFn
	target    TerminalTargetFn
}

// NewTerminalHandler creates a TerminalHandler.
// maxTerminalFrameBytes bounds a single WebSocket frame on the pty passthrough.
// Generous vs the 256 KiB scrollback replay, finite vs an unbounded memory sink.
const maxTerminalFrameBytes = 1 << 20 // 1 MiB

func NewTerminalHandler(getAgents GetAgentsFn, target TerminalTargetFn) *TerminalHandler {
	return &TerminalHandler{getAgents: getAgents, target: target}
}

// Terminal handles GET /api/agents/{pid}/terminal. Unlike the other
// /api/agents/{pid}/* mutations, this route is NOT covered by
// RequireSameOriginForMutations (that middleware only guards non-GET
// requests) and may run with auth fully bypassed (auth.mode=none), so the
// same-origin check performed at websocket.Accept below is this handler's
// only defense against a malicious page hijacking the pty over WebSocket
// (CSWSH). Not wrapped in apierr.ErrorMiddleware: once websocket.Accept
// hijacks the connection, there is no response left for that middleware to
// write.
func (h *TerminalHandler) Terminal(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 0 {
		apierr.JSONError(w, http.StatusBadRequest, "invalid pid")
		return
	}

	agentList, err := h.getAgents(r.Context())
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, "failed to look up agent")
		return
	}
	var agent *sdk.Agent
	for i := range agentList {
		if agentList[i].PID == pid {
			agent = &agentList[i]
			break
		}
	}
	// Only spawned/injectable sessions have a broker pty to attach to.
	if agent == nil || !agent.LiveInjectable {
		http.Error(w, `{"error":"session has no terminal"}`, http.StatusConflict)
		return
	}

	port, token, err := h.target(pid)
	if err != nil {
		if errors.Is(err, ErrNoTerminal) {
			http.Error(w, `{"error":"session has no terminal"}`, http.StatusConflict)
			return
		}
		http.Error(w, `{"error":"failed to resolve terminal target"}`, http.StatusInternalServerError)
		return
	}

	// This handler enforces same-origin itself and unconditionally — it cannot
	// rely on the router's mutation-origin middleware (RequireSameOriginForMutations
	// skips GET, and this is a GET handshake) or on auth (RequireAuth is a no-op
	// when the install runs with auth.mode=none). Without an origin check here, any
	// page open in the user's browser could open a cross-origin WebSocket to this
	// route — WebSocket upgrades are not subject to CORS — and get a live pty with
	// full read/write access to the spawned agent, i.e. remote code execution.
	//
	// Passing no OriginPatterns leaves websocket.Accept's default same-origin
	// check active: a request with an Origin header is only accepted if that
	// origin's host matches r.Host; a request with no Origin header (a
	// non-browser client — browsers always set Origin on WebSocket handshakes)
	// is allowed through since there is no browser to defend against.
	browserConn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = browserConn.Close(websocket.StatusInternalError, "") }()
	// coder/websocket defaults to a 32 KiB read limit; the pty broker replays up
	// to a 256 KiB scrollback snapshot in one frame and large client pastes can
	// exceed 32 KiB too. Raise to a finite bound rather than the unbounded -1 so a
	// hostile local frame cannot exhaust memory.
	browserConn.SetReadLimit(maxTerminalFrameBytes)

	pipeCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	brokerConn, _, err := websocket.Dial(pipeCtx, fmt.Sprintf("ws://127.0.0.1:%d/ws", port), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		_ = browserConn.Close(websocket.StatusInternalError, "failed to reach terminal broker")
		return
	}
	defer func() { _ = brokerConn.Close(websocket.StatusInternalError, "") }()
	brokerConn.SetReadLimit(maxTerminalFrameBytes)

	go func() {
		defer cancel()
		pumpFrames(pipeCtx, brokerConn, browserConn)
	}()
	pumpFrames(pipeCtx, browserConn, brokerConn)
}

// pumpFrames copies WebSocket frames from src to dst, preserving message type
// (binary/text), until ctx is cancelled or either side errors. The caller is
// responsible for cancelling ctx and closing both connections on return so the
// peer pump (running in the sibling goroutine) unblocks and exits too.
func pumpFrames(ctx context.Context, src, dst *websocket.Conn) {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return
		}
		if err := dst.Write(ctx, typ, data); err != nil {
			return
		}
	}
}
