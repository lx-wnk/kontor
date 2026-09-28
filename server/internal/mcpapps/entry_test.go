package mcpapps_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestParseEntry_Stdio(t *testing.T) {
	e, err := mcpapps.ParseEntry(json.RawMessage(`{"command":"npx","args":["-y","imap-mcp-server"]}`))
	require.NoError(t, err)
	require.True(t, e.IsStdio())
	require.Equal(t, []string{"-y", "imap-mcp-server"}, e.Args)

	e, err = mcpapps.ParseEntry(json.RawMessage(`{"type":"http","url":"http://127.0.0.1:1/mcp"}`))
	require.NoError(t, err)
	require.False(t, e.IsStdio())
}

func TestWithEnv_MergesAndPreservesUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"command":"npx","env":{"KEEP":"1","OVERRIDE":"old"},"timeout":30}`)
	out, err := mcpapps.WithEnv(raw, map[string]string{"OVERRIDE": "new", "ADDED": "2"})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, float64(30), got["timeout"], "fields the CLI adds must survive")
	require.Equal(t, map[string]any{"KEEP": "1", "OVERRIDE": "new", "ADDED": "2"}, got["env"])
}

func TestWithEnv_NoEnvReturnsInputUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"command":"npx"}`)
	out, err := mcpapps.WithEnv(raw, nil)
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(out))
}
