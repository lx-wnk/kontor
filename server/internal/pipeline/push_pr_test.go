package pipeline_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// makeFinalizationOrchestrator builds an orchestrator with the given
// OrchestratorOptions overrides applied on top of the minimal repo set every
// orchestrator requires.
func makeFinalizationOrchestrator(t *testing.T, configure func(*pipeline.OrchestratorOptions)) *pipeline.PipelineOrchestrator {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	opts := pipeline.OrchestratorOptions{
		TaskRepo:       repo.NewTaskRepo(bundle.Client),
		StageRunRepo:   repo.NewStageRunRepo(bundle.Client),
		PermissionRepo: repo.NewPermissionRepo(bundle.Client),
		AuditRepo:      repo.NewAuditEventRepo(bundle.Client),
		ConfigRepo:     repo.NewPipelineConfigRepo(bundle.Client),
	}
	if configure != nil {
		configure(&opts)
	}
	orch, err := pipeline.NewOrchestrator(opts)
	require.NoError(t, err)
	return orch
}

func worktreeTask() *ent.Task {
	return &ent.Task{
		ID:           "task-1",
		Slug:         "my-task",
		Title:        "add foo",
		WorktreePath: ptr("/tmp/wt-my-task"),
		SourceBranch: ptr("feat/my-task"),
	}
}

func finalizationRun() *ent.StageRun {
	return &ent.StageRun{ID: "run-1", Stage: "finalization"}
}

func failIfCalled(t *testing.T, name string) {
	t.Helper()
	t.Fatalf("%s must not be called", name)
}

func TestDecideFinalization_CleanPushed_CreatesPR(t *testing.T) {
	ctx := context.Background()
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool { return false }
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			return 42, "https://github.com/lx-wnk/kontor/pull/42", nil
		}
	})

	transition := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{"summary": "done"})

	done, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
	require.Equal(t, 42, done.MetadataPatch["pr_number"])
	require.Equal(t, "https://github.com/lx-wnk/kontor/pull/42", done.MetadataPatch["pr_url"])
}

func TestDecideFinalization_Dirty_Fails(t *testing.T) {
	ctx := context.Background()
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool { return true }
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			failIfCalled(t, "CreateDraftPRFn")
			return 0, "", nil
		}
	})

	transition := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})

	fail, ok := transition.(pipeline.FailTransition)
	require.True(t, ok, "expected FailTransition, got %T", transition)
	require.Contains(t, fail.Reason, "unpushed")
}

func TestDecideFinalization_UnpushedPushSucceeds_CreatesPR(t *testing.T) {
	ctx := context.Background()
	pushCalled := false
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.PushFn = func(ctx context.Context, task *ent.Task) error {
			pushCalled = true
			return nil
		}
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool { return false }
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			return 7, "https://github.com/lx-wnk/kontor/pull/7", nil
		}
	})

	transition := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})

	require.True(t, pushCalled, "PushFn must be called when push is allowed")
	done, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
	require.Equal(t, 7, done.MetadataPatch["pr_number"])
}

func TestDecideFinalization_UnpushedPushFails(t *testing.T) {
	ctx := context.Background()
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.PushFn = func(ctx context.Context, task *ent.Task) error {
			return errors.New("remote rejected")
		}
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			failIfCalled(t, "CreateDraftPRFn")
			return 0, "", nil
		}
	})

	transition := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})

	fail, ok := transition.(pipeline.FailTransition)
	require.True(t, ok, "expected FailTransition, got %T", transition)
	require.Contains(t, fail.Reason, "git push failed")
	require.Contains(t, fail.Reason, "remote rejected")
}

func TestDecideFinalization_PushDisabled_DoneWithoutChecks(t *testing.T) {
	ctx := context.Background()
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = false
		o.PushFn = func(ctx context.Context, task *ent.Task) error {
			failIfCalled(t, "PushFn")
			return nil
		}
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool {
			failIfCalled(t, "HasUnpushedWorkFn")
			return true
		}
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			failIfCalled(t, "CreateDraftPRFn")
			return 0, "", nil
		}
	})

	transition := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})

	done, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
	require.Nil(t, done.MetadataPatch)
}

