package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

func TestTaskRepo_CreateAndGet(t *testing.T) {
	client := openDB(t)

	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	desc := "fix the login"
	task, err := r.Create(ctx, repo.CreateTaskInput{
		Slug:                "fix-login",
		Title:               "Fix Login",
		Description:         &desc,
		Cwd:                 "/tmp/proj",
		CurrentStage:        "backlog",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)
	require.Equal(t, "fix-login", task.Slug)

	got, err := r.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, task.ID, got.ID)

	_, err = r.GetBySlug(ctx, "fix-login")
	require.NoError(t, err)
}

func TestTaskRepo_Update_CurrentStage(t *testing.T) {
	client := openDB(t)

	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	task, err := r.Create(ctx, repo.CreateTaskInput{
		Slug: "my-task", Title: "My Task", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	stage := "implementation"
	updated, err := r.Update(ctx, task.ID, repo.UpdateTaskInput{CurrentStage: &stage})
	require.NoError(t, err)
	require.Equal(t, "implementation", updated.CurrentStage)
}

func TestTaskRepo_Delete(t *testing.T) {
	client := openDB(t)

	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	task, err := r.Create(ctx, repo.CreateTaskInput{
		Slug: "to-delete", Title: "Delete Me", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
	})
	require.NoError(t, err)

	err = r.Delete(ctx, task.ID)
	require.NoError(t, err)

	_, err = r.GetByID(ctx, task.ID)
	require.Error(t, err)
}

func TestTaskRepo_Update_MetadataClear(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	meta := map[string]any{"key": "value"}
	task, err := r.Create(ctx, repo.CreateTaskInput{
		Slug: "meta-task", Title: "Meta Task", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
		Metadata: meta,
	})
	require.NoError(t, err)
	require.NotNil(t, task.Metadata)

	updated, err := r.Update(ctx, task.ID, repo.UpdateTaskInput{MetadataClear: true})
	require.NoError(t, err)
	require.Nil(t, updated.Metadata)
}

func TestTaskRepo_ListForUser_UnscopedSeesAll(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	uid1 := "user-1"
	uid2 := "user-2"
	_, err := r.Create(ctx, repo.CreateTaskInput{
		Slug: "task-u1", Title: "Task U1", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
		UserID: &uid1,
	})
	require.NoError(t, err)
	_, err = r.Create(ctx, repo.CreateTaskInput{
		Slug: "task-u2", Title: "Task U2", Cwd: "/tmp",
		CurrentStage: "backlog", Priority: "medium",
		MaxIterations: 20, StageTimeoutSeconds: 1800,
		UserID: &uid2,
	})
	require.NoError(t, err)

	// Scoped: only own tasks.
	own, err := r.ListForUser(ctx, uid1, false)
	require.NoError(t, err)
	require.Len(t, own, 1)
	require.Equal(t, "task-u1", own[0].Slug)

	// Unscoped (loopback single-user mode): all tasks.
	all, err := r.ListForUser(ctx, uid1, true)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestTaskRepo_ListPickable(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	stages := []struct {
		slug     string
		stage    string
		pickable bool
	}{
		{"t-implementation", "implementation", true},
		{"t-done", "done", false},
		{"t-cancelled", "cancelled", false},
		{"t-on-hold", "on_hold", false},
		{"t-concept", "backlog", false},
	}

	for _, tc := range stages {
		_, err := r.Create(ctx, repo.CreateTaskInput{
			Slug: tc.slug, Title: tc.slug, Cwd: "/tmp",
			CurrentStage: tc.stage, Priority: "medium",
			MaxIterations: 20, StageTimeoutSeconds: 1800,
		})
		require.NoError(t, err)
	}

	pickable, err := r.ListPickable(ctx)
	require.NoError(t, err)
	require.Len(t, pickable, 1)
	require.Equal(t, "t-implementation", pickable[0].Slug)
}

func TestTaskRepo_GetBySlug_NotFound(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	_, err := r.GetBySlug(ctx, "does-not-exist")
	require.Error(t, err)
}

func ptrFloat(v float64) *float64 { return &v }

func TestTaskRepo_RerankBetween(t *testing.T) {
	const gap = 1 << 20 // rankGap constant from task_repo.go

	tests := []struct {
		name       string
		beforeRank *float64
		afterRank  *float64
		// wantRank is a function so the "empty-empty" case can assert > 0 without a fixed value.
		check func(t *testing.T, got float64)
	}{
		{
			name:       "midpoint between two ranked neighbors",
			beforeRank: ptrFloat(1000.0),
			afterRank:  ptrFloat(3000.0),
			check: func(t *testing.T, got float64) {
				t.Helper()
				require.InDelta(t, 2000.0, got, 0.001, "rank should be midpoint of 1000 and 3000")
			},
		},
		{
			name:       "before-only: drop at bottom adds rankGap",
			beforeRank: ptrFloat(5000.0),
			afterRank:  nil,
			check: func(t *testing.T, got float64) {
				t.Helper()
				require.InDelta(t, 5000.0+gap, got, 0.001, "rank should be before+gap")
			},
		},
		{
			name:       "after-only: drop at top subtracts rankGap",
			beforeRank: nil,
			afterRank:  ptrFloat(8000.0),
			check: func(t *testing.T, got float64) {
				t.Helper()
				require.InDelta(t, 8000.0-gap, got, 0.001, "rank should be after-gap")
			},
		},
		{
			name:       "empty-empty: fallback returns positive rank",
			beforeRank: nil,
			afterRank:  nil,
			check: func(t *testing.T, got float64) {
				t.Helper()
				require.Greater(t, got, 0.0, "fallback rank must be positive (unix microseconds)")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := openDB(t)
			r := repo.NewTaskRepo(client)
			ctx := context.Background()

			// Seed the task under test with a known initial rank so it doesn't
			// accidentally match any neighbor value.
			target, err := r.Create(ctx, repo.CreateTaskInput{
				Slug:                "target",
				Title:               "Target",
				Cwd:                 "/tmp",
				CurrentStage:        "backlog",
				Priority:            "medium",
				MaxIterations:       20,
				StageTimeoutSeconds: 1800,
				Rank:                ptrFloat(9999.0),
			})
			require.NoError(t, err)

			var beforeID, afterID string

			if tc.beforeRank != nil {
				before, err := r.Create(ctx, repo.CreateTaskInput{
					Slug:                "before",
					Title:               "Before",
					Cwd:                 "/tmp",
					CurrentStage:        "backlog",
					Priority:            "medium",
					MaxIterations:       20,
					StageTimeoutSeconds: 1800,
					Rank:                tc.beforeRank,
				})
				require.NoError(t, err)
				beforeID = before.ID
			}

			if tc.afterRank != nil {
				after, err := r.Create(ctx, repo.CreateTaskInput{
					Slug:                "after",
					Title:               "After",
					Cwd:                 "/tmp",
					CurrentStage:        "backlog",
					Priority:            "medium",
					MaxIterations:       20,
					StageTimeoutSeconds: 1800,
					Rank:                tc.afterRank,
				})
				require.NoError(t, err)
				afterID = after.ID
			}

			updated, err := r.RerankBetween(ctx, target.ID, beforeID, afterID)
			require.NoError(t, err)
			require.NotNil(t, updated.Rank, "Rank pointer must be set after RerankBetween")
			tc.check(t, *updated.Rank)

			// Verify the rank is persisted by re-fetching.
			fetched, err := r.GetByID(ctx, target.ID)
			require.NoError(t, err)
			require.NotNil(t, fetched.Rank)
			require.InDelta(t, *updated.Rank, *fetched.Rank, 0.001, "persisted rank must match returned rank")
		})
	}
}

func TestTaskRepo_CountActiveBySourceBranch(t *testing.T) {
	client := openDB(t)
	r := repo.NewTaskRepo(client)
	ctx := context.Background()

	mk := func(slug, stage, branch string) string {
		b := branch
		task, err := r.Create(ctx, repo.CreateTaskInput{
			Slug: slug, Title: slug, Cwd: "/tmp",
			CurrentStage: stage, Priority: "medium",
			MaxIterations: 20, StageTimeoutSeconds: 1800,
			SourceBranch: &b,
		})
		require.NoError(t, err)
		return task.ID
	}

	mk("a-impl", "implementation", "feat/shared")
	mk("b-concept", "backlog", "feat/shared")
	mk("c-done", "done", "feat/shared")           // terminal — must be ignored
	mk("d-cancelled", "cancelled", "feat/shared") // terminal — must be ignored
	mk("e-other", "implementation", "feat/other")

	n, err := r.CountActiveBySourceBranch(ctx, "feat/shared", "")
	require.NoError(t, err)
	require.Equal(t, 2, n, "only non-terminal tasks on the branch count")

	excludeID := mk("f-self", "implementation", "feat/self")
	n, err = r.CountActiveBySourceBranch(ctx, "feat/self", excludeID)
	require.NoError(t, err)
	require.Equal(t, 0, n, "excluded task must not count itself")

	n, err = r.CountActiveBySourceBranch(ctx, "feat/none", "")
	require.NoError(t, err)
	require.Equal(t, 0, n, "unused branch counts zero")
}
