package serverapp

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/api/schedules"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// provideScheduler wires the schedule firing engine and its REST handler. It
// reuses the task handler's CreateTaskFromInput core via an injected closure, so
// the scheduler package never imports api/tasks (leaf boundary). The LLM
// translator fallback is injected here (nil = rule-based only); firing reads
// only the stored cron and never needs the LLM.
func provideScheduler(client *ent.Client, taskHandler *tasks.Handler, tb *sse.TaskBroadcaster, bypassAuth bool) (*scheduler.Scheduler, *schedules.Handler) {
	if client == nil || taskHandler == nil {
		return nil, nil
	}
	schedRepo := repo.NewTaskScheduleRepo(client)
	taskRepo := repo.NewTaskRepo(client)
	permRepo := repo.NewPermissionRepo(client)
	cfgRepo := repo.NewPipelineConfigRepo(client)

	createFn := func(ctx context.Context, spec scheduler.NewTaskSpec) (string, error) {
		enriched, err := taskHandler.CreateTaskFromInput(ctx, tasks.CreateTaskParams{
			Slug:            spec.Slug,
			Title:           spec.Title,
			Description:     spec.Description,
			Cwd:             spec.Cwd,
			Priority:        spec.Priority,
			SilverBullet:    spec.SilverBullet,
			MaxIterations:   spec.MaxIterations,
			SourceBranch:    spec.SourceBranch,
			TargetBranch:    spec.TargetBranch,
			TokenBudget:     spec.TokenBudget,
			CostBudgetCents: spec.CostBudgetCents,
			ProjectID:       spec.ProjectID,
			SpawnerID:       spec.SpawnerID,
			RoutineID:       spec.RoutineID,
			Applications:    spec.Applications,
			UserID:          spec.UserID,
			Metadata:        spec.Metadata,
			Kind:            spec.Kind,
			Stage:           spec.Stage,
			Autonomy:        spec.Autonomy,
		})
		if err != nil {
			return "", err
		}
		return enriched.ID, nil
	}

	materializer := scheduler.NewMaterializer(createFn, taskRepo, permRepo)
	onChange := func(scheduleID string) {
		tb.Broadcast(sse.TaskEvent{Type: "schedule_changed", TaskID: scheduleID, Payload: map[string]string{}})
	}
	sched := scheduler.New(scheduler.Options{
		Schedules:    schedRepo,
		Tasks:        taskRepo,
		Config:       cfgRepo,
		Materializer: materializer,
		OnChange:     onChange,
	})

	// Rule-based fast-path with a one-shot LLM fallback for the long tail;
	// firing stays deterministic and offline (it reads only the stored cron).
	translator := scheduler.NewNLCron(scheduler.NewExecLLMTranslator())
	handler := schedules.NewHandler(schedRepo, translator, sched, repo.NewMCPApplicationRepo(client), taskRepo, repo.NewStageRunRepo(client), bypassAuth)
	return sched, handler
}