func TestDecideFinalization_PRCreateFails_DoneWithWarning(t *testing.T) {
	ctx := context.Background()
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool { return false }
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			return 0, "", errors.New("gh: not logged in")
		}
	})

	transition := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})

	done, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
	require.Contains(t, done.MetadataPatch["pr_error"], "not logged in")
	require.NotContains(t, done.MetadataPatch, "pr_url")
}

func TestDecideFinalization_ExistingPRIdempotent(t *testing.T) {
	ctx := context.Background()
	calls := 0
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool { return false }
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			calls++
			return 9, "https://github.com/lx-wnk/kontor/pull/9", nil
		}
	})

	first := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})
	second := orch.DecideCompletedTransitionForTest(ctx, worktreeTask(), finalizationRun(), map[string]any{})

	for _, transition := range []pipeline.StageTransition{first, second} {
		done, ok := transition.(pipeline.DoneTransition)
		require.True(t, ok, "expected DoneTransition, got %T", transition)
		require.Equal(t, 9, done.MetadataPatch["pr_number"])
	}
	// The pipeline always calls CreateDraftPRFn on each finalization; idempotency
	// lives inside ProductionCreateDraftPRFn (findExistingPR), not the pipeline itself.
	require.Equal(t, 2, calls, "pipeline delegates both calls; ProductionCreateDraftPRFn owns the idempotency guard")
}

func TestDecideFinalization_NoWorktree_Passthrough(t *testing.T) {
	ctx := context.Background()
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.HasUnpushedWorkFn = func(ctx context.Context, task *ent.Task) bool {
			failIfCalled(t, "HasUnpushedWorkFn")
			return false
		}
		o.PushFn = func(ctx context.Context, task *ent.Task) error {
			failIfCalled(t, "PushFn")
			return nil
		}
		o.CreateDraftPRFn = func(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
			failIfCalled(t, "CreateDraftPRFn")
			return 0, "", nil
		}
	})

	task := &ent.Task{ID: "task-2", Slug: "no-worktree-task", Title: "add bar"}
	transition := orch.DecideCompletedTransitionForTest(ctx, task, finalizationRun(), map[string]any{"summary": "done"})

	done, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
	require.Nil(t, done.MetadataPatch)
}

func TestResolveBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repoDir := t.TempDir()
	out, err := exec.Command("git", "-C", repoDir, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))

	t.Run("no origin HEAD falls back to source branch", func(t *testing.T) {
		require.Equal(t, "feat/x", pipeline.ResolveBaseForTest(ctx, repoDir, &ent.Task{SourceBranch: ptr("feat/x")}))
	})
	t.Run("nothing known leaves base empty", func(t *testing.T) {
		require.Equal(t, "", pipeline.ResolveBaseForTest(ctx, repoDir, &ent.Task{}))
	})
	t.Run("origin HEAD names the default branch", func(t *testing.T) {
		out, err := exec.Command("git", "-C", repoDir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk").CombinedOutput()
		require.NoError(t, err, string(out))
		require.Equal(t, "trunk", pipeline.ResolveBaseForTest(ctx, repoDir, &ent.Task{SourceBranch: ptr("feat/x")}))
	})
}

