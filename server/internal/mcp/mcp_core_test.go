package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ResolveScopes
// ---------------------------------------------------------------------------

func TestResolveScopes_TasksRead_ImpliesNothing(t *testing.T) {
	scopes := ResolveScopes([]string{"tasks:read"})
	assert.True(t, scopes["tasks:read"])
	assert.False(t, scopes["tasks:write"])
	assert.False(t, scopes["pipeline:control"])
	assert.False(t, scopes["keys:manage"])
}

func TestResolveScopes_TasksWrite_ImpliesRead(t *testing.T) {
	scopes := ResolveScopes([]string{"tasks:write"})
	assert.True(t, scopes["tasks:write"])
	assert.True(t, scopes["tasks:read"])
	assert.False(t, scopes["pipeline:control"])
	assert.False(t, scopes["keys:manage"])
}

func TestResolveScopes_KeysManage_ImpliesAll(t *testing.T) {
	scopes := ResolveScopes([]string{"keys:manage"})
	assert.True(t, scopes["keys:manage"])
	assert.True(t, scopes["tasks:read"])
	assert.True(t, scopes["tasks:write"])
	assert.True(t, scopes["pipeline:control"])
}

func TestAgentCoordScope(t *testing.T) {
	for _, name := range []string{"write_scratchpad", "read_scratchpad", "list_scratchpad", "acquire_lock", "release_lock"} {
		if ToolScopeMap[name] != "agent:coord" {
			t.Fatalf("%s scope = %q want agent:coord", name, ToolScopeMap[name])
		}
	}
	if !ResolveScopes([]string{"pipeline:control"})["agent:coord"] {
		t.Fatalf("pipeline:control must imply agent:coord")
	}
	if !ResolveScopes([]string{"keys:manage"})["agent:coord"] {
		t.Fatalf("keys:manage must imply agent:coord")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func doRPC(t *testing.T, handler http.Handler, body any) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func doRPCWithAuth(t *testing.T, handler http.Handler, body any, info *MCPAuthInfo) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	// inject auth directly via unexported key (same package)
	req = req.WithContext(context.WithValue(req.Context(), mcpAuthKey{}, info))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

// ---------------------------------------------------------------------------
// MCPHandler — initialize
// ---------------------------------------------------------------------------

func TestMCPHandler_Initialize(t *testing.T) {
	registry := make(ToolRegistry)
	handler := MCPHandler(registry, nil, nil)

	resp := doRPC(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{},
	})

	assert.Equal(t, "2.0", resp["jsonrpc"])
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "result should be a map")
	assert.Equal(t, protocolVersion, result["protocolVersion"])
	serverInfo, ok := result["serverInfo"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, ServerName, serverInfo["name"])
}

// ---------------------------------------------------------------------------
// MCPHandler — tools/list
// ---------------------------------------------------------------------------

func TestMCPHandler_ToolsList_ReturnsRegisteredTools(t *testing.T) {
	registry := make(ToolRegistry)
	registry.Register(&ToolDef{
		Name:        "list_tasks",
		Description: "List all tasks",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (*ToolResult, error) {
			return OK([]string{})
		},
	})

	handler := MCPHandler(registry, nil, nil)
	resp := doRPC(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})

	result, ok := resp["result"].(map[string]any)
	require.True(t, ok)
	tools, ok := result["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "list_tasks", tool["name"])
}

// ---------------------------------------------------------------------------
// MCPHandler — tools/call — no auth → scope error
// ---------------------------------------------------------------------------

func TestMCPHandler_ToolsCall_NoAuth_ReturnsScopeError(t *testing.T) {
	registry := make(ToolRegistry)
	registry.Register(&ToolDef{
		Name:        "list_tasks",
		Description: "List tasks",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (*ToolResult, error) {
			return OK("should not reach")
		},
	})

	handler := MCPHandler(registry, nil, nil)
	resp := doRPC(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_tasks",
			"arguments": map[string]any{},
		},
	})

	rpcErr, ok := resp["error"].(map[string]any)
	require.True(t, ok, "expected error field")
	assert.Equal(t, float64(-32003), rpcErr["code"])
	assert.True(t, strings.Contains(rpcErr["message"].(string), "scope"), "error message should mention scope")
}

// ---------------------------------------------------------------------------
// MCPHandler — tools/call — correct scope → handler called
// ---------------------------------------------------------------------------

func TestMCPHandler_ToolsCall_WithScope_CallsHandler(t *testing.T) {
	registry := make(ToolRegistry)
	called := false
	registry.Register(&ToolDef{
		Name:        "list_tasks",
		Description: "List tasks",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args map[string]any) (*ToolResult, error) {
			called = true
			return OK([]string{"task-1"})
		},
	})

	handler := MCPHandler(registry, nil, nil)
	auth := &MCPAuthInfo{
		KeyID:  "test-key",
		Scopes: ResolveScopes([]string{"tasks:read"}),
	}
	resp := doRPCWithAuth(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "list_tasks",
			"arguments": map[string]any{},
		},
	}, auth)

	assert.True(t, called, "handler should have been called")
	result, ok := resp["result"].(map[string]any)
	require.True(t, ok, "expected result field, got: %v", resp)
	_ = result
}

// ---------------------------------------------------------------------------
// MCPHandler — unknown method → -32601
// ---------------------------------------------------------------------------

func TestMCPHandler_UnknownMethod_Returns32601(t *testing.T) {
	registry := make(ToolRegistry)
	handler := MCPHandler(registry, nil, nil)

	resp := doRPC(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "unknown/method",
		"params":  map[string]any{},
	})

	rpcErr, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32601), rpcErr["code"])
}

// ---------------------------------------------------------------------------
// MCPHandler — unknown tool → -32601
// ---------------------------------------------------------------------------

func TestMCPHandler_UnknownTool_Returns32601(t *testing.T) {
	registry := make(ToolRegistry)
	handler := MCPHandler(registry, nil, nil)

	auth := &MCPAuthInfo{
		KeyID:  "test-key",
		Scopes: ResolveScopes([]string{"keys:manage"}),
	}
	resp := doRPCWithAuth(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      6,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "nonexistent_tool",
			"arguments": map[string]any{},
		},
	}, auth)

	rpcErr, ok := resp["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(-32601), rpcErr["code"])
}
