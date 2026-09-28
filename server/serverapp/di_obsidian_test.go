package serverapp

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	"github.com/lx-wnk/kontor/server/internal/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSettingsServiceForTest returns a settings service over a fresh in-memory
// database with a deterministic box, so a secret written in a test is readable
// back in the same test.
func newSettingsServiceForTest(t *testing.T) *settings.Service {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	svc := settings.New(settingsRepoAdapter{inner: repo.NewAppSettingRepo(bundle.Client)}, box)
	require.NoError(t, svc.Load(t.Context()))
	return svc
}

func TestBuildObsidianClient_UnconfiguredIsNotAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t) // nothing set
	client, err := buildObsidianClient(t.Context(), svc)
	require.NoError(t, err)
	assert.Nil(t, client, "an unconfigured vault must disable the feature, not fail the boot")
}

// baseURL, vaultRoot and apiKey are a required trio: if any one is set, all
// three must be, or a half-configured vault would build a client that looks
// ready and then fails every request with an unhelpful 401. Each of the
// three tests below pins exactly one missing member so a regression in one
// check cannot hide behind the other two passing.

func TestBuildObsidianClient_MissingAPIKeyIsAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", "https://127.0.0.1:27124"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.vaultRoot", "notes"))
	// apiKey deliberately left unset
	_, err := buildObsidianClient(t.Context(), svc)
	require.Error(t, err, "a URL and root with no key must fail loudly, not ship an empty bearer token")
	assert.Contains(t, err.Error(), "obsidian.apiKey")
}

func TestBuildObsidianClient_MissingBaseURLIsAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.vaultRoot", "notes"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "secret-key"))
	// baseURL deliberately left unset
	_, err := buildObsidianClient(t.Context(), svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obsidian.baseURL")
}

func TestBuildObsidianClient_MissingVaultRootIsAnError(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", "https://127.0.0.1:27124"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "secret-key"))
	// vaultRoot deliberately left unset
	_, err := buildObsidianClient(t.Context(), svc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obsidian.vaultRoot")
}

// TestEnsureObsidianSpace_IdempotentWithStableID pins the two properties
// IndexNotes' spaceID argument depends on: a second call must not create a
// second row, and the id a caller captured from the first call must still
// resolve the same space after the second.
func TestEnsureObsidianSpace_IdempotentWithStableID(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	resources := repo.NewResourceRepo(bundle.Client)

	first, err := ensureObsidianSpace(t.Context(), resources)
	require.NoError(t, err)
	assert.Equal(t, "obsidian", first.Slug)

	second, err := ensureObsidianSpace(t.Context(), resources)
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "a second call must resolve the same row, not create a new one")

	rows, err := resources.ListForKind(t.Context(), repo.ResourceKindMemorySpace)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "want exactly one memory_space resource after two calls")
}

// TestBuildObsidianClient_ClearingTheTrioTurnsTheVaultOff is the round trip
// the Settings panel walks: configure the vault, then clear all three
// fields through the same settings.Service.Set the HTTP surface uses, and
// boot again. The partial-trio failure is deliberate and still tested above;
// what this pins is that the all-empty state stays reachable from the same
// path, so a user who turns the vault off does not brick the next start.
//
// The stuck state this guards against was never in Set: an encrypted empty
// string decrypts back to "", so even the old Set would have landed here in
// the all-empty branch. It was the panel, which sent the mask sentinel
// ("leave unchanged") or skipped the key entirely and so could not express
// "clear" at all. The Effective assertion below is the half Set owns: a
// cleared key must read back unset, not as the mask that made it look
// configured on every surface the user can see.
func TestBuildObsidianClient_ClearingTheTrioTurnsTheVaultOff(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", "https://127.0.0.1:27124"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.vaultRoot", "notes"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "secret-key"))

	client, err := buildObsidianClient(t.Context(), svc)
	require.NoError(t, err)
	require.NotNil(t, client, "the configured trio must build a client, or the clear below proves nothing")

	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", ""))
	require.NoError(t, svc.Set(t.Context(), "obsidian.vaultRoot", ""))
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", ""))

	client, err = buildObsidianClient(t.Context(), svc)
	require.NoError(t, err, "clearing all three must turn the vault off, not refuse the boot")
	assert.Nil(t, client)
	assert.Empty(t, svc.Effective()["obsidian.apiKey"],
		"the panel re-reads this after the save — a cleared key that still shows the mask reads as configured")
}

// TestBootObsidianClient_PartialTrioLogsAndLeavesTheVaultOff pins the boot
// tolerance a half-filled Obsidian setup must have: the same "optional
// integration, never a boot failure" rule buildGitHubClient's caller already
// applies (see di.go's githubClient assignment).
func TestBootObsidianClient_PartialTrioLogsAndLeavesTheVaultOff(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", "https://127.0.0.1:27124"))
	// vaultRoot and apiKey deliberately left unset: a partial trio.

	client := bootObsidianClient(t.Context(), svc)
	assert.Nil(t, client, "a partial trio must start the server with the vault off, not fail the boot")
}

func TestBootObsidianClient_FullTrioBuildsAClient(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", "https://127.0.0.1:27124"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.vaultRoot", "notes"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "secret-key"))

	client := bootObsidianClient(t.Context(), svc)
	assert.NotNil(t, client)
}

func TestWatchObsidianSettings_AppliesACompleteTrioWithoutARestart(t *testing.T) {
	svc := newSettingsServiceForTest(t)
	clients := obsidian.NewClientHolder(nil)
	watchObsidianSettings(svc, clients)

	require.NoError(t, svc.Set(t.Context(), "obsidian.baseURL", "https://127.0.0.1:27124"))
	assert.Nil(t, clients.Get(), "a partial trio leaves the vault off without failing the save")

	require.NoError(t, svc.Set(t.Context(), "obsidian.vaultRoot", "notes"))
	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", "secret-key"))
	assert.NotNil(t, clients.Get())

	require.NoError(t, svc.Set(t.Context(), "obsidian.apiKey", ""))
	assert.Nil(t, clients.Get(), "breaking the trio turns the vault off again")
}
