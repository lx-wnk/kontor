package repo_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func openTestDB(t *testing.T) *ent.Client {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	return bundle.Client
}

func TestApiKeyRepo_CreateAndGetByHash(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewApiKeyRepo(client)

	key, err := r.Create(t.Context(), repo.CreateApiKeyInput{Name: "my-key", Hash: "deadbeef", Scopes: []string{"tasks:read"}})
	require.NoError(t, err)
	require.Equal(t, "my-key", key.Name)
	require.Equal(t, "deadbeef", key.KeyHash)

	got, err := r.GetByHash(t.Context(), "deadbeef")
	require.NoError(t, err)
	require.Equal(t, key.ID, got.ID)
}

func TestApiKeyRepo_List(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewApiKeyRepo(client)

	_, err := r.Create(t.Context(), repo.CreateApiKeyInput{Name: "k1", Hash: "hash1", Scopes: []string{"tasks:read"}})
	require.NoError(t, err)
	_, err = r.Create(t.Context(), repo.CreateApiKeyInput{Name: "k2", Hash: "hash2", Scopes: []string{"tasks:write"}})
	require.NoError(t, err)

	keys, err := r.List(t.Context())
	require.NoError(t, err)
	require.Len(t, keys, 2)
}

func TestApiKeyRepo_Delete(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewApiKeyRepo(client)

	key, err := r.Create(t.Context(), repo.CreateApiKeyInput{Name: "to-delete", Hash: "hashX"})
	require.NoError(t, err)

	require.NoError(t, r.Delete(t.Context(), key.ID))

	keys, err := r.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestApiKeyRepo_TouchLastUsed(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewApiKeyRepo(client)

	key, err := r.Create(t.Context(), repo.CreateApiKeyInput{Name: "track", Hash: "hashT"})
	require.NoError(t, err)
	require.Nil(t, key.LastUsedAt)

	require.NoError(t, r.TouchLastUsed(t.Context(), key.ID))

	got, err := r.GetByHash(t.Context(), "hashT")
	require.NoError(t, err)
	require.NotNil(t, got.LastUsedAt)
	require.WithinDuration(t, time.Now(), *got.LastUsedAt, 2*time.Second)
}

func TestApiKey_ExpiredKeyIsNotFoundByHash(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	past := time.Now().Add(-time.Minute)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "expired", Hash: "hash-expired", Scopes: []string{"memory:read"},
		Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-1", ExpiresAt: &past,
	})
	require.NoError(t, err)

	_, err = r.GetByHash(ctx, "hash-expired")
	require.Error(t, err, "an expired key must not resolve, the same way a revoked one does not")
}

func TestApiKey_UnexpiredStageRunKeyResolves(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	future := time.Now().Add(time.Hour)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "live", Hash: "hash-live", Scopes: []string{"memory:read"},
		Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-1", ExpiresAt: &future,
	})
	require.NoError(t, err)

	got, err := r.GetByHash(ctx, "hash-live")
	require.NoError(t, err)
	require.Equal(t, "sr-1", got.StageRunID)
	require.Equal(t, repo.ApiKeyKindStageRun, got.Kind)
}

func TestApiKey_UserKeyWithoutExpiryStillResolves(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	// Every row that existed before this change looks like this one.
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "human", Hash: "hash-human", Scopes: []string{"tasks:read"},
	})
	require.NoError(t, err)

	got, err := r.GetByHash(ctx, "hash-human")
	require.NoError(t, err)
	require.Equal(t, repo.ApiKeyKindUser, got.Kind, "the default kind must be user")
	require.Nil(t, got.ExpiresAt)
}

func TestApiKey_ListShowsOnlyUserKeys(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	_, err = r.Create(ctx, repo.CreateApiKeyInput{Name: "human", Hash: "h1", Scopes: []string{"tasks:read"}})
	require.NoError(t, err)
	future := time.Now().Add(time.Hour)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "sr", Hash: "h2", Scopes: []string{"memory:read"},
		Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-1", ExpiresAt: &future,
	})
	require.NoError(t, err)

	keys, err := r.List(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 1, "an ephemeral key must not appear in the human-facing list")
	require.Equal(t, "human", keys[0].Name)
}

func TestApiKey_RevokeForStageRun(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	future := time.Now().Add(time.Hour)
	for _, h := range []string{"a", "b"} {
		_, err = r.Create(ctx, repo.CreateApiKeyInput{
			Name: h, Hash: h, Scopes: []string{"memory:read"},
			Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-1", ExpiresAt: &future,
		})
		require.NoError(t, err)
	}
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "other", Hash: "c", Scopes: []string{"memory:read"},
		Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-2", ExpiresAt: &future,
	})
	require.NoError(t, err)

	n, err := r.RevokeForStageRun(ctx, "sr-1")
	require.NoError(t, err)
	require.Equal(t, 2, n)

	_, err = r.GetByHash(ctx, "a")
	require.Error(t, err)
	_, err = r.GetByHash(ctx, "c")
	require.NoError(t, err, "another stage run's key must survive")
}

