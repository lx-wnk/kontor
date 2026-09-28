package llmadapter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/llmadapter"
)

func TestNewLLMSpawnerFromSpawner_Nil(t *testing.T) {
	got, err := llmadapter.NewLLMSpawnerFromSpawner(nil, nil)
	require.NoError(t, err)
	assert.Nil(t, got, "nil spawner row must yield nil adapter (caller falls back to native path)")
}

func TestNewLLMSpawnerFromSpawner_ClaudeAndEmpty(t *testing.T) {
	for _, adapterType := range []string{"", "claude"} {
		t.Run("adapter_type="+adapterType, func(t *testing.T) {
			row := &ent.Spawner{AdapterType: adapterType}
			got, err := llmadapter.NewLLMSpawnerFromSpawner(row, nil)
			require.NoError(t, err)
			assert.Nil(t, got, "claude/empty adapter_type must yield nil adapter")
		})
	}
}

func TestNewLLMSpawnerFromSpawner_Ollama(t *testing.T) {
	row := &ent.Spawner{
		AdapterType: "ollama",
		AdapterConfig: map[string]string{
			"host":          "http://10.0.0.5:11434",
			"default_model": "llama3:8b",
		},
	}
	got, err := llmadapter.NewLLMSpawnerFromSpawner(row, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	o, ok := got.(*llmadapter.OllamaSpawner)
	require.True(t, ok, "expected *OllamaSpawner, got %T", got)
	assert.Equal(t, "http://10.0.0.5:11434", o.Host)
	assert.Equal(t, "llama3:8b", o.DefaultModel)
	assert.Equal(t, "ollama", got.Name())
}

func TestNewLLMSpawnerFromSpawner_OpenAI(t *testing.T) {
	row := &ent.Spawner{
		AdapterType: "openai",
		AdapterConfig: map[string]string{
			"base_url":      "https://api.openai.com/v1",
			"api_key_env":   "OPENAI_API_KEY",
			"default_model": "gpt-4o-mini",
		},
	}
	got, err := llmadapter.NewLLMSpawnerFromSpawner(row, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	o, ok := got.(*llmadapter.OpenAISpawner)
	require.True(t, ok, "expected *OpenAISpawner, got %T", got)
	assert.Equal(t, "https://api.openai.com/v1", o.BaseURL)
	assert.Equal(t, "OPENAI_API_KEY", o.APIKeyEnv)
	assert.Equal(t, "gpt-4o-mini", o.DefaultModel)
}

func TestNewLLMSpawnerFromSpawner_CustomWithCommand(t *testing.T) {
	row := &ent.Spawner{
		AdapterType: "custom",
		Command:     "/usr/local/bin/my-llm-shim",
	}
	got, err := llmadapter.NewLLMSpawnerFromSpawner(row, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	c, ok := got.(*llmadapter.CustomCommandSpawner)
	require.True(t, ok, "expected *CustomCommandSpawner, got %T", got)
	assert.Equal(t, "/usr/local/bin/my-llm-shim", c.Command)
}

func TestNewLLMSpawnerFromSpawner_CustomMissingCommand(t *testing.T) {
	row := &ent.Spawner{
		AdapterType: "custom",
		Command:     "",
	}
	got, err := llmadapter.NewLLMSpawnerFromSpawner(row, nil)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "custom adapter requires spawner.command")
}

func TestNewLLMSpawnerFromSpawner_UnknownType(t *testing.T) {
	row := &ent.Spawner{AdapterType: "fancy-new-llm"}
	got, err := llmadapter.NewLLMSpawnerFromSpawner(row, nil)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "unknown adapter_type")
}

func TestFactoryBuildsTheACPAdapter(t *testing.T) {
	s, err := llmadapter.NewLLMSpawnerFromSpawner(&ent.Spawner{
		AdapterType:   "acp",
		AdapterConfig: map[string]string{"command": "npx", "args": "-y @agentclientprotocol/claude-agent-acp@latest"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
	require.Equal(t, "acp", s.Name())

	a, ok := s.(*llmadapter.ACPSpawner)
	require.True(t, ok)
	require.Equal(t, "npx", a.Command)
	require.Equal(t, []string{"-y", "@agentclientprotocol/claude-agent-acp@latest"}, a.Args)
}

func TestFactoryDefaultsTheACPCommand(t *testing.T) {
	s, err := llmadapter.NewLLMSpawnerFromSpawner(&ent.Spawner{AdapterType: "acp"}, nil)
	require.NoError(t, err)

	a, ok := s.(*llmadapter.ACPSpawner)
	require.True(t, ok)
	require.NotEmpty(t, a.Command, "an unconfigured acp row must still be runnable")
}

func TestCatalogListsTheACPAdapter(t *testing.T) {
	var found *llmadapter.AdapterMeta
	for i := range llmadapter.AvailableAdapters {
		if llmadapter.AvailableAdapters[i].Name == "acp" {
			found = &llmadapter.AvailableAdapters[i]
		}
	}
	require.NotNil(t, found, "the settings UI reads this catalog")
	require.NotEmpty(t, found.Description)
}

// TestCatalogClaudeAdapterDeclaresEffortKey guards the save path in
// SpawnerSettings.vue's buildAdapterConfig(), which only sends a key that
// appears in this catalog's configKeys — removing this entry would break
// saving effort in production while every Go test outside this one and the
// Vue test's own mocked catalog stayed green.
func TestCatalogClaudeAdapterDeclaresEffortKey(t *testing.T) {
	var found *llmadapter.AdapterMeta
	for i := range llmadapter.AvailableAdapters {
		if llmadapter.AvailableAdapters[i].Name == "claude" {
			found = &llmadapter.AvailableAdapters[i]
		}
	}
	require.NotNil(t, found, "the settings UI reads this catalog")

	hasEffort := false
	for _, k := range found.ConfigKeys {
		if k.Key == "effort" {
			hasEffort = true
		}
	}
	require.True(t, hasEffort, "claude adapter must declare the effort config key")
}