func TestBuildPRBody_Format(t *testing.T) {
	task := &ent.Task{ID: "task-3", Slug: "my-task", Title: "add foo"}
	finOutput := map[string]any{
		"summary":   "Implemented foo end to end.",
		"testPlan":  []any{"run pnpm test", "open the app and click foo"},
		"openTodos": []any{"wire the retry button"},
	}
	selfReviewOutput := map[string]any{
		"findings": []any{
			map[string]any{"severity": "high", "description": "no input validation", "file": "foo.go"},
			map[string]any{"severity": "low", "description": "minor naming nit", "file": "foo.go"},
		},
	}

	body := pipeline.BuildPRBodyForTest(task, finOutput, selfReviewOutput)

	require.Contains(t, body, "- [ ] run pnpm test")
	require.Contains(t, body, "- [ ] open the app and click foo")
	require.Contains(t, body, "wire the retry button")
	require.Contains(t, body, "[high] no input validation (foo.go)")
	require.NotContains(t, body, "minor naming nit", "low-severity findings must not appear in Known Issues")
	require.Contains(t, body, "Kontor task: `my-task`")
	require.Contains(t, body, "Generated with")
}

// TestProductionCreateDraftPRFn_ReusesExistingPR exercises the findExistingPR
// code path inside ProductionCreateDraftPRFn using a PATH-injected fake gh
// binary. The fake returns an existing open PR for pr list and fails for any
// other subcommand, so the test asserts that pr create is never reached.
func TestProductionCreateDraftPRFn_ReusesExistingPR(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake sh script not supported on windows")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  echo '[{"number":7,"url":"https://github.com/lx-wnk/kontor/pull/7"}]'
  exit 0
fi
echo "unexpected gh call: $*" >&2
exit 1
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	number, url, err := pipeline.ProductionCreateDraftPRFn(
		context.Background(), dir, "feat/my-task", "develop", "feat: my task", "body",
	)
	require.NoError(t, err)
	require.Equal(t, 7, number)
	require.Equal(t, "https://github.com/lx-wnk/kontor/pull/7", url)
}

func TestDeriveConventionalTitle(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"already prefixed feat", "feat: add foo", "feat: add foo"},
		{"already prefixed with scope", "fix(api): handle nil", "fix(api): handle nil"},
		{"unprefixed gets feat", "add foo", "feat: add foo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := &ent.Task{Title: tc.title}
			require.Equal(t, tc.want, pipeline.DeriveConventionalTitleForTest(task))
		})
	}
}

func TestDecideFinalization_ReleasesSpawnArtefactsBeforeDirtyCheck(t *testing.T) {
	released := false
	orch := makeFinalizationOrchestrator(t, func(o *pipeline.OrchestratorOptions) {
		o.AllowGitPush = true
		o.HasUnpushedWorkFn = func(context.Context, *ent.Task) bool { return !released }
	})
	orch.RegisterSpawnCleanupForTest("run-1", func() { released = true })

	transition := orch.DecideCompletedTransitionForTest(context.Background(), worktreeTask(), finalizationRun(), map[string]any{})

	_, ok := transition.(pipeline.DoneTransition)
	require.True(t, ok, "expected DoneTransition, got %T", transition)
}

func TestApplyDone_MergesPatchIntoStoredMetadataNotSnapshot(t *testing.T) {
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: repo.NewPermissionRepo(bundle.Client),
		AuditRepo:      repo.NewAuditEventRepo(bundle.Client),
		ConfigRepo:     repo.NewPipelineConfigRepo(bundle.Client),
	})
	require.NoError(t, err)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "done-merge", Title: "Done Merge", Cwd: t.TempDir(), CurrentStage: "finalization",
		Priority: "medium", MaxIterations: 3, Metadata: map[string]any{"written_meanwhile": "yes"},
	})
	require.NoError(t, err)
	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "finalization", SessionName: "done-merge-0"})
	require.NoError(t, err)
	stale := *task
	stale.Metadata = nil

	_, err = orch.ApplyTransitionForTest(ctx, &stale, sr, pipeline.DoneTransition{MetadataPatch: map[string]any{"pr_number": 3}})
	require.NoError(t, err)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "yes", updated.Metadata["written_meanwhile"])
	require.InDelta(t, 3, updated.Metadata["pr_number"], 0)
}

