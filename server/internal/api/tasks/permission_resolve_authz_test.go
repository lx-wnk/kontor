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

// seedPendingPermission creates a task with one stage_run and one pending
// permission request, returning the task id, stage_run id, and request id.
func seedPendingPermission(t *testing.T, client *ent.Client, slug string) (taskID, runID, reqID string) {
	t.Helper()
	ctx := context.Background()
	task, err := repo.NewTaskRepo(client).Create(ctx, repo.CreateTaskInput{
		Slug:          slug,
		Title:         slug,
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation", Iteration: 0})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	req, err := repo.NewPermissionRepo(client).CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{StageRunID: run.ID, Tool: "Bash"})
	if err != nil {
		t.Fatalf("create permission request: %v", err)
	}
	return task.ID, run.ID, req.ID
}

// A caller scoped to task A must not be able to resolve a permission request
// that belongs to task B by passing B's id in the bulk-resolve permissionIds.
func TestBulkResolve_RejectsForeignPermissionIDs(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskA, _, _ := seedPendingPermission(t, client, "authz-task-a")
	_, runB, reqB := seedPendingPermission(t, client, "authz-task-b")

	body := `{"taskId":"` + taskA + `","outcome":"granted","permissionIds":["` + reqB + `"]}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Resolved int      `json:"resolved"`
		Errors   []string `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Resolved != 0 {
		t.Errorf("foreign permission must not be resolved, got resolved=%d", resp.Resolved)
	}
	if len(resp.Errors) == 0 {
		t.Errorf("expected an error entry for the foreign permission id")
	}
	if n, err := repo.NewPermissionRepo(client).CountForStageRun(context.Background(), runB); err != nil || n != 1 {
		t.Errorf("task B permission must remain pending (count=%d, err=%v)", n, err)
	}
}

// seedPendingPermissionWithPattern creates a task with one stage_run and a pending
// permission request carrying the given tool and pattern.
func seedPendingPermissionWithPattern(t *testing.T, client *ent.Client, slug, tool, pattern string) (taskID, runID, reqID string) {
	t.Helper()
	ctx := context.Background()
	task, err := repo.NewTaskRepo(client).Create(ctx, repo.CreateTaskInput{
		Slug:          slug,
		Title:         slug,
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation", Iteration: 0})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	req, err := repo.NewPermissionRepo(client).CreatePermissionRequest(ctx, repo.CreatePermissionRequestInput{
		StageRunID: run.ID,
		Tool:       tool,
		Pattern:    &pattern,
	})
	if err != nil {
		t.Fatalf("create permission request: %v", err)
	}
	return task.ID, run.ID, req.ID
}

// TestBulkResolve_Granted_CreatesTaskPermission verifies that resolving a
// permission request as "granted" creates a matching task_permission so the
// respawned agent's allow-list actually includes the tool.
func TestBulkResolve_Granted_CreatesTaskPermission(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "grant-creates-perm", "Bash", "grep -r*")

	body := `{"taskId":"` + taskID + `","outcome":"granted","permissionIds":["` + reqID + `"]}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Resolved int      `json:"resolved"`
		Errors   []string `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Resolved != 1 {
		t.Fatalf("expected resolved=1, got %d", resp.Resolved)
	}

	perms, err := repo.NewPermissionRepo(client).ListEffectiveTaskPermissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(perms) == 0 {
		t.Fatal("expected at least one task_permission after granted resolve, got none")
	}
	found := false
	for _, p := range perms {
		if p.Tool == "Bash" && p.Pattern != nil && *p.Pattern == "grep -r*" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("task_permission for Bash/\"grep -r*\" not found; got %+v", perms)
	}
}

// TestBulkResolve_Granted_RecordsDecidedBy verifies the bulk-resolve REST path
// stamps decided_by/decided_at on the created task_permission — one of the
// three resolve paths that used to leave both columns unset.
func TestBulkResolve_Granted_RecordsDecidedBy(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "bulk-decided-by", "Bash", "grep -r*")

	body := `{"taskId":"` + taskID + `","outcome":"granted","permissionIds":["` + reqID + `"]}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	perms, err := repo.NewPermissionRepo(client).ListEffectiveTaskPermissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(perms) == 0 {
		t.Fatal("expected a task_permission after granted bulk resolve, got none")
	}
	// "user-1" is the Sub withAuth signs into the request's JWT — pinning the
	// specific value, not just non-emptiness, is what proves the real caller
	// identity was threaded through rather than a role literal.
	if perms[0].DecidedBy == nil || *perms[0].DecidedBy != "user-1" {
		t.Errorf("expected decided_by=%q, got %v", "user-1", perms[0].DecidedBy)
	}
	if perms[0].DecidedAt == nil {
		t.Error("expected decided_at to be recorded, got nil")
	}
}

// TestBulkResolve_Denied_DoesNotCreateTaskPermission verifies that resolving a
// request as "denied" does NOT create a task_permission.
func TestBulkResolve_Denied_DoesNotCreateTaskPermission(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "deny-no-perm", "Bash", "grep -r*")

	body := `{"taskId":"` + taskID + `","outcome":"denied","permissionIds":["` + reqID + `"]}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	perms, err := repo.NewPermissionRepo(client).ListEffectiveTaskPermissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(perms) != 0 {
		t.Errorf("expected no task_permissions after denied resolve, got %d", len(perms))
	}
}

