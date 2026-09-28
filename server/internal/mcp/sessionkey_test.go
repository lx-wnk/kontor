package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

func newSessionIssuer(t *testing.T) (mcp.KontorSessionKeyIssuer, repo.ApiKeyRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	keys := repo.NewApiKeyRepo(bundle.Client)
	return mcp.KontorSessionKeyIssuer{Keys: keys}, keys
}

func TestKontorSessionScopes_EverythingButKeysManage(t *testing.T) {
	got := mcp.KontorSessionScopes()
	require.True(t, slices.IsSorted(got))
	require.Equal(t, len(got), len(slices.Compact(slices.Clone(got))), "no duplicates")
	require.NotContains(t, got, "keys:manage")
	for _, scope := range mcp.ToolScopeMap {
		if scope != "keys:manage" {
			require.Contains(t, got, scope)
		}
	}
}

func TestKontorSessionAllowedTools_AreExactlyTheReadTools(t *testing.T) {
	allow := mcp.KontorSessionAllowedTools()
	require.True(t, slices.IsSorted(allow))
	for tool, scope := range mcp.ToolScopeMap {
		require.Equal(t, strings.HasSuffix(scope, ":read"), slices.Contains(allow, toolPrefix+tool), "tool %q (scope %q)", tool, scope)
	}
}

func TestKontorSessionKeyIssuer_Lifecycle(t *testing.T) {
	issuer, keys := newSessionIssuer(t)
	ctx := t.Context()

	token, err := issuer.Issue(ctx)
	require.NoError(t, err)
	row, err := keys.GetByHash(ctx, mcp.HashToken(token))
	require.NoError(t, err)
	require.Equal(t, repo.ApiKeyKindKontorSession, row.Kind)
	require.Nil(t, row.ExpiresAt, "a session lives until it is ended")

	pid, ok, err := issuer.Current(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, pid)

	require.NoError(t, issuer.Attach(ctx, 4242))
	pid, ok, err = issuer.Current(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 4242, pid)

	require.NoError(t, issuer.Revoke(ctx))
	_, ok, err = issuer.Current(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = keys.GetByHash(ctx, mcp.HashToken(token))
	require.Error(t, err)
}

func TestKontorSessionKey_IsRefusedCreateAPIKey(t *testing.T) {
	issuer, keys := newSessionIssuer(t)
	token, err := issuer.Issue(t.Context())
	require.NoError(t, err)

	called := map[string]bool{}
	registry := mcp.ToolRegistry{}
	for _, name := range []string{"create_api_key", "list_tasks"} {
		registry.Register(&mcp.ToolDef{Name: name, InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (*mcp.ToolResult, error) {
				called[name] = true
				return mcp.OK(nil)
			}})
	}
	h := mcp.McpAuthMiddleware(keys)(mcp.MCPHandler(registry, nil, nil, nil))
	call := func(tool string) map[string]any {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp
	}

	require.Nil(t, call("list_tasks")["error"], "positive control: the key authenticates")
	rpcErr := call("create_api_key")["error"].(map[string]any)
	require.EqualValues(t, -32003, rpcErr["code"])
	require.False(t, called["create_api_key"])
}
