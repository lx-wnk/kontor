package repo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/taskschedule"
)

// TaskScheduleRepo persists recurring task-schedule definitions.
type TaskScheduleRepo interface {
	Create(ctx context.Context, in CreateTaskScheduleInput) (*ent.TaskSchedule, error)
	GetByID(ctx context.Context, id string) (*ent.TaskSchedule, error)
	Update(ctx context.Context, id string, in UpdateTaskScheduleInput) (*ent.TaskSchedule, error)
	Delete(ctx context.Context, id string) error
	// ListForUser returns userID's schedules, or every schedule when unscoped
	// is set — the loopback single-user mode, where there is one implicit user.
	ListForUser(ctx context.Context, userID string, unscoped bool) ([]*ent.TaskSchedule, error)
	ListEnabled(ctx context.Context) ([]*ent.TaskSchedule, error)
	ListDue(ctx context.Context, now time.Time) ([]*ent.TaskSchedule, error)
	SetEnabled(ctx context.Context, id string, enabled bool) (*ent.TaskSchedule, error)
	UpdateFireState(ctx context.Context, id string, in FireStateInput) (*ent.TaskSchedule, error)
	// RecordSkip increments skipped_count and stamps last_skipped_at for a fire
	// refused because the routine's previous run was still in flight.
	RecordSkip(ctx context.Context, id string, at time.Time) (*ent.TaskSchedule, error)
	// ListForModule returns the routines a module owns.
	ListForModule(ctx context.Context, moduleID string) ([]*ent.TaskSchedule, error)
	// DisableForModule takes a module's own routines out of service and
	// reports how many it touched. A routine outlives the module that brought
	// it only as a disabled row, so nothing fires for a module that is gone
	// and nothing the operator wrote is lost.
	DisableForModule(ctx context.Context, moduleID string) (int, error)
}

// Run modes select what a fired schedule spawns: a single job task or a full
// pipeline. Empty CreateTaskScheduleInput.RunMode defaults to RunModeJob.
const (
	RunModeJob      = "job"
	RunModePipeline = "pipeline"
)

// IsValidRunMode reports whether mode is one of the known run modes.
func IsValidRunMode(mode string) bool {
	return mode == RunModeJob || mode == RunModePipeline
}

// CreateTaskScheduleInput carries the schedule definition plus its task template.
type CreateTaskScheduleInput struct {
	// OwnerModule names the module that brought this routine, empty for one a
	// human created. It decides who may change the row later.
	OwnerModule         string
	Name                string
	Enabled             *bool
	NLText              *string
	CronExpr            string
	Timezone            string
	Catchup             string
	SlugPrefix          string
	Title               string
	Description         *string
	Cwd                 string
	SourceBranch        *string
	TargetBranch        *string
	Priority            string
	MaxIterations       int
	TokenBudget         *int
	CostBudgetCents     *int
	StageTimeoutSeconds int
	SilverBullet        bool
	ProjectID           *string
	SpawnerID           *string
	PermissionTemplate  *string
	Metadata            map[string]any
	UserID              *string
	NextRunAt           *time.Time
	Applications        []string
	RunMode             string
}

// UpdateTaskScheduleInput patches schedule fields. Nil pointers leave a field
// unchanged. Template fields and the cron definition can all be edited.
type UpdateTaskScheduleInput struct {
	Name                *string
	Enabled             *bool
	NLText              *string
	CronExpr            *string
	Timezone            *string
	Catchup             *string
	SlugPrefix          *string
	Title               *string
	Description         *string
	Cwd                 *string
	SourceBranch        *string
	TargetBranch        *string
	Priority            *string
	MaxIterations       *int
	TokenBudget         *int
	CostBudgetCents     *int
	StageTimeoutSeconds *int
	SilverBullet        *bool
	ProjectID           *string
	SpawnerID           *string
	PermissionTemplate  *string
	Metadata            map[string]any
	NextRunAt           *time.Time
	Applications        *[]string
	RunMode             *string
}

// FireStateInput records the result of a fire: the spawned task and the next
// scheduled run. LastRunAt is set to the fire time.
type FireStateInput struct {
	LastRunAt  time.Time
	LastTaskID *string
	NextRunAt  *time.Time
}

type entTaskScheduleRepo struct {
	client    *ent.Client
	resources ResourceRepo
}

// NewTaskScheduleRepo returns a TaskScheduleRepo that propagates every lifecycle
// change to the resource registry. The registry repo is built here rather than
// injected: it is a stateless wrapper over the same client, nothing substitutes
// it, and an injected one only offers each call site a way to pass nil and
// silently lose the sync — which is what every mutating call site did.
func NewTaskScheduleRepo(client *ent.Client) TaskScheduleRepo {
	return &entTaskScheduleRepo{client: client, resources: NewResourceRepo(client)}
}

