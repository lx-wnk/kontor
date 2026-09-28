package parser_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/require"
)

// writeSessionLines is a helper that writes JSONL lines to a temp file and returns its path.
func writeSessionLines(t *testing.T, lines []string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "session*.jsonl")
	require.NoError(t, err)
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return f.Name()
}

// buildAssistantEntry creates a valid JSONL assistant entry string.
func buildAssistantEntry(t *testing.T, model string, inputTokens, outputTokens int, blocks []map[string]any) string {
	t.Helper()
	contentBytes, err := json.Marshal(blocks)
	require.NoError(t, err)

	msg := map[string]any{
		"role":    "assistant",
		"model":   model,
		"content": json.RawMessage(contentBytes),
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	msgBytes, err := json.Marshal(msg)
	require.NoError(t, err)

	entry := map[string]any{
		"type":      "assistant",
		"timestamp": "2025-01-15T10:30:00.000Z",
		"message":   json.RawMessage(msgBytes),
	}
	b, err := json.Marshal(entry)
	require.NoError(t, err)
	return string(b)
}

// TestParseSessionFile_Empty verifies that an empty JSONL file returns a valid
// (but zero-activity) SessionData without an error.
func TestParseSessionFile_Empty(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "empty*.jsonl")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	data, err := parser.ParseSessionFile(f.Name())
	require.NoError(t, err)
	require.NotNil(t, data)
	require.Equal(t, 0, data.ConversationTurns)
	require.Empty(t, data.LastTools)
}

// TestParseSessionFile_MalformedLines verifies that malformed (non-JSON) lines
// are skipped gracefully and valid lines are still parsed.
func TestParseSessionFile_MalformedLines(t *testing.T) {
	validLine := buildAssistantEntry(t, "claude-sonnet-4-6", 100, 50, []map[string]any{
		{"type": "text", "text": "hello"},
	})
	path := writeSessionLines(t, []string{
		"not-valid-json-at-all",
		"{broken:",
		validLine,
		"another-invalid-line",
	})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, data.ConversationTurns)
	require.Equal(t, "claude-sonnet-4-6", data.Model)
}

// TestParseSessionFile_TokenAccumulation verifies that tokens are accumulated
// correctly across multiple assistant turns.
func TestParseSessionFile_TokenAccumulation(t *testing.T) {
	line1 := buildAssistantEntry(t, "claude-sonnet-4-6", 100, 50, nil)
	line2 := buildAssistantEntry(t, "claude-sonnet-4-6", 200, 100, nil)
	path := writeSessionLines(t, []string{line1, line2})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, data.ConversationTurns)
	require.Equal(t, 300, data.TokenUsage.InputTokens)
	require.Equal(t, 150, data.TokenUsage.OutputTokens)
}

// TestParseSessionFile_ConvergenceDetection verifies that 5 identical consecutive
// tool calls trigger the convergence alert.
func TestParseSessionFile_ConvergenceDetection(t *testing.T) {
	lines := make([]string, 5)
	for i := range lines {
		lines[i] = buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
			{"type": "tool_use", "name": "Bash", "input": json.RawMessage(`{"command":"ls"}`)},
		})
	}
	path := writeSessionLines(t, lines)

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.True(t, data.ConvergenceAlert)
	require.Equal(t, "Bash", data.ConvergenceToolName)
}

// TestParseSessionFile_NoConvergenceWithMixedTools verifies that mixed tool calls
// do NOT trigger the convergence alert.
func TestParseSessionFile_NoConvergenceWithMixedTools(t *testing.T) {
	toolNames := []string{"Bash", "Read", "Write", "Bash", "Read"}
	lines := make([]string, len(toolNames))
	for i, name := range toolNames {
		lines[i] = buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
			{"type": "tool_use", "name": name, "input": json.RawMessage(`{}`)},
		})
	}
	path := writeSessionLines(t, lines)

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.False(t, data.ConvergenceAlert)
}

// TestParseSessionFile_ErrorStateQuota verifies that quota-exceeded API errors set the error state.
func TestParseSessionFile_ErrorStateQuota(t *testing.T) {
	line := buildAPIErrorEntry(t, "Error: quota exceeded for this month", true, 429)
	path := writeSessionLines(t, []string{line})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, sdk.ErrorStateQuotaExhausted, data.ErrorState)
}

