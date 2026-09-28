package repo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestProjectFolderRepo_CreateAndList(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "FP", "fp", nil, nil, nil, nil)
	require.NoError(t, err)

	f1, err := r.Create(t.Context(), p.ID, "/tmp/a", ptr("alpha"), false)
	require.NoError(t, err)
	require.Equal(t, "/tmp/a", f1.Path)
	require.False(t, f1.IsDefault)

	f2, err := r.Create(t.Context(), p.ID, "/tmp/b", ptr("beta"), true)
	require.NoError(t, err)
	require.True(t, f2.IsDefault)

	list, err := r.ListByProject(t.Context(), p.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestProjectFolderRepo_PathUniquenessPerProject(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "U", "u", nil, nil, nil, nil)
	require.NoError(t, err)

	_, err = r.Create(t.Context(), p.ID, "/tmp/same", ptr("first"), false)
	require.NoError(t, err)

	_, err = r.Create(t.Context(), p.ID, "/tmp/same", ptr("second"), false)
	require.Error(t, err, "expected UNIQUE(project_id, path) violation")
}

func TestProjectFolderRepo_OnlyOneDefaultOnCreate(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "D", "d", nil, nil, nil, nil)
	require.NoError(t, err)

	f1, err := r.Create(t.Context(), p.ID, "/tmp/1", ptr("one"), false)
	require.NoError(t, err)
	require.False(t, f1.IsDefault)

	f2, err := r.Create(t.Context(), p.ID, "/tmp/2", ptr("two"), true)
	require.NoError(t, err)
	require.True(t, f2.IsDefault)

	// Third creation with isDefault=true must flip f2 to false.
	f3, err := r.Create(t.Context(), p.ID, "/tmp/3", ptr("three"), true)
	require.NoError(t, err)
	require.True(t, f3.IsDefault)

	// Verify f1 stayed false, f2 is now false.
	f1After, err := r.GetByID(t.Context(), f1.ID)
	require.NoError(t, err)
	require.False(t, f1After.IsDefault)

	f2After, err := r.GetByID(t.Context(), f2.ID)
	require.NoError(t, err)
	require.False(t, f2After.IsDefault, "previous default should have been flipped to false")

	f3After, err := r.GetByID(t.Context(), f3.ID)
	require.NoError(t, err)
	require.True(t, f3After.IsDefault)
}

func TestProjectFolderRepo_UpdateSetsDefaultExclusively(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "DU", "du", nil, nil, nil, nil)
	require.NoError(t, err)

	a, err := r.Create(t.Context(), p.ID, "/tmp/x", ptr("x"), true)
	require.NoError(t, err)
	b, err := r.Create(t.Context(), p.ID, "/tmp/y", ptr("y"), false)
	require.NoError(t, err)

	tru := true
	updated, err := r.Update(t.Context(), b.ID, nil, nil, false, &tru)
	require.NoError(t, err)
	require.True(t, updated.IsDefault)

	aAfter, err := r.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.False(t, aAfter.IsDefault, "previous default should have been flipped to false")
}

func TestProjectFolderRepo_UpdatePathAndLabel(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "UP", "up", nil, nil, nil, nil)
	require.NoError(t, err)

	f, err := r.Create(t.Context(), p.ID, "/tmp/initial", ptr("init"), false)
	require.NoError(t, err)

	newPath := "/tmp/changed"
	newLabel := "changed"
	updated, err := r.Update(t.Context(), f.ID, &newPath, &newLabel, false, nil)
	require.NoError(t, err)
	require.Equal(t, "/tmp/changed", updated.Path)
	require.NotNil(t, updated.Label)
	require.Equal(t, "changed", *updated.Label)

	cleared, err := r.Update(t.Context(), f.ID, nil, nil, true, nil)
	require.NoError(t, err)
	require.Nil(t, cleared.Label)
}

func TestProjectFolderRepo_Delete(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "DL", "dl", nil, nil, nil, nil)
	require.NoError(t, err)

	f, err := r.Create(t.Context(), p.ID, "/tmp/dl", ptr("dl"), false)
	require.NoError(t, err)

	require.NoError(t, r.Delete(t.Context(), f.ID))

	list, err := r.ListByProject(t.Context(), p.ID)
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestProjectFolderRepo_Suggest(t *testing.T) {
	client := openTestDB(t)
	pr := repo.NewProjectRepo(client)
	r := repo.NewProjectFolderRepo(client)

	p, err := pr.Create(t.Context(), "SG", "sg", nil, nil, nil, nil)
	require.NoError(t, err)

	_, err = r.Create(t.Context(), p.ID, "/tmp/z", ptr("z"), false)
	require.NoError(t, err)
	_, err = r.Create(t.Context(), p.ID, "/tmp/a", ptr("a"), false)
	require.NoError(t, err)
	_, err = r.Create(t.Context(), p.ID, "/tmp/m", ptr("m"), true)
	require.NoError(t, err)

	suggestions, err := r.Suggest(t.Context(), p.ID)
	require.NoError(t, err)
	require.Len(t, suggestions, 3)
	// Default folder must be first.
	require.True(t, suggestions[0].IsDefault)
	require.Equal(t, "/tmp/m", suggestions[0].Path)
	// Remaining ordered by label asc.
	require.NotNil(t, suggestions[1].Label)
	require.Equal(t, "a", *suggestions[1].Label)
	require.NotNil(t, suggestions[2].Label)
	require.Equal(t, "z", *suggestions[2].Label)
}
