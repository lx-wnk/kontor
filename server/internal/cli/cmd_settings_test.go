package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

// settingsRepoAdapter maps settings.Repo onto repo.AppSettingRepo, mirroring
// serverapp/di.go's adapter of the same shape. Duplicated here rather than
// exported across the package boundary — see CLAUDE.md's DRY-stops-at-the-
// context-boundary rule; cli and serverapp are different bounded contexts.
type settingsRepoAdapter struct{ inner repo.AppSettingRepo }

func (a settingsRepoAdapter) Get(ctx context.Context, k string) (string, bool, error) {
	return a.inner.Get(ctx, k)
}
func (a settingsRepoAdapter) Set(ctx context.Context, k, v string) error {
	_, err := a.inner.Upsert(ctx, k, v)
	return err
}
func (a settingsRepoAdapter) ListAll(ctx context.Context) (map[string]string, error) {
	rows, err := a.inner.List(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}
func (a settingsRepoAdapter) SetSecret(ctx context.Context, k, ciphertext, nonce string) error {
	_, err := a.inner.UpsertSecret(ctx, k, ciphertext, nonce)
	return err
}
func (a settingsRepoAdapter) GetSecret(ctx context.Context, k string) (string, string, bool, error) {
	return a.inner.GetSecret(ctx, k)
}

// isolateMasterKeyPaths keeps LoadOrGenerateMasterKey (called from
// openDBStore) off the developer's real ~/.claude/dashboard-secret.key.
// CLAUDE_CONFIG_DIR alone is not enough: its legacy-key fallback still reads
// HOME/.claude when the primary configured path has no key yet, so both must
// be isolated to a temp dir together.
func isolateMasterKeyPaths(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestDBStore_SetGetList(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"
	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NoError(t, store.Set(ctx, "auth.mode", "plugin"))
	require.NoError(t, store.Set(ctx, "auth.mode", "none")) // upsert

	v, ok, err := store.Get(ctx, "auth.mode")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "none", v)

	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, "none", all["auth.mode"])
}

func TestDBStore_RejectsUnknownKey(t *testing.T) {
	isolateMasterKeyPaths(t)
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	// CLI validates against the registry before writing.
	require.Error(t, store.SetValidated(context.Background(), "nope", "x"))
	require.Error(t, store.SetValidated(context.Background(), "spawn.rateLimit", "abc"))
	require.NoError(t, store.SetValidated(context.Background(), "spawn.rateLimit", "7"))
}

// TestDBStore_SetValidated_EncryptsSecretValue guards the CLI's lockout
// recovery path: a secret set through the CLI must land encrypted, mirroring
// settings.Service's own round-trip test (internal/settings/service_test.go
// TestService_SecretRoundTripAndMasking, assert.NotEqual on the stored row).
// The decrypt round trip through settings.Service.Secret — the one real
// non-masking read path — is the load-bearing assertion: "not equal to the
// plaintext, not empty" alone would stay green even if Encrypt's ciphertext
// and nonce were passed to UpsertSecret in the wrong order, which is exactly
// the contract this test exists to pin.
func TestDBStore_SetValidated_EncryptsSecretValue(t *testing.T) {
	isolateMasterKeyPaths(t)
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NoError(t, store.SetValidated(ctx, "obsidian.apiKey", "sk-live-999"))

	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, "sk-live-999", all["obsidian.apiKey"])
	assert.NotEmpty(t, all["obsidian.apiKey"])

	box, err := loadSecretBox()
	require.NoError(t, err)
	svc := settings.New(settingsRepoAdapter{inner: store.repo}, box)
	require.NoError(t, svc.Load(ctx))
	got, err := svc.Secret(ctx, "obsidian.apiKey")
	require.NoError(t, err)
	assert.Equal(t, "sk-live-999", got)
}

// TestDBStore_SetValidated_ClearsSecretOnEmptyValue pins the CLI to the same
// semantics settings.Service.Set has: an empty value clears the secret
// instead of encrypting the empty string. The two surfaces must agree, and
// this one is load-bearing for the boot. Encrypting "" leaves a row that
// reads back as the mask on every non-decrypting surface — so a user who
// cleared the key from the CLI (the documented lockout-recovery path, used
// precisely when the server is down) then sees "********" in Settings →
// Obsidian, the panel counts the API-key field as filled, the trio guard
// permits the save, the mask is sent as "leave unchanged", and the next boot
// fails with "missing required settings: obsidian.apiKey". The panel guard is
// not bypassed there — it is walked straight through.
func TestDBStore_SetValidated_ClearsSecretOnEmptyValue(t *testing.T) {
	isolateMasterKeyPaths(t)
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NoError(t, store.SetValidated(ctx, "obsidian.apiKey", "sk-live-999"))
	require.NoError(t, store.SetValidated(ctx, "obsidian.apiKey", ""))

	all, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all["obsidian.apiKey"], "the ciphertext must be gone, not overwritten with an encrypted empty string")

	box, err := loadSecretBox()
	require.NoError(t, err)
	svc := settings.New(settingsRepoAdapter{inner: store.repo}, box)
	require.NoError(t, svc.Load(ctx))

	// Effective() is the surface the Settings panel reads: a cleared key that
	// still shows the mask there is what lets the trio save through.
	assert.Empty(t, svc.Effective()["obsidian.apiKey"], "a cleared secret must read as unset, not as the mask")
	got, err := svc.Secret(ctx, "obsidian.apiKey")
	require.NoError(t, err)
	assert.Empty(t, got, "no stale ciphertext may survive the clear")
}

