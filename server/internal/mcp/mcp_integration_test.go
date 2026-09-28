package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPEndpoint_Initialize(t *testing.T) {
	h := mcp.MCPHandler(mcp.ToolRegistry{})
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	result := resp["result"].(map[string]any)
	require.Equal(t, "2024-11-05", result["protocolVersion"])
	info := result["serverInfo"].(map[string]any)
	require.Equal(t, mcp.ServerName, info["name"])
}

func TestMCPEndpoint_ToolsList_SortedAlphabetically(t *testing.T) {
	noop := func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
		return mcp.OK(nil)
	}
	registry := mcp.ToolRegistry{}
	// Use real scope-map names that sort correctly: "list_tasks" < "update_task"
	registry.Register(&mcp.ToolDef{Name: "update_task", Description: "Z", InputSchema: map[string]any{"type": "object"}, Handler: noop})
	registry.Register(&mcp.ToolDef{Name: "list_tasks", Description: "A", InputSchema: map[string]any{"type": "object"}, Handler: noop})
	h := mcp.MCPHandler(registry)
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	toolsList := resp["result"].(map[string]any)["tools"].([]any)
	require.Equal(t, "list_tasks", toolsList[0].(map[string]any)["name"])
	require.Equal(t, "update_task", toolsList[1].(map[string]any)["name"])
}

func TestMCPEndpoint_ToolsCall_MissingScope(t *testing.T) {
	registry := mcp.ToolRegistry{}
	registry.Register(&mcp.ToolDef{
		Name:        "list_tasks",
		Description: "test",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			return mcp.OK([]string{})
		},
	})
	h := mcp.MCPHandler(registry)
	// No auth in context — scope check fails with -32003
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_tasks","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	rpcErr := resp["error"].(map[string]any)
	require.EqualValues(t, -32003, rpcErr["code"])
}
