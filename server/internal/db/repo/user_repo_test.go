package repo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestUserRepo_UpsertAndGet(t *testing.T) {
	client := openTestDB(t) // defined in api_key_repo_test.go
	r := repo.NewUserRepo(client)

	user, err := r.Upsert(t.Context(), repo.ProviderUserInfo{
		ID:          "123456",
		Login:       "octocat",
		DisplayName: "The Octocat",
		AvatarURL:   "https://example.com/avatar.png",
	})
	require.NoError(t, err)
	require.Equal(t, "123456", user.ID)
	require.Equal(t, "octocat", user.ProviderLogin)

	got, err := r.GetByID(t.Context(), "123456")
	require.NoError(t, err)
	require.Equal(t, user.ID, got.ID)
}

func TestUserRepo_Upsert_UpdatesLogin(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewUserRepo(client)

	_, err := r.Upsert(t.Context(), repo.ProviderUserInfo{ID: "7", Login: "oldname"})
	require.NoError(t, err)

	updated, err := r.Upsert(t.Context(), repo.ProviderUserInfo{ID: "7", Login: "newname"})
	require.NoError(t, err)
	require.Equal(t, "newname", updated.ProviderLogin)
}

func TestUserRepo_GetByID_NotFound(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewUserRepo(client)

	_, err := r.GetByID(t.Context(), "nonexistent")
	require.Error(t, err)
}