// TestDBStore_SetValidated_ClearNeedsNoMasterKey pins the ordering of the
// clear branch: it runs before loadSecretBox. Removing a secret must not
// depend on resolving a master key — this is the recovery command, run when
// the key may be exactly what is unreachable (a different HOME under sudo,
// a key file lost with the config dir). Without this, the branch could drift
// below loadSecretBox and the test above would stay green, since by then the
// key file already exists.
func TestDBStore_SetValidated_ClearNeedsNoMasterKey(t *testing.T) {
	keyDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", keyDir)
	t.Setenv("HOME", t.TempDir())
	keyPath := filepath.Join(keyDir, "dashboard-secret.key")

	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.SetValidated(context.Background(), "obsidian.apiKey", ""))

	_, statErr := os.Stat(keyPath)
	assert.True(t, os.IsNotExist(statErr), "clearing a secret must not generate a master key file")
}

// TestDBStore_SetValidated_RejectsMaskSentinelForSecret is the BLOCKING fix:
// Service.Set treats secretbox.MaskedSentinel as "leave unchanged" because it
// always has the previous ciphertext to fall back to. SetValidated is a raw
// upsert with no such fallback — round-tripping list/get's own masked output
// back into "set" (by hand, or by scripting list into set) must not encrypt
// the literal sentinel over the real secret.
func TestDBStore_SetValidated_RejectsMaskSentinelForSecret(t *testing.T) {
	isolateMasterKeyPaths(t)
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	require.NoError(t, store.SetValidated(ctx, "obsidian.apiKey", "sk-live-real"))

	err = store.SetValidated(ctx, "obsidian.apiKey", secretbox.MaskedSentinel)
	require.ErrorIs(t, err, ErrMaskedValueRejected)

	// The real secret must still be there — the rejected write must not have
	// touched storage.
	ciphertext, nonce, found, err := store.repo.GetSecret(ctx, "obsidian.apiKey")
	require.NoError(t, err)
	require.True(t, found)
	box, err := loadSecretBox()
	require.NoError(t, err)
	pt, err := box.Decrypt(ciphertext, nonce)
	require.NoError(t, err)
	assert.Equal(t, "sk-live-real", pt)
}

