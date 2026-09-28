package serverapp

import (
	"context"
	"errors"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/serverask"
)

// A nil *serverask.Asker assigned straight into an interface field yields a
// NON-nil interface holding a nil pointer. memory.Gate.Authorize branches on
// `Asker == nil`, so the plain assignment would send an ask decision to a nil
// pointer and panic instead of denying — in auth mode none, where nobody can
// answer in the first place. Both helpers exist only to prevent that, so both
// are pinned here.
func TestAskerHelpersReturnGenuinelyNilInterfaces(t *testing.T) {
	if got := askerArgFor(nil); got != nil {
		t.Errorf("askerArgFor(nil) = %#v, want a nil interface — memory.Gate.Authorize tests Asker == nil", got)
	}
	if got := capabilityAskerFor(nil); got != nil {
		t.Errorf("capabilityAskerFor(nil) = %#v, want a nil interface — the router tests deps.CapabilityAsker != nil", got)
	}

	present := serverask.New(nil)
	if askerArgFor(present) == nil {
		t.Error("askerArgFor(asker) = nil, want the asker — a wired asker must reach the Gate")
	}
	if capabilityAskerFor(present) == nil {
		t.Error("capabilityAskerFor(asker) = nil, want the asker — a wired asker must receive the rescan")
	}
}

// TestGateBuiltLikeBypassAuthDeniesRatherThanPanics runs the combination the
// helper above protects: an ask decision against the Gate DI builds when no
// asker is wired must return, not crash.
//
// An earlier version of this test only re-asserted that the interface was nil
// — the same thing the test above already checks — so it proved neither
// "denies" nor "rather than panics". Reaching Authorize is the point.
func TestGateBuiltLikeBypassAuthDeniesRatherThanPanics(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = bundle.Client.Close() }()
	ctx := context.Background()

	capabilities := repo.NewCapabilityRepo(bundle.Client)
	if repo.SeedCapabilities(ctx, capabilities) == 0 {
		t.Fatal("seeded no capabilities — the gate would deny for the wrong reason")
	}
	grants := repo.NewGrantRepo(bundle.Client)
	if _, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Mode:           repo.GrantModeAsk,
		GrantedBy:      "test",
	}); err != nil {
		t.Fatalf("create ask grant: %v", err)
	}

	gate := memory.Gate{
		Capabilities: capabilities,
		Grants:       grants,
		GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
		Asker:        askerArgFor(nil),
	}

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	if !errors.Is(err, capability.ErrAskRequired) {
		t.Fatalf("Authorize = %v, want ErrAskRequired — an ask with no asker must deny, not panic", err)
	}
}
