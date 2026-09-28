package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newCapabilityRepo(t *testing.T) (repo.CapabilityRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewCapabilityRepo(bundle.Client), context.Background()
}

func TestCapabilityUpsertIsIdempotent(t *testing.T) {
	r, ctx := newCapabilityRepo(t)
	in := repo.UpsertCapabilityInput{
		Name:          "mail.send",
		Class:         repo.CapClassReach,
		EnforceableBy: []string{capability.EnforcerServer},
		Reversible:    false,
		Description:   "Send mail on the user's behalf",
	}
	first, err := r.Upsert(ctx, in)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	in.Description = "Send an email"
	second, err := r.Upsert(ctx, in)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("upsert created a second row: %s != %s", first.ID, second.ID)
	}
	if second.Description != "Send an email" {
		t.Errorf("description = %q, want the updated value", second.Description)
	}
}

func TestCapabilityIrreversibleIsPersisted(t *testing.T) {
	r, ctx := newCapabilityRepo(t)
	got, err := r.Upsert(ctx, repo.UpsertCapabilityInput{
		Name:          "obsidian.delete",
		Class:         repo.CapClassReach,
		EnforceableBy: []string{capability.EnforcerServer},
		Reversible:    false,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got.Reversible {
		t.Error("reversible must persist as false — it gates the auto-grant rule")
	}
}
