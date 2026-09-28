package repo_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/require"
)

// TestPermissionPresetRepo_UpsertNilPattern verifies that calling Upsert twice
// with an identical input that has a nil Pattern does NOT create duplicate rows.
//
// SQLite treats two NULL values as distinct in a UNIQUE index, so naive
// INSERT OR IGNORE semantics would silently create duplicates. The repo works
// around this with a manual existence check inside a transaction.
func TestPermissionPresetRepo_UpsertNilPattern(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewPermissionPresetRepo(client)

	input := repo.UpsertPresetInput{
		UserID:     nil,
		ProjectCwd: "/projects/myapp",
		Tool:       "Read",
		Pattern:    nil,
	}

	// First upsert — should insert.
	err := r.Upsert(context.Background(), input)
	require.NoError(t, err)

	// Second upsert — identical input; should be a no-op, not insert a second row.
	err = r.Upsert(context.Background(), input)
	require.NoError(t, err)

	// Verify exactly one row exists.
	summaries, err := r.ListSummaries(context.Background(), nil)
	require.NoError(t, err)

	var found int
	for _, s := range summaries {
		if s.ProjectCwd == input.ProjectCwd {
			for _, e := range s.Entries {
				if e.Tool == input.Tool && e.Pattern == nil {
					found++
				}
			}
		}
	}
	require.Equal(t, 1, found,
		"Upsert with nil Pattern must deduplicate: expected 1 row, got %d", found)
}

// TestPermissionPresetRepo_UpsertWithPattern verifies normal deduplication
// for the common case where Pattern is non-nil.
func TestPermissionPresetRepo_UpsertWithPattern(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewPermissionPresetRepo(client)
	userID := "user-123"

	input := repo.UpsertPresetInput{
		UserID:     &userID,
		ProjectCwd: "/projects/myapp",
		Tool:       "Bash",
		Pattern:    strPtr("git *"),
	}

	require.NoError(t, r.Upsert(context.Background(), input))
	require.NoError(t, r.Upsert(context.Background(), input))

	summaries, err := r.ListSummaries(context.Background(), &userID)
	require.NoError(t, err)

	var found int
	for _, s := range summaries {
		if s.ProjectCwd == input.ProjectCwd {
			for _, e := range s.Entries {
				if e.Tool == "Bash" {
					found++
				}
			}
		}
	}
	require.Equal(t, 1, found,
		"Upsert with non-nil Pattern must deduplicate: expected 1 row, got %d", found)
}

// TestPermissionPresetRepo_ListForCwd_GlobalUserScoping verifies that ListForCwd
// returns global presets (user_id IS NULL) for both nil and non-nil callers, and
// returns user-scoped presets only when the matching userID is provided.
func TestPermissionPresetRepo_ListForCwd_GlobalUserScoping(t *testing.T) {
	ctx := context.Background()
	client := openTestDB(t)
	r := repo.NewPermissionPresetRepo(client)

	cwd := "/projects/scoping"
	userID := "user-abc"
	otherUser := "user-xyz"

	// Global preset (no user_id).
	require.NoError(t, r.Upsert(ctx, repo.UpsertPresetInput{ProjectCwd: cwd, Tool: "Read"}))
	// User-scoped preset.
	require.NoError(t, r.Upsert(ctx, repo.UpsertPresetInput{UserID: &userID, ProjectCwd: cwd, Tool: "Bash", Pattern: strPtr("git *")}))
	// Preset for a different cwd — must not appear.
	require.NoError(t, r.Upsert(ctx, repo.UpsertPresetInput{ProjectCwd: "/other", Tool: "Write"}))

	// nil userID: only global preset.
	rows, err := r.ListForCwd(ctx, nil, cwd)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "Read", rows[0].Tool)

	// matching userID: global + user-scoped.
	rows, err = r.ListForCwd(ctx, &userID, cwd)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	// different userID: only global.
	rows, err = r.ListForCwd(ctx, &otherUser, cwd)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "Read", rows[0].Tool)
}

func strPtr(s string) *string { return &s }
