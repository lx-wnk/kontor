package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

// TestRefinementTurnRepo_ListForTaskNewest_Order verifies that ListForTaskNewest
// returns turns oldest-first (ASC) after fetching them newest-first from the DB
// and reversing the slice internally.
func TestRefinementTurnRepo_ListForTaskNewest_Order(t *testing.T) {
	client := openTestDB(t)
	taskRepo := repo.NewTaskRepo(client)
	turnRepo := repo.NewRefinementTurnRepo(client)
	ctx := context.Background()

	taskID := createTask(t, taskRepo, "refine-order-test")

	// Insert turn A first.
	turnA, err := turnRepo.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "user",
		Content: "first message",
	})
	require.NoError(t, err)

	// Insert turn B second.
	turnB, err := turnRepo.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "assistant",
		Content: "second message",
	})
	require.NoError(t, err)

	// ListForTaskNewest with limit 2 should return both, oldest first (A then B).
	turns, err := turnRepo.ListForTaskNewest(ctx, taskID, 2)
	require.NoError(t, err)
	require.Len(t, turns, 2)

	require.Equal(t, turnA.ID, turns[0].ID,
		"first element must be the oldest turn (A)")
	require.Equal(t, turnB.ID, turns[1].ID,
		"second element must be the newest turn (B)")
}

// TestRefinementTurnRepo_ListForTaskNewest_Limit verifies that the limit parameter
// returns only the N most-recent turns (still in ASC order).
func TestRefinementTurnRepo_ListForTaskNewest_Limit(t *testing.T) {
	client := openTestDB(t)
	taskRepo := repo.NewTaskRepo(client)
	turnRepo := repo.NewRefinementTurnRepo(client)
	ctx := context.Background()

	taskID := createTask(t, taskRepo, "refine-limit-test")

	for i := 0; i < 5; i++ {
		_, err := turnRepo.Create(ctx, repo.CreateTurnInput{
			TaskID:  taskID,
			Role:    "user",
			Content: "message",
		})
		require.NoError(t, err)
	}

	// With limit 3 we should only get the 3 most recent turns.
	turns, err := turnRepo.ListForTaskNewest(ctx, taskID, 3)
	require.NoError(t, err)
	require.Len(t, turns, 3)
}

// TestRefinementTurnRepo_OptionsRoundTrip covers the ent write path for prepared
// answers, which nothing else reaches: the runner's own test uses a fake repo, so
// without this the JSON field could fail to persist and every suite stays green.
// A turn created without options must read back empty rather than as a stored
// null the UI would have to special-case.
func TestRefinementTurnRepo_OptionsRoundTrip(t *testing.T) {
	client := openTestDB(t)
	taskRepo := repo.NewTaskRepo(client)
	turnRepo := repo.NewRefinementTurnRepo(client)
	ctx := context.Background()

	taskID := createTask(t, taskRepo, "refine-options-test")

	withOptions, err := turnRepo.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "assistant",
		Content: "Which database?",
		Options: []string{"PostgreSQL", "SQLite"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"PostgreSQL", "SQLite"}, withOptions.Options)

	withoutOptions, err := turnRepo.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "assistant",
		Content: "No question here.",
	})
	require.NoError(t, err)
	require.Empty(t, withoutOptions.Options)

	turns, err := turnRepo.ListForTaskNewest(ctx, taskID, 10)
	require.NoError(t, err)
	require.Len(t, turns, 2)
	require.Equal(t, []string{"PostgreSQL", "SQLite"}, turns[0].Options,
		"options must survive the round trip through the database")
	require.Empty(t, turns[1].Options)
}
