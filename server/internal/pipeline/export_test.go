package pipeline

import (
	"context"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// ExportedWriteSettingsFile exposes writeSettingsFile for spawner tests.
func ExportedWriteSettingsFile(autonomy, cwd string, perms []*ent.TaskPermission, enableChannel, allowGitPush bool) (string, bool, bool, error) {
	allow := BuildAllowList(autonomy, perms, enableChannel, allowGitPush, nil)
	deny := BuildDenyList(autonomy, allowGitPush)
	return writeSettingsFile(cwd, allow, deny)
}

// CapabilityViewForTest exposes capabilityViewFor so the allow-list parity
// test resolves capability views the same way production does, instead of
// hardcoding a CapabilityView that can drift from the real catalogue.
var CapabilityViewForTest = capabilityViewFor

// SortPickCandidatesForTest exposes sortPickCandidates for testing.
var SortPickCandidatesForTest = sortPickCandidates

// DecideCompletedTransitionForTest exposes decideCompletedTransition for testing.
func (o *PipelineOrchestrator) DecideCompletedTransitionForTest(
	ctx context.Context, task *ent.Task, run *ent.StageRun, output map[string]any,
) StageTransition {
	return o.decideCompletedTransition(ctx, task, run, output)
}

// IsRateLimitErrorForTest exposes isRateLimitError for testing.
var IsRateLimitErrorForTest = isRateLimitError

// FinalizeCompletedAsyncRunsForTest exposes finalizeCompletedAsyncRuns for testing.
func (o *PipelineOrchestrator) FinalizeCompletedAsyncRunsForTest(ctx context.Context, runs []*ent.StageRun) error {
	return o.finalizeCompletedAsyncRuns(ctx, runs)
}

// SweepRequeueableRunsForTest exposes sweepRequeueableRuns for testing.
func (o *PipelineOrchestrator) SweepRequeueableRunsForTest(ctx context.Context) error {
	return o.sweepRequeueableRuns(ctx)
}

// SweepOrphanRunsForTest exposes sweepOrphanRuns for testing.
func (o *PipelineOrchestrator) SweepOrphanRunsForTest(ctx context.Context, allRunning []*ent.StageRun) error {
	return o.sweepOrphanRuns(ctx, allRunning)
}

// PickNextTasksForFreeSlots exposes pickNextTasksForFreeSlots for testing.
func (o *PipelineOrchestrator) PickNextTasksForFreeSlots(ctx context.Context, allRunning []*ent.StageRun) {
	o.pickNextTasksForFreeSlots(ctx, allRunning)
}

// BuildStageUserPromptForTest exposes buildStageUserPrompt for testing.
var BuildStageUserPromptForTest = buildStageUserPrompt

// ResumeContinueInstructionForTest exposes resumeContinueInstruction for testing.
const ResumeContinueInstructionForTest = resumeContinueInstruction

// SyntheticSpawnPIDForTest exposes syntheticSpawnPID for use in test capture
// spawn functions that want to return the canonical no-op PID.
const SyntheticSpawnPIDForTest = syntheticSpawnPID

// NewAgentStageHandlerForTest creates an agentStageHandler with the given spawn
// function injected, so unit tests can capture SpawnAgentOptions without running
// a real claude subprocess.
func NewAgentStageHandlerForTest(stage string, spawnFn func(SpawnAgentOptions) (SpawnResult, error)) StageHandler {
	return &agentStageHandler{
		stage:       stage,
		buildPrompt: func(_ *StageContext) PromptBundle { return PromptBundle{UserPrompt: "test"} },
		spawnFn:     spawnFn,
	}
}

// SetResolveSpawner injects a SpawnerResolverFunc for testing spawner-override
// precedence without a full DB-backed spawner repo.
func (o *PipelineOrchestrator) SetResolveSpawner(fn SpawnerResolverFunc) {
	o.opts.ResolveSpawner = fn
}

// PlanReviewBuilderForTest exposes planReviewBuilder for unit tests.
var PlanReviewBuilderForTest = planReviewBuilder

// HandleDependentTasksForTest exposes handleDependentTasks for cascade integration tests.
func (o *PipelineOrchestrator) HandleDependentTasksForTest(ctx context.Context, taskID, newStage string) {
	o.handleDependentTasks(ctx, taskID, newStage)
}

// ApplyTransitionForTest exposes applyTransition for tx-correctness tests.
func (o *PipelineOrchestrator) ApplyTransitionForTest(ctx context.Context, task *ent.Task, sr *ent.StageRun, t StageTransition) (*ent.StageRun, error) {
	return o.applyTransition(ctx, task, sr, t)
}

// SeedTaskLockForTest pre-populates the per-task mutex, mirroring what
// runProgressTaskLocked does before a real transition. Used to assert the
// lock is (or is not) released by a transition's postCommit closures.
func (o *PipelineOrchestrator) SeedTaskLockForTest(taskID string) {
	o.getTaskMutex(taskID)
}

// TaskLockHeldForTest reports whether a per-task mutex is still registered for taskID.
func (o *PipelineOrchestrator) TaskLockHeldForTest(taskID string) bool {
	_, ok := o.taskLocks.Load(taskID)
	return ok
}

// FillHTTPResultChForTest saturates httpResultCh to its fixed construction
// capacity without draining it, so a test can force a subsequent dispatch's
// result send to contend with a full channel.
func (o *PipelineOrchestrator) FillHTTPResultChForTest() {
	for {
		select {
		case o.httpResultCh <- httpSpawnResult{}:
		default:
			return
		}
	}
}

// HTTPPoolInFlightForTest reports the current number of active httpPool slots.
func (o *PipelineOrchestrator) HTTPPoolInFlightForTest() int {
	return o.httpPool.inFlightForTest()
}

// SaturateHTTPPoolForTest fills the httpPool to its current live limit by
// directly setting its slot counter, bypassing acquire/tryAcquire, so a test
// can force a subsequent dispatch to park without spawning real work in
// every slot.
func (o *PipelineOrchestrator) SaturateHTTPPoolForTest() {
	limit := o.httpPool.limit()
	o.httpPool.mu.Lock()
	o.httpPool.active = limit
	o.httpPool.mu.Unlock()
}

// ReleaseHTTPPoolForTest frees every slot filled by SaturateHTTPPoolForTest.
func (o *PipelineOrchestrator) ReleaseHTTPPoolForTest() {
	o.httpPool.mu.Lock()
	o.httpPool.active = 0
	o.httpPool.mu.Unlock()
}

// SetHTTPPoolPollForTest overrides the httpPool's retry interval so a test
// can bound how long a parked acquire waits between attempts.
func (o *PipelineOrchestrator) SetHTTPPoolPollForTest(d time.Duration) {
	o.httpPool.poll = d
}

// ChannelAllowForTest exposes the channel tool allow-list for the parity test
// in channel_allowlist_test.go.
func ChannelAllowForTest() []string { return channelAllow }

// SharedContextForTest exposes sharedContext, the system prompt every stage
// spawn carries, for the output-channel test in stage_output_channel_test.go.
const SharedContextForTest = sharedContext

// SweepAwaitingUserRunsForTest exposes sweepAwaitingUserRuns for testing.
func (o *PipelineOrchestrator) SweepAwaitingUserRunsForTest(ctx context.Context, runs []*ent.StageRun) error {
	return o.sweepAwaitingUserRuns(ctx, runs)
}

// FilePermissionRequestForTest exposes filePermissionRequest so the auto-grant
// rule is asserted against the production path, not a copy of its condition.
func (o *PipelineOrchestrator) FilePermissionRequestForTest(
	ctx context.Context, task *ent.Task, stageRunID, tool, pattern, reason string,
) *ent.PermissionRequest {
	return o.filePermissionRequest(ctx, task, stageRunID, tool, pattern, reason)
}
