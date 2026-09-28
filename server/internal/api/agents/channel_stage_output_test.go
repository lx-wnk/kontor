package agents_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/agents"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// fakeStageRunRepo implements repo.StageRunRepo with only GetByID and Update wired.
type fakeStageRunRepo struct {
	repo.StageRunRepo
	run           *ent.StageRun
	getErr        error
	capturedInput *repo.UpdateStageRunInput
}

// GetByID matches on id so a lookup for a run the fake does not hold returns
// not-found, distinguishing "wrong run" from "this run, but an injected error".
func (f *fakeStageRunRepo) GetByID(_ context.Context, id string) (*ent.StageRun, error) {
	if f.run != nil && f.run.ID == id {
		return f.run, f.getErr
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	return nil, errors.New("stage run not found")
}

func (f *fakeStageRunRepo) Update(_ context.Context, _ string, in repo.UpdateStageRunInput) (*ent.StageRun, error) {
	f.capturedInput = &in
	return f.run, nil
}

// fakeApiKeyRepo implements repo.ApiKeyRepo; GetByHash returns a valid key or error.
// A zero-value kind (the unset default in tests that only set `valid`) must behave
// like repo.ApiKeyKindUser, matching repo.Create's normalisation of "".
type fakeApiKeyRepo struct {
	repo.ApiKeyRepo
	valid      bool
	kind       string
	stageRunID string
}

func (f *fakeApiKeyRepo) GetByHash(_ context.Context, _ string) (*ent.ApiKey, error) {
	if f.valid {
		return &ent.ApiKey{Kind: f.kind, StageRunID: f.stageRunID}, nil
	}
	return nil, errors.New("invalid token")
}

func TestChannelStageOutput_ValidImplementation_Persists(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-1", Stage: "implementation", Status: "running"},
	}
	fakeKeys := &fakeApiKeyRepo{valid: true}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-1",
		"output": map[string]any{
			"summary":   "did it",
			"commits":   []any{"abc"},
			"openItems": []any{},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if fake.capturedInput == nil {
		t.Fatal("Update was not called")
	}
	if fake.capturedInput.Output["summary"] != "did it" {
		t.Errorf("unexpected output: %v", fake.capturedInput.Output)
	}
}

// TestChannelStageOutput_KeyScoping pins the fix for a stage-run key that
// could submit output for ANY stage run, not just its own: only the matching
// stage_run key, or an operator (user) key, may write.
func TestChannelStageOutput_KeyScoping(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		stageRunID string
		wantStatus int
	}{
		{"stage_run key for its own run", repo.ApiKeyKindStageRun, "run-1", http.StatusOK},
		{"stage_run key for another run", repo.ApiKeyKindStageRun, "run-2", http.StatusForbidden},
		{"user key", repo.ApiKeyKindUser, "", http.StatusOK},
		{"legacy empty kind behaves like a user key", "", "", http.StatusOK},
		{"kontor_session key", repo.ApiKeyKindKontorSession, "", http.StatusForbidden},
		{"module key", repo.ApiKeyKindModule, "", http.StatusForbidden},
		{"a future kind not yet in the switch", "other", "", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeStageRunRepo{
				run: &ent.StageRun{ID: "run-1", Stage: "implementation", Status: "running"},
			}
			fakeKeys := &fakeApiKeyRepo{valid: true, kind: tt.kind, stageRunID: tt.stageRunID}

			h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

			body, _ := json.Marshal(map[string]any{
				"stageRunId": "run-1",
				"output": map[string]any{
					"summary":   "did it",
					"commits":   []any{"abc"},
					"openItems": []any{},
				},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer some-token")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.Post(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}
			if tt.wantStatus == http.StatusOK && fake.capturedInput == nil {
				t.Error("Update should have been called")
			}
			if tt.wantStatus != http.StatusOK && fake.capturedInput != nil {
				t.Error("Update should not have been called")
			}
			if tt.wantStatus == http.StatusForbidden {
				const wantBody = `{"error":"key not issued for this stage run"}`
				if got := strings.TrimSpace(w.Body.String()); got != wantBody {
					t.Errorf("expected body %s, got %s", wantBody, got)
				}
			}
		})
	}
}

// TestChannelStageOutput_KeyScoping_PrecedesLookup pins that a stage_run key
// is checked against its own scope before the stage run is even looked up:
// a key for run-1 posting to a run the repo does not have gets 403 for the
// scope mismatch, not 404 for the missing run.
func TestChannelStageOutput_KeyScoping_PrecedesLookup(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-1", Stage: "implementation", Status: "running"},
	}
	fakeKeys := &fakeApiKeyRepo{valid: true, kind: repo.ApiKeyKindStageRun, stageRunID: "run-1"}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-404",
		"output":     map[string]any{"summary": "did it", "commits": []any{"abc"}, "openItems": []any{}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer some-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (scope checked before lookup), got %d: %s", w.Code, w.Body.String())
	}
	if fake.capturedInput != nil {
		t.Error("Update should not have been called")
	}
}

func TestChannelStageOutput_BadToken_401(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-1", Stage: "implementation", Status: "running"},
	}
	fakeKeys := &fakeApiKeyRepo{valid: false}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-1",
		"output":     map[string]any{"summary": "did it"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if fake.capturedInput != nil {
		t.Error("Update should not have been called")
	}
}

func TestChannelStageOutput_SchemaInvalid_422(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-2", Stage: "self_review", Status: "running"},
	}
	fakeKeys := &fakeApiKeyRepo{valid: true}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	// empty output is missing required self_review fields
	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-2",
		"output":     map[string]any{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	if fake.capturedInput != nil {
		t.Error("Update should not have been called")
	}
}

