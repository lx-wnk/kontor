package parser_test

import (
	"encoding/json"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/require"
)

// buildUserToolResult builds a JSONL user entry carrying a tool_result that
// resolves the given tool_use id.
func buildUserToolResult(t *testing.T, toolUseID string) string {
	t.Helper()
	msg := map[string]any{
		"role":    "user",
		"content": []map[string]any{{"type": "tool_result", "tool_use_id": toolUseID}},
	}
	mb, err := json.Marshal(msg)
	require.NoError(t, err)
	entry := map[string]any{
		"type":      "user",
		"timestamp": "2025-01-15T10:31:00.000Z",
		"message":   json.RawMessage(mb),
	}
	b, err := json.Marshal(entry)
	require.NoError(t, err)
	return string(b)
}

func bashToolUseEntry(t *testing.T) string {
	t.Helper()
	return buildAssistantEntry(t, "claude-opus-4", 1, 1, []map[string]any{
		{"type": "tool_use", "id": "tu_1", "name": "Bash", "input": json.RawMessage(`{"command":"npm publish"}`)},
	})
}

// A trailing tool_use with no matching tool_result marks the agent as pending on that tool.
func TestParseSessionFile_PendingToolUse_Set(t *testing.T) {
	path := writeSessionLines(t, []string{bashToolUseEntry(t)})
	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.NotNil(t, data.PendingToolUse)
	require.Equal(t, "Bash", data.PendingToolUse.Tool)
	require.Equal(t, "npm publish", data.PendingToolUse.Pattern)
	require.Equal(t, "tu_1", data.PendingToolUse.ID)
}

// Once the tool_result arrives, there is no pending tool use.
func TestParseSessionFile_PendingToolUse_ResolvedIsNil(t *testing.T) {
	path := writeSessionLines(t, []string{bashToolUseEntry(t), buildUserToolResult(t, "tu_1")})
	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)
	require.Nil(t, data.PendingToolUse)
}
