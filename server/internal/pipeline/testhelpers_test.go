package pipeline_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// ptr returns a pointer to v. Used in table-driven tests that need *T fields.
func ptr[T any](v T) *T { return &v }

// stubStageHandler is a test double that returns a predetermined transition.
type stubStageHandler struct {
	stage      string
	transition pipeline.StageTransition
}

func (h *stubStageHandler) Stage() string       { return h.stage }
func (h *stubStageHandler) RequiresAgent() bool { return false }
func (h *stubStageHandler) Execute(_ *pipeline.StageContext) (pipeline.StageTransition, error) {
	return h.transition, nil
}

// makeTestOrchestratorWithRepos opens an in-memory SQLite DB and returns an orchestrator + task repo.
func makeTestOrchestratorWithRepos(t *testing.T) (*pipeline.PipelineOrchestrator, repo.TaskRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: permRepo,
		AuditRepo:      auditRepo,
		ConfigRepo:     cfgRepo,
	})
	require.NoError(t, err)
	return orch, taskRepo
}

// makeTestOrchestrator is a convenience wrapper for tests that only need the orchestrator.
func makeTestOrchestrator(t *testing.T) *pipeline.PipelineOrchestrator {
	t.Helper()
	orch, _ := makeTestOrchestratorWithRepos(t)
	return orch
}

// capturedEvents records OnTaskChanged calls for assertion in dep/cascade tests.
type capturedEvents struct {
	mu     sync.Mutex
	events []taskChangedEvent
}

type taskChangedEvent struct {
	taskID string
	reason string
}

func (c *capturedEvents) record(taskID, reason string, _ any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, taskChangedEvent{taskID: taskID, reason: reason})
}

func (c *capturedEvents) all() []taskChangedEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]taskChangedEvent, len(c.events))
	copy(out, c.events)
	return out
}

// makeTestOrchestratorWithDeps opens an in-memory SQLite DB and returns an
// orchestrator wired with a real DependencyRepo and an OnTaskChanged recorder.
// maxParallelOrchestrators is NOT set via a config row; the default (3) provides
// enough free slots for picker tests that pass at most 2 tasks.
func makeTestOrchestratorWithDeps(t *testing.T) (*pipeline.PipelineOrchestrator, repo.TaskRepo, repo.DependencyRepo, *capturedEvents) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	client := bundle.Client
	t.Cleanup(func() { _ = client.Close() })

	taskRepo := repo.NewTaskRepo(client)
	srRepo := repo.NewStageRunRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	auditRepo := repo.NewAuditEventRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)
	depRepo := repo.NewDependencyRepo(client)

	events := &capturedEvents{}

	orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
		TaskRepo:       taskRepo,
		StageRunRepo:   srRepo,
		PermissionRepo: permRepo,
		AuditRepo:      auditRepo,
		ConfigRepo:     cfgRepo,
		DepRepo:        depRepo,
		OnTaskChanged:  events.record,
	})
	require.NoError(t, err)
	return orch, taskRepo, depRepo, events
}
