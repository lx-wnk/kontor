package pipeline_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// progressRecorder is a stage handler that records which tasks were progressed
// and completes them synchronously with a DoneTransition so the stage run lands
// in a terminal state that is easy to assert.
type progressRecorder struct {
	mu            sync.Mutex
	progressedIDs []string
}

func (r *progressRecorder) Stage() string       { return "implementation" }
func (r *progressRecorder) RequiresAgent() bool { return false }
func (r *progressRecorder) Execute(_ *pipeline.StageContext) (pipeline.StageTransition, error) {
	return pipeline.DoneTransition{}, nil
}

func (r *progressRecorder) record(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progressedIDs = append(r.progressedIDs, taskID)
}

// captureHandler records the task ID when Execute is called, then delegates to
// an inner transition so the orchestrator can complete the stage run normally.
type captureHandler struct {
	recorder *progressRecorder
	inner    pipeline.StageTransition
}

func (h *captureHandler) Stage() string       { return "implementation" }
func (h *captureHandler) RequiresAgent() bool { return false }
func (h *captureHandler) Execute(ctx *pipeline.StageContext) (pipeline.StageTransition, error) {
	h.recorder.record(ctx.Task.ID)
	return h.inner, nil
}

func newCaptureHandler(recorder *progressRecorder) pipeline.StageHandler {
	return &captureHandler{recorder: recorder, inner: pipeline.DoneTransition{}}
}

// makeDepPickerTask creates a task at "implementation" stage (returned by ListPickable)
// and returns its ID string. Named to avoid collision with makePickerTask in orchestrator_test.go.
func makeDepPickerTask(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, slug string) string {
	t.Helper()
	return makePickerTask(t, ctx, taskRepo, slug).ID
}

// TestPickerGate_BlockedUpstreamNotProgressed asserts that a downstream task is
// not picked when its upstream is still in "implementation" (not yet "done").
func TestPickerGate_BlockedUpstreamNotProgressed(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, _ := makeTestOrchestratorWithDeps(t)

	recorder := &progressRecorder{}
	orch.SetHandlerOverride("implementation", newCaptureHandler(recorder))

	upstreamID := makeDepPickerTask(t, ctx, taskRepo, "upstream-blocked")
	downstreamID := makeDepPickerTask(t, ctx, taskRepo, "downstream-blocked")

	// downstream waits for upstream to reach "done"
	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	// upstream is still "implementation" — dep is not satisfied
	orch.PickNextTasksForFreeSlots(ctx, nil)

	for _, id := range recorder.progressedIDs {
		require.NotEqual(t, downstreamID, id, "downstream must not be progressed while upstream is in progress")
	}
}

// TestPickerGate_SatisfiedUpstreamAllowsProgress asserts that a task with no
// upstream deps (or with a done upstream) is picked normally by the picker.
func TestPickerGate_SatisfiedUpstreamAllowsProgress(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, _ := makeTestOrchestratorWithDeps(t)

	recorder := &progressRecorder{}
	orch.SetHandlerOverride("implementation", newCaptureHandler(recorder))

	// upstream is moved to "done" stage before the dep row is created
	upstreamID := makeDepPickerTask(t, ctx, taskRepo, "upstream-done")
	_, err := taskRepo.Update(ctx, upstreamID, repo.UpdateTaskInput{CurrentStage: ptr("done")})
	require.NoError(t, err)

	downstreamID := makeDepPickerTask(t, ctx, taskRepo, "downstream-satisfied")
	_, err = depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	// independent task with no deps — must always be picked
	noDepsID := makeDepPickerTask(t, ctx, taskRepo, "no-deps-task")

	orch.PickNextTasksForFreeSlots(ctx, nil)

	require.Contains(t, recorder.progressedIDs, downstreamID, "downstream must be progressed when upstream is done")
	require.Contains(t, recorder.progressedIDs, noDepsID, "task with no deps must always be progressed")
}

// TestPickerGate_NilDepRepoSkipsGating verifies back-compat: the original helper
// (no DepRepo) uses a nil DepRepo, meaning the gate is never consulted and any
// pickable task is progressed regardless of what dep rows might exist in DB.
// Since makeTestOrchestratorWithRepos does not wire a DepRepo, we confirm the
// orchestrator runs the picker without error by verifying a simple task is progressed.
func TestPickerGate_NilDepRepoSkipsGating(t *testing.T) {
	ctx := context.Background()
	// Uses the original helper — DepRepo is nil.
	orch, taskRepo := makeTestOrchestratorWithRepos(t)

	recorder := &progressRecorder{}
	orch.SetHandlerOverride("implementation", newCaptureHandler(recorder))

	taskID := makeDepPickerTask(t, ctx, taskRepo, "nil-dep-repo-task")

	orch.PickNextTasksForFreeSlots(ctx, nil)

	require.Contains(t, recorder.progressedIDs, taskID, "task must be progressed when DepRepo is nil")
}
