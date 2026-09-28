package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func strptr(s string) *string { return &s }

// orchWithWorktreeFn builds an orchestrator whose EnsureWorktreeFn is replaced by
// the given stub, and registers an implementation handler stub.
func orchWithWorktreeFn(t *testing.T, bundle *db.DBBundle, wt func(context.Context, *ent.Task, string) (string, string, error), handlerTransition pipeline.StageTransition) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	c := bundle.Client
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:         repo.NewTaskRepo(c),
		StageRunRepo:     repo.NewStageRunRepo(c),
		PermissionRepo:   repo.NewPermissionRepo(c),
		AuditRepo:        repo.NewAuditEventRepo(c),
		ConfigRepo:       repo.NewPipelineConfigRepo(c),
		EnsureWorktreeFn: wt,
	})
	require.NoError(t, err)
	orch.SetHandlerOverride("implementation", &agentStubHandler{
		stage:      "implementation",
		transition: handlerTransition,
	})
	return orch, repo.NewTaskRepo(c)
}

// TestOrchestrator_WorktreeFailureRecordedAsFailedRun proves the core fix: when
// worktree creation fails, a stage_run is created and marked failed with the error
// recorded, instead of swallowing the error and leaving zero runs (silent stall).
func TestOrchestrator_WorktreeFailureRecordedAsFailedRun(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	wtErr := errors.New("'feat/x' is already used by worktree at /tmp/other")
	orch, taskRepo := orchWithWorktreeFn(t, bundle,
		func(context.Context, *ent.Task, string) (string, string, error) { return "", "", wtErr },
		pipeline.FailTransition{Reason: "handler must not be reached"},
	)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "worktree-fail",
		Title:               "Worktree Fail",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
		SourceBranch:        strptr("feat/x"),
	})
	require.NoError(t, err)

	result, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, result, "a failed stage_run must be returned, not a swallowed nil")

	runs := listRunsForTask(t, srRepo, ctx, task.ID)
	require.Len(t, runs, 1, "exactly one stage_run must exist for the failed worktree attempt")
	require.Equal(t, "failed", runs[0].Status)
	errMsg, _ := runs[0].Output["error"].(string)
	require.True(t, strings.Contains(errMsg, "worktree creation failed"),
		"failed run must record the worktree error, got: %q", errMsg)
	require.True(t, strings.Contains(errMsg, "already used by worktree"),
		"failed run must preserve the underlying git error, got: %q", errMsg)
}

// TestOrchestrator_WorktreeSuccessPersistsPath proves the success path is unchanged:
// the worktree path is persisted on the task and a run progresses past worktree setup.
func TestOrchestrator_WorktreeSuccessPersistsPath(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	const wtPath = "/tmp/wt/worktree-ok"
	calls := 0
	orch, taskRepo := orchWithWorktreeFn(t, bundle,
		func(_ context.Context, _ *ent.Task, _ string) (string, string, error) {
			calls++
			return wtPath, "feat/x", nil
		},
		pipeline.WaitUserTransition{Reason: "reached handler"},
	)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "worktree-ok",
		Title:               "Worktree OK",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
		SourceBranch:        strptr("feat/x"),
	})
	require.NoError(t, err)

	_, err = orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)

	require.Equal(t, 1, calls, "EnsureWorktreeFn must be invoked once on the success path")
	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.WorktreePath)
	require.Equal(t, wtPath, *updated.WorktreePath, "worktree path must be persisted on success")

	runs := listRunsForTask(t, srRepo, ctx, task.ID)
	require.Len(t, runs, 1)
	require.NotEqual(t, "failed", runs[0].Status,
		fmt.Sprintf("success path must not fail the run, got status %q", runs[0].Status))
}
