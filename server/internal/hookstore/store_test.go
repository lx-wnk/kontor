package hookstore

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
)

func ev(tool string) sdk.HookEvent {
	return sdk.HookEvent{Type: "PostToolUse", Tool: tool, At: "2026-06-14T00:00:00Z", Summary: tool}
}

func TestRecord_CapEvictsOldest(t *testing.T) {
	s := New(3, 0)
	for i := range 5 {
		s.Record("sess", ev(strconv.Itoa(i)))
	}
	got := s.Recent("sess")
	if len(got) != 3 {
		t.Fatalf("cap=3 after 5 records: got %d events, want 3", len(got))
	}
	// Newest first: 4, 3, 2 survive; 0 and 1 evicted.
	wantNewestFirst := []string{"4", "3", "2"}
	for i, w := range wantNewestFirst {
		if got[i].Tool != w {
			t.Errorf("event[%d].Tool = %q, want %q (order/eviction wrong)", i, got[i].Tool, w)
		}
	}
}

func TestRecent_NewestFirst(t *testing.T) {
	s := New(10, 0)
	s.Record("sess", ev("a"))
	s.Record("sess", ev("b"))
	got := s.Recent("sess")
	if len(got) != 2 || got[0].Tool != "b" || got[1].Tool != "a" {
		t.Fatalf("Recent order: got %+v, want [b a]", got)
	}
}

func TestRecent_EmptyReturnsNil(t *testing.T) {
	s := New(10, time.Minute)
	if got := s.Recent("never-seen"); got != nil {
		t.Errorf("Recent on unknown session: got %v, want nil (so the field is omitted)", got)
	}
}

func TestRecord_EmptySessionIgnored(t *testing.T) {
	s := New(10, 0)
	s.Record("", ev("a"))
	if got := s.Recent(""); got != nil {
		t.Errorf("Record with empty sessionID should be ignored, got %v", got)
	}
}

func TestTTL_PrunesExpired(t *testing.T) {
	base := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	clock := base
	s := New(10, 30*time.Second)
	s.now = func() time.Time { return clock }

	s.Record("sess", ev("old"))
	clock = base.Add(31 * time.Second) // first event now older than TTL
	s.Record("sess", ev("fresh"))

	got := s.Recent("sess")
	if len(got) != 1 || got[0].Tool != "fresh" {
		t.Fatalf("TTL prune: got %+v, want only [fresh]", got)
	}
}

func TestTTL_AllExpiredReturnsNil(t *testing.T) {
	base := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	clock := base
	s := New(10, 10*time.Second)
	s.now = func() time.Time { return clock }

	s.Record("sess", ev("a"))
	clock = base.Add(time.Minute)
	if got := s.Recent("sess"); got != nil {
		t.Errorf("all events expired: got %v, want nil", got)
	}
}

func TestSweep_RemovesStaleSessionsFromMap(t *testing.T) {
	base := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	clock := base
	s := New(10, 10*time.Second)
	s.now = func() time.Time { return clock }

	s.Record("stale", ev("old"))
	s.Record("fresh-too", ev("also-old"))

	// Advance past TTL to trigger sweep on next Record call.
	clock = base.Add(11 * time.Second)
	s.Record("new-sess", ev("new"))

	s.mu.Lock()
	_, stalePresent := s.events["stale"]
	_, freshTooPresent := s.events["fresh-too"]
	s.mu.Unlock()

	if stalePresent {
		t.Error("stale session should have been swept from the map")
	}
	if freshTooPresent {
		t.Error("fresh-too session (all events expired) should have been swept from the map")
	}
}

func TestConcurrentRecordRecent_NoRace(t *testing.T) {
	s := New(20, time.Minute)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sid := "sess-" + strconv.Itoa(id%3)
			for i := range 100 {
				s.Record(sid, ev(strconv.Itoa(i)))
				_ = s.Recent(sid)
			}
		}(w)
	}
	wg.Wait()
}
