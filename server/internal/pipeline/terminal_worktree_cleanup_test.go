package pipeline_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// removeWorktreeSpy records every RemoveWorktreeFn invocation so tests can assert
// the seam fired with the expected force flag without touching real git.
type removeWorktreeSpy struct {
	calls atomic.Int32
	force atomic.Bool
}

func (s *removeWorktreeSpy) fn(taskRepo repo.TaskRepo) func(context.Context, *ent.Task, bool) error {
	return func(ctx context.Context, task *ent.Task, force bool) error {
		s.calls.Add(1)
		s.force.Store(force)
		// Mirror production: clear the DB path so the branch becomes reusable.
		_, _ = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{ClearWorktreePath: true})
		return nil
	}
}

func orchWithRemoveFn(t *testing.T, bundle *db.DBBundle, spy *removeWorktreeSpy) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:         taskRepo,
		StageRunRepo:     repo.NewStageRunRepo(c),
		PermissionRepo:   repo.NewPermissionRepo(c),
		AuditRepo:        repo.NewAuditEventRepo(c),
		ConfigRepo:       repo.NewPipelineConfigRepo(c),
		RemoveWorktreeFn: spy.fn(taskRepo),
	})
	require.NoError(t, err)
	return orch, taskRepo
}

func seedTaskWithWorktree(t *testing.T, ctx context.Context, taskRepo repo.TaskRepo, slug, stage string) *ent.Task {
	t.Helper()
	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                slug,
		Title:               slug,
		Cwd:                 "/tmp",
		CurrentStage:        stage,
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
		SourceBranch:        strptr("feat/" + slug),
	})
	require.NoError(t, err)
	wt := "/tmp/wt/" + slug
	task, err = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{WorktreePath: &wt})
	require.NoError(t, err)
	return task
}

// TestTerminalCleanup_DoneRemovesWorktree proves that completing a task force-
// removes its worktree, clears WorktreePath, and records a worktree_removed audit
// event — freeing the source branch for reuse.
func TestTerminalCleanup_DoneRemovesWorktree(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	spy := &removeWorktreeSpy{}
	orch, taskRepo := orchWithRemoveFn(t, bundle, spy)

	task := seedTaskWithWorktree(t, ctx, taskRepo, "done-cleanup", "finalization")
	orch.SetHandlerOverride("finalization", &stubStageHandler{
		stage:      "finalization",
		transition: pipeline.DoneTransition{Output: map[string]any{"ok": true}},
	})

	_, err := orch.ProgressTask(ctx, task.ID, nil)
	require.NoError(t, err)

	require.Equal(t, int32(1), spy.calls.Load(), "RemoveWorktreeFn must fire once on done")
	require.True(t, spy.force.Load(), "terminal cleanup must force-remove")

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, updated.WorktreePath == nil || *updated.WorktreePath == "",
		"WorktreePath must be cleared after terminal cleanup")

	auditRepo := repo.NewAuditEventRepo(bundle.Client)
	events, err := auditRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, hasAction(events, "worktree_removed"), "a worktree_removed audit event must be recorded")

	// Branch is now reusable: CountActive sees no active task holding it.
	n, err := taskRepo.CountActiveBySourceBranch(ctx, "feat/done-cleanup", "")
	require.NoError(t, err)
	require.Equal(t, 0, n, "branch must be free once the done task released its worktree")
}

// TestTerminalCleanup_CancelRemovesWorktree proves NotifyTaskTerminated (the
// shared cancel path for REST + MCP) force-removes the worktree.
func TestTerminalCleanup_CancelRemovesWorktree(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	spy := &removeWorktreeSpy{}
	orch, taskRepo := orchWithRemoveFn(t, bundle, spy)

	task := seedTaskWithWorktree(t, ctx, taskRepo, "cancel-cleanup", "implementation")

	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")

	require.Equal(t, int32(1), spy.calls.Load(), "RemoveWorktreeFn must fire on cancel")
	require.True(t, spy.force.Load(), "cancel cleanup must force-remove")

	updated, err := taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, updated.WorktreePath == nil || *updated.WorktreePath == "",
		"WorktreePath must be cleared after cancel cleanup")
}

// TestTerminalCleanup_NoWorktreeNoOp proves the cleanup is a no-op (no seam call)
// when the task never had a worktree.
func TestTerminalCleanup_NoWorktreeNoOp(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	spy := &removeWorktreeSpy{}
	orch, taskRepo := orchWithRemoveFn(t, bundle, spy)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:                "no-wt",
		Title:               "no-wt",
		Cwd:                 "/tmp",
		CurrentStage:        "implementation",
		Priority:            "medium",
		MaxIterations:       3,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")
	require.Equal(t, int32(0), spy.calls.Load(), "no worktree → no removal call")
}

