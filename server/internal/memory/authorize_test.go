package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// newAuthorizeGateForTest wires a memory.Gate against a real in-memory
// SQLite database, with the capability catalogue seeded so a grant actually
// resolves to allow rather than the fail-closed "capability never
// catalogued" default. asker is passed straight through as Gate.Asker so
// tests can assert both the nil (fail-closed) and wired-asker paths.
func newAuthorizeGateForTest(t *testing.T, asker capability.Asker) (memory.Gate, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(context.Background(), capRepo)
	gate := memory.Gate{
		Capabilities: capRepo,
		Grants:       repo.NewGrantRepo(bundle.Client),
		GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
		Asker:        asker,
	}
	return gate, context.Background()
}

// TestAuthorizeEnforcesGrantRateLimit is the regression test for the
// "rate-limited memory grant is silently unlimited" bug: Authorize used to
// pass capability.GrantView{}, 0 to Enforce regardless of what the winning
// grant actually configured, so a LimitCount was accepted at grant-creation
// time and then never read back. With the fix, the second call inside the
// same window must be refused (Enforce downgrades to ask, and with no Asker
// wired that fails closed to an error) rather than silently allowed forever.
func TestAuthorizeEnforcesGrantRateLimit(t *testing.T) {
	gate, ctx := newAuthorizeGateForTest(t, nil)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName:     repo.CapabilityMemoryRead,
		Context:            repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:            "",
		Mode:               repo.GrantModeAllow,
		LimitCount:         1,
		LimitWindowSeconds: 3600,
		GrantedBy:          "test",
	})
	require.NoError(t, err)

	scope := repo.GlobalScope()
	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope)
	require.NoError(t, err, "first call must be within the limit")

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope)
	require.Error(t, err, "second call within the same window must be refused by the grant's own rate limit")
}

// TestAuthorizeUnlimitedGrantNeverTouchesUsage exercises the LimitCount 0
// (unlimited) path repeatedly to guard against a regression that starts
// recording usage — and therefore eventually rate-limiting — a grant that
// was never configured with a limit at all.
func TestAuthorizeUnlimitedGrantNeverTouchesUsage(t *testing.T) {
	gate, ctx := newAuthorizeGateForTest(t, nil)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	scope := repo.GlobalScope()
	for i := 0; i < 5; i++ {
		err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope)
		require.NoError(t, err, "call %d: an unlimited grant must never be rate-limited", i)
	}
}

// TestAuthorizeApprovedAskAfterLimitRecordsUsage is the regression test for
// the "human-approved rate-limited use is never recorded" bug: once a grant
// is exhausted, Enforce downgrades the allow to ask, and an approving human
// used to leave no trace at all — the next call would ask again having
// silently allowed one extra use for free. The approved use must count.
func TestAuthorizeApprovedAskAfterLimitRecordsUsage(t *testing.T) {
	asker := &recordingAsker{answer: true}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	grant, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName:     repo.CapabilityMemoryRead,
		Context:            repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:            "",
		Mode:               repo.GrantModeAllow,
		LimitCount:         1,
		LimitWindowSeconds: 3600,
		GrantedBy:          "test",
	})
	require.NoError(t, err)

	scope := repo.GlobalScope()
	require.NoError(t, gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope), "first call must be within the limit")
	require.False(t, asker.called, "the first, within-limit call must not need a human")

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope)
	require.NoError(t, err, "an approved ask must let the call through")
	require.True(t, asker.called, "the exhausted second call must have been downgraded to ask")

	count, err := gate.GrantUsage.CountSince(ctx, grant.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, count, "the within-limit call and the human-approved call must each record exactly one usage row")
}

// TestAuthorizeDeniedAskAfterLimitRecordsNoUsage proves a human refusal
// records nothing: only an actually-approved use counts against the grant.
func TestAuthorizeDeniedAskAfterLimitRecordsNoUsage(t *testing.T) {
	asker := &recordingAsker{answer: false}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	grant, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName:     repo.CapabilityMemoryRead,
		Context:            repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:            "",
		Mode:               repo.GrantModeAllow,
		LimitCount:         1,
		LimitWindowSeconds: 3600,
		GrantedBy:          "test",
	})
	require.NoError(t, err)

	scope := repo.GlobalScope()
	require.NoError(t, gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope))

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope)
	require.Error(t, err, "a refused ask must still deny the call")

	count, err := gate.GrantUsage.CountSince(ctx, grant.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, count, "a refused ask must not record a second usage row")
}

