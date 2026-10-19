package planusage

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestSetGetRoundtrip(t *testing.T) {
	s := NewStore()
	now := time.Now()
	sample := Sample{
		FiveHour:  &Window{UsedPct: 42.5, ResetsAt: now.Add(3 * time.Hour)},
		SevenDay:  &Window{UsedPct: 10.0, ResetsAt: now.Add(5 * 24 * time.Hour)},
		SampledAt: now,
	}
	s.Set("/home/user/.claude", sample)

	got, ok := s.Get("/home/user/.claude")
	if !ok {
		t.Fatal("expected sample to exist")
	}
	if got.FiveHour.UsedPct != 42.5 {
		t.Errorf("FiveHour.UsedPct = %f, want 42.5", got.FiveHour.UsedPct)
	}
	if got.SevenDay.UsedPct != 10.0 {
		t.Errorf("SevenDay.UsedPct = %f, want 10.0", got.SevenDay.UsedPct)
	}
}

func TestGetMissing(t *testing.T) {
	s := NewStore()
	_, ok := s.Get("/nonexistent")
	if ok {
		t.Error("expected missing entry")
	}
}

func TestTildeCanonicalization(t *testing.T) {
	s := NewStore()
	now := time.Now()
	sample := Sample{FiveHour: &Window{UsedPct: 80, ResetsAt: now.Add(time.Hour)}, SampledAt: now}
	// Set with tilde, get with expanded path
	s.Set("~/.claude-personal", sample)

	// Get with tilde should find it
	got, ok := s.Get("~/.claude-personal")
	if !ok {
		t.Fatal("expected sample from tilde path")
	}
	if got.FiveHour.UsedPct != 80 {
		t.Errorf("UsedPct = %f, want 80", got.FiveHour.UsedPct)
	}
}

func TestLastWriteWins(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Set("/home/user/.claude", Sample{FiveHour: &Window{UsedPct: 10, ResetsAt: now.Add(time.Hour)}, SampledAt: now})
	s.Set("/home/user/.claude", Sample{FiveHour: &Window{UsedPct: 90, ResetsAt: now.Add(time.Hour)}, SampledAt: now})

	got, _ := s.Get("/home/user/.claude")
	if got.FiveHour.UsedPct != 90 {
		t.Errorf("UsedPct = %f, want 90 (last write)", got.FiveHour.UsedPct)
	}
}

func TestAll(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Set("/a", Sample{SampledAt: now})
	s.Set("/b", Sample{SampledAt: now})

	all := s.All()
	if len(all) != 2 {
		t.Errorf("All() returned %d entries, want 2", len(all))
	}
}

func TestConcurrentWriteRace(t *testing.T) {
	s := NewStore()
	now := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Set("/home/user/.claude", Sample{
				FiveHour:  &Window{UsedPct: float64(n), ResetsAt: now.Add(time.Hour)},
				SampledAt: now,
			})
		}(i)
	}
	wg.Wait()
	_, ok := s.Get("/home/user/.claude")
	if !ok {
		t.Fatal("expected sample after concurrent writes")
	}
}

func TestValidWindow(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		w     *Window
		valid bool
	}{
		{"nil window", nil, true},
		{"valid", &Window{UsedPct: 50, ResetsAt: now.Add(time.Hour)}, true},
		{"zero pct", &Window{UsedPct: 0, ResetsAt: now.Add(time.Hour)}, true},
		{"100 pct", &Window{UsedPct: 100, ResetsAt: now.Add(time.Hour)}, true},
		{"negative pct", &Window{UsedPct: -1, ResetsAt: now.Add(time.Hour)}, false},
		{"over 100 pct", &Window{UsedPct: 101, ResetsAt: now.Add(time.Hour)}, false},
		{"NaN pct", &Window{UsedPct: math.NaN(), ResetsAt: now.Add(time.Hour)}, false},
		{"Inf pct", &Window{UsedPct: math.Inf(1), ResetsAt: now.Add(time.Hour)}, false},
		{"resets_at too far past", &Window{UsedPct: 50, ResetsAt: now.Add(-25 * time.Hour)}, false},
		{"resets_at too far future", &Window{UsedPct: 50, ResetsAt: now.Add(9 * 24 * time.Hour)}, false},
		{"resets_at near past ok", &Window{UsedPct: 50, ResetsAt: now.Add(-23 * time.Hour)}, true},
		{"resets_at near future ok", &Window{UsedPct: 50, ResetsAt: now.Add(7 * 24 * time.Hour)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidWindow(tt.w, now); got != tt.valid {
				t.Errorf("ValidWindow() = %v, want %v", got, tt.valid)
			}
		})
	}
}
