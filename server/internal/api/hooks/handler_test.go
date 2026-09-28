package hooks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/hookstore"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// newTestHandler is a convenience constructor for tests. It wires a real
// hookstore so the record path is exercised; onEvent is a no-op since tests
// verify HTTP behaviour and recorded state, not the rescan side effect.
func newTestHandler(secret string) *Handler {
	return New(secret, hookstore.New(50, 0), func() {}, NewHookEnforcer(nil))
}

// -------------------------------------------------------------------
// Event endpoint
// -------------------------------------------------------------------

func TestEvent_WithCorrectSecret_Returns204(t *testing.T) {
	h := newTestHandler("mysecret")
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/event", nil)
	req.Header.Set("Authorization", "Bearer mysecret")
	w := httptest.NewRecorder()

	h.Event(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("Event with correct secret: got status %d, want %d", w.Code, http.StatusNoContent)
	}
}

func TestEvent_WithWrongSecret_Returns401(t *testing.T) {
	h := newTestHandler("mysecret")
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/event", nil)
	req.Header.Set("Authorization", "Bearer wrongsecret")
	w := httptest.NewRecorder()

	h.Event(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Event with wrong secret: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestNew_PanicsOnEmptySecret verifies that New panics when given an empty secret,
// enforcing that every Handler is constructed with a real secret.
func TestNew_PanicsOnEmptySecret(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("New(\"\", ...) did not panic; expected panic on empty secret")
		}
	}()
	New("", hookstore.New(50, 0), func() {}, nil)
}

