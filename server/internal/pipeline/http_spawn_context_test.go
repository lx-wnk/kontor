package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// dispatchCaptureHandler invokes ctx.DispatchHTTPSpawn with a spawn func that
// reports the context it was handed, so tests can inspect the real seam
// produced by progress_guards.go instead of a hand-built StageContext. spawn
// blocks on release so the seam's own cleanup (which cancels the spawn
// context once spawn returns) cannot race the test's assertions.
type dispatchCaptureHandler struct {
	stage       string
	capturedCtx chan context.Context
	release     chan struct{}
}

func newDispatchCaptureHandler(stage string) *dispatchCaptureHandler {
	return &dispatchCaptureHandler{
		stage:       stage,
		capturedCtx: make(chan context.Context, 1),
		release:     make(chan struct{}),
	}
}

func (h *dispatchCaptureHandler) Stage() string       { return h.stage }
func (h *dispatchCaptureHandler) RequiresAgent() bool { return true }
func (h *dispatchCaptureHandler) Execute(ctx *pipeline.StageContext) (pipeline.StageTransition, error) {
	ctx.DispatchHTTPSpawn(ctx.StageRun.ID, ctx.Task.ID, func(spawnCtx context.Context) (string, error) {
		h.capturedCtx <- spawnCtx
		<-h.release
		return "", nil
	})
	return pipeline.AsyncRunningTransition{PID: 0}, nil
}

// newHTTPSpawnTestTask seeds a task at the "implementation" stage so
// ProgressTask routes through the agent-driven handler override.
func newHTTPSpawnTestTask(t *testing.T, taskRepo repo.TaskRepo, slug string) string {
	t.Helper()
	task, err := taskRepo.Create(context.Background(), repo.CreateTaskInput{
		Slug:          slug,
		Title:         "HTTP Spawn Context Test",
		Cwd:           "/tmp",
		CurrentStage:  "implementation",
		Priority:      "medium",
		MaxIterations: 3,
	})
	require.NoError(t, err)
	return task.ID
}

func awaitSpawnCtx(t *testing.T, ch chan context.Context) context.Context {
	t.Helper()
	select {
	case spawnCtx := <-ch:
		return spawnCtx
	case <-time.After(2 * time.Second):
		t.Fatal("spawn was never dispatched")
		return nil
	}
}

// TestHTTPSpawnSeam_RequestCancellationDoesNotReachSpawn proves that cancelling
// the caller's (HTTP request) context after ProgressTask returns — mirroring the
// handler returning to net/http — does not cancel the context handed to spawn.
func TestHTTPSpawnSeam_RequestCancellationDoesNotReachSpawn(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)

	taskID := newHTTPSpawnTestTask(t, taskRepo, "req-cancel-no-reach-spawn")

	reqCtx, cancel := context.WithCancel(context.Background())
	_, err := orch.ProgressTask(reqCtx, taskID, nil)
	require.NoError(t, err)
	cancel() // simulate the HTTP handler returning and net/http cancelling r.Context()

	spawnCtx := awaitSpawnCtx(t, handler.capturedCtx)
	require.NoError(t, spawnCtx.Err(), "spawn ctx must not be cancelled by the finished HTTP request")
	close(handler.release)
}

// TestHTTPSpawnSeam_ShutdownCancelsSpawn proves that cancelling the
// orchestrator's base (long-lived) context still cancels an in-flight spawn.
func TestHTTPSpawnSeam_ShutdownCancelsSpawn(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)
	orch.SetBaseContext(baseCtx)

	taskID := newHTTPSpawnTestTask(t, taskRepo, "shutdown-cancels-spawn")

	_, err := orch.ProgressTask(context.Background(), taskID, nil)
	require.NoError(t, err)

	spawnCtx := awaitSpawnCtx(t, handler.capturedCtx)
	require.NoError(t, spawnCtx.Err(), "spawn ctx must be alive before shutdown")

	baseCancel()

	select {
	case <-spawnCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("spawn ctx was not cancelled after orchestrator shutdown")
	}
	close(handler.release)
}

// TestHTTPSpawnSeam_ValuesSurvive proves that values attached to the caller's
// context (e.g. a trace ID) remain readable from the spawn context.
func TestHTTPSpawnSeam_ValuesSurvive(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)

	taskID := newHTTPSpawnTestTask(t, taskRepo, "values-survive")

	type ctxKey string
	const traceIDKey ctxKey = "traceID"
	reqCtx := context.WithValue(context.Background(), traceIDKey, "trace-abc-123")

	_, err := orch.ProgressTask(reqCtx, taskID, nil)
	require.NoError(t, err)

	spawnCtx := awaitSpawnCtx(t, handler.capturedCtx)
	require.Equal(t, "trace-abc-123", spawnCtx.Value(traceIDKey))
	close(handler.release)
}

