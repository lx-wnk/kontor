package repo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func ptr(s string) *string { return &s }

func TestProjectRepo_CreateAndGet(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)

	desc := "test project"
	color := "#ff00ff"
	p, err := r.Create(t.Context(), "Alpha", "alpha", &desc, &color, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "Alpha", p.Name)
	require.Equal(t, "alpha", p.Slug)
	require.NotNil(t, p.Description)
	require.Equal(t, "test project", *p.Description)
	require.NotNil(t, p.Color)
	require.Equal(t, "#ff00ff", *p.Color)
	require.Nil(t, p.DefaultSpawnerID)

	gotByID, err := r.GetByID(t.Context(), p.ID)
	require.NoError(t, err)
	require.Equal(t, p.ID, gotByID.ID)

	gotBySlug, err := r.GetBySlug(t.Context(), "alpha")
	require.NoError(t, err)
	require.Equal(t, p.ID, gotBySlug.ID)
}

func TestProjectRepo_SlugUniqueness(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)

	_, err := r.Create(t.Context(), "First", "dup", nil, nil, nil, nil)
	require.NoError(t, err)

	_, err = r.Create(t.Context(), "Second", "dup", nil, nil, nil, nil)
	require.Error(t, err, "expected unique-constraint failure on duplicate slug")
}

func TestProjectRepo_ListAndListWithFolderCount(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)
	fr := repo.NewProjectFolderRepo(client)

	p1, err := r.Create(t.Context(), "A", "a", nil, nil, nil, nil)
	require.NoError(t, err)
	p2, err := r.Create(t.Context(), "B", "b", nil, nil, nil, nil)
	require.NoError(t, err)

	// Two folders for p1, none for p2.
	_, err = fr.Create(t.Context(), p1.ID, "/tmp/one", ptr("one"), true)
	require.NoError(t, err)
	_, err = fr.Create(t.Context(), p1.ID, "/tmp/two", ptr("two"), false)
	require.NoError(t, err)

	all, err := r.List(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 2)

	withCount, err := r.ListWithFolderCount(t.Context())
	require.NoError(t, err)
	require.Len(t, withCount, 2)

	counts := map[string]int{}
	for _, pwc := range withCount {
		counts[pwc.ID] = pwc.FolderCount
	}
	require.Equal(t, 2, counts[p1.ID])
	require.Equal(t, 0, counts[p2.ID])
}

func TestProjectRepo_ListByIDs(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)

	p1, err := r.Create(t.Context(), "A", "a", nil, nil, nil, nil)
	require.NoError(t, err)
	_, err = r.Create(t.Context(), "B", "b", nil, nil, nil, nil)
	require.NoError(t, err)

	got, err := r.ListByIDs(t.Context(), []string{p1.ID, "missing"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, p1.ID, got[0].ID)

	none, err := r.ListByIDs(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

func TestProjectRepo_Update(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)

	desc := "initial"
	color := "#aaa"
	p, err := r.Create(t.Context(), "Gamma", "gamma", &desc, &color, nil, nil)
	require.NoError(t, err)

	t.Run("set name and slug", func(t *testing.T) {
		updated, err := r.Update(t.Context(), p.ID, ptr("Gamma2"), ptr("gamma2"), nil, nil, nil, nil, false, false, false, false)
		require.NoError(t, err)
		require.Equal(t, "Gamma2", updated.Name)
		require.Equal(t, "gamma2", updated.Slug)
	})

	t.Run("set description and color", func(t *testing.T) {
		newDesc := "updated"
		newColor := "#bbb"
		updated, err := r.Update(t.Context(), p.ID, nil, nil, &newDesc, &newColor, nil, nil, false, false, false, false)
		require.NoError(t, err)
		require.Equal(t, "updated", *updated.Description)
		require.Equal(t, "#bbb", *updated.Color)
	})

	t.Run("clear description", func(t *testing.T) {
		updated, err := r.Update(t.Context(), p.ID, nil, nil, nil, nil, nil, nil, true, false, false, false)
		require.NoError(t, err)
		require.Nil(t, updated.Description)
	})

	t.Run("clear color", func(t *testing.T) {
		updated, err := r.Update(t.Context(), p.ID, nil, nil, nil, nil, nil, nil, false, true, false, false)
		require.NoError(t, err)
		require.Nil(t, updated.Color)
	})

	t.Run("set and clear defaultSpawnerID", func(t *testing.T) {
		sr := repo.NewSpawnerRepo(client)
		s, err := sr.Create(t.Context(), "S", "s-update", "claude", nil, nil, nil, nil, "", nil, false)
		require.NoError(t, err)

		updated, err := r.Update(t.Context(), p.ID, nil, nil, nil, nil, &s.ID, nil, false, false, false, false)
		require.NoError(t, err)
		require.NotNil(t, updated.DefaultSpawnerID)
		require.Equal(t, s.ID, *updated.DefaultSpawnerID)

		cleared, err := r.Update(t.Context(), p.ID, nil, nil, nil, nil, nil, nil, false, false, true, false)
		require.NoError(t, err)
		require.Nil(t, cleared.DefaultSpawnerID)
	})
}

func TestProjectRepo_SetupCommandRoundTrip(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)

	cmd := "pnpm install"
	p, err := r.Create(t.Context(), "Test", "test-proj", nil, nil, nil, &cmd)
	require.NoError(t, err)
	require.NotNil(t, p.SetupCommand)
	require.Equal(t, "pnpm install", *p.SetupCommand)

	updated, err := r.Update(t.Context(), p.ID, nil, nil, nil, nil, nil, nil, false, false, false, true)
	require.NoError(t, err)
	require.Nil(t, updated.SetupCommand)
}

func TestProjectRepo_Delete(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewProjectRepo(client)

	p, err := r.Create(t.Context(), "Bye", "bye", nil, nil, nil, nil)
	require.NoError(t, err)

	require.NoError(t, r.Delete(t.Context(), p.ID))

	_, err = r.GetByID(t.Context(), p.ID)
	require.Error(t, err)
}
