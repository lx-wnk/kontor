package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

const (
	// configKeyTickIntervalMs is the pipeline-config key for the tick cadence.
	configKeyTickIntervalMs = "scheduleTickIntervalMs"
	// configKeyCatchup is the global fallback catchup policy.
	configKeyCatchup = "scheduleCatchup"

	defaultTickIntervalMs = 30000
	defaultCatchup        = "none"

	catchupOnce = "once"
)

// terminalStages are the task stages that count as "finished" for overlap
// detection. A schedule's prior task in any other stage is still in flight.
var terminalStages = map[string]bool{
	"done":      true,
	"cancelled": true,
}

// taskGetter reads a task for overlap detection. Satisfied by repo.TaskRepo.
type taskGetter interface {
	GetByID(ctx context.Context, id string) (*ent.Task, error)
}

// Scheduler fires due TaskSchedules on a fixed tick. It runs as a dedicated
// errgroup worker alongside the pipeline orchestrator. Firing is driven only by
// the stored cron expression, so it is deterministic and offline-safe.
type Scheduler struct {
	schedules    repo.TaskScheduleRepo
	tasks        taskGetter
	cfg          repo.PipelineConfigRepo
	materializer *Materializer
	// now is injected so tests drive time deterministically. Defaults to time.Now.
	now func() time.Time
	// onChange broadcasts a schedule-changed event after a fire-state update.
	// May be nil.
	onChange func(scheduleID string)
}

// Options configures a Scheduler. Now and OnChange are optional.
type Options struct {
	Schedules    repo.TaskScheduleRepo
	Tasks        taskGetter
	Config       repo.PipelineConfigRepo
	Materializer *Materializer
	Now          func() time.Time
	OnChange     func(scheduleID string)
}

// New builds a Scheduler from Options.
func New(o Options) *Scheduler {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Scheduler{
		schedules:    o.Schedules,
		tasks:        o.Tasks,
		cfg:          o.Config,
		materializer: o.Materializer,
		now:          now,
		onChange:     o.OnChange,
	}
}

// Run drives the tick loop until ctx is cancelled. It returns nil on cancellation
// so it composes with errgroup alongside the other long-lived workers.
func (s *Scheduler) Run(ctx context.Context) error {
	interval := s.tickInterval(ctx)
	slog.Info("scheduler: starting", "tickIntervalMs", interval.Milliseconds())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.tick(ctx, s.now())
		}
	}
}

func (s *Scheduler) tickInterval(ctx context.Context) time.Duration {
	ms := defaultTickIntervalMs
	if s.cfg != nil {
		ms = int(s.cfg.GetNumber(ctx, configKeyTickIntervalMs, float64(defaultTickIntervalMs)))
	}
	if ms < 1000 {
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}

// tick performs one due-detection pass at the given wall-clock instant. Each
// schedule is processed in isolation: one schedule's failure never aborts the
// pass or the loop.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	s.initNextRuns(ctx, now)

	due, err := s.schedules.ListDue(ctx, now)
	if err != nil {
		slog.Error("scheduler: list due", "err", err)
		return
	}
	globalCatchup := s.globalCatchup(ctx)
	for _, sched := range due {
		s.fireOne(ctx, sched, now, globalCatchup)
	}
}

// initNextRuns assigns a next_run_at to enabled schedules that have none yet, so
// a freshly created schedule fires on its next cron boundary rather than
// immediately. It does not fire anything.
func (s *Scheduler) initNextRuns(ctx context.Context, now time.Time) {
	enabled, err := s.schedules.ListEnabled(ctx)
	if err != nil {
		slog.Error("scheduler: list enabled", "err", err)
		return
	}
	for _, sched := range enabled {
		if sched.NextRunAt != nil {
			continue
		}
		next, err := nextAfter(sched.CronExpr, sched.Timezone, now)
		if err != nil {
			slog.Error("scheduler: init next_run_at", "schedule", sched.ID, "err", err)
			continue
		}
		if _, err := s.schedules.Update(ctx, sched.ID, repo.UpdateTaskScheduleInput{NextRunAt: &next}); err != nil {
			slog.Error("scheduler: persist init next_run_at", "schedule", sched.ID, "err", err)
			continue
		}
		s.notify(sched.ID)
	}
}

