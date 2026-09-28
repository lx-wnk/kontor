package llmadapter_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/llmadapter"
)

// Compile-time guard: ensure the streaming interface is exported and shaped
// correctly. The test body is empty — the assertion is the var declaration.
func TestStreamingLLMSpawner_InterfaceShape(t *testing.T) {
	var _ llmadapter.StreamingLLMSpawner = (*fakeStreaming)(nil)
	_ = t // silence unused
}

type fakeStreaming struct{}

func (fakeStreaming) Name() string { return "fake" }
func (fakeStreaming) Spawn(_ context.Context, _ llmadapter.LLMSpawnArgs) (llmadapter.LLMSpawnResult, error) {
	return llmadapter.LLMSpawnResult{}, nil
}
func (fakeStreaming) SpawnStream(_ context.Context, _ llmadapter.LLMSpawnArgs) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}
