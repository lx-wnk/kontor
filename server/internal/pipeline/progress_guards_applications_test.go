package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

type countingAgentHandler struct {
	stage string
	runs  atomic.Int32
}

func (h *countingAgentHandler) Stage() string       { return h.stage }
func (h *countingAgentHandler) RequiresAgent() bool { return true }
func (h *countingAgentHandler) Execute(_ *pipeline.StageContext) (pipeline.StageTransition, error) {
	h.runs.Add(1)
	return pipeline.FailTransition{Reason: "handler reached"}, nil
}

type upFrontFixture struct {
	orch    *pipeline.PipelineOrchestrator
	task    *ent.Task
	handler *countingAgentHandler
	srRepo  repo.StageRunRepo
}

// newUpFrontFixture builds an orchestrator whose spawner resolver gives
// implementation and self_review spawner A and finalization spawner B.
// tweak, when set, edits the options before the orchestrator is built.
func newUpFrontFixture(t *testing.T, resolve func(ctx context.Context, task *ent.Task, spawner *ent.Spawner, stage string) (mcpapps.RunApplications, error), spawners *[2]*ent.Spawner, tweak ...func(*pipeline.OrchestratorOptions)) upFrontFixture {
	t.Helper()
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	c := bundle.Client

	spawnerRepo := repo.NewSpawnerRepo(c)
	for i, name := range []string{"A", "B"} {
		spawners[i], err = spawnerRepo.Create(ctx, name, "sp-"+name, "claude", nil, nil, nil, nil, "", nil, false)
		require.NoError(t, err)
	}
	spawnerFor := func(stage string) *ent.Spawner {
		if stage == "finalization" {
			return spawners[1]
		}
		return spawners[0]
	}

	taskRepo := repo.NewTaskRepo(c)
	srRepo := repo.NewStageRunRepo(c)
	opts := pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: repo.NewPermissionRepo(c),
		AuditRepo:      repo.NewAuditEventRepo(c),
		ConfigRepo:     repo.NewPipelineConfigRepo(c),
		ResolveSpawner: func(_ context.Context, _, stage string) (*ent.Spawner, error) {
			return spawnerFor(stage), nil
		},
		ResolveApplications: resolve,
	}
	for _, fn := range tweak {
		fn(&opts)
	}
	orch, err := pipeline.NewOrchestrator(opts)
	require.NoError(t, err)

	handler := &countingAgentHandler{stage: "implementation"}
	orch.SetHandlerOverride("implementation", handler)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "up-front", Title: "t", Cwd: "/tmp", CurrentStage: "implementation",
		Priority: "medium", MaxIterations: 3, Applications: []string{"res-mail"},
	})
	require.NoError(t, err)
	return upFrontFixture{orch: orch, task: task, handler: handler, srRepo: srRepo}
}

// newMailFixture wires the real resolver over one explicitly attached, non-attach-all
// application "mail" that lists only spawner A (or every spawner when restricted is false).
func newMailFixture(t *testing.T, restricted bool, tweak ...func(*pipeline.OrchestratorOptions)) upFrontFixture {
	t.Helper()
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	appRepo := repo.NewMCPApplicationRepo(bundle.Client)
	resolver := mcpapps.Resolver{
		Apps:         appRepo,
		Secrets:      repo.NewApplicationSecretRepo(bundle.Client, box),
		Grants:       repo.NewGrantRepo(bundle.Client),
		Capabilities: repo.NewCapabilityRepo(bundle.Client),
	}
	var spawners [2]*ent.Spawner
	f := newUpFrontFixture(t, resolver.ResolveRun, &spawners, tweak...)

	_, err = appRepo.Upsert(ctx, repo.UpsertMCPApplicationInput{
		ResourceID: "res-mail", ServerName: "mail", Entry: json.RawMessage(`{"command":"npx","args":["mail"]}`),
	})
	require.NoError(t, err)
	if restricted {
		_, err = appRepo.SetAllowedSpawners(ctx, "res-mail", []string{spawners[0].ID})
		require.NoError(t, err)
	}
	return f
}

func (f upFrontFixture) progress(t *testing.T) *ent.StageRun {
	t.Helper()
	run, err := f.orch.ProgressTask(context.Background(), f.task.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, run)
	got, err := f.srRepo.GetByID(context.Background(), run.ID)
	require.NoError(t, err)
	return got
}

func TestProgressTask_RejectsWhenALaterStageSpawnerIsNotAllowed(t *testing.T) {
	f := newMailFixture(t, true)

	got := f.progress(t)
	require.Equal(t, "failed", got.Status)
	require.Contains(t, got.Output["error"], "MCP applications:")
	require.Contains(t, got.Output["error"], "finalization")
	require.Zero(t, f.handler.runs.Load(), "no agent may be spawned")
}

