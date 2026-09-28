package tasks_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	rawrepo "github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// countingBulk wraps a real StageRunBulkRepo and counts LatestPerTask calls.
type countingBulk struct {
	rawrepo.StageRunBulkRepo
	latest int
}

func (c *countingBulk) LatestPerTask(ctx context.Context, taskIDs []string) (map[string]*ent.StageRun, error) {
	c.latest++
	return c.StageRunBulkRepo.LatestPerTask(ctx, taskIDs)
}

// countingPerm wraps a real PermissionRepo and counts CountForStageRunsBulk calls.
type countingPerm struct {
	repo.PermissionRepo
	bulk int
}

func (c *countingPerm) CountForStageRunsBulk(ctx context.Context, stageRunIDs []string) (map[string]int, error) {
	c.bulk++
	return c.PermissionRepo.CountForStageRunsBulk(ctx, stageRunIDs)
}

func TestEnrichTasksBulk_BoundedQueries(t *testing.T) {
	cases := []struct{ n int }{{1}, {500}}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("n=%d", tc.n), func(t *testing.T) {
			bundle, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("db.Open: %v", err)
			}
			t.Cleanup(func() { _ = bundle.Client.Close() })

			ctx := context.Background()
			taskRepo := repo.NewTaskRepo(bundle.Client)
			srRepo := repo.NewStageRunRepo(bundle.Client)

			// Seed tc.n tasks, each with one stage run on its current stage.
			for i := range tc.n {
				tsk, err := taskRepo.Create(ctx, repo.CreateTaskInput{
					Slug:         fmt.Sprintf("bulk-q-task-%d-%d", tc.n, i),
					Title:        fmt.Sprintf("Task %d", i),
					Cwd:          "/tmp/bulk",
					CurrentStage: "implementation",
					Priority:     "medium",
				})
				if err != nil {
					t.Fatalf("create task %d: %v", i, err)
				}
				_, err = srRepo.Create(ctx, repo.CreateStageRunInput{
					TaskID:    tsk.ID,
					Stage:     "implementation",
					Iteration: 1,
				})
				if err != nil {
					t.Fatalf("create stage run %d: %v", i, err)
				}
			}

			allTasks, err := bundle.Client.Task.Query().All(ctx)
			if err != nil {
				t.Fatalf("list tasks: %v", err)
			}

			cBulk := &countingBulk{StageRunBulkRepo: rawrepo.NewStageRunBulkRepo(bundle.DB)}
			cPerm := &countingPerm{PermissionRepo: repo.NewPermissionRepo(bundle.Client)}

			_, err = tasks.EnrichTasksBulk(ctx, allTasks, nil, cPerm, cBulk)
			if err != nil {
				t.Fatalf("EnrichTasksBulk: %v", err)
			}

			if cBulk.latest != 1 {
				t.Errorf("n=%d: expected 1 LatestPerTask call, got %d", tc.n, cBulk.latest)
			}
			if cPerm.bulk != 1 {
				t.Errorf("n=%d: expected 1 CountForStageRunsBulk call, got %d", tc.n, cPerm.bulk)
			}
		})
	}
}

func TestEnrichTasksBulk_EmptySlice_ZeroQueries(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	cBulk := &countingBulk{StageRunBulkRepo: rawrepo.NewStageRunBulkRepo(bundle.DB)}
	cPerm := &countingPerm{PermissionRepo: repo.NewPermissionRepo(bundle.Client)}

	result, err := tasks.EnrichTasksBulk(ctx, []*ent.Task{}, nil, cPerm, cBulk)
	if err != nil {
		t.Fatalf("EnrichTasksBulk: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
	if cBulk.latest != 0 {
		t.Errorf("expected 0 LatestPerTask calls for empty input, got %d", cBulk.latest)
	}
	if cPerm.bulk != 0 {
		t.Errorf("expected 0 CountForStageRunsBulk calls for empty input, got %d", cPerm.bulk)
	}
}
