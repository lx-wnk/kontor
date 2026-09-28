package repo_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestReconcilePluginResourcesIsIdempotent(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	pluginRepo := repo.NewPluginRepo(bundle.Client)
	resourceRepo := repo.NewResourceRepo(bundle.Client)

	if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{
		ID:      "github-oauth",
		Name:    "GitHub OAuth",
		Version: "1.0.0",
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}

	n, err := repo.ReconcilePluginResources(ctx, resourceRepo, bundle.Client)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("first reconcile linked %d plugins, want 1", n)
	}

	again, err := repo.ReconcilePluginResources(ctx, resourceRepo, bundle.Client)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again != 0 {
		t.Errorf("second reconcile linked %d plugins, want 0 — it must be idempotent", again)
	}

	res, err := resourceRepo.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "github-oauth")
	if err != nil {
		t.Fatalf("registry row missing after reconcile: %v", err)
	}
	if res.OriginRef != "github-oauth" {
		t.Errorf("origin_ref = %q, want the manifest id", res.OriginRef)
	}

	rows, err := resourceRepo.ListForKind(ctx, repo.ResourceKindApplication)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected exactly 1 application resource, got %d", len(rows))
	}
}

func TestReconcilePluginResourcesSkipsOversizedIDButLinksTheRest(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	pluginRepo := repo.NewPluginRepo(bundle.Client)
	resourceRepo := repo.NewResourceRepo(bundle.Client)

	// The registry slug is capped at 64 chars, but the plugins table predates
	// that cap — a real database can hold a longer manifest id.
	oversizedID := strings.Repeat("a", 65)
	if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{
		ID:      oversizedID,
		Name:    "Oversized Plugin",
		Version: "1.0.0",
	}); err != nil {
		t.Fatalf("seed oversized plugin: %v", err)
	}
	if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{
		ID:      "valid-plugin",
		Name:    "Valid Plugin",
		Version: "1.0.0",
	}); err != nil {
		t.Fatalf("seed valid plugin: %v", err)
	}

	linked, err := repo.ReconcilePluginResources(ctx, resourceRepo, bundle.Client)
	if err != nil {
		t.Fatalf("reconcile must not fail because one plugin id is invalid: %v", err)
	}
	if linked != 1 {
		t.Errorf("linked = %d, want 1 (only the valid plugin)", linked)
	}

	if _, err := resourceRepo.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "valid-plugin"); err != nil {
		t.Errorf("valid plugin must still receive a registry identity: %v", err)
	}
}

func TestReconcilePluginResourcesDerivesStateFromPluginFields(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	pluginRepo := repo.NewPluginRepo(bundle.Client)
	resourceRepo := repo.NewResourceRepo(bundle.Client)

	seed := func(id string) {
		if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{
			ID:      id,
			Name:    id,
			Version: "1.0.0",
		}); err != nil {
			t.Fatalf("seed plugin %s: %v", id, err)
		}
	}

	seed("discovered-plugin")

	seed("disabled-plugin")
	installedAt := time.Now()
	if err := pluginRepo.SetInstalledAt(ctx, "disabled-plugin", &installedAt); err != nil {
		t.Fatalf("set installed_at: %v", err)
	}

	seed("enabled-plugin")
	enabledInstalledAt := time.Now()
	if err := pluginRepo.SetInstalledAt(ctx, "enabled-plugin", &enabledInstalledAt); err != nil {
		t.Fatalf("set installed_at: %v", err)
	}
	if err := pluginRepo.SetActive(ctx, "enabled-plugin", true); err != nil {
		t.Fatalf("set active: %v", err)
	}

	if _, err := repo.ReconcilePluginResources(ctx, resourceRepo, bundle.Client); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	cases := []struct {
		slug  string
		state string
	}{
		{"discovered-plugin", repo.ResourceStateDiscovered},
		{"disabled-plugin", repo.ResourceStateDisabled},
		{"enabled-plugin", repo.ResourceStateEnabled},
	}
	for _, c := range cases {
		res, err := resourceRepo.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), c.slug)
		if err != nil {
			t.Fatalf("registry row missing for %s: %v", c.slug, err)
		}
		if res.State != c.state {
			t.Errorf("%s: state = %q, want %q", c.slug, res.State, c.state)
		}
	}
}

// The projection linked a plugin to its registry identity once and then skipped
// it forever, so a manifest that gained a name (or a new version) never reached
// the registry row — it kept whatever the first boot happened to see. Five rows
// in the author's own database sat there with an empty name for that reason.
func TestReconcilePluginResourcesRefreshesALinkedRow(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	pluginRepo := repo.NewPluginRepo(bundle.Client)
	resourceRepo := repo.NewResourceRepo(bundle.Client)

	if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{ID: "voice-whisper", Version: "1.0.0"}); err != nil {
		t.Fatalf("seed nameless plugin: %v", err)
	}
	if _, err := repo.ReconcilePluginResources(ctx, resourceRepo, bundle.Client); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// The manifest gains a name, exactly as it does when the author fills it in.
	if _, err := pluginRepo.Upsert(ctx, repo.UpsertPluginInput{ID: "voice-whisper", Name: "Voice (Whisper)", Version: "1.1.0"}); err != nil {
		t.Fatalf("rename plugin: %v", err)
	}
	if _, err := repo.ReconcilePluginResources(ctx, resourceRepo, bundle.Client); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	res, err := resourceRepo.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "voice-whisper")
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if res.Name != "Voice (Whisper)" {
		t.Errorf("name = %q, want the refreshed manifest name", res.Name)
	}
	if res.Version != "1.1.0" {
		t.Errorf("version = %q, want the refreshed manifest version", res.Version)
	}
}
