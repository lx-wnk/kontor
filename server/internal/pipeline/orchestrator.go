// Package pipeline implements the task pipeline state machine.
//
// File layout (all in package pipeline):
//
//	orchestrator.go     — PipelineOrchestrator struct, Run/tick entry, helpers
//	scheduler.go        — capacity + candidate selection, sort helpers (F-PERF-007, F-PERF-010)
//	sweeps.go           — sweepAwaitingUserRuns, sweepOrphanRuns
//	progress_guards.go  — runProgressTaskLocked (Re-entry Guard + Lingering-Pending Gate)
//	transitions.go      — applyTransition, applyTransitionWrites, decideCompletedTransition
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pricing"
	"github.com/lx-wnk/kontor/server/internal/proc"
)

const (
	defaultPollInterval        = 2 * time.Second
	stageTimeoutKey            = "stageTimeoutSeconds"
	defaultStageTimeoutSeconds = db.DefaultStageTimeoutSeconds
	awaitingUserTimeoutKey     = "awaitingUserTimeoutSeconds"
	defaultAwaitingUserTimeout = 14400 // 4h
	pendingStaleDuration       = 5 * time.Minute
	maxReviewCyclesKey         = "maxReviewCycles"
	defaultMaxReviewCycles     = 3
	maxAutoRetriesKey          = "maxAutoRetries"
	defaultMaxAutoRetries      = 3
	retryBackoffKey            = "retryBackoffSeconds"
	defaultRetryBackoff        = 60
	rateLimitBackoffKey        = "rateLimitBackoffSeconds"
	defaultRateLimitBackoff    = 600
	maxRateLimitRetriesKey     = "maxRateLimitRetries"
	defaultMaxRateLimitRetries = 36
)

// httpSpawnResult carries the outcome of an asynchronous HTTP-adapter spawn.
// Written by the goroutine pool worker and drained by tick() on the next cycle.
type httpSpawnResult struct {
	stageRunID  string
	taskID      string
	sessionFile string // path to synthetic session file written by the adapter
	err         error
}

// httpPool bounds concurrent HTTP-adapter dispatches by maxParallelOrchestrators —
// the same live setting the scheduler picks tasks by. The limit is re-read on
// every acquire attempt, including from a parked waiter, so raising the setting
// takes effect without a restart. A fixed-capacity channel semaphore sized at
// construction could not: a long-running adapter (an ACP turn runs up to 30
// minutes) would hold a stale slot for the whole turn.
type httpPool struct {
	cache  *configCache
	poll   time.Duration
	mu     sync.Mutex
	active int
}

func newHTTPPool(cache *configCache) *httpPool {
	return &httpPool{cache: cache, poll: defaultPollInterval}
}

func (p *httpPool) limit() int {
	n := p.cache.Number(context.Background(), maxParallelKey, defaultMaxParallel)
	if n < 1 {
		return 1
	}
	return n
}

