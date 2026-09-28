package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func openDBBundle(t *testing.T) (*ent.Client, repo.ResourceRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle.Client, repo.NewResourceRepo(bundle.Client)
}

func createScheduleRaw(t *testing.T, client *ent.Client, name string, enabled bool) *ent.TaskSchedule {
	t.Helper()
	s, err := client.TaskSchedule.Create().
		SetID("sched-" + name).
		SetName(name).
		SetCronExpr("0 9 * * *").
		SetSlugPrefix(name).
		SetTitle(name).
		SetCwd("/tmp").
		SetMaxIterations(20).
		SetStageTimeoutSeconds(1800).
		SetEnabled(enabled).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create schedule %q: %v", name, err)
	}
	return s
}

func TestReconcileScheduleResources_LinksUnlinked(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	createScheduleRaw(t, client, "alpha", true)
	createScheduleRaw(t, client, "beta", false)

	linked, err := repo.ReconcileScheduleResources(ctx, resources, client)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if linked != 2 {
		t.Fatalf("expected 2 linked, got %d", linked)
	}

	// Verify resource rows exist
	rows, err := resources.ListForKind(ctx, repo.ResourceKindRoutine)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(rows))
	}

	// Verify backlinks
	s, _ := client.TaskSchedule.Get(ctx, "sched-alpha")
	if s.ResourceID == "" {
		t.Fatal("alpha resource_id not set")
	}
}

func TestReconcileScheduleResources_Idempotent(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	createScheduleRaw(t, client, "gamma", true)

	first, err := repo.ReconcileScheduleResources(ctx, resources, client)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if first != 1 {
		t.Fatalf("expected 1 on first run, got %d", first)
	}

	second, err := repo.ReconcileScheduleResources(ctx, resources, client)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if second != 0 {
		t.Fatalf("expected 0 on second run (idempotent), got %d", second)
	}

	// Resource count unchanged
	rows, _ := resources.ListForKind(ctx, repo.ResourceKindRoutine)
	if len(rows) != 1 {
		t.Fatalf("expected 1 resource after two reconciles, got %d", len(rows))
	}
}

func TestUpsertScheduleResource_EnabledState(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	s := createScheduleRaw(t, client, "enabled-test", true)
	resID, err := repo.UpsertScheduleResource(ctx, resources, client, s)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if res.ID != resID {
		t.Fatalf("resource ID mismatch: %s != %s", res.ID, resID)
	}
	if res.State != repo.ResourceStateEnabled {
		t.Fatalf("expected enabled, got %s", res.State)
	}
	if res.Kind != repo.ResourceKindRoutine {
		t.Fatalf("expected routine kind, got %s", res.Kind)
	}
	if res.OriginRef != s.ID {
		t.Fatalf("origin_ref mismatch: %s != %s", res.OriginRef, s.ID)
	}
}

func TestUpsertScheduleResource_DisabledState(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	s := createScheduleRaw(t, client, "disabled-test", false)
	_, err := repo.UpsertScheduleResource(ctx, resources, client, s)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if res.State != repo.ResourceStateDisabled {
		t.Fatalf("expected disabled, got %s", res.State)
	}
}

func TestOrphanScheduleResource_SetsOrphaned(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	s := createScheduleRaw(t, client, "orphan-test", true)
	resID, err := repo.UpsertScheduleResource(ctx, resources, client, s)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := repo.OrphanScheduleResource(ctx, resources, resID); err != nil {
		t.Fatalf("orphan: %v", err)
	}

	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if res.State != repo.ResourceStateOrphaned {
		t.Fatalf("expected orphaned, got %s", res.State)
	}
}

func TestReconcileScheduleResources_SkipsAlreadyLinked(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	s := createScheduleRaw(t, client, "prelinked", true)
	// Manually link it
	resID, err := repo.UpsertScheduleResource(ctx, resources, client, s)
	if err != nil {
		t.Fatalf("pre-link: %v", err)
	}
	_ = resID

	// Reconcile should find 0 unlinked
	linked, err := repo.ReconcileScheduleResources(ctx, resources, client)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if linked != 0 {
		t.Fatalf("expected 0 (already linked), got %d", linked)
	}
}

// The twin of TestReconcilePluginResourcesRefreshesALinkedRow: this projection
// mirrors the plugin one, including the filter that skipped already-linked rows,
// so a renamed routine kept its old label in the registry forever.
func TestReconcileScheduleResourcesRefreshesALinkedRow(t *testing.T) {
	client, resources := openDBBundle(t)
	ctx := context.Background()

	s := createScheduleRaw(t, client, "nightly", true)
	if _, err := repo.ReconcileScheduleResources(ctx, resources, client); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	if err := client.TaskSchedule.UpdateOneID(s.ID).SetName("Nightly sweep").Exec(ctx); err != nil {
		t.Fatalf("rename schedule: %v", err)
	}
	if _, err := repo.ReconcileScheduleResources(ctx, resources, client); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	res, err := resources.Get(ctx, repo.ResourceKindRoutine, repo.GlobalScope(), s.ID)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if res.Name != "Nightly sweep" {
		t.Errorf("name = %q, want the refreshed schedule name", res.Name)
	}
}
