package serverapp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// A module's routine is created once and starts disabled: a module cannot know
// where the work belongs, and a routine firing against a guessed directory is
// worse than one waiting to be pointed somewhere. A second boot must not
// create it again, nor undo what the operator changed.
func TestRegisterModuleRoutines_CreatesOnceAndStartsDisabled(t *testing.T) {
	moduleDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "routines"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "routines", "daily.yaml"), []byte(
		"name: daily-sweep\ntitle: Daily sweep\ncronExpr: 0 9 * * *\n"), 0o600))

	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	schedules := repo.NewTaskScheduleRepo(bundle.Client)

	registry := plugin.New(t.TempDir())
	registry.InjectEntryWithDirForTest(plugin.Descriptor{
		Contract: plugin.CurrentContract, ID: "obsidian", Name: "Obsidian",
		Routines: []string{"routines/*.yaml"},
	}, moduleDir, true)

	registerModuleRoutines(t.Context(), registry, schedules)

	rows, err := schedules.ListForModule(t.Context(), "obsidian")
	require.NoError(t, err)
	require.Len(t, rows, 1, "the routine the module ships is created")
	assert.Equal(t, "daily-sweep", rows[0].Name)
	assert.False(t, rows[0].Enabled, "it waits for a directory before it fires")

	// The operator enables it; a second boot must leave that alone.
	_, err = schedules.SetEnabled(t.Context(), rows[0].ID, true)
	require.NoError(t, err)

	registerModuleRoutines(t.Context(), registry, schedules)

	rows, err = schedules.ListForModule(t.Context(), "obsidian")
	require.NoError(t, err)
	require.Len(t, rows, 1, "a second boot must not create it again")
	assert.True(t, rows[0].Enabled, "what the operator changed survives")
}