// fireOne applies overlap + catchup policy to a single due schedule, optionally
// materializes a task, and always advances next_run_at so the schedule never
// stalls.
func (s *Scheduler) fireOne(ctx context.Context, sched *ent.TaskSchedule, now time.Time, globalCatchup string) {
	next, err := nextAfter(sched.CronExpr, sched.Timezone, now)
	if err != nil {
		slog.Error("scheduler: compute next", "schedule", sched.ID, "err", err)
		return
	}

	if s.overlaps(ctx, sched) {
		slog.Info("scheduler: skip fire (prior task in flight)", "schedule", sched.ID, "lastTask", deref(sched.LastTaskID))
		if _, err := s.schedules.RecordSkip(ctx, sched.ID, now); err != nil {
			slog.Warn("scheduler: record skipped fire", "schedule", sched.ID, "err", err)
		}
		s.advance(ctx, sched, next)
		return
	}

	catchup := sched.Catchup
	if catchup == "" {
		catchup = globalCatchup
	}
	// catchup=none skips the missed window entirely; catchup=once fires a single
	// catch-up run. Either way next_run_at advances to the next cron boundary.
	if catchup != catchupOnce {
		// A schedule whose next_run_at was only just initialized (last_run_at nil)
		// must not be treated as a "missed window" skip on its very first due tick.
		if sched.LastRunAt != nil {
			s.advance(ctx, sched, next)
			return
		}
	}

	taskID, err := s.materializer.Materialize(ctx, sched, now)
	if err != nil {
		slog.Error("scheduler: materialize", "schedule", sched.ID, "err", err)
		// Still advance so a persistent failure does not hot-loop every tick.
		s.advance(ctx, sched, next)
		return
	}
	slog.Info("scheduler: fired", "schedule", sched.ID, "task", taskID)
	if _, err := s.schedules.UpdateFireState(ctx, sched.ID, repo.FireStateInput{
		LastRunAt:  now,
		LastTaskID: &taskID,
		NextRunAt:  &next,
	}); err != nil {
		slog.Error("scheduler: update fire state", "schedule", sched.ID, "err", err)
		return
	}
	s.notify(sched.ID)
}

// RunNow materializes a task from the schedule immediately, ignoring cron
// timing, and records it as the last fire. next_run_at is left unchanged.
// Returns the new task ID.
func (s *Scheduler) RunNow(ctx context.Context, scheduleID string) (string, error) {
	sched, err := s.schedules.GetByID(ctx, scheduleID)
	if err != nil {
		return "", err
	}
	now := s.now()
	taskID, err := s.materializer.Materialize(ctx, sched, now)
	if err != nil {
		return "", err
	}
	if _, err := s.schedules.UpdateFireState(ctx, scheduleID, repo.FireStateInput{
		LastRunAt:  now,
		LastTaskID: &taskID,
		NextRunAt:  sched.NextRunAt,
	}); err != nil {
		return taskID, err
	}
	s.notify(scheduleID)
	return taskID, nil
}

// overlaps reports whether the schedule's previously fired task is still
// in-flight (non-terminal). A missing or unreadable prior task is treated as
// not-overlapping so a deleted task never blocks future fires.
func (s *Scheduler) overlaps(ctx context.Context, sched *ent.TaskSchedule) bool {
	if sched.LastTaskID == nil || *sched.LastTaskID == "" || s.tasks == nil {
		return false
	}
	t, err := s.tasks.GetByID(ctx, *sched.LastTaskID)
	if err != nil {
		return false
	}
	return !terminalStages[t.CurrentStage]
}

func (s *Scheduler) advance(ctx context.Context, sched *ent.TaskSchedule, next time.Time) {
	if _, err := s.schedules.Update(ctx, sched.ID, repo.UpdateTaskScheduleInput{NextRunAt: &next}); err != nil {
		slog.Error("scheduler: advance next_run_at", "schedule", sched.ID, "err", err)
		return
	}
	s.notify(sched.ID)
}

func (s *Scheduler) globalCatchup(ctx context.Context) string {
	if s.cfg == nil {
		return defaultCatchup
	}
	all, err := s.cfg.GetAll(ctx)
	if err != nil {
		return defaultCatchup
	}
	if v := all[configKeyCatchup]; v != "" {
		return v
	}
	return defaultCatchup
}

func (s *Scheduler) notify(scheduleID string) {
	if s.onChange != nil {
		s.onChange(scheduleID)
	}
}

// nextAfter computes the next cron fire time strictly after `now`, evaluated in
// the schedule's timezone (falling back to UTC on an unknown zone).
func nextAfter(cronExpr, timezone string, now time.Time) (time.Time, error) {
	sched, err := standardParser.Parse(cronExpr)
	if err != nil {
		return time.Time{}, err
	}
	loc := time.UTC
	if timezone != "" {
		if l, lerr := time.LoadLocation(timezone); lerr == nil {
			loc = l
		}
	}
	return sched.Next(now.In(loc)), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