func (r *entTaskScheduleRepo) Create(ctx context.Context, in CreateTaskScheduleInput) (*ent.TaskSchedule, error) {
	q := r.client.TaskSchedule.Create().
		SetID(uuid.New().String()).
		SetName(in.Name).
		SetCronExpr(in.CronExpr).
		SetSlugPrefix(in.SlugPrefix).
		SetTitle(in.Title).
		SetCwd(in.Cwd).
		SetMaxIterations(in.MaxIterations).
		SetStageTimeoutSeconds(in.StageTimeoutSeconds).
		SetSilverBullet(in.SilverBullet).
		SetOwnerModule(in.OwnerModule)

	if in.Enabled != nil {
		q = q.SetEnabled(*in.Enabled)
	}
	if in.Timezone != "" {
		q = q.SetTimezone(in.Timezone)
	}
	if in.Catchup != "" {
		q = q.SetCatchup(in.Catchup)
	}
	if in.Priority != "" {
		q = q.SetPriority(in.Priority)
	}
	if in.RunMode != "" {
		if !IsValidRunMode(in.RunMode) {
			return nil, fmt.Errorf("task_schedule.create: invalid run mode %q", in.RunMode)
		}
		q = q.SetRunMode(in.RunMode)
	}
	q = q.SetNillableNlText(in.NLText).
		SetNillableDescription(in.Description).
		SetNillableSourceBranch(in.SourceBranch).
		SetNillableTargetBranch(in.TargetBranch).
		SetNillableTokenBudget(in.TokenBudget).
		SetNillableCostBudgetCents(in.CostBudgetCents).
		SetNillableProjectID(in.ProjectID).
		SetNillableSpawnerID(in.SpawnerID).
		SetNillablePermissionTemplate(in.PermissionTemplate).
		SetNillableUserID(in.UserID).
		SetNillableNextRunAt(in.NextRunAt)
	if in.Metadata != nil {
		q = q.SetMetadata(in.Metadata)
	}
	if in.Applications != nil {
		q = q.SetApplications(in.Applications)
	}

	s, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.Create: %w", err)
	}

	if _, err := UpsertScheduleResource(ctx, r.resources, r.client, s); err != nil {
		slog.Warn("taskschedule.Create: resource upsert failed, reconciler will catch it", "schedule_id", s.ID, "err", err)
	}
	return s, nil
}

func (r *entTaskScheduleRepo) ListForModule(ctx context.Context, moduleID string) ([]*ent.TaskSchedule, error) {
	if moduleID == "" {
		return nil, nil
	}
	rows, err := r.client.TaskSchedule.Query().
		Where(taskschedule.OwnerModuleEQ(moduleID)).
		Order(ent.Asc(taskschedule.FieldName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.ListForModule: %w", err)
	}
	return rows, nil
}

func (r *entTaskScheduleRepo) DisableForModule(ctx context.Context, moduleID string) (int, error) {
	// Guarded here rather than trusted to the caller: every routine a human
	// created carries an empty owner, so an empty id would disable all of them.
	if moduleID == "" {
		return 0, nil
	}
	n, err := r.client.TaskSchedule.Update().
		Where(taskschedule.OwnerModuleEQ(moduleID), taskschedule.EnabledEQ(true)).
		SetEnabled(false).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("taskschedule.DisableForModule: %w", err)
	}
	return n, nil
}

func (r *entTaskScheduleRepo) GetByID(ctx context.Context, id string) (*ent.TaskSchedule, error) {
	s, err := r.client.TaskSchedule.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.GetByID: %w", err)
	}
	return s, nil
}

