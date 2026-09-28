package restart_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/restart"
)

type fakeHealth struct{ entries map[string]plugin.Entry }

func (f fakeHealth) Lookup(id string) (plugin.Entry, bool) {
	e, ok := f.entries[id]
	return e, ok
}

func writeManifest(t *testing.T, dir, id string, caps []string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, id), 0o755))
	data, err := json.Marshal(plugin.Descriptor{ID: id, Capabilities: caps, Addr: "127.0.0.1:19999"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id, "plugin.json"), data, 0o644))
}

func active(ids ...string) func(context.Context) ([]string, error) {
	return func(context.Context) ([]string, error) { return ids, nil }
}

func TestValidatePassesWithNoActivePlugins(t *testing.T) {
	v := restart.NewAuthProviderValidator(fakeHealth{}, active(), t.TempDir())
	require.NoError(t, v.Validate(context.Background()))
}

func TestValidatePassesWhenActiveAuthProviderHealthy(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "oauth", []string{plugin.CapAuthProvider})
	reg := fakeHealth{entries: map[string]plugin.Entry{
		"oauth": plugin.NewEntryForTest(plugin.Descriptor{ID: "oauth"}, true),
	}}
	v := restart.NewAuthProviderValidator(reg, active("oauth"), dir)
	require.NoError(t, v.Validate(context.Background()))
}

func TestValidateFailsWhenActiveAuthProviderUnhealthy(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "oauth", []string{plugin.CapAuthProvider})
	reg := fakeHealth{entries: map[string]plugin.Entry{
		"oauth": plugin.NewEntryForTest(plugin.Descriptor{ID: "oauth"}, false),
	}}
	v := restart.NewAuthProviderValidator(reg, active("oauth"), dir)
	require.Error(t, v.Validate(context.Background()))
}

func TestValidateFailsWhenActiveAuthProviderAbsent(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "oauth", []string{plugin.CapAuthProvider})
	// active in DB, declares auth_provider, but NOT in the registry (failed start).
	v := restart.NewAuthProviderValidator(fakeHealth{}, active("oauth"), dir)
	require.Error(t, v.Validate(context.Background()))
}

func TestValidateIgnoresActiveNonAuthPlugin(t *testing.T) {
	dir := t.TempDir()
	writeManifest(t, dir, "voice", []string{plugin.CapRouteExtension})
	// route_extension absent from registry does not brick boot -> pass.
	v := restart.NewAuthProviderValidator(fakeHealth{}, active("voice"), dir)
	require.NoError(t, v.Validate(context.Background()))
}
