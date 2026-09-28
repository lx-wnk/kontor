package tasks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// notifierRecorder is a tasks.PermissionNotifier that records every call for
// assertion in tests.
type notifierRecorder struct {
	calls []string
}

func (n *notifierRecorder) PermissionRequested(_ context.Context, taskID, _ string, tool string) {
	n.calls = append(n.calls, taskID+":"+tool)
}

// newNotifyTestHandler mirrors newTestHandlerWithBroadcaster but adds a
// notifierRecorder so tests can assert on PermissionRequested calls.
func newNotifyTestHandler(t *testing.T, client *ent.Client, rec tasks.PermissionNotifier) *chi.Mux {
	t.Helper()
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)

	broadcaster := sse.NewTaskBroadcaster(sse.NewBroadcaster())

	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       srRepo,
		PermRepo:     permRepo,
		AuditRepo:    auditRepo,
		CfgRepo:      cfgRepo,
		Orchestrator: &noopOrchestrator{},
		Broadcaster:  broadcaster,
		Notifier:     rec,
	})

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	h.MountAgentIngress(r)
	return r
}

func mustCreateNotifyTask(t *testing.T, client *ent.Client, autonomy string) *ent.Task {
	t.Helper()
	taskRepo := repo.NewTaskRepo(client)
	a := autonomy
	tk, err := taskRepo.Create(testCtx(t), repo.CreateTaskInput{
		Slug:          "notify-" + autonomy,
		Title:         "Notify task",
		Cwd:           "/tmp",
		MaxIterations: 1,
		Priority:      "medium",
		CurrentStage:  "implementation",
		Autonomy:      &a,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk
}

func mustCreateNotifyStageRun(t *testing.T, client *ent.Client, taskID string) *ent.StageRun {
	t.Helper()
	srRepo := repo.NewStageRunRepo(client)
	sr, err := srRepo.Create(testCtx(t), repo.CreateStageRunInput{
		TaskID: taskID,
		Stage:  "implementation",
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	running := "running"
	sr, err = srRepo.Update(testCtx(t), sr.ID, repo.UpdateStageRunInput{Status: &running})
	if err != nil {
		t.Fatalf("mark stage run running: %v", err)
	}
	return sr
}

func postPermissionRequest(t *testing.T, r *chi.Mux, stageRunID, toolName, pattern string) {
	t.Helper()
	body := map[string]any{"stageRunId": stageRunID, "tool": toolName}
	if pattern != "" {
		body["pattern"] = pattern
	}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/permission-requests", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = withAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create permission request: got %d, body %s", rr.Code, rr.Body.String())
	}
}

// setApprovalPushPreference writes the notification preference that gates the
// approval push, via the same route the Settings panel uses.
func setApprovalPushPreference(t *testing.T, r *chi.Mux, enabled bool, channels []string) {
	t.Helper()
	body := map[string]any{"eventType": "approval_needed", "channels": channels, "enabled": enabled}
	b, _ := json.Marshal(body)
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/notifications/preferences/approval_needed", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("set approval push preference: got %d, body %s", rr.Code, rr.Body.String())
	}
}

func TestPermissionNotifier_ManualTaskGatedRequest_Notifies(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)
	setApprovalPushPreference(t, r, true, []string{"browser"})

	tk := mustCreateNotifyTask(t, bundle.Client, "manual")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	postPermissionRequest(t, r, sr.ID, "Bash", "make build")

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 notifier call, got %d: %v", len(rec.calls), rec.calls)
	}
	if rec.calls[0] != tk.ID+":Bash" {
		t.Errorf("unexpected call: %s", rec.calls[0])
	}
}

func TestPermissionNotifier_FullTaskAutoApprovedRequest_NoNotify(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)

	tk := mustCreateNotifyTask(t, bundle.Client, "full")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	postPermissionRequest(t, r, sr.ID, "Read", "")

	if len(rec.calls) != 0 {
		t.Fatalf("expected 0 notifier calls for auto-approved request, got %d: %v", len(rec.calls), rec.calls)
	}
}

func TestPermissionNotifier_FullTaskApplicationTool_Notifies(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)
	setApprovalPushPreference(t, r, true, []string{"browser"})

	tk := mustCreateNotifyTask(t, bundle.Client, "full")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	postPermissionRequest(t, r, sr.ID, "mcp__mail__imap_move_email", "")

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 notifier call for gated application tool, got %d: %v", len(rec.calls), rec.calls)
	}
	if rec.calls[0] != tk.ID+":mcp__mail__imap_move_email" {
		t.Errorf("unexpected call: %s", rec.calls[0])
	}
}

func TestPermissionNotifier_BulkFullTask_NotifiesOnlyForPendingEntries(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)
	setApprovalPushPreference(t, r, true, []string{"browser"})

	tk := mustCreateNotifyTask(t, bundle.Client, "full")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	b, _ := json.Marshal(map[string]any{
		"stageRunId": sr.ID,
		"entries": []map[string]any{
			{"tool": "Read"},
			{"tool": "mcp__mail__imap_move_email"},
		},
	})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk create: got %d, body %s", rr.Code, rr.Body.String())
	}

	if len(rec.calls) != 1 || rec.calls[0] != tk.ID+":mcp__mail__imap_move_email" {
		t.Fatalf("expected exactly one call for the pending application tool, got %v", rec.calls)
	}
}
func TestPermissionNotifier_NoPreference_NoPush(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)

	tk := mustCreateNotifyTask(t, bundle.Client, "manual")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	postPermissionRequest(t, r, sr.ID, "Bash", "make build")

	if len(rec.calls) != 0 {
		t.Fatalf("expected 0 notifier calls with no stored preference, got %d: %v", len(rec.calls), rec.calls)
	}
}

func TestPermissionNotifier_PreferenceWithoutBrowserChannel_NoPush(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)
	setApprovalPushPreference(t, r, true, []string{"webhook"})

	tk := mustCreateNotifyTask(t, bundle.Client, "manual")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	postPermissionRequest(t, r, sr.ID, "Bash", "make build")

	if len(rec.calls) != 0 {
		t.Fatalf("expected 0 notifier calls without the browser channel, got %d: %v", len(rec.calls), rec.calls)
	}
}

func TestPermissionNotifier_BulkWithoutPreference_NoPush(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	rec := &notifierRecorder{}
	r := newNotifyTestHandler(t, bundle.Client, rec)

	tk := mustCreateNotifyTask(t, bundle.Client, "full")
	sr := mustCreateNotifyStageRun(t, bundle.Client, tk.ID)

	b, _ := json.Marshal(map[string]any{
		"stageRunId": sr.ID,
		"entries":    []map[string]any{{"tool": "mcp__mail__imap_move_email"}},
	})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk", bytes.NewReader(b)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("bulk create: got %d, body %s", rr.Code, rr.Body.String())
	}

	if len(rec.calls) != 0 {
		t.Fatalf("expected no push without a stored preference, got %v", rec.calls)
	}
}
