package repo_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newMemoryRepo(t *testing.T) (repo.MemoryRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewMemoryRepo(bundle.Client, bundle.WriteClient), context.Background()
}

// newMemoryRepoWithStageRun is newMemoryRepo plus a real stage_run row, for
// tests that exercise RecordInjection: stage_run_id must reference a real
// row (mustBeStageRun), so a made-up id like "stage-run-1" is no longer
// accepted.
func newMemoryRepoWithStageRun(t *testing.T) (repo.MemoryRepo, context.Context, string) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()
	taskID := createTask(t, repo.NewTaskRepo(bundle.Client), "memory-injection-task")
	run, err := repo.NewStageRunRepo(bundle.Client).Create(ctx, repo.CreateStageRunInput{
		TaskID:    taskID,
		Stage:     "backlog",
		Iteration: 1,
	})
	if err != nil {
		t.Fatalf("create stage run: %v", err)
	}
	return repo.NewMemoryRepo(bundle.Client, bundle.WriteClient), ctx, run.ID
}

func mustSpace(t *testing.T, r repo.MemoryRepo, ctx context.Context, slug string) *ent.Resource {
	t.Helper()
	space, err := r.CreateSpace(ctx, repo.CreateSpaceInput{
		Slug:  slug,
		Name:  slug,
		Scope: repo.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("create space: %v", err)
	}
	return space
}

func mustEntry(t *testing.T, r repo.MemoryRepo, ctx context.Context, spaceID, summary string) *ent.MemoryEntry {
	t.Helper()
	entry, err := r.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID:    spaceID,
		Summary:    summary,
		Content:    summary,
		Kind:       "fact",
		SourceKind: "user",
		Confidence: 1,
	})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	return entry
}

func TestListValidExcludesExpiredAndSuperseded(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")
	now := time.Now()
	past := now.Add(-time.Hour)

	live := mustEntry(t, r, ctx, space.ID, "still true")
	expired := mustEntry(t, r, ctx, space.ID, "no longer true")
	if err := r.ExpireEntry(ctx, expired.ID, past); err != nil {
		t.Fatalf("expire: %v", err)
	}
	old := mustEntry(t, r, ctx, space.ID, "replaced")
	replacement := mustEntry(t, r, ctx, space.ID, "replacement")
	if err := r.SupersedeEntry(ctx, old.ID, replacement.ID); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	got, err := r.ListValid(ctx, space.ID, now)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		ids[e.ID] = true
	}
	if !ids[live.ID] || !ids[replacement.ID] {
		t.Error("a live entry and a replacement must both be returned")
	}
	if ids[expired.ID] {
		t.Error("an expired entry must not be returned")
	}
	if ids[old.ID] {
		t.Error("a superseded entry must not be returned")
	}
}

func TestSupersedeDoesNotDelete(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")
	old := mustEntry(t, r, ctx, space.ID, "replaced")
	replacement := mustEntry(t, r, ctx, space.ID, "replacement")
	if err := r.SupersedeEntry(ctx, old.ID, replacement.ID); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	got, err := r.GetEntry(ctx, old.ID)
	if err != nil {
		t.Fatalf("the superseded row must still exist: %v", err)
	}
	if got.SupersededBy == nil || *got.SupersededBy != replacement.ID {
		t.Error("superseded_by must point at the replacement — the chain is the audit trail")
	}
}

func TestCreateSpaceIsIdempotent(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	first := mustSpace(t, r, ctx, "project-a")
	second, err := r.CreateSpace(ctx, repo.CreateSpaceInput{
		Slug:  "project-a",
		Name:  "renamed",
		Scope: repo.GlobalScope(),
	})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("creating a space with the same slug/scope created a second row: %s != %s", first.ID, second.ID)
	}
	if second.Name != "renamed" {
		t.Errorf("name = %q, want the refreshed value", second.Name)
	}
}

func TestGetSpaceAndListSpaces(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")

	got, err := r.GetSpace(ctx, repo.GlobalScope(), "project-a")
	if err != nil {
		t.Fatalf("get space: %v", err)
	}
	if got.ID != space.ID {
		t.Errorf("GetSpace returned a different row: %s != %s", got.ID, space.ID)
	}

	list, err := r.ListSpaces(ctx, repo.GlobalScope())
	if err != nil {
		t.Fatalf("list spaces: %v", err)
	}
	if len(list) != 1 || list[0].ID != space.ID {
		t.Errorf("ListSpaces = %v, want exactly the one created space", list)
	}
}

func TestGetSpaceByID(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")

	got, err := r.GetSpaceByID(ctx, space.ID)
	if err != nil {
		t.Fatalf("get space by id: %v", err)
	}
	if got.Slug != "project-a" {
		t.Errorf("GetSpaceByID returned slug %q, want %q", got.Slug, "project-a")
	}
}

func TestGetSpaceByIDRejectsNonSpaceResource(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	entry := mustEntry(t, r, ctx, mustSpace(t, r, ctx, "project-a").ID, "not a space")

	if _, err := r.GetSpaceByID(ctx, entry.ID); err == nil {
		t.Error("GetSpaceByID must refuse an id that is not a memory_space resource row")
	}
}

