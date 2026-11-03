package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/claudemodel"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

const (
	maxTitleRunes = 120
	maxTurnGap    = 30 * time.Minute
)

type sessionStats struct {
	Usage          sdk.TokenUsage
	Model          string
	Cwd            string
	StartedAt      time.Time
	LastActivity   time.Time
	Title          string
	Turns          int
	EstimateMs     int64
	CostState      *costState
	CostStateStale bool
}

type costState struct {
	TotalCostUSD     float64
	APIDurationMs    int64
	ToolDurationMs   int64
	LinesAdded       int
	LinesRemoved     int
	UnknownModelCost bool
	ModelCosts       map[string]float64
}

// The total* fields are only present on type == "cost-state" lines.
type jsonlEntry struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	IsMeta      bool   `json:"isMeta"`
	CustomTitle string `json:"customTitle"`
	AiTitle     string `json:"aiTitle"`
	Message     struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	TotalCostUSD        float64 `json:"totalCostUSD"`
	TotalAPIDuration    int64   `json:"totalAPIDuration"`
	TotalToolDuration   int64   `json:"totalToolDuration"`
	TotalLinesAdded     int     `json:"totalLinesAdded"`
	TotalLinesRemoved   int     `json:"totalLinesRemoved"`
	HasUnknownModelCost bool    `json:"hasUnknownModelCost"`
	ModelUsage          map[string]struct {
		CostUSD float64 `json:"costUSD"`
	} `json:"modelUsage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Assistant turns are type "assistant" in modern logs and "message" in older ones.
func (e *jsonlEntry) isAssistant() bool {
	return (e.Type == "assistant" || e.Type == "message") && e.Message.Role == "assistant"
}

func (e *jsonlEntry) userContent() (text string, toolResult bool) {
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return s, false
	}
	var blocks []contentBlock
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return "", false
	}
	found := false
	for _, b := range blocks {
		if b.Type == "tool_result" {
			toolResult = true
		}
		if b.Type == "text" && !found {
			text, found = b.Text, true
		}
	}
	return text, toolResult
}

func (e *jsonlEntry) costState() *costState {
	cs := &costState{
		TotalCostUSD:     e.TotalCostUSD,
		APIDurationMs:    e.TotalAPIDuration,
		ToolDurationMs:   e.TotalToolDuration,
		LinesAdded:       e.TotalLinesAdded,
		LinesRemoved:     e.TotalLinesRemoved,
		UnknownModelCost: e.HasUnknownModelCost,
		ModelCosts:       make(map[string]float64, len(e.ModelUsage)),
	}
	for model, u := range e.ModelUsage {
		cs.ModelCosts[model] = u.CostUSD
	}
	return cs
}

type turnClock struct {
	inTurn   bool
	answered bool
	last     time.Time
}

func (c *turnClock) start(ts time.Time) {
	*c = turnClock{inTurn: true, last: ts}
}

func (c *turnClock) tick(ts time.Time) int64 {
	if !c.inTurn || ts.IsZero() {
		return 0
	}
	prev := c.last
	c.last = ts
	if prev.IsZero() {
		return 0
	}
	if gap := ts.Sub(prev); gap >= 0 && gap <= maxTurnGap {
		return gap.Milliseconds()
	}
	return 0
}

// The scanner buffer allows single lines up to 4 MiB (large tool output).
func parseSession(r io.Reader) (sessionStats, error) {
	stats := sessionStats{Model: claudemodel.Latest(claudemodel.Sonnet)}
	var (
		clock                        turnClock
		cost                         *costState
		customTitle, aiTitle, prompt string
	)

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e jsonlEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		ts, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		if stats.StartedAt.IsZero() {
			stats.StartedAt = ts
		}
		if e.Cwd != "" {
			stats.Cwd = e.Cwd
		}
		if e.CustomTitle != "" {
			customTitle = e.CustomTitle
		}
		if e.AiTitle != "" {
			aiTitle = e.AiTitle
		}

		switch {
		case e.Type == "cost-state":
			cost = e.costState()
			stats.CostStateStale = false
		case e.isAssistant():
			stats.CostStateStale = cost != nil
			stats.addAssistant(&e, ts)
			if clock.inTurn && !clock.answered {
				clock.answered = true
				stats.Turns++
			}
			stats.EstimateMs += clock.tick(ts)
		case e.Type == "user" && !e.IsMeta:
			text, toolResult := e.userContent()
			if toolResult {
				stats.EstimateMs += clock.tick(ts)
			} else {
				clock.start(ts)
				// System-injected tags (<command-name>, …) start a turn but never name the session.
				if text = strings.TrimSpace(text); prompt == "" && !strings.HasPrefix(text, "<") {
					prompt = text
				}
			}
		}
	}
	stats.CostState = cost
	stats.Title = cleanTitle(firstNonEmpty(customTitle, aiTitle, prompt))
	if err := scanner.Err(); err != nil {
		return stats, fmt.Errorf("scan: %w", err)
	}
	return stats, nil
}

func (s *sessionStats) addAssistant(e *jsonlEntry, ts time.Time) {
	if e.Message.Model != "" {
		s.Model = e.Message.Model
	}
	if u := e.Message.Usage; u != nil {
		s.Usage.InputTokens += u.InputTokens
		s.Usage.OutputTokens += u.OutputTokens
		s.Usage.CacheCreationTokens += u.CacheCreationTokens
		s.Usage.CacheReadTokens += u.CacheReadTokens
	}
	if ts.After(s.LastActivity) {
		s.LastActivity = ts
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func cleanTitle(s string) string {
	s = parser.ScrubSecrets(strings.Join(strings.Fields(s), " "))
	if runes := []rune(s); len(runes) > maxTitleRunes {
		return string(runes[:maxTitleRunes])
	}
	return s
}
