package pipeline_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestApplyTransition_KindJobRefusesPipelineStage(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "job-refuses-pipeline-stage", Title: "t", Cwd: "/tmp", CurrentStage: "job", Kind: "job",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "job", SessionName: "jrp-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: strPtr("running")})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, sr, pipeline.NextTransition{Stage: "implementation"})
	require.NoError(t, err)

	gotTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotEqual(t, "implementation", gotTask.CurrentStage)

	gotSR, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", gotSR.Status)
	errMsg, _ := gotSR.Output["error"].(string)
	require.True(t, strings.Contains(errMsg, "job tasks run only the job stage"), "got: %q", errMsg)
}

func TestApplyTransition_KindPipelineRefusesJobStage(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "pipeline-refuses-job-stage", Title: "t", Cwd: "/tmp", CurrentStage: "ready",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "ready", SessionName: "prj-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: strPtr("running")})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, sr, pipeline.NextTransition{Stage: "job"})
	require.NoError(t, err)

	gotSR, err := srRepo.GetByID(ctx, sr.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", gotSR.Status)
	errMsg, _ := gotSR.Output["error"].(string)
	require.True(t, strings.Contains(errMsg, "only job tasks run the job stage"), "got: %q", errMsg)
}

func TestApplyTransition_KindPipelineAllowsPipelineStage(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo := makeOrchestratorFull(t)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "pipeline-allows-pipeline-stage", Title: "t", Cwd: "/tmp", CurrentStage: "ready",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "ready", SessionName: "pap-0"})
	require.NoError(t, err)
	sr, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: strPtr("running")})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, sr, pipeline.NextTransition{Stage: "implementation"})
	require.NoError(t, err)

	gotTask, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "implementation", gotTask.CurrentStage)
}
