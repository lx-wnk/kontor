package llmadapter

import (
	"context"
	"fmt"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/acp"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// NewLLMSpawnerFromSpawner constructs the LLMSpawner adapter that corresponds
// to the spawner row's AdapterType. Returns nil with no error when the row's
// AdapterType is "claude" (or empty) — that case is handled by the native
// subprocess SpawnStageAgent path, not via the LLMSpawner abstraction.
//
// AdapterConfig keys are the same plain keys exposed by AvailableAdapters
// (e.g. "host", "default_model", "base_url", "api_key_env") — NOT the legacy
// dotted "adapters.ollama.*" form.
func NewLLMSpawnerFromSpawner(s *ent.Spawner, permission func(context.Context, acp.PermissionRequest) (acp.PermissionDecision, error)) (LLMSpawner, error) {
	if s == nil {
		return nil, nil // legacy: caller falls back to native claude path
	}
	switch s.AdapterType {
	case "", "claude":
		return nil, nil
	case "ollama":
		return &OllamaSpawner{
			Host:         s.AdapterConfig["host"],
			DefaultModel: s.AdapterConfig["default_model"],
		}, nil
	case "openai":
		return &OpenAISpawner{
			BaseURL:      s.AdapterConfig["base_url"],
			APIKeyEnv:    s.AdapterConfig["api_key_env"],
			DefaultModel: s.AdapterConfig["default_model"],
		}, nil
	case "anthropic":
		// Native Anthropic Messages API via the out-of-process anthropic-spawner
		// binary (keeps anthropic-sdk-go out of the server module). Reuses the
		// custom-exec contract; the binary handles model/auth/streaming.
		path, err := resolveAnthropicSpawnerPath()
		if err != nil {
			return nil, err
		}
		return &CustomCommandSpawner{Command: path}, nil
	case "custom":
		// Custom adapter reuses the row's top-level `command` column. The
		// CustomCommandSpawner exec contract (stdin=LLMSpawnArgs JSON,
		// stdout=LLMSpawnResult JSON) is unchanged.
		if s.Command == "" {
			return nil, fmt.Errorf("custom adapter requires spawner.command to be set")
		}
		return &CustomCommandSpawner{Command: s.Command}, nil
	case "acp":
		// The ACP adapter owns the agent process for the whole stage, because
		// an ACP agent blocks on permission requests until the client answers.
		a := &ACPSpawner{Command: s.AdapterConfig["command"], Permission: permission}
		if a.Command == "" {
			a.Command = "npx"
			a.Args = []string{"-y", "@agentclientprotocol/claude-agent-acp@0.68.0"}
		} else if raw := s.AdapterConfig["args"]; raw != "" {
			a.Args = strings.Fields(raw)
		}
		return a, nil
	default:
		return nil, fmt.Errorf("unknown adapter_type: %q", s.AdapterType)
	}
}