func (r *entTaskScheduleRepo) Update(ctx context.Context, id string, in UpdateTaskScheduleInput) (*ent.TaskSchedule, error) {
	q := r.client.TaskSchedule.UpdateOneID(id).SetUpdatedAt(time.Now())
	if in.Name != nil {
		q = q.SetName(*in.Name)
	}
	if in.Enabled != nil {
		q = q.SetEnabled(*in.Enabled)
	}
	if in.CronExpr != nil {
		q = q.SetCronExpr(*in.CronExpr)
	}
	if in.Timezone != nil {
		q = q.SetTimezone(*in.Timezone)
	}
	if in.Catchup != nil {
		q = q.SetCatchup(*in.Catchup)
	}
	if in.SlugPrefix != nil {
		q = q.SetSlugPrefix(*in.SlugPrefix)
	}
	if in.Title != nil {
		q = q.SetTitle(*in.Title)
	}
	if in.Cwd != nil {
		q = q.SetCwd(*in.Cwd)
	}
	if in.Priority != nil {
		q = q.SetPriority(*in.Priority)
	}
	if in.MaxIterations != nil {
		q = q.SetMaxIterations(*in.MaxIterations)
	}
	if in.StageTimeoutSeconds != nil {
		q = q.SetStageTimeoutSeconds(*in.StageTimeoutSeconds)
	}
	if in.SilverBullet != nil {
		q = q.SetSilverBullet(*in.SilverBullet)
	}
	if in.RunMode != nil {
		if !IsValidRunMode(*in.RunMode) {
			return nil, fmt.Errorf("task_schedule.update: invalid run mode %q", *in.RunMode)
		}
		q = q.SetRunMode(*in.RunMode)
	}
	// Nillable pointer fields: a non-nil pointer sets the value. Clearing is not
	// exposed here (absent = unchanged) to mirror the task repo's update contract.
	q = q.SetNillableNlText(in.NLText).
		SetNillableDescription(in.Description).
		SetNillableSourceBranch(in.SourceBranch).
		SetNillableTargetBranch(in.TargetBranch).
		SetNillableTokenBudget(in.TokenBudget).
		SetNillableCostBudgetCents(in.CostBudgetCents).
		SetNillableProjectID(in.ProjectID).
		SetNillableSpawnerID(in.SpawnerID).
		SetNillablePermissionTemplate(in.PermissionTemplate).
		SetNillableNextRunAt(in.NextRunAt)
	if in.Metadata != nil {
		q = q.SetMetadata(in.Metadata)
	}
	if in.Applications != nil {
		q = q.SetApplications(*in.Applications)
	}
	s, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.Update: %w", err)
	}

	// Sync name and enabled changes to the resource row.
	if s.ResourceID != "" {
		if in.Name != nil || in.Enabled != nil {
			if _, uErr := UpsertScheduleResource(ctx, r.resources, r.client, s); uErr != nil {
				slog.Warn("taskschedule.Update: resource sync failed", "schedule_id", id, "err", uErr)
			}
		}
	}
	return s, nil
}

func (r *entTaskScheduleRepo) Delete(ctx context.Context, id string) error {
	s, err := r.client.TaskSchedule.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("taskschedule.Delete: %w", err)
	}
	resourceID := s.ResourceID

	if err := r.client.TaskSchedule.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("taskschedule.Delete: %w", err)
	}

	if resourceID != "" {
		if err := OrphanScheduleResource(ctx, r.resources, resourceID); err != nil {
			slog.Warn("taskschedule.Delete: resource orphan failed", "schedule_id", id, "err", err)
		}
	}
	return nil
}

func (r *entTaskScheduleRepo) ListForUser(ctx context.Context, userID string, unscoped bool) ([]*ent.TaskSchedule, error) {
	q := r.client.TaskSchedule.Query().Order(ent.Asc(taskschedule.FieldCreatedAt))
	if !unscoped {
		q = q.Where(taskschedule.UserID(userID))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.ListForUser: %w", err)
	}
	return rows, nil
}

func (r *entTaskScheduleRepo) ListEnabled(ctx context.Context) ([]*ent.TaskSchedule, error) {
	rows, err := r.client.TaskSchedule.Query().Where(taskschedule.Enabled(true)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.ListEnabled: %w", err)
	}
	return rows, nil
}

// ListDue returns enabled schedules whose next_run_at is non-null and at or
// before now. Schedules with a null next_run_at are excluded — the scheduler
// initializes next_run_at on first observation rather than firing immediately.
func (r *entTaskScheduleRepo) ListDue(ctx context.Context, now time.Time) ([]*ent.TaskSchedule, error) {
	rows, err := r.client.TaskSchedule.Query().
		Where(
			taskschedule.Enabled(true),
			taskschedule.NextRunAtNotNil(),
			taskschedule.NextRunAtLTE(now),
		).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.ListDue: %w", err)
	}
	return rows, nil
}

func (r *entTaskScheduleRepo) SetEnabled(ctx context.Context, id string, enabled bool) (*ent.TaskSchedule, error) {
	s, err := r.client.TaskSchedule.UpdateOneID(id).SetEnabled(enabled).SetUpdatedAt(time.Now()).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.SetEnabled: %w", err)
	}

	if s.ResourceID != "" {
		state := ResourceStateDisabled
		if enabled {
			state = ResourceStateEnabled
		}
		if _, err := r.resources.SetState(ctx, s.ResourceID, state); err != nil {
			slog.Warn("taskschedule.SetEnabled: resource state sync failed", "schedule_id", id, "err", err)
		}
	}
	return s, nil
}

func (r *entTaskScheduleRepo) UpdateFireState(ctx context.Context, id string, in FireStateInput) (*ent.TaskSchedule, error) {
	q := r.client.TaskSchedule.UpdateOneID(id).
		SetLastRunAt(in.LastRunAt).
		SetUpdatedAt(time.Now()).
		SetNillableLastTaskID(in.LastTaskID).
		SetNillableNextRunAt(in.NextRunAt)
	s, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.UpdateFireState: %w", err)
	}
	return s, nil
}

func (r *entTaskScheduleRepo) RecordSkip(ctx context.Context, id string, at time.Time) (*ent.TaskSchedule, error) {
	s, err := r.client.TaskSchedule.UpdateOneID(id).
		AddSkippedCount(1).
		SetLastSkippedAt(at).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("taskschedule.RecordSkip: %w", err)
	}
	return s, nil
}
