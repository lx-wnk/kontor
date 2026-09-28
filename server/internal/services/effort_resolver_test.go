package services_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/services"
)

func TestResolveEffortReadsAdapterConfig(t *testing.T) {
	f := setupResolver(t)

	sp, err := f.spawners.Create(t.Context(), "Claude High", "claude-high", "claude", nil, nil, nil, nil, "claude", map[string]string{"effort": "high"}, false)
	require.NoError(t, err)
	taskID := createTaskWith(t, f, "with-effort", nil, &sp.ID)

	effort, src, supported, err := services.ResolveEffort(t.Context(), f.resolver, taskID, "")
	require.NoError(t, err)
	require.Equal(t, "high", effort)
	require.Equal(t, services.SpawnerSourceTask, src)
	require.True(t, supported)
}

func TestResolveEffortUnsupportedAdapterIsVisible(t *testing.T) {
	f := setupResolver(t)

	sp, err := f.spawners.Create(t.Context(), "Ollama", "ollama-1", "ollama-cmd", nil, nil, nil, nil, "ollama", map[string]string{"effort": "high"}, false)
	require.NoError(t, err)
	taskID := createTaskWith(t, f, "ollama-task", nil, &sp.ID)

	_, _, supported, err := services.ResolveEffort(t.Context(), f.resolver, taskID, "")
	require.NoError(t, err)
	require.False(t, supported)
}

func TestResolveEffortAbsentIsNotAnError(t *testing.T) {
	f := setupResolver(t)

	taskID := createTaskWith(t, f, "bare-effort", nil, nil)

	effort, src, supported, err := services.ResolveEffort(t.Context(), f.resolver, taskID, "")
	require.NoError(t, err)
	require.Empty(t, effort)
	require.Equal(t, services.SpawnerSourceDefault, src)
	require.True(t, supported)
}
