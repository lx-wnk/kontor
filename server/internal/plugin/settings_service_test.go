package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

type fakeRepo struct{ rows map[string]row }
type row struct {
	value, nonce string
	secret       bool
	pluginID     string
}

func (f *fakeRepo) ListByPlugin(_ context.Context, pluginID string) ([]Stored, error) {
	out := []Stored{}
	for k, r := range f.rows {
		if r.pluginID != "" && r.pluginID != pluginID {
			continue
		}
		out = append(out, Stored{Key: k, Value: r.value, Nonce: r.nonce, Secret: r.secret})
	}
	return out, nil
}
func (f *fakeRepo) Upsert(_ context.Context, pluginID string, s Stored) error {
	f.rows[s.Key] = row{s.Value, s.Nonce, s.Secret, pluginID}
	return nil
}
func (f *fakeRepo) DeleteByPlugin(_ context.Context, _ string) error {
	f.rows = map[string]row{}
	return nil
}

func TestService_PutEncryptsSecret_GetMasks(t *testing.T) {
	box, _ := secretbox.New(make([]byte, 32))
	repo := &fakeRepo{rows: map[string]row{}}
	svc := NewSettingsService(repo, box)
	schema := []SettingField{
		{Key: "endpoint", Type: "url"},
		{Key: "apiKey", Type: "string", Secret: true},
	}
	ctx := context.Background()

	require.NoError(t, svc.Put(ctx, "p1", schema, map[string]string{"endpoint": "https://x", "apiKey": "KEY123"}))

	// stored secret is encrypted (not plaintext)
	assert.NotEqual(t, "KEY123", repo.rows["apiKey"].value)
	assert.True(t, repo.rows["apiKey"].secret)

	// Get masks secrets, shows non-secret values
	view, err := svc.Get(ctx, "p1", schema)
	require.NoError(t, err)
	assert.Equal(t, "https://x", view["endpoint"])
	assert.Equal(t, MaskedSentinel, view["apiKey"])

	// PUT with the sentinel leaves the secret unchanged
	require.NoError(t, svc.Put(ctx, "p1", schema, map[string]string{"apiKey": MaskedSentinel}))
	dec, err := svc.Decrypted(ctx, "p1", schema)
	require.NoError(t, err)
	assert.Equal(t, "KEY123", dec["apiKey"])
}

func TestService_DecryptedAll_ReturnsPlaintext(t *testing.T) {
	box, _ := secretbox.New(make([]byte, 32))
	repo := &fakeRepo{rows: map[string]row{}}
	svc := NewSettingsService(repo, box)
	schema := []SettingField{
		{Key: "endpoint", Type: "url"},
		{Key: "apiKey", Type: "string", Secret: true},
	}
	ctx := context.Background()

	require.NoError(t, svc.Put(ctx, "p1", schema,
		map[string]string{"endpoint": "https://x", "apiKey": "TOPSECRET"}))

	all, err := svc.DecryptedAll(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, "https://x", all["endpoint"])
	assert.Equal(t, "TOPSECRET", all["apiKey"], "secret value must be decrypted")

	// Plugin not found → empty map, no error.
	empty, err := svc.DecryptedAll(ctx, "unknown-plugin")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestService_Put_TypeValidation(t *testing.T) {
	box, _ := secretbox.New(make([]byte, 32))
	ctx := context.Background()

	schema := []SettingField{
		{Key: "count", Type: "int"},
		{Key: "enabled", Type: "bool"},
		{Key: "endpoint", Type: "url"},
		{Key: "mode", Type: "enum", Enum: []string{"fast", "slow"}},
		{Key: "label", Type: "string"},
		{Key: "token", Type: "string", Secret: true},
	}

	cases := []struct {
		name    string
		values  map[string]string
		wantErr bool
		errIs   error
	}{
		{"valid int", map[string]string{"count": "42"}, false, nil},
		{"invalid int", map[string]string{"count": "abc"}, true, ErrInvalidValue},
		{"valid bool true", map[string]string{"enabled": "true"}, false, nil},
		{"valid bool false", map[string]string{"enabled": "false"}, false, nil},
		{"invalid bool", map[string]string{"enabled": "yes"}, true, ErrInvalidValue},
		{"valid url https", map[string]string{"endpoint": "https://api.example.com"}, false, nil},
		{"valid url http", map[string]string{"endpoint": "http://api.example.com"}, false, nil},
		{"invalid url no scheme", map[string]string{"endpoint": "api.example.com"}, true, ErrInvalidValue},
		{"invalid url empty", map[string]string{"endpoint": ""}, true, ErrInvalidValue},
		{"invalid url file scheme", map[string]string{"endpoint": "file://host/etc/passwd"}, true, ErrInvalidValue},
		{"invalid url ftp scheme", map[string]string{"endpoint": "ftp://host/x"}, true, ErrInvalidValue},
		{"invalid url javascript scheme", map[string]string{"endpoint": "javascript:alert(1)"}, true, ErrInvalidValue},
		{"valid enum", map[string]string{"mode": "fast"}, false, nil},
		{"invalid enum", map[string]string{"mode": "turbo"}, true, ErrInvalidValue},
		{"valid string any", map[string]string{"label": "anything goes"}, false, nil},
		{"unknown key", map[string]string{"nope": "x"}, true, ErrUnknownKey},
		{"masked sentinel skips validation", map[string]string{"token": MaskedSentinel}, false, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{rows: map[string]row{}}
			svc := NewSettingsService(repo, box)
			err := svc.Put(ctx, "p1", schema, tc.values)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errIs != nil {
					require.ErrorIs(t, err, tc.errIs)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestService_Put_ValidationFailDoesNotPersist(t *testing.T) {
	box, _ := secretbox.New(make([]byte, 32))
	repo := &fakeRepo{rows: map[string]row{}}
	svc := NewSettingsService(repo, box)
	schema := []SettingField{
		{Key: "count", Type: "int"},
		{Key: "label", Type: "string"},
	}
	ctx := context.Background()

	// count is invalid, label is valid — nothing should be persisted.
	err := svc.Put(ctx, "p1", schema, map[string]string{"count": "bad", "label": "ok"})
	require.ErrorIs(t, err, ErrInvalidValue)
	assert.Empty(t, repo.rows, "no value must be persisted when validation fails")
}
