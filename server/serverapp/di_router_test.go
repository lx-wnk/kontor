package serverapp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/settings"
)

type fakeSettingsRepo struct{ m map[string]string }

func (f *fakeSettingsRepo) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := f.m[k]
	return v, ok, nil
}
func (f *fakeSettingsRepo) Set(_ context.Context, k, v string) error { f.m[k] = v; return nil }
func (f *fakeSettingsRepo) SetSecret(_ context.Context, k, ciphertext, _ string) error {
	f.m[k] = ciphertext
	return nil
}
func (f *fakeSettingsRepo) GetSecret(_ context.Context, k string) (string, string, bool, error) {
	v, ok := f.m[k]
	return v, "", ok, nil
}
func (f *fakeSettingsRepo) ListAll(_ context.Context) (map[string]string, error) {
	return f.m, nil
}

func newSettingsService(t *testing.T, rows map[string]string) *settings.Service {
	t.Helper()
	svc := settings.New(&fakeSettingsRepo{m: rows}, nil)
	require.NoError(t, svc.Load(context.Background()))
	return svc
}

func TestResolveBypassAuth(t *testing.T) {
	t.Run("nil service falls back to none -> bypass", func(t *testing.T) {
		assert.True(t, resolveBypassAuth(nil))
	})

	t.Run("auth.mode=none -> bypass", func(t *testing.T) {
		svc := newSettingsService(t, map[string]string{"auth.mode": "none"})
		assert.True(t, resolveBypassAuth(svc))
	})

	t.Run("default (no row) -> bypass", func(t *testing.T) {
		svc := newSettingsService(t, map[string]string{})
		assert.True(t, resolveBypassAuth(svc))
	})

	t.Run("auth.mode=plugin -> no bypass", func(t *testing.T) {
		svc := newSettingsService(t, map[string]string{"auth.mode": "plugin"})
		assert.False(t, resolveBypassAuth(svc))
	})
}
