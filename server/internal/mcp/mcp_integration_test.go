package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPEndpoint_Initialize(t *testing.T) {
	h := mcp.MCPHandler(mcp.ToolRegistry{}, nil, nil, nil)
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
	h := mcp.MCPHandler(registry, nil, nil, nil)
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
	h := mcp.MCPHandler(registry, nil, nil, nil)
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

func TestMCPEndpoint_InitializeDeclaresListChanged(t *testing.T) {
	notifier := mcp.NewNotifier()
	h := mcp.MCPHandler(mcp.ToolRegistry{}, nil, nil, notifier)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp struct {
		Result struct {
			Capabilities struct {
				Tools struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Result.Capabilities.Tools.ListChanged, "with a notifier, listChanged must be true")

	// Without notifier
	hNoNotifier := mcp.MCPHandler(mcp.ToolRegistry{}, nil, nil, nil)
	req2 := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	rec2 := httptest.NewRecorder()
	hNoNotifier.ServeHTTP(rec2, req2)

	var resp2 struct {
		Result struct {
			Capabilities struct {
				Tools struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &resp2))
	assert.False(t, resp2.Result.Capabilities.Tools.ListChanged, "without notifier, listChanged must be false")
}

func TestMCPEndpoint_GETStreamsToolsListChangedNotification(t *testing.T) {
	notifier := mcp.NewNotifier()
	srv := httptest.NewServer(mcp.MCPHandler(mcp.ToolRegistry{}, nil, nil, notifier))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))

	// Headers arrive only after the handler subscribed, so no sleep is needed.
	notifier.NotifyToolsChanged()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, line, "notifications/tools/list_changed")
}

func TestMCPEndpoint_GETWithoutNotifierIs405WithAllow(t *testing.T) {
	h := mcp.MCPHandler(mcp.ToolRegistry{}, nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

func TestMCPEndpoint_GETStreamSendsHeartbeat(t *testing.T) {
	srv := httptest.NewServer(mcp.MCPHandler(mcp.ToolRegistry{}, nil, nil, mcp.NewNotifierWithHeartbeat(10*time.Millisecond)))
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, ": heartbeat\n", line)
}