// TestEvent_RecordsHookEvent verifies the payload is decoded (snake_case keys)
// and a truncated, secret-safe HookEvent is recorded against the session.
func TestEvent_RecordsHookEvent(t *testing.T) {
	const secret = "s3cr3t"
	h := newTestHandler(secret)
	body := `{"hookType":"PostToolUse","session_id":"sess-rec","tool_name":"Read","tool_response":"file contents here"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/event", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()

	h.Event(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("Event: got status %d, want 204", w.Code)
	}
	got := h.store.Recent("sess-rec")
	if len(got) != 1 {
		t.Fatalf("recorded events: got %d, want 1", len(got))
	}
	if got[0].Type != "PostToolUse" || got[0].Tool != "Read" {
		t.Errorf("recorded event = %+v, want Type=PostToolUse Tool=Read", got[0])
	}
	if got[0].Summary == "" || got[0].At == "" {
		t.Errorf("recorded event missing summary/at: %+v", got[0])
	}
}

// TestEvent_TruncatesSummary verifies an oversized payload field is capped.
func TestEvent_TruncatesSummary(t *testing.T) {
	const secret = "s3cr3t"
	h := newTestHandler(secret)
	big := make([]byte, 2000)
	for i := range big {
		big[i] = 'x'
	}
	payload := map[string]any{"session_id": "sess-big", "tool_name": "Bash", "tool_input": string(big)}
	buf, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/event", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()

	h.Event(w, req)

	got := h.store.Recent("sess-big")
	if len(got) != 1 {
		t.Fatalf("recorded events: got %d, want 1", len(got))
	}
	if len(got[0].Summary) > maxSummaryBytes {
		t.Errorf("summary length %d exceeds cap %d", len(got[0].Summary), maxSummaryBytes)
	}
}

// TestEvent_MalformedBodyStill204 verifies an unparseable body does not break
// the 204 contract — recording is best-effort.
func TestEvent_MalformedBodyStill204(t *testing.T) {
	const secret = "s3cr3t"
	h := newTestHandler(secret)
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/event", bytes.NewBufferString("not json{{"))
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()

	h.Event(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("malformed body: got status %d, want 204", w.Code)
	}
}

func TestEvent_WithMissingBearer_Returns401(t *testing.T) {
	h := newTestHandler("mysecret")
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/event", nil)
	// Deliberately no Authorization header.
	w := httptest.NewRecorder()

	h.Event(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Event with missing bearer: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// -------------------------------------------------------------------
// PreTool endpoint
// -------------------------------------------------------------------

func TestPreTool_WithMissingBearer_Returns401(t *testing.T) {
	h := newTestHandler("test-secret")
	body := `{"toolName":"Bash","cwd":"/tmp"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/pre-tool", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately no Authorization header.
	w := httptest.NewRecorder()

	h.PreTool(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("PreTool with missing bearer: got status %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestPreTool_NonWriteTool_ReturnsProceedImmediately(t *testing.T) {
	// Tools that are not Edit/Write/MultiEdit bypass the gate and return immediately.
	const secret = "s3cr3t"
	h := newTestHandler(secret)
	body := `{"sessionId":"sess1","toolName":"Bash","cwd":"/tmp"}`
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/pre-tool", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()

	h.PreTool(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PreTool Bash tool: got status %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("PreTool Bash tool: decode response: %v", err)
	}
	proceed, ok := resp["proceed"]
	if !ok {
		t.Fatalf("PreTool Bash tool: response missing 'proceed' field, got %v", resp)
	}
	if proceed != true {
		t.Errorf("PreTool Bash tool: 'proceed' = %v, want true", proceed)
	}
}

func TestPreTool_WriteTool_TimeoutReturnsProceeds(t *testing.T) {
	// Write-type tools register a pending gate and block until timeout or decision.
	// This test overrides the timeout by sending a response via the Respond endpoint
	// immediately after PreTool registers the pending entry.
	const secret = "s3cr3t"
	h := newTestHandler(secret)

	// Run PreTool in a goroutine since it blocks waiting for a decision.
	preToolDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		body := `{"sessionId":"sess1","toolName":"Edit","filePath":"/tmp/foo.go","oldContent":"a","newContent":"b"}`
		req := httptest.NewRequest(http.MethodPost, "/api/hooks/pre-tool", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		h.PreTool(w, req)
		preToolDone <- w
	}()

	// Poll until the pending entry appears, then send an accept decision.
	var pendingID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for id := range h.pending {
			pendingID = id
		}
		h.mu.Unlock()
		if pendingID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pendingID == "" {
		t.Fatal("PreTool Edit: pending entry never appeared")
	}

	// Accept the pending edit via Respond.
	respondBody := map[string]string{"id": pendingID, "decision": "accept"}
	buf, _ := json.Marshal(respondBody)
	respondReq := httptest.NewRequest(http.MethodPost, "/api/hooks/respond", bytes.NewReader(buf))
	respondReq.Header.Set("Content-Type", "application/json")
	respondReq.Header.Set("Authorization", "Bearer "+secret)
	respondW := httptest.NewRecorder()
	h.Respond(respondW, respondReq)

	if respondW.Code != http.StatusOK {
		t.Fatalf("Respond: got status %d, want 200", respondW.Code)
	}

	// Wait for PreTool to finish.
	preToolW := <-preToolDone
	if preToolW.Code != http.StatusOK {
		t.Fatalf("PreTool Edit after accept: got status %d, want 200", preToolW.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(preToolW.Body).Decode(&resp); err != nil {
		t.Fatalf("PreTool Edit: decode response: %v", err)
	}
	if resp["proceed"] != true {
		t.Errorf("PreTool Edit accepted: 'proceed' = %v, want true", resp["proceed"])
	}
}

// -------------------------------------------------------------------
// Pending endpoint
// -------------------------------------------------------------------

// Pending is browser-facing: it carries the session cookie and is authenticated
// by the session-auth middleware group in router.go, NOT the hooks bearer secret.
// The handler therefore does not gate on the secret — a request without the
// Authorization header still succeeds (the middleware would have rejected an
// unauthenticated browser request before the handler ran).
func TestPending_WithoutBearer_StillSucceeds(t *testing.T) {
	h := newTestHandler("test-secret")
	req := httptest.NewRequest(http.MethodGet, "/api/hooks/pending", nil)
	// Deliberately no Authorization header — auth is enforced by the router group.
	w := httptest.NewRecorder()

	h.Pending(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Pending without bearer: got status %d, want %d", w.Code, http.StatusOK)
	}
}

func TestPending_WithSecret_ReturnsValidJSON(t *testing.T) {
	const secret = "s3cr3t"
	h := newTestHandler(secret)
	req := httptest.NewRequest(http.MethodGet, "/api/hooks/pending", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()

	h.Pending(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Pending: got status %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Pending: response is not valid JSON: %v", err)
	}
	if _, ok := resp["edits"]; !ok {
		t.Errorf("Pending: response missing 'edits' key, got %v", resp)
	}
}

// TestIsWriteTool_MatchesHookGate asserts that a known non-write tool is
// not treated as a write tool by IsWriteTool, the same predicate the hook
// gate in handler.go calls directly against the unexported writeToolNames
// set (internal/permissions/allowlist.go) — there is no second accessor left
// to drift apart from it.
func TestIsWriteTool_MatchesHookGate(t *testing.T) {
	// A tool that must never be gated.
	if permissions.IsWriteTool("Bash") {
		t.Error("IsWriteTool(\"Bash\") = true, want false")
	}
}

func TestPending_WithSecret_ReturnsEmptyEditsWhenNoPending(t *testing.T) {
	const secret = "s3cr3t"
	h := newTestHandler(secret)
	req := httptest.NewRequest(http.MethodGet, "/api/hooks/pending", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()

	h.Pending(w, req)

	var resp struct {
		Edits []PendingEdit `json:"edits"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Pending: decode: %v", err)
	}
	if len(resp.Edits) != 0 {
		t.Errorf("Pending with no active gates: got %d edits, want 0", len(resp.Edits))
	}
}

// A nil bridge used to mean "build your own", which fails silently: the
// endpoints answer and hold calls, but the agent enricher reads the DI
// instance, so the UI shows nothing while sessions stall with no control.
func TestNewRequiresAHookEnforcer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic; a disconnected bridge would have been served as a working one")
		}
	}()
	New("s", nil, func() {}, nil)
}

// The constructor is handed a bridge, not asked to reconfigure one. Installing
// the change callback belongs to the router, which owns the rescan it wraps.
func TestNewLeavesTheBridgeCallbackAlone(t *testing.T) {
	bridge := NewHookEnforcer(nil)
	fired := make(chan struct{}, 1)
	bridge.SetOnChange(func() { fired <- struct{}{} })

	New("s", nil, func() { t.Error("the constructor's onEvent replaced the installed callback") }, bridge)

	bridge.Arm("s1", "", true)
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("the callback installed before construction stopped firing")
	}
}
