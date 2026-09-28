package eval

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func openTestDB(t *testing.T) *db.DBBundle {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle
}

func strPtr(s string) *string { return &s }

func TestCollector_BucketsByDimension(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)

	// Task A: spawner="spawnerA", model="claude-opus-4"
	taskA, err := tr.Create(ctx, repo.CreateTaskInput{
		Slug:                "task-a",
		Title:               "Task A",
		Cwd:                 "/tmp",
		CurrentStage:        "implement",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           strPtr("spawnerA"),
		Metadata:            map[string]any{"model": "claude-opus-4"},
	})
	if err != nil {
		t.Fatalf("create taskA: %v", err)
	}

	// Task B: spawner="spawnerB", no model → "default"
	taskB, err := tr.Create(ctx, repo.CreateTaskInput{
		Slug:                "task-b",
		Title:               "Task B",
		Cwd:                 "/tmp",
		CurrentStage:        "review",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           strPtr("spawnerB"),
	})
	if err != nil {
		t.Fatalf("create taskB: %v", err)
	}

	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	// Stage runs for taskA — stage "implement": 2 done, 1 failed
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 0, 100, 500)
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 1, 200, 1000)
	createRun(t, sr, ctx, taskA.ID, "implement", "failed", 0, 0, 0)

	// Stage runs for taskB — stage "review": 1 done
	createRun(t, sr, ctx, taskB.ID, "review", "done", 0, 50, 200)

	collector := NewCollector(sr, tr)
	result, err := collector.Collect(ctx, from, to)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Expect two distinct dimensions
	if len(result) != 2 {
		t.Errorf("expected 2 dimensions, got %d", len(result))
	}

	dimA := Dimension{SpawnerID: "spawnerA", Model: "claude-opus-4", Stage: "implement"}
	dimB := Dimension{SpawnerID: "spawnerB", Model: "default", Stage: "review"}

	if _, ok := result[dimA]; !ok {
		t.Errorf("missing dimension %+v", dimA)
	}
	if _, ok := result[dimB]; !ok {
		t.Errorf("missing dimension %+v", dimB)
	}
}

func TestCollector_SuccessRate(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)

	taskA, err := tr.Create(ctx, repo.CreateTaskInput{
		Slug:                "task-sr",
		Title:               "SR Task",
		Cwd:                 "/tmp",
		CurrentStage:        "implement",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           strPtr("sp1"),
		Metadata:            map[string]any{"model": "m1"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	// 3 done, 1 failed → success_rate = 0.75
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 0, 100, 500)
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 1, 100, 500)
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 2, 100, 500)
	createRun(t, sr, ctx, taskA.ID, "implement", "failed", 0, 0, 0)

	collector := NewCollector(sr, tr)
	result, err := collector.Collect(ctx, from, to)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	dim := Dimension{SpawnerID: "sp1", Model: "m1", Stage: "implement"}
	metrics := result[dim]

	sr_val := findMetric(metrics, MetricSuccessRate)
	if sr_val == nil {
		t.Fatalf("success_rate metric missing")
	}
	if math.Abs(sr_val.Value-0.75) > 0.001 {
		t.Errorf("success_rate: got %.3f, want 0.750", sr_val.Value)
	}
	if sr_val.SampleCount != 4 {
		t.Errorf("success_rate SampleCount: got %d, want 4", sr_val.SampleCount)
	}
}

func TestCollector_MeanCostCents(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)

	taskA, err := tr.Create(ctx, repo.CreateTaskInput{
		Slug:                "task-cost",
		Title:               "Cost Task",
		Cwd:                 "/tmp",
		CurrentStage:        "implement",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           strPtr("sp1"),
		Metadata:            map[string]any{"model": "m1"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	// 2 terminal runs: costs 100 and 200 → mean = 150
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 0, 100, 500)
	createRun(t, sr, ctx, taskA.ID, "implement", "failed", 0, 200, 1000)

	collector := NewCollector(sr, tr)
	result, err := collector.Collect(ctx, from, to)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	dim := Dimension{SpawnerID: "sp1", Model: "m1", Stage: "implement"}
	metrics := result[dim]

	cost := findMetric(metrics, MetricMeanCostCents)
	if cost == nil {
		t.Fatalf("mean_cost_cents metric missing")
	}
	if math.Abs(cost.Value-150.0) > 0.01 {
		t.Errorf("mean_cost_cents: got %.2f, want 150.00", cost.Value)
	}
}

func TestCollector_AwaitingUserRate(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)

	taskA, err := tr.Create(ctx, repo.CreateTaskInput{
		Slug:                "task-await",
		Title:               "Await Task",
		Cwd:                 "/tmp",
		CurrentStage:        "implement",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		SpawnerID:           strPtr("sp1"),
		Metadata:            map[string]any{"model": "m1"},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	now := time.Now()
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)

	// 1 done, 1 awaiting_user → awaiting_user_rate = 0.5
	createRun(t, sr, ctx, taskA.ID, "implement", "done", 0, 50, 100)
	createRun(t, sr, ctx, taskA.ID, "implement", "awaiting_user", 0, 0, 0)

	collector := NewCollector(sr, tr)
	result, err := collector.Collect(ctx, from, to)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	dim := Dimension{SpawnerID: "sp1", Model: "m1", Stage: "implement"}
	metrics := result[dim]

	await := findMetric(metrics, MetricAwaitingUserRate)
	if await == nil {
		t.Fatalf("awaiting_user_rate metric missing")
	}
	if math.Abs(await.Value-0.5) > 0.001 {
		t.Errorf("awaiting_user_rate: got %.3f, want 0.500", await.Value)
	}
}

func TestCollector_EmptyWindow(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	sr := repo.NewStageRunRepo(bundle.Client)
	tr := repo.NewTaskRepo(bundle.Client)

	now := time.Now()
	collector := NewCollector(sr, tr)
	result, err := collector.Collect(ctx, now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Collect on empty db: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d dimensions", len(result))
	}
}

// createRun inserts a stage_run directly via StageRunRepo then updates its status and cost fields.
func createRun(t *testing.T, sr repo.StageRunRepo, ctx context.Context, taskID, stage, status string, iteration, costCents, tokensUsed int) {
	t.Helper()
	run, err := sr.Create(ctx, repo.CreateStageRunInput{
		TaskID:    taskID,
		Stage:     stage,
		Iteration: iteration,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	sessionID := uuid.New().String()
	if _, err := sr.Update(ctx, run.ID, repo.UpdateStageRunInput{
		Status:     &status,
		SessionID:  &sessionID,
		CostCents:  &costCents,
		TokensUsed: &tokensUsed,
	}); err != nil {
		t.Fatalf("update stage run: %v", err)
	}
}

// findMetric searches a slice of MetricValue for the given key.
func findMetric(metrics []MetricValue, key string) *MetricValue {
	for i := range metrics {
		if metrics[i].Key == key {
			return &metrics[i]
		}
	}
	return nil
}
