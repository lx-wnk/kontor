package tasks_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	rawrepo "github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// listScopeRouter builds a task handler in the requested deployment mode and
// seeds one task per user. It mounts without RequireAuth so the test drives the
// handler directly; scoping is decided by the mode, not by the request.
func listScopeRouter(t *testing.T, bypassAuth bool) *chi.Mux {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	for slug, owner := range map[string]string{"owned": "bypass-user", "foreign": "someone-else"} {
		uid := owner
		if _, err := taskRepo.Create(t.Context(), repo.CreateTaskInput{
			Slug: slug, Title: slug, Cwd: "/tmp",
			CurrentStage: "backlog", Priority: "medium",
			MaxIterations: 20, StageTimeoutSeconds: 1800,
			UserID: &uid,
		}); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}

	h := tasks.NewHandler(tasks.Deps{
		Client:       bundle.Client,
		TaskRepo:     taskRepo,
		SRBulkRepo:   rawrepo.NewStageRunBulkRepo(bundle.DB),
		SRRepo:       repo.NewStageRunRepo(bundle.Client),
		PermRepo:     repo.NewPermissionRepo(bundle.Client),
		AuditRepo:    repo.NewAuditEventRepo(bundle.Client),
		CfgRepo:      repo.NewPipelineConfigRepo(bundle.Client),
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
		BypassAuth:   bypassAuth,
	})
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func listedSlugs(t *testing.T, r *chi.Mux) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks: got %d, body=%s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v — body=%s", err, rec.Body.String())
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Slug)
	}
	return out
}

// TestList_BypassModeIsUnscoped pins the behaviour that used to ride on the
// admin role: in loopback single-user mode the listing shows every task, not
// only those owned by the implicit user. Tasks created before the current
// identity existed — or by the pipeline — would otherwise vanish from the UI.
func TestList_BypassModeIsUnscoped(t *testing.T) {
	got := listedSlugs(t, listScopeRouter(t, true))
	if len(got) != 2 {
		t.Fatalf("bypass mode must list every task, got %v", got)
	}
}

// TestList_JWTModeIsScopedToTheCaller is the other half: with auth enabled the
// listing is restricted to the requesting user, so one deployment mode cannot
// leak another user's tasks.
func TestList_JWTModeIsScopedToTheCaller(t *testing.T) {
	got := listedSlugs(t, listScopeRouter(t, false))
	if len(got) != 0 {
		t.Fatalf("JWT mode must scope to the caller (no identity here), got %v", got)
	}
}
