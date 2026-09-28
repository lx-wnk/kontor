package tasks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// resumeRecorder records ResumeFromUser; every other orchestrator call is a no-op.
type resumeRecorder struct {
	*noopOrchestrator
	resumed bool
	prompt  string
}

func (r *resumeRecorder) ResumeFromUser(_ context.Context, _ string, userPrompt string) (*ent.StageRun, error) {
	r.resumed = true
	r.prompt = userPrompt
	return &ent.StageRun{ID: "resumed"}, nil
}

func TestSingleResolve_Denied_ResumesWithTheRefusal(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newRetryHandler(t, rec)
	taskID, _, reqID := seedPendingPermissionWithPattern(t, client, "deny-resumes", "WebFetch", "domain:example.com")

	url := "/api/tasks/" + taskID + "/permission-requests/" + reqID + "/resolve"
	req := withAuth(t, httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"outcome":"denied"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !rec.resumed {
		t.Fatal("a refused request must resume the run; otherwise it waits forever")
	}
	if !strings.Contains(rec.prompt, "WebFetch") {
		t.Errorf("resume prompt must name the refused tool, got %q", rec.prompt)
	}
}

func TestBulkResolve_Denied_ResumesWithTheRefusal(t *testing.T) {
	rec := &resumeRecorder{noopOrchestrator: &noopOrchestrator{}}
	client, r := newRetryHandler(t, rec)
	taskID, _, _ := seedPendingPermissionWithPattern(t, client, "bulk-deny-resumes", "WebFetch", "domain:example.com")

	body := `{"taskId":"` + taskID + `","outcome":"denied","all":true}`
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !rec.resumed || !strings.Contains(rec.prompt, "WebFetch") {
		t.Fatalf("bulk refusal must resume and name the tool; resumed=%v prompt=%q", rec.resumed, rec.prompt)
	}
}
