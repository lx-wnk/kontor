package agentbroadcast

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/merger"
)

// fakeStageRuns embeds repo.StageRunRepo so only ListBySessionIDs needs an
// implementation; any other method call would panic (none are reached here).
type fakeStageRuns struct {
	repo.StageRunRepo
	bySession map[string]*ent.StageRun
	err       error
}

func (f fakeStageRuns) ListBySessionIDs(_ context.Context, sessionIDs []string) ([]*ent.StageRun, error) {
	if f.err != nil {
		return nil, f.err
	}
	runs := make([]*ent.StageRun, 0, len(sessionIDs))
	for _, sid := range sessionIDs {
		if sr, ok := f.bySession[sid]; ok {
			runs = append(runs, sr)
		}
	}
	return runs, nil
}

type fakeTasks struct {
	repo.TaskRepo
	byID map[string]*ent.Task
	err  error
}

func (f fakeTasks) ListByIDs(_ context.Context, ids []string) ([]*ent.Task, error) {
	if f.err != nil {
		return nil, f.err
	}
	tasks := make([]*ent.Task, 0, len(ids))
	for _, id := range ids {
		if t, ok := f.byID[id]; ok {
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

// fakePermissions embeds repo.PermissionRepo so only ListPendingForStageRuns
// needs an implementation.
type fakePermissions struct {
	repo.PermissionRepo
	byStageRun map[string][]*ent.PermissionRequest
	err        error
}

func (f fakePermissions) ListPendingForStageRuns(_ context.Context, stageRunIDs []string) ([]*ent.PermissionRequest, error) {
	if f.err != nil {
		return nil, f.err
	}
	reqs := make([]*ent.PermissionRequest, 0, len(stageRunIDs))
	for _, id := range stageRunIDs {
		reqs = append(reqs, f.byStageRun[id]...)
	}
	return reqs, nil
}

func sessionPtr(s string) *string { return &s }

func TestPipelineTaskEnricher_SetsBothFieldsOnMatch(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "Implement enricher"},
	}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Equal(t, "task-1", agents[0].PipelineTaskID)
	require.Equal(t, "Implement enricher", agents[0].PipelineTaskTitle)
}

func TestPipelineTaskEnricher_NoMatchLeavesEmpty(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{}}
	tasks := fakeTasks{byID: map[string]*ent.Task{}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-unknown"}, {SessionID: ""}}
	enrich(context.Background(), agents)

	require.Empty(t, agents[0].PipelineTaskID)
	require.Empty(t, agents[0].PipelineTaskTitle)
	require.Empty(t, agents[1].PipelineTaskID)
	require.Empty(t, agents[1].PipelineTaskTitle)
}

func TestPipelineTaskEnricher_StageRunErrorLeavesEmptyNoPanic(t *testing.T) {
	stageRuns := fakeStageRuns{err: errors.New("db down")}
	tasks := fakeTasks{}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	require.NotPanics(t, func() { enrich(context.Background(), agents) })

	require.Empty(t, agents[0].PipelineTaskID)
	require.Empty(t, agents[0].PipelineTaskTitle)
}

func TestPipelineTaskEnricher_TaskErrorKeepsIDDropsTitle(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{err: errors.New("db down")}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Equal(t, "task-1", agents[0].PipelineTaskID)
	require.Empty(t, agents[0].PipelineTaskTitle)
}

func TestPipelineTaskEnricher_NilReposNoop(t *testing.T) {
	enrich := NewPipelineTaskEnricher(nil, nil, nil, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	require.NotPanics(t, func() { enrich(context.Background(), agents) })
	require.Empty(t, agents[0].PipelineTaskID)
}

func TestPipelineTaskEnricher_PendingPermissions_TwoRequests(t *testing.T) {
	pattern := "*.go"
	reason := "need read"
	ts := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "Perm task"},
	}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {
			{ID: "req-1", StageRunID: "sr-1", Tool: "Bash", RequestedAt: ts},
			{ID: "req-2", StageRunID: "sr-1", Tool: "Read", Pattern: &pattern, Reason: &reason, RequestedAt: ts},
		},
	}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 2)
	require.Equal(t, "req-1", agents[0].PendingPermissions[0].ID)
	require.Equal(t, "Bash", agents[0].PendingPermissions[0].Tool)
	require.Equal(t, "req-2", agents[0].PendingPermissions[1].ID)
	require.Equal(t, "Read", agents[0].PendingPermissions[1].Tool)
	require.Equal(t, &pattern, agents[0].PendingPermissions[1].Pattern)
	require.Equal(t, "2025-01-15T10:00:00Z", agents[0].PendingPermissions[0].RequestedAt)
}

