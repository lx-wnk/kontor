package repo_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lx-wnk/agent-dashboard/server/internal/db"
	"github.com/lx-wnk/agent-dashboard/server/internal/db/repo"
)

func newResourceRepo(t *testing.T) (repo.ResourceRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewResourceRepo(bundle.Client), context.Background()
}

func TestResourceUpsertIsIdempotent(t *testing.T) {
	r, ctx := newResourceRepo(t)
	in := repo.UpsertResourceInput{
		Kind:  repo.ResourceKindApplication,
		Slug:  "obsidian",
		Name:  "Obsidian",
		Scope: repo.GlobalScope(),
		State: repo.ResourceStateInstalled,
	}

	first, err := r.Upsert(ctx, in)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	in.Name = "Obsidian Vault"
	second, err := r.Upsert(ctx, in)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("upsert created a second row: %s != %s", first.ID, second.ID)
	}
	if second.Name != "Obsidian Vault" {
		t.Errorf("name = %q, want the updated value", second.Name)
	}

	all, err := r.ListForKind(ctx, repo.ResourceKindApplication)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected exactly 1 row after two upserts, got %d", len(all))
	}
}

func TestResourceSameSlugInDifferentScopes(t *testing.T) {
	r, ctx := newResourceRepo(t)
	base := repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill,
		Slug: "review",
		Name: "Review",
	}

	base.Scope = repo.GlobalScope()
	if _, err := r.Upsert(ctx, base); err != nil {
		t.Fatalf("global upsert: %v", err)
	}
	base.Scope = repo.ProjectScope("/tmp/project-a")
	if _, err := r.Upsert(ctx, base); err != nil {
		t.Fatalf("project upsert: %v", err)
	}

	all, err := r.ListForKind(ctx, repo.ResourceKindSkill)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("same slug in two scopes must coexist, got %d rows", len(all))
	}
}

func TestResourceGetAndSetState(t *testing.T) {
	r, ctx := newResourceRepo(t)
	created, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindApplication,
		Slug:  "mail",
		Scope: repo.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created.State != repo.ResourceStateDiscovered {
		t.Errorf("default state = %q, want %q", created.State, repo.ResourceStateDiscovered)
	}

	if _, err := r.SetState(ctx, created.ID, repo.ResourceStateEnabled); err != nil {
		t.Fatalf("set state: %v", err)
	}
	got, err := r.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "mail")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != repo.ResourceStateEnabled {
		t.Errorf("state = %q, want %q", got.State, repo.ResourceStateEnabled)
	}
}

func TestResourceUpsertDoesNotResetState(t *testing.T) {
	r, ctx := newResourceRepo(t)
	created, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindApplication,
		Slug:  "obsidian",
		Name:  "Obsidian",
		Scope: repo.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if _, err := r.SetState(ctx, created.ID, repo.ResourceStateEnabled); err != nil {
		t.Fatalf("set state: %v", err)
	}

	// Re-discovery upsert with no State set (defaults to "discovered") must not
	// clobber the state that SetState established.
	if _, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindApplication,
		Slug:  "obsidian",
		Name:  "Obsidian",
		Scope: repo.GlobalScope(),
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := r.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "obsidian")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != repo.ResourceStateEnabled {
		t.Errorf("state = %q, want %q (upsert must not reset lifecycle state)", got.State, repo.ResourceStateEnabled)
	}
}

func TestResourceGetNormalizesScope(t *testing.T) {
	r, ctx := newResourceRepo(t)
	if _, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindRoutine,
		Slug:  "morning",
		Scope: repo.GlobalScope(),
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A project scope with an empty ref means global; Get must find the row.
	got, err := r.Get(ctx, repo.ResourceKindRoutine, repo.Scope{Kind: repo.ScopeProject, Ref: ""}, "morning")
	if err != nil {
		t.Fatalf("get with a collapsing scope: %v", err)
	}
	if got.Slug != "morning" {
		t.Errorf("slug = %q, want morning", got.Slug)
	}
}

func TestResourceRejectsInvalidSlug(t *testing.T) {
	r, ctx := newResourceRepo(t)
	_, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindApplication,
		Slug:  "Not A Slug",
		Scope: repo.GlobalScope(),
	})
	if err == nil {
		t.Fatal("an invalid slug must be rejected before it reaches the database")
	}
}

func TestResourceResolveFallsBackToGlobal(t *testing.T) {
	r, ctx := newResourceRepo(t)
	if _, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindSkill,
		Slug:  "review",
		Name:  "Global Review",
		Scope: repo.GlobalScope(),
	}); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	got, err := r.Resolve(ctx, repo.ResourceKindSkill, repo.ProjectScope("/tmp/project-a"), "review")
	if err != nil {
		t.Fatalf("Resolve with no project row must fall back: %v", err)
	}
	if got.Name != "Global Review" {
		t.Errorf("name = %q, want the global row", got.Name)
	}
}

