package github_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/capability"
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

// TestRegisterPutsMergeInSpend is the decision this application turns on:
// capability.defaultEffect (decide.go:233-242) sends "spend" to deny and
// "reach" to ask, so the class alone is what makes an ungranted merge
// impossible rather than a prompt somebody can click through.
func TestRegisterPutsMergeInSpend(t *testing.T) {
	resources, caps, ctx := newRepos(t)
	if err := github.Register(ctx, resources, caps); err != nil {
		t.Fatalf("Register: %v", err)
	}

	want := map[string]struct {
		class      string
		reversible bool
	}{
		github.CapabilityRead:    {repo.CapClassReach, true},
		github.CapabilitySearch:  {repo.CapClassReach, true},
		github.CapabilityComment: {repo.CapClassReach, false},
		github.CapabilityMerge:   {repo.CapClassSpend, false},
	}
	for name, exp := range want {
		row, err := caps.Get(ctx, name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if row.Class != exp.class {
			t.Errorf("%s: Class = %q, want %q", name, row.Class, exp.class)
		}
		if row.Reversible != exp.reversible {
			t.Errorf("%s: Reversible = %v, want %v", name, row.Reversible, exp.reversible)
		}
		if len(row.EnforceableBy) != 1 || row.EnforceableBy[0] != capability.EnforcerServer {
			t.Errorf("%s: EnforceableBy = %v, want [%s]", name, row.EnforceableBy, capability.EnforcerServer)
		}
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	resources, caps, ctx := newRepos(t)
	if err := github.Register(ctx, resources, caps); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := github.Register(ctx, resources, caps); err != nil {
		t.Fatalf("second Register: %v", err)
	}

	apps, err := resources.ListForKind(ctx, repo.ResourceKindApplication)
	if err != nil {
		t.Fatalf("ListForKind: %v", err)
	}
	count := 0
	for _, a := range apps {
		if a.Slug == github.Slug {
			count++
		}
	}
	if count != 1 {
		t.Errorf("registry holds %d rows for slug %q, want exactly 1", count, github.Slug)
	}
}

// TestCapabilitiesMatchWhatRegisterWrote keeps the exported declaration list —
// which Task 6's surface-parity test iterates — from drifting away from the
// rows Register actually writes.
func TestCapabilitiesMatchWhatRegisterWrote(t *testing.T) {
	resources, caps, ctx := newRepos(t)
	if err := github.Register(ctx, resources, caps); err != nil {
		t.Fatalf("Register: %v", err)
	}
	decls := github.Capabilities()
	if len(decls) != 4 {
		t.Fatalf("Capabilities() returned %d decls, want 4", len(decls))
	}
	for _, d := range decls {
		row, err := caps.Get(ctx, d.Name)
		if err != nil {
			t.Fatalf("get %s: %v", d.Name, err)
		}
		if row.Class != d.Class || row.Reversible != d.Reversible {
			t.Errorf("%s: row = (%s, %v), decl = (%s, %v)", d.Name, row.Class, row.Reversible, d.Class, d.Reversible)
		}
	}
}
