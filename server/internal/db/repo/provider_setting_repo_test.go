package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestProviderSettingRepo_UpsertAndList(t *testing.T) {
	client := openDB(t)
	r := repo.NewProviderSettingRepo(client)
	ctx := context.Background()

	if _, err := r.Upsert(ctx, "codex", true); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upsert(ctx, "codex", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upsert(ctx, "junie", true); err != nil {
		t.Fatal(err)
	}

	list, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 rows (codex updated, junie new), got %d", len(list))
	}
	got := map[string]bool{}
	for _, ps := range list {
		got[ps.ProviderID] = ps.Enabled
	}
	if got["codex"] != false || got["junie"] != true {
		t.Fatalf("unexpected state: %v", got)
	}
}
