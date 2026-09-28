package refine_test

import (
	"context"
	"testing"

	apirefine "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

// TestConfirm_RevokesTheStageRunCredential mirrors
// plan.TestApprovePlan_RevokesTheStageRunCredential: Confirm marks the
// concept stage run done, so its MCP credential must be revoked the same way.
func TestConfirm_RevokesTheStageRunCredential(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)

	task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
		Slug:          "refine-confirm-revoke-test",
		Title:         "Refine Confirm Revoke Test",
		Cwd:           t.TempDir(),
		MaxIterations: 3,
		Priority:      "normal",
		CurrentStage:  "backlog",
	})
	require.NoError(t, err)

	run, err := srRepo.Create(ctx, repo.CreateStageRunInput{
		TaskID:    task.ID,
		Stage:     "backlog",
		Iteration: 0,
	})
	require.NoError(t, err)
	status := "awaiting_user"
	_, err = srRepo.Update(ctx, run.ID, repo.UpdateStageRunInput{Status: &status})
	require.NoError(t, err)

	var revoked []string
	_, err = apirefine.Confirm(ctx, apirefine.ConfirmDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Revoke: func(_ context.Context, stageRunID string) error {
			revoked = append(revoked, stageRunID)
			return nil
		},
	}, task.ID)

	require.NoError(t, err)
	require.Equal(t, []string{run.ID}, revoked,
		"confirming the concept must revoke its stage run's MCP credentials")
}
