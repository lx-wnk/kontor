package history

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func promptLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`, ts, text)
}

func assistantLine(ts string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","model":"claude-opus-4","usage":{"input_tokens":10,"output_tokens":5}}}`, ts)
}

func toolResultLine(ts string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`, ts)
}

func costStateLine(cost float64) string {
	return fmt.Sprintf(`{"type":"cost-state","totalCostUSD":%v,"totalAPIDuration":1200,"totalToolDuration":300,"totalLinesAdded":7,"totalLinesRemoved":2,"hasUnknownModelCost":true,"modelUsage":{"claude-opus-4":{"costUSD":%v,"inputTokens":1}}}`, cost, cost)
}

func mustParse(t *testing.T, lines ...string) sessionStats {
	t.Helper()
	stats, err := parseSession(strings.NewReader(strings.Join(lines, "\n")))
	require.NoError(t, err)
	return stats
}

func TestParseSession_CostStateLastWins(t *testing.T) {
	stats := mustParse(t,
		promptLine("2025-01-15T10:00:00Z", "hi"),
		assistantLine("2025-01-15T10:00:05Z"),
		costStateLine(1.0),
		costStateLine(2.5),
	)
	require.NotNil(t, stats.CostState)
	assert.Equal(t, costState{
		TotalCostUSD:     2.5,
		APIDurationMs:    1200,
		ToolDurationMs:   300,
		LinesAdded:       7,
		LinesRemoved:     2,
		UnknownModelCost: true,
		ModelCosts:       map[string]float64{"claude-opus-4": 2.5},
	}, *stats.CostState)
}

func TestParseSession_StaleCostStateIsKeptAndFlagged(t *testing.T) {
	stats := mustParse(t,
		promptLine("2025-01-15T10:00:00Z", "hi"),
		costStateLine(1.0),
		assistantLine("2025-01-15T10:00:05Z"),
	)
	require.NotNil(t, stats.CostState)
	assert.True(t, stats.CostStateStale)
	assert.InDelta(t, 1.0, stats.CostState.TotalCostUSD, 1e-12)
}

func TestParseSession_CostStateAfterAssistantIsFresh(t *testing.T) {
	stats := mustParse(t,
		costStateLine(1.0),
		assistantLine("2025-01-15T10:00:05Z"),
		costStateLine(2.0),
	)
	require.NotNil(t, stats.CostState)
	assert.False(t, stats.CostStateStale)
	assert.InDelta(t, 2.0, stats.CostState.TotalCostUSD, 1e-12)
}

func TestParseSession_NoCostState(t *testing.T) {
	stats := mustParse(t,
		promptLine("2025-01-15T10:00:00Z", "hi"),
		assistantLine("2025-01-15T10:00:05Z"),
		assistantLine("2025-01-15T10:00:06Z"),
	)
	assert.Nil(t, stats.CostState)
	assert.Equal(t, 20, stats.Usage.InputTokens)
	assert.Equal(t, 10, stats.Usage.OutputTokens)
}

func TestParseSession_TurnsAndEstimate(t *testing.T) {
	stats := mustParse(t,
		promptLine("2025-01-15T10:00:00Z", "first"),
		assistantLine("2025-01-15T10:00:10Z"),
		toolResultLine("2025-01-15T10:00:40Z"),
		assistantLine("2025-01-15T10:01:00Z"),
		promptLine("2025-01-15T11:00:00Z", "second"),
		assistantLine("2025-01-15T11:00:05Z"),
	)
	assert.Equal(t, 2, stats.Turns)
	assert.Equal(t, int64(65000), stats.EstimateMs)
}

func TestParseSession_GapOver30MinSkipped(t *testing.T) {
	stats := mustParse(t,
		promptLine("2025-01-15T09:59:50Z", "go"),
		assistantLine("2025-01-15T10:00:00Z"),
		toolResultLine("2025-01-15T10:31:00Z"),
		assistantLine("2025-01-15T10:31:05Z"),
	)
	assert.Equal(t, int64(15000), stats.EstimateMs)
}

func TestParseSession_MixedToolResultIsNotATurn(t *testing.T) {
	mixed := `{"type":"user","timestamp":"2025-01-15T10:00:20Z","message":{"role":"user","content":[{"type":"text","text":"note"},{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	stats := mustParse(t,
		promptLine("2025-01-15T10:00:00Z", "go"),
		assistantLine("2025-01-15T10:00:10Z"),
		mixed,
		assistantLine("2025-01-15T10:00:30Z"),
	)
	assert.Equal(t, 1, stats.Turns)
	assert.Equal(t, int64(30000), stats.EstimateMs)
}

func TestParseSession_MetaAndUnansweredPromptsNotTurns(t *testing.T) {
	meta := `{"type":"user","isMeta":true,"timestamp":"2025-01-15T10:00:20Z","message":{"role":"user","content":"caveat"}}`
	stats := mustParse(t,
		promptLine("2025-01-15T10:00:00Z", "go"),
		assistantLine("2025-01-15T10:00:10Z"),
		meta,
		assistantLine("2025-01-15T10:00:30Z"),
		promptLine("2025-01-15T10:05:00Z", "unanswered"),
	)
	assert.Equal(t, 1, stats.Turns)
	assert.Equal(t, int64(30000), stats.EstimateMs)
}

func TestParseSession_TitleOrder(t *testing.T) {
	custom := `{"type":"custom-title","customTitle":"Renamed session"}`
	ai := `{"type":"ai-title","aiTitle":"Generated title"}`
	prompt := promptLine("2025-01-15T10:00:00Z", "Fix the login bug")
	command := promptLine("2025-01-15T09:59:00Z", "<command-name>/clear</command-name>")

	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"custom beats ai and prompt", []string{prompt, ai, custom}, "Renamed session"},
		{"ai beats prompt", []string{prompt, ai}, "Generated title"},
		{"prompt alone", []string{prompt}, "Fix the login bug"},
		{"system-injected prompt skipped", []string{command, prompt}, "Fix the login bug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mustParse(t, tc.lines...).Title)
		})
	}
}

func TestParseSession_TitleFromFirstTextBlock(t *testing.T) {
	line := `{"type":"user","timestamp":"2025-01-15T10:00:00Z","message":{"role":"user","content":[{"type":"image"},{"type":"text","text":"Describe this"}]}}`
	assert.Equal(t, "Describe this", mustParse(t, line).Title)
}

func TestParseSession_TitleCappedAndScrubbed(t *testing.T) {
	key := "sk-ant-" + strings.Repeat("a", 30)
	text := "Use " + key + "\n\nthen " + strings.Repeat("x", 200-len("Use \n\nthen ")-len(key))
	require.Equal(t, 200, utf8.RuneCountInString(text))

	title := mustParse(t, promptLine("2025-01-15T10:00:00Z", text)).Title
	assert.LessOrEqual(t, utf8.RuneCountInString(title), 120)
	assert.NotContains(t, title, "\n")
	assert.Contains(t, title, "[REDACTED]")
	assert.NotContains(t, title, key)
}

func TestParseSession_StartedAt(t *testing.T) {
	stats := mustParse(t,
		`{"type":"summary","summary":"x"}`,
		`{"type":"system","timestamp":"2025-01-15T09:00:00Z"}`,
		promptLine("2025-01-15T10:00:00Z", "go"),
		assistantLine("2025-01-15T10:00:05Z"),
	)
	assert.True(t, stats.StartedAt.Equal(time.Date(2025, 1, 15, 9, 0, 0, 0, time.UTC)), "got %v", stats.StartedAt)
	assert.True(t, stats.LastActivity.Equal(time.Date(2025, 1, 15, 10, 0, 5, 0, time.UTC)), "got %v", stats.LastActivity)
}
