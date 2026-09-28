package llmadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/ollamaclient"
)

// OllamaSpawner calls the Ollama HTTP API synchronously and writes a
// synthetic JSONL session file so the completion detector can parse the output.
type OllamaSpawner struct {
	Host         string // e.g. "http://localhost:11434"
	DefaultModel string
	clientOnce   sync.Once
	client       *ollamaclient.Client
}

func (o *OllamaSpawner) Name() string { return "ollama" }

func (o *OllamaSpawner) ollamaClient() *ollamaclient.Client {
	o.clientOnce.Do(func() { o.client = ollamaclient.New(o.Host, 5*time.Minute) })
	return o.client
}

func (o *OllamaSpawner) chatRequest(args LLMSpawnArgs) ollamaclient.ChatRequest {
	model := args.Model
	if model == "" {
		model = o.DefaultModel
	}
	return ollamaclient.ChatRequest{
		Model: model,
		Messages: []ollamaclient.ChatMessage{
			{Role: "system", Content: args.SystemPrompt},
			{Role: "user", Content: args.UserPrompt},
		},
	}
}

func (o *OllamaSpawner) Spawn(ctx context.Context, args LLMSpawnArgs) (LLMSpawnResult, error) {
	result, err := o.ollamaClient().Chat(ctx, o.chatRequest(args))
	if err != nil {
		return LLMSpawnResult{}, fmt.Errorf("OllamaSpawner: %w", err)
	}

	sessionFile, err := writeSyntheticSession(args.WorkDir, args.StageRunID, result.Message.Content)
	if err != nil {
		return LLMSpawnResult{}, err
	}
	return LLMSpawnResult{PID: 0, SessionID: "ollama-" + args.StageRunID, SessionFile: sessionFile}, nil
}

// SpawnStream calls Ollama's /api/chat with stream:true and emits message.content
// chunks on the returned channel. The channel is closed when the stream ends or
// the context is cancelled.
func (o *OllamaSpawner) SpawnStream(ctx context.Context, args LLMSpawnArgs) (<-chan string, error) {
	body, err := o.ollamaClient().ChatStream(ctx, o.chatRequest(args))
	if err != nil {
		return nil, fmt.Errorf("OllamaSpawner.SpawnStream: %w", err)
	}

	ch := make(chan string, 16)
	go func() {
		defer close(ch)
		defer body.Close()
		dec := json.NewDecoder(body)
		for dec.More() {
			var chunk ollamaclient.ChatResponse
			if err := dec.Decode(&chunk); err != nil {
				ch <- "[ERROR] OllamaSpawner.SpawnStream: decode: " + err.Error()
				return
			}
			if chunk.Done {
				return
			}
			if chunk.Message.Content == "" {
				continue
			}
			select {
			case ch <- chunk.Message.Content:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// writeSyntheticSession writes a single-line JSONL file in the format the
// completion detector expects: one assistant message whose text contains the
// ```json ... ``` block produced by the LLM.
//
// The parser (session_reader.go / parseJsonlLines) expects each line to match
// JsonlEntry: {"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
func writeSyntheticSession(workDir, stageRunID, content string) (string, error) {
	dir := filepath.Join(os.TempDir(), "dashboard-synthetic-sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("writeSyntheticSession: mkdir: %w", err)
	}
	sessionID := "synthetic-" + stageRunID
	path := filepath.Join(dir, sessionID+".jsonl")

	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role      string         `json:"role"`
		Content   []contentBlock `json:"content"`
		Timestamp string         `json:"timestamp"`
	}
	type line struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
	}
	entry := line{
		Type: "assistant",
		Message: message{
			Role: "assistant",
			Content: []contentBlock{
				{Type: "text", Text: content},
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("writeSyntheticSession: write: %w", err)
	}
	return path, nil
}
