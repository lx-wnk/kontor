package serverapp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

func TestSeedPluginsFromEnabledList(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := t.Context()

	// Seed the legacy setting directly via the repo (bypasses registry
	// validation — the key has been removed from the registry).
	appSettingRepo := repo.NewAppSettingRepo(bundle.Client)
	_, err = appSettingRepo.Upsert(ctx, "plugins.enabled", "p1,p2")
	require.NoError(t, err)
	settingsSvc := settings.New(settingsRepoAdapter{inner: appSettingRepo}, nil)
	require.NoError(t, settingsSvc.Load(ctx))

	pluginRepo := repo.NewPluginRepo(bundle.Client)
	require.NoError(t, seedPluginsFromEnabledList(ctx, settingsSvc, pluginRepo))

	for _, id := range []string{"p1", "p2"} {
		p, getErr := pluginRepo.Get(ctx, id)
		require.NoError(t, getErr)
		assert.True(t, p.Active, "%s should be active", id)
		require.NotNil(t, p.InstalledAt, "%s should have installed_at", id)
	}

	// Idempotent: a second run changes nothing and creates no duplicates.
	first, err := pluginRepo.Get(ctx, "p1")
	require.NoError(t, err)
	require.NoError(t, seedPluginsFromEnabledList(ctx, settingsSvc, pluginRepo))
	all, err := pluginRepo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2)
	again, err := pluginRepo.Get(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, first.InstalledAt.UnixNano(), again.InstalledAt.UnixNano(), "installed_at must be preserved")
}

func TestSeedPluginsFromEnabledList_NilArgs_NoOp(t *testing.T) {
	require.NoError(t, seedPluginsFromEnabledList(t.Context(), nil, nil))
}