func TestPipelineTaskEnricher_PendingPermissions_NoStageRun(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{}}
	tasks := fakeTasks{byID: map[string]*ent.Task{}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-no-run"}}
	enrich(context.Background(), agents)

	require.Empty(t, agents[0].PendingPermissions)
}

func TestPipelineTaskEnricher_PendingPermissions_ResolvedNotIncluded(t *testing.T) {
	// ListPendingForStageRuns only returns pending (outcome IS NULL) — resolved
	// ones are excluded by the repo. Verify the enricher passes through exactly
	// what the repo returns: no resolved request in the result.
	ts := time.Now().UTC()
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "T"},
	}}
	// Fake repo returns only the one still-pending request (resolved ones are
	// filtered at the repo layer, not here).
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-pending", StageRunID: "sr-1", Tool: "Write", RequestedAt: ts}},
	}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 1)
	require.Equal(t, "req-pending", agents[0].PendingPermissions[0].ID)
}

func TestPipelineTaskEnricher_PendingPermissions_QueryErrorLeavesEmpty(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "T"},
	}}
	perms := fakePermissions{err: errors.New("db down")}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, nil, nil, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	require.NotPanics(t, func() { enrich(context.Background(), agents) })

	// Task fields still populated; permissions silently empty.
	require.Equal(t, "task-1", agents[0].PipelineTaskID)
	require.Empty(t, agents[0].PendingPermissions)
}

// TestPipelineTaskEnricher_MultiAgentBatchMatchesPerAgentResult verifies the
// batched lookup produces the same per-agent annotations that three
// independent per-agent lookups would have produced pre-PERF-DB2, across a
// mix of a fully-resolved agent, a stage-run-only agent, and an ad-hoc agent
// with no stage_run at all.
func TestPipelineTaskEnricher_MultiAgentBatchMatchesPerAgentResult(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-full": {ID: "sr-full", TaskID: "task-full", SessionID: sessionPtr("sess-full")},
		"sess-bare": {ID: "sr-bare", TaskID: "task-bare", SessionID: sessionPtr("sess-bare")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-full": {ID: "task-full", Title: "Full Task"},
		"task-bare": {ID: "task-bare", Title: "Bare Task"},
	}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-full": {{ID: "req-1", StageRunID: "sr-full", Tool: "Bash", RequestedAt: ts}},
	}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, nil, nil, nil)
	agents := []sdk.Agent{
		{SessionID: "sess-full"},
		{SessionID: "sess-bare"},
		{SessionID: "sess-adhoc"},
	}
	enrich(context.Background(), agents)

	require.Equal(t, "task-full", agents[0].PipelineTaskID)
	require.Equal(t, "Full Task", agents[0].PipelineTaskTitle)
	require.Len(t, agents[0].PendingPermissions, 1)
	require.Equal(t, "req-1", agents[0].PendingPermissions[0].ID)

	require.Equal(t, "task-bare", agents[1].PipelineTaskID)
	require.Equal(t, "Bare Task", agents[1].PipelineTaskTitle)
	require.Empty(t, agents[1].PendingPermissions)

	require.Empty(t, agents[2].PipelineTaskID)
	require.Empty(t, agents[2].PipelineTaskTitle)
	require.Empty(t, agents[2].PendingPermissions)
}

// fakeGrants embeds repo.GrantRepo so only ListForCapability needs an
// implementation. calls counts invocations per capability name, letting a
// test assert the enricher's per-pass cache is doing its job.
type fakeGrants struct {
	repo.GrantRepo
	byCapability map[string][]*ent.Grant
	err          error
	calls        map[string]int
}