// acquire blocks until a slot is free or ctx is done, whichever comes first.
// ctx is the orchestrator's base (shutdown) context, not the caller's request
// context on purpose: the request context that triggered the dispatch is
// often already cancelled by the time the spawn goroutine runs, but the base
// context is what must still stop a parked waiter from launching a process
// after shutdown. Returns false when ctx ends the wait first — the caller
// must not treat that as a slot held and must not call spawn.
func (p *httpPool) acquire(ctx context.Context) bool {
	for !p.tryAcquire() {
		timer := time.NewTimer(p.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return true
}

func (p *httpPool) tryAcquire() bool {
	limit := p.limit()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active >= limit {
		return false
	}
	p.active++
	return true
}

func (p *httpPool) release() {
	p.mu.Lock()
	if p.active > 0 {
		p.active--
	}
	p.mu.Unlock()
}

// PipelineOrchestrator drives the task pipeline state machine.
type PipelineOrchestrator struct {
	opts             OrchestratorOptions
	handlers         map[string]StageHandler // per-orchestrator stage registry
	taskLocks        sync.Map                // map[taskID string]*sync.Mutex
	handlerOverrides sync.Map                // map[stage string]StageHandler — test seam
	detectCompletion func(*ent.StageRun, string, CompletionDeps) (CompletionResult, error)
	configCache      *configCache
	modelResolver    *modelResolver
	stageRuns        *stageRunService
	scheduler        *scheduler
	spawnCleanups    *spawnCleanupRegistry

	httpResultCh chan httpSpawnResult // buffered channel for goroutine pool results
	httpPool     *httpPool            // limits concurrent HTTP spawns

	// F-PERF-008: dedupe inflight tryAttachSessionID goroutines.
	// Key: stageRunID string; Value: struct{} (presence = goroutine in flight).
	attachInFlight sync.Map

	// startedAt is when this orchestrator was constructed. Used by sweepOrphanRuns
	// to distinguish a "running" run left behind by a crashed/killed prior process
	// (started before us) from one legitimately in flight in this process.
	// Assumes a single orchestrator per database: with two instances sharing one
	// SQLite file, the newer instance's startedAt is later than every in-flight
	// run of the older one, so it would reap the older instance's live,
	// PID-less HTTP-adapter runs on every tick.
	startedAt time.Time

	// baseCtx is the long-lived context passed to Run, used by the HTTP-spawn
	// dispatch seam so a spawned adapter process is cancelled on orchestrator
	// shutdown rather than when the triggering HTTP request ends.
	baseCtxMu sync.Mutex
	baseCtx   context.Context
}

// ProgressOpts carries optional parameters for ProgressTask.
type ProgressOpts struct {
	ResumeSessionID      string
	UserAdditionalPrompt string
}

// NewOrchestrator constructs a PipelineOrchestrator with the given options.
// All repo fields in opts are required (validated at construction).
func NewOrchestrator(opts OrchestratorOptions) (*PipelineOrchestrator, error) {
	if opts.TaskRepo == nil || opts.StageRunRepo == nil || opts.PermissionRepo == nil || opts.AuditRepo == nil || opts.ConfigRepo == nil {
		return nil, fmt.Errorf("NewOrchestrator: all repo fields are required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = defaultPollInterval
	}
	sf := opts.SpawnFn
	if sf == nil {
		sf = syntheticSpawn
	}
	if opts.EnsureWorktreeFn == nil {
		opts.EnsureWorktreeFn = ensureTaskWorktree
	}
	if opts.RemoveWorktreeFn == nil {
		taskRepo := opts.TaskRepo
		opts.RemoveWorktreeFn = func(ctx context.Context, task *ent.Task, _ bool) error {
			if task == nil || task.WorktreePath == nil || *task.WorktreePath == "" {
				return nil
			}
			if err := removeTaskWorktree(ctx, task.Cwd, *task.WorktreePath); err != nil {
				return err
			}
			_, err := taskRepo.Update(ctx, task.ID, repo.UpdateTaskInput{ClearWorktreePath: true})
			return err
		}
	}
	cc := newConfigCache(opts.ConfigRepo)
	cleanups := newSpawnCleanupRegistry()
	o := &PipelineOrchestrator{
		opts:             opts,
		handlers:         NewStageHandlers(sf),
		detectCompletion: DetectCompletion,
		configCache:      cc,
		modelResolver:    newModelResolver(cc, opts.ConfigRepo),
		spawnCleanups:    cleanups,
		stageRuns:        newStageRunService(opts.StageRunRepo, opts.RevokeTaskAPIKeys, cleanups.release),
		scheduler:        newScheduler(cc, opts.TaskRepo, opts.StageRunRepo, opts.DepRepo),
		httpPool:         newHTTPPool(cc),
		httpResultCh:     make(chan httpSpawnResult, defaultMaxParallel*2),
		startedAt:        time.Now(),
	}
	return o, nil
}

// SetHandlerOverride replaces a stage handler — test seam only.
func (o *PipelineOrchestrator) SetHandlerOverride(stage string, h StageHandler) {
	o.handlerOverrides.Store(stage, h)
}

// baseContext returns the context passed to Run, or context.Background() when
// Run has never been called (sync tests, DI without a live tick loop).
func (o *PipelineOrchestrator) baseContext() context.Context {
	o.baseCtxMu.Lock()
	defer o.baseCtxMu.Unlock()
	if o.baseCtx == nil {
		return context.Background()
	}
	return o.baseCtx
}

// SetBaseContext overrides the context returned by baseContext without
// starting the tick loop. Callers that start the orchestrator via serverapp
// must call this before any goroutine can reach DispatchHTTPSpawn — otherwise
// baseContext falls back to context.Background(), against which
// context.AfterFunc registers nothing (propagateCancel returns early when
// parent.Done() == nil), so a spawn dispatched in that window is never
// cancelled at shutdown.
func (o *PipelineOrchestrator) SetBaseContext(ctx context.Context) {
	o.baseCtxMu.Lock()
	o.baseCtx = ctx
	o.baseCtxMu.Unlock()
}

// BaseContext returns the context most recently supplied via SetBaseContext
// or Run, or context.Background() if neither has been called yet. Exported
// so a caller (e.g. serverapp's wiring test) can confirm shutdown
// cancellation is attached before the HTTP-spawn seam can be reached.
func (o *PipelineOrchestrator) BaseContext() context.Context {
	return o.baseContext()
}

// Start sets the base context synchronously — before returning, not from
// inside a goroutine — and returns a closure that runs the tick loop via Run.
// A caller that dispatches the returned closure with go/errgroup instead of
// calling Run directly gets a hard guarantee, not a race: by the Go memory
// model, every statement in Start happens-before any goroutine the caller
// starts afterward, so DispatchHTTPSpawn can never observe baseContext()
// fall back to context.Background() no matter how those goroutines are
// scheduled. Run is kept for existing tests and for callers that only run
// the orchestrator on its own.
func (o *PipelineOrchestrator) Start(ctx context.Context) func() error {
	o.SetBaseContext(ctx)
	return func() error {
		return o.run(ctx)
	}
}

// ClearHandlerOverrides removes all test handler overrides.
func (o *PipelineOrchestrator) ClearHandlerOverrides() {
	o.handlerOverrides.Range(func(k, _ any) bool { o.handlerOverrides.Delete(k); return true })
}

// SetCompletionDetector replaces the completion detection function — test seam.
func (o *PipelineOrchestrator) SetCompletionDetector(fn func(*ent.StageRun, string, CompletionDeps) (CompletionResult, error)) {
	o.detectCompletion = fn
}

// InvalidateConfigCache clears the TTL config cache — call after REST writes to pipeline_config.
func (o *PipelineOrchestrator) InvalidateConfigCache() {
	o.configCache.Invalidate()
}

func (o *PipelineOrchestrator) resolveHandler(stage string) StageHandler {
	if h, ok := o.handlerOverrides.Load(stage); ok {
		return h.(StageHandler)
	}
	return o.handlers[stage]
}

// EffectiveStageModel returns the effective global model for the given stage,
// applying coded default → global DB config row precedence.
// Exported for use by api/* handlers (global reads, no project context).
func (o *PipelineOrchestrator) EffectiveStageModel(ctx context.Context, stage string) string {
	return o.modelResolver.StageDefault(ctx, stage, nil)
}

// EffectiveStageModelForProject returns the effective model for the given stage
// and project, applying coded default → global DB row → project DB row.
// Exported for use by api/* handlers that serve per-project config reads.
func (o *PipelineOrchestrator) EffectiveStageModelForProject(ctx context.Context, projectID *string, stage string) string {
	return o.modelResolver.StageDefault(ctx, stage, projectID)
}

// run drives the orchestrator tick loop until ctx is cancelled. Unexported so
// the loop cannot be started without Start having supplied the base context
// first: a caller that reached it directly would reintroduce the window in
// which DispatchHTTPSpawn observes context.Background().
func (o *PipelineOrchestrator) run(ctx context.Context) error {
	o.SetBaseContext(ctx)
	o.recoverRunningStageRuns(ctx)
	ticker := time.NewTicker(o.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := o.tick(ctx); err != nil {
				slog.Error("orchestrator tick error", "err", err)
			}
		}
	}
}

// drainHTTPResults processes all pending goroutine-pool results without blocking.
// Successful results write the synthetic session file path into stage_run.output
// so finalizeCompletedAsyncRuns can pick them up on the next tick.
// Failed results mark the stage_run directly as failed.
func (o *PipelineOrchestrator) drainHTTPResults(ctx context.Context) {
	for {
		select {
		case res := <-o.httpResultCh:
			if res.err != nil {
				slog.Error("orchestrator: HTTP spawn goroutine failed", "stageRunID", res.stageRunID, "err", res.err)
				run, getErr := o.stageRuns.GetByID(ctx, res.stageRunID)
				if getErr != nil || run == nil || run.Status != "running" {
					continue
				}
				task, taskErr := o.opts.TaskRepo.GetByID(ctx, res.taskID)
				if taskErr != nil {
					continue
				}
				if _, err := o.applyTransition(ctx, task, run, FailTransition{
					Reason: fmt.Sprintf("HTTP adapter error: %s", res.err),
				}); err != nil {
					slog.Error("drainHTTPResults.applyFail", "err", err)
				}
			} else {
				// Write the synthetic session file path into stage_run.output so
				// finalizeCompletedAsyncRuns detects it on the next tick.
				run, getErr := o.stageRuns.GetByID(ctx, res.stageRunID)
				if getErr != nil || run == nil {
					continue
				}
				output := map[string]any{"synthetic_session_file": res.sessionFile}
				if run.Output != nil {
					// Merge: preserve any prior output keys (e.g. spawner name logged at spawn time).
					merged := make(map[string]any, len(run.Output)+1)
					for k, v := range run.Output {
						merged[k] = v
					}
					merged["synthetic_session_file"] = res.sessionFile
					output = merged
				}
				if _, err := o.stageRuns.Update(ctx, res.stageRunID, repo.UpdateStageRunInput{
					Output: output,
				}); err != nil {
					slog.Error("drainHTTPResults.writeSessionFile", "stageRunID", res.stageRunID, "err", err)
				} else {
					slog.Info("orchestrator: HTTP spawn completed — session file recorded",
						"stageRunID", res.stageRunID, "sessionFile", res.sessionFile)
					if o.opts.OnTaskChanged != nil {
						o.opts.OnTaskChanged(res.taskID, "async_running", nil)
					}
				}
			}
		default:
			return // channel empty — nothing more to drain
		}
	}
}

func (o *PipelineOrchestrator) tick(ctx context.Context) error {
	o.drainHTTPResults(ctx)
	allRunning, err := o.stageRuns.ListByStatus(ctx, "running", "awaiting_user", "on_hold")
	if err != nil {
		return fmt.Errorf("orchestrator.tick.listRunning: %w", err)
	}
	if err := o.finalizeCompletedAsyncRuns(ctx, allRunning); err != nil {
		slog.Error("finalizeCompletedAsyncRuns error", "err", err)
	}
	if err := o.sweepAwaitingUserRuns(ctx, allRunning); err != nil {
		slog.Error("sweepAwaitingUserRuns error", "err", err)
	}
	if err := o.sweepOrphanRuns(ctx, allRunning); err != nil {
		slog.Error("sweepOrphanRuns error", "err", err)
	}
	if err := o.sweepRequeueableRuns(ctx); err != nil {
		slog.Error("sweepRequeueableRuns error", "err", err)
	}
	o.pickNextTasksForFreeSlots(ctx, allRunning)
	return nil
}

// ProgressTask advances a task from its current stage.
// Calls are serialized per task via a per-task mutex — concurrent callers
// for the same task queue up rather than racing.
func (o *PipelineOrchestrator) ProgressTask(ctx context.Context, taskID string, opts *ProgressOpts) (*ent.StageRun, error) {
	mu := o.getTaskMutex(taskID)
	mu.Lock()
	defer mu.Unlock()
	return o.runProgressTaskLocked(ctx, taskID, opts)
}

// pickNextTasksForFreeSlots selects tasks for available runner slots via the
// scheduler and calls ProgressTask for each.
func (o *PipelineOrchestrator) pickNextTasksForFreeSlots(ctx context.Context, allRunning []*ent.StageRun) {
	for _, task := range o.scheduler.Pick(ctx, allRunning) {
		if _, err := o.ProgressTask(ctx, task.ID, nil); err != nil {
			slog.Error("orchestrator: pickup failed", "taskID", task.ID, "err", err)
		}
	}
}

func (o *PipelineOrchestrator) ensureStageRun(ctx context.Context, task *ent.Task, prefetched *ent.StageRun) (*ent.StageRun, error) {
	existing := prefetched
	if existing == nil {
		existing, _ = o.stageRuns.GetLatestByTaskAndStage(ctx, task.ID, task.CurrentStage)
	}
	if existing != nil {
		if existing.Status == "pending" || existing.Status == "running" {
			return existing, nil
		}
		// awaiting_user with live PID — don't create a new iteration
		if existing.Status == "awaiting_user" && existing.Pid != nil && proc.IsPidAlive(*existing.Pid) {
			return existing, nil
		}
	}
	iteration := 0
	if existing != nil {
		iteration = existing.Iteration + 1
	}
	return o.createNextPendingRun(ctx, task, iteration, "")
}

// createNextPendingRun creates a new pending stage_run at the given iteration.
// userPrompt is persisted as pending_user_prompt when non-empty, so the picker-driven
// spawn can read it without opts being passed explicitly.
func (o *PipelineOrchestrator) createNextPendingRun(ctx context.Context, task *ent.Task, iteration int, userPrompt string) (*ent.StageRun, error) {
	return o.stageRuns.Create(ctx, repo.CreateStageRunInput{
		TaskID:            task.ID,
		Stage:             task.CurrentStage,
		Iteration:         iteration,
		SessionName:       BuildSessionName(task.Slug, task.CurrentStage, iteration),
		PendingUserPrompt: userPrompt,
	})
}

// resolveResumeSessionID returns the newest session JSONL still on disk for the
// task's current stage, walking stage_runs newest-first. Returns "" when none is found.
func (o *PipelineOrchestrator) resolveResumeSessionID(ctx context.Context, task *ent.Task) string {
	cwd := task.Cwd
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		cwd = *task.WorktreePath
	}
	runs, err := o.stageRuns.ListForTask(ctx, task.ID)
	if err != nil {
		return ""
	}
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.Stage != task.CurrentStage || run.SessionID == nil || *run.SessionID == "" {
			continue
		}
		if SessionFileExists(cwd, *run.SessionID) {
			return *run.SessionID
		}
	}
	return ""
}

