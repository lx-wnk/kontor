package settings

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

type fakeRepo struct {
	values map[string]string
	nonces map[string]string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{values: map[string]string{}, nonces: map[string]string{}}
}

func (f *fakeRepo) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := f.values[k]
	return v, ok, nil
}
func (f *fakeRepo) Set(_ context.Context, k, v string) error { f.values[k] = v; return nil }
func (f *fakeRepo) SetSecret(_ context.Context, k, ciphertext, nonce string) error {
	f.values[k] = ciphertext
	f.nonces[k] = nonce
	return nil
}
func (f *fakeRepo) GetSecret(_ context.Context, k string) (string, string, bool, error) {
	v, ok := f.values[k]
	if !ok {
		return "", "", false, nil
	}
	return v, f.nonces[k], true, nil
}
func (f *fakeRepo) ListAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out, nil
}

func TestService_DefaultsAndTypedAccess(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	require.NoError(t, svc.Load(context.Background()))

	// registry default when no DB row
	assert.Equal(t, 5, svc.Int("spawn.rateLimit"))
	assert.True(t, svc.Bool("worktree.force"))
	assert.Equal(t, "none", svc.String("auth.mode"))
	assert.Empty(t, svc.StringSlice("spawn.allowedCommands"))

	// Set persists + updates snapshot, validated
	require.NoError(t, svc.Set(context.Background(), "worktree.force", "true"))
	assert.True(t, svc.Bool("worktree.force"))

	// invalid value rejected
	require.Error(t, svc.Set(context.Background(), "spawn.rateLimit", "abc"))
	// unknown key rejected
	require.Error(t, svc.Set(context.Background(), "nope", "1"))

	// stringSlice round-trips
	require.NoError(t, svc.Set(context.Background(), "spawn.allowedCommands", "voice-whisper,voice-webspeech"))
	assert.Equal(t, []string{"voice-whisper", "voice-webspeech"}, svc.StringSlice("spawn.allowedCommands"))
}

func TestService_SecretRoundTripAndMasking(t *testing.T) {
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	repo := newFakeRepo()
	svc := New(repo, box)

	require.NoError(t, svc.Load(t.Context()))

	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "sk-live-123"))

	// Stored ciphertext must not be the plaintext.
	assert.NotEqual(t, "sk-live-123", repo.values["obsidian.apiKey"])
	assert.NotEmpty(t, repo.nonces["obsidian.apiKey"])

	// Reading it back decrypts.
	got, err := svc.Secret(t.Context(), "obsidian.apiKey")
	require.NoError(t, err)
	assert.Equal(t, "sk-live-123", got)

	// Every non-decrypting read path masks.
	assert.Equal(t, secretbox.MaskedSentinel, svc.String("obsidian.apiKey"))
	assert.Equal(t, secretbox.MaskedSentinel, svc.Effective()["obsidian.apiKey"])

	// Re-submitting the mask leaves the stored value untouched, so a UI that
	// round-trips what it was shown cannot overwrite the real secret.
	before := repo.values["obsidian.apiKey"]
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", secretbox.MaskedSentinel))
	assert.Equal(t, before, repo.values["obsidian.apiKey"])
}

func TestService_LoadMasksAPreExistingSecretRow(t *testing.T) {
	// Simulate a ciphertext row already sitting in the repo from a previous
	// process (e.g. across a restart) — never written via Set in this test,
	// so only Load's own masking loop can be responsible for the mask below.
	repo := newFakeRepo()
	repo.values["obsidian.apiKey"] = "ciphertext-from-before-restart"
	repo.nonces["obsidian.apiKey"] = "nonce-from-before-restart"

	svc := New(repo, nil)
	require.NoError(t, svc.Load(t.Context()))

	assert.Equal(t, secretbox.MaskedSentinel, svc.String("obsidian.apiKey"))
	assert.Equal(t, secretbox.MaskedSentinel, svc.Effective()["obsidian.apiKey"])
}

func TestService_NilBox_SecretAndSetReturnErrNoSecretBox(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	require.NoError(t, svc.Load(t.Context()))

	_, err := svc.Secret(t.Context(), "obsidian.apiKey")
	require.ErrorIs(t, err, ErrNoSecretBox)

	err = svc.Set(t.Context(), "obsidian.apiKey", "sk-live-456")
	require.ErrorIs(t, err, ErrNoSecretBox)
}

// TestService_SetEmptyClearsASecret pins the semantics buildObsidianClient's
// all-empty "vault off" branch depends on: an empty value on a secret
// definition clears the stored secret instead of encrypting the empty
// string. Encrypting it would leave a row that still reads back as the mask
// on every surface while decrypting to "" — a secret that looks configured
// and is not.
func TestService_SetEmptyClearsASecret(t *testing.T) {
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	repo := newFakeRepo()
	svc := New(repo, box)
	require.NoError(t, svc.Load(t.Context()))

	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "sk-live-123"))
	require.Equal(t, secretbox.MaskedSentinel, svc.String("obsidian.apiKey"))

	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", ""))

	got, err := svc.Secret(t.Context(), "obsidian.apiKey")
	require.NoError(t, err)
	assert.Empty(t, got, "a cleared secret must decrypt to nothing")
	assert.Empty(t, repo.nonces["obsidian.apiKey"], "the nonce must be cleared, so no stale ciphertext can be decrypted")

	// The mask is what makes a cleared secret indistinguishable from a
	// configured one, so every non-decrypting surface must report it unset.
	assert.Empty(t, svc.String("obsidian.apiKey"))
	assert.Empty(t, svc.Effective()["obsidian.apiKey"])

	// And it must survive a restart: Load rebuilds the snapshot from the
	// repo and must not mask the cleared row back into looking configured.
	reloaded := New(repo, box)
	require.NoError(t, reloaded.Load(t.Context()))
	assert.Empty(t, reloaded.String("obsidian.apiKey"), "a cleared secret must still read as unset after a reload")
	assert.Empty(t, reloaded.Effective()["obsidian.apiKey"])
}
