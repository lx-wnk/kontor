package eval

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// TestService_Scan_DetectsDrift seeds baseline snapshots and recent stage_runs that
// produce a clear success_rate drop, then asserts that Scan inserts a snapshot,
// opens a drift alert, and fires the onDrift callback.
func TestService_Scan_DetectsDrift(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)
	metricRepo := repo.NewEvalMetricRepo(bundle.Client)
	alertRepo := repo.NewDriftAlertRepo(bundle.Client)

	// Clock is set to a short window in the future so that stage_runs inserted
	// right now (real wall time) fall within [now-W, now].
	windowHours := 1
	W := time.Duration(windowHours) * time.Hour
	now := time.Now().Add(W) // recent window = [realNow, realNow+W]
	recentFrom := now.Add(-W)
	baseFrom := now.Add(-2 * W)

	// Seed a task; the stage_runs will have created_at ≈ real wall time, inside [recentFrom, now].
	task, err := tr.Create(ctx, repo.CreateTaskInput{
		Slug:                "svc-test-task",
		Title:               "Service Test Task",
		Cwd:                 "/tmp",
		CurrentStage:        "implement",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           strPtr("sp-test"),
		Metadata:            map[string]any{"model": "claude-sonnet"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Recent window stage_runs: 2 done, 8 failed → success_rate = 0.20.
	for i := 0; i < 2; i++ {
		createRun(t, sr, ctx, task.ID, "implement", "done", 0, 0, 0)
	}
	for i := 0; i < 8; i++ {
		createRun(t, sr, ctx, task.ID, "implement", "failed", 0, 0, 0)
	}

	// Seed historical baseline snapshots in [baseFrom, recentFrom): stable ~0.95.
	baseSnapshots := []repo.EvalMetricSnapshotRow{
		{SpawnerID: "sp-test", Model: "claude-sonnet", Stage: "implement", MetricKey: MetricSuccessRate, Value: 0.95, SampleCount: 40, WindowStart: baseFrom, WindowEnd: baseFrom.Add(W / 3), RecordedAt: baseFrom.Add(W / 4)},
		{SpawnerID: "sp-test", Model: "claude-sonnet", Stage: "implement", MetricKey: MetricSuccessRate, Value: 0.94, SampleCount: 38, WindowStart: baseFrom.Add(W / 3), WindowEnd: baseFrom.Add(2 * W / 3), RecordedAt: baseFrom.Add(5 * W / 12)},
		{SpawnerID: "sp-test", Model: "claude-sonnet", Stage: "implement", MetricKey: MetricSuccessRate, Value: 0.96, SampleCount: 42, WindowStart: baseFrom.Add(2 * W / 3), WindowEnd: recentFrom, RecordedAt: baseFrom.Add(3 * W / 4)},
	}
	if err := metricRepo.Insert(ctx, baseSnapshots); err != nil {
		t.Fatalf("insert baseline snapshots: %v", err)
	}

	// Wire up the service with a low threshold so the drop fires reliably.
	th := Thresholds{
		RateDropPP: 10, // 10pp threshold; drop from 0.95→0.20 is 75pp
		StddevK:    3,
		MinSamples: 5,
	}
	collector := NewCollector(sr, tr)

	var spyFindings []DriftFinding
	svc := NewService(collector, metricRepo, alertRepo, th, windowHours).
		WithClock(func() time.Time { return now }).
		WithOnDrift(func(f []DriftFinding) { spyFindings = append(spyFindings, f...) })

	if err := svc.Scan(ctx); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	// (a) A snapshot was inserted for the recent window (RecordedAt == now from clock).
	// ListByMetric uses a half-open [from, to) window; bound just past now to include it.
	snaps, err := metricRepo.ListByMetric(ctx, MetricSuccessRate, now, now.Add(time.Nanosecond))
	if err != nil {
		t.Fatalf("ListByMetric: %v", err)
	}
	if len(snaps) == 0 {
		t.Error("expected at least one snapshot inserted for the recent window")
	}

	// (b) An open drift alert exists for success_rate on this dimension.
	alerts, err := alertRepo.ListByStatus(ctx, "open")
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	found := false
	for _, a := range alerts {
		if a.SpawnerID == "sp-test" && a.Model == "claude-sonnet" && a.Stage == "implement" && a.MetricKey == MetricSuccessRate {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected open drift alert for success_rate on sp-test/claude-sonnet/implement, got %d alerts", len(alerts))
	}

	// (c) The spy received at least one finding.
	if len(spyFindings) == 0 {
		t.Error("expected onDrift callback to receive findings, got none")
	}
}

// TestService_RunLoop_BootOnly verifies that RunLoop with interval<=0 runs exactly
// one Scan and returns without blocking.
func TestService_RunLoop_BootOnly(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)
	metricRepo := repo.NewEvalMetricRepo(bundle.Client)
	alertRepo := repo.NewDriftAlertRepo(bundle.Client)

	now := time.Now().Add(time.Hour)
	th := Thresholds{RateDropPP: 15, StddevK: 3, MinSamples: 20}
	collector := NewCollector(sr, tr)

	scans := 0
	svc := NewService(collector, metricRepo, alertRepo, th, 168).
		WithClock(func() time.Time { scans++; return now })

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.RunLoop(ctx, 0)
	}()

	select {
	case <-done:
		// RunLoop returned — correct boot-only behaviour.
	case <-time.After(3 * time.Second):
		t.Fatal("RunLoop with interval<=0 did not return within 3 seconds")
	}

	if scans < 1 {
		t.Errorf("expected at least one Scan to run, clock called %d times", scans)
	}
}
