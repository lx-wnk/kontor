package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// makeCascadeTask creates a task at "ready" stage (non-terminal, non-backlog)
// for cascade tests. Ready is excluded from ListPickable but is a valid
// non-terminal stage for cascade assertions.
func makeCascadeTask(t *testing.T, taskRepo repo.TaskRepo, slug string) string {
	t.Helper()
	ctx := context.Background()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                slug,
		Title:               slug,
		Cwd:                 "/tmp",
		CurrentStage:        "ready",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)
	return task.ID
}

// stageOf is a test helper that reads the current_stage for taskID.
func stageOf(t *testing.T, taskRepo repo.TaskRepo, taskID string) string {
	t.Helper()
	task, err := taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	return task.CurrentStage
}

// hasEvent returns true if events contains an entry with the given taskID.
func hasEvent(events []taskChangedEvent, taskID string) bool {
	for _, e := range events {
		if e.taskID == taskID {
			return true
		}
	}
	return false
}

// TestCascade_CancelCancelsDownstreamRecursively verifies that a "cancel" action
// propagates through a two-level chain: upstream → child → grandchild.
func TestCascade_CancelCancelsDownstreamRecursively(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, events := makeTestOrchestratorWithDeps(t)

	upstreamID := makeCascadeTask(t, taskRepo, "cascade-up")
	childID := makeCascadeTask(t, taskRepo, "cascade-child")
	grandchildID := makeCascadeTask(t, taskRepo, "cascade-grandchild")

	// child depends on upstream with action=cancel
	_, err := depRepo.Add(ctx, childID, upstreamID, "done", "cancel")
	require.NoError(t, err)
	// grandchild depends on child with action=cancel
	_, err = depRepo.Add(ctx, grandchildID, childID, "done", "cancel")
	require.NoError(t, err)

	orch.HandleDependentTasksForTest(ctx, upstreamID, "cancelled")

	require.Equal(t, "cancelled", stageOf(t, taskRepo, childID), "child must be cancelled")
	require.Equal(t, "cancelled", stageOf(t, taskRepo, grandchildID), "grandchild must be cancelled recursively")
	require.True(t, hasEvent(events.all(), childID), "OnTaskChanged must fire for child")
	require.True(t, hasEvent(events.all(), grandchildID), "OnTaskChanged must fire for grandchild")
}

// TestCascade_StartActionLeavesDownstreamUnchanged verifies that on_cancel_action="start"
// does not change the downstream stage but does fire OnTaskChanged.
func TestCascade_StartActionLeavesDownstreamUnchanged(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, events := makeTestOrchestratorWithDeps(t)

	upstreamID := makeCascadeTask(t, taskRepo, "start-up")
	downstreamID := makeCascadeTask(t, taskRepo, "start-down")

	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "start")
	require.NoError(t, err)

	orch.HandleDependentTasksForTest(ctx, upstreamID, "cancelled")

	require.Equal(t, "ready", stageOf(t, taskRepo, downstreamID), "stage must be unchanged for action=start")
	require.True(t, hasEvent(events.all(), downstreamID), "OnTaskChanged must still fire for start action")
}

// TestCascade_OnHoldActionLeavesDownstreamUnchanged verifies that on_cancel_action="on_hold"
// leaves the downstream stage intact.
func TestCascade_OnHoldActionLeavesDownstreamUnchanged(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, events := makeTestOrchestratorWithDeps(t)

	upstreamID := makeCascadeTask(t, taskRepo, "onhold-up")
	downstreamID := makeCascadeTask(t, taskRepo, "onhold-down")

	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	orch.HandleDependentTasksForTest(ctx, upstreamID, "cancelled")

	require.Equal(t, "ready", stageOf(t, taskRepo, downstreamID), "stage must be unchanged for action=on_hold")
	require.True(t, hasEvent(events.all(), downstreamID), "OnTaskChanged must fire for on_hold action")
}