// TestSingleResolve_Granted_CreatesTaskPermission verifies the single-resolve
// path also creates a task_permission when the outcome is "granted".
func TestSingleResolve_Granted_CreatesTaskPermission(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "single-grant-perm", "Read", "")

	url := "/api/tasks/" + taskID + "/permission-requests/" + reqID + "/resolve"
	req := withAuth(t, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"outcome":"granted"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	perms, err := repo.NewPermissionRepo(client).ListEffectiveTaskPermissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	found := false
	for _, p := range perms {
		if p.Tool == "Read" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("task_permission for Read not found after single granted resolve; got %+v", perms)
	}
}

// TestSingleResolve_Granted_RecordsDecidedBy verifies the single-resolve REST
// path stamps decided_by/decided_at on the created task_permission.
func TestSingleResolve_Granted_RecordsDecidedBy(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "single-decided-by", "Read", "")

	url := "/api/tasks/" + taskID + "/permission-requests/" + reqID + "/resolve"
	req := withAuth(t, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"outcome":"granted"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	perms, err := repo.NewPermissionRepo(client).ListEffectiveTaskPermissions(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list permissions: %v", err)
	}
	if len(perms) == 0 {
		t.Fatal("expected a task_permission after granted single resolve, got none")
	}
	if perms[0].DecidedBy == nil || *perms[0].DecidedBy != "user-1" {
		t.Errorf("expected decided_by=%q, got %v", "user-1", perms[0].DecidedBy)
	}
	if perms[0].DecidedAt == nil {
		t.Error("expected decided_at to be recorded, got nil")
	}
}

// The single-resolve route nests {reqID} under {taskId}; resolving a request
// that belongs to a different task must 404 and leave the request pending.
func TestSingleResolve_RejectsCrossTaskRequest(t *testing.T) {
	client, r := newRetryHandler(t, &captureOrchestrator{})
	taskA, _, _ := seedPendingPermission(t, client, "authz-task-a2")
	_, runB, reqB := seedPendingPermission(t, client, "authz-task-b2")

	url := "/api/tasks/" + taskA + "/permission-requests/" + reqB + "/resolve"
	req := withAuth(t, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"outcome":"granted"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-task resolve, got %d: %s", w.Code, w.Body.String())
	}
	if n, err := repo.NewPermissionRepo(client).CountForStageRun(context.Background(), runB); err != nil || n != 1 {
		t.Errorf("task B permission must remain pending (count=%d, err=%v)", n, err)
	}
}