// asyncFinalizationFixture seeds a worktree task whose finalization run has
// completed, wired with the given push stub and a counting PR stub.
func asyncFinalizationFixture(t *testing.T, pushFn func(context.Context, *ent.Task) error, prCalls *atomic.Int32) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.StageRunRepo, *ent.Task, *ent.StageRun) {
	t.Helper()
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:          taskRepo,
		StageRunRepo:      srRepo,
		PermissionRepo:    repo.NewPermissionRepo(bundle.Client),
		AuditRepo:         repo.NewAuditEventRepo(bundle.Client),
		ConfigRepo:        repo.NewPipelineConfigRepo(bundle.Client),
		AllowGitPush:      true,
		RemoveWorktreeFn:  func(context.Context, *ent.Task, bool) error { return nil },
		PushFn:            pushFn,
		HasUnpushedWorkFn: func(context.Context, *ent.Task) bool { return false },
		CreateDraftPRFn: func(context.Context, string, string, string, string, string) (int, string, error) {
			prCalls.Add(1)
			return 7, "https://github.com/lx-wnk/kontor/pull/7", nil
		},
	})
	require.NoError(t, err)
	orch.SetCompletionDetector(func(*ent.StageRun, string, pipeline.CompletionDeps) (pipeline.CompletionResult, error) {
		return pipeline.CompletionResult{Kind: "completed", Output: map[string]any{}}, nil
	})

	task, run := makeRunningStageRunAtStage(t, ctx, taskRepo, srRepo, "async-push", "finalization")
	task, err = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{WorktreePath: ptr(t.TempDir())})
	require.NoError(t, err)
	return orch, taskRepo, srRepo, task, run
}

func TestFinalizationPush_TickReturnsWhilePushBlocksAndStartsItOnce(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	var pushes, prs atomic.Int32
	orch, taskRepo, _, task, run := asyncFinalizationFixture(t, func(context.Context, *ent.Task) error {
		pushes.Add(1)
		<-release
		return nil
	}, &prs)

	returned := make(chan error, 1)
	go func() { returned <- orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run}) }()
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("tick blocked on the push")
	}
	require.NoError(t, orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run}))
	require.True(t, orch.FinalizationPushInFlightForTest(task.ID))

	close(release)
	require.Eventually(t, func() bool { return !orch.FinalizationPushInFlightForTest(task.ID) }, 2*time.Second, 5*time.Millisecond)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "done", updated.CurrentStage)
	require.Equal(t, int32(1), pushes.Load(), "a second tick must not start a second push")
	require.Equal(t, int32(1), prs.Load())
}

func TestFinalizationPush_CancelDuringPush_StaysCancelledWithoutPR(t *testing.T) {
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	var prs atomic.Int32
	orch, taskRepo, srRepo, task, run := asyncFinalizationFixture(t, func(context.Context, *ent.Task) error {
		close(started)
		<-release
		return nil
	}, &prs)

	require.NoError(t, orch.FinalizeCompletedAsyncRunsForTest(ctx, []*ent.StageRun{run}))
	<-started
	cancelled := "cancelled"
	_, err := taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{CurrentStage: &cancelled})
	require.NoError(t, err)
	orch.NotifyTaskTerminated(ctx, task.ID, cancelled)
	close(release)
	require.Eventually(t, func() bool { return !orch.FinalizationPushInFlightForTest(task.ID) }, 2*time.Second, 5*time.Millisecond)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", updated.CurrentStage)
	updatedRun, err := srRepo.GetByID(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", updatedRun.Status)
	require.Equal(t, int32(0), prs.Load(), "a cancelled task must not get a PR")
}

func TestApplyDone_RefusesTerminalTask(t *testing.T) {
	ctx := context.Background()
	var prs atomic.Int32
	orch, taskRepo, _, task, run := asyncFinalizationFixture(t, nil, &prs)
	cancelled := "cancelled"
	_, err := taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{CurrentStage: &cancelled})
	require.NoError(t, err)

	_, err = orch.ApplyTransitionForTest(ctx, task, run, pipeline.DoneTransition{})
	require.Error(t, err)

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", updated.CurrentStage)
}