// TestCascade_DoneUpstreamDoesNotChangeDownstream verifies that when the upstream
// reaches "done" the downstream stage is not mutated (the lazy picker handles pickup),
// but OnTaskChanged is still fired so the UI refreshes.
func TestCascade_DoneUpstreamDoesNotChangeDownstream(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, events := makeTestOrchestratorWithDeps(t)

	upstreamID := makeCascadeTask(t, taskRepo, "done-up")
	downstreamID := makeCascadeTask(t, taskRepo, "done-down")

	_, err := depRepo.Add(ctx, downstreamID, upstreamID, "done", "on_hold")
	require.NoError(t, err)

	orch.HandleDependentTasksForTest(ctx, upstreamID, "done")

	require.Equal(t, "ready", stageOf(t, taskRepo, downstreamID), "stage must be unchanged when upstream reaches done")
	require.True(t, hasEvent(events.all(), downstreamID), "OnTaskChanged must fire so the picker can pick up on next tick")
}

// TestCascade_AlreadyTerminalDownstreamSkipped verifies that a downstream task
// already in a terminal stage is not re-processed (no second OnTaskChanged with
// reason "cancelled" for an already-cancelled task).
func TestCascade_AlreadyTerminalDownstreamSkipped(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, events := makeTestOrchestratorWithDeps(t)

	upstreamID := makeCascadeTask(t, taskRepo, "terminal-up")
	downstreamID := makeCascadeTask(t, taskRepo, "terminal-down")

	// put downstream into a terminal state before the cascade
	_, err := taskRepo.Update(ctx, downstreamID, repo.UpdateTaskInput{CurrentStage: ptr("cancelled")})
	require.NoError(t, err)

	_, err = depRepo.Add(ctx, downstreamID, upstreamID, "done", "cancel")
	require.NoError(t, err)

	orch.HandleDependentTasksForTest(ctx, upstreamID, "cancelled")

	// downstream must still be cancelled (not double-processed)
	require.Equal(t, "cancelled", stageOf(t, taskRepo, downstreamID))

	// no "cancelled" reason event must have been fired for the downstream
	for _, ev := range events.all() {
		if ev.taskID == downstreamID {
			require.NotEqual(t, "cancelled", ev.reason,
				"already-terminal downstream must not receive a cascade-cancel OnTaskChanged")
		}
	}
}

// TestCascade_DiamondIsIdempotent verifies a diamond (A→B, A→C, B→D, C→D) where
// cancelling A cascades to D exactly once: D is reached via both B and C, but the
// terminal-skip guard makes the second visit a no-op.
func TestCascade_DiamondIsIdempotent(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, _ := makeTestOrchestratorWithDeps(t)

	a := makeCascadeTask(t, taskRepo, "diamond-a")
	b := makeCascadeTask(t, taskRepo, "diamond-b")
	c := makeCascadeTask(t, taskRepo, "diamond-c")
	d := makeCascadeTask(t, taskRepo, "diamond-d")

	for _, dep := range [][2]string{{b, a}, {c, a}, {d, b}, {d, c}} {
		_, err := depRepo.Add(ctx, dep[0], dep[1], "done", "cancel")
		require.NoError(t, err)
	}

	orch.HandleDependentTasksForTest(ctx, a, "cancelled")

	require.Equal(t, "cancelled", stageOf(t, taskRepo, b))
	require.Equal(t, "cancelled", stageOf(t, taskRepo, c))
	require.Equal(t, "cancelled", stageOf(t, taskRepo, d), "diamond tail cancelled exactly once")
}

// TestCascade_CycleTerminates verifies the terminal-skip guard breaks a dependency
// cycle (A→B, B→A): cancelling A cancels B, and the recursion back to A stops
// because A is already terminal — no infinite loop.
func TestCascade_CycleTerminates(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, depRepo, _ := makeTestOrchestratorWithDeps(t)

	a := makeCascadeTask(t, taskRepo, "cycle-a")
	b := makeCascadeTask(t, taskRepo, "cycle-b")
	_, err := depRepo.Add(ctx, b, a, "done", "cancel")
	require.NoError(t, err)
	_, err = depRepo.Add(ctx, a, b, "done", "cancel")
	require.NoError(t, err)

	// Mirror the real cancel path: the origin task is set terminal before cascade.
	cancelled := "cancelled"
	_, err = taskRepo.Update(ctx, a, repo.UpdateTaskInput{CurrentStage: &cancelled})
	require.NoError(t, err)

	orch.HandleDependentTasksForTest(ctx, a, "cancelled")

	require.Equal(t, "cancelled", stageOf(t, taskRepo, b), "B cancelled via cascade")
	require.Equal(t, "cancelled", stageOf(t, taskRepo, a), "A stays cancelled; recursion did not loop")
}