func TestResourceResolvePrefersTheScopedRow(t *testing.T) {
	r, ctx := newResourceRepo(t)
	if _, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "review",
		Name: "Global Review", Scope: repo.GlobalScope(),
	}); err != nil {
		t.Fatalf("seed global: %v", err)
	}
	if _, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "review",
		Name: "Project Review", Scope: repo.ProjectScope("/tmp/project-a"),
	}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	got, err := r.Resolve(ctx, repo.ResourceKindSkill, repo.ProjectScope("/tmp/project-a"), "review")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Name != "Project Review" {
		t.Errorf("name = %q, want the project row to win", got.Name)
	}
}

func TestResourceGetDoesNotFallBack(t *testing.T) {
	r, ctx := newResourceRepo(t)
	if _, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "review", Scope: repo.GlobalScope(),
	}); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	if _, err := r.Get(ctx, repo.ResourceKindSkill, repo.ProjectScope("/tmp/project-a"), "review"); err == nil {
		t.Error("Get must not fall back to the global row — that is Resolve's job")
	}
}

func TestResourceListMergedScopedWins(t *testing.T) {
	r, ctx := newResourceRepo(t)
	for _, in := range []repo.UpsertResourceInput{
		{Kind: repo.ResourceKindSkill, Slug: "review", Name: "Global Review", Scope: repo.GlobalScope()},
		{Kind: repo.ResourceKindSkill, Slug: "deploy", Name: "Global Deploy", Scope: repo.GlobalScope()},
		{Kind: repo.ResourceKindSkill, Slug: "review", Name: "Project Review", Scope: repo.ProjectScope("/tmp/project-a")},
	} {
		if _, err := r.Upsert(ctx, in); err != nil {
			t.Fatalf("seed %s: %v", in.Slug, err)
		}
	}

	merged, err := r.ListMerged(ctx, repo.ResourceKindSkill, repo.ProjectScope("/tmp/project-a"))
	if err != nil {
		t.Fatalf("ListMerged: %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("merged length = %d, want 2 (review and deploy)", len(merged))
	}
	byName := map[string]string{}
	for _, row := range merged {
		byName[row.Slug] = row.Name
	}
	if byName["review"] != "Project Review" {
		t.Errorf("review = %q, want the project row to win", byName["review"])
	}
	if byName["deploy"] != "Global Deploy" {
		t.Errorf("deploy = %q, want the global row to survive", byName["deploy"])
	}
}

func TestResourceDeleteRefusesBuiltin(t *testing.T) {
	r, ctx := newResourceRepo(t)
	created, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:   repo.ResourceKindApplication,
		Slug:   "builtin-app",
		Scope:  repo.GlobalScope(),
		Origin: repo.ResourceOriginBuiltin,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	err = r.Delete(ctx, created.ID)
	if !errors.Is(err, repo.ErrResourceBuiltIn) {
		t.Fatalf("Delete of a builtin resource = %v, want ErrResourceBuiltIn", err)
	}

	if _, err := r.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "builtin-app"); err != nil {
		t.Errorf("refused delete must leave the row in place, got %v", err)
	}
}

func TestResourceDeleteAllowsLocal(t *testing.T) {
	r, ctx := newResourceRepo(t)
	created, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:   repo.ResourceKindApplication,
		Slug:   "local-app",
		Scope:  repo.GlobalScope(),
		Origin: repo.ResourceOriginLocal,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := r.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete of a local resource: %v", err)
	}
	if _, err := r.Get(ctx, repo.ResourceKindApplication, repo.GlobalScope(), "local-app"); err == nil {
		t.Error("row still present after a successful delete")
	}
}

// Three writers project rows into `application` (the plugin reconciler, the MCP
// application reconciler and the built-in apps), and a plugin manifest carries
// no required name, so a nameless row reached the cockpit and rendered as a
// blank line nobody could identify. The slug is always present, so it is what a
// row falls back to.
func TestResourceUpsertFallsBackToSlugWhenNameIsEmpty(t *testing.T) {
	r, ctx := newResourceRepo(t)

	res, err := r.Upsert(ctx, repo.UpsertResourceInput{
		Kind:  repo.ResourceKindApplication,
		Slug:  "voice-whisper",
		Name:  "",
		Scope: repo.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if res.Name != "voice-whisper" {
		t.Errorf("name = %q, want the slug as the fallback label", res.Name)
	}
}
