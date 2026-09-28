package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

const testJWTSecret = "test-secret-projects"

// authedRouter mounts h behind RequireAuth so PayloadFromContext is populated
// from the request's JWT cookie — matching production auth wiring.
func authedRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return r
}

// withJWT attaches a signed session cookie. The bool parameter is retained so
// call sites keep reading as "privileged / not privileged", but the JWT no
// longer carries a role — privilege now follows the deployment mode.
func withJWT(t *testing.T, req *http.Request, _ bool) *http.Request {
	t.Helper()
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "u1", Login: "tester"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	return req
}

// seedProject inserts a project directly via the repo and returns its id.
func seedProject(t *testing.T, h *Handler) string {
	t.Helper()
	p, err := h.projects.Create(context.Background(), "Proj", "proj", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p.ID
}

func TestUpdate_JWTSetupCommand_Forbidden(t *testing.T) {
	h := newTestHandler(t, false)
	id := seedProject(t, h)
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(`{"setupCommand":"rm -rf /"}`))
	req.Header.Set("Content-Type", "application/json")
	req = withJWT(t, req, false)
	rr := httptest.NewRecorder()
	authedRouter(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("authenticated setupCommand: got %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

// TestUpdate_PrivilegedJWTSetupCommand_StillForbidden pins the consequence of
// removing the admin role: setup_command runs an arbitrary server-side `sh -c`,
// so under JWT auth NO caller may set it any more. The case this replaces
// asserted that an admin could — a privilege the codebase never granted to
// anyone, since nothing ever set is_admin.
func TestUpdate_PrivilegedJWTSetupCommand_StillForbidden(t *testing.T) {
	h := newTestHandler(t, false)
	id := seedProject(t, h)
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(`{"setupCommand":"echo hi"}`))
	req.Header.Set("Content-Type", "application/json")
	req = withJWT(t, req, true)
	rr := httptest.NewRecorder()
	authedRouter(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("privileged JWT setupCommand: got %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpdate_BypassSetupCommand_Allowed(t *testing.T) {
	h := newTestHandler(t, true)
	id := seedProject(t, h)
	// Bypass mode skips RequireAuth, so mount directly without a JWT.
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(`{"setupCommand":"echo hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bypass setupCommand: got %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpdate_JWTWithoutSetupCommand_Allowed(t *testing.T) {
	h := newTestHandler(t, false)
	id := seedProject(t, h)
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(`{"name":"Renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	req = withJWT(t, req, false)
	rr := httptest.NewRecorder()
	authedRouter(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("non-admin non-setupCommand update: got %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreate_JWTSetupCommand_Forbidden(t *testing.T) {
	h := newTestHandler(t, false)
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBufferString(`{"name":"P","slug":"p","setupCommand":"rm -rf /"}`))
	req.Header.Set("Content-Type", "application/json")
	req = withJWT(t, req, false)
	rr := httptest.NewRecorder()
	authedRouter(h).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin create setupCommand: got %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}

// stripSSEFrame strips the "data: " prefix and "\n\n" suffix added by Broadcaster.
func stripSSEFrame(raw []byte) []byte {
	raw = bytes.TrimPrefix(raw, []byte("data: "))
	raw = bytes.TrimSuffix(raw, []byte("\n\n"))
	return raw
}

// newTestHandler creates a Handler backed by an in-memory SQLite DB with no
// TaskProjectOps. bypassAuth controls the per-field setup_command admin gate.
func newTestHandler(t *testing.T, bypassAuth bool) *Handler {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return NewHandler(repo.NewProjectRepo(bundle.Client), repo.NewProjectFolderRepo(bundle.Client), nil, nil, bypassAuth)
}

func TestCreate_BroadcastsProjectCreated(t *testing.T) {
	bc := sse.NewProjectBroadcaster(sse.NewBroadcaster())
	h := newTestHandler(t, true)
	h.broadcaster = bc
	ch := bc.Subscribe()
	defer bc.Unsubscribe(ch)

	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBufferString(`{"name":"Proj","slug":"proj"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != 201 {
		t.Fatalf("create: got %d, body=%s", rr.Code, rr.Body.String())
	}

	select {
	case raw := <-ch:
		var ev map[string]any
		if err := json.Unmarshal(stripSSEFrame(raw), &ev); err != nil {
			t.Fatalf("frame not JSON: %v", err)
		}
		if ev["type"] != "project_created" {
			t.Errorf("event type: got %v, want project_created", ev["type"])
		}
		if ev["payload"] == nil {
			t.Error("project_created must carry the project payload")
		}
	case <-time.After(time.Second):
		t.Fatal("no project_created event broadcast")
	}
}

// The MCP writer bounds these; the HTTP writer must agree, or the same rule
// depends on which door the caller used.
func TestCreate_RejectsAnOverlongName(t *testing.T) {
	h := newTestHandler(t, true)
	body := `{"name":"` + strings.Repeat("n", validation.MaxProjectNameLen+1) + `","slug":"long-name"}`
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r := chi.NewRouter()
	h.Mount(r)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong name: got %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), validation.ProjectNameLengthMessage) {
		t.Fatalf("error must name the limit, body=%s", rr.Body.String())
	}
}

// A bound that only guards creation is no bound: the same project can be
// renamed past it a second later.
func TestUpdate_RejectsAnOverlongName(t *testing.T) {
	h := newTestHandler(t, true)
	id := seedProject(t, h)
	body := `{"name":"` + strings.Repeat("n", validation.MaxProjectNameLen+1) + `"}`
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r := chi.NewRouter()
	h.Mount(r)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong name: got %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), validation.ProjectNameLengthMessage) {
		t.Fatalf("error must name the limit, body=%s", rr.Body.String())
	}
}

func TestUpdate_RejectsAnOverlongDescription(t *testing.T) {
	h := newTestHandler(t, true)
	id := seedProject(t, h)
	body := `{"description":"` + strings.Repeat("d", validation.MaxProjectDescriptionLen+1) + `"}`
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r := chi.NewRouter()
	h.Mount(r)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong description: got %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), validation.ProjectDescriptionLengthMessage) {
		t.Fatalf("error must name the limit, body=%s", rr.Body.String())
	}
}

func TestUpdate_RejectsAnEmptyName(t *testing.T) {
	h := newTestHandler(t, true)
	id := seedProject(t, h)
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(`{"name":""}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r := chi.NewRouter()
	h.Mount(r)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("empty name: got %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

// A name at the limit must still pass, or the guard above would be indistinguishable
// from a broken one that rejects every rename.
func TestUpdate_AcceptsANameAtTheLimit(t *testing.T) {
	h := newTestHandler(t, true)
	id := seedProject(t, h)
	body := `{"name":"` + strings.Repeat("n", validation.MaxProjectNameLen) + `"}`
	req := httptest.NewRequest("PATCH", "/api/projects/"+id, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r := chi.NewRouter()
	h.Mount(r)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("name at the limit: got %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreate_RejectsAnOverlongDescription(t *testing.T) {
	h := newTestHandler(t, true)
	body := `{"name":"Fine","slug":"long-description","description":"` + strings.Repeat("d", validation.MaxProjectDescriptionLen+1) + `"}`
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r := chi.NewRouter()
	h.Mount(r)
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong description: got %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), validation.ProjectDescriptionLengthMessage) {
		t.Fatalf("error must name the limit, body=%s", rr.Body.String())
	}
}