// A user key carries stage_run_id = "" like every other user key, so an
// empty stageRunID must never reach the update — it would deactivate every
// user key in the table. stagekey.go already guards its one caller, but the
// guarantee belongs at the repo boundary too.
func TestApiKey_RevokeForStageRun_EmptyIDTouchesNothing(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	_, err = r.Create(ctx, repo.CreateApiKeyInput{Name: "human-1", Hash: "u1", Scopes: []string{"tasks:read"}})
	require.NoError(t, err)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{Name: "human-2", Hash: "u2", Scopes: []string{"tasks:read"}})
	require.NoError(t, err)

	n, err := r.RevokeForStageRun(ctx, "")
	require.NoError(t, err)
	require.Zero(t, n, "an empty stageRunID must touch no rows")

	_, err = r.GetByHash(ctx, "u1")
	require.NoError(t, err, "a user key must survive a call with no stage run id")
	_, err = r.GetByHash(ctx, "u2")
	require.NoError(t, err, "a user key must survive a call with no stage run id")
}

func TestApiKey_DeleteExpiredRemovesOnlyEphemeralRows(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "old", Hash: "old", Scopes: []string{"memory:read"},
		Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-1", ExpiresAt: &past,
	})
	require.NoError(t, err)
	// The user key carries an expiry in the past too, so `kind` is the only
	// thing standing between it and the sweep. With expires_at = NULL it was
	// protected by the ExpiresAtNotNil clause instead, and dropping the kind
	// filter altogether left this test green.
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "human", Hash: "human", Scopes: []string{"tasks:read"},
		ExpiresAt: &past,
	})
	require.NoError(t, err)

	n, err := r.DeleteExpired(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, n, "only the stage_run key may be swept")

	keys, err := r.List(ctx)
	require.NoError(t, err)
	require.Len(t, keys, 1, "a user key must never be swept, whatever its expiry")
}

// TestApiKey_DeleteExpiredSparesAKeyExpiringExactlyAtTheCutoff pins the
// sweep's boundary as exclusive: expires_at == before means the key's last
// moment is now, not that it is already gone. `before` is a parameter, so the
// equality case is reachable here — unlike GetByHash, which compares against
// its own internal time.Now() and therefore has no reachable equality case
// without a clock seam in production code.
func TestApiKey_DeleteExpiredSparesAKeyExpiringExactlyAtTheCutoff(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	r := repo.NewApiKeyRepo(bundle.Client)
	ctx := context.Background()

	cutoff := time.Now().Truncate(time.Second)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{
		Name: "on-the-boundary", Hash: "boundary", Scopes: []string{"memory:read"},
		Kind: repo.ApiKeyKindStageRun, StageRunID: "sr-boundary", ExpiresAt: &cutoff,
	})
	require.NoError(t, err)

	n, err := r.DeleteExpired(ctx, cutoff)
	require.NoError(t, err)
	require.Zero(t, n, "a key expiring exactly at the cutoff is not yet expired")

	// A cutoff one nanosecond later is past it, which also proves the row was
	// findable all along and the zero above is a boundary result, not a miss.
	n, err = r.DeleteExpired(ctx, cutoff.Add(time.Nanosecond))
	require.NoError(t, err)
	require.Equal(t, 1, n, "a cutoff past the expiry must sweep the key")
}

func TestApiKey_KontorSessionLifecycle(t *testing.T) {
	r := repo.NewApiKeyRepo(openTestDB(t))
	ctx := t.Context()

	none, err := r.ActiveKontorSession(ctx)
	require.NoError(t, err)
	require.Nil(t, none)

	k, err := r.Create(ctx, repo.CreateApiKeyInput{Name: "kontor-session", Hash: "ks", Scopes: []string{"tasks:read"}, Kind: repo.ApiKeyKindKontorSession})
	require.NoError(t, err)
	require.Nil(t, k.SessionPid)
	require.NoError(t, r.SetSessionPID(ctx, k.ID, 4242))

	got, err := r.ActiveKontorSession(ctx)
	require.NoError(t, err)
	require.Equal(t, k.ID, got.ID)
	require.Equal(t, 4242, *got.SessionPid)

	n, err := r.RevokeKontorSessions(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err = r.ActiveKontorSession(ctx)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestApiKey_RevokeKontorSessionsSparesOtherKinds(t *testing.T) {
	r := repo.NewApiKeyRepo(openTestDB(t))
	ctx := t.Context()
	_, err := r.Create(ctx, repo.CreateApiKeyInput{Name: "u", Hash: "user"})
	require.NoError(t, err)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{Name: "k", Hash: "kontor", Kind: repo.ApiKeyKindKontorSession})
	require.NoError(t, err)

	n, err := r.RevokeKontorSessions(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = r.GetByHash(ctx, "user")
	require.NoError(t, err, "a user key must survive ending the session")
}

// Down path: ent auto-migrate only adds columns, so a rollback is the manual
// DROP COLUMN below. The test proves it is one statement (no index or
// constraint references the column) and that the next boot re-adds it.
func TestApiKey_SessionPidColumnDownPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kontor.db")
	b, err := db.Open(path)
	require.NoError(t, err)
	k, err := repo.NewApiKeyRepo(b.Client).Create(t.Context(), repo.CreateApiKeyInput{Name: "u", Hash: "h"})
	require.NoError(t, err)
	require.NoError(t, b.Close())

	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE api_keys DROP COLUMN session_pid`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	b, err = db.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	got, err := repo.NewApiKeyRepo(b.Client).GetByID(t.Context(), k.ID)
	require.NoError(t, err)
	require.Nil(t, got.SessionPid)
}
