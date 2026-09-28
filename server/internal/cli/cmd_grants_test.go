package cli

import (
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

func TestParseGrantScope(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    repo.GrantContext
		wantErr bool
	}{
		{name: "bare global", in: "global", want: repo.GrantContext{Kind: "global", Ref: ""}},
		{name: "project with path ref", in: "project:/home/x", want: repo.GrantContext{Kind: "project", Ref: "/home/x"}},
		{name: "task ref containing a colon splits on first only", in: "task:a:b", want: repo.GrantContext{Kind: "task", Ref: "a:b"}},
		{name: "unknown kind rejected", in: "nope:x", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseGrantScope(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseGrantScope_UnknownKind_ErrorNamesValidKinds(t *testing.T) {
	_, err := parseGrantScope("nope:x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "global")
}

// A non-empty ref on the global context is not rejected by parseGrantScope
// (the kind alone is valid) — GrantRepo.Create is the layer that rejects it.
func TestAddGrant_GlobalScopeWithRef_RejectedByRepoCreate(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	gctx, err := parseGrantScope("global:x")
	require.NoError(t, err)
	assert.Equal(t, "global", gctx.Kind)
	assert.Equal(t, "x", gctx.Ref)

	_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{Scope: "global:x", Mode: repo.GrantModeAllow, Pattern: "", PatternSet: true})
	require.Error(t, err)
}

func TestAddGrant_RejectsUnknownCapability(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	_, err = addGrant(ctx, store, "not.a.real.capability", grantAddOpts{Scope: "global", Mode: repo.GrantModeAllow, Pattern: "", PatternSet: true})
	require.Error(t, err)

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestAddGrant_SucceedsForSeededCapability(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	g, err := addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:      "global",
		Mode:       repo.GrantModeAllow,
		Pattern:    "",
		PatternSet: true,
	})
	require.NoError(t, err)

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, g.ID, rows[0].ID)
	assert.Equal(t, repo.CapabilityMemoryRead, rows[0].CapabilityName)
	assert.Equal(t, repo.GrantModeAllow, rows[0].Mode)
	assert.Equal(t, "global", rows[0].ContextKind)
	assert.Equal(t, "", rows[0].ContextRef)
	assert.Equal(t, "", rows[0].Pattern)
}

func TestAddGrant_ExpiresIn(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	before := time.Now()
	g, err := addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:      "global",
		Mode:       repo.GrantModeAllow,
		Pattern:    "",
		PatternSet: true,
		ExpiresIn:  "1h",
	})
	require.NoError(t, err)
	after := time.Now()

	require.NotNil(t, g.ExpiresAt)
	assert.True(t, g.ExpiresAt.After(before.Add(59*time.Minute)))
	assert.True(t, g.ExpiresAt.Before(after.Add(61*time.Minute)))
}

func TestRevokeGrant_TombstonesNotDeletes(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	g, err := addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{Scope: "global", Mode: repo.GrantModeAllow, Pattern: "", PatternSet: true})
	require.NoError(t, err)

	require.NoError(t, store.grants.Revoke(ctx, g.ID, "cli:tester"))

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].RevokedAt)
	assert.Equal(t, "cli:tester", rows[0].RevokedBy)
}

// TestGrantCLI_OpensTheAuthorizeGate is the acceptance criterion for this
// unit: memory.Gate.Authorize denies with no grant present, and a grant created
// through the CLI's own add helper makes the identical call succeed.
func TestGrantCLI_OpensTheAuthorizeGate(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NotZero(t, repo.SeedCapabilities(ctx, store.caps))

	err = memory.Gate{Capabilities: store.caps, Grants: store.grants, GrantUsage: store.grantUsage}.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.Error(t, err, "no grant exists yet — the gate must deny")

	_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:      "global",
		Mode:       repo.GrantModeAllow,
		Pattern:    "",
		PatternSet: true,
	})
	require.NoError(t, err)

	err = memory.Gate{Capabilities: store.caps, Grants: store.grants, GrantUsage: store.grantUsage}.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	assert.NoError(t, err, "the grant just created must be the one Authorize matches")
}