func (f *fakeGrants) ListForCapability(_ context.Context, capabilityName string) ([]*ent.Grant, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[capabilityName]++
	if f.err != nil {
		return nil, f.err
	}
	return f.byCapability[capabilityName], nil
}

// fakeCapabilities answers like production does for a refreshed application
// tool: a capability row of class "tool", whose fallback when no grant applies
// is ask. A missing row is a different case with a different answer and has
// its own test below.
type fakeCapabilities struct {
	repo.CapabilityRepo
}

func (fakeCapabilities) Get(_ context.Context, name string) (*ent.Capability, error) {
	return &ent.Capability{Name: name, Class: "tool"}, nil
}

// missingCapabilities models an application tool whose catalogue was never
// refreshed, so no capability row exists.
type missingCapabilities struct {
	repo.CapabilityRepo
}

func (missingCapabilities) Get(_ context.Context, _ string) (*ent.Capability, error) {
	return nil, errors.New("not found")
}

func globalDenyGrant(capabilityName string) *ent.Grant {
	return &ent.Grant{ID: "grant-1", CapabilityName: capabilityName, ContextKind: "global", Mode: "deny"}
}

func TestPipelineTaskEnricher_DeniedByDefault_LiveGlobalDenyGrant(t *testing.T) {
	const capName = "mcp__mail__imap_send_email"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{"task-1": {ID: "task-1", Title: "T"}}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-1", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()}},
	}}
	grants := &fakeGrants{byCapability: map[string][]*ent.Grant{capName: {globalDenyGrant(capName)}}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, fakeCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 1)
	require.True(t, agents[0].PendingPermissions[0].DeniedByDefault)
}

func TestPipelineTaskEnricher_DeniedByDefault_GrantRevoked(t *testing.T) {
	const capName = "mcp__mail__imap_send_email"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{"task-1": {ID: "task-1", Title: "T"}}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-1", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()}},
	}}
	revoked := globalDenyGrant(capName)
	revokedAt := time.Now()
	revoked.RevokedAt = &revokedAt
	grants := &fakeGrants{byCapability: map[string][]*ent.Grant{capName: {revoked}}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, fakeCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 1)
	require.False(t, agents[0].PendingPermissions[0].DeniedByDefault)
}

func TestPipelineTaskEnricher_DeniedByDefault_BuiltinToolSkipsGrantsLookup(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{"task-1": {ID: "task-1", Title: "T"}}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-1", StageRunID: "sr-1", Tool: "Bash", RequestedAt: time.Now()}},
	}}
	grants := &fakeGrants{}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, fakeCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 1)
	require.False(t, agents[0].PendingPermissions[0].DeniedByDefault)
	require.Zero(t, grants.calls["Bash"])
	require.Empty(t, grants.calls)
}

func TestPipelineTaskEnricher_DeniedByDefault_GrantsErrorLeavesFalseStillListed(t *testing.T) {
	const capName = "mcp__mail__imap_send_email"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{"task-1": {ID: "task-1", Title: "T"}}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-1", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()}},
	}}
	grants := &fakeGrants{err: errors.New("db down")}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, fakeCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	require.NotPanics(t, func() { enrich(context.Background(), agents) })

	require.Len(t, agents[0].PendingPermissions, 1)
	require.Equal(t, "req-1", agents[0].PendingPermissions[0].ID)
	require.False(t, agents[0].PendingPermissions[0].DeniedByDefault)
}

func TestPipelineTaskEnricher_DeniedByDefault_SameCapabilityOneLookupPerPass(t *testing.T) {
	const capName = "mcp__mail__imap_send_email"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{"task-1": {ID: "task-1", Title: "T"}}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {
			{ID: "req-1", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()},
			{ID: "req-2", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()},
		},
	}}
	grants := &fakeGrants{byCapability: map[string][]*ent.Grant{capName: {globalDenyGrant(capName)}}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, fakeCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 2)
	require.True(t, agents[0].PendingPermissions[0].DeniedByDefault)
	require.True(t, agents[0].PendingPermissions[1].DeniedByDefault)
	require.Equal(t, 1, grants.calls[capName])
}