func TestProgressTask_UpFrontCheckSkipsNonClaudeStages(t *testing.T) {
	ollamaLater := func(o *pipeline.OrchestratorOptions) {
		sp := o.ResolveSpawner
		o.ResolveSpawner = func(ctx context.Context, taskID, stage string) (*ent.Spawner, error) {
			if stage == "finalization" {
				return &ent.Spawner{ID: "sp-ollama", AdapterType: "ollama"}, nil
			}
			return sp(ctx, taskID, stage)
		}
	}
	f := newMailFixture(t, true, ollamaLater)

	got := f.progress(t)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "handler reached", got.Output["error"])
	require.EqualValues(t, 1, f.handler.runs.Load(), "a stage that never receives applications must not block the task")
}

func TestProgressTask_UpFrontCheckAppliesToARetryAfterFailure(t *testing.T) {
	f := newMailFixture(t, true)

	first := f.progress(t)
	second := f.progress(t)

	require.NotEqual(t, first.ID, second.ID, "a retry starts a new iteration")
	require.Equal(t, "failed", second.Status)
	require.Contains(t, second.Output["error"], "finalization")
	require.Zero(t, f.handler.runs.Load(), "a retry must not spawn the earlier stages either")
}

func TestProgressTask_LaterStageSpawnerResolveErrorFailsClosedForRestrictedApp(t *testing.T) {
	failLater := func(o *pipeline.OrchestratorOptions) {
		sp := o.ResolveSpawner
		o.ResolveSpawner = func(ctx context.Context, taskID, stage string) (*ent.Spawner, error) {
			if stage == "finalization" {
				return nil, errors.New("spawner lookup failed")
			}
			return sp(ctx, taskID, stage)
		}
	}

	restricted := newMailFixture(t, true, failLater)
	got := restricted.progress(t)
	require.Equal(t, "failed", got.Status)
	require.Contains(t, got.Output["error"], mcpapps.UnresolvedSpawner)
	require.Zero(t, restricted.handler.runs.Load())

	unrestricted := newMailFixture(t, false, failLater)
	unrestricted.progress(t)
	require.Equal(t, int32(1), unrestricted.handler.runs.Load(), "an unrestricted application does not fail on an unresolved spawner")
}

func TestProgressTask_CurrentStageSpawnerResolveErrorIsLeftToTheStageGate(t *testing.T) {
	f := newMailFixture(t, true, func(o *pipeline.OrchestratorOptions) {
		sp := o.ResolveSpawner
		o.ResolveSpawner = func(ctx context.Context, taskID, stage string) (*ent.Spawner, error) {
			if stage == "implementation" {
				return nil, errors.New("spawner lookup failed")
			}
			return sp(ctx, taskID, "self_review")
		}
	})

	got := f.progress(t)
	require.Equal(t, int32(1), f.handler.runs.Load(), "the up-front check must not blame the current stage's spawner")
	require.NotContains(t, got.Output["error"], mcpapps.UnresolvedSpawner)
}

func TestProgressTask_UpFrontCheckIsSkippedWithoutBothResolvers(t *testing.T) {
	tests := map[string]func(*pipeline.OrchestratorOptions){
		"no ResolveApplications": func(o *pipeline.OrchestratorOptions) { o.ResolveApplications = nil },
		"no ResolveSpawner":      func(o *pipeline.OrchestratorOptions) { o.ResolveSpawner = nil },
	}
	for name, tweak := range tests {
		t.Run(name, func(t *testing.T) {
			f := newMailFixture(t, true, tweak)
			f.progress(t)
			require.Equal(t, int32(1), f.handler.runs.Load(), "the stage must start")
		})
	}
}

func TestProgressTask_UpFrontCheckIgnoresOtherResolveErrors(t *testing.T) {
	ctx := context.Background()
	var spawners [2]*ent.Spawner
	f := newUpFrontFixture(t, func(_ context.Context, _ *ent.Task, _ *ent.Spawner, stage string) (mcpapps.RunApplications, error) {
		if stage == "finalization" {
			return mcpapps.RunApplications{}, &mcpapps.MissingSecretError{Server: "mail", EnvName: "MAIL_PASSWORD"}
		}
		return mcpapps.RunApplications{}, nil
	}, &spawners)

	_, err := f.orch.ProgressTask(ctx, f.task.ID, nil)
	require.NoError(t, err)
	require.Equal(t, int32(1), f.handler.runs.Load(), "a secret missing on a later stage must not stop this stage")
}
