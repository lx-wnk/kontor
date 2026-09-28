package refine_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/refine"
)

func TestInjectConceptEndpoint_ReturnsStatusDraftReady(t *testing.T) {
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-rest"))
	r := makeRouter(turns, tasks, noopSpawner)

	body, _ := json.Marshal(map[string]any{
		"spec":         "integrate payments",
		"plan":         []string{"step A", "step B"},
		"toolRequests": []string{"Bash", "Edit"},
		"refinedTitle": "Payment Integration",
		"sourceBranch": "feat/payments",
	})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-rest/concept", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["status"] != refine.StatusDraftReady {
		t.Errorf("status = %q, want %q", resp["status"], refine.StatusDraftReady)
	}
}

func TestInjectConceptEndpoint_MissingSpecReturns400(t *testing.T) {
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-rest-bad"))
	r := makeRouter(turns, tasks, noopSpawner)

	body, _ := json.Marshal(map[string]any{"plan": []string{"step A"}})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-rest-bad/concept", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

// TestInjectConceptEndpoint_ThenConfirmFreezesMetadata is a REST-layer end-to-end:
// inject via REST → confirm via REST → task metadata populated.
func TestInjectConceptEndpoint_ThenConfirmFreezesMetadata(t *testing.T) {
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-rest-e2e"))
	r := makeRouter(turns, tasks, noopSpawner)

	// 1. Inject the concept.
	injectBody, _ := json.Marshal(map[string]any{
		"spec":         "implement caching layer",
		"refinedTitle": "Cache Layer",
	})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-rest-e2e/concept", bytes.NewReader(injectBody)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("inject: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// 2. Confirm.
	req2 := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-rest-e2e/confirm", nil))
	rr2 := httptest.NewRecorder()
	r.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("confirm: want 200, got %d: %s", rr2.Code, rr2.Body.String())
	}

	// 3. Task metadata must contain spec.
	up := tasks.lastUpdate
	if up == nil {
		t.Fatal("expected task update from confirm")
	}
	if up.Metadata == nil || up.Metadata["spec"] != "implement caching layer" {
		t.Errorf("Metadata.spec = %v, want %q", up.Metadata["spec"], "implement caching layer")
	}
	if up.Title == nil || *up.Title != "Cache Layer" {
		t.Errorf("Title = %v, want %q", up.Title, "Cache Layer")
	}
}