func TestCreateEntryRejectsUnknownSpace(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	_, err := r.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID:    "does-not-exist",
		Summary:    "orphan",
		Content:    "orphan",
		Kind:       "fact",
		SourceKind: "user",
		Confidence: 1,
	})
	if err == nil {
		t.Error("an entry referencing an unrecognised space_id must be refused, not written")
	}
}

func TestCreateEntryRejectsUnknownKind(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")
	_, err := r.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID:    space.ID,
		Summary:    "s",
		Content:    "c",
		Kind:       "wharrgarbl",
		SourceKind: "user",
		Confidence: 1,
	})
	if !errors.Is(err, repo.ErrInvalidKind) {
		t.Errorf("err = %v, want ErrInvalidKind — an unrecognised kind must be refused, not silently deranked forever at read time", err)
	}
}

func TestCreateEntryRejectsUnknownSourceKind(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")
	_, err := r.CreateEntry(ctx, repo.CreateEntryInput{
		SpaceID:    space.ID,
		Summary:    "s",
		Content:    "c",
		Kind:       "fact",
		SourceKind: "wharrgarbl",
		Confidence: 1,
	})
	if !errors.Is(err, repo.ErrInvalidSourceKind) {
		t.Errorf("err = %v, want ErrInvalidSourceKind", err)
	}
}

func TestSupersedeRejectsUnknownReplacement(t *testing.T) {
	r, ctx := newMemoryRepo(t)
	space := mustSpace(t, r, ctx, "project-a")
	old := mustEntry(t, r, ctx, space.ID, "replaced")

	if err := r.SupersedeEntry(ctx, old.ID, "does-not-exist"); err == nil {
		t.Error("superseding with a replacement that does not exist must be refused")
	}

	got, err := r.GetEntry(ctx, old.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if got.SupersededBy != nil {
		t.Error("a rejected supersede must not have written a dangling pointer")
	}
}

func TestRecordInjectionPersistsEntryIDs(t *testing.T) {
	r, ctx, stageRunID := newMemoryRepoWithStageRun(t)
	space := mustSpace(t, r, ctx, "project-a")
	entry := mustEntry(t, r, ctx, space.ID, "used in a spawn")

	got, err := r.RecordInjection(ctx, repo.RecordInjectionInput{
		StageRunID:     stageRunID,
		EntryIDs:       []string{entry.ID},
		CharBudget:     4000,
		CharsUsed:      120,
		CandidateCount: 3,
	})
	if err != nil {
		t.Fatalf("record injection: %v", err)
	}
	if len(got.EntryIds) != 1 || got.EntryIds[0] != entry.ID {
		t.Errorf("entry_ids = %v, want [%s]", got.EntryIds, entry.ID)
	}
	if got.CharBudget != 4000 || got.CharsUsed != 120 || got.CandidateCount != 3 {
		t.Errorf("counters not persisted as given: %+v", got)
	}
}

func TestListInjectionsByStageRunReturnsOnlyThatStageRun(t *testing.T) {
	r, ctx, stageRunID := newMemoryRepoWithStageRun(t)
	space := mustSpace(t, r, ctx, "project-a")
	entry := mustEntry(t, r, ctx, space.ID, "used in a spawn")

	_, err := r.RecordInjection(ctx, repo.RecordInjectionInput{
		StageRunID: stageRunID,
		EntryIDs:   []string{entry.ID},
		CharBudget: 4000, CharsUsed: 120, CandidateCount: 3,
	})
	if err != nil {
		t.Fatalf("record injection: %v", err)
	}

	got, err := r.ListInjectionsByStageRun(ctx, stageRunID)
	if err != nil {
		t.Fatalf("list injections: %v", err)
	}
	if len(got) != 1 || got[0].StageRunID != stageRunID {
		t.Errorf("ListInjectionsByStageRun = %v, want exactly the one injection for %s", got, stageRunID)
	}

	other, err := r.ListInjectionsByStageRun(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("list injections for unknown stage run: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("ListInjectionsByStageRun for an unrelated stage run = %v, want none", other)
	}
}

func TestRecordInjectionRejectsUnknownStageRun(t *testing.T) {
	r, ctx, _ := newMemoryRepoWithStageRun(t)
	space := mustSpace(t, r, ctx, "project-a")
	entry := mustEntry(t, r, ctx, space.ID, "used in a spawn")

	_, err := r.RecordInjection(ctx, repo.RecordInjectionInput{
		StageRunID: "does-not-exist",
		EntryIDs:   []string{entry.ID},
	})
	if err == nil {
		t.Error("a stage_run_id that does not reference a real stage run must be refused, not written")
	}
}

func TestRecordInjectionRejectsUnknownEntryID(t *testing.T) {
	r, ctx, stageRunID := newMemoryRepoWithStageRun(t)

	_, err := r.RecordInjection(ctx, repo.RecordInjectionInput{
		StageRunID: stageRunID,
		EntryIDs:   []string{"does-not-exist"},
	})
	if err == nil {
		t.Error("an entry id that does not reference a real memory entry must be refused, not written")
	}
}
