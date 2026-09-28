package llmadapter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/llmadapter"
)

// parseAndExtractAssistantText mimics what session_reader.go does:
// it parses JSONL lines and returns the last assistant text block content.
// Used to verify that writeSyntheticSession writes the correct format.
func parseAndExtractAssistantText(jsonlContent string) string {
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type entry struct {
		Type    string   `json:"type"`
		Message *message `json:"message"`
	}
	var last string
	for _, line := range strings.Split(jsonlContent, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e entry
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.Type != "assistant" || e.Message == nil {
			continue
		}
		var parts []contentBlock
		if err := json.Unmarshal(e.Message.Content, &parts); err != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				last = p.Text
			}
		}
	}
	return last
}

func TestOllamaSpawner_Spawn_WritesSessionFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/chat", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": "```json\n{\"summary\":\"ok\"}\n```"},
		})
	}))
	defer srv.Close()

	spawner := &llmadapter.OllamaSpawner{Host: srv.URL, DefaultModel: "llama3"}
	result, err := spawner.Spawn(context.Background(), llmadapter.LLMSpawnArgs{
		TaskID:       "t1",
		StageRunID:   "sr1",
		UserPrompt:   "do the thing",
		SystemPrompt: "you are helpful",
		WorkDir:      t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.PID)
	assert.NotEmpty(t, result.SessionFile)

	// Session file must exist and contain valid JSONL.
	data, err := os.ReadFile(result.SessionFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "assistant")

	// Sub-test: the file must be parseable by the actual session reader logic
	// and must surface the LLM content as assistant text.
	t.Run("parser_round_trip", func(t *testing.T) {
		text := parseAndExtractAssistantText(string(data))
		require.NotEmpty(t, text, "no assistant text found — JSONL format mismatch (check writeSyntheticSession)")
		assert.Contains(t, text, `"summary":"ok"`)
	})

	// Cleanup
	_ = os.Remove(result.SessionFile)
}

func TestOpenAISpawner_Spawn_WritesSessionFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"content": "```json\n{\"result\":\"done\"}\n```"}},
			},
		})
	}))
	defer srv.Close()

	spawner := &llmadapter.OpenAISpawner{BaseURL: srv.URL, DefaultModel: "gpt-4o"}
	result, err := spawner.Spawn(context.Background(), llmadapter.LLMSpawnArgs{
		TaskID:     "t2",
		StageRunID: "sr2",
		UserPrompt: "do a thing",
		WorkDir:    t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, result.PID)
	assert.NotEmpty(t, result.SessionFile)

	data, err := os.ReadFile(result.SessionFile)
	require.NoError(t, err)
	assert.Contains(t, string(data), "assistant")

	// Sub-test: the file must be parseable by the actual session reader logic.
	t.Run("parser_round_trip", func(t *testing.T) {
		text := parseAndExtractAssistantText(string(data))
		require.NotEmpty(t, text, "no assistant text found — JSONL format mismatch (check writeSyntheticSession)")
		assert.Contains(t, text, `"result":"done"`)
	})

	_ = os.Remove(result.SessionFile)
}

func TestOllamaSpawner_Name(t *testing.T) {
	s := &llmadapter.OllamaSpawner{}
	assert.Equal(t, "ollama", s.Name())
}

func TestOpenAISpawner_Name(t *testing.T) {
	s := &llmadapter.OpenAISpawner{}
	assert.Equal(t, "openai", s.Name())
}

func TestCustomCommandSpawner_Name(t *testing.T) {
	s := &llmadapter.CustomCommandSpawner{Command: "/bin/echo"}
	assert.Equal(t, "custom", s.Name())
}
