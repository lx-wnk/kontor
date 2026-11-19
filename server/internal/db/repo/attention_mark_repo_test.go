package repo_test

import (
	"context"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func openBundle(t *testing.T) *db.DBBundle {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	return bundle
}

func newAttentionRepo(t *testing.T) repo.AttentionMarkRepo {
	t.Helper()
	bundle := openBundle(t)
	return repo.NewAttentionMarkRepo(bundle.Client, bundle.WriteClient)
}

func TestAttentionMarkRepo_MarkCreates(t *testing.T) {
	r := newAttentionRepo(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	_, changed, err := r.Mark(ctx, "finished:s1", t0)
	if err != nil {
		t.Fatalf("Mark: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	rows, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ItemKey != "finished:s1" || !rows[0].SeenThrough.Equal(t0) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestAttentionMarkRepo_MarkKeepsLater(t *testing.T) {
	r := newAttentionRepo(t)
	ctx := context.Background()
	later := time.Date(2026, 11, 19, 10, 0, 5, 0, time.UTC)
	earlier := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	if _, _, err := r.Mark(ctx, "k", later); err != nil {
		t.Fatalf("Mark later: %v", err)
	}
	row, changed, err := r.Mark(ctx, "k", earlier)
	if err != nil {
		t.Fatalf("Mark earlier: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if !row.SeenThrough.Equal(later) {
		t.Fatalf("SeenThrough = %v, want %v", row.SeenThrough, later)
	}
	rows, _ := r.List(ctx)
	if len(rows) != 1 || !rows[0].SeenThrough.Equal(later) {
		t.Fatalf("stored = %+v", rows)
	}
}

func TestAttentionMarkRepo_MarkSameIsNoChange(t *testing.T) {
	r := newAttentionRepo(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	if _, _, err := r.Mark(ctx, "k", t0); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	_, changed, err := r.Mark(ctx, "k", t0)
	if err != nil {
		t.Fatalf("Mark again: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	rows, _ := r.List(ctx)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
}

func TestAttentionMarkRepo_MarkAdvances(t *testing.T) {
	r := newAttentionRepo(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	t1 := time.Date(2026, 11, 19, 10, 0, 7, 0, time.UTC)

	if _, _, err := r.Mark(ctx, "k", t0); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	row, changed, err := r.Mark(ctx, "k", t1)
	if err != nil {
		t.Fatalf("Mark advance: %v", err)
	}
	if !changed || !row.SeenThrough.Equal(t1) {
		t.Fatalf("changed=%v SeenThrough=%v, want true %v", changed, row.SeenThrough, t1)
	}
	rows, _ := r.List(ctx)
	if len(rows) != 1 || !rows[0].SeenThrough.Equal(t1) {
		t.Fatalf("stored = %+v", rows)
	}
}

func TestAttentionMarkRepo_NanosRoundTrip(t *testing.T) {
	r := newAttentionRepo(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 19, 10, 0, 0, 123456789, time.UTC)

	if _, _, err := r.Mark(ctx, "k", t0); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	rows, err := r.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || !rows[0].SeenThrough.Equal(t0) {
		t.Fatalf("SeenThrough = %v, want %v", rows[0].SeenThrough, t0)
	}
}

func TestAttentionMarkRepo_BaselineIsStable(t *testing.T) {
	bundle := openBundle(t)
	client := bundle.Client
	r := repo.NewAttentionMarkRepo(bundle.Client, bundle.WriteClient)
	ctx := context.Background()

	first, err := r.Baseline(ctx)
	if err != nil {
		t.Fatalf("Baseline: %v", err)
	}
	if first.IsZero() || time.Since(first) > 5*time.Second || time.Until(first) > 5*time.Second {
		t.Fatalf("first = %v, want within 5s of now", first)
	}
	second, err := r.Baseline(ctx)
	if err != nil {
		t.Fatalf("Baseline again: %v", err)
	}
	if !second.Equal(first) {
		t.Fatalf("second = %v, want %v", second, first)
	}
	n, err := client.AttentionBaseline.Query().Count(ctx)
	if err != nil || n != 1 {
		t.Fatalf("baseline rows = %d, err = %v, want 1", n, err)
	}
}
