package spawners

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// stripSSEFrame strips the "data: " prefix and "\n\n" suffix added by Broadcaster.
func stripSSEFrame(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, []byte("data: "))
	raw = bytes.TrimSuffix(raw, []byte("\n\n"))
	return raw
}

// newTestHandlerForPkg creates a Handler backed by an in-memory SQLite DB.
func newTestHandlerForPkg(t *testing.T) *Handler {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return NewHandler(repo.NewSpawnerRepo(bundle.Client), nil)
}

func TestCreate_BroadcastsSpawnerCreated(t *testing.T) {
	bc := sse.NewSpawnerBroadcaster(sse.NewBroadcaster())
	h := newTestHandlerForPkg(t)
	h.broadcaster = bc
	ch := bc.Subscribe()
	defer bc.Unsubscribe(ch)

	r := chi.NewRouter()
	h.Mount(r)
	body := `{"name":"My Spawner","slug":"my-spawner","command":"claude"}`
	req := httptest.NewRequest("POST", "/api/spawners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create: got %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	select {
	case raw := <-ch:
		var ev map[string]any
		if err := json.Unmarshal(stripSSEFrame(raw), &ev); err != nil {
			t.Fatalf("frame not JSON: %v", err)
		}
		if ev["type"] != "spawner_created" {
			t.Errorf("event type: got %v, want spawner_created", ev["type"])
		}
		if ev["payload"] == nil {
			t.Error("spawner_created must carry the spawner payload")
		}
	case <-time.After(time.Second):
		t.Fatal("no spawner_created event broadcast")
	}
}

