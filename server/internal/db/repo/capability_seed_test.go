package repo_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

func TestSeedCapabilitiesIsIdempotent(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	// One row per grantable tool, plus the two memory capabilities
	// (memory.read, memory.write) that SeedCapabilities seeds alongside them.
	want := len(permissions.GrantableToolNames()) + 2

	seeded := repo.SeedCapabilities(ctx, capRepo)
	if seeded != want {
		t.Errorf("first seed created %d rows, want %d (one per grantable tool, plus memory.read/memory.write)", seeded, want)
	}

	again := repo.SeedCapabilities(ctx, capRepo)
	if again != 0 {
		t.Errorf("second seed created %d rows, want 0 — it must be idempotent", again)
	}

	rows, err := capRepo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != want {
		t.Errorf("catalogue has %d rows, want %d", len(rows), want)
	}
}

func TestSeedCapabilitiesClassesAndEnforcement(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(ctx, capRepo)

	webFetch, err := capRepo.Get(ctx, "WebFetch")
	if err != nil {
		t.Fatalf("get WebFetch: %v", err)
	}
	if webFetch.Class != repo.CapClassReach {
		t.Errorf("WebFetch class = %q, want %q", webFetch.Class, repo.CapClassReach)
	}

	bash, err := capRepo.Get(ctx, "Bash")
	if err != nil {
		t.Fatalf("get Bash: %v", err)
	}
	if bash.Class != repo.CapClassTool {
		t.Errorf("Bash class = %q, want %q", bash.Class, repo.CapClassTool)
	}

	for _, row := range []*struct {
		name string
		by   []string
	}{
		{"Bash", bash.EnforceableBy},
		{"WebFetch", webFetch.EnforceableBy},
	} {
		want := map[string]bool{capability.EnforcerSpawn: true, capability.EnforcerHook: true}
		if len(row.by) != len(want) {
			t.Errorf("%s enforceable_by = %v, want %v", row.name, row.by, want)
			continue
		}
		for _, v := range row.by {
			if !want[v] {
				t.Errorf("%s enforceable_by contains unexpected %q", row.name, v)
			}
		}
	}
}

// TestSeedCapabilitiesDoesNotOverwriteHumanEdit proves a class change made
// through the catalogue after seeding survives a second boot's seed pass.
func TestSeedCapabilitiesDoesNotOverwriteHumanEdit(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(ctx, capRepo)

	// A human edits Bash's class through the catalogue after seeding.
	if _, err := capRepo.Upsert(ctx, repo.UpsertCapabilityInput{
		Name:          "Bash",
		Class:         repo.CapClassResource,
		EnforceableBy: []string{capability.EnforcerServer},
	}); err != nil {
		t.Fatalf("human edit: %v", err)
	}

	repo.SeedCapabilities(ctx, capRepo)

	bash, err := capRepo.Get(ctx, "Bash")
	if err != nil {
		t.Fatalf("get Bash: %v", err)
	}
	if bash.Class != repo.CapClassResource {
		t.Errorf("Bash class = %q, want %q (human edit must survive a re-seed)", bash.Class, repo.CapClassResource)
	}
}

// failingUpsertCapabilityRepo wraps a real CapabilityRepo and fails Upsert
// for one specific name, to exercise SeedCapabilities' warn-and-continue
// path — mirroring TestReconcilePluginResourcesSkipsOversizedIDButLinksTheRest
// for the analogous plugin-reconcile precedent.
type failingUpsertCapabilityRepo struct {
	repo.CapabilityRepo
	failName string
}

func (f *failingUpsertCapabilityRepo) Upsert(ctx context.Context, in repo.UpsertCapabilityInput) (*ent.Capability, error) {
	if in.Name == f.failName {
		return nil, fmt.Errorf("simulated failure for %s", f.failName)
	}
	return f.CapabilityRepo.Upsert(ctx, in)
}

// TestSeedCapabilitiesSkipsUnseedableNameButSeedsTheRest proves one
// unseedable name does not stop every name ordered after it — the
// warn-and-continue behaviour the brief asked to mirror from
// ReconcilePluginResources. A loop that aborted on the first failure would
// leave every later capability missing from the catalogue on every boot.
func TestSeedCapabilitiesSkipsUnseedableNameButSeedsTheRest(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	real := repo.NewCapabilityRepo(bundle.Client)
	names := permissions.GrantableToolNames()
	if len(names) < 2 {
		t.Fatal("test needs at least two grantable tool names")
	}
	failName := names[0] // GrantableToolNames is sorted, so later names exist to prove they still seed.
	fake := &failingUpsertCapabilityRepo{CapabilityRepo: real, failName: failName}

	seeded := repo.SeedCapabilities(ctx, fake)
	// Every tool name except the failing one, plus the two memory
	// capabilities — failName never matches either of those.
	if want := len(names) - 1 + 2; seeded != want {
		t.Errorf("seeded = %d, want %d (every name except the failing one, plus memory.read/memory.write)", seeded, want)
	}

	if _, err := real.Get(ctx, failName); err == nil {
		t.Errorf("failing name %q must not have been seeded", failName)
	}

	for _, later := range names[1:] {
		if _, err := real.Get(ctx, later); err != nil {
			t.Errorf("name %q ordered after the failing one was not seeded: %v", later, err)
		}
	}
}

// TestSeedCapabilitiesSeedsMemoryCapabilities proves memory.read and
// memory.write land in the catalogue with class "resource" and enforcement
// at the server point — without this, Decide looks up either name, gets a
// zero-value CapabilityView, and denies every memory access even after a
// human issues a grant.
func TestSeedCapabilitiesSeedsMemoryCapabilities(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(ctx, capRepo)

	for _, name := range []string{repo.CapabilityMemoryRead, repo.CapabilityMemoryWrite} {
		row, err := capRepo.Get(ctx, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if row.Class != repo.CapClassResource {
			t.Errorf("%s class = %q, want %q", name, row.Class, repo.CapClassResource)
		}
		want := []string{capability.EnforcerServer}
		if len(row.EnforceableBy) != len(want) || row.EnforceableBy[0] != want[0] {
			t.Errorf("%s enforceable_by = %v, want %v", name, row.EnforceableBy, want)
		}
	}
}

// TestSeedCapabilitiesMemoryCapabilitiesAreIdempotent proves a second seed
// pass neither duplicates nor overwrites the memory capability rows.
func TestSeedCapabilitiesMemoryCapabilitiesAreIdempotent(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	capRepo := repo.NewCapabilityRepo(bundle.Client)
	repo.SeedCapabilities(ctx, capRepo)

	// A human edits memory.write's class after the first seed.
	if _, err := capRepo.Upsert(ctx, repo.UpsertCapabilityInput{
		Name:          repo.CapabilityMemoryWrite,
		Class:         repo.CapClassSpend,
		EnforceableBy: []string{capability.EnforcerServer},
	}); err != nil {
		t.Fatalf("human edit: %v", err)
	}

	again := repo.SeedCapabilities(ctx, capRepo)
	if again != 0 {
		t.Errorf("second seed created %d rows, want 0 — it must be idempotent", again)
	}

	row, err := capRepo.Get(ctx, repo.CapabilityMemoryWrite)
	if err != nil {
		t.Fatalf("get memory.write: %v", err)
	}
	if row.Class != repo.CapClassSpend {
		t.Errorf("memory.write class = %q, want %q (human edit must survive a re-seed)", row.Class, repo.CapClassSpend)
	}
}
