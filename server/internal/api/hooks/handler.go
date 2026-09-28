// Package hooks implements the /api/hooks route group.
//
// Routes:
//   - POST /api/hooks/event   — receives Claude Code hook events, triggers debounced SSE rescan
//   - POST /api/hooks/pre-tool — edit gate: holds a tool call pending user approval
//   - POST /api/hooks/respond  — approve or reject a pending edit gate decision
//   - GET  /api/hooks/pending  — list pending edit gate decisions
//   - POST /api/hooks/permission — permission bridge: holds a PreToolUse call
//     open so the prompt can be answered here instead of in the terminal
//   - POST /api/hooks/permission/respond — allow or deny a held call
//   - POST /api/hooks/notification — records that a terminal prompt is up
package hooks

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/hookstore"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

const (
	editGateTimeout = 30 * time.Second
	// maxSummaryBytes bounds the secret-safe payload preview stored per event.
	// Raw tool_input / tool_response are never stored in full.
	maxSummaryBytes = 512
	// maxEventBodyBytes caps how many bytes we read from the hook event body
	// before JSON decoding — only the first field prefix is ever retained.
	maxEventBodyBytes = 64 * 1024
)

// OnEventFn is called when an authenticated hook event is received.
type OnEventFn func()

// PendingEdit holds an in-flight edit gate decision.
type PendingEdit struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionId"`
	ToolName   string `json:"toolName"`
	FilePath   string `json:"filePath"`
	OldContent string `json:"oldContent"`
	NewContent string `json:"newContent"`
	CreatedAt  int64  `json:"createdAt"` // unix ms
	Decision   string `json:"decision"`  // "pending" | "accept" | "reject"
}

// Handler handles /api/hooks requests.
type Handler struct {
	secret  string
	onEvent OnEventFn
	// store records per-event hook granularity for the merger to read. May be
	// nil — Event then skips recording and behaves exactly as before.
	store *hookstore.Store

	mu      sync.Mutex
	pending map[string]*pendingEntry

	// permissions holds PreToolUse calls open for a dashboard decision. Separate
	// from the edit gate above: that one gates write tools with its own payload
	// shape, this one speaks Claude Code's native hook protocol for every tool.
	permissions *HookEnforcer
	// sessionCWD resolves a session id against the live scan. Installed by the
	// router, which owns the agent accessor. Nil disables the check, which is
	// what a handler built without one gets.
	sessionCWD SessionCWDFn
}

// SessionCWDFn returns the working directory the scanner reports for a session,
// and whether that session is running at all.
type SessionCWDFn func(ctx context.Context, sessionID string) (string, bool)

// SetSessionCWD installs the live-session lookup used to vouch for a session at
// arming time.
func (h *Handler) SetSessionCWD(fn SessionCWDFn) { h.sessionCWD = fn }

type pendingEntry struct {
	edit PendingEdit
	ch   chan string // receives "accept" or "reject"
}

// New creates a Handler with the given shared secret and rescan callback.
// The secret is required on every hook endpoint — missing or wrong bearer
// tokens are rejected with 401. Config.Load always provides a non-empty
// secret (auto-generated on first boot when DASHBOARD_HOOKS_SECRET is not set).
// Panics if secret is empty — callers must always supply a secret.
//
// store may be nil (per-event recording disabled); when non-nil, Event records a
// truncated, secret-safe HookEvent per received payload.
//
// bridge is required. A nil-means-build-your-own branch would fail silently: the
// endpoints would still answer 200 and hold calls, but the agent enricher reads
// the DI instance, so the UI would show nothing while sessions stalled with no
// control. Panicking is what the empty secret above already does.
//
// Installing the bridge's change callback is the ROUTER's job, not this
// constructor's — see NewRouter. A constructor that reconfigures an injected
// dependency makes "who owns the callback" a question with two answers.
func New(secret string, store *hookstore.Store, onEvent OnEventFn, bridge *HookEnforcer) *Handler {
	if secret == "" {
		panic("hooks.Handler requires a non-empty secret")
	}
	if bridge == nil {
		panic("hooks.Handler requires a permission bridge")
	}
	return &Handler{
		secret:      secret,
		store:       store,
		onEvent:     onEvent,
		pending:     make(map[string]*pendingEntry),
		permissions: bridge,
	}
}

// Event handles POST /api/hooks/event.
// Validates the bearer secret, records a truncated hook event (when a store is
// configured), and acknowledges 204. A missing or incorrect secret always
// returns 401. A malformed or empty body is tolerated — the rescan still fires
// and the response is still 204, so a payload-blind hook script keeps working.
func (h *Handler) Event(w http.ResponseWriter, r *http.Request) {
	if !h.requireSecret(w, r) {
		return
	}
	h.recordEvent(r)
	w.WriteHeader(http.StatusNoContent)
	if h.onEvent != nil {
		go h.onEvent()
	}
}

