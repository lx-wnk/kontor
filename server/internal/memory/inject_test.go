package memory_test

import (
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/memory"
)

func TestBuildBlockNeverExceedsBudget(t *testing.T) {
	entries := make([]memory.Entry, 50)
	for i := range entries {
		entries[i] = memory.Entry{Summary: strings.Repeat("x", 100)}
	}
	block, used, dropped := memory.BuildBlock(entries, 500)
	if len(block) > 500 {
		t.Errorf("block is %d chars, budget was 500", len(block))
	}
	if used > 500 {
		t.Errorf("used = %d, budget was 500", used)
	}
	if dropped == 0 {
		t.Error("50 entries of 100 chars cannot fit in 500 — dropped must be non-zero")
	}
}

func TestBuildBlockEmitsNothingForNoEntries(t *testing.T) {
	block, used, dropped := memory.BuildBlock(nil, 500)
	if block != "" || used != 0 || dropped != 0 {
		t.Errorf("empty input must produce no block at all, got %q", block)
	}
}

func TestBuildBlockDoesNotMarkTruncation(t *testing.T) {
	entries := []memory.Entry{{Summary: strings.Repeat("x", 1000)}}
	block, _, dropped := memory.BuildBlock(entries, 100)
	if strings.Contains(block, "truncated") || strings.Contains(block, "…") {
		t.Error("truncation is counted, not marked — a marker inside the text is one the text can forge")
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

// TestBuildBlockExactFitAndOneCharShort proves the budget is a hard cap, not
// a target, at its two sharpest boundaries: an entry that fits exactly, and
// the same entry one character short of that. The exact-fit budget is
// derived from a real call rather than hardcoded, so the test does not
// silently stop meaning anything if the block's rendering format changes.
func TestBuildBlockExactFitAndOneCharShort(t *testing.T) {
	entries := []memory.Entry{{ID: "a", Summary: "a short memory"}}

	fullBlock, fullUsed, fullDropped := memory.BuildBlock(entries, 10_000)
	if fullDropped != 0 {
		t.Fatalf("sanity check failed: entry unexpectedly dropped at a large budget")
	}
	exactBudget := fullUsed

	block, used, dropped := memory.BuildBlock(entries, exactBudget)
	if dropped != 0 {
		t.Errorf("an entry that fits exactly must not be dropped, got dropped=%d", dropped)
	}
	if used != exactBudget {
		t.Errorf("used = %d, want exactly the budget %d", used, exactBudget)
	}
	if block != fullBlock {
		t.Errorf("block changed at the exact-fit budget: got %q want %q", block, fullBlock)
	}

	shortBlock, shortUsed, shortDropped := memory.BuildBlock(entries, exactBudget-1)
	if shortDropped != 1 {
		t.Errorf("one character short of the exact fit must drop the entry, dropped=%d", shortDropped)
	}
	if shortBlock != "" || shortUsed != 0 {
		t.Errorf("one character short must produce no block at all (not a partial one), got block=%q used=%d", shortBlock, shortUsed)
	}
}

// TestBuildBlockFailsClosedOnNonPositiveBudget is this task's fail-closed
// decision: a budget of zero or less must never be read as "unbounded". It
// disables injection rather than emitting an uncapped block.
func TestBuildBlockFailsClosedOnNonPositiveBudget(t *testing.T) {
	entries := []memory.Entry{{Summary: "one"}, {Summary: "two"}}
	for _, budget := range []int{0, -1, -1000} {
		block, used, dropped := memory.BuildBlock(entries, budget)
		if block != "" || used != 0 {
			t.Errorf("budget %d: want no block, got block=%q used=%d", budget, block, used)
		}
		if dropped != len(entries) {
			t.Errorf("budget %d: dropped = %d, want %d", budget, dropped, len(entries))
		}
	}
}

// TestSelectReportsWhichEntriesMadeTheCut is the interface the push injector
// needs beyond BuildBlock: the memory_injection audit row records which
// entries were actually used, not just how many characters that took.
func TestSelectReportsWhichEntriesMadeTheCut(t *testing.T) {
	entries := []memory.Entry{
		{ID: "a", Summary: "alpha"},
		{ID: "b", Summary: "beta"},
	}
	sel := memory.Select(entries, 10_000)
	if len(sel.Entries) != 2 || sel.Entries[0].ID != "a" || sel.Entries[1].ID != "b" {
		t.Fatalf("want both entries selected in rank order, got %v", sel.Entries)
	}
	if !strings.Contains(sel.Block, "alpha") || !strings.Contains(sel.Block, "beta") {
		t.Errorf("block must contain both selected entries, got %q", sel.Block)
	}
	if sel.Used != len(sel.Block) {
		t.Errorf("Used = %d, want len(Block) = %d", sel.Used, len(sel.Block))
	}
}

// TestSelectDropsOversizedEntryButKeepsSmallerOnesAfterIt proves Select does
// not stop at the first entry that doesn't fit: a lower-ranked but smaller
// entry later in the slice can still use the space the oversized one left
// behind, since the budget bounds total size rather than being a
// first-miss-and-stop cutoff.
func TestSelectDropsOversizedEntryButKeepsSmallerOnesAfterIt(t *testing.T) {
	entries := []memory.Entry{
		{ID: "big", Summary: strings.Repeat("x", 1000)},
		{ID: "small", Summary: "fits"},
	}
	sel := memory.Select(entries, 100)
	if sel.Dropped != 1 {
		t.Fatalf("want exactly the oversized entry dropped, got dropped=%d", sel.Dropped)
	}
	if len(sel.Entries) != 1 || sel.Entries[0].ID != "small" {
		t.Errorf("want the smaller entry selected despite ranking second, got %v", sel.Entries)
	}
}
