package planusage

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(3 * time.Hour)
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name        string
		sample      *Sample
		fiveHourPct float64
		sevenDayPct float64
		wantBlock   bool
	}{
		{
			name:      "nil sample = open",
			sample:    nil,
			wantBlock: false,
		},
		{
			name:        "below threshold = open",
			sample:      &Sample{FiveHour: &Window{UsedPct: 80, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   false,
		},
		{
			name:        "at threshold = block",
			sample:      &Sample{FiveHour: &Window{UsedPct: 90, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   true,
		},
		{
			name:        "above threshold = block",
			sample:      &Sample{FiveHour: &Window{UsedPct: 95, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   true,
		},
		{
			name:        "resets_at in past = open",
			sample:      &Sample{FiveHour: &Window{UsedPct: 95, ResetsAt: past}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   false,
		},
		{
			name:        "absent window = open",
			sample:      &Sample{FiveHour: nil, SevenDay: nil},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   false,
		},
		{
			name:        "stale above threshold = blocks",
			sample:      &Sample{FiveHour: &Window{UsedPct: 95, ResetsAt: future}, SampledAt: now.Add(-30 * time.Minute)},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   true,
		},
		{
			name:        "stale below threshold = open",
			sample:      &Sample{FiveHour: &Window{UsedPct: 50, ResetsAt: future}, SampledAt: now.Add(-30 * time.Minute)},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   false,
		},
		{
			name:        "weekly only block",
			sample:      &Sample{FiveHour: &Window{UsedPct: 50, ResetsAt: future}, SevenDay: &Window{UsedPct: 96, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   true,
		},
		{
			name:        "5h only block",
			sample:      &Sample{FiveHour: &Window{UsedPct: 91, ResetsAt: future}, SevenDay: &Window{UsedPct: 50, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   true,
		},
		{
			name:        "5h absent, weekly above = block",
			sample:      &Sample{FiveHour: nil, SevenDay: &Window{UsedPct: 96, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   true,
		},
		{
			name:        "both below = open",
			sample:      &Sample{FiveHour: &Window{UsedPct: 50, ResetsAt: future}, SevenDay: &Window{UsedPct: 50, ResetsAt: future}},
			fiveHourPct: 90,
			sevenDayPct: 95,
			wantBlock:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.sample, now, tt.fiveHourPct, tt.sevenDayPct)
			if d.Block != tt.wantBlock {
				t.Errorf("Decide().Block = %v, want %v (reason: %s)", d.Block, tt.wantBlock, d.Reason)
			}
			if d.Block && d.Until.IsZero() {
				t.Error("Block=true but Until is zero")
			}
			if d.Block && d.Reason == "" {
				t.Error("Block=true but Reason is empty")
			}
		})
	}
}

func TestDecide_UntilIsMaxOfWindows(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fiveReset := now.Add(2 * time.Hour)
	weeklyReset := now.Add(48 * time.Hour)

	d := Decide(&Sample{
		FiveHour: &Window{UsedPct: 95, ResetsAt: fiveReset},
		SevenDay: &Window{UsedPct: 99, ResetsAt: weeklyReset},
	}, now, 90, 95)

	if !d.Block {
		t.Fatal("expected block")
	}
	if !d.Until.Equal(weeklyReset) {
		t.Errorf("Until = %v, want %v (the later reset)", d.Until, weeklyReset)
	}
}
