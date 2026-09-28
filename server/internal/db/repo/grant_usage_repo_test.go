package repo_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newGrantUsageRepo(t *testing.T) (repo.GrantUsageRepo, *db.DBBundle, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient), bundle, context.Background()
}

func TestGrantUsageRecordIfWithinLimitRequiresGrantID(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	if _, err := r.RecordIfWithinLimit(ctx, "", 3, time.Hour); err == nil {
		t.Fatal("RecordIfWithinLimit with an empty grant_id must be refused")
	}
}

func TestGrantUsageRecordUsageRequiresGrantID(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	if err := r.RecordUsage(ctx, ""); err == nil {
		t.Fatal("RecordUsage with an empty grant_id must be refused")
	}
}

// TestGrantUsageRecordUsageInsertsUnconditionally proves RecordUsage writes
// a row even past an already-exhausted limit — it is the human-approved
// override path, not a second limit check.
func TestGrantUsageRecordUsageInsertsUnconditionally(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	grantID := "g1"

	ok, err := r.RecordIfWithinLimit(ctx, grantID, 1, time.Hour)
	if err != nil || !ok {
		t.Fatalf("seed call: want permitted, got ok=%v err=%v", ok, err)
	}
	// The grant is now exhausted for a limit of 1; RecordUsage must still record.
	if err := r.RecordUsage(ctx, grantID); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	count, err := r.CountSince(ctx, grantID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != 2 {
		t.Errorf("CountSince = %d, want 2 (the seeded use plus the unconditional RecordUsage)", count)
	}
}

func TestGrantUsageRecordIfWithinLimitPermitsUnderTheLimitAndRecords(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	grantID := "g1"

	for i := 0; i < 2; i++ {
		ok, err := r.RecordIfWithinLimit(ctx, grantID, 3, time.Hour)
		if err != nil {
			t.Fatalf("RecordIfWithinLimit: %v", err)
		}
		if !ok {
			t.Fatalf("call %d: want permitted (under a limit of 3)", i+1)
		}
	}

	count, err := r.CountSince(ctx, grantID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != 2 {
		t.Errorf("CountSince = %d, want 2 recorded uses", count)
	}
}

// TestGrantUsageRecordIfWithinLimitRefusesAtTheLimitWithoutRecording proves
// the boundary: a limit of 3 permits three calls, not four, and a refused
// call is not itself recorded.
func TestGrantUsageRecordIfWithinLimitRefusesAtTheLimitWithoutRecording(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	grantID := "g1"

	for i := 0; i < 3; i++ {
		ok, err := r.RecordIfWithinLimit(ctx, grantID, 3, time.Hour)
		if err != nil || !ok {
			t.Fatalf("call %d: want permitted, got ok=%v err=%v", i+1, ok, err)
		}
	}

	ok, err := r.RecordIfWithinLimit(ctx, grantID, 3, time.Hour)
	if err != nil {
		t.Fatalf("RecordIfWithinLimit: %v", err)
	}
	if ok {
		t.Fatal("the 4th call must be refused — a limit of 3 permits three calls, not four")
	}

	count, err := r.CountSince(ctx, grantID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != 3 {
		t.Errorf("CountSince = %d, want 3 — the refused 4th call must not be recorded", count)
	}
}

func TestGrantUsageRecordIfWithinLimitZeroIsUnlimited(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	grantID := "g1"

	for i := 0; i < 50; i++ {
		ok, err := r.RecordIfWithinLimit(ctx, grantID, 0, time.Hour)
		if err != nil {
			t.Fatalf("RecordIfWithinLimit: %v", err)
		}
		if !ok {
			t.Fatalf("call %d: an unlimited grant (limit 0) must never be refused", i+1)
		}
	}
}

// TestGrantUsageRecordIfWithinLimitNegativeIsExhausted proves the read-side
// fail-closed guard: a negative limit is invalid and resolves to exhausted,
// never to unlimited, regardless of how little usage exists.
func TestGrantUsageRecordIfWithinLimitNegativeIsExhausted(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	grantID := "g1"

	ok, err := r.RecordIfWithinLimit(ctx, grantID, -1, time.Hour)
	if err != nil {
		t.Fatalf("RecordIfWithinLimit: %v", err)
	}
	if ok {
		t.Fatal("a negative limit must resolve to exhausted, not unlimited")
	}

	count, err := r.CountSince(ctx, grantID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != 0 {
		t.Errorf("CountSince = %d, want 0 — a refused call must not be recorded", count)
	}
}

// TestGrantUsageRecordIfWithinLimitIsAtomicUnderConcurrency is the
// regression for the TOCTOU a separate count-then-insert would leave open:
// N goroutines race RecordIfWithinLimit against the same grant and a limit
// of 3. Two independent, non-transactional calls could both observe "under
// the limit" and both proceed, overrunning the cap; the single write
// transaction in RecordIfWithinLimit must make the check-and-insert pair
// atomic instead, so exactly 3 of the N concurrent attempts succeed.
func TestGrantUsageRecordIfWithinLimitIsAtomicUnderConcurrency(t *testing.T) {
	r, _, ctx := newGrantUsageRepo(t)
	grantID := "g1"
	const limit = 3
	const attempts = 15

	var wg sync.WaitGroup
	var mu sync.Mutex
	permitted := 0
	var errs []error

	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := r.RecordIfWithinLimit(ctx, grantID, limit, time.Hour)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if ok {
				permitted++
			}
		}()
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("got %d unexpected errors from concurrent RecordIfWithinLimit calls: %v", len(errs), errs[0])
	}
	if permitted != limit {
		t.Fatalf("permitted = %d, want exactly %d — concurrent calls must not overrun the limit", permitted, limit)
	}

	count, err := r.CountSince(ctx, grantID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != limit {
		t.Errorf("CountSince = %d, want %d rows actually recorded", count, limit)
	}
}

// TestGrantUsageCountSinceExcludesRowsOlderThanTheWindow pins the sliding
// window's boundary using rows seeded at explicit, known times rather than
// sleeping — a test that sleeps to prove a window is slow and still proves
// nothing about the boundary.
func TestGrantUsageCountSinceExcludesRowsOlderThanTheWindow(t *testing.T) {
	_, bundle, ctx := newGrantUsageRepo(t)
	grantID := "g1"

	now := time.Now()
	seed := func(usedAt time.Time) {
		t.Helper()
		if err := bundle.Client.GrantUsage.Create().
			SetID(uuid.New().String()).
			SetGrantID(grantID).
			SetUsedAt(usedAt).
			Exec(ctx); err != nil {
			t.Fatalf("seed grant usage: %v", err)
		}
	}

	seed(now.Add(-2 * time.Hour))    // outside a 1h window
	seed(now.Add(-90 * time.Minute)) // outside a 1h window
	seed(now.Add(-30 * time.Minute)) // inside a 1h window
	seed(now.Add(-1 * time.Minute))  // inside a 1h window
	// A different grant's usage must never be counted for this one.
	if err := bundle.Client.GrantUsage.Create().
		SetID(uuid.New().String()).
		SetGrantID("g2").
		SetUsedAt(now).
		Exec(ctx); err != nil {
		t.Fatalf("seed other grant usage: %v", err)
	}

	r := repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)
	count, err := r.CountSince(ctx, grantID, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != 2 {
		t.Errorf("CountSince = %d, want 2 (rows older than the window and rows for another grant must be excluded)", count)
	}
}

// TestGrantUsageCountSinceBoundaryIsInclusive pins the exact-boundary case:
// a row used exactly at `since` counts as inside the window.
func TestGrantUsageCountSinceBoundaryIsInclusive(t *testing.T) {
	_, bundle, ctx := newGrantUsageRepo(t)
	grantID := "g1"
	since := time.Now().Add(-time.Hour)

	if err := bundle.Client.GrantUsage.Create().
		SetID(uuid.New().String()).
		SetGrantID(grantID).
		SetUsedAt(since).
		Exec(ctx); err != nil {
		t.Fatalf("seed grant usage: %v", err)
	}

	r := repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient)
	count, err := r.CountSince(ctx, grantID, since)
	if err != nil {
		t.Fatalf("CountSince: %v", err)
	}
	if count != 1 {
		t.Errorf("CountSince = %d, want 1 (a row exactly at the boundary is inside the window)", count)
	}
}