func (o *PipelineOrchestrator) getPreviousStageOutput(ctx context.Context, task *ent.Task) map[string]any {
	for i := len(StageOrder) - 1; i >= 0; i-- {
		if StageOrder[i] == task.CurrentStage {
			for j := i - 1; j >= 0; j-- {
				prev, _ := o.stageRuns.GetLatestByTaskAndStage(ctx, task.ID, StageOrder[j])
				if prev != nil && prev.Output != nil {
					return prev.Output
				}
			}
			return nil
		}
	}
	return nil
}

func (o *PipelineOrchestrator) getPriorIterationOutput(ctx context.Context, task *ent.Task, sr *ent.StageRun) map[string]any {
	if sr.Iteration == 0 {
		return nil
	}
	prev, _ := o.stageRuns.GetByTaskStageIteration(ctx, task.ID, sr.Stage, sr.Iteration-1)
	if prev != nil {
		return prev.Output
	}
	return nil
}

// hasSyntheticSessionFile returns true when the stage_run's output already
// contains the synthetic_session_file key written by drainHTTPResults.
func hasSyntheticSessionFile(run *ent.StageRun) bool {
	if run.Output == nil {
		return false
	}
	sf, ok := run.Output["synthetic_session_file"].(string)
	return ok && sf != ""
}

