package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
)

// scopeFixture holds two unrelated tasks plus the stage run whose credential a
// stage agent working on task A would carry.
type scopeFixture struct {
	registry mcp.ToolRegistry
	taskA    *ent.Task
	taskB    *ent.Task
	runA     *ent.StageRun
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	client := bundle.Client
	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)

	newTask := func(slug string) *ent.Task {
		task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
			Slug: slug, Title: slug, Cwd: t.TempDir(),
			CurrentStage: "implementation", Priority: "medium",
			MaxIterations: 3,
		})
		require.NoError(t, err)
		return task
	}
	taskA := newTask("task-a")
	taskB := newTask("task-b")

	runA, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: taskA.ID, Stage: "implementation"})
	require.NoError(t, err)
	_, err = srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: taskB.ID, Stage: "implementation"})
	require.NoError(t, err)

	registry := mcp.ToolRegistry{}
	RegisterReadTools(registry, ReadDeps{
		TaskRepo:  taskRepo,
		SRRepo:    srRepo,
		PermRepo:  permRepo,
		AuditRepo: auditRepo,
		Caller:    mcp.CallerResolver{StageRuns: srRepo, Tasks: taskRepo},
	})
	return scopeFixture{registry: registry, taskA: taskA, taskB: taskB, runA: runA}
}

// stageRunCtx is what the auth middleware puts on a request made with a
// per-stage-run MCP key.
func stageRunCtx(runID string) context.Context {
	return mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k-stage", StageRunID: runID})
}

// userCtx is what the middleware puts on a request made with a key a person
// created in the dashboard: no stage run.
func userCtx() context.Context {
	return mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k-user"})
}

func callTool(t *testing.T, f scopeFixture, name string, ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
	t.Helper()
	tool, ok := f.registry[name]
	require.True(t, ok, "tool %s not registered", name)
	return tool.Handler(ctx, args)
}

func slugsOf(t *testing.T, result *mcp.ToolResult) []string {
	t.Helper()
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	var tasks []struct {
		Slug string `json:"slug"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].Text), &tasks))
	out := make([]string, len(tasks))
	for i, task := range tasks {
		out[i] = task.Slug
	}
	return out
}

// A stage agent must see its own task and nothing else. Without the narrowing
// list_tasks hands it every task in every project (CWE-200).
func TestListTasks_StageRunKeySeesOnlyItsOwnTask(t *testing.T) {
	f := newScopeFixture(t)
	result, err := callTool(t, f, "list_tasks", stageRunCtx(f.runA.ID), map[string]any{})
	require.NoError(t, err)
	require.Equal(t, []string{"task-a"}, slugsOf(t, result))
}

// The regression that would break real users: a dashboard API key must keep
// seeing every task.
func TestListTasks_UserKeyStillSeesEveryTask(t *testing.T) {
	f := newScopeFixture(t)
	result, err := callTool(t, f, "list_tasks", userCtx(), map[string]any{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"task-a", "task-b"}, slugsOf(t, result))
}

// The stage filter must narrow within the caller's own task, not around it.
func TestListTasks_StageRunKeyStaysNarrowedWithAStageFilter(t *testing.T) {
	f := newScopeFixture(t)
	ctx := stageRunCtx(f.runA.ID)

	matching, err := callTool(t, f, "list_tasks", ctx, map[string]any{"stage": "implementation"})
	require.NoError(t, err)
	require.Equal(t, []string{"task-a"}, slugsOf(t, matching))

	other, err := callTool(t, f, "list_tasks", ctx, map[string]any{"stage": "review"})
	require.NoError(t, err)
	require.Empty(t, slugsOf(t, other))
}

// Fail closed: a credential naming a stage run that no longer exists must be
// refused, not promoted to an unrestricted one.
func TestListTasks_UnresolvableCallerIsRefused(t *testing.T) {
	f := newScopeFixture(t)
	_, err := callTool(t, f, "list_tasks", stageRunCtx("gone"), map[string]any{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

func TestGetTask_StageRunKeyIsRefusedAnotherTask(t *testing.T) {
	f := newScopeFixture(t)

	own, err := callTool(t, f, "get_task", stageRunCtx(f.runA.ID), map[string]any{"id_or_slug": f.taskA.ID})
	require.NoError(t, err)
	require.NotNil(t, own)

	_, err = callTool(t, f, "get_task", stageRunCtx(f.runA.ID), map[string]any{"id_or_slug": f.taskB.ID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// The slug path resolves a different task id than the argument names, so it
// needs its own proof that the check sits after resolution.
func TestGetTask_StageRunKeyIsRefusedAnotherTaskBySlug(t *testing.T) {
	f := newScopeFixture(t)
	_, err := callTool(t, f, "get_task", stageRunCtx(f.runA.ID), map[string]any{"id_or_slug": f.taskB.Slug})
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

func TestGetTask_UserKeyStillReadsEveryTask(t *testing.T) {
	f := newScopeFixture(t)
	for _, id := range []string{f.taskA.ID, f.taskB.ID} {
		result, err := callTool(t, f, "get_task", userCtx(), map[string]any{"id_or_slug": id})
		require.NoError(t, err)
		require.NotNil(t, result)
	}
}

func TestGetTask_UnresolvableCallerIsRefused(t *testing.T) {
	f := newScopeFixture(t)
	_, err := callTool(t, f, "get_task", stageRunCtx("gone"), map[string]any{"id_or_slug": f.taskA.ID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "access denied")
}

// list_stage_runs, list_audit and list_permission_requests all take a foreign
// task_id argument and all route through the same guard.
func TestTaskScopedListTools_StageRunKeyIsRefusedAnotherTask(t *testing.T) {
	f := newScopeFixture(t)
	for _, name := range []string{"list_stage_runs", "list_audit", "list_permission_requests"} {
		t.Run(name, func(t *testing.T) {
			_, err := callTool(t, f, name, stageRunCtx(f.runA.ID), map[string]any{"task_id": f.taskB.ID})
			require.Error(t, err)
			require.Contains(t, err.Error(), "access denied")

			_, ownErr := callTool(t, f, name, stageRunCtx(f.runA.ID), map[string]any{"task_id": f.taskA.ID})
			require.NoError(t, ownErr, "the caller's own task must still be readable")
		})
	}
}

func TestTaskScopedListTools_UserKeyStillReadsEveryTask(t *testing.T) {
	f := newScopeFixture(t)
	for _, name := range []string{"list_stage_runs", "list_audit", "list_permission_requests"} {
		t.Run(name, func(t *testing.T) {
			_, err := callTool(t, f, name, userCtx(), map[string]any{"task_id": f.taskB.ID})
			require.NoError(t, err)
		})
	}
}

func TestTaskScopedListTools_UnresolvableCallerIsRefused(t *testing.T) {
	f := newScopeFixture(t)
	for _, name := range []string{"list_stage_runs", "list_audit", "list_permission_requests"} {
		t.Run(name, func(t *testing.T) {
			_, err := callTool(t, f, name, stageRunCtx("gone"), map[string]any{"task_id": f.taskA.ID})
			require.Error(t, err)
			require.Contains(t, err.Error(), "access denied")
		})
	}
}
