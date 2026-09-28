package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

// TestMcpAuthMiddleware_StageRunIDReachesTheCallerContexts crosses the one
// seam every other attribution test injects around: the middleware copying
// the stored key's stage_run_id onto MCPAuthInfo. Every CallerResolver and
// tool test builds MCPAuthInfo by hand, so hardcoding StageRunID to "" in
// McpAuthMiddleware would silently strip task and routine attribution from
// every real request while the whole suite stayed green.
//
// The token goes in as a real Bearer header and the contexts come out of a
// resolver reading the request's own context — no MCPAuthInfo is constructed
// by the test.
func TestMcpAuthMiddleware_StageRunIDReachesTheCallerContexts(t *testing.T) {
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	keys := repo.NewApiKeyRepo(bundle.Client)
	runID, taskID := seedRun(t, bundle, "middleware-attribution", "sched-9")

	token, err := mcp.StageKeyIssuer{Keys: keys}.Issue(ctx, runID, time.Minute)
	require.NoError(t, err)

	resolver := mcp.CallerResolver{
		StageRuns: repo.NewStageRunRepo(bundle.Client),
		Tasks:     repo.NewTaskRepo(bundle.Client),
	}

	var got []capability.Context
	handler := mcp.McpAuthMiddleware(keys)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = resolver.Contexts(r.Context())
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []capability.Context{
		{Kind: repo.GrantContextTask, Ref: taskID},
		{Kind: repo.GrantContextRoutine, Ref: "sched-9"},
	}, got, "a stage-run credential must arrive carrying its task and routine")
}

// The counterpart: an ordinary user key carries no stage run, so it must
// resolve to no contexts. Without this, StageRunID could be pinned to any
// constant and the test above would still pass.
func TestMcpAuthMiddleware_UserKeyCarriesNoStageRun(t *testing.T) {
	ctx := context.Background()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	keys := repo.NewApiKeyRepo(bundle.Client)
	token := mcp.GenerateAPIToken()
	_, err = keys.Create(ctx, repo.CreateApiKeyInput{
		Name: "human", Hash: mcp.HashToken(token), Scopes: []string{"tasks:read"},
	})
	require.NoError(t, err)

	// A stage run exists in the database, so an accidentally hardcoded id
	// would have something real to resolve to.
	_, _ = seedRun(t, bundle, "not-this-one", "sched-9")

	resolver := mcp.CallerResolver{
		StageRuns: repo.NewStageRunRepo(bundle.Client),
		Tasks:     repo.NewTaskRepo(bundle.Client),
	}

	var got []capability.Context
	var gotStageRunID string
	handler := mcp.McpAuthMiddleware(keys)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = resolver.Contexts(r.Context())
		gotStageRunID = mcp.AuthFromContext(r.Context()).StageRunID
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	// Asserted on the field, not only on the (empty either way) contexts: a
	// middleware copying the wrong field — key.ID, say — would resolve to
	// nothing too, and the contexts assertion alone could never see it.
	require.Empty(t, gotStageRunID, "a user key's row carries no stage run")
	require.Empty(t, got, "a machine-wide key must resolve to no contexts")
}