// enforceBudget kills run's live agent and fails its stage_run when the
// amount spent (via sumFn) exceeds limit. A sum-fetch error is logged and
// enforcement is skipped for this tick — it does not kill the agent. Returns
// true when it killed the run, so the caller must `continue` its loop.
// CQ-05: collapses the near-identical cost/token budget blocks that used to
// live inline in finalizeCompletedAsyncRuns.
func (o *PipelineOrchestrator) enforceBudget(
	ctx context.Context,
	task *ent.Task,
	run *ent.StageRun,
	limit *int,
	sumFn func(ctx context.Context, taskID string) (int64, error),
	kind string, // "cost" or "token" — used in log lines and the failure reason
	spentPhrase string, // e.g. "cents spent" or "tokens used"
	unit string, // e.g. "cents" or "tokens"
) bool {
	if run.Pid == nil || limit == nil || *limit <= 0 {
		return false
	}
	spent, err := sumFn(ctx, task.ID)
	if err != nil {
		slog.Warn(fmt.Sprintf("orchestrator: %s budget sum failed — skipping enforcement this tick", kind),
			"taskID", task.ID, "err", err)
	}
	if spent <= int64(*limit) {
		return false
	}
	slog.Warn(fmt.Sprintf("orchestrator: task exceeded %s budget — killing agent", kind),
		"taskID", task.ID, "spent", spent, "budget", *limit)
	_ = syscallKill(*run.Pid)
	fresh, _ := o.stageRuns.GetByID(ctx, run.ID)
	if fresh != nil && fresh.Status == "running" {
		if _, err := o.applyTransition(ctx, task, fresh, FailTransition{
			Reason: fmt.Sprintf("%s budget exceeded: %d %s, limit %d %s", kind, spent, spentPhrase, *limit, unit),
		}); err != nil {
			slog.Error("finalizeCompletedAsyncRuns."+kind+"Budget", "err", err)
		}
	}
	return true
}

// enforceBudgetsAndTimeout handles a "still_running" completion result:
// attaching the session ID for the live cross-link banner, enforcing the
// cost/token budgets, and enforcing the stage timeout. Any of these may kill
// the agent and fail the run; the caller (finalizeCompletedAsyncRuns) always
// continues its loop afterward regardless of outcome, matching the original
// inline still_running branch.
// CQ-04: extracted from finalizeCompletedAsyncRuns.
func (o *PipelineOrchestrator) enforceBudgetsAndTimeout(ctx context.Context, task *ent.Task, run *ent.StageRun, cwd string) {
	// Try to attach session_id for live cross-link banner (subprocess runs only).
	// F-PERF-008: dedupe inflight goroutines via attachInFlight sync.Map.
	if run.Pid != nil && run.SessionID == nil && run.StartedAt != nil {
		if _, loaded := o.attachInFlight.LoadOrStore(run.ID, struct{}{}); !loaded {
			go func(runID, taskID, cwd string, startedAt time.Time) {
				defer o.attachInFlight.Delete(runID)
				o.tryAttachSessionID(ctx, runID, taskID, cwd, startedAt)
			}(run.ID, task.ID, cwd, *run.StartedAt)
		}
	}
	// Cost budget enforcement (subprocess runs only — HTTP runs finalize atomically)
	if o.enforceBudget(ctx, task, run, task.CostBudgetCents, o.stageRuns.SumCompletedCostCents, "cost", "cents spent", "cents") {
		return
	}
	// Token budget enforcement (subprocess runs only — HTTP runs finalize atomically)
	if o.enforceBudget(ctx, task, run, task.TokenBudget, o.stageRuns.SumCompletedTokens, "token", "tokens used", "tokens") {
		return
	}
	// Stage timeout enforcement (subprocess runs only).
	// StartedAt is read from the passed-in run, not a re-fetch: it is written once
	// at spawn and never mutated on an existing stage_run row (requeues allocate a
	// new row at iter+1), so the in-memory copy always equals the DB value here.
	if run.Pid != nil {
		timeoutSec := o.configCache.Number(ctx, stageTimeoutKey, defaultStageTimeoutSeconds)
		if timeoutSec > 0 && run.StartedAt != nil && time.Since(*run.StartedAt) > time.Duration(timeoutSec)*time.Second {
			elapsed := time.Since(*run.StartedAt).Seconds()
			slog.Warn("orchestrator: stage timed out — killing agent",
				"runID", run.ID, "stage", run.Stage, "elapsedSec", elapsed)
			_ = syscallKill(*run.Pid)
			fresh, _ := o.stageRuns.GetByID(ctx, run.ID)
			if fresh != nil && fresh.Status == "running" {
				if _, err := o.applyTransition(ctx, task, fresh, FailTransition{
					Reason: fmt.Sprintf("stage timeout: ran %.0fs (limit %ds)", elapsed, timeoutSec),
				}); err != nil {
					slog.Error("finalizeCompletedAsyncRuns.timeout", "err", err)
				}
			}
		}
	}
}