// TestAuthorizeApprovedAskForNonLimitReasonRecordsNoUsage proves the
// recording is specific to an ask caused by a rate limit: a plain
// GrantModeAsk grant (no limit at all) that a human approves must not gain a
// usage row it was never rate-limited by in the first place.
func TestAuthorizeApprovedAskForNonLimitReasonRecordsNoUsage(t *testing.T) {
	asker := &recordingAsker{answer: true}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	grant, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAsk,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.NoError(t, err)
	require.True(t, asker.called)

	count, err := gate.GrantUsage.CountSince(ctx, grant.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 0, count, "an ask not caused by a rate limit must never write a usage row")
}

// recordingAsker mirrors capability_test's recordingAsker (unexported there,
// so it cannot be reused directly) to pin what Gate.Authorize hands its
// Asker: the capability name and value being asked about, not merely a
// resolved Decision.
type recordingAsker struct {
	answer      bool
	err         error
	called      bool
	lastRequest capability.Request
}

func (a *recordingAsker) Ask(_ context.Context, req capability.Request, _ capability.Decision) (bool, error) {
	a.called = true
	a.lastRequest = req
	return a.answer, a.err
}

// TestGateNilAskerFailsClosedOnAsk proves a Gate built without an Asker
// (the four non-interactive call sites: pipeline memory push, obsidian
// index) cannot block for a human — an ask-effect decision denies instead.
func TestGateNilAskerFailsClosedOnAsk(t *testing.T) {
	gate, ctx := newAuthorizeGateForTest(t, nil)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAsk,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.ErrorIs(t, err, capability.ErrAskRequired)
}

// TestGateDenyIsNotAskable proves a deny grant denies through the Gate and
// never reaches the asker. Routing a deny to a human would let one turn an
// explicit deny into an allow, which no grant in the model can do.
//
// Written after mutation testing: replacing the enforcer's deny branch with
// "return nil" left every test in this package green, so nothing here pinned
// that a deny denies at the layer production actually calls.
func TestGateDenyIsNotAskable(t *testing.T) {
	asker := &recordingAsker{answer: true}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeDeny,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.ErrorIs(t, err, capability.ErrDenied)
	require.False(t, asker.called, "a deny must never be offered to a human")
}

// TestGateAskerGrantedAllowsAndSeesTheRequest proves a wired Asker that
// answers yes lets the call through, and that it receives the capability
// name and value being asked about — the whole point of Problem 1.
func TestGateAskerGrantedAllowsAndSeesTheRequest(t *testing.T) {
	asker := &recordingAsker{answer: true}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAsk,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "some-value", repo.GlobalScope())
	require.NoError(t, err)
	require.True(t, asker.called, "an ask decision must consult the wired asker")
	require.Equal(t, repo.CapabilityMemoryRead, asker.lastRequest.Capability)
	require.Equal(t, "some-value", asker.lastRequest.Value)
}

// TestGateAskerRefusedDenies proves a wired Asker that answers no denies the
// call with capability.ErrDenied, not a silent pass-through.
func TestGateAskerRefusedDenies(t *testing.T) {
	asker := &recordingAsker{answer: false}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAsk,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.ErrorIs(t, err, capability.ErrDenied)
}

// TestGateAskerErrorSurfaces proves an asker transport failure is returned
// to the caller rather than silently treated as a denial.
func TestGateAskerErrorSurfaces(t *testing.T) {
	sentinel := errors.New("asker transport failed")
	asker := &recordingAsker{err: sentinel}
	gate, ctx := newAuthorizeGateForTest(t, asker)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "",
		Mode:           repo.GrantModeAsk,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.ErrorIs(t, err, sentinel)
}
