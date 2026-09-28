package search_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/search"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/merger"
)

const testJWTSecret = "search-test-secret"

// withAdminAuth signs a JWT as an admin and attaches it as a cookie.
func withAdminAuth(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1", Login: "admin"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	return r
}

// newTestRouter builds a chi router with the search handler wired in.
func newTestRouter(t *testing.T) (*chi.Mux, *db.DBBundle) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	h := search.NewHandler(rawrepo.NewSearchRepo(bundle.DB), merger.New(), nil, false)

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	r.Get("/api/search", apierr.ErrorMiddleware(h.Search))
	return r, bundle
}

// TestSanitizeFtsQuery tests the FTS5 query sanitizer via the exported function.
// We drive it indirectly through the Search handler by asserting no panic/error
// on various inputs. For direct testing we use a whitebox test in the same package.
// The real sanitizer tests are in sanitize_test.go (same package).

// TestSearch_EmptyQuery verifies that an empty q returns {"tasks":[],"agents":[]}.
func TestSearch_EmptyQuery(t *testing.T) {
	r, _ := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=", nil)
	req = withAdminAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var tasks []any
	if err := json.Unmarshal(resp["tasks"], &tasks); err != nil || len(tasks) != 0 {
		t.Errorf("expected tasks=[], got %s", resp["tasks"])
	}
	var agents []any
	if err := json.Unmarshal(resp["agents"], &agents); err != nil || len(agents) != 0 {
		t.Errorf("expected agents=[], got %s", resp["agents"])
	}
}

// TestSearch_EmptyQuery_NoQ verifies that a missing q parameter also returns empty results.
func TestSearch_EmptyQuery_NoQ(t *testing.T) {
	r, _ := newTestRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/search", nil)
	req = withAdminAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestSearch_TaskFTS inserts a task directly and verifies it appears in search results.
func TestSearch_TaskFTS(t *testing.T) {
	r, bundle := newTestRouter(t)

	// Insert a task row directly via raw SQL so the FTS5 trigger fires.
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := bundle.DB.Exec(`
		INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, stage_timeout_seconds, silver_bullet, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"task-id-fts-1", "fts-test-slug", "FTSUniqueKeywordXYZ", "/tmp/test",
		"backlog", "medium", 20, 1800, false, now, now,
	)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=FTSUniqueKeywordXYZ&type=tasks", nil)
	req = withAdminAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Tasks []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"tasks"`
		Agents []any `json:"agents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Tasks) == 0 {
		t.Fatalf("expected at least 1 task result, got 0; body=%s", rr.Body.String())
	}
	if resp.Tasks[0].ID != "task-id-fts-1" {
		t.Errorf("expected task id task-id-fts-1, got %s", resp.Tasks[0].ID)
	}
	if resp.Tasks[0].Title != "FTSUniqueKeywordXYZ" {
		t.Errorf("expected title FTSUniqueKeywordXYZ, got %s", resp.Tasks[0].Title)
	}
}

// TestSearch_TaskVisibility_NonAdmin verifies that a non-admin user only sees their own tasks.
func TestSearch_TaskVisibility_NonAdmin(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	now := time.Now().UTC().Format(time.RFC3339)

	// Insert a task owned by alice.
	_, err = bundle.DB.Exec(`
		INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, stage_timeout_seconds, silver_bullet, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"task-alice", "alice-task", "ScopingTestAlice", "/tmp/alice",
		"backlog", "medium", 20, 1800, false, "user-alice", now, now,
	)
	if err != nil {
		t.Fatalf("insert alice task: %v", err)
	}

	// Insert a task owned by bob.
	_, err = bundle.DB.Exec(`
		INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, stage_timeout_seconds, silver_bullet, user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"task-bob", "bob-task", "ScopingTestBob", "/tmp/bob",
		"backlog", "medium", 20, 1800, false, "user-bob", now, now,
	)
	if err != nil {
		t.Fatalf("insert bob task: %v", err)
	}

	h := search.NewHandler(rawrepo.NewSearchRepo(bundle.DB), merger.New(), nil, false)
	ro := chi.NewRouter()
	ro.Use(auth.RequireAuth(testJWTSecret))
	ro.Get("/api/search", apierr.ErrorMiddleware(h.Search))

	// Sign a non-admin token for alice.
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "user-alice", Login: "alice"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=ScopingTest&type=tasks", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	rr := httptest.NewRecorder()
	ro.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, task := range resp.Tasks {
		if task.ID == "task-bob" {
			t.Errorf("non-admin alice should not see bob's task, but it appeared in results")
		}
	}

	var aliceFound bool
	for _, task := range resp.Tasks {
		if task.ID == "task-alice" {
			aliceFound = true
		}
	}
	if !aliceFound {
		t.Errorf("alice should see her own task, but task-alice was not in results; body=%s", rr.Body.String())
	}
}

// TestSearch_TypeAgents_UnderJWT verifies the agent roster stays out of search
// results whenever the dashboard is not in loopback single-user mode. The
// branch used to be gated on the admin role; it now follows the deployment
// mode, because that role was never grantable to anyone.
func TestSearch_TypeAgents_UnderJWT(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	h := search.NewHandler(rawrepo.NewSearchRepo(bundle.DB), merger.New(), nil, false)
	ro := chi.NewRouter()
	ro.Use(auth.RequireAuth(testJWTSecret))
	ro.Get("/api/search", apierr.ErrorMiddleware(h.Search))

	// Sign a non-admin token.
	token, _ := auth.SignJWT(auth.JWTPayload{Sub: "user-2", Login: "regular"}, testJWTSecret, 3600)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=test&type=agents", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	rr := httptest.NewRecorder()
	ro.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp struct {
		Agents []any `json:"agents"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Agents) != 0 {
		t.Errorf("JWT mode should get empty agents, got %d", len(resp.Agents))
	}
}
