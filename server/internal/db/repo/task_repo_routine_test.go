package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestTaskRepo_ListByRoutine(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	routineID := "routine-1"
	otherRoutineID := "routine-2"

	mk := func(slug string, rid *string) {
		_, err := r.Create(ctx, repo.CreateTaskInput{
			Slug: slug, Title: slug, Cwd: "/tmp", CurrentStage: "job",
			Priority: "medium", MaxIterations: 20, StageTimeoutSeconds: 1800,
			Kind: "job", RoutineID: rid,
		})
		require.NoError(t, err)
	}

	mk("older", &routineID)
	time.Sleep(1100 * time.Millisecond)
	mk("middle", &routineID)
	time.Sleep(1100 * time.Millisecond)
	mk("newest", &routineID)
	mk("elsewhere", &otherRoutineID)

	got, err := r.ListByRoutine(ctx, routineID, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "newest", got[0].Slug)
	require.Equal(t, "middle", got[1].Slug)
}