// TestUpdate_BuiltInEditableExceptSlug verifies a built-in spawner accepts field
// edits but rejects a slug change (the resolution backstop depends on its slug).
func TestUpdate_BuiltInEditableExceptSlug(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	bi, err := h.repo.Create(t.Context(), "Claude (default)", "claude-default", "claude", nil, nil, nil, nil, "claude", nil, true)
	if err != nil {
		t.Fatalf("seed built-in: %v", err)
	}

	// Editing the name is allowed.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/spawners/"+bi.ID, bytes.NewBufferString(`{"name":"Renamed Default"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("builtin name edit: got %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var v spawnerView
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Name != "Renamed Default" {
		t.Errorf("name: got %q, want %q", v.Name, "Renamed Default")
	}

	// Changing the slug of a built-in is forbidden.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("PATCH", "/api/spawners/"+bi.ID, bytes.NewBufferString(`{"slug":"new-slug"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatalf("builtin slug change: got %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCreate_RejectsUntrustedAcpAdapterConfigCommand verifies adapter_config.command
// is checked against the same trust policy as the row's command column.
func TestCreate_RejectsUntrustedAcpAdapterConfigCommand(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	body := `{"name":"ACP","slug":"acp-spawner","command":"claude","adapterType":"acp","adapterConfig":{"command":"/tmp/evil"}}`
	req := httptest.NewRequest("POST", "/api/spawners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("create with untrusted adapter_config.command: got %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("could not be resolved")) {
		t.Errorf("error body must explain the reason: %s", rr.Body.String())
	}
}

func TestCreate_RejectsUnknownAcpAdapterConfigCommand(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	body := `{"name":"ACP","slug":"acp-spawner","command":"claude","adapterType":"acp","adapterConfig":{"command":"evil-binary"}}`
	req := httptest.NewRequest("POST", "/api/spawners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("create with unknown adapter_config.command: got %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("not in the allow-list")) {
		t.Errorf("error body must explain the reason: %s", rr.Body.String())
	}
}

func TestCreate_AcceptsAllowlistedAcpAdapterConfigCommand(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	body := `{"name":"ACP","slug":"acp-spawner","command":"claude","adapterType":"acp","adapterConfig":{"command":"npx"}}`
	req := httptest.NewRequest("POST", "/api/spawners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create with allowlisted adapter_config.command: got %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCreate_SavesEffortForClaudeAdapter verifies effort can be persisted
// through the real create endpoint, not merely accepted by the resolver in
// isolation.
func TestCreate_SavesEffortForClaudeAdapter(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	body := `{"name":"Claude High","slug":"claude-high","command":"claude","adapterType":"claude","adapterConfig":{"effort":"high"}}`
	req := httptest.NewRequest("POST", "/api/spawners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create with effort adapter_config: got %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var v spawnerView
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.AdapterConfig["effort"] != "high" {
		t.Fatalf("adapterConfig.effort: got %q, want %q", v.AdapterConfig["effort"], "high")
	}
}

// TestCreate_RejectsUnrecognisedEffortLevel verifies the trust boundary
// refuses an effort value outside the known set through the real create
// endpoint, not merely in the resolver's own validation-only tests.
func TestCreate_RejectsUnrecognisedEffortLevel(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	body := `{"name":"Claude Ultra","slug":"claude-ultra","command":"claude","adapterType":"claude","adapterConfig":{"effort":"ultra"}}`
	req := httptest.NewRequest("POST", "/api/spawners", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("create with unrecognised effort adapter_config: got %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestUpdate_RejectsUntrustedAcpAdapterConfigCommand mirrors the create-path check
// on PATCH, where effectiveAdapterType may come from the existing row.
func TestUpdate_RejectsUntrustedAcpAdapterConfigCommand(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	s, err := h.repo.Create(t.Context(), "ACP", "acp-spawner", "claude", nil, nil, nil, nil, "acp", map[string]string{"command": "npx"}, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/spawners/"+s.ID, bytes.NewBufferString(`{"adapterConfig":{"command":"/tmp/evil"}}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("update with untrusted adapter_config.command: got %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestSetDefault_SwitchesAndBroadcastsBoth verifies POST /default moves the flag
// atomically and broadcasts an update for both the new and the former default.
func TestSetDefault_SwitchesAndBroadcastsBoth(t *testing.T) {
	bc := sse.NewSpawnerBroadcaster(sse.NewBroadcaster())
	h := newTestHandlerForPkg(t)
	h.broadcaster = bc
	r := chi.NewRouter()
	h.Mount(r)

	a, err := h.repo.Create(t.Context(), "A", "spawner-a", "claude", nil, nil, nil, nil, "claude", nil, false)
	if err != nil {
		t.Fatalf("seed a: %v", err)
	}
	b, err := h.repo.Create(t.Context(), "B", "spawner-b", "claude", nil, nil, nil, nil, "claude", nil, false)
	if err != nil {
		t.Fatalf("seed b: %v", err)
	}
	if _, _, err := h.repo.SetDefault(t.Context(), a.ID); err != nil {
		t.Fatalf("seed default a: %v", err)
	}

	ch := bc.Subscribe()
	defer bc.Unsubscribe(ch)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/spawners/"+b.ID+"/default", nil)
	r.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("set default: got %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var v spawnerView
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !v.IsDefault {
		t.Error("new default response must have isDefault=true")
	}

	// The former default must have been cleared.
	prev, err := h.repo.GetByID(t.Context(), a.ID)
	if err != nil {
		t.Fatalf("reload a: %v", err)
	}
	if prev.IsDefault {
		t.Error("former default a must no longer be default")
	}

	// Two spawner_updated events: the new default and the cleared former default.
	seen := map[string]bool{}
	for range 2 {
		select {
		case raw := <-ch:
			var ev map[string]any
			if err := json.Unmarshal(stripSSEFrame(raw), &ev); err != nil {
				t.Fatalf("frame not JSON: %v", err)
			}
			if ev["type"] != "spawner_updated" {
				t.Errorf("event type: got %v, want spawner_updated", ev["type"])
			}
			seen[ev["spawnerId"].(string)] = true
		case <-time.After(time.Second):
			t.Fatal("expected two spawner_updated events")
		}
	}
	if !seen[a.ID] || !seen[b.ID] {
		t.Errorf("expected updates for both %s and %s, saw %v", a.ID, b.ID, seen)
	}
}

// TestDelete_DefaultRejected verifies the current default cannot be deleted.
func TestDelete_DefaultRejected(t *testing.T) {
	h := newTestHandlerForPkg(t)
	r := chi.NewRouter()
	h.Mount(r)

	a, err := h.repo.Create(t.Context(), "A", "spawner-a", "claude", nil, nil, nil, nil, "claude", nil, false)
	if err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if _, _, err := h.repo.SetDefault(t.Context(), a.ID); err != nil {
		t.Fatalf("set default: %v", err)
	}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/spawners/"+a.ID, nil)
	r.ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("delete default: got %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}
