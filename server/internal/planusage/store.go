// Package planusage stores the latest Claude plan usage sample per account
// and gates pipeline spawns before the subscription limit is hit.
//
// Accounts are keyed by their canonical Claude config directory (tilde-expanded,
// cleaned). The store is in-memory, last-write-wins — no history, no DB table.
package planusage

import (
	"maps"
	"math"
	"path/filepath"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/pathutil"
)

// Window holds one rate-limit window (5-hour or 7-day).
type Window struct {
	UsedPct  float64   `json:"usedPct"`
	ResetsAt time.Time `json:"resetsAt"`
}

// Sample is a single statusline snapshot for one account.
type Sample struct {
	FiveHour  *Window   `json:"fiveHour,omitempty"`
	SevenDay  *Window   `json:"sevenDay,omitempty"`
	SampledAt time.Time `json:"sampledAt"`
}

// Store is a concurrent-safe in-memory map of config-dir → latest Sample.
type Store struct {
	mu      sync.RWMutex
	entries map[string]Sample
}

// NewStore creates an empty Store.
func NewStore() *Store {
	return &Store{entries: make(map[string]Sample)}
}

// canonicalKey normalises a config directory path: tilde-expand then clean.
func canonicalKey(configDir string) string {
	return filepath.Clean(pathutil.ExpandLeadingTilde(configDir))
}

// Set records a sample for configDir (last write wins).
func (s *Store) Set(configDir string, sample Sample) {
	key := canonicalKey(configDir)
	s.mu.Lock()
	s.entries[key] = sample
	s.mu.Unlock()
}

// Get returns the latest sample for configDir, or false if none recorded.
func (s *Store) Get(configDir string) (Sample, bool) {
	key := canonicalKey(configDir)
	s.mu.RLock()
	sample, ok := s.entries[key]
	s.mu.RUnlock()
	return sample, ok
}

// All returns a snapshot of every account's latest sample, keyed by canonical
// config dir.
func (s *Store) All() map[string]Sample {
	s.mu.RLock()
	out := make(map[string]Sample, len(s.entries))
	maps.Copy(out, s.entries)
	s.mu.RUnlock()
	return out
}

// ValidWindow checks that a window's fields are within the expected range.
// usedPct must be in [0,100] and finite; resetsAt must be in [now-24h, now+8d].
func ValidWindow(w *Window, now time.Time) bool {
	if w == nil {
		return true
	}
	if math.IsNaN(w.UsedPct) || math.IsInf(w.UsedPct, 0) {
		return false
	}
	if w.UsedPct < 0 || w.UsedPct > 100 {
		return false
	}
	earliest := now.Add(-24 * time.Hour)
	latest := now.Add(8 * 24 * time.Hour)
	if w.ResetsAt.Before(earliest) || w.ResetsAt.After(latest) {
		return false
	}
	return true
}
