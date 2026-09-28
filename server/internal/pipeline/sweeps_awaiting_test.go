package pipeline_test

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func makeAwaitingSweepOrchestrator(t *testing.T) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo, repo.PipelineConfigRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	cfgRepo := repo.NewPipelineConfigRepo(bundle.Client)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: repo.NewPermissionRepo(bundle.Client),
		AuditRepo:      repo.NewAuditEventRepo(bundle.Client),
		ConfigRepo:     cfgRepo,
	})
	require.NoError(t, err)
	return orch, taskRepo, srRepo, cfgRepo
}

func makeAwaitingRun(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, srRepo repo.StageRunRepo, slug string, pid *int, startedAt time.Time) string {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: slug, Title: slug, Cwd: "/tmp", CurrentStage: "plan_review",
		Priority: "medium", MaxIterations: 3,
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "plan_review", SessionName: slug + "-0"})
	require.NoError(t, err)
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:    strPtr("awaiting_user"),
		PID:       pid,
		StartedAt: &startedAt,
		Output:    map[string]any{"plan": "the plan under review"},
	})
	require.NoError(t, err)
	return sr.ID
}

func TestSweepAwaitingUserRuns_NoAgentProcess_NeverTimedOut(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo, cfgRepo := makeAwaitingSweepOrchestrator(t)
	require.NoError(t, cfgRepo.Set(ctx, "awaitingUserTimeoutSeconds", "60"))
	runID := makeAwaitingRun(t, ctx, taskRepo, srRepo, "wait-without-agent", nil, time.Now().Add(-2*time.Hour))

	runs, err := srRepo.ListByStatus(ctx, "awaiting_user")
	require.NoError(t, err)
	require.NoError(t, orch.SweepAwaitingUserRunsForTest(ctx, runs))

	got, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "awaiting_user", got.Status, "a run waiting for a human without an agent process must never be timed out")
}

func TestSweepAwaitingUserRuns_LiveAgentPastLimit_Failed(t *testing.T) {
	ctx := context.Background()
	orch, taskRepo, srRepo, cfgRepo := makeAwaitingSweepOrchestrator(t)
	require.NoError(t, cfgRepo.Set(ctx, "awaitingUserTimeoutSeconds", "60"))

	// The sweep signals the PID's process group, so the live agent is a child
	// in its own group — never the test process.
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	pid := cmd.Process.Pid

	runID := makeAwaitingRun(t, ctx, taskRepo, srRepo, "wait-live-agent", &pid, time.Now().Add(-2*time.Hour))
	runs, err := srRepo.ListByStatus(ctx, "awaiting_user")
	require.NoError(t, err)
	require.NoError(t, orch.SweepAwaitingUserRunsForTest(ctx, runs))

	got, err := srRepo.GetByID(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, "failed", got.Status, "a live agent past the limit is still reaped")
}
