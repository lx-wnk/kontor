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

func TestRetryResetAfter429(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	fiveReset := now.Add(2 * time.Hour)
	weeklyReset := now.Add(72 * time.Hour)
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name   string
		sample *Sample
		want   *time.Time
	}{
		{
			name:   "no sample",
			sample: nil,
		},
		{
			name:   "sample without windows",
			sample: &Sample{},
		},
		{
			name:   "over-threshold 5h window = its reset, not the later weekly one",
			sample: &Sample{FiveHour: &Window{UsedPct: 95, ResetsAt: fiveReset}, SevenDay: &Window{UsedPct: 50, ResetsAt: weeklyReset}},
			want:   &fiveReset,
		},
		{
			name:   "over-threshold weekly window = its reset, not the earlier 5h one",
			sample: &Sample{FiveHour: &Window{UsedPct: 50, ResetsAt: fiveReset}, SevenDay: &Window{UsedPct: 99, ResetsAt: weeklyReset}},
			want:   &weeklyReset,
		},
		{
			name:   "both over threshold = the blocking decision's later reset",
			sample: &Sample{FiveHour: &Window{UsedPct: 95, ResetsAt: fiveReset}, SevenDay: &Window{UsedPct: 99, ResetsAt: weeklyReset}},
			want:   &weeklyReset,
		},
		{
			name:   "none over threshold = earliest future reset (5h, not 7d)",
			sample: &Sample{FiveHour: &Window{UsedPct: 70, ResetsAt: fiveReset}, SevenDay: &Window{UsedPct: 40, ResetsAt: weeklyReset}},
			want:   &fiveReset,
		},
		{
			name:   "none over threshold, 5h reset order reversed = still the earliest",
			sample: &Sample{FiveHour: &Window{UsedPct: 70, ResetsAt: weeklyReset}, SevenDay: &Window{UsedPct: 40, ResetsAt: fiveReset}},
			want:   &fiveReset,
		},
		{
			name:   "none over threshold, only weekly present",
			sample: &Sample{SevenDay: &Window{UsedPct: 40, ResetsAt: weeklyReset}},
			want:   &weeklyReset,
		},
		{
			name:   "none over threshold, past 5h reset ignored",
			sample: &Sample{FiveHour: &Window{UsedPct: 70, ResetsAt: past}, SevenDay: &Window{UsedPct: 40, ResetsAt: weeklyReset}},
			want:   &weeklyReset,
		},
		{
			name:   "all resets in the past",
			sample: &Sample{FiveHour: &Window{UsedPct: 70, ResetsAt: past}, SevenDay: &Window{UsedPct: 40, ResetsAt: past}},
		},
		{
			name:   "over-threshold window already reset falls back to the other future reset",
			sample: &Sample{FiveHour: &Window{UsedPct: 95, ResetsAt: past}, SevenDay: &Window{UsedPct: 40, ResetsAt: weeklyReset}},
			want:   &weeklyReset,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetryResetAfter429(tt.sample, now, 90, 95)
			switch {
			case tt.want == nil && got != nil:
				t.Errorf("RetryResetAfter429() = %v, want nil", *got)
			case tt.want != nil && got == nil:
				t.Errorf("RetryResetAfter429() = nil, want %v", *tt.want)
			case tt.want != nil && !got.Equal(*tt.want):
				t.Errorf("RetryResetAfter429() = %v, want %v", *got, *tt.want)
			}
		})
	}
}