// handleFailedResult classifies a failed completion result into one of four
// outcomes, checked in priority order: rate-limited requeue/exhaust, infra
// requeue/exhaust, schema-validation retry (iterate) or wait-user, or a plain
// hard fail.
// CQ-04: extracted from finalizeCompletedAsyncRuns's failure ladder.
func (o *PipelineOrchestrator) handleFailedResult(ctx context.Context, task *ent.Task, fresh *ent.StageRun, result CompletionResult) {
	// RateLimited implies Infra; check it first so it uses the dedicated budget and fixed backoff.
	// RetryCount is shared with the infra-retry counter.
	if result.RateLimited {
		maxRL := o.configCache.Number(ctx, maxRateLimitRetriesKey, defaultMaxRateLimitRetries)
		if fresh.RetryCount < maxRL {
			attempt := fresh.RetryCount + 1
			backoffSec := o.configCache.Number(ctx, rateLimitBackoffKey, defaultRateLimitBackoff)
			nextRetryAt := time.Now().Add(time.Duration(backoffSec) * time.Second)
			slog.Info("orchestrator: requeuing rate-limited run",
				"runID", fresh.ID, "stage", fresh.Stage, "attempt", attempt,
				"maxRateLimitRetries", maxRL, "backoffSec", backoffSec)
			if _, err := o.applyTransition(ctx, task, fresh, RequeueTransition{
				Reason:      result.Error,
				Output:      result.Output,
				Attempt:     attempt,
				NextRetryAt: nextRetryAt,
			}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.rateLimitRequeue", "err", err)
			}
		} else {
			slog.Warn("orchestrator: rate-limit retry budget exhausted — hard failing",
				"runID", fresh.ID, "stage", fresh.Stage, "maxRateLimitRetries", maxRL)
			output := make(map[string]any, len(result.Output)+1)
			for k, v := range result.Output {
				output[k] = v
			}
			output["rate_limit_retries_exhausted"] = maxRL
			if _, err := o.applyTransition(ctx, task, fresh, FailTransition{Reason: result.Error, Output: output}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.rateLimitHardFail", "err", err)
			}
		}
		return
	}

	if result.Infra {
		maxRetries := o.configCache.Number(ctx, maxAutoRetriesKey, defaultMaxAutoRetries)
		if fresh.RetryCount < maxRetries {
			attempt := fresh.RetryCount + 1
			backoffSec := o.configCache.Number(ctx, retryBackoffKey, defaultRetryBackoff) * attempt // linear backoff
			nextRetryAt := time.Now().Add(time.Duration(backoffSec) * time.Second)
			slog.Info("orchestrator: requeuing infra-failed run",
				"runID", fresh.ID, "stage", fresh.Stage, "attempt", attempt,
				"maxAutoRetries", maxRetries, "backoffSec", backoffSec)
			if _, err := o.applyTransition(ctx, task, fresh, RequeueTransition{
				Reason:      result.Error,
				Output:      result.Output,
				Attempt:     attempt,
				NextRetryAt: nextRetryAt,
			}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.requeue", "err", err)
			}
		} else {
			slog.Warn("orchestrator: auto-retry budget exhausted — hard failing",
				"runID", fresh.ID, "stage", fresh.Stage, "maxAutoRetries", maxRetries)
			output := make(map[string]any, len(result.Output)+1)
			for k, v := range result.Output {
				output[k] = v
			}
			output["auto_retries_exhausted"] = maxRetries
			if _, err := o.applyTransition(ctx, task, fresh, FailTransition{Reason: result.Error, Output: output}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.hardFail", "err", err)
			}
		}
		return
	}

	if result.Retryable {
		// Schema rejection retry logic: iter 0 → iterate; iter >= 1 → wait_user
		if fresh.Iteration == 0 {
			if _, err := o.applyTransition(ctx, task, fresh, IterateTransition{Output: map[string]any{
				"validation_error": result.Error,
				"rejected_output":  result.Output,
			}}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.iterate", "err", err)
			}
		} else {
			if _, err := o.applyTransition(ctx, task, fresh, WaitUserTransition{
				Reason:    fmt.Sprintf("schema validation failed twice at stage %s: %s", fresh.Stage, result.Error),
				Output:    map[string]any{"validation_error": result.Error, "rejected_output": result.Output},
				AgentDone: true,
			}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.waitUser", "err", err)
			}
		}
		return
	}

	if _, err := o.applyTransition(ctx, task, fresh, FailTransition{Reason: result.Error, Output: result.Output}); err != nil {
		slog.Error("finalizeCompletedAsyncRuns.applyTransition.hardFail", "err", err)
	}
}

