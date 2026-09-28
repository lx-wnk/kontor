package repo_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newScheduleRepo(t *testing.T) (repo.TaskScheduleRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return repo.NewTaskScheduleRepo(bundle.Client), context.Background()
}

func createRoutine(t *testing.T, r repo.TaskScheduleRepo, ctx context.Context, name, owner string) string {
	t.Helper()
	row, err := r.Create(ctx, repo.CreateTaskScheduleInput{
		Name: name, CronExpr: "0 9 * * *", SlugPrefix: name, Title: name,
		Cwd: "/tmp", MaxIterations: 20,
		OwnerModule: owner,
	})
	require.NoError(t, err)
	return row.ID
}

// A routine a module created belongs to that module, and removing the module
// takes its routines out of service — but only its own. A module that could
// disable the operator's routines, or another module's, would be a far larger
// permission than "may bring routines along".
func TestDisableForModule_TouchesOnlyThatModulesRoutines(t *testing.T) {
	r, ctx := newScheduleRepo(t)

	mine := createRoutine(t, r, ctx, "mine", "obsidian")
	theirs := createRoutine(t, r, ctx, "theirs", "github")
	human := createRoutine(t, r, ctx, "human", "")

	n, err := r.DisableForModule(ctx, "obsidian")
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly the one routine that module owns")

	for id, wantEnabled := range map[string]bool{mine: false, theirs: true, human: true} {
		row, err := r.GetByID(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, wantEnabled, row.Enabled, "routine %s", row.Name)
	}
}

// An empty module id must not match every human-created routine, which all
// carry an empty owner.
func TestDisableForModule_RefusesAnEmptyModule(t *testing.T) {
	r, ctx := newScheduleRepo(t)
	human := createRoutine(t, r, ctx, "human", "")

	n, err := r.DisableForModule(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, 0, n, "an empty module owns nothing")

	row, err := r.GetByID(ctx, human)
	require.NoError(t, err)
	assert.True(t, row.Enabled, "a human's routine is untouched")
}