// TestEnsureTaskWorktree_RejectsHeldBranch proves the git-authoritative preflight:
// EnsureTaskWorktree fails with a descriptive error when the source branch is
// already checked out by another worktree (e.g. a terminal task's leftover).
func TestEnsureTaskWorktree_RejectsHeldBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available:", err)
	}
	repoDir := t.TempDir()
	git := func(args ...string) {
		full := append([]string{"-C", repoDir, "-c", "commit.gpgsign=false"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v (%s)", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("commit", "--allow-empty", "-m", "init")
	leftover := filepath.Join(t.TempDir(), "leftover")
	git("worktree", "add", "-b", "feat/held", leftover)

	task := &ent.Task{Slug: "new-task", Cwd: repoDir, SourceBranch: strptr("feat/held")}
	_, _, err := pipeline.EnsureTaskWorktree(context.Background(), task, t.TempDir())
	require.Error(t, err, "must reject a branch already held by another worktree")
	require.Contains(t, err.Error(), "already checked out", "error must be descriptive")
	require.Contains(t, err.Error(), leftover, "error must name the holding worktree path")
}

func orchWithUnpushedCheckFn(
	t *testing.T,
	bundle *db.DBBundle,
	spy *removeWorktreeSpy,
	hasUnpushedFn func(context.Context, *ent.Task) bool,
) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:          taskRepo,
		StageRunRepo:      repo.NewStageRunRepo(c),
		PermissionRepo:    repo.NewPermissionRepo(c),
		AuditRepo:         repo.NewAuditEventRepo(c),
		ConfigRepo:        repo.NewPipelineConfigRepo(c),
		RemoveWorktreeFn:  spy.fn(taskRepo),
		HasUnpushedWorkFn: hasUnpushedFn,
	})
	require.NoError(t, err)
	return orch, taskRepo
}

// TestTerminalCleanup_RetainsWorktreeWhenUnpushed proves that when
// HasUnpushedWorkFn returns true, RemoveWorktreeFn is NOT called and a
// "worktree_retained_unpushed" audit event is recorded.
func TestTerminalCleanup_RetainsWorktreeWhenUnpushed(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	spy := &removeWorktreeSpy{}
	orch, taskRepo := orchWithUnpushedCheckFn(t, bundle, spy,
		func(_ context.Context, _ *ent.Task) bool { return true },
	)

	task := seedTaskWithWorktree(t, ctx, taskRepo, "retain-unpushed", "implementation")
	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")

	require.Equal(t, int32(0), spy.calls.Load(), "RemoveWorktreeFn must NOT fire when HasUnpushedWorkFn returns true")

	auditRepo := repo.NewAuditEventRepo(bundle.Client)
	events, err := auditRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, hasAction(events, "worktree_retained_unpushed"),
		"a worktree_retained_unpushed audit event must be recorded")
}

// TestTerminalCleanup_RemovesWorktreeWhenNoPendingWork proves that when
// HasUnpushedWorkFn returns false, RemoveWorktreeFn IS called normally.
func TestTerminalCleanup_RemovesWorktreeWhenNoPendingWork(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	spy := &removeWorktreeSpy{}
	orch, taskRepo := orchWithUnpushedCheckFn(t, bundle, spy,
		func(_ context.Context, _ *ent.Task) bool { return false },
	)

	task := seedTaskWithWorktree(t, ctx, taskRepo, "remove-clean", "implementation")
	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")

	require.Equal(t, int32(1), spy.calls.Load(), "RemoveWorktreeFn must fire when HasUnpushedWorkFn returns false")
}

// TestTerminalCleanup_VanishedWorktreeClearsPath proves that when a terminal
// task's WorktreePath points to a directory that no longer exists on disk,
// HasUnpushedWork returns false, RemoveWorktreeFn fires, WorktreePath is
// cleared, and no "worktree_retained_unpushed" audit event is recorded.
func TestTerminalCleanup_VanishedWorktreeClearsPath(t *testing.T) {
	ctx := context.Background()
	bundle := openSharedBundle(t)
	spy := &removeWorktreeSpy{}

	c := bundle.Client
	taskRepo := repo.NewTaskRepo(c)
	mgr := services.NewWorktreeManager(taskRepo)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:          taskRepo,
		StageRunRepo:      repo.NewStageRunRepo(c),
		PermissionRepo:    repo.NewPermissionRepo(c),
		AuditRepo:         repo.NewAuditEventRepo(c),
		ConfigRepo:        repo.NewPipelineConfigRepo(c),
		RemoveWorktreeFn:  spy.fn(taskRepo),
		HasUnpushedWorkFn: mgr.HasUnpushedWork,
	})
	require.NoError(t, err)

	task := seedTaskWithWorktree(t, ctx, taskRepo, "vanished-cleanup", "implementation")
	// Overwrite WorktreePath with a path that does not exist on disk.
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	_, err = taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{WorktreePath: &nonExistentPath})
	require.NoError(t, err)

	orch.NotifyTaskTerminated(ctx, task.ID, "cancelled")

	require.Equal(t, int32(1), spy.calls.Load(),
		"RemoveWorktreeFn must fire: vanished directory is not unpushed work")

	auditRepo := repo.NewAuditEventRepo(bundle.Client)
	events, err := auditRepo.ListForTask(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, hasAction(events, "worktree_retained_unpushed"),
		"must NOT record worktree_retained_unpushed for a vanished directory")
}

func hasAction(events []*ent.AuditEvent, action string) bool {
	for _, e := range events {
		if e.Action == action {
			return true
		}
	}
	return false
}
