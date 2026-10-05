package pipeline_test

import (
	"context"
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
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

var submittedPlan = map[string]any{
	"summary":      "Guard approve on the submitted marker",
	"steps":        []any{"extend ValidateStageOutput"},
	"filesTouched": []any{"server/internal/pipeline/transitions.go"},
	"testApproach": "finalize integration test",
}

func TestDecideCompletedTransition_PlanReviewCarriesTranscriptPlan(t *testing.T) {
	orch := makeTestOrchestrator(t)
	task := &ent.Task{PlanMode: true, CurrentStage: "plan_review"}

	t.Run("transcript plan is stored with the submitted marker", func(t *testing.T) {
		in := maps.Clone(submittedPlan)

		got := orch.DecideCompletedTransitionForTest(context.Background(), task, &ent.StageRun{Stage: "plan_review"}, in)

		wait, ok := got.(pipeline.WaitUserTransition)
		require.True(t, ok, "expected WaitUserTransition, got %T", got)
		require.True(t, wait.AgentDone)
		want := maps.Clone(submittedPlan)
		want[pipeline.StageOutputSubmittedKey] = true
		require.Equal(t, want, wait.Output)
		require.Equal(t, submittedPlan, in, "the caller's map must not be mutated")
	})

	t.Run("run already stamped by set_stage_output is left untouched", func(t *testing.T) {
		stamped := maps.Clone(submittedPlan)
		stamped[pipeline.StageOutputSubmittedKey] = true

		got := orch.DecideCompletedTransitionForTest(context.Background(), task,
			&ent.StageRun{Stage: "plan_review", Output: stamped}, maps.Clone(submittedPlan))

		wait, ok := got.(pipeline.WaitUserTransition)
		require.True(t, ok, "expected WaitUserTransition, got %T", got)
		require.Nil(t, wait.Output, "a nil Output keeps the stored set_stage_output result as-is")
	})

	t.Run("empty output never fabricates a marker", func(t *testing.T) {
		for _, empty := range []map[string]any{nil, {}} {
			got := orch.DecideCompletedTransitionForTest(context.Background(), task, &ent.StageRun{Stage: "plan_review"}, empty)

			wait, ok := got.(pipeline.WaitUserTransition)
			require.True(t, ok, "expected WaitUserTransition, got %T", got)
			require.Nil(t, wait.Output)
		}
	})
}

// planReviewDetector runs the real DetectCompletion against a dead agent whose
// transcript yields transcriptPlan, so the stored run reflects what production
// would persist for each output channel.
func planReviewDetector(transcriptPlan map[string]any) func(*ent.StageRun, string, pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
	return func(sr *ent.StageRun, cwd string, _ pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.DetectCompletion(sr, cwd, pipeline.CompletionDeps{
			IsPidAlive:  func(int) bool { return false },
			FindSession: func(string, string) (string, error) { return "sid-plan", nil },
			ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
				return pipeline.StageOutputRead{Output: transcriptPlan, RawText: "```json\n...\n```"}, nil
			},
		})
	}
}

func TestFinalizeCompletedAsyncRuns_PlanReview_TranscriptFencePlanIsApprovable(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)
	_, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "plan-fence", "plan_review")
	orch.SetCompletionDetector(planReviewDetector(maps.Clone(submittedPlan)))

	require.NoError(t, orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run}))

	got, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", got.Status)
	require.Equal(t, true, got.Output[pipeline.StageOutputSubmittedKey], "the fenced plan must be marked as submitted")
	require.Equal(t, submittedPlan, pipeline.StageResult(got.Output), "the plan from the fence must be on the run")
}

func TestFinalizeCompletedAsyncRuns_PlanReview_SetStageOutputResultUnchanged(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorWithSRRepo(t)
	_, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "plan-tool", "plan_review")
	stored := maps.Clone(submittedPlan)
	stored[pipeline.StageOutputSubmittedKey] = true
	run, err := srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Output: stored})
	require.NoError(t, err)
	orch.SetCompletionDetector(planReviewDetector(map[string]any{"summary": "a different transcript plan"}))

	require.NoError(t, orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run}))

	got, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", got.Status)
	require.Equal(t, pipeline.StageResult(stored), pipeline.StageResult(got.Output), "the tool-submitted result must not be rewritten from the transcript")
	require.Equal(t, true, got.Output[pipeline.StageOutputSubmittedKey])
}
