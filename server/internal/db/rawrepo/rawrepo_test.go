package rawrepo_test

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func openTestDB(t *testing.T) *db.DBBundle {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	return bundle
}

// TestNotificationConfigRepo_GetSet verifies Set persists a value and Get returns it.
func TestNotificationConfigRepo_GetSet(t *testing.T) {
	bundle := openTestDB(t)
	repo := rawrepo.NewNotificationConfigRepo(bundle.DB)
	ctx := context.Background()

	// Key absent before Set.
	val, found, err := repo.Get(ctx, "test_key")
	if err != nil {
		t.Fatalf("Get before Set: %v", err)
	}
	if found {
		t.Fatalf("expected key absent, got value %q", val)
	}

	// Set a value.
	if err := repo.Set(ctx, "test_key", "hello"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Get returns the set value.
	val, found, err = repo.Get(ctx, "test_key")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if !found {
		t.Fatal("expected key found after Set")
	}
	if val != "hello" {
		t.Fatalf("expected value %q, got %q", "hello", val)
	}

	// Set again (upsert) replaces the value.
	if err := repo.Set(ctx, "test_key", "world"); err != nil {
		t.Fatalf("Set upsert: %v", err)
	}
	val, _, _ = repo.Get(ctx, "test_key")
	if val != "world" {
		t.Fatalf("expected upserted value %q, got %q", "world", val)
	}
}

// TestPushSubscriptionRepo_Register verifies Register inserts and ListAll returns it.
func TestPushSubscriptionRepo_Register(t *testing.T) {
	bundle := openTestDB(t)
	repo := rawrepo.NewPushSubscriptionRepo(bundle.DB)
	ctx := context.Background()

	// Empty list initially.
	subs, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll empty: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected 0 subs, got %d", len(subs))
	}

	// Register a subscription.
	sub := rawrepo.PushSubscription{
		Endpoint: "https://push.example.com/endpoint1",
		P256dh:   "key_p256dh",
		Auth:     "key_auth",
	}
	if err := repo.Register(ctx, sub); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// ListAll returns the subscription.
	subs, err = repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub, got %d", len(subs))
	}
	got := subs[0]
	if got.Endpoint != sub.Endpoint {
		t.Errorf("endpoint: want %q, got %q", sub.Endpoint, got.Endpoint)
	}
	if got.P256dh != sub.P256dh {
		t.Errorf("p256dh: want %q, got %q", sub.P256dh, got.P256dh)
	}
	if got.Auth != sub.Auth {
		t.Errorf("auth: want %q, got %q", sub.Auth, got.Auth)
	}
	if got.ID == "" {
		t.Error("expected non-empty ID")
	}
	if got.CreatedAt == "" {
		t.Error("expected non-empty CreatedAt")
	}

	// Register same endpoint again (INSERT OR IGNORE) — should not duplicate.
	if err := repo.Register(ctx, sub); err != nil {
		t.Fatalf("Register duplicate: %v", err)
	}
	subs, _ = repo.ListAll(ctx)
	if len(subs) != 1 {
		t.Fatalf("expected 1 sub after duplicate register, got %d", len(subs))
	}
}

// TestChildSummariesByParent verifies that ChildSummariesByParent aggregates counts
// and representative active-child fields correctly with a single query.
func TestChildSummariesByParent(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	bulkRepo := rawrepo.NewStageRunBulkRepo(bundle.DB)

	parent, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "parent-task", Title: "Parent", Cwd: "/tmp/parent",
		CurrentStage: "implementation", Priority: "medium",
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parentID := parent.ID

	childRunning, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "child-running", Title: "Child Running", Cwd: "/tmp/child-running",
		CurrentStage: "implementation", Priority: "medium",
		ParentTaskID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child-running: %v", err)
	}
	childDone, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "child-done", Title: "Child Done", Cwd: "/tmp/child-done",
		CurrentStage: "review", Priority: "medium",
		ParentTaskID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child-done: %v", err)
	}

	// Stage run for running child: status=running with output containing summary.
	srRunning, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID: childRunning.ID, Stage: "implementation", Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run running: %v", err)
	}
	runningStatus := "running"
	tokens := 500
	cost := 100
	_, err = srRepo.Update(ctx, srRunning.ID, repo.UpdateStageRunInput{
		Status:     &runningStatus,
		TokensUsed: &tokens,
		CostCents:  &cost,
		Output:     map[string]any{"summary": "doing work"},
	})
	if err != nil {
		t.Fatalf("update stage run running: %v", err)
	}

	// Stage run for done child: status=done — terminal, not active.
	srDone, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID: childDone.ID, Stage: "review", Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run done: %v", err)
	}
	doneStatus := "done"
	_, err = srRepo.Update(ctx, srDone.ID, repo.UpdateStageRunInput{Status: &doneStatus})
	if err != nil {
		t.Fatalf("update stage run done: %v", err)
	}

	summaries, err := bulkRepo.ChildSummariesByParent(ctx, []string{parentID})
	if err != nil {
		t.Fatalf("ChildSummariesByParent: %v", err)
	}

	s, ok := summaries[parentID]
	if !ok {
		t.Fatal("expected summary for parent, got none")
	}
	if s.ChildCount != 2 {
		t.Errorf("ChildCount: want 2, got %d", s.ChildCount)
	}
	if s.ActiveChildCount != 1 {
		t.Errorf("ActiveChildCount: want 1, got %d", s.ActiveChildCount)
	}
	if !s.HasActive {
		t.Error("HasActive: want true")
	}
	if s.TokensUsed != 500 {
		t.Errorf("TokensUsed: want 500, got %d", s.TokensUsed)
	}
	if s.CostCents != 100 {
		t.Errorf("CostCents: want 100, got %d", s.CostCents)
	}
	if s.CurrentStage != "implementation" {
		t.Errorf("CurrentStage: want implementation, got %q", s.CurrentStage)
	}
	if s.LatestOutput != "doing work" {
		t.Errorf("LatestOutput: want %q, got %q", "doing work", s.LatestOutput)
	}

	// Parent absent from query → no entry in map.
	other := "non-existent-parent-id"
	emptySummaries, err := bulkRepo.ChildSummariesByParent(ctx, []string{other})
	if err != nil {
		t.Fatalf("ChildSummariesByParent (no children): %v", err)
	}
	if _, ok := emptySummaries[other]; ok {
		t.Error("expected no entry for parent with no children")
	}
}

