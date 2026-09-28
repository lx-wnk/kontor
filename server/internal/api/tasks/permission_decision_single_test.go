package tasks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// newDecisionHandler is newRetryHandler plus a wired GrantRepo, needed for
// allow_routine/deny_routine decisions.
func newDecisionHandler(t *testing.T, orch tasks.OrchestratorIface) (*ent.Client, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     repo.NewTaskRepo(client),
		SRRepo:       repo.NewStageRunRepo(client),
		PermRepo:     repo.NewPermissionRepo(client),
		AuditRepo:    repo.NewAuditEventRepo(client),
		CfgRepo:      repo.NewPipelineConfigRepo(client),
		GrantRepo:    repo.NewGrantRepo(client),
		Orchestrator: orch,
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return client, r
}

// seedRoutinePermission seeds a pending permission request for tool/pattern;
// when routine is true the owning task is stamped as started by routine "r1".
func seedRoutinePermission(t *testing.T, client *ent.Client, slug, tool, pattern string, routine bool) (taskID, reqID string) {
	t.Helper()
	taskID, _, reqID = seedPendingPermissionWithPattern(t, client, slug, tool, pattern)
	if routine {
		if _, err := client.Task.UpdateOneID(taskID).SetRoutineID("r1").SetAutonomy("full").Save(context.Background()); err != nil {
			t.Fatalf("set routine: %v", err)
		}
	}
	return taskID, reqID
}

