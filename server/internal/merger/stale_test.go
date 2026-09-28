package merger

import (
	"errors"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

func newTestTracker(sessions map[string]*parser.SessionData) *staleTracker {
	t := newStaleTracker()
	t.parseFn = func(path string) (*parser.SessionData, error) { return sessions[path], nil }
	return t
}

func TestStaleTracker_EmitsFinishedForDeadChannelAgent(t *testing.T) {
	tr := newTestTracker(
		map[string]*parser.SessionData{
			"/p/s1.jsonl": {SessionID: "s1", LastActivity: time.Now(), Model: "claude-opus-4-8"},
		},
	)
	tr.record(42, liveSnapshot{sessionID: "s1", path: "/p/s1.jsonl", projectPath: "/proj", provider: sdk.ProviderClaude})

	stale := tr.buildStale(map[int]bool{}, 0) // pid 42 no longer live

	if len(stale) != 1 {
		t.Fatalf("want 1 stale agent, got %d", len(stale))
	}
	if stale[0].Status != sdk.AgentStatusFinished {
		t.Errorf("status = %q, want finished", stale[0].Status)
	}
	if stale[0].SessionID != "s1" || stale[0].PID != 42 {
		t.Errorf("got sessionID=%q pid=%d", stale[0].SessionID, stale[0].PID)
	}
	if !stale[0].ChannelAvailable || stale[0].LiveInjectable {
		t.Errorf("want ChannelAvailable=true LiveInjectable=false, got %v/%v", stale[0].ChannelAvailable, stale[0].LiveInjectable)
	}
}

func TestStaleTracker_DismissForgets(t *testing.T) {
	tr := newTestTracker(map[string]*parser.SessionData{"/p/s1.jsonl": {SessionID: "s1", LastActivity: time.Now()}})
	tr.record(42, liveSnapshot{sessionID: "s1", path: "/p/s1.jsonl"})

	if got := tr.buildStale(map[int]bool{}, 0); len(got) != 1 {
		t.Fatalf("want 1 stale agent before dismiss, got %d", len(got))
	}

	tr.dismiss(42)

	if got := tr.buildStale(map[int]bool{}, 0); len(got) != 0 {
		t.Fatalf("want tracker to have forgotten pid 42 after dismiss, got %d", len(got))
	}
}

func TestStaleTracker_RetainsOnParseErrorThenEmitsWhenParseSucceeds(t *testing.T) {
	tr := newTestTracker(nil)
	tr.parseFn = func(path string) (*parser.SessionData, error) { return nil, errors.New("boom") }
	tr.record(42, liveSnapshot{sessionID: "s1", path: "/p/s1.jsonl", projectPath: "/proj", provider: sdk.ProviderClaude})

	if got := tr.buildStale(map[int]bool{}, 0); len(got) != 0 {
		t.Fatalf("want 0 on parse error, got %d", len(got))
	}
	// still erroring: tracker must retain the pid (retry, not forget)
	if got := tr.buildStale(map[int]bool{}, 0); len(got) != 0 {
		t.Fatalf("want 0 on second parse error, got %d", len(got))
	}

	// flip parseFn to a valid session: retention proven by emitting now
	tr.parseFn = func(path string) (*parser.SessionData, error) {
		return &parser.SessionData{SessionID: "s1", LastActivity: time.Now()}, nil
	}
	stale := tr.buildStale(map[int]bool{}, 0)
	if len(stale) != 1 {
		t.Fatalf("want 1 finished agent after parse recovers, got %d", len(stale))
	}
	if stale[0].SessionID != "s1" || stale[0].PID != 42 {
		t.Errorf("got sessionID=%q pid=%d", stale[0].SessionID, stale[0].PID)
	}
}

func TestStaleTracker_SkipsWhenSessionIDEmpty(t *testing.T) {
	tr := newTestTracker(
		map[string]*parser.SessionData{"/p/s1.jsonl": {}},
	)
	tr.record(42, liveSnapshot{sessionID: "s1", path: "/p/s1.jsonl"})

	if got := tr.buildStale(map[int]bool{}, 0); len(got) != 0 {
		t.Fatalf("want 0 for empty SessionID, got %d", len(got))
	}
}

func TestStaleTracker_SkipsLivePID(t *testing.T) {
	tr := newTestTracker(map[string]*parser.SessionData{"/p/s1.jsonl": {SessionID: "s1", LastActivity: time.Now()}})
	tr.record(42, liveSnapshot{sessionID: "s1", path: "/p/s1.jsonl"})

	stale := tr.buildStale(map[int]bool{42: true}, 0) // pid 42 still live
	if len(stale) != 0 {
		t.Fatalf("want 0 (pid still live), got %d", len(stale))
	}
}