func TestAddGrantRejectsALimitCountWithoutAWindow(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:      "global",
		Mode:       repo.GrantModeAllow,
		Pattern:    "",
		PatternSet: true,
		LimitCount: 2,
	})
	require.Error(t, err, "a limit with a zero window is counted since now and never triggers")
	assert.Contains(t, err.Error(), "--limit-window")

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "a rejected grant must not be written")

	_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:       "global",
		Mode:        repo.GrantModeAllow,
		Pattern:     "",
		PatternSet:  true,
		LimitCount:  2,
		LimitWindow: 60,
	})
	require.NoError(t, err)
}

func TestAddGrant_PatternRequired(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{Scope: "global", Mode: repo.GrantModeAllow})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--pattern is required")

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "a rejected grant must not be written")
}

func TestAddGrant_WildcardPatternStoredLiterally(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	g, err := addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:      "global",
		Mode:       repo.GrantModeAllow,
		Pattern:    "*",
		PatternSet: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "*", g.Pattern)

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "*", rows[0].Pattern)
}

// TestGrantCLI_WildcardPatternOpensTheAuthorizeGate is TestGrantCLI_OpensTheAuthorizeGate's
// deny-before/allow-after shape, but with an explicit "*" pattern against a
// non-empty value — proving change 1 did not break Match's wildcard handling.
func TestGrantCLI_WildcardPatternOpensTheAuthorizeGate(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NotZero(t, repo.SeedCapabilities(ctx, store.caps))

	err = memory.Gate{Capabilities: store.caps, Grants: store.grants, GrantUsage: store.grantUsage}.Authorize(ctx, repo.CapabilityMemoryRead, "some/value", repo.GlobalScope())
	require.Error(t, err, "no grant exists yet — the gate must deny")

	_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
		Scope:      "global",
		Mode:       repo.GrantModeAllow,
		Pattern:    "*",
		PatternSet: true,
	})
	require.NoError(t, err)

	err = memory.Gate{Capabilities: store.caps, Grants: store.grants, GrantUsage: store.grantUsage}.Authorize(ctx, repo.CapabilityMemoryRead, "some/value", repo.GlobalScope())
	assert.NoError(t, err, "a literal '*' pattern must match just like the empty-string wildcard")
}

func TestAddGrant_RejectsNonPositiveExpiresIn(t *testing.T) {
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	for _, d := range []string{"-1h", "0s"} {
		_, err = addGrant(ctx, store, repo.CapabilityMemoryRead, grantAddOpts{
			Scope:      "global",
			Mode:       repo.GrantModeAllow,
			Pattern:    "*",
			PatternSet: true,
			ExpiresIn:  d,
		})
		require.Error(t, err, "duration %q must be rejected", d)
	}

	rows, err := store.grants.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, rows, "a rejected grant must not be written")
}

// TestGrantsAddCmd_DefaultsReachTheDatabase is the only test in this file that
// drives a RunE through cobra rather than calling addGrant directly — the
// documented --scope/--mode defaults are worthless if cobra never wires them
// to the flags addGrant reads.
func TestGrantsAddCmd_DefaultsReachTheDatabase(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"

	cmd := newGrantsCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"add", repo.CapabilityMemoryRead, "--db", dbPath, "--pattern", "*"})
	require.NoError(t, cmd.Execute())

	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	rows, err := store.grants.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, repo.GrantModeAllow, rows[0].Mode)
	assert.Equal(t, "global", rows[0].ContextKind)
}

func TestQuoteIfControl(t *testing.T) {
	assert.Equal(t, "plain-value", quoteIfControl("plain-value"))
	assert.Equal(t, strconv.Quote("\x1b[1A"), quoteIfControl("\x1b[1A"))
}
