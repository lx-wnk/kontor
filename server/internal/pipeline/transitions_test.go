package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestDecideCompletedTransition_PlanMode(t *testing.T) {
	orch := makeTestOrchestrator(t)
	ctx := context.Background()

	t.Run("plan_mode=true ready→plan_review", func(t *testing.T) {
		task := &ent.Task{PlanMode: true, CurrentStage: "ready"}
		run := &ent.StageRun{Stage: "ready"}
		output := map[string]any{"summary": "done"}

		got := orch.DecideCompletedTransitionForTest(ctx, task, run, output)

		next, ok := got.(pipeline.NextTransition)
		require.True(t, ok, "expected NextTransition, got %T", got)
		assert.Equal(t, "plan_review", next.Stage)
		assert.Equal(t, output, next.Output)
	})

	t.Run("plan_mode=false ready skips plan_review→implementation", func(t *testing.T) {
		task := &ent.Task{PlanMode: false, CurrentStage: "ready"}
		run := &ent.StageRun{Stage: "ready"}
		output := map[string]any{"summary": "done"}

		got := orch.DecideCompletedTransitionForTest(ctx, task, run, output)

		next, ok := got.(pipeline.NextTransition)
		require.True(t, ok, "expected NextTransition, got %T", got)
		assert.Equal(t, "implementation", next.Stage)
		assert.Equal(t, output, next.Output)
	})

	t.Run("plan_review→awaiting_user (human gate, no auto-advance)", func(t *testing.T) {
		task := &ent.Task{PlanMode: true, CurrentStage: "plan_review"}
		run := &ent.StageRun{Stage: "plan_review"}

		got := orch.DecideCompletedTransitionForTest(ctx, task, run, nil)

		_, ok := got.(pipeline.WaitUserTransition)
		require.True(t, ok, "expected WaitUserTransition, got %T", got)
	})
}