// TestDBStore_SetValidated_RefusesSecretWithoutMasterKey pins Finding A's
// second half: a secret write must never fall back to plaintext when the
// master key cannot be resolved. DASHBOARD_SECRET_KEY set to invalid hex
// forces the real resolution path in openDBStore to fail, exactly as it
// would if an operator misconfigured the env var during recovery.
func TestDBStore_SetValidated_RefusesSecretWithoutMasterKey(t *testing.T) {
	isolateMasterKeyPaths(t)
	t.Setenv("DASHBOARD_SECRET_KEY", "not-valid-hex")
	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	ctx := context.Background()

	err = store.SetValidated(ctx, "obsidian.apiKey", "sk-live-000")
	require.ErrorIs(t, err, ErrNoMasterKey)

	all, listErr := store.List(ctx)
	require.NoError(t, listErr)
	_, exists := all["obsidian.apiKey"]
	assert.False(t, exists, "a refused write must not persist any row")
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it. cmd_settings.go's set command prints via
// fmt.Printf straight to os.Stdout (not cmd.OutOrStdout()), so this is the
// only way to observe it end to end.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// TestSettingsListCmd_MasksSecretValueInOutput guards "settings list": the
// stored row for a secret key is base64 ciphertext, not plaintext, but it is
// still a value no consumer should see (same rule Service.Load already
// enforces server-side). Asserts on the whole captured stdout, not just a
// substring pulled from one column, so the ciphertext escaping through any
// part of the formatted line still fails the test.
func TestSettingsListCmd_MasksSecretValueInOutput(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.SetValidated(context.Background(), "obsidian.apiKey", "sk-live-list"))
	rows, err := store.List(context.Background())
	require.NoError(t, err)
	ciphertext := rows["obsidian.apiKey"]
	require.NotEmpty(t, ciphertext)
	require.NoError(t, store.Close())

	out := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetArgs([]string{"list", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.NotContains(t, out, "sk-live-list")
	assert.NotContains(t, out, ciphertext)
	assert.Contains(t, out, secretbox.MaskedSentinel)
}

// TestSettingsGetCmd_MasksSecretValueInOutput mirrors the list test for
// "settings get <key>".
func TestSettingsGetCmd_MasksSecretValueInOutput(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.SetValidated(context.Background(), "obsidian.apiKey", "sk-live-get"))
	rows, err := store.List(context.Background())
	require.NoError(t, err)
	ciphertext := rows["obsidian.apiKey"]
	require.NotEmpty(t, ciphertext)
	require.NoError(t, store.Close())

	out := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetArgs([]string{"get", "obsidian.apiKey", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.NotContains(t, out, "sk-live-get")
	assert.NotContains(t, out, ciphertext)
	assert.Equal(t, secretbox.MaskedSentinel+"\n", out)
}

// TestSettingsSetCmd_MasksSecretValueInOutput guards the same defect class as
// the PATCH-response mask (settings/handler_test.go), on the CLI's own
// surface: "dashboard settings set obsidian.apiKey <value>" must not echo
// the plaintext it was just given into stdout, where it lands in terminal
// scrollback, tmux capture-pane, or a redirected log file.
func TestSettingsSetCmd_MasksSecretValueInOutput(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	out := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetArgs([]string{"set", "obsidian.apiKey", "sk-live-777", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.NotContains(t, out, "sk-live-777")
	assert.Contains(t, out, secretbox.MaskedSentinel)
}

// TestSettingsGetCmd_UnsetSecretPrintsEmptyNotMask is Minor 1: with no row
// stored, "get obsidian.apiKey" must not print the mask sentinel — that
// would be indistinguishable from a configured key and would send an
// operator debugging a failing vault call down the wrong path.
func TestSettingsGetCmd_UnsetSecretPrintsEmptyNotMask(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	out := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetArgs([]string{"get", "obsidian.apiKey", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.NotContains(t, out, secretbox.MaskedSentinel)
	assert.Equal(t, "\n", out)
}

// TestSettingsGetCmd_FailsClosedOnUnknownKey is Minor 2: a stored row whose
// key is no longer in the registry (e.g. a secret key renamed or dropped in
// a later release — registry drift) must still be masked, not printed in
// clear just because settings.Lookup misses.
func TestSettingsGetCmd_FailsClosedOnUnknownKey(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	store, err := openDBStore(dbPath)
	require.NoError(t, err)
	require.NoError(t, store.Set(context.Background(), "obsidian.oldApiKey", "raw-value-should-not-print"))
	require.NoError(t, store.Close())

	out := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetArgs([]string{"get", "obsidian.oldApiKey", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.NotContains(t, out, "raw-value-should-not-print")
	assert.Equal(t, secretbox.MaskedSentinel+"\n", out)
}

// TestSettingsSetCmd_WarnsOnLiveApplyKey guards the running-server blind spot:
// "settings set" writes straight to the DB, but a running server only reads
// its settings once at startup (settings.Service.Load), so an ApplyLive key
// silently does not reach it until a restart. The warning must land on
// stderr, not stdout, so stdout stays script-safe.
func TestSettingsSetCmd_WarnsOnLiveApplyKey(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	var stderr bytes.Buffer
	stdout := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"set", "onboarding.completed", "true", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.Equal(t, "set onboarding.completed = true\n", stdout)
	assert.Contains(t, stderr.String(), "restart")
}

// TestSettingsSetCmd_NoWarningOnRestartApplyKey mirrors the above for an
// ApplyRestart key, where the registry already documents that a restart is
// needed — repeating that here would just be noise.
func TestSettingsSetCmd_NoWarningOnRestartApplyKey(t *testing.T) {
	isolateMasterKeyPaths(t)
	dbPath := t.TempDir() + "/test.db"

	var stderr bytes.Buffer
	stdout := captureStdout(t, func() {
		cmd := newSettingsCmd()
		cmd.SetErr(&stderr)
		cmd.SetArgs([]string{"set", "auth.mode", "plugin", "--db", dbPath})
		require.NoError(t, cmd.Execute())
	})

	assert.Equal(t, "set auth.mode = plugin\n", stdout)
	assert.Empty(t, stderr.String())
}

// TestOpenDBStore_DoesNotTouchMasterKeyFile is Minor 3: a read-only command
// (list/get), or a non-secret set, must never generate or read the
// secretbox master key file. LoadOrGenerateMasterKey persists a new key on
// first use — resolving it eagerly in openDBStore means every dbStore user
// (settings list/get, grants, caps) pays that side effect, and a command run
// under a different HOME (e.g. sudo) would silently generate a second,
// different key the server can never decrypt with.
func TestOpenDBStore_DoesNotTouchMasterKeyFile(t *testing.T) {
	keyDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", keyDir)
	t.Setenv("HOME", t.TempDir())
	keyPath := filepath.Join(keyDir, "dashboard-secret.key")

	store, err := openDBStore(t.TempDir() + "/test.db")
	require.NoError(t, err)
	defer store.Close()
	_, statErr := os.Stat(keyPath)
	assert.True(t, os.IsNotExist(statErr), "openDBStore must not generate a master key file")

	require.NoError(t, store.Set(context.Background(), "auth.mode", "none"))
	_, statErr = os.Stat(keyPath)
	assert.True(t, os.IsNotExist(statErr), "a non-secret write must not generate a master key file")
}
