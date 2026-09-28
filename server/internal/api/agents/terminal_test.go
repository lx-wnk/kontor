package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalTarget_ReturnsPortAndTokenFromDiscoveryFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, channelconfig.DiscoveryDir)
	require.NoError(t, os.MkdirAll(dir, 0o700))

	pid := 33001
	writeDiscoveryFile(t, dir, fmt.Sprintf("%d.pty.json", pid), map[string]any{
		"port":  4242,
		"token": "term-secret",
	})

	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	port, token, err := m.TerminalTarget(pid)
	require.NoError(t, err)
	assert.Equal(t, 4242, port)
	assert.Equal(t, "term-secret", token)
}

func TestTerminalTarget_NoDiscoveryFile_ReturnsErrNoTerminal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	m := NewSpawnManager(5, 60000, 30, 60000, nil, nil)
	_, _, err := m.TerminalTarget(99999)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoTerminal))
}