func (o *PipelineOrchestrator) finalizeCompletedAsyncRuns(ctx context.Context, allRunning []*ent.StageRun) error {
	for _, run := range allRunning {
		if run.Status != "running" {
			continue
		}

		// Determine whether this run belongs to a subprocess-based agent (pid != nil)
		// or an HTTP adapter (pid == nil).
		//
		// HTTP adapter runs (pid IS NULL):
		//   - Still in flight: no synthetic_session_file in output yet → skip.
		//   - Completed:       synthetic_session_file present → fall through to detectCompletion.
		//
		// Subprocess runs (pid != nil):
		//   - Still alive → skip.
		//   - Exited      → fall through to detectCompletion.
		if run.Pid == nil {
			if !hasSyntheticSessionFile(run) {
				// HTTP goroutine has not finished yet — skip until drainHTTPResults writes the file.
				continue
			}
			// Synthetic session file written — run is ready to finalize.
		}
		// Subprocess runs (pid != nil): fall through whether alive or exited.
		// detectCompletion returns "still_running" for live PIDs; cost-budget and
		// stage-timeout enforcement in that branch applies unconditionally.

		task, err := o.opts.TaskRepo.GetByID(ctx, run.TaskID)
		if err != nil {
			continue
		}
		if IsTerminalStage(task.CurrentStage) {
			if run.Pid != nil && proc.IsPidAlive(*run.Pid) {
				_ = syscallKill(*run.Pid)
			}
			if _, err := o.applyTransition(ctx, task, run, FailTransition{Reason: "task cancelled externally"}); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.externalCancel", "err", err)
			}
			continue
		}
		cwd := task.Cwd
		if task.WorktreePath != nil && *task.WorktreePath != "" {
			cwd = *task.WorktreePath
		}

		result, err := o.detectCompletion(run, cwd, CompletionDeps{})
		if err != nil {
			slog.Error("orchestrator: completion detection failed", "runID", run.ID, "err", err)
			continue
		}
		if result.Kind == "still_running" {
			o.enforceBudgetsAndTimeout(ctx, task, run, cwd)
			continue
		}

		// Process exited (or HTTP adapter finished) — persist token usage for subprocess runs.
		if run.Pid != nil && run.SessionID != nil {
			go o.updateTokenUsage(ctx, run.ID, cwd, *run.SessionID)
		}

		fresh, _ := o.stageRuns.GetByID(ctx, run.ID)
		if fresh == nil || fresh.Status != "running" {
			continue
		}

		if result.Kind == "completed" {
			transition := o.decideCompletedTransition(ctx, task, fresh, result.Output)
			if _, err := o.applyTransition(ctx, task, fresh, transition); err != nil {
				slog.Error("finalizeCompletedAsyncRuns.applyTransition.completed", "err", err)
			}
			continue
		}

		o.handleFailedResult(ctx, task, fresh, result)
	}
	return nil
}

func (o *PipelineOrchestrator) recoverRunningStageRuns(ctx context.Context) {
	running, _ := o.stageRuns.ListByStatus(ctx, "running")
	for _, run := range running {
		decision := DecideRecovery(run)
		_ = o.opts.AuditRepo.RecordTaskAudit(ctx, run.TaskID, nil, "recovery_decision", "task:"+run.TaskID,
			map[string]any{"stage": run.Stage, "iteration": run.Iteration, "decision": decision.Kind, "reason": decision.Reason})
		if decision.Kind == "alive" {
			continue
		}
		if decision.Kind == "resume" {
			_, _ = o.stageRuns.MarkPending(ctx, run.ID)
		} else {
			_, _ = o.stageRuns.MarkFailed(ctx, run.ID, map[string]any{"error": "orchestrator crashed before completion; no session to resume"})
		}
	}
}

// cascadeCancelDownstream cancels a downstream task under its per-task mutex so the
// write cannot race a concurrent ProgressTask transition. Re-reads state under the
// lock; returns true only if it actually moved the task to cancelled.
func (o *PipelineOrchestrator) cascadeCancelDownstream(ctx context.Context, downstreamID string) bool {
	mu := o.getTaskMutex(downstreamID)
	mu.Lock()
	defer mu.Unlock()
	fresh, err := o.opts.TaskRepo.GetByID(ctx, downstreamID)
	if err != nil {
		slog.Warn("handleDependentTasks: cascade re-fetch failed", "downstreamID", downstreamID, "err", err)
		return false
	}
	if IsTerminalStage(fresh.CurrentStage) {
		return false
	}
	cancelled := "cancelled"
	if _, err := o.opts.TaskRepo.Update(ctx, downstreamID, repo.UpdateTaskInput{CurrentStage: &cancelled}); err != nil {
		slog.Warn("handleDependentTasks: cascade cancel failed", "downstreamID", downstreamID, "err", err)
		return false
	}
	// A cascade reaches this task through handleDependentTasks, never through
	// NotifyTaskTerminated, so its open stage runs need ending here too — a
	// downstream agent cancelled by its upstream holds the same live credential
	// as one cancelled directly.
	o.cancelOpenStageRuns(ctx, downstreamID)
	return true
}

func (o *PipelineOrchestrator) handleDependentTasks(ctx context.Context, taskID, newStage string) {
	if o.opts.OnTaskChanged != nil {
		o.opts.OnTaskChanged(taskID, "dependent_check", nil)
	}
	if o.opts.DepRepo == nil {
		return
	}
	downstreams, err := o.opts.DepRepo.ListDownstream(ctx, taskID)
	if err != nil {
		slog.Warn("handleDependentTasks: ListDownstream failed", "taskID", taskID, "err", err)
		return
	}
	for _, dep := range downstreams {
		downstreamID := dep.TaskID
		downstream, err := o.opts.TaskRepo.GetByID(ctx, downstreamID)
		if err != nil {
			slog.Warn("handleDependentTasks: GetByID failed", "downstreamID", downstreamID, "err", err)
			continue
		}
		if IsTerminalStage(downstream.CurrentStage) {
			continue
		}
		if newStage == "cancelled" {
			switch dep.OnCancelAction {
			case "cancel":
				if !o.cascadeCancelDownstream(ctx, downstreamID) {
					continue
				}
				if o.opts.OnTaskChanged != nil {
					o.opts.OnTaskChanged(downstreamID, "cancelled", nil)
				}
				// recurse so multi-level chains cascade
				o.handleDependentTasks(ctx, downstreamID, "cancelled")
			case "start":
				// upstream cancelled + start = treated as satisfied by the lazy gate; just refresh UI
				if o.opts.OnTaskChanged != nil {
					o.opts.OnTaskChanged(downstreamID, "dependent_check", nil)
				}
			default: // "on_hold" or anything else — leave state, refresh UI
				if o.opts.OnTaskChanged != nil {
					o.opts.OnTaskChanged(downstreamID, "dependent_check", nil)
				}
			}
		} else {
			// upstream reached its required stage (e.g. "done") — lazy picker will pick
			// up the downstream on the next poll tick
			if o.opts.OnTaskChanged != nil {
				o.opts.OnTaskChanged(downstreamID, "dependent_check", nil)
			}
		}
	}
}

