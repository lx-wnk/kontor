package serverapp

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

type idleOrchestrator struct{}

func (idleOrchestrator) ProgressTask(context.Context, string, *pipeline.ProgressOpts) (*ent.StageRun, error) {
	return nil, nil
}
func (idleOrchestrator) ResumeFromUser(context.Context, string, string) (*ent.StageRun, error) {
	return nil, nil
}
func (idleOrchestrator) RequeueForUser(context.Context, string, string) (*ent.StageRun, error) {
	return nil, nil
}
func (idleOrchestrator) NotifyTaskTerminated(context.Context, string, string) {}
func (idleOrchestrator) InvalidateConfigCache()                               {}
func (idleOrchestrator) ClearStalePendingPermissions(context.Context, string) {}
func (idleOrchestrator) EffectiveStageModel(context.Context, string) string   { return "" }

// TestProvideScheduler_RunModeReachesTheCreatedTask drives the real wiring from
// a routine to the stored task: the materializer decides kind, stage and
// autonomy, but only the composition root carries them into task creation.
func TestProvideScheduler_RunModeReachesTheCreatedTask(t *testing.T) {
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	tb := sse.NewTaskBroadcaster(sse.NewBroadcaster())
	taskRepo := repo.NewTaskRepo(bundle.Client)
	h := tasks.NewHandler(tasks.Deps{
		TaskRepo:     taskRepo,
		SRRepo:       repo.NewStageRunRepo(bundle.Client),
		PermRepo:     repo.NewPermissionRepo(bundle.Client),
		AuditRepo:    repo.NewAuditEventRepo(bundle.Client),
		CfgRepo:      repo.NewPipelineConfigRepo(bundle.Client),
		ProjectRepo:  repo.NewProjectRepo(bundle.Client),
		Orchestrator: idleOrchestrator{},
		Broadcaster:  tb,
		BypassAuth:   true,
	})
	sched, _ := provideScheduler(bundle.Client, h, tb, true)
	if sched == nil {
		t.Fatal("provideScheduler returned no scheduler")
	}
	schedRepo := repo.NewTaskScheduleRepo(bundle.Client)

	cases := []struct {
		runMode, kind, stage string
	}{
		{repo.RunModeJob, pipeline.TaskKindJob, pipeline.StageJob},
		{repo.RunModePipeline, pipeline.TaskKindPipeline, "ready"},
	}
	for _, tc := range cases {
		t.Run(tc.runMode, func(t *testing.T) {
			s, err := schedRepo.Create(ctx, repo.CreateTaskScheduleInput{
				Name: "routine-" + tc.runMode, CronExpr: "0 9 * * *", SlugPrefix: "routine-" + tc.runMode,
				Title: "Routine", Cwd: t.TempDir(), MaxIterations: 20,
				RunMode: tc.runMode,
			})
			if err != nil {
				t.Fatalf("create schedule: %v", err)
			}
			taskID, err := sched.RunNow(ctx, s.ID)
			if err != nil {
				t.Fatalf("RunNow: %v", err)
			}
			got, err := taskRepo.GetByID(ctx, taskID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if got.Kind != tc.kind || got.CurrentStage != tc.stage || got.Autonomy != "full" {
				t.Fatalf("task kind=%q stage=%q autonomy=%q, want %q %q \"full\"", got.Kind, got.CurrentStage, got.Autonomy, tc.kind, tc.stage)
			}
		})
	}
}
