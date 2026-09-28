package serverapp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/provider"
)

// A module carries its provider descriptors in its own directory. Without this
// the operator would have to copy files into a shared directory the module
// knows nothing about, and an update of the module would silently leave the
// copy behind.
func TestRegisterModuleProviders_LoadsWhatTheModuleShips(t *testing.T) {
	moduleDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(moduleDir, "providers"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "providers", "acme.yaml"), []byte(
		"id: acme\ndisplayName: Acme\nexeNames: [acme]\nsource: jsonl\nsessionGlob: \"*.jsonl\"\n"), 0o600))

	registry := plugin.New(t.TempDir())
	registry.InjectEntryWithDirForTest(plugin.Descriptor{
		Contract:  plugin.CurrentContract,
		ID:        "acme-module",
		Name:      "Acme",
		Providers: []string{"providers/*.yaml"},
	}, moduleDir, true)

	providers, err := provider.NewRegistry(provider.Options{})
	require.NoError(t, err)
	before := len(providers.Descriptors())

	registerModuleProviders(registry, providers)

	require.Len(t, providers.Descriptors(), before+1, "the module's own descriptor must be registered")
	_, found := providers.Descriptors()["acme"]
	require.True(t, found, "the provider is registered under the id its descriptor declares")

	// A module without a directory loads nothing rather than reading the
	// server's working directory.
	registry.InjectEntryForTest(plugin.Descriptor{
		Contract: plugin.CurrentContract, ID: "dirless", Name: "Dirless",
		Providers: []string{"providers/*.yaml"},
	}, true)
	registerModuleProviders(registry, providers)
	require.Len(t, providers.Descriptors(), before+1, "an entry without a directory must load nothing")
}