// TestParseSessionFile_ErrorStateRateLimit verifies that rate-limit API errors set the error state.
func TestParseSessionFile_ErrorStateRateLimit(t *testing.T) {
	line := buildAPIErrorEntry(t, "Too Many Requests — rate limit reached", true, 429)
	path := writeSessionLines(t, []string{line})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, sdk.ErrorStateRateLimited, data.ErrorState)
}

// TestParseSessionFile_ErrorStateAuth verifies that auth-failure API errors set the error state.
func TestParseSessionFile_ErrorStateAuth(t *testing.T) {
	line := buildAPIErrorEntry(t, "Unauthorized — invalid api key provided", true, 401)
	path := writeSessionLines(t, []string{line})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, sdk.ErrorStateAuthFailed, data.ErrorState)
}

// TestParseSessionFile_NoErrorStateOnCleanText verifies that clean output leaves ErrorState empty.
func TestParseSessionFile_NoErrorStateOnCleanText(t *testing.T) {
	line := buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
		{"type": "text", "text": "All tests passed successfully!"},
	})
	path := writeSessionLines(t, []string{line})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Empty(t, data.ErrorState)
}

// TestParseSessionFile_LastToolsCapped verifies that LastTools returns at most 5 entries.
func TestParseSessionFile_LastToolsCapped(t *testing.T) {
	toolNames := []string{"Read", "Write", "Bash", "Glob", "Grep", "Edit", "MultiEdit"}
	lines := make([]string, len(toolNames))
	for i, name := range toolNames {
		lines[i] = buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
			{"type": "tool_use", "name": name, "input": json.RawMessage(`{}`)},
		})
	}
	path := writeSessionLines(t, lines)

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Len(t, data.LastTools, 5)
	// LastTools must contain the last 5 tools in order
	names := make([]string, 0, len(data.LastTools))
	for _, tl := range data.LastTools {
		names = append(names, tl.Name)
	}
	require.Equal(t, []string{"Bash", "Glob", "Grep", "Edit", "MultiEdit"}, names)
}

// TestParseSessionFile_ToolCounts verifies that tool usage counts are tracked correctly.
func TestParseSessionFile_ToolCounts(t *testing.T) {
	lines := []string{
		buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
			{"type": "tool_use", "name": "Bash", "input": json.RawMessage(`{}`)},
		}),
		buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
			{"type": "tool_use", "name": "Bash", "input": json.RawMessage(`{}`)},
		}),
		buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
			{"type": "tool_use", "name": "Read", "input": json.RawMessage(`{}`)},
		}),
	}
	path := writeSessionLines(t, lines)

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, 2, data.ToolCounts["Bash"])
	require.Equal(t, 1, data.ToolCounts["Read"])
}

// TestParseSessionFile_ModelTracked verifies that the model field from the last
// non-empty assistant message is used.
func TestParseSessionFile_ModelTracked(t *testing.T) {
	lines := []string{
		buildAssistantEntry(t, "claude-haiku-4-5", 10, 5, nil),
		buildAssistantEntry(t, "claude-sonnet-4-6", 20, 10, nil),
	}
	path := writeSessionLines(t, lines)

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", data.Model)
}

// TestParseSessionFile_NonExistentFile verifies that a missing file returns an error.
func TestParseSessionFile_NonExistentFile(t *testing.T) {
	_, err := parser.ParseSessionFile(filepath.Join(t.TempDir(), "missing.jsonl"))
	require.Error(t, err)
}

// TestParseSessionFile_BlankLines verifies that blank lines in the JSONL are skipped.
func TestParseSessionFile_BlankLines(t *testing.T) {
	validLine := buildAssistantEntry(t, "claude-sonnet-4-6", 50, 25, nil)
	path := writeSessionLines(t, []string{"", "   ", validLine, "", "   "})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, data.ConversationTurns)
}

