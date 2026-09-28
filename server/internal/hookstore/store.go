// Package hookstore holds an in-memory, bounded ring of recent lifecycle-hook
// events keyed by session ID. It enriches the process/JSONL scan with per-event
// granularity when the opt-in hook receiver is installed.
//
// Deliberately a leaf package: it imports only the sdk and the standard library
// so the merger (read side) and the hooks API (write side) can both depend on it
// without creating an import cycle. There is NO persistence — events live only
// for the server process lifetime, honoring the agent-side dual-persistence rule
// (sensitive tool payloads are never written to disk or a database).
package hookstore

import (
	"sync"
	"time"

	"github.com/lx-wnk/kontor/sdk"
)

// defaultPerSessionCap is used when New receives a non-positive cap.
const defaultPerSessionCap = 50

// DefaultTTL is the retention window for hook events. Events older than this are
// pruned on access, bounding memory for sessions that go quiet. 30 minutes
// comfortably covers the idle threshold (5 min) plus review time.
const DefaultTTL = 30 * time.Minute

// entry pairs a recorded event with its arrival time so TTL pruning does not
// have to re-parse the RFC3339 string on every access.
type entry struct {
	ev sdk.HookEvent
	at time.Time
}

// Store is a concurrency-safe, per-session bounded ring of hook events.
// The zero value is not usable — construct with New.
type Store struct {
	mu        sync.Mutex
	cap       int
	ttl       time.Duration // <=0 disables TTL pruning
	events    map[string][]entry
	now       func() time.Time // injectable clock; defaults to time.Now
	lastSwept time.Time        // guards the global sweep to at most once per ttl
}

// New returns a Store retaining at most perSessionCap events per session and
// pruning entries older than ttl. A non-positive perSessionCap falls back to
// defaultPerSessionCap; a non-positive ttl disables TTL pruning.
func New(perSessionCap int, ttl time.Duration) *Store {
	if perSessionCap < 1 {
		perSessionCap = defaultPerSessionCap
	}
	return &Store{
		cap:    perSessionCap,
		ttl:    ttl,
		events: make(map[string][]entry),
		now:    time.Now,
	}
}

// Record appends ev to the session's ring, pruning expired entries and evicting
// the oldest when the per-session cap is exceeded (FIFO). Empty sessionID is
// ignored — events without a session cannot be attributed to an agent.
func (s *Store) Record(sessionID string, ev sdk.HookEvent) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	buf := s.pruneLocked(s.events[sessionID], now)
	buf = append(buf, entry{ev: ev, at: now})
	if len(buf) > s.cap {
		// Copy the tail into a fresh slice so the evicted head's backing array
		// is released rather than retained behind a reslice.
		trimmed := make([]entry, s.cap)
		copy(trimmed, buf[len(buf)-s.cap:])
		buf = trimmed
	}
	s.events[sessionID] = buf
	s.sweepLocked(now)
}

// Recent returns the session's non-expired events, newest first. It returns nil
// (not an empty slice) when the session has no live events, so callers can omit
// the field entirely and keep payloads byte-identical for hook-less clients.
func (s *Store) Recent(sessionID string) []sdk.HookEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf := s.pruneLocked(s.events[sessionID], s.now())
	if len(buf) == 0 {
		delete(s.events, sessionID)
		return nil
	}
	s.events[sessionID] = buf

	out := make([]sdk.HookEvent, len(buf))
	for i, e := range buf {
		out[len(buf)-1-i] = e.ev
	}
	return out
}

// sweepLocked removes sessions whose newest event has aged past the TTL. It
// runs at most once per TTL interval to keep Record at O(1) in the common case.
// Caller must hold s.mu.
func (s *Store) sweepLocked(now time.Time) {
	if s.ttl <= 0 || now.Sub(s.lastSwept) < s.ttl {
		return
	}
	cutoff := now.Add(-s.ttl)
	for sid, buf := range s.events {
		if len(buf) > 0 && !buf[len(buf)-1].at.After(cutoff) {
			delete(s.events, sid)
		}
	}
	s.lastSwept = now
}

// pruneLocked drops entries older than the TTL. Caller must hold s.mu.
func (s *Store) pruneLocked(buf []entry, now time.Time) []entry {
	if s.ttl <= 0 || len(buf) == 0 {
		return buf
	}
	cutoff := now.Add(-s.ttl)
	expired := 0
	for expired < len(buf) && !buf[expired].at.After(cutoff) {
		expired++
	}
	if expired == 0 {
		return buf
	}
	// Entries are append-ordered (oldest first), so the expired ones are a
	// prefix — copy the surviving suffix into a fresh slice.
	survivors := make([]entry, len(buf)-expired)
	copy(survivors, buf[expired:])
	return survivors
}