// tryAttachSessionID finds and records the session ID for a live subprocess run.
// Called from a goroutine; deduplication is handled by attachInFlight in the caller.
func (o *PipelineOrchestrator) tryAttachSessionID(ctx context.Context, stageRunID, taskID, cwd string, startedAt time.Time) {
	sid, err := FindNewestSessionID(cwd, startedAt.Format("2006-01-02T15:04:05Z"))
	if err != nil || sid == "" {
		return
	}
	if err := AttachSessionID(ctx, stageRunID, sid, o.opts.StageRunRepo); err != nil {
		slog.Warn("orchestrator.tryAttachSessionID", "err", err)
		return
	}
	if o.opts.OnTaskChanged != nil {
		o.opts.OnTaskChanged(taskID, "async_running", nil)
	}
}

func (o *PipelineOrchestrator) updateTokenUsage(ctx context.Context, stageRunID, cwd, sessionID string) {
	summary, err := ReadSessionTokenSummary(cwd, sessionID)
	if err != nil {
		return
	}
	total := summary.InputTokens + summary.OutputTokens + summary.CacheCreationTokens + summary.CacheReadTokens
	if total == 0 {
		return
	}
	costUsd := pricing.EstimateCost(sdk.TokenUsage{
		InputTokens:         summary.InputTokens,
		OutputTokens:        summary.OutputTokens,
		CacheCreationTokens: summary.CacheCreationTokens,
		CacheReadTokens:     summary.CacheReadTokens,
	}, summary.Model)
	costCents := int(costUsd * 100)
	_, _ = o.stageRuns.Update(ctx, stageRunID, repo.UpdateStageRunInput{
		TokensUsed: &total,
		CostCents:  &costCents,
	})
}

// NotifyTaskTerminated is called by cancel routes to cascade terminal state to dependents.
// It also removes the per-task mutex so taskLocks does not grow unbounded.
func (o *PipelineOrchestrator) NotifyTaskTerminated(ctx context.Context, taskID, stage string) {
	o.taskLocks.Delete(taskID)
	o.cancelOpenStageRuns(ctx, taskID)
	if task, err := o.opts.TaskRepo.GetByID(ctx, taskID); err == nil {
		o.cleanupTerminalWorktree(ctx, task, true)
	}
	o.handleDependentTasks(ctx, taskID, stage)
}

// cancelOpenStageRuns writes the terminal "cancelled" status on every stage run
// of taskID that is not terminal yet. Cancelling a task marks only the task
// itself, so without this the abandoned agent's stage run stays "running" and
// its MCP credentials stay valid until expires_at. Routing the write through
// stageRunService.Update is what releases them.
func (o *PipelineOrchestrator) cancelOpenStageRuns(ctx context.Context, taskID string) {
	runs, err := o.stageRuns.ListForTask(ctx, taskID)
	if err != nil {
		slog.Warn("orchestrator: listing stage runs of a cancelled task failed — credentials stay valid until expiry",
			"taskID", taskID, "err", err)
		return
	}
	now := time.Now()
	for _, run := range runs {
		if terminalStageRunStatuses[run.Status] {
			continue
		}
		if _, err := o.stageRuns.Update(ctx, run.ID, repo.UpdateStageRunInput{
			Status:  strPtr("cancelled"),
			EndedAt: &now,
		}); err != nil {
			slog.Warn("orchestrator: cancelling a terminated task's stage run failed",
				"taskID", taskID, "stageRun", run.ID, "err", err)
		}
	}
}

// afterCommitTerminalCleanup removes the task's worktree once a DoneTransition
// has been committed. Cancellation is handled separately via NotifyTaskTerminated.
func (o *PipelineOrchestrator) afterCommitTerminalCleanup(ctx context.Context, task *ent.Task, t StageTransition) {
	if _, ok := t.(DoneTransition); ok {
		o.cleanupTerminalWorktree(ctx, task, true)
	}
}

// cleanupTerminalWorktree removes a terminal task's git worktree via the
// RemoveWorktreeFn seam, freeing its source branch and reclaiming disk. It is
// best-effort: a missing worktree is a no-op and any removal error is logged and
// swallowed so terminal-state handling never fails on git. On success it records
// a "worktree_removed" audit event (force=true discards uncommitted work).
// When HasUnpushedWorkFn reports unpushed work it instead retains the worktree and
// records a "worktree_retained_unpushed" audit event rather than removing it.
func (o *PipelineOrchestrator) cleanupTerminalWorktree(ctx context.Context, task *ent.Task, force bool) {
	if task == nil || task.WorktreePath == nil || *task.WorktreePath == "" {
		return
	}
	if o.opts.RemoveWorktreeFn == nil {
		return
	}
	path := *task.WorktreePath
	// Retain (do not remove) a terminal task's worktree when it still holds
	// unpushed commits or uncommitted changes, so the work is not orphaned.
	// Keep the checkpoint refs too — they are part of the retained work.
	if o.opts.HasUnpushedWorkFn != nil && o.opts.HasUnpushedWorkFn(ctx, task) {
		slog.Warn("orchestrator: retaining terminal worktree with unpushed work", "taskID", task.ID, "path", path)
		_ = o.opts.AuditRepo.RecordTaskAudit(ctx, task.ID, nil, "worktree_retained_unpushed", "task:"+task.ID,
			map[string]any{"path": path})
		return
	}
	// Stop the checkpoint watcher and prune its refs/rows before the worktree is
	// removed (the refs live in the worktree's git dir).
	if o.opts.CheckpointerStopFn != nil {
		o.opts.CheckpointerStopFn(task.ID)
	}
	if err := o.opts.RemoveWorktreeFn(ctx, task, force); err != nil {
		slog.Warn("orchestrator: terminal worktree cleanup failed", "taskID", task.ID, "path", path, "err", err)
		return
	}
	_ = o.opts.AuditRepo.RecordTaskAudit(ctx, task.ID, nil, "worktree_removed", "task:"+task.ID,
		map[string]any{"path": path, "force": force})
	slog.Info("orchestrator: removed terminal worktree", "taskID", task.ID, "path", path)
}

