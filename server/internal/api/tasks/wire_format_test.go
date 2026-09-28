package tasks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// decodeRows decodes a JSON array response into raw maps so assertions run
// against the actual wire keys. Decoding into a typed struct would pass
// regardless of the key casing the server emits.
func decodeRows(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, body)
	}
	return rows
}

func assertKeys(t *testing.T, row map[string]any, want, forbidden []string) {
	t.Helper()
	for _, k := range want {
		if _, ok := row[k]; !ok {
			t.Errorf("missing key %q in %v", k, row)
		}
	}
	for _, k := range forbidden {
		if _, ok := row[k]; ok {
			t.Errorf("unexpected key %q in %v", k, row)
		}
	}
}

// TestListPermissions_WireFormat asserts the camelCase keys the client declares
// in src/types.ts, and that no snake_case entity key leaks onto the wire.
func TestListPermissions_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "perm-wire-format",
		Title:         "Permission Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	permRepo := repo.NewPermissionRepo(client)
	pattern := "npm run *"
	if _, err = permRepo.CreateTaskPermission(testCtx(t), repo.CreateTaskPermissionInput{
		TaskID:    task.ID,
		Tool:      "Bash",
		Pattern:   &pattern,
		Granted:   true,
		DecidedBy: "user-1",
	}); err != nil {
		t.Fatalf("create granted permission: %v", err)
	}
	// Zero values: the entity's omitempty tags would drop granted and pattern
	// from the payload entirely instead of sending false and null.
	if _, err = permRepo.CreateTaskPermission(testCtx(t), repo.CreateTaskPermissionInput{
		TaskID:  task.ID,
		Tool:    "Write",
		Granted: false,
	}); err != nil {
		t.Fatalf("create denied permission: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/permissions", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	rows := decodeRows(t, w.Body.Bytes())
	if len(rows) != 2 {
		t.Fatalf("expected 2 permissions, got %d: %s", len(rows), w.Body.String())
	}

	byTool := map[string]map[string]any{}
	for _, row := range rows {
		tool, _ := row["tool"].(string)
		byTool[tool] = row
	}

	want := []string{"id", "taskId", "tool", "pattern", "granted", "decidedBy", "requestedAt", "decidedAt", "expiresAt"}
	forbidden := []string{"task_id", "requested_at", "decided_at", "decided_by", "expires_at", "manual_override", "edges"}
	for tool, row := range byTool {
		t.Run(tool, func(t *testing.T) {
			assertKeys(t, row, want, forbidden)
		})
	}

	granted := byTool["Bash"]
	if granted["taskId"] != task.ID {
		t.Errorf("taskId = %v, want %q", granted["taskId"], task.ID)
	}
	if granted["decidedBy"] != "user-1" {
		t.Errorf("decidedBy = %v, want %q", granted["decidedBy"], "user-1")
	}
	if granted["granted"] != true {
		t.Errorf("granted = %v, want true", granted["granted"])
	}
	if granted["requestedAt"] == "" || granted["requestedAt"] == nil {
		t.Errorf("requestedAt = %v, want an RFC3339 timestamp", granted["requestedAt"])
	}
	if granted["decidedAt"] == nil {
		t.Error("decidedAt = null, want a timestamp for a decided grant")
	}
	if granted["expiresAt"] != nil {
		t.Errorf("expiresAt = %v, want null", granted["expiresAt"])
	}

	denied := byTool["Write"]
	if denied["granted"] != false {
		t.Errorf("granted = %v, want false (not omitted)", denied["granted"])
	}
	if denied["pattern"] != nil {
		t.Errorf("pattern = %v, want null (not omitted)", denied["pattern"])
	}
	if denied["decidedBy"] != nil {
		t.Errorf("decidedBy = %v, want null", denied["decidedBy"])
	}
}

// TestGrantPermission_WireFormat asserts the single-grant response uses the same
// camelCase shape as the list, since the client types both as TaskPermission.
func TestGrantPermission_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "grant-wire-format",
		Title:         "Grant Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/permissions", strings.NewReader(`{"tool":"Write"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var row map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	assertKeys(t, row,
		[]string{"id", "taskId", "tool", "pattern", "granted", "decidedBy", "requestedAt", "decidedAt", "expiresAt"},
		[]string{"task_id", "requested_at", "decided_at", "decided_by", "expires_at", "manual_override", "edges"},
	)
}

// TestListAudit_WireFormat asserts the audit rows carry the keys AuditLogTab.vue
// reads (timestamp, details, actor), not the storage column names.
func TestListAudit_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "audit-wire-format",
		Title:         "Audit Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	auditRepo := repo.NewAuditEventRepo(client)
	if err = auditRepo.RecordTaskAudit(testCtx(t), task.ID, nil, "retry_requested", "task:"+task.ID,
		map[string]any{"actor": "user", "stage": "implementation"}); err != nil {
		t.Fatalf("record tagged audit: %v", err)
	}
	if err = auditRepo.RecordTaskAudit(testCtx(t), task.ID, nil, "stage_transition", "task:"+task.ID, nil); err != nil {
		t.Fatalf("record untagged audit: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/audit", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	rows := decodeRows(t, w.Body.Bytes())
	if len(rows) != 2 {
		t.Fatalf("expected 2 audit entries, got %d: %s", len(rows), w.Body.String())
	}

	byAction := map[string]map[string]any{}
	for _, row := range rows {
		assertKeys(t, row,
			[]string{"id", "taskId", "userId", "actor", "action", "target", "timestamp", "details"},
			[]string{"task_id", "user_id", "ts", "metadata", "edges"},
		)
		action, _ := row["action"].(string)
		byAction[action] = row
	}

	tagged := byAction["retry_requested"]
	if tagged["actor"] != "user" {
		t.Errorf("actor = %v, want %q from metadata", tagged["actor"], "user")
	}
	if tagged["taskId"] != task.ID {
		t.Errorf("taskId = %v, want %q", tagged["taskId"], task.ID)
	}
	if _, ok := tagged["details"].(map[string]any); !ok {
		t.Errorf("details = %v, want the metadata object", tagged["details"])
	}
	if tagged["timestamp"] == "" || tagged["timestamp"] == nil {
		t.Errorf("timestamp = %v, want an RFC3339 timestamp", tagged["timestamp"])
	}

	untagged := byAction["stage_transition"]
	if untagged["actor"] != "system" {
		t.Errorf("actor = %v, want %q for a write with no actor tag and no user", untagged["actor"], "system")
	}
	if untagged["details"] != nil {
		t.Errorf("details = %v, want null", untagged["details"])
	}
}

// TestListStageRuns_WireFormat asserts the camelCase keys src/types.ts declares
// for StageRun, and that a zero iteration/cost/token count is sent as 0 rather
// than dropped by the entity's omitempty tags.
func TestListStageRuns_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "stage-run-wire-format",
		Title:         "Stage Run Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	srRepo := repo.NewStageRunRepo(client)
	finished, err := srRepo.Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:      task.ID,
		Stage:       "implementation",
		Iteration:   1,
		SessionName: "impl-session",
	})
	if err != nil {
		t.Fatalf("create finished run: %v", err)
	}
	started := time.Now().Add(-time.Hour)
	ended := time.Now()
	done := "done"
	sessionID := "sess-1"
	tokens := 1234
	cost := 42
	if _, err = srRepo.Update(testCtx(t), finished.ID, repo.UpdateStageRunInput{
		Status:     &done,
		SessionID:  &sessionID,
		StartedAt:  &started,
		EndedAt:    &ended,
		TokensUsed: &tokens,
		CostCents:  &cost,
	}); err != nil {
		t.Fatalf("update finished run: %v", err)
	}
	// Zero values: iteration, tokensUsed and costCents are all 0 here, which the
	// entity's omitempty tags drop from the payload entirely.
	if _, err = srRepo.Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "self_review",
		Iteration: 0,
	}); err != nil {
		t.Fatalf("create pending run: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/tasks/"+task.ID+"/stage-runs", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	rows := decodeRows(t, w.Body.Bytes())
	if len(rows) != 2 {
		t.Fatalf("expected 2 stage runs, got %d: %s", len(rows), w.Body.String())
	}

	byStage := map[string]map[string]any{}
	for _, row := range rows {
		stage, _ := row["stage"].(string)
		byStage[stage] = row
		t.Run(stage, func(t *testing.T) {
			assertKeys(t, row, stageRunWant, stageRunForbidden)
		})
	}

	impl := byStage["implementation"]
	if impl["taskId"] != task.ID {
		t.Errorf("taskId = %v, want %q", impl["taskId"], task.ID)
	}
	if impl["sessionName"] != "impl-session" {
		t.Errorf("sessionName = %v, want %q", impl["sessionName"], "impl-session")
	}
	if impl["startedAt"] == nil {
		t.Error("startedAt = null, want a timestamp for a run that started")
	}
	if impl["endedAt"] == nil {
		t.Error("endedAt = null, want a timestamp for a finished run")
	}
	if impl["tokensUsed"] != float64(1234) {
		t.Errorf("tokensUsed = %v, want 1234", impl["tokensUsed"])
	}

	pending := byStage["self_review"]
	if pending["iteration"] != float64(0) {
		t.Errorf("iteration = %v, want 0 (not omitted)", pending["iteration"])
	}
	if pending["costCents"] != float64(0) {
		t.Errorf("costCents = %v, want 0 (not omitted)", pending["costCents"])
	}
	if pending["startedAt"] != nil {
		t.Errorf("startedAt = %v, want null", pending["startedAt"])
	}
	if pending["output"] != nil {
		t.Errorf("output = %v, want null (not omitted)", pending["output"])
	}
}

// TestListDependencies_WireFormat asserts the dependency rows carry the joined
// titles and the upstream task's current stage, which ent.TaskDependency stores
// no column for, under the camelCase names TaskDependenciesTab.vue reads.
func TestListDependencies_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	taskRepo := repo.NewTaskRepo(client)
	downstream, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "dep-wire-downstream",
		Title:         "Downstream Task",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "ready",
	})
	if err != nil {
		t.Fatalf("create downstream: %v", err)
	}
	upstream, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "dep-wire-upstream",
		Title:         "Upstream Task",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	if _, err = repo.NewDependencyRepo(client).Add(testCtx(t), downstream.ID, upstream.ID, "done", "on_hold"); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	want := []string{"id", "taskId", "taskTitle", "dependsOnId", "dependsOnTitle", "dependsOnStage", "requiredStage", "onCancelAction"}
	forbidden := []string{"task_id", "depends_on_id", "required_stage", "on_cancel_action", "created_at", "createdAt", "edges"}

	t.Run("upstream", func(t *testing.T) {
		req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/tasks/"+downstream.ID+"/dependencies", nil))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		rows := decodeRows(t, w.Body.Bytes())
		if len(rows) != 1 {
			t.Fatalf("expected 1 dependency, got %d: %s", len(rows), w.Body.String())
		}
		assertKeys(t, rows[0], want, forbidden)
		if rows[0]["dependsOnTitle"] != "Upstream Task" {
			t.Errorf("dependsOnTitle = %v, want %q", rows[0]["dependsOnTitle"], "Upstream Task")
		}
		if rows[0]["dependsOnStage"] != "implementation" {
			t.Errorf("dependsOnStage = %v, want the upstream task's current stage %q", rows[0]["dependsOnStage"], "implementation")
		}
		if rows[0]["requiredStage"] != "done" {
			t.Errorf("requiredStage = %v, want %q", rows[0]["requiredStage"], "done")
		}
		if rows[0]["onCancelAction"] != "on_hold" {
			t.Errorf("onCancelAction = %v, want %q", rows[0]["onCancelAction"], "on_hold")
		}
	})

	t.Run("downstream", func(t *testing.T) {
		req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/tasks/"+upstream.ID+"/dependents", nil))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		rows := decodeRows(t, w.Body.Bytes())
		if len(rows) != 1 {
			t.Fatalf("expected 1 dependent, got %d: %s", len(rows), w.Body.String())
		}
		assertKeys(t, rows[0], want, forbidden)
		if rows[0]["taskTitle"] != "Downstream Task" {
			t.Errorf("taskTitle = %v, want %q", rows[0]["taskTitle"], "Downstream Task")
		}
	})

	t.Run("create", func(t *testing.T) {
		third, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
			Slug:          "dep-wire-third",
			Title:         "Third Task",
			Cwd:           t.TempDir(),
			MaxIterations: 5,
			Priority:      "normal",
			CurrentStage:  "backlog",
		})
		if err != nil {
			t.Fatalf("create third: %v", err)
		}
		body := `{"dependsOnId":"` + third.ID + `","requiredStage":"done","onCancelAction":"cancel"}`
		req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+downstream.ID+"/dependencies", strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var row map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
			t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
		}
		assertKeys(t, row, want, forbidden)
		if row["dependsOnStage"] != "backlog" {
			t.Errorf("dependsOnStage = %v, want %q", row["dependsOnStage"], "backlog")
		}
	})
}

// TestTaskActions_WireFormat asserts that the three task-returning action routes
// answer camelCase and send false booleans rather than dropping them.
func TestTaskActions_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	taskRepo := repo.NewTaskRepo(client)

	want := []string{"id", "slug", "title", "description", "cwd", "worktreePath", "sourceBranch", "targetBranch", "currentStage", "priority", "autonomy", "userId", "parentTaskId", "projectId", "spawnerId", "maxIterations", "tokenBudget", "costBudgetCents", "stageTimeoutSeconds", "silverBullet", "planMode", "rank", "metadata", "createdAt", "updatedAt"}
	forbidden := []string{"worktree_path", "source_branch", "target_branch", "current_stage", "parent_task_id", "project_id", "spawner_id", "max_iterations", "token_budget", "cost_budget_cents", "stage_timeout_seconds", "silver_bullet", "plan_mode", "user_id", "created_at", "updated_at", "edges"}

	newTask := func(t *testing.T, slug string) string {
		t.Helper()
		task, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
			Slug:          slug,
			Title:         "Action Wire Format",
			Cwd:           t.TempDir(),
			MaxIterations: 5,
			Priority:      "normal",
			CurrentStage:  "implementation",
		})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		return task.ID
	}

	decodeOne := func(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var row map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
			t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
		}
		return row
	}

	t.Run("patch", func(t *testing.T) {
		id := newTask(t, "action-wire-patch")
		req := withAuth(t, httptest.NewRequest(http.MethodPatch, "/api/tasks/"+id, strings.NewReader(`{"title":"Renamed"}`)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		row := decodeOne(t, w)
		assertKeys(t, row, want, forbidden)
		if row["silverBullet"] != false {
			t.Errorf("silverBullet = %v, want false (not omitted)", row["silverBullet"])
		}
		if row["title"] != "Renamed" {
			t.Errorf("title = %v, want %q", row["title"], "Renamed")
		}
	})

	t.Run("cancel", func(t *testing.T) {
		id := newTask(t, "action-wire-cancel")
		req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+id+"/cancel", nil))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		row := decodeOne(t, w)
		assertKeys(t, row, want, forbidden)
		if row["currentStage"] != "cancelled" {
			t.Errorf("currentStage = %v, want %q", row["currentStage"], "cancelled")
		}
	})

	t.Run("hold", func(t *testing.T) {
		id := newTask(t, "action-wire-hold")
		req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+id+"/hold", nil))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		row := decodeOne(t, w)
		assertKeys(t, row, want, forbidden)
		if row["currentStage"] != "on_hold" {
			t.Errorf("currentStage = %v, want %q", row["currentStage"], "on_hold")
		}
	})
}

// TestPermissionWriteRoutes_WireFormat asserts the write routes answer the same
// shape as the list routes beside them, which feed the same TypeScript types.
func TestPermissionWriteRoutes_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "perm-write-wire-format",
		Title:         "Permission Write Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	t.Run("createPermissionRequest", func(t *testing.T) {
		body := `{"stageRunId":"` + run.ID + `","tool":"Bash","pattern":"ls -la"}`
		req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests", strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var row map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
			t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
		}
		assertKeys(t, row,
			[]string{"id", "stageRunId", "tool", "pattern", "reason", "outcome", "requestedAt", "resolvedAt", "outsideSafeList"},
			[]string{"stage_run_id", "requested_at", "resolved_at", "edges"},
		)
	})

	t.Run("bulkGrantPermissions", func(t *testing.T) {
		body := `{"permissions":[{"tool":"Write"},{"tool":"Read"}]}`
		req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/permissions/bulk", strings.NewReader(body)))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		rows := decodeRows(t, w.Body.Bytes())
		if len(rows) == 0 {
			t.Fatalf("expected granted permissions, got none: %s", w.Body.String())
		}
		for _, row := range rows {
			assertKeys(t, row,
				[]string{"id", "taskId", "tool", "pattern", "granted", "decidedBy", "requestedAt", "decidedAt", "expiresAt"},
				[]string{"task_id", "requested_at", "decided_at", "decided_by", "expires_at", "manual_override", "edges"},
			)
		}
	})
}

// TestCreatePermissionRequestGated_WireFormat asserts createPermissionRequest's
// second return site (permission_request_routes.go's gated/manual-autonomy
// fallthrough, reached when the owning task is not allow-all) answers the same
// camelCase shape as its auto-approve return site above it. The tasks
// newTestHandlerWithRepos creates default to spec_gated (allow-all per
// taskcontrol.IsAllowAll), so TestPermissionWriteRoutes_WireFormat's
// createPermissionRequest subtest only ever exercises the auto-approve
// return — this task needs manual autonomy to reach the fallthrough.
func TestCreatePermissionRequestGated_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	manual := "manual"
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "perm-write-wire-format-gated",
		Title:         "Permission Write Wire Format Gated",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
		Autonomy:      &manual,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	body := `{"stageRunId":"` + run.ID + `","tool":"Bash","pattern":"ls -la"}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	assertKeys(t, row,
		[]string{"id", "stageRunId", "tool", "pattern", "reason", "outcome", "requestedAt", "resolvedAt", "outsideSafeList"},
		[]string{"stage_run_id", "requested_at", "resolved_at", "edges"},
	)
	if row["outcome"] != nil {
		t.Errorf("outcome = %v, want null on the gated (non-auto-approved) path", row["outcome"])
	}
}

// stageRunOrchestrator returns a populated *ent.StageRun from every
// orchestrator call the stage-run action routes use, so tests reach the 2xx
// path instead of the 409 conflict a nil stage run would produce.
type stageRunOrchestrator struct{}

func (stageRunOrchestrator) ProgressTask(_ context.Context, taskID string, _ *pipeline.ProgressOpts) (*ent.StageRun, error) {
	return &ent.StageRun{ID: taskID + "-progress"}, nil
}
func (stageRunOrchestrator) ResumeFromUser(_ context.Context, taskID, _ string) (*ent.StageRun, error) {
	return &ent.StageRun{ID: taskID + "-resume"}, nil
}
func (stageRunOrchestrator) RequeueForUser(_ context.Context, taskID, _ string) (*ent.StageRun, error) {
	return &ent.StageRun{ID: taskID + "-requeue"}, nil
}
func (stageRunOrchestrator) NotifyTaskTerminated(_ context.Context, _, _ string)      {}
func (stageRunOrchestrator) InvalidateConfigCache()                                   {}
func (stageRunOrchestrator) ClearStalePendingPermissions(_ context.Context, _ string) {}
func (stageRunOrchestrator) EffectiveStageModel(_ context.Context, _ string) string   { return "" }

// newActionRouteHandler wires a handler whose orchestrator always returns a
// real stage run, needed by progress/retry/resume/resume-stage to reach their
// success path.
func newActionRouteHandler(t *testing.T) (*ent.Client, *chi.Mux) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	h := tasks.NewHandler(tasks.Deps{
		Client:       client,
		TaskRepo:     repo.NewTaskRepo(client),
		SRRepo:       repo.NewStageRunRepo(client),
		PermRepo:     repo.NewPermissionRepo(client),
		AuditRepo:    repo.NewAuditEventRepo(client),
		CfgRepo:      repo.NewPipelineConfigRepo(client),
		Orchestrator: stageRunOrchestrator{},
		Broadcaster:  sse.NewTaskBroadcaster(sse.NewBroadcaster()),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return client, r
}

// stageRunWant/stageRunForbidden are the full stage-run wire shape asserted by
// the list route (TestListStageRuns_WireFormat) and reused by every route that
// answers a single stage run, so a field dropped from stageRunResponse fails
// here rather than only in the list test. sessionName, startedAt and endedAt
// were the fields silently missing before this branch's fix; a narrower list
// here would let /progress, /retry, /resume and /resume-stage regress unnoticed.
var stageRunWant = []string{"id", "taskId", "stage", "sessionId", "sessionName", "pid", "status", "iteration", "output", "tokensUsed", "costCents", "startedAt", "endedAt", "lastGrantAt"}
var stageRunForbidden = []string{"task_id", "session_id", "session_name", "started_at", "ended_at", "tokens_used", "cost_cents", "last_grant_at", "created_at", "retry_count", "next_retry_at", "pending_user_prompt", "edges"}

// TestProgressRoute_WireFormat asserts /progress answers both halves of its
// map through ToTaskResponse and toStageRunResponse, not raw entities.
func TestProgressRoute_WireFormat(t *testing.T) {
	client, r := newActionRouteHandler(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "action-wire-progress",
		Title:         "Progress Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/progress", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	taskRow, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("expected task to be an object, got %T: %v", body["task"], body["task"])
	}
	assertKeys(t, taskRow,
		[]string{"id", "slug", "currentStage", "silverBullet", "planMode", "userId"},
		[]string{"current_stage", "silver_bullet", "plan_mode", "user_id", "edges"},
	)
	stageRunRow, ok := body["stageRun"].(map[string]any)
	if !ok {
		t.Fatalf("expected stageRun to be an object, got %T: %v", body["stageRun"], body["stageRun"])
	}
	assertKeys(t, stageRunRow, stageRunWant, stageRunForbidden)
}

// TestProgressRoute_NilTaskStaysNull asserts that a discarded GetByID error
// (task not found) still encodes "task" as JSON null rather than panicking
// ToTaskResponse on a nil *ent.Task.
func TestProgressRoute_NilTaskStaysNull(t *testing.T) {
	_, r := newActionRouteHandler(t)

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/does-not-exist/progress", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	if body["task"] != nil {
		t.Errorf("task = %v, want null for a missing task", body["task"])
	}
	if _, ok := body["stageRun"].(map[string]any); !ok {
		t.Fatalf("expected stageRun to still be present, got %T", body["stageRun"])
	}
}

// TestRetryRoute_WireFormat asserts /retry answers the same stage-run shape
// as the stage-run list route beside it.
func TestRetryRoute_WireFormat(t *testing.T) {
	client, r := newActionRouteHandler(t)
	taskID := seedFailedRun(t, client, t.TempDir(), "implementation", "", false)

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/retry", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	assertKeys(t, row, stageRunWant, stageRunForbidden)
}

// TestResumeRoute_WireFormat asserts /resume answers the same stage-run shape.
func TestResumeRoute_WireFormat(t *testing.T) {
	client, r := newActionRouteHandler(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "action-wire-resume",
		Title:         "Resume Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/resume", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	assertKeys(t, row, stageRunWant, stageRunForbidden)
}

// TestResumeStageRoute_WireFormat asserts /resume-stage answers the same
// stage-run shape.
func TestResumeStageRoute_WireFormat(t *testing.T) {
	client, r := newActionRouteHandler(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "action-wire-resume-stage",
		Title:         "Resume Stage Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/resume-stage", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	assertKeys(t, row, stageRunWant, stageRunForbidden)
}

// TestAdvanceRoute_WireFormat asserts /advance's retry primary answers the
// same stage-run DTO as /retry beside it, not a raw *ent.StageRun: the
// dedicated route already converted, but Advance assigned sr straight into
// AdvanceResult.Result, so a retry dispatched through /advance sent
// created_at, an empty edges object, and dropped taskId/startedAt/costCents
// under the entity's own omitempty tags.
func TestAdvanceRoute_WireFormat(t *testing.T) {
	client, r := newActionRouteHandler(t)
	taskID := seedFailedRun(t, client, t.TempDir(), "implementation", "", false)

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/advance", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	if body["primary"] != "retry" {
		t.Fatalf("expected primary=retry, got %v", body["primary"])
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result to be an object, got %T: %v", body["result"], body["result"])
	}
	assertKeys(t, result, []string{"taskId", "startedAt", "costCents"}, []string{"edges", "created_at"})
}

// TestAdvanceApproveAllPending_WireFormat asserts /advance's
// approve_all_pending primary answers the same lowercase shape as the
// dedicated /approve-all-pending route and the MCP tool: ApproveAllPending's
// result struct carried no json tags, so /advance (which embeds the struct
// directly) answered PascalCase Go field names while the other two transports
// hand-mapped the same two fields to lowercase.
func TestAdvanceApproveAllPending_WireFormat(t *testing.T) {
	client, r := newActionRouteHandler(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "action-wire-advance-approve-all",
		Title:         "Advance Approve All Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	srRepo := repo.NewStageRunRepo(client)
	run, err := srRepo.Create(testCtx(t), repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation", Iteration: 0})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	awaiting := "awaiting_user"
	if _, err := srRepo.Update(testCtx(t), run.ID, repo.UpdateStageRunInput{Status: &awaiting}); err != nil {
		t.Fatalf("update run: %v", err)
	}
	pattern := "echo hi"
	if _, err := repo.NewPermissionRepo(client).CreatePermissionRequest(testCtx(t), repo.CreatePermissionRequestInput{
		StageRunID: run.ID,
		Tool:       "Bash",
		Pattern:    &pattern,
	}); err != nil {
		t.Fatalf("create perm request: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/advance", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	if body["primary"] != "approve_all_pending" {
		t.Fatalf("expected primary=approve_all_pending, got %v", body["primary"])
	}
	result, ok := body["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result to be an object, got %T: %v", body["result"], body["result"])
	}
	assertKeys(t, result, []string{"approved", "requeued"}, []string{"Approved", "Requeued"})
}

// TestResolvePermissionRequest_WireFormat asserts the resolve route answers
// the same permission-request shape as the list route beside it.
func TestResolvePermissionRequest_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "action-wire-resolve",
		Title:         "Resolve Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := repo.NewStageRunRepo(client).Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	preq, err := repo.NewPermissionRepo(client).CreatePermissionRequest(testCtx(t), repo.CreatePermissionRequestInput{
		StageRunID: run.ID,
		Tool:       "Bash",
	})
	if err != nil {
		t.Fatalf("create permission request: %v", err)
	}

	body := `{"outcome":"denied"}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/tasks/"+task.ID+"/permission-requests/"+preq.ID+"/resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var row map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	assertKeys(t, row,
		[]string{"id", "stageRunId", "tool", "pattern", "reason", "outcome", "requestedAt", "resolvedAt", "outsideSafeList"},
		[]string{"stage_run_id", "requested_at", "resolved_at", "edges"},
	)
}

// TestExportTasksJSON_WireFormat asserts the downloaded JSON export carries the
// same camelCase field names as the API, including inside the nested stage runs.
func TestExportTasksJSON_WireFormat(t *testing.T) {
	client, r := newTestHandlerWithRepos(t)
	task, err := repo.NewTaskRepo(client).Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "export-wire-format",
		Title:         "Export Wire Format",
		Cwd:           t.TempDir(),
		MaxIterations: 5,
		Priority:      "normal",
		CurrentStage:  "implementation",
		UserID:        strPtr(t, "user-1"),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err = repo.NewStageRunRepo(client).Create(testCtx(t), repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	}); err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/tasks/export?format=json", nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	rows := decodeRows(t, w.Body.Bytes())
	if len(rows) != 1 {
		t.Fatalf("expected 1 exported task, got %d: %s", len(rows), w.Body.String())
	}
	assertKeys(t, rows[0],
		[]string{"id", "slug", "title", "currentStage", "maxIterations", "silverBullet", "createdAt", "stageRuns"},
		[]string{"current_stage", "max_iterations", "silver_bullet", "created_at", "edges"},
	)

	runs, ok := rows[0]["stageRuns"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("stageRuns = %v, want one nested run", rows[0]["stageRuns"])
	}
	assertKeys(t, runs[0].(map[string]any),
		[]string{"id", "taskId", "stage", "status", "iteration", "tokensUsed", "costCents", "startedAt", "endedAt"},
		[]string{"task_id", "tokens_used", "cost_cents", "started_at", "ended_at", "edges"},
	)
}
