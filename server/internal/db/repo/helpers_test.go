package repo_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func openDB(t *testing.T) *ent.Client {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle.Client
}

func createTask(t *testing.T, r repo.TaskRepo, slug string) string {
	t.Helper()
	task, err := r.Create(context.Background(), repo.CreateTaskInput{
		Slug:                slug,
		Title:               "Test Task",
		Cwd:                 "/tmp",
		CurrentStage:        "backlog",
		Priority:            "medium",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	})
	if err != nil {
		t.Fatalf("create task %q: %v", slug, err)
	}
	return task.ID
}

func TestWithTxCommitsOnSuccess(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	err = repo.WithTx(ctx, bundle.Client, func(tx *ent.Tx) error {
		_, err := tx.Resource.Create().
			SetID("res-tx-1").
			SetKind("application").
			SetSlug("committed").
			Save(ctx)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	n, err := bundle.Client.Resource.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("row count after commit = %d, want 1", n)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	sentinel := errors.New("deliberate failure")
	err = repo.WithTx(ctx, bundle.Client, func(tx *ent.Tx) error {
		if _, err := tx.Resource.Create().
			SetID("res-tx-2").
			SetKind("application").
			SetSlug("rolled-back").
			Save(ctx); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx error = %v, want it to wrap the sentinel", err)
	}

	n, err := bundle.Client.Resource.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("row count after rollback = %d, want 0", n)
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	didPanic := false
	defer func() {
		if p := recover(); p != nil {
			didPanic = true
		}
	}()

	_ = repo.WithTx(ctx, bundle.Client, func(tx *ent.Tx) error {
		if _, err := tx.Resource.Create().
			SetID("res-tx-3").
			SetKind("application").
			SetSlug("panicked").
			Save(ctx); err != nil {
			return err
		}
		panic("deliberate panic")
	})

	if !didPanic {
		t.Fatalf("WithTx did not propagate panic")
	}

	n, err := bundle.Client.Resource.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("row count after panic rollback = %d, want 0", n)
	}
}
