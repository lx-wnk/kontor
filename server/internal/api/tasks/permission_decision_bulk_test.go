package tasks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// countingResumeRecorder records every ResumeFromUser call, unlike the
// shared resumeRecorder which only keeps the last one.
type countingResumeRecorder struct {
	*noopOrchestrator
	calls  int
	prompt string
}

func (r *countingResumeRecorder) ResumeFromUser(_ context.Context, _ string, userPrompt string) (*ent.StageRun, error) {
	r.calls++
	r.prompt = userPrompt
	return &ent.StageRun{ID: "resumed"}, nil
}

// bulkResolveDecision posts to /api/permission-requests/bulk-resolve.
func bulkResolveDecision(t *testing.T, r http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// addPendingPermission adds a second pending permission request on the task's
// existing (first) stage run.
func addPendingPermission(t *testing.T, client *ent.Client, taskID, tool, pattern string) string {
	t.Helper()
	ctx := context.Background()
	runs, err := repo.NewStageRunRepo(client).ListForTask(ctx, taskID)
	if err != nil || len(runs) == 0 {
		t.Fatalf("list runs for task %s: %v", taskID, err)
	}
	req, err := repo.NewPermissionRepo(client).CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
		StageRunID: runs[0].ID,
		Tool:       tool,
		Pattern:    &pattern,
	})
	if err != nil {
		t.Fatalf("create permission request: %v", err)
	}
	return req.ID
}

func mustStageRunID(t *testing.T, client *ent.Client, taskID string) string {
	t.Helper()
	runs, err := repo.NewStageRunRepo(client).ListForTask(context.Background(), taskID)
	if err != nil || len(runs) == 0 {
		t.Fatalf("list runs for task %s: %v", taskID, err)
	}
	return runs[0].ID
}

func TestBulkResolveDecision_AllowRoutineTwoTools(t *testing.T) {
	rec := &countingResumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, _ := seedRoutinePermission(t, client, "bulk-allow-routine", "mcp__mail__read_message", "", true)
	addPendingPermission(t, client, taskID, "mcp__mail__move_message", "")

	w := bulkResolveDecision(t, r, `{"taskId":"`+taskID+`","decision":"allow_routine","all":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Resolved int `json:"resolved"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Resolved != 2 {
		t.Fatalf("expected resolved=2, got %d", resp.Resolved)
	}

	for _, tool := range []string{"mcp__mail__read_message", "mcp__mail__move_message"} {
		grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), tool)
		if err != nil {
			t.Fatalf("list grants for %s: %v", tool, err)
		}
		live := 0
		for _, g := range grants {
			if g.RevokedAt == nil {
				live++
				if g.ContextKind != repo.GrantContextRoutine || g.ContextRef != "r1" || g.Mode != repo.GrantModeAllow {
					t.Errorf("tool %s: expected routine allow grant, got kind=%s ref=%s mode=%s", tool, g.ContextKind, g.ContextRef, g.Mode)
				}
			}
		}
		if live != 1 {
			t.Errorf("tool %s: expected exactly one live grant, got %d", tool, live)
		}
	}
	if rec.calls != 1 || rec.prompt != "" {
		t.Errorf("expected exactly one resume with empty prompt, got calls=%d prompt=%q", rec.calls, rec.prompt)
	}
}

func TestBulkResolveDecision_DenyRoutineTwoTools(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, _ := seedRoutinePermission(t, client, "bulk-deny-routine", "mcp__mail__read_message", "", true)
	addPendingPermission(t, client, taskID, "mcp__mail__move_message", "")

	w := bulkResolveDecision(t, r, `{"taskId":"`+taskID+`","decision":"deny_routine","all":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	for _, tool := range []string{"mcp__mail__read_message", "mcp__mail__move_message"} {
		grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), tool)
		if err != nil {
			t.Fatalf("list grants for %s: %v", tool, err)
		}
		live := 0
		for _, g := range grants {
			if g.RevokedAt == nil {
				live++
				if g.Mode != repo.GrantModeDeny {
					t.Errorf("tool %s: expected deny grant, got mode=%s", tool, g.Mode)
				}
			}
		}
		if live != 1 {
			t.Errorf("tool %s: expected exactly one live grant, got %d", tool, live)
		}
	}
	if !rec.resumed {
		t.Fatal("expected the run to resume")
	}
	if !strings.Contains(rec.prompt, "mcp__mail__read_message") || !strings.Contains(rec.prompt, "mcp__mail__move_message") {
		t.Errorf("prompt must name both tools, got %q", rec.prompt)
	}
	if !strings.Contains(rec.prompt, "for this routine") {
		t.Errorf("prompt must say the scope is the routine, got %q", rec.prompt)
	}
}

func TestBulkResolveDecision_MixedToolsRoutine_400NothingResolved(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, _ := seedRoutinePermission(t, client, "bulk-mixed-routine", "mcp__mail__read_message", "", true)
	addPendingPermission(t, client, taskID, "Bash", "ls")

	w := bulkResolveDecision(t, r, `{"taskId":"`+taskID+`","decision":"allow_routine","all":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "application tools only") {
		t.Errorf("expected the application-tools-only message, got %s", w.Body.String())
	}
	if n, err := repo.NewPermissionRepo(client).CountForStageRun(context.Background(), mustStageRunID(t, client, taskID)); err != nil || n != 2 {
		t.Errorf("expected both requests to remain pending (count=%d, err=%v)", n, err)
	}
	if rec.resumed {
		t.Error("a refused decision must not resume the run")
	}
	grants, err := repo.NewGrantRepo(client).ListForCapability(context.Background(), "mcp__mail__read_message")
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("expected no grant rows, got %d", len(grants))
	}
}

func TestBulkResolveDecision_OutcomeDeniedStillResumes(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, _, _ := seedPendingPermissionWithPattern(t, client, "bulk-outcome-denied", "WebFetch", "domain:example.com")

	w := bulkResolveDecision(t, r, `{"taskId":"`+taskID+`","outcome":"denied","all":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !rec.resumed {
		t.Fatal("expected the run to resume")
	}
	if !strings.Contains(rec.prompt, "WebFetch") {
		t.Errorf("prompt must name the tool, got %q", rec.prompt)
	}
}

func TestBulkResolveDecision_AllowOnceWritesTaskGrant(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newDecisionHandler(t, rec)
	taskID, _ := seedRoutinePermission(t, client, "bulk-allow-once", "mcp__mail__move_message", "", true)

	w := bulkResolveDecision(t, r, `{"taskId":"`+taskID+`","decision":"allow_once","all":true}`)
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
}
