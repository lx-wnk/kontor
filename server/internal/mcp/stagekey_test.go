package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

func newIssuer(t *testing.T) (mcp.StageKeyIssuer, repo.ApiKeyRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	keys := repo.NewApiKeyRepo(bundle.Client)
	return mcp.StageKeyIssuer{Keys: keys}, keys, context.Background()
}

func TestStageKeyIssuer_IssuedKeyResolvesAndCarriesAttribution(t *testing.T) {
	issuer, keys, ctx := newIssuer(t)

	token, err := issuer.Issue(ctx, "sr-1", 30*time.Minute)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, "mcp_"), "the token must be an ordinary MCP bearer")

	row, err := keys.GetByHash(ctx, mcp.HashToken(token))
	require.NoError(t, err)
	require.Equal(t, "sr-1", row.StageRunID)
	require.Equal(t, repo.ApiKeyKindStageRun, row.Kind)
	require.NotNil(t, row.ExpiresAt)
	require.WithinDuration(t, time.Now().Add(30*time.Minute+mcp.StageKeyTTLBuffer), *row.ExpiresAt, time.Minute)
}

// The scope set is fixed by design (spec D3). keys:manage is excluded on
// purpose: an agent that can mint keys can mint one with no stage run and
// escape its own attribution.
func TestStageKeyIssuer_NeverGrantsKeysManage(t *testing.T) {
	issuer, keys, ctx := newIssuer(t)

	token, err := issuer.Issue(ctx, "sr-1", time.Minute)
	require.NoError(t, err)
	row, err := keys.GetByHash(ctx, mcp.HashToken(token))
	require.NoError(t, err)

	require.NotContains(t, row.Scopes, "keys:manage")
	require.Contains(t, row.Scopes, "memory:read")
	require.Contains(t, row.Scopes, "obsidian:write")
}

// Only the memory and obsidian tools resolve through capability.Decide;
// every other MCP tool is gated by its scope alone, so a scope handed out
// here is a capability granted outright, not narrowed later. This test pins
// the exclusions by name — deleting an assertion here should require
// deleting the reason it names, not just widening StageRunScopes in passing.
func TestStageKeyIssuer_NeverGrantsEscalationScopes(t *testing.T) {
	issuer, keys, ctx := newIssuer(t)

	token, err := issuer.Issue(ctx, "sr-1", time.Minute)
	require.NoError(t, err)
	row, err := keys.GetByHash(ctx, mcp.HashToken(token))
	require.NoError(t, err)

	require.NotContains(t, row.Scopes, "pipeline:control",
		"pipeline:control reaches grant_permission, resolve_permission_request and approve_all_pending on scope alone — a spec_gated agent must not approve its own spec")
	require.NotContains(t, row.Scopes, "tasks:write",
		"tasks:write reaches manage_task's grant_permissions action, which lets a caller widen its own permissions")
	require.NotContains(t, row.Scopes, "keys:manage",
		"keys:manage lets a caller mint a key with no stage run, escaping its own attribution")
}

func TestStageKeyIssuer_RevokeStopsTheKey(t *testing.T) {
	issuer, keys, ctx := newIssuer(t)

	token, err := issuer.Issue(ctx, "sr-1", time.Hour)
	require.NoError(t, err)
	_, err = keys.GetByHash(ctx, mcp.HashToken(token))
	require.NoError(t, err)

	require.NoError(t, issuer.Revoke(ctx, "sr-1"))

	_, err = keys.GetByHash(ctx, mcp.HashToken(token))
	require.Error(t, err)
}

// Revoking a stage run that never had a key is not an error: the orchestrator
// calls Revoke on every terminal transition, including runs whose spawn never
// got a credential.
func TestStageKeyIssuer_RevokeUnknownStageRunIsNotAnError(t *testing.T) {
	issuer, _, ctx := newIssuer(t)
	require.NoError(t, issuer.Revoke(ctx, "sr-never-existed"))
}

func TestStageKeyIssuer_TwoIssuesGiveDifferentTokens(t *testing.T) {
	issuer, _, ctx := newIssuer(t)

	a, err := issuer.Issue(ctx, "sr-1", time.Minute)
	require.NoError(t, err)
	b, err := issuer.Issue(ctx, "sr-1", time.Minute)
	require.NoError(t, err)
	require.NotEqual(t, a, b, "a retry must not reuse the previous run's token")
}

// keys.List only ever returns kind=user rows (see api_key_repo.go), so it
// cannot see a stray stage_run row and would pass even with the guard
// deleted. Counting through the ent client directly is the honest check:
// it counts every ApiKey row regardless of kind.
func TestStageKeyIssuer_RefusesEmptyStageRunID(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	issuer := mcp.StageKeyIssuer{Keys: repo.NewApiKeyRepo(bundle.Client)}

	_, err = issuer.Issue(context.Background(), "", time.Minute)
	require.Error(t, err, "a key with no stage run would be unattributable and unrevocable")

	count, err := bundle.Client.ApiKey.Query().Count(context.Background())
	require.NoError(t, err)
	require.Zero(t, count, "the guard must refuse before it mints — no row of any kind should exist")
}
