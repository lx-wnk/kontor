package planusage

import (
	"strconv"
	"time"
)

// Decision is the outcome of a usage gate check.
type Decision struct {
	Block bool
	Until time.Time
	// Reason is a human-readable explanation, empty when not blocked.
	Reason string
}

// Decide checks whether an account's usage exceeds the configured thresholds.
//
// Block iff window != nil AND window.ResetsAt.After(now) AND
// window.UsedPct >= threshold — regardless of sample age (stale-above still
// blocks, because the risk of a wasted spawn outweighs the risk of a stale
// reading that dropped below the threshold).
//
// Absent window or nil sample → open (fail-open).
// ResetsAt in past → open (the window has reset).
func Decide(s *Sample, now time.Time, fiveHourPct, sevenDayPct float64) Decision {
	if s == nil {
		return Decision{}
	}
	var block bool
	var until time.Time
	var reason string

	if w := s.FiveHour; w != nil && w.ResetsAt.After(now) && w.UsedPct >= fiveHourPct {
		block = true
		until = w.ResetsAt
		reason = "5-hour usage at " + pctStr(w.UsedPct) + "% (threshold " + pctStr(fiveHourPct) + "%)"
	}
	if w := s.SevenDay; w != nil && w.ResetsAt.After(now) && w.UsedPct >= sevenDayPct {
		block = true
		if w.ResetsAt.After(until) {
			until = w.ResetsAt
		}
		if reason != "" {
			reason += "; "
		}
		reason += "weekly usage at " + pctStr(w.UsedPct) + "% (threshold " + pctStr(sevenDayPct) + "%)"
	}
	return Decision{Block: block, Until: until, Reason: reason}
}

func pctStr(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// RetryResetAfter429 returns when a rate-limited run may be retried, or nil
// when the sample says nothing useful.
//
// A window at or over its threshold explains the 429, so its reset (per
// Decide) wins. Otherwise the stored sample is stale: headless `claude -p`
// spawns never run the statusline, so real usage may sit above what the last
// interactive session reported. The earliest future reset is the first moment
// any window can free up; the weekly reset (up to seven days out) must not
// delay a retry that a 5-hour reset would unblock.
func RetryResetAfter429(s *Sample, now time.Time, fiveHourPct, sevenDayPct float64) *time.Time {
	if s == nil {
		return nil
	}
	if d := Decide(s, now, fiveHourPct, sevenDayPct); d.Block {
		return &d.Until
	}
	var earliest time.Time
	for _, w := range []*Window{s.FiveHour, s.SevenDay} {
		if w != nil && w.ResetsAt.After(now) && (earliest.IsZero() || w.ResetsAt.Before(earliest)) {
			earliest = w.ResetsAt
		}
	}
	if earliest.IsZero() {
		return nil
	}
	return &earliest
}