func TestPipelineTaskEnricher_DeniedByDefault_NoCapabilityRowIsDenied(t *testing.T) {
	const capName = "mcp__mail__imap_send_email"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{"task-1": {ID: "task-1", Title: "T"}}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-1", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()}},
	}}
	grants := &fakeGrants{byCapability: map[string][]*ent.Grant{}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, missingCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}}
	enrich(context.Background(), agents)

	require.Len(t, agents[0].PendingPermissions, 1)
	require.True(t, agents[0].PendingPermissions[0].DeniedByDefault,
		"a tool with no capability row resolves to deny, and the run's allow list agrees — the band must not offer a decision that would not take effect")
}

// fakeProjects embeds repo.ProjectRepo so only ListByIDs needs an
// implementation.
type fakeProjects struct {
	repo.ProjectRepo
	byID map[string]*ent.Project
	err  error
}

func (f fakeProjects) ListByIDs(_ context.Context, ids []string) ([]*ent.Project, error) {
	if f.err != nil {
		return nil, f.err
	}
	projects := make([]*ent.Project, 0, len(ids))
	for _, id := range ids {
		if p, ok := f.byID[id]; ok {
			projects = append(projects, p)
		}
	}
	return projects, nil
}

func TestPipelineTaskEnricher_SetsProjectNameFromKontorProject(t *testing.T) {
	projectID := "proj-1"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "Obsidian Vault Root Validation", ProjectID: &projectID},
	}}
	projects := fakeProjects{byID: map[string]*ent.Project{
		"proj-1": {ID: "proj-1", Name: "kontor"},
	}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, projects)
	agents := []sdk.Agent{{SessionID: "sess-1", ProjectName: "obsidian-vault-root-validation"}}
	enrich(context.Background(), agents)

	require.Equal(t, "kontor", agents[0].ProjectName, "ProjectName must be the Kontor project name, not the worktree cwd basename")
	require.Equal(t, "proj-1", agents[0].ProjectID)
	require.Equal(t, "task-1", agents[0].PipelineTaskID)
}

func TestPipelineTaskEnricher_KeepsProjectNameWhenTaskHasNoProject(t *testing.T) {
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "Ad-hoc fix"},
	}}
	projects := fakeProjects{byID: map[string]*ent.Project{}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, projects)
	agents := []sdk.Agent{{SessionID: "sess-1", ProjectName: "my-worktree"}}
	enrich(context.Background(), agents)

	require.Equal(t, "my-worktree", agents[0].ProjectName, "ProjectName stays as cwd basename when task has no ProjectID")
}

func TestPipelineTaskEnricher_ProjectLookupErrorKeepsProjectName(t *testing.T) {
	projectID := "proj-1"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "Implement enricher", ProjectID: &projectID},
	}}
	projects := fakeProjects{err: errors.New("db down")}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, projects)
	agents := []sdk.Agent{{SessionID: "sess-1", ProjectName: "my-worktree"}}
	require.NotPanics(t, func() { enrich(context.Background(), agents) })

	require.Equal(t, "my-worktree", agents[0].ProjectName, "a failed project lookup keeps the cwd basename")
	require.Equal(t, "Implement enricher", agents[0].PipelineTaskTitle, "a failed project lookup must not drop the task annotation")
}

// fakeFolders embeds repo.ProjectFolderRepo so only ListAll needs an
// implementation.
type fakeFolders struct {
	repo.ProjectFolderRepo
	rows []*ent.ProjectFolder
	err  error
}

func (f fakeFolders) ListAll(context.Context) ([]*ent.ProjectFolder, error) {
	return f.rows, f.err
}

func kontorFolders() fakeFolders {
	return fakeFolders{rows: []*ent.ProjectFolder{{
		Path:  "/code/agent-dashboard",
		Edges: ent.ProjectFolderEdges{Project: &ent.Project{ID: "proj-1", Name: "kontor"}},
	}}}
}