func TestChannelStageOutput_UnknownStageRun_404(t *testing.T) {
	fake := &fakeStageRunRepo{
		getErr: context.DeadlineExceeded, // any non-nil error
	}
	// auth runs first; valid token so the handler proceeds to GetByID and 404s
	fakeKeys := &fakeApiKeyRepo{valid: true}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "no-such-run",
		"output":     map[string]any{"x": 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sometoken")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestChannelStageOutput_BadTokenUnknownRun_401(t *testing.T) {
	fake := &fakeStageRunRepo{} // GetByID must not be reached
	fakeKeys := &fakeApiKeyRepo{valid: false}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "any-run-id",
		"output":     map[string]any{"x": 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer bad-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	// auth fires before lookup — Update must not be called either
	if fake.capturedInput != nil {
		t.Error("Update should not have been called")
	}
}

func TestChannelStageOutput_TerminalRun_409(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-done", Stage: "implementation", Status: "done"},
	}
	fakeKeys := &fakeApiKeyRepo{valid: true}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-done",
		"output": map[string]any{
			"summary":   "done",
			"commits":   []any{"abc"},
			"openItems": []any{},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if fake.capturedInput != nil {
		t.Error("Update should not have been called for a terminal run")
	}
}

func TestChannelStageOutput_WrongTypes_400(t *testing.T) {
	fake := &fakeStageRunRepo{}
	fakeKeys := &fakeApiKeyRepo{valid: true}

	h := agents.NewChannelStageOutputHandler(fake, fakeKeys, nil)

	// output is an array, not an object — wrong type
	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-1",
		"output":     []any{"not", "an", "object"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if fake.capturedInput != nil {
		t.Error("Update should not have been called")
	}
}

// recordedAudits captures RecordTaskAudit calls.
type recordedAudits struct {
	repo.AuditEventRepo
	actions  []string
	taskIDs  []string
	targets  []string
	metadata []map[string]any
	err      error
}

func (r *recordedAudits) RecordTaskAudit(_ context.Context, taskID string, _ *string, action, target string, metadata map[string]any) error {
	r.actions = append(r.actions, action)
	r.taskIDs = append(r.taskIDs, taskID)
	r.targets = append(r.targets, target)
	r.metadata = append(r.metadata, metadata)
	return r.err
}

// TestChannelStageOutput_AcceptedOutput_RecordsTheToolChannel pins the only
// signal that separates the two channels a stage result can arrive on. The
// set_stage_output tool writes stage_runs.output directly; a transcript scrape
// writes the same column, so afterwards the two are indistinguishable. Without
// this event there is no way to measure how often the tool channel works, which
// is what the "agent did not produce a json output block" failures hinge on.
func TestChannelStageOutput_AcceptedOutput_RecordsTheToolChannel(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-1", TaskID: "task-9", Stage: "implementation", Status: "running", Iteration: 2},
	}
	audits := &recordedAudits{}
	h := agents.NewChannelStageOutputHandler(fake, &fakeApiKeyRepo{valid: true}, audits)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-1",
		"output":     map[string]any{"summary": "did it", "commits": []any{"abc"}, "openItems": []any{}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(audits.actions) != 1 || audits.actions[0] != "stage_output_submitted" {
		t.Fatalf("expected one stage_output_submitted audit, got %v", audits.actions)
	}
	if audits.taskIDs[0] != "task-9" {
		t.Errorf("audit must be task-scoped so it is queryable per task, got taskID %q", audits.taskIDs[0])
	}
	if audits.targets[0] != "stage_run:run-1" {
		t.Errorf("audit must name the stage run it measures, got target %q", audits.targets[0])
	}
	if audits.metadata[0]["stage"] != "implementation" {
		t.Errorf("the channel split is counted per stage, so stage must be recorded, got %v", audits.metadata[0])
	}
}

// TestChannelStageOutput_RejectedOutput_RecordsNothing keeps the event honest:
// counting a rejected submission as a tool-channel success would overstate the
// channel that the measurement exists to evaluate.
func TestChannelStageOutput_RejectedOutput_RecordsNothing(t *testing.T) {
	// self_review, because ValidateStageOutput only has a schema for self_review
	// and finalization — every other stage falls through to OK, so an
	// implementation run cannot produce the rejection this test needs.
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-1", TaskID: "task-9", Stage: "self_review", Status: "running"},
	}
	audits := &recordedAudits{}
	h := agents.NewChannelStageOutputHandler(fake, &fakeApiKeyRepo{valid: true}, audits)

	body, _ := json.Marshal(map[string]any{"stageRunId": "run-1", "output": map[string]any{"summary": "no passed field"}})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
	if len(audits.actions) != 0 {
		t.Fatalf("a rejected output must record no submission, got %v", audits.actions)
	}
}

// TestChannelStageOutput_AuditFailure_StillAccepts: the measurement must never
// cost an accepted stage result. An audit write that fails is logged, not
// returned.
func TestChannelStageOutput_AuditFailure_StillAccepts(t *testing.T) {
	fake := &fakeStageRunRepo{
		run: &ent.StageRun{ID: "run-1", TaskID: "task-9", Stage: "implementation", Status: "running"},
	}
	audits := &recordedAudits{err: errors.New("audit table is on fire")}
	h := agents.NewChannelStageOutputHandler(fake, &fakeApiKeyRepo{valid: true}, audits)

	body, _ := json.Marshal(map[string]any{
		"stageRunId": "run-1",
		"output":     map[string]any{"summary": "did it", "commits": []any{"abc"}, "openItems": []any{}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/channel-stage-output", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()

	h.Post(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("a failed audit write must not fail the submission, got %d: %s", w.Code, w.Body.String())
	}
	if fake.capturedInput == nil {
		t.Fatal("the output must still be persisted")
	}
}
