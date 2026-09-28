package tasks

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
)

type stubRefineReader struct{ status, errMsg string }

func (s stubRefineReader) State(string) (string, string) { return s.status, s.errMsg }

func TestApplyRefineStatus_SetsFields(t *testing.T) {
	h := &Handler{refineReader: stubRefineReader{status: "refining"}}
	e := &EnrichedTask{}
	h.applyRefineStatus(e, "t")
	if e.RefineStatus == nil || *e.RefineStatus != "refining" {
		t.Fatalf("expected RefineStatus=refining, got %v", e.RefineStatus)
	}
	if e.RefineError != nil {
		t.Fatalf("expected RefineError=nil, got %v", e.RefineError)
	}
}

func TestApplyRefineStatus_SetsErrorField(t *testing.T) {
	h := &Handler{refineReader: stubRefineReader{status: "failed", errMsg: "context deadline exceeded"}}
	e := &EnrichedTask{}
	h.applyRefineStatus(e, "t")
	if e.RefineStatus == nil || *e.RefineStatus != "failed" {
		t.Fatalf("expected RefineStatus=failed, got %v", e.RefineStatus)
	}
	if e.RefineError == nil || *e.RefineError != "context deadline exceeded" {
		t.Fatalf("expected RefineError=context deadline exceeded, got %v", e.RefineError)
	}
}

func TestApplyRefineStatus_NilReaderNoPanic(t *testing.T) {
	h := &Handler{}
	h.applyRefineStatus(&EnrichedTask{}, "t")
	// must not panic
}

func TestApplyRefineStatus_NilTaskNoPanic(t *testing.T) {
	h := &Handler{refineReader: stubRefineReader{status: "none"}}
	h.applyRefineStatus(nil, "t")
	// must not panic
}

// Regression: applyRefineStatus recomputes AvailableActions and must preserve
// the pending-permission count enrichOne resolved. Previously the recompute
// assumed zero pending perms, flipping the primary action away from
// approve_all_pending and defeating the stall rescue for blocked tasks.
func TestApplyRefineStatus_PreservesPendingPermsPrimary(t *testing.T) {
	h := &Handler{refineReader: stubRefineReader{status: "none"}}
	awaiting := "awaiting_user"
	e := &EnrichedTask{
		TaskResponse:         TaskResponse{CurrentStage: "implementation"},
		LatestStageRunStatus: &awaiting,
		NeedsUser:            true,
		pendingPermsCount:    3,
	}
	e.RecomputeAvailableActions()

	h.applyRefineStatus(e, "t")

	var primary string
	for _, a := range e.AvailableActions {
		if a.Primary {
			primary = a.Action
		}
	}
	if primary != taskcontrol.ActionApproveAllPending {
		t.Fatalf("expected primary=%q after applyRefineStatus, got %q", taskcontrol.ActionApproveAllPending, primary)
	}
}