func resolveDecision(t *testing.T, r *chi.Mux, taskID, reqID, body string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/tasks/" + taskID + "/permission-requests/" + reqID + "/resolve"
	req := withAuth(t, httptest.NewRequest(http.MethodPost, url, strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestSingleResolve_AllowOnce_WritesTaskGrantForApplicationTool(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "allow-once", "mcp__mail__move_message", "", true)

	w := resolveDecision(t, r, taskID, reqID, `{"decision":"allow_once"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("expected exactly one grant, got %d", len(grants))
	}
	if grants[0].ContextKind != repo.GrantContextTask || grants[0].ContextRef != taskID || grants[0].Mode != repo.GrantModeAllow {
		t.Errorf("expected task-context allow grant, got kind=%s ref=%s mode=%s", grants[0].ContextKind, grants[0].ContextRef, grants[0].Mode)
	}
	if !rec.resumed || rec.prompt != "" {
		t.Errorf("expected resume with empty prompt, got resumed=%v prompt=%q", rec.resumed, rec.prompt)
	}
}

func TestSingleResolve_AllowRoutine_WritesRoutineGrant(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "allow-routine", "mcp__mail__move_message", "", true)

	w := resolveDecision(t, r, taskID, reqID, `{"decision":"allow_routine"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("expected exactly one grant, got %d", len(grants))
	}
	if grants[0].ContextKind != repo.GrantContextRoutine || grants[0].ContextRef != "r1" || grants[0].Mode != repo.GrantModeAllow {
		t.Errorf("expected routine-context allow grant, got kind=%s ref=%s mode=%s", grants[0].ContextKind, grants[0].ContextRef, grants[0].Mode)
	}
	if !rec.resumed || rec.prompt != "" {
		t.Errorf("expected resume with empty prompt, got resumed=%v prompt=%q", rec.resumed, rec.prompt)
	}
}

func TestSingleResolve_DenyRoutine_WritesDenyAndSaysSo(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "deny-routine", "mcp__mail__move_message", "", true)

	w := resolveDecision(t, r, taskID, reqID, `{"decision":"deny_routine"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("expected exactly one grant, got %d", len(grants))
	}
	if grants[0].ContextKind != repo.GrantContextRoutine || grants[0].ContextRef != "r1" || grants[0].Mode != repo.GrantModeDeny {
		t.Errorf("expected routine-context deny grant, got kind=%s ref=%s mode=%s", grants[0].ContextKind, grants[0].ContextRef, grants[0].Mode)
	}
	if !rec.resumed {
		t.Fatal("expected the run to resume")
	}
	if !strings.Contains(rec.prompt, "mcp__mail__move_message") || !strings.Contains(rec.prompt, "for this routine") {
		t.Errorf("prompt must name the tool and the routine scope, got %q", rec.prompt)
	}
}

func TestSingleResolve_DenyOnce_WritesNothing(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "deny-once", "mcp__mail__move_message", "", true)

	w := resolveDecision(t, r, taskID, reqID, `{"decision":"deny_once"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("expected no grant rows, got %d", len(grants))
	}
	if !rec.resumed {
		t.Fatal("expected the run to resume")
	}
	if !strings.Contains(rec.prompt, "refused permission") {
		t.Errorf("prompt must say permission was refused, got %q", rec.prompt)
	}
}

func TestSingleResolve_OutcomeAliasStillWorks(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "outcome-alias", "mcp__mail__move_message", "", true)

	w := resolveDecision(t, r, taskID, reqID, `{"outcome":"granted"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 1 || grants[0].ContextKind != repo.GrantContextTask || grants[0].ContextRef != taskID {
		t.Fatalf("expected a task-context grant like allow_once, got %+v", grants)
	}
}

func TestSingleResolve_RoutineDecisionWithoutRoutine_400KeepsPending(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "no-routine", "mcp__mail__move_message", "", false)

	w := resolveDecision(t, r, taskID, reqID, `{"decision":"allow_routine"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "routine decisions need a task started by a routine") {
		t.Errorf("expected the routine-required message, got %s", w.Body.String())
	}
	pr, err := repo.NewPermissionRepo(client).GetPermissionRequest(context.Background(), reqID)
	if err != nil {
		t.Fatalf("get permission request: %v", err)
	}
	if pr.Outcome != nil {
		t.Errorf("expected the request to remain pending, got outcome=%v", *pr.Outcome)
	}
	if rec.resumed {
		t.Error("a refused decision must not resume the run")
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("expected no grant rows, got %d", len(grants))
	}
}

func TestSingleResolve_RoutineDecisionForBash_400(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, reqID := seedRoutinePermission(t, client, "routine-bash", "Bash", "ls", true)

	w := resolveDecision(t, r, taskID, reqID, `{"decision":"allow_routine"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "application tools only") {
		t.Errorf("expected the application-tools-only message, got %s", w.Body.String())
	}
}

func TestSingleResolve_AllowRoutineTwice_OneGrant(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	ctx := context.Background()

	task, err := repo.NewTaskRepo(client).Create(ctx, repo.CreateTaskInput{
		Slug: "routine-twice", Title: "routine-twice", Cwd: t.TempDir(),
		MaxIterations: 5, Priority: "normal", CurrentStage: "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := client.Task.UpdateOneID(task.ID).SetRoutineID("r1").SetAutonomy("full").Save(ctx); err != nil {
		t.Fatalf("set routine: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation", Iteration: 0})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	pattern := ""
	req1, err := repo.NewPermissionRepo(client).CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
		StageRunID: run.ID, Tool: "mcp__mail__move_message", Pattern: &pattern,
	})
	if err != nil {
		t.Fatalf("create req1: %v", err)
	}
	req2, err := repo.NewPermissionRepo(client).CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
		StageRunID: run.ID, Tool: "mcp__mail__move_message", Pattern: &pattern,
	})
	if err != nil {
		t.Fatalf("create req2: %v", err)
	}

	if w := resolveDecision(t, r, task.ID, req1.ID, `{"decision":"allow_routine"}`); w.Code != http.StatusOK {
		t.Fatalf("first resolve: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := resolveDecision(t, r, task.ID, req2.ID, `{"decision":"allow_routine"}`); w.Code != http.StatusOK {
		t.Fatalf("second resolve: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	grants, err := repo.NewGrantRepo(client).ListForCapability(ctx, "mcp__mail__move_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	live := 0
	for _, g := range grants {
		if g.RevokedAt == nil {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("expected exactly one live routine grant, got %d (of %d total)", live, len(grants))
	}
}
