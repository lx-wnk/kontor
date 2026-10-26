package repo_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/cloudsession"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newCloudSessionInput(sessionID string) repo.CreateCloudSessionInput {
	return repo.CreateCloudSessionInput{
		SessionID: sessionID,
		URL:       "https://claude.ai/code/" + sessionID,
		SpawnerID: "spawner-1",
		Title:     "Fix the build",
		Origin:    cloudsession.OriginStarted,
	}
}

func TestCloudSessionRepo_RoundTrip(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))

	created, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.Equal(t, "session_a", created.SessionID)
	require.Equal(t, "https://claude.ai/code/session_a", created.URL)
	require.Equal(t, "spawner-1", created.SpawnerID)
	require.Equal(t, "Fix the build", created.Title)
	require.Equal(t, cloudsession.OriginStarted, created.Origin)
	require.Nil(t, created.LastMessageAt)

	got, err := r.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
}

func TestCloudSessionRepo_ListNewestFirst(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))

	first, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)
	time.Sleep(2 * time.Millisecond)
	second, err := r.Create(t.Context(), newCloudSessionInput("session_b"))
	require.NoError(t, err)

	list, err := r.List(t.Context())
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, second.ID, list[0].ID)
	require.Equal(t, first.ID, list[1].ID)
}

func TestCloudSessionRepo_DuplicateSessionID(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))

	_, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)

	_, err = r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.ErrorIs(t, err, repo.ErrCloudSessionExists)
}

func TestCloudSessionRepo_RecordSendStoresError(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))
	created, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)

	at := time.Now().UTC().Truncate(time.Second)
	got, err := r.RecordSend(t.Context(), created.ID, "hello", at, "claude exited 1")
	require.NoError(t, err)
	require.Equal(t, "hello", got.LastMessage)
	require.NotNil(t, got.LastMessageAt)
	require.True(t, got.LastMessageAt.Equal(at))
	require.Equal(t, "claude exited 1", got.LastError)
}

func TestCloudSessionRepo_RecordSendEmptyErrorClears(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))
	created, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)

	_, err = r.RecordSend(t.Context(), created.ID, "first", time.Now(), "boom")
	require.NoError(t, err)

	got, err := r.RecordSend(t.Context(), created.ID, "second", time.Now(), "")
	require.NoError(t, err)
	require.Equal(t, "second", got.LastMessage)
	require.Empty(t, got.LastError)

	reread, err := r.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Empty(t, reread.LastError)
}

func TestCloudSessionRepo_Delete(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))
	created, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)

	require.NoError(t, r.Delete(t.Context(), created.ID))

	_, err = r.Get(t.Context(), created.ID)
	require.True(t, ent.IsNotFound(err))
}

func TestCloudSessionRepo_UnknownID(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))

	_, err := r.Get(t.Context(), "missing")
	require.True(t, ent.IsNotFound(err))
	require.True(t, ent.IsNotFound(r.Delete(t.Context(), "missing")))
	_, err = r.RecordSend(t.Context(), "missing", "m", time.Now(), "")
	require.True(t, ent.IsNotFound(err))
}

func TestCloudSessionRepo_RecordErrorLeavesMessageUntouched(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))
	created, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)
	at := time.Now().UTC().Truncate(time.Second)
	_, err = r.RecordSend(t.Context(), created.ID, "hello", at, "")
	require.NoError(t, err)

	got, err := r.RecordError(t.Context(), created.ID, "claude is not signed in")
	require.NoError(t, err)
	require.Equal(t, "claude is not signed in", got.LastError)
	require.Equal(t, "hello", got.LastMessage)
	require.NotNil(t, got.LastMessageAt)
	require.True(t, got.LastMessageAt.Equal(at))

	reread, err := r.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "claude is not signed in", reread.LastError)
	require.Equal(t, "hello", reread.LastMessage)
}

func TestCloudSessionRepo_RecordErrorUnknownID(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))

	_, err := r.RecordError(t.Context(), "missing", "boom")
	require.True(t, ent.IsNotFound(err))
}

func TestCloudSessionRepo_RecordSendCapsLastMessage(t *testing.T) {
	r := repo.NewCloudSessionRepo(openTestDB(t))
	created, err := r.Create(t.Context(), newCloudSessionInput("session_a"))
	require.NoError(t, err)

	long := strings.Repeat("ä", repo.MaxLastMessageRunes+10)
	got, err := r.RecordSend(t.Context(), created.ID, long, time.Now(), "")
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("ä", repo.MaxLastMessageRunes), got.LastMessage)
}