// TestChildSummariesByParent_DeadPidNotActive verifies that a child whose DB
// status is non-terminal but whose PID is dead is not counted as active.
func TestChildSummariesByParent_DeadPidNotActive(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)

	// alwaysDead probe — simulates a zombie process.
	alwaysDead := func(int) bool { return false }
	bulkRepo := rawrepo.NewStageRunBulkRepoWithProbe(bundle.DB, alwaysDead)

	parent, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "parent-zombie", Title: "Parent Zombie", Cwd: "/tmp/pz",
		CurrentStage: "implementation", Priority: "medium",
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parentID := parent.ID

	child, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug: "child-zombie", Title: "Child Zombie", Cwd: "/tmp/cz",
		CurrentStage: "implementation", Priority: "medium",
		ParentTaskID: &parentID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID: child.ID, Stage: "implementation", Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	runningStatus := "running"
	pid := 99999
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status: &runningStatus,
		PID:    &pid,
	})
	if err != nil {
		t.Fatalf("update stage run: %v", err)
	}

	summaries, err := bulkRepo.ChildSummariesByParent(ctx, []string{parentID})
	if err != nil {
		t.Fatalf("ChildSummariesByParent: %v", err)
	}
	s, ok := summaries[parentID]
	if !ok {
		t.Fatal("expected summary for parent, got none")
	}
	if s.ChildCount != 1 {
		t.Errorf("ChildCount: want 1, got %d", s.ChildCount)
	}
	if s.ActiveChildCount != 0 {
		t.Errorf("ActiveChildCount: want 0 for dead-pid child, got %d", s.ActiveChildCount)
	}
	if s.HasActive {
		t.Error("HasActive: want false for dead-pid child")
	}
}

// TestStageRunBulkRepo_LatestPerTask_RetryFields verifies that retry_count and
// next_retry_at are correctly scanned by LatestPerTask.
func TestStageRunBulkRepo_LatestPerTask_RetryFields(t *testing.T) {
	bundle := openTestDB(t)
	ctx := context.Background()

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	bulkRepo := rawrepo.NewStageRunBulkRepo(bundle.DB)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:         "bulk-retry-test",
		Title:        "Bulk Retry Test",
		Cwd:          "/tmp/bulk-retry-test",
		CurrentStage: "implementation",
		Priority:     "medium",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}

	retryCount := 3
	nextRetryAt := time.Now().Add(60 * time.Second).UTC().Truncate(time.Second)
	status := "requeued"
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      &status,
		RetryCount:  &retryCount,
		NextRetryAt: &nextRetryAt,
	})
	if err != nil {
		t.Fatalf("update stage run: %v", err)
	}

	latest, err := bulkRepo.LatestPerTask(ctx, []string{task.ID})
	if err != nil {
		t.Fatalf("LatestPerTask: %v", err)
	}

	got, ok := latest[task.ID]
	if !ok || got == nil {
		t.Fatal("expected stage run in LatestPerTask result")
	}
	if got.RetryCount != 3 {
		t.Errorf("expected RetryCount=3, got %d", got.RetryCount)
	}
	if got.NextRetryAt == nil {
		t.Fatal("expected NextRetryAt to be set")
	}
	if !got.NextRetryAt.Equal(nextRetryAt) {
		t.Errorf("expected NextRetryAt=%v, got %v", nextRetryAt, *got.NextRetryAt)
	}
}
