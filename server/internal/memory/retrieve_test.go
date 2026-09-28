package memory_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// newRetriever wires a Retriever against a real in-memory SQLite database —
// FTS5 table, sync triggers and all — so these tests exercise the actual
// glue between an index hit and a resolved entry, not a stubbed candidate
// list.
func newRetriever(t *testing.T) (*memory.Retriever, repo.MemoryRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	memRepo := repo.NewMemoryRepo(bundle.Client, bundle.WriteClient)
	return memory.NewRetriever(bundle.DB, memRepo), memRepo, context.Background()
}

func mustSpace(t *testing.T, r repo.MemoryRepo, ctx context.Context, slug string, scope repo.Scope) string {
	t.Helper()
	space, err := r.CreateSpace(ctx, repo.CreateSpaceInput{Slug: slug, Name: slug, Scope: scope})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	return space.ID
}

func mustEntry(t *testing.T, r repo.MemoryRepo, ctx context.Context, spaceID, summary, content, kind string) string {
	t.Helper()
	e, err := r.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID:    spaceID,
		Summary:    summary,
		Content:    content,
		Kind:       kind,
		SourceKind: "user",
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	return e.ID
}

func TestRetrieveResolvesFTSHitsToRankedEntries(t *testing.T) {
	ret, memRepo, ctx := newRetriever(t)
	space := mustSpace(t, memRepo, ctx, "space-a", repo.GlobalScope())
	wantID := mustEntry(t, memRepo, ctx, space, "database migration runbook", "how to run a migration", "fact")
	mustEntry(t, memRepo, ctx, space, "unrelated entry", "nothing to do with the search term", "fact")

	got, err := ret.Retrieve(ctx, memory.Query{Text: "migration", Scope: repo.GlobalScope(), Limit: 10})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 1 || got[0].ID != wantID {
		t.Fatalf("got %+v, want exactly the migration entry", got)
	}
}

func TestRetrieveDropsEntrySupersededSinceTheIndexHit(t *testing.T) {
	ret, memRepo, ctx := newRetriever(t)
	space := mustSpace(t, memRepo, ctx, "space-b", repo.GlobalScope())
	oldID := mustEntry(t, memRepo, ctx, space, "onboarding checklist", "step one, step two", "fact")
	newID := mustEntry(t, memRepo, ctx, space, "onboarding checklist v2", "updated steps", "fact")

	q := memory.Query{Text: "onboarding", Scope: repo.GlobalScope(), Limit: 10}
	before, err := ret.Retrieve(ctx, q)
	if err != nil {
		t.Fatalf("retrieve before supersede: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("before supersede: got %d entries, want 2", len(before))
	}

	if err := memRepo.SupersedeEntry(ctx, oldID, newID); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	after, err := ret.Retrieve(ctx, q)
	if err != nil {
		t.Fatalf("retrieve after supersede: %v", err)
	}
	for _, e := range after {
		if e.ID == oldID {
			t.Error("a superseded entry must not be returned even though the FTS index still holds its old row")
		}
	}
	if len(after) != 1 || after[0].ID != newID {
		t.Fatalf("got %+v, want only the replacement entry", after)
	}
}

func TestRetrieveScopesToVisibleSpaces(t *testing.T) {
	ret, memRepo, ctx := newRetriever(t)
	globalSpace := mustSpace(t, memRepo, ctx, "global-space", repo.GlobalScope())
	projectSpace := mustSpace(t, memRepo, ctx, "project-space", repo.ProjectScope("/repo/a"))
	globalID := mustEntry(t, memRepo, ctx, globalSpace, "deploy checklist global", "applies everywhere", "fact")
	projectID := mustEntry(t, memRepo, ctx, projectSpace, "deploy checklist project", "applies to repo a only", "fact")

	globalOnly, err := ret.Retrieve(ctx, memory.Query{Text: "deploy", Scope: repo.GlobalScope(), Limit: 10})
	if err != nil {
		t.Fatalf("retrieve global: %v", err)
	}
	if len(globalOnly) != 1 || globalOnly[0].ID != globalID {
		t.Fatalf("global scope got %+v, want only the global entry", globalOnly)
	}

	both, err := ret.Retrieve(ctx, memory.Query{Text: "deploy", Scope: repo.ProjectScope("/repo/a"), Limit: 10})
	if err != nil {
		t.Fatalf("retrieve project: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range both {
		ids[e.ID] = true
	}
	if !ids[globalID] || !ids[projectID] {
		t.Fatalf("project scope must see both the global space and its own project space, got %+v", both)
	}
}

func TestRetrieveOnEmptyTextReturnsNoResultsWithoutErroring(t *testing.T) {
	ret, memRepo, ctx := newRetriever(t)
	space := mustSpace(t, memRepo, ctx, "space-c", repo.GlobalScope())
	mustEntry(t, memRepo, ctx, space, "anything", "anything", "fact")

	got, err := ret.Retrieve(ctx, memory.Query{Text: "   ", Scope: repo.GlobalScope(), Limit: 10})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries for a whitespace-only query, want 0", len(got))
	}
}
