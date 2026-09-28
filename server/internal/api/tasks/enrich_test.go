package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enrichForTest is a thin wrapper so tests don't repeat the nil permRepo wiring.
func enrichForTest(ctx context.Context, t *testing.T, task *ent.Task, srRepo repo.StageRunRepo, permRepo repo.PermissionRepo) *tasks.EnrichedTask {
	t.Helper()
	enriched, err := tasks.EnrichTask(ctx, task, srRepo, permRepo, nil)
	if err != nil {
		t.Fatalf("EnrichTask: %v", err)
	}
	return enriched
}

func TestEnrichOne_Requeued_PopulatesRetryFields(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:         "enrich-requeued",
		Title:        "Enrich Requeued",
		Cwd:          "/tmp/enrich-requeued",
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

	retryCount := 2
	nextRetryAt := time.Now().Add(30 * time.Second).UTC().Truncate(time.Second)
	status := "requeued"
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status:      &status,
		RetryCount:  &retryCount,
		NextRetryAt: &nextRetryAt,
	})
	if err != nil {
		t.Fatalf("update stage run: %v", err)
	}

	enriched := enrichForTest(ctx, t, task, srRepo, permRepo)

	if enriched.LatestStageRunStatus == nil || *enriched.LatestStageRunStatus != "requeued" {
		t.Errorf("expected latestStageRunStatus=requeued, got %v", enriched.LatestStageRunStatus)
	}
	if enriched.NeedsUser {
		t.Error("expected needsUser=false for requeued status")
	}
	if enriched.AutoRetryCount == nil || *enriched.AutoRetryCount != 2 {
		t.Errorf("expected autoRetryCount=2, got %v", enriched.AutoRetryCount)
	}
	if enriched.NextRetryAt == nil {
		t.Error("expected nextRetryAt to be set")
	}
}

func TestEnrichOne_RetryCountZero_AutoRetryCountNil(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:         "enrich-zero-retry",
		Title:        "Enrich Zero Retry",
		Cwd:          "/tmp/enrich-zero-retry",
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

	status := "requeued"
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{
		Status: &status,
	})
	if err != nil {
		t.Fatalf("update stage run: %v", err)
	}

	enriched := enrichForTest(ctx, t, task, srRepo, permRepo)

	if enriched.AutoRetryCount != nil {
		t.Errorf("expected autoRetryCount=nil for retry_count=0, got %v", *enriched.AutoRetryCount)
	}
}

// TestEnrichOne_AvailableActions_FailedImplementation verifies that the enriched
// task carries a non-nil AvailableActions slice and that "retry" is the primary
// action for a failed implementation run.
func TestEnrichOne_AvailableActions_FailedImplementation(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:         "enrich-actions-failed",
		Title:        "Actions Failed",
		Cwd:          "/tmp/enrich-actions-failed",
		CurrentStage: "implementation",
		Priority:     "medium",
	})
	require.NoError(t, err)

	sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "implementation",
		Iteration: 1,
	})
	require.NoError(t, err)

	status := "failed"
	_, err = srRepo.Update(ctx, sr.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)

	enriched := enrichForTest(ctx, t, task, srRepo, permRepo)

	require.NotNil(t, enriched.AvailableActions, "AvailableActions must not be nil")
	require.NotEmpty(t, enriched.AvailableActions, "AvailableActions must not be empty")

	var primary string
	for _, a := range enriched.AvailableActions {
		if a.Primary {
			primary = a.Action
		}
	}
	assert.Equal(t, "retry", primary, "primary action for failed implementation must be retry")
}

// TestEnrichOne_AvailableActions_ConceptNoRun verifies that a concept task with
// no stage run returns "approve_spec" as the primary action.
func TestEnrichOne_AvailableActions_ConceptNoRun(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	permRepo := repo.NewPermissionRepo(bundle.Client)
	ctx := context.Background()

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:         "enrich-actions-concept",
		Title:        "Actions Concept",
		Cwd:          "/tmp/enrich-actions-concept",
		CurrentStage: "backlog",
		Priority:     "medium",
	})
	require.NoError(t, err)

	enriched := enrichForTest(ctx, t, task, srRepo, permRepo)

	require.NotNil(t, enriched.AvailableActions, "AvailableActions must not be nil")

	var primary string
	for _, a := range enriched.AvailableActions {
		if a.Primary {
			primary = a.Action
		}
	}
	assert.Equal(t, "approve_spec", primary, "primary action for concept with no run must be approve_spec")
}
