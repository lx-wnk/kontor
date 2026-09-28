package mcpapps_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestEnsureGrant_DedupesLiveEquivalentGrants(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	grants := repo.NewGrantRepo(bundle.Client)

	in := repo.CreateGrantInput{
		CapabilityName: "mcp__mail__search",
		Context:        repo.GrantContextFor(repo.GrantContextRoutine, "routine-1"),
		Mode:           "allow",
		GrantedBy:      "test",
		Reason:         "test",
	}

	created, err := mcpapps.EnsureGrant(ctx, grants, in)
	require.NoError(t, err)
	require.True(t, created)

	created, err = mcpapps.EnsureGrant(ctx, grants, in)
	require.NoError(t, err)
	require.False(t, created, "an identical grant must not be duplicated")

	rows, err := grants.ListForCapability(ctx, in.CapabilityName)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestEnsureGrant_RevokedGrantDoesNotCount(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	grants := repo.NewGrantRepo(bundle.Client)

	in := repo.CreateGrantInput{
		CapabilityName: "mcp__mail__search",
		Context:        repo.GrantContextFor(repo.GrantContextRoutine, "routine-1"),
		Mode:           "allow",
		GrantedBy:      "test",
		Reason:         "test",
	}

	row, err := grants.Create(ctx, in)
	require.NoError(t, err)
	require.NoError(t, grants.Revoke(ctx, row.ID, "test"))

	created, err := mcpapps.EnsureGrant(ctx, grants, in)
	require.NoError(t, err)
	require.True(t, created, "a revoked grant must not block a new one")

	rows, err := grants.ListForCapability(ctx, in.CapabilityName)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

func TestEnsureGrant_DifferentContextRefCreatesSecondRow(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	grants := repo.NewGrantRepo(bundle.Client)

	base := repo.CreateGrantInput{
		CapabilityName: "mcp__mail__search",
		Mode:           "allow",
		GrantedBy:      "test",
		Reason:         "test",
	}

	first := base
	first.Context = repo.GrantContextFor(repo.GrantContextRoutine, "routine-1")
	created, err := mcpapps.EnsureGrant(ctx, grants, first)
	require.NoError(t, err)
	require.True(t, created)

	second := base
	second.Context = repo.GrantContextFor(repo.GrantContextRoutine, "routine-2")
	created, err = mcpapps.EnsureGrant(ctx, grants, second)
	require.NoError(t, err)
	require.True(t, created)

	rows, err := grants.ListForCapability(ctx, base.CapabilityName)
	require.NoError(t, err)
	require.Len(t, rows, 2)
}
