package parser_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_TurnOpen(t *testing.T) {
	dir := t.TempDir()
	closed := filepath.Join(dir, "closed.jsonl")
	require.NoError(t, os.WriteFile(closed, []byte(
		`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`+"\n"), 0o644))
	d, err := parser.ParseSessionFile(closed)
	require.NoError(t, err)
	assert.False(t, d.TurnOpen, "completed assistant turn → TurnOpen false")

	open := filepath.Join(dir, "open.jsonl")
	require.NoError(t, os.WriteFile(open, []byte(
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"next task"}}`+"\n"), 0o644))
	d2, err := parser.ParseSessionFile(open)
	require.NoError(t, err)
	assert.True(t, d2.TurnOpen, "trailing user message → TurnOpen true")
}

func TestParse_TurnOpenExtended(t *testing.T) {
	const (
		userPrompt = `{"type":"user","message":{"role":"user","content":"hi"}}`
		bgToolUse  = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"bg1","name":"Agent","input":{"prompt":"do stuff","run_in_background":true}}],"stop_reason":"end_turn"}}`
	)

	tests := []struct {
		name               string
		lines              []string
		wantTurnOpen       bool
		wantPendingBgTasks bool
	}{
		{
			name: "end_turn closes the turn",
			lines: []string{
				userPrompt,
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}`,
			},
			wantTurnOpen: false,
		},
		{
			name: "tool_use stop_reason keeps the turn open",
			lines: []string{
				userPrompt,
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"/tmp/x"}}],"stop_reason":"tool_use"}}`,
			},
			wantTurnOpen: true,
		},
		{
			name: "unresolved run_in_background tool_use is pending background",
			lines: []string{
				userPrompt,
				bgToolUse,
			},
			// The unresolved tool_use is also the pending tool use, which opens the turn.
			wantTurnOpen:       true,
			wantPendingBgTasks: true,
		},
		{
			name: "run_in_background tool_use with matching tool_result is resolved",
			lines: []string{
				userPrompt,
				bgToolUse,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"bg1","content":"done"}]}}`,
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"bg done"}],"stop_reason":"end_turn"}}`,
			},
			wantTurnOpen:       false,
			wantPendingBgTasks: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(strings.Join(tc.lines, "\n")+"\n"), 0o644))

			d, err := parser.ParseSessionFile(path)
			require.NoError(t, err)

			assert.Equal(t, tc.wantTurnOpen, d.TurnOpen, "TurnOpen")
			assert.Equal(t, tc.wantPendingBgTasks, d.HasPendingBackground, "HasPendingBackground")
		})
	}
}

func TestParse_TurnOpenByStopReason(t *testing.T) {
	const assistantTextFmt = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}],"stop_reason":%s}}`

	tests := []struct {
		name         string
		stopReasonJS string
		wantTurnOpen bool
	}{
		{"tool_use", `"tool_use"`, true},
		{"pause_turn", `"pause_turn"`, true},
		{"end_turn", `"end_turn"`, false},
		{"stop_sequence", `"stop_sequence"`, false},
		{"refusal", `"refusal"`, false},
		{"model_context_window_exceeded", `"model_context_window_exceeded"`, false},
		{"max_tokens", `"max_tokens"`, false},
		{"empty string", `""`, false},
		{"null", `null`, false},
		{"future_reason", `"future_reason"`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := fmt.Sprintf(assistantTextFmt, tc.stopReasonJS)
			path := filepath.Join(t.TempDir(), "session.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(line+"\n"), 0o644))

			d, err := parser.ParseSessionFile(path)
			require.NoError(t, err)

			assert.Equal(t, tc.wantTurnOpen, d.TurnOpen, "TurnOpen")
		})
	}
}

func TestTailRead_ReturnsContent(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "session*.jsonl")
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"message","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[]}}` + "\n")
	require.NoError(t, err)
	f.Close()

	content, err := parser.TailRead(f.Name())
	require.NoError(t, err)
	require.Contains(t, content, "claude-sonnet-4-6")
}

func TestTailRead_MissingFile(t *testing.T) {
	_, err := parser.TailRead(filepath.Join(t.TempDir(), "missing.jsonl"))
	require.Error(t, err)
}

func TestTailRead_LargeFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "large*.jsonl")
	require.NoError(t, err)
	// Write more than 32KB
	line := `{"type":"message","message":{"role":"assistant","model":"claude-sonnet-4-6","content":[]}}` + "\n"
	for i := 0; i < 500; i++ {
		_, err = f.WriteString(line)
		require.NoError(t, err)
	}
	f.Close()

	content, err := parser.TailRead(f.Name())
	require.NoError(t, err)
	// Should return at most tailBytes (32768) bytes
	require.LessOrEqual(t, len(content), 32768)
	require.Contains(t, content, "claude-sonnet-4-6")
}

func TestTailRead_WithAssistantTurn(t *testing.T) {
	type usageBlock struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	type msgBody struct {
		Role    string        `json:"role"`
		Model   string        `json:"model"`
		Content []interface{} `json:"content"`
		Usage   usageBlock    `json:"usage"`
	}
	type entry struct {
		Type    string  `json:"type"`
		Message msgBody `json:"message"`
	}
	e := entry{
		Type: "message",
		Message: msgBody{
			Role:    "assistant",
			Model:   "claude-sonnet-4-6",
			Content: []interface{}{},
			Usage:   usageBlock{InputTokens: 100, OutputTokens: 50},
		},
	}
	b, err := json.Marshal(e)
	require.NoError(t, err)

	f, err := os.CreateTemp(t.TempDir(), "session*.jsonl")
	require.NoError(t, err)
	_, err = f.WriteString(string(b) + "\n")
	require.NoError(t, err)
	f.Close()

	content, err := parser.TailRead(f.Name())
	require.NoError(t, err)
	require.Contains(t, content, `"role":"assistant"`)
	require.Contains(t, content, `"input_tokens":100`)
}