// hookPayload mirrors the Claude Code hook event written to stdin by the hook
// script. Both camelCase and snake_case keys are accepted so the receiver
// tolerates the raw Claude payload (session_id/tool_name) and any normalized
// variant (sessionId/toolName).
type hookPayload struct {
	HookType     string          `json:"hookType"`
	SessionID    string          `json:"sessionId"`
	SessionIDSnk string          `json:"session_id"`
	ToolName     string          `json:"toolName"`
	ToolNameSnk  string          `json:"tool_name"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

// recordEvent decodes the hook payload and stores a truncated HookEvent. Any
// decode error is swallowed — recording is best-effort and must never change the
// 204 contract. A nil store or empty session is a no-op (store.Record guards the
// empty session).
func (h *Handler) recordEvent(r *http.Request) {
	if h.store == nil {
		return
	}
	var p hookPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, maxEventBodyBytes)).Decode(&p); err != nil {
		return
	}
	sessionID := firstNonEmpty(p.SessionID, p.SessionIDSnk)
	if sessionID == "" {
		return
	}
	h.store.Record(sessionID, sdk.HookEvent{
		Type:    p.HookType,
		Tool:    firstNonEmpty(p.ToolName, p.ToolNameSnk),
		At:      time.Now().Format(time.RFC3339),
		Summary: summarize(p.ToolResponse, p.ToolInput),
	})
}

// summarize returns a truncated, secret-safe preview of the first non-empty raw
// payload field, capped at maxSummaryBytes and kept valid UTF-8. The full
// tool_input / tool_response is never stored or logged.
func summarize(raws ...json.RawMessage) string {
	for _, raw := range raws {
		if len(raw) == 0 {
			continue
		}
		s := strings.TrimSpace(string(raw))
		if len(s) > maxSummaryBytes {
			s = strings.ToValidUTF8(s[:maxSummaryBytes], "")
		}
		return s
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// requireSecret validates the bearer secret on every request.
// h.secret is always non-empty — New panics if an empty secret is supplied.
func (h *Handler) requireSecret(w http.ResponseWriter, r *http.Request) bool {
	got := bearerToken(r)
	if subtle.ConstantTimeCompare([]byte(got), []byte(h.secret)) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

// PreTool handles POST /api/hooks/pre-tool.
// Registers a pending edit decision and blocks until the user approves/rejects or the timeout fires.
func (h *Handler) PreTool(w http.ResponseWriter, r *http.Request) {
	if !h.requireSecret(w, r) {
		return
	}
	var body struct {
		SessionID  string `json:"sessionId"`
		ToolName   string `json:"toolName"`
		FilePath   string `json:"filePath"`
		OldContent string `json:"oldContent"`
		NewContent string `json:"newContent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}

	// Only gate write-type tools. The canonical set is permissions.IsWriteTool —
	// do not add names here; update the source of truth in permissions/allowlist.go instead.
	if !permissions.IsWriteTool(body.ToolName) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"proceed": true})
		return
	}

	id := uuid.New().String()
	entry := &pendingEntry{
		edit: PendingEdit{
			ID:         id,
			SessionID:  body.SessionID,
			ToolName:   body.ToolName,
			FilePath:   body.FilePath,
			OldContent: body.OldContent,
			NewContent: body.NewContent,
			CreatedAt:  time.Now().UnixMilli(),
			Decision:   "pending",
		},
		ch: make(chan string, 1),
	}

	h.mu.Lock()
	h.pending[id] = entry
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()

	select {
	case decision := <-entry.ch:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"proceed": decision == "accept"})
	case <-time.After(editGateTimeout):
		// Timeout: fail-closed — secret is always set here (requireSecret guards entry).
		proceed := false
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"proceed": proceed})
	case <-r.Context().Done():
		// Client disconnected.
	}
}

// Respond handles POST /api/hooks/respond.
// Sends the approve/reject decision to the waiting pre-tool handler.
//
// Browser-facing: authenticated by the session-auth middleware group in
// router.go (or bypass mode), NOT the hooks bearer secret — the edit-gate UI
// carries the session cookie, not DASHBOARD_HOOKS_SECRET.
func (h *Handler) Respond(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Decision string `json:"decision"` // "accept" | "reject"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON"})
		return
	}
	if body.Decision != "accept" && body.Decision != "reject" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "decision must be accept or reject"})
		return
	}

	h.mu.Lock()
	entry, ok := h.pending[body.ID]
	h.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no pending edit with that id"})
		return
	}

	select {
	case entry.ch <- body.Decision:
	default:
		// Already decided (shouldn't normally happen).
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// Pending handles GET /api/hooks/pending.
// Returns all pending edit gate decisions, optionally filtered by sessionId.
//
// Browser-facing: authenticated by the session-auth middleware group in
// router.go (or bypass mode), NOT the hooks bearer secret — see Respond.
func (h *Handler) Pending(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")

	h.mu.Lock()
	var edits []PendingEdit
	for _, entry := range h.pending {
		if sessionID == "" || entry.edit.SessionID == sessionID {
			edits = append(edits, entry.edit)
		}
	}
	h.mu.Unlock()

	if edits == nil {
		edits = []PendingEdit{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"edits": edits})
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}
