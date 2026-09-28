package obsidian_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newRepos(t *testing.T) (repo.ResourceRepo, repo.CapabilityRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewResourceRepo(bundle.Client), repo.NewCapabilityRepo(bundle.Client), context.Background()
}

func TestRegisterDeclaresIrreversibleWriteAndDelete(t *testing.T) {
	resources, caps, ctx := newRepos(t)

	if err := obsidian.Register(ctx, resources, caps); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, name := range []string{obsidian.CapabilityWrite, obsidian.CapabilityDelete} {
		row, err := caps.Get(ctx, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if row.Reversible {
			t.Errorf("%s: Reversible = true, want false — a preset alone must never satisfy this capability", name)
		}
	}

	for _, name := range []string{obsidian.CapabilityRead, obsidian.CapabilitySearch} {
		row, err := caps.Get(ctx, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if !row.Reversible {
			t.Errorf("%s: Reversible = false, want true", name)
		}
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	resources, caps, ctx := newRepos(t)

	if err := obsidian.Register(ctx, resources, caps); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := obsidian.Register(ctx, resources, caps); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	apps, err := resources.ListForKind(ctx, repo.ResourceKindApplication)
	if err != nil {
		t.Fatalf("ListForKind: %v", err)
	}
	if len(apps) != 1 {
		t.Errorf("application resources = %d, want 1", len(apps))
	}

	rows, err := caps.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 4 {
		t.Errorf("capability rows = %d, want 4", len(rows))
	}
}

func TestRegisterCapabilityClassAndEnforcement(t *testing.T) {
	resources, caps, ctx := newRepos(t)

	if err := obsidian.Register(ctx, resources, caps); err != nil {
		t.Fatalf("Register: %v", err)
	}

	for _, name := range []string{obsidian.CapabilityRead, obsidian.CapabilitySearch, obsidian.CapabilityWrite, obsidian.CapabilityDelete} {
		row, err := caps.Get(ctx, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if row.Class != repo.CapClassReach {
			t.Errorf("%s: class = %q, want %q", name, row.Class, repo.CapClassReach)
		}
		if len(row.EnforceableBy) != 1 || row.EnforceableBy[0] != "server" {
			t.Errorf("%s: EnforceableBy = %v, want [server]", name, row.EnforceableBy)
		}
	}
}