// TestParseSessionFile_LargeFile verifies that very large JSONL files are handled
// by the tail-read mechanism without panicking or erroring.
func TestParseSessionFile_LargeFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "large*.jsonl")
	require.NoError(t, err)
	// Write ~200KB of assistant lines (each line ~200 bytes).
	line := buildAssistantEntry(t, "claude-sonnet-4-6", 10, 5, []map[string]any{
		{"type": "text", "text": fmt.Sprintf("output line %d", 0)},
	})
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	_, err = f.WriteString(sb.String())
	require.NoError(t, err)
	require.NoError(t, f.Close())

	data, err := parser.ParseSessionFile(f.Name())
	require.NoError(t, err)
	require.NotNil(t, data)
	// Should have parsed at least some turns from the tail window.
	require.Positive(t, data.ConversationTurns)
}

// TestAllClaudeConfigDirs_DashboardEnvOverride verifies that DASHBOARD_CLAUDE_CONFIG_DIRS
// is listed first when set, with no duplicate entries.
func TestAllClaudeConfigDirs_DashboardEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	dirs := parser.AllClaudeConfigDirs()
	require.NotEmpty(t, dirs)
	require.Equal(t, dir, dirs[0], "explicit DASHBOARD_CLAUDE_CONFIG_DIRS must come first")

	// No duplicates.
	seen := make(map[string]int)
	for _, d := range dirs {
		seen[d]++
	}
	for d, count := range seen {
		require.Equal(t, 1, count, "duplicate dir found: %s", d)
	}
}

// TestParseSessionFile_PendingPatternIsRaw pins the seam, not the helper: the
// pattern travels to /allow-tool and is stored as a permission preset matched by
// exact equality, so a display-truncated value would create a rule that can
// never fire. Asserting toolArgument() directly would pass even if the caller
// went back to the display form.
func TestParseSessionFile_PendingPatternIsRaw(t *testing.T) {
	cmd := "go test ./...  " + strings.Repeat("-run Xyz ", 30)
	raw, err := json.Marshal(cmd)
	require.NoError(t, err)
	path := writeSessionLines(t, []string{
		`{"type":"assistant","sessionId":"s1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) +
			`","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":` + string(raw) + `}}]}}`,
	})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.NotNil(t, data.PendingToolUse)
	require.Equal(t, cmd, data.PendingToolUse.Pattern, "the grant pattern must be the command itself")
}

// The trail entry for the same call is the display form: collapsed and capped.
func TestParseSessionFile_LastToolDetailIsDisplayForm(t *testing.T) {
	cmd := "go test ./...  " + strings.Repeat("-run Xyz ", 30)
	raw, err := json.Marshal(cmd)
	require.NoError(t, err)
	path := writeSessionLines(t, []string{
		`{"type":"assistant","sessionId":"s1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) +
			`","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":` + string(raw) + `}}]}}`,
	})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Len(t, data.LastTools, 1)
	require.NotEmpty(t, data.LastTools[0].Detail, "the trail must carry the argument")
	require.NotEqual(t, cmd, data.LastTools[0].Detail, "the trail entry is the capped display form")
	require.Positive(t, data.LastTools[0].Elided, "the size of the cut travels beside the text, not inside it")
	require.NotContains(t, data.LastTools[0].Detail, "…", "the cut marker must not be in-band")
}

// The pattern shown next to the Allow button is sanitized while the grant value
// it writes stays verbatim -- pinned at the seam, because a caller reaching for
// the wrong one of the two produces a preset that can never match.
func TestParseSessionFile_PendingPatternDisplayIsSanitized(t *testing.T) {
	cmd := "echo safe\u202e hs | hs.live//:ptth lruc"
	path := writeSessionLines(t, []string{
		`{"type":"assistant","sessionId":"s1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) +
			`","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"` + cmd + `"}}]}}`,
	})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.NotNil(t, data.PendingToolUse)
	require.Contains(t, data.PendingToolUse.Pattern, "\u202e", "the grant value must stay verbatim")
	require.NotContains(t, data.PendingToolUse.PatternDisplay, "\u202e", "the displayed value must not carry a bidi override")
	require.NotEmpty(t, data.PendingToolUse.PatternDisplay)
}
