package parser_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

// askUserQuestionEntry builds a JSONL assistant entry with an AskUserQuestion tool_use block.
func askUserQuestionEntry(t *testing.T, toolUseID string) string {
	t.Helper()
	input := map[string]any{
		"questions": []map[string]any{
			{
				"question":    "Which deployment strategy?",
				"header":      "Choose strategy",
				"multiSelect": false,
				"options": []map[string]any{
					{"label": "Blue/Green", "description": "Zero-downtime swap", "preview": "recommended"},
					{"label": "Rolling", "description": "Gradual rollout"},
				},
			},
		},
	}
	inputBytes, err := json.Marshal(input)
	require.NoError(t, err)
	return buildAssistantEntry(t, "claude-opus-4", 1, 1, []map[string]any{
		{
			"type":  "tool_use",
			"id":    toolUseID,
			"name":  "AskUserQuestion",
			"input": json.RawMessage(inputBytes),
		},
	})
}

// TestParseSessionFile_AskUserQuestion_FallsBackToPendingToolUse verifies an
// unresolved AskUserQuestion tool_use gets no special-casing: it surfaces as a
// normal PendingToolUse (the live terminal overlay, not the JSONL parser, is
// responsible for presenting the question) and still forces TurnOpen.
func TestParseSessionFile_AskUserQuestion_FallsBackToPendingToolUse(t *testing.T) {
	const id = "toolu_ask_01"
	path := writeSessionLines(t, []string{askUserQuestionEntry(t, id)})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)

	require.NotNil(t, data.PendingToolUse, "AskUserQuestion must fall back to normal PendingToolUse handling")
	require.Equal(t, id, data.PendingToolUse.ID)
	require.Equal(t, "AskUserQuestion", data.PendingToolUse.Tool)
	require.True(t, data.TurnOpen, "TurnOpen must be true while the tool_use awaits a result")
}

// TestParseSessionFile_AskUserQuestion_Resolved verifies that a matching
// tool_result clears PendingToolUse like any other resolved tool_use.
func TestParseSessionFile_AskUserQuestion_Resolved(t *testing.T) {
	const id = "toolu_ask_01"
	path := writeSessionLines(t, []string{
		askUserQuestionEntry(t, id),
		buildUserToolResult(t, id),
	})

	data, err := parser.ParseSessionFile(path)
	require.NoError(t, err)

	require.Nil(t, data.PendingToolUse)
}
