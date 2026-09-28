package serverapp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

// A module calls back into the server with a credential of its own, scoped to
// what its manifest declares. The shared MCP token is deliberately withheld
// from modules, so without this a module can be called but can call nothing.
func TestModuleCredentials_IssueScopesAndRevoke(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	keys := repo.NewApiKeyRepo(bundle.Client)
	issuer := moduleCredentials{keys: keys}

	token, err := issuer.Issue(t.Context(), "obsidian", []string{"memory.read"})
	require.NoError(t, err)
	require.NotEmpty(t, token)

	key, err := keys.GetByHash(t.Context(), mcp.HashToken(token))
	require.NoError(t, err, "the issued token must authenticate")
	assert.Equal(t, repo.ApiKeyKindModule, key.Kind)
	assert.Equal(t, []string{"memory.read"}, key.Scopes, "scopes come from the manifest's uses list")

	// A second issue replaces the first: a restarted module gets a fresh
	// credential and the previous one stops working.
	second, err := issuer.Issue(t.Context(), "obsidian", []string{"memory.read"})
	require.NoError(t, err)
	_, err = keys.GetByHash(t.Context(), mcp.HashToken(token))
	require.Error(t, err, "the previous credential must stop working")

	require.NoError(t, issuer.Revoke(t.Context(), "obsidian"))
	_, err = keys.GetByHash(t.Context(), mcp.HashToken(second))
	require.Error(t, err, "a revoked credential must not authenticate")
}

// Visibility is one query, not one authorization per tool: Gate.Authorize
// books usage against a grant's rate limit, so asking it per listed tool would
// spend on a tool list what exists to bound real calls.
func TestModuleToolGate_VisibleReadsGrantsWithoutSpendingTheBudget(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	grantRepo := repo.NewGrantRepo(bundle.Client)
	gate := moduleToolGate{grants: grantRepo}

	granted := "module:obsidian:search"
	_, err = grantRepo.Create(t.Context(), repo.CreateGrantInput{
		CapabilityName: granted,
		Context:        repo.GrantContextFor("global", ""),
		Mode:           "allow",
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	visible := gate.Visible(t.Context(), []string{granted, "module:obsidian:write"})
	assert.True(t, visible[granted], "a granted tool is visible")
	assert.False(t, visible["module:obsidian:write"], "an ungranted tool is not")
}
