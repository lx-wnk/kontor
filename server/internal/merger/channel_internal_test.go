package merger

import (
	"os"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/testsupport/fakespawn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelDiscovery(t *testing.T) {
	fs := fakespawn.New(t)

	bridge := fs.Spawn(fakespawn.SpawnOpts{})
	avail, inject := channelDiscovery(bridge.PID)
	assert.True(t, avail, "discovery file present → channel available")
	assert.False(t, inject, "no tmuxPane/ptyInject → not live-injectable")

	avail, _ = channelDiscovery(bridge.PID + 1000000)
	assert.False(t, avail, "no discovery file → not available")

	// A discovery file carrying a tmux pane reference is live-injectable.
	live := fs.Spawn(fakespawn.SpawnOpts{LiveInjectable: true})
	avail, inject = channelDiscovery(live.PID)
	assert.True(t, avail, "discovery file present → channel available")
	assert.True(t, inject, "tmuxPane set → live-injectable")

	// A directory at the discovery path must not be mistaken for a channel.
	require.NoError(t, os.MkdirAll(channelconfig.DiscoveryFile(fs.Home, 5555), 0o700))
	avail, _ = channelDiscovery(5555)
	assert.False(t, avail, "directory at path → not available")
}

// TestChannelDiscovery_TwoFileModel verifies the two-file model introduced to
// fix the pty-broker vs channel-bridge discovery collision on the no-tmux path.
func TestChannelDiscovery_TwoFileModel(t *testing.T) {
	fs := fakespawn.New(t)

	t.Run("neither file → not available", func(t *testing.T) {
		unspawned := fs.Spawn(fakespawn.SpawnOpts{NoChannel: true})
		avail, inject := channelDiscovery(unspawned.PID)
		assert.False(t, avail)
		assert.False(t, inject)
	})

	t.Run("only pty file with ptyInject → available and injectable", func(t *testing.T) {
		ag := fs.Spawn(fakespawn.SpawnOpts{NoChannel: true, Pty: true})
		avail, inject := channelDiscovery(ag.PID)
		assert.True(t, avail, "pty file present → channel available")
		assert.True(t, inject, "ptyInject:true in pty file → live-injectable")
	})

	t.Run("only bridge file no tmux → available but not injectable", func(t *testing.T) {
		ag := fs.Spawn(fakespawn.SpawnOpts{})
		avail, inject := channelDiscovery(ag.PID)
		assert.True(t, avail, "bridge file present → channel available")
		assert.False(t, inject, "no tmuxPane and no pty file → not injectable")
	})

	t.Run("only bridge file with tmuxPane → available and injectable", func(t *testing.T) {
		ag := fs.Spawn(fakespawn.SpawnOpts{LiveInjectable: true})
		avail, inject := channelDiscovery(ag.PID)
		assert.True(t, avail)
		assert.True(t, inject, "tmuxPane in bridge file → live-injectable")
	})

	t.Run("both files → available and injectable", func(t *testing.T) {
		ag := fs.Spawn(fakespawn.SpawnOpts{Pty: true})
		avail, inject := channelDiscovery(ag.PID)
		assert.True(t, avail, "bridge+pty file → channel available")
		assert.True(t, inject, "pty file with ptyInject → live-injectable")
	})
}
