package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// fakeStageRunRepo is a minimal repo.StageRunRepo stub that records the last
// Update call, so tests can assert the exact UpdateStageRunInput a composite
// method produces without a DB round trip.
type fakeStageRunRepo struct {
	updateCalls int
	lastID      string
	lastInput   repo.UpdateStageRunInput
}

func (f *fakeStageRunRepo) Create(_ context.Context, _ repo.CreateStageRunInput) (*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) GetByID(_ context.Context, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) GetBySessionID(_ context.Context, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) ListBySessionIDs(_ context.Context, _ []string) ([]*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) GetLatestForTask(_ context.Context, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) GetLatestByTaskAndStage(_ context.Context, _, _ string) (*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) GetByTaskStageIteration(_ context.Context, _, _ string, _ int) (*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) ListForTask(_ context.Context, _ string) ([]*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) ListStageRunsByTaskIDs(_ context.Context, _ []string) (map[string][]*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) ListByStatus(_ context.Context, _ ...string) ([]*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) ListPending(_ context.Context) ([]*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) Update(_ context.Context, id string, input repo.UpdateStageRunInput) (*ent.StageRun, error) {
	f.updateCalls++
	f.lastID = id
	f.lastInput = input
	return nil, nil
}
func (f *fakeStageRunRepo) SumCompletedCostCents(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeStageRunRepo) SumCompletedTokens(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeStageRunRepo) GetLatestForTasks(_ context.Context, _ []string) (map[string]*ent.StageRun, error) {
	return nil, nil
}
func (f *fakeStageRunRepo) ListInWindow(_ context.Context, _, _ time.Time) ([]*ent.StageRun, error) {
	return nil, nil
}

func TestStageRunService_MarkPending_SetsPendingAndClearsPID(t *testing.T) {
	repoFake := &fakeStageRunRepo{}
	s := newStageRunService(repoFake, nil, nil)
	ctx := context.Background()

	_, _ = s.MarkPending(ctx, "run-1")

	if repoFake.updateCalls != 1 {
		t.Fatalf("Update called %d times, want 1", repoFake.updateCalls)
	}
	if repoFake.lastID != "run-1" {
		t.Fatalf("got id %q, want run-1", repoFake.lastID)
	}
	got := repoFake.lastInput
	if got.Status == nil || *got.Status != "pending" {
		t.Fatalf("got Status %v, want pending", got.Status)
	}
	if !got.PIDClear {
		t.Fatal("got PIDClear false, want true")
	}
	if got.EndedAt != nil || got.Output != nil {
		t.Fatalf("MarkPending must not set EndedAt/Output, got EndedAt=%v Output=%v", got.EndedAt, got.Output)
	}
}

func TestStageRunService_MarkFailed_SetsFailedEndedAtAndOutput(t *testing.T) {
	repoFake := &fakeStageRunRepo{}
	s := newStageRunService(repoFake, nil, nil)
	ctx := context.Background()
	output := map[string]any{"error": "boom"}

	before := time.Now()
	_, _ = s.MarkFailed(ctx, "run-2", output)
	after := time.Now()

	if repoFake.updateCalls != 1 {
		t.Fatalf("Update called %d times, want 1", repoFake.updateCalls)
	}
	if repoFake.lastID != "run-2" {
		t.Fatalf("got id %q, want run-2", repoFake.lastID)
	}
	got := repoFake.lastInput
	if got.Status == nil || *got.Status != "failed" {
		t.Fatalf("got Status %v, want failed", got.Status)
	}
	if got.EndedAt == nil || got.EndedAt.Before(before) || got.EndedAt.After(after) {
		t.Fatalf("got EndedAt %v, want a timestamp between %v and %v", got.EndedAt, before, after)
	}
	if got.PIDClear {
		t.Fatal("got PIDClear true, want false")
	}
	if len(got.Output) != 1 || got.Output["error"] != "boom" {
		t.Fatalf("got Output %v, want %v", got.Output, output)
	}
}
