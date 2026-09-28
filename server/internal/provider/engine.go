package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// EngineResult is the parsed output of one session file: the SessionData the
// merger consumes, plus the in-file provider and cost the cost-classifier needs.
type EngineResult struct {
	Session    *parser.SessionData
	Provider   string  // in-file model provider (e.g. "ollama", "anthropic"); "" if none
	InFileCost float64 // summed in-file cost; only meaningful when Cost.Rule == CostInFile
}

// parseJSONL parses one session file per a jsonl descriptor. Unreadable lines
// are skipped. Token fields aggregate by descriptor mode; model/provider take
// the last non-empty value; in-file cost sums across matching lines.
func parseJSONL(d Descriptor, path string) (*EngineResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("provider.parseJSONL: %w", err)
	}
	defer f.Close()

	tok := d.Parse.Tokens
	var in, out, cr, cc float64
	var model, prov string
	var cost float64
	var lastActivity time.Time

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			continue
		}
		if m := firstString(obj, d.Parse.Model); m != "" {
			model = m
		}
		if p := firstString(obj, d.Parse.Provider); p != "" {
			prov = p
		}
		// Timestamps live on every line (including non-event headers), so track the
		// newest before the event filter gate — otherwise LastActivity stays zero.
		if len(d.Parse.Timestamp) > 0 {
			if vals := resolveFirst(obj, d.Parse.Timestamp); len(vals) > 0 {
				if ts := parseActivityTimestamp(vals[0]); !ts.IsZero() && ts.After(lastActivity) {
					lastActivity = ts
				}
			}
		}
		if !matchesFilter(obj, d.Parse.EventFilter) {
			continue
		}
		in = accumulate(in, obj, tok.Input, tok.Mode)
		out = accumulate(out, obj, tok.Output, tok.Mode)
		cr = accumulate(cr, obj, tok.CacheRead, tok.Mode)
		cc = accumulate(cc, obj, tok.CacheCreate, tok.Mode)
		if d.Cost.Rule == CostInFile {
			for _, v := range resolveFirst(obj, d.Cost.InFilePath) {
				cost += toFloat(v)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("provider.parseJSONL scan: %w", err)
	}

	return &EngineResult{
		Session: &parser.SessionData{
			TokenUsage: sdk.TokenUsage{
				InputTokens:         int(in),
				OutputTokens:        int(out),
				CacheReadTokens:     int(cr),
				CacheCreationTokens: int(cc),
			},
			Model:        model,
			LastActivity: lastActivity,
		},
		Provider:   prov,
		InFileCost: cost,
	}, nil
}

// parseActivityTimestamp coerces a JSON value to a time.Time. Accepts
// RFC3339/RFC3339Nano strings and millisecond-epoch float64 values (e.g. Junie).
func parseActivityTimestamp(v any) time.Time {
	switch t := v.(type) {
	case string:
		if ts, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return ts.UTC()
		}
		if ts, err := time.Parse(time.RFC3339, t); err == nil {
			return ts.UTC()
		}
	case float64:
		// millisecond epoch (json.Unmarshal decodes all JSON numbers as float64)
		return time.UnixMilli(int64(t)).UTC()
	}
	return time.Time{}
}

// accumulate folds the values at paths into acc per mode: cumulative keeps the
// last line's summed value; perMessage adds every line's values to the running
// total.
func accumulate(acc float64, obj map[string]any, paths []string, mode TokenMode) float64 {
	vals := resolveFirst(obj, paths)
	if len(vals) == 0 {
		return acc
	}
	var sum float64
	for _, v := range vals {
		sum += toFloat(v)
	}
	if mode == TokenCumulative {
		return sum
	}
	return acc + sum
}

// matchesFilter reports whether obj passes the event filter (nil filter = all).
func matchesFilter(obj map[string]any, f *EventFilter) bool {
	if f == nil {
		return true
	}
	for _, v := range resolvePath(obj, f.Path) {
		if s, ok := v.(string); ok && s == f.Equals {
			return true
		}
	}
	return false
}