func TestProjectFolderEnricher_ResolvesEveryAgentByFolder(t *testing.T) {
	enrich := NewProjectFolderEnricher(kontorFolders())
	agents := []sdk.Agent{
		{SessionID: "sess-adhoc", CWD: "/code/agent-dashboard/server", ProjectName: "server"},
		{SessionID: "sess-other", CWD: "/code/elsewhere", ProjectName: "elsewhere"},
	}
	enrich(context.Background(), agents)

	require.Equal(t, "proj-1", agents[0].ProjectID)
	require.Equal(t, "kontor", agents[0].ProjectName, "an ad-hoc agent in a project folder gets that project")
	require.Empty(t, agents[1].ProjectID)
	require.Equal(t, "elsewhere", agents[1].ProjectName, "an agent outside every project folder keeps its folder name")
}

func TestProjectFolderEnricher_LookupErrorLeavesAgentsUntouched(t *testing.T) {
	enrich := NewProjectFolderEnricher(fakeFolders{err: errors.New("db down")})
	agents := []sdk.Agent{{CWD: "/code/agent-dashboard", ProjectName: "agent-dashboard"}}
	enrich(context.Background(), agents)

	require.Empty(t, agents[0].ProjectID)
	require.Equal(t, "agent-dashboard", agents[0].ProjectName)
}

func TestEnricherChain_TaskProjectWinsOverFolderMatch(t *testing.T) {
	taskProject := "proj-2"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {TaskID: "task-1", SessionID: sessionPtr("sess-1")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "T", ProjectID: &taskProject},
	}}
	projects := fakeProjects{byID: map[string]*ent.Project{
		"proj-2": {ID: "proj-2", Name: "website"},
	}}

	enrich := merger.ChainEnrichers(
		NewProjectFolderEnricher(kontorFolders()),
		NewPipelineTaskEnricher(stageRuns, tasks, nil, nil, nil, projects),
	)
	agents := []sdk.Agent{
		{SessionID: "sess-1", CWD: "/code/agent-dashboard/.worktrees/t", ProjectName: "t"},
		{SessionID: "sess-adhoc", CWD: "/code/agent-dashboard", ProjectName: "agent-dashboard"},
	}
	enrich(context.Background(), agents)

	require.Equal(t, "proj-2", agents[0].ProjectID, "the task's project overrides the folder match")
	require.Equal(t, "website", agents[0].ProjectName)
	require.Equal(t, "proj-1", agents[1].ProjectID, "an ad-hoc agent in the same folder still resolves by folder")
	require.Equal(t, "kontor", agents[1].ProjectName)
}

func TestPipelineTaskEnricher_DeniedByDefault_IsPerTaskNotPerTool(t *testing.T) {
	const capName = "mcp__mail__imap_send_email"
	stageRuns := fakeStageRuns{bySession: map[string]*ent.StageRun{
		"sess-1": {ID: "sr-1", TaskID: "task-1", SessionID: sessionPtr("sess-1")},
		"sess-2": {ID: "sr-2", TaskID: "task-2", SessionID: sessionPtr("sess-2")},
	}}
	tasks := fakeTasks{byID: map[string]*ent.Task{
		"task-1": {ID: "task-1", Title: "A"},
		"task-2": {ID: "task-2", Title: "B"},
	}}
	perms := fakePermissions{byStageRun: map[string][]*ent.PermissionRequest{
		"sr-1": {{ID: "req-1", StageRunID: "sr-1", Tool: capName, RequestedAt: time.Now()}},
		"sr-2": {{ID: "req-2", StageRunID: "sr-2", Tool: capName, RequestedAt: time.Now()}},
	}}
	// The deny is scoped to task-1 only.
	taskDeny := &ent.Grant{ID: "grant-task", CapabilityName: capName, ContextKind: "task", ContextRef: "task-1", Mode: "deny"}
	grants := &fakeGrants{byCapability: map[string][]*ent.Grant{capName: {taskDeny}}}

	enrich := NewPipelineTaskEnricher(stageRuns, tasks, perms, grants, fakeCapabilities{}, nil)
	agents := []sdk.Agent{{SessionID: "sess-1"}, {SessionID: "sess-2"}}
	enrich(context.Background(), agents)

	require.True(t, agents[0].PendingPermissions[0].DeniedByDefault, "task-1 carries the deny")
	require.False(t, agents[1].PendingPermissions[0].DeniedByDefault,
		"another task in the same tick must not inherit task-1's answer from the cache")
}