// TestHTTPSpawnSeam_ResultSendAbandonedOnShutdown proves the dispatch
// goroutine does not block forever when httpResultCh is full and the
// orchestrator's base context is cancelled — and that the httpPool slot it
// held is still released.
func TestHTTPSpawnSeam_ResultSendAbandonedOnShutdown(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))
	orch.FillHTTPResultChForTest()

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)
	orch.SetBaseContext(baseCtx)

	taskID := newHTTPSpawnTestTask(t, taskRepo, "result-send-abandoned")

	_, err := orch.ProgressTask(context.Background(), taskID, nil)
	require.NoError(t, err)

	awaitSpawnCtx(t, handler.capturedCtx)
	// spawn returns immediately, so the dispatch goroutine reaches the result
	// send while httpResultCh is still full and blocks there.
	close(handler.release)

	require.Eventually(t, func() bool {
		return orch.HTTPPoolInFlightForTest() == 1
	}, time.Second, 10*time.Millisecond, "dispatch goroutine never acquired its httpPool slot")

	baseCancel()

	require.Eventually(t, func() bool {
		return orch.HTTPPoolInFlightForTest() == 0
	}, 2*time.Second, 10*time.Millisecond,
		"httpPool slot was never released — the goroutine is stuck sending on the full result channel")
}

// TestHTTPSpawnSeam_ParkedAcquireAbandonsOnShutdown proves that a dispatch
// goroutine parked on a saturated httpPool returns as soon as the
// orchestrator's base context is cancelled, without ever acquiring a slot or
// calling spawn — rather than waiting out the pool's poll interval and
// launching a process during shutdown.
func TestHTTPSpawnSeam_ParkedAcquireAbandonsOnShutdown(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))
	orch.SetHTTPPoolPollForTest(20 * time.Millisecond)
	orch.SaturateHTTPPoolForTest()
	limit := orch.HTTPPoolInFlightForTest()

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	orch.SetBaseContext(baseCtx)

	taskID := newHTTPSpawnTestTask(t, taskRepo, "parked-acquire-abandons")

	_, err := orch.ProgressTask(context.Background(), taskID, nil)
	require.NoError(t, err)

	baseCancel()
	time.Sleep(50 * time.Millisecond) // well past one poll tick — the parked acquire must have observed cancellation

	require.Equal(t, limit, orch.HTTPPoolInFlightForTest(),
		"a parked acquire must not increment the pool while abandoning the wait")

	// Free the saturating slots after the abandon window: if the parked
	// acquire is still looping (fix disabled), it will succeed here and call
	// spawn; if it already exited on cancellation, nothing is left to resume.
	orch.ReleaseHTTPPoolForTest()

	select {
	case <-handler.capturedCtx:
		t.Fatal("spawn ran after shutdown — parked acquire did not abandon the wait")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHTTPSpawnSeam_AcquiresAndRunsSpawnWhenBaseContextLive proves the normal
// path still acquires a slot and runs spawn when the base context is live —
// the shutdown exit must not regress the common case.
func TestHTTPSpawnSeam_AcquiresAndRunsSpawnWhenBaseContextLive(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)
	orch.SetBaseContext(baseCtx)

	taskID := newHTTPSpawnTestTask(t, taskRepo, "acquire-runs-spawn-live-ctx")

	_, err := orch.ProgressTask(context.Background(), taskID, nil)
	require.NoError(t, err)

	spawnCtx := awaitSpawnCtx(t, handler.capturedCtx)
	require.NoError(t, spawnCtx.Err(), "spawn must run when a slot is acquired under a live base context")
	require.Equal(t, 1, orch.HTTPPoolInFlightForTest(), "acquire must hold the slot while spawn is in flight")
	close(handler.release)

	require.Eventually(t, func() bool {
		return orch.HTTPPoolInFlightForTest() == 0
	}, time.Second, 10*time.Millisecond, "slot was not released after spawn returned")
}

// TestHTTPSpawnSeam_StartSetsBaseContextBeforeLoopRuns proves
// Orchestrator.Start sets the base context synchronously — before the
// returned loop closure is ever invoked, not merely before it is likely to
// run. serverapp.go passes that closure straight to g.Go, so no sibling
// goroutine started afterward (the API server included) can be scheduled
// ahead of the base context being set. A wall-clock race between sibling
// goroutines cannot be asserted on deterministically, so this checks the
// ordering the language spec actually guarantees: everything in the caller
// before a `go` statement happens-before that goroutine's body.
func TestHTTPSpawnSeam_StartSetsBaseContextBeforeLoopRuns(t *testing.T) {
	orch, taskRepo := makeOrchFromBundle(t, openSharedBundle(t))

	baseCtx, baseCancel := context.WithCancel(context.Background())
	t.Cleanup(baseCancel)

	orch.Start(baseCtx) // return value intentionally unused — Run's tick loop is exercised elsewhere.

	// Asserted before the loop is ever invoked: Start alone must have set it.
	require.NotEqual(t, context.Background(), orch.BaseContext())
	require.NotNil(t, orch.BaseContext().Done())

	handler := newDispatchCaptureHandler("implementation")
	orch.SetHandlerOverride("implementation", handler)
	taskID := newHTTPSpawnTestTask(t, taskRepo, "start-sets-base-ctx")

	_, err := orch.ProgressTask(context.Background(), taskID, nil)
	require.NoError(t, err)

	spawnCtx := awaitSpawnCtx(t, handler.capturedCtx)
	require.NoError(t, spawnCtx.Err(), "spawn ctx must be alive — it must never fall back to context.Background()")

	baseCancel()

	select {
	case <-spawnCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("spawn ctx was not cancelled — Start's base context was not wired")
	}
	close(handler.release)
}