// ClearStalePendingPermissions expires unresolved permission_requests left on the
// task's current-stage run once that run can no longer act on them (terminal, or
// awaiting_user with a dead PID). Called on user-initiated retry/resume so the
// lingering-pending gate does not block the respawn; the fresh run re-requests if needed.
func (o *PipelineOrchestrator) ClearStalePendingPermissions(ctx context.Context, taskID string) {
	task, err := o.opts.TaskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return
	}
	run, err := o.stageRuns.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
	if err != nil || run == nil {
		return
	}
	pid := 0
	if run.Pid != nil {
		pid = *run.Pid
	}
	terminalOrZombie := run.Status == "failed" || run.Status == "done" ||
		(run.Status == "awaiting_user" && !proc.IsPidAlive(pid))
	if !terminalOrZombie {
		return
	}
	n, err := o.opts.PermissionRepo.ExpirePendingForStageRun(ctx, run.ID)
	if err != nil {
		slog.Error("ClearStalePendingPermissions", "taskID", taskID, "err", err)
		return
	}
	if n > 0 {
		slog.Info("orchestrator: expired stale pending permission_requests on user retry/resume",
			"taskID", taskID, "runID", run.ID, "count", n)
	}
}

// ResumeFromUser re-queues a task after a user action (permission grant) so the
// tick-loop picker picks it up when a slot is free.
func (o *PipelineOrchestrator) ResumeFromUser(ctx context.Context, taskID, userPrompt string) (*ent.StageRun, error) {
	return o.RequeueForUser(ctx, taskID, userPrompt)
}

// RequeueForUser creates a new pending stage_run for taskID so the tick-loop
// picker can spawn it when a slot is free. userPrompt is stored on the run and
// consumed at spawn time. Never spawns immediately — even if a slot is free.
//
// Lock discipline mirrors ResumeFromUser: reapAwaitingUserAgent takes the
// per-task mutex internally, so it must be called BEFORE we acquire it here.
func (o *PipelineOrchestrator) RequeueForUser(ctx context.Context, taskID, userPrompt string) (*ent.StageRun, error) {
	// Kill the awaiting_user agent (if any) and mark its run failed before
	// creating the new pending run. reapAwaitingUserAgent acquires the mutex.
	o.reapAwaitingUserAgent(ctx, taskID)
	// Expire stale pending permission_requests so the lingering-pending gate
	// does not block the new run when the picker processes it.
	o.ClearStalePendingPermissions(ctx, taskID)

	mu := o.getTaskMutex(taskID)
	mu.Lock()
	defer mu.Unlock()

	task, err := o.opts.TaskRepo.GetByID(ctx, taskID)
	if err != nil || IsTerminalStage(task.CurrentStage) {
		return nil, nil
	}

	latest, _ := o.stageRuns.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
	// After reap, a formerly awaiting_user run is now failed. Accept failed or requeued.
	if latest == nil || (latest.Status != "failed" && latest.Status != "requeued") {
		return nil, nil
	}

	iteration := latest.Iteration + 1
	// A requeued latest still has its cooldown promotion pending. Mark it failed
	// before creating the new run, else sweepRequeueableRuns later promotes it
	// in place to pending — leaving two pending runs on the same task+stage, the
	// older of which never spawns and is never reaped (StartedAt stays nil).
	if latest.Status == "requeued" {
		if _, err := o.stageRuns.Update(ctx, latest.ID, repo.UpdateStageRunInput{Status: strPtr("failed")}); err != nil {
			return nil, err
		}
	}
	return o.createNextPendingRun(ctx, task, iteration, userPrompt)
}

// reapAwaitingUserAgent kills the task's current awaiting_user stage-run agent
// and marks the run failed, so a subsequent ProgressTask spawns a fresh
// iteration instead of short-circuiting on the still-alive PID.
func (o *PipelineOrchestrator) reapAwaitingUserAgent(ctx context.Context, taskID string) {
	mu := o.getTaskMutex(taskID)
	mu.Lock()
	defer mu.Unlock()
	task, err := o.opts.TaskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return
	}
	run, err := o.stageRuns.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
	if err != nil || run == nil || run.Status != "awaiting_user" {
		return
	}
	if run.Pid != nil && proc.IsPidAlive(*run.Pid) {
		_ = syscallKill(*run.Pid)
	}
	if _, err := o.applyTransition(ctx, task, run, FailTransition{
		Reason: "user resolved permissions — restarting stage with grants applied",
	}); err != nil {
		slog.Error("reapAwaitingUserAgent.applyTransition", "taskID", taskID, "err", err)
	}
}

// KillRunningStage kills the live agent for taskID (if any) on its current stage
// and marks the run failed, so the checkpoint-revert path never restores the
// worktree under a live writer. A missing/dead run is a no-op. Returns an error
// only when the kill itself fails, so callers can abort the revert.
func (o *PipelineOrchestrator) KillRunningStage(ctx context.Context, taskID string) error {
	mu := o.getTaskMutex(taskID)
	mu.Lock()
	defer mu.Unlock()

	task, err := o.opts.TaskRepo.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("KillRunningStage: task lookup: %w", err)
	}
	run, err := o.stageRuns.GetLatestByTaskAndStage(ctx, taskID, task.CurrentStage)
	if err != nil || run == nil {
		return nil
	}
	if run.Status != "running" && run.Status != "awaiting_user" {
		return nil
	}
	if run.Pid == nil || !proc.IsPidAlive(*run.Pid) {
		return nil
	}
	if err := syscallKill(*run.Pid); err != nil {
		return fmt.Errorf("KillRunningStage: kill pid %d: %w", *run.Pid, err)
	}
	_, err = o.applyTransition(ctx, task, run, FailTransition{Reason: "killed for checkpoint revert"})
	return err
}

func strPtr(s string) *string { return &s }

func syscallKill(pid int) error {
	if pid <= 0 {
		return nil
	}
	// Signal the process group so spawned subprocesses (Setpgid: true) are also terminated.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	// Fall back to signaling the process directly if group kill fails (e.g. process already exited).
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(syscall.SIGTERM)
}
