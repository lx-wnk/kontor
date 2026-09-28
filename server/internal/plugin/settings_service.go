// Per-plugin configuration values: encrypted secret fields at rest, masked in
// API responses.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

// MaskedSentinel is returned for secret values and, when sent back on Put,
// signals "leave unchanged".
const MaskedSentinel = secretbox.MaskedSentinel

// ErrUnknownKey is returned by Put when a submitted key is not in the schema.
var ErrUnknownKey = errors.New("pluginsettings: unknown setting key")

// ErrInvalidValue is returned by Put when a submitted value does not satisfy
// the field's declared Type (int, bool, url, enum).
var ErrInvalidValue = errors.New("pluginsettings: invalid value")

// Stored is one persisted setting row (storage-agnostic).
type Stored struct {
	Key, Value, Nonce string
	Secret            bool
}

// Repo is the persistence the service needs.
type Repo interface {
	ListByPlugin(ctx context.Context, pluginID string) ([]Stored, error)
	Upsert(ctx context.Context, pluginID string, s Stored) error
	DeleteByPlugin(ctx context.Context, pluginID string) error
}

type Service struct {
	repo Repo
	box  *secretbox.Box
}

func NewSettingsService(repo Repo, box *secretbox.Box) *Service {
	return &Service{repo: repo, box: box}
}

func (s *Service) load(ctx context.Context, pluginID string) (map[string]Stored, error) {
	rows, err := s.repo.ListByPlugin(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]Stored, len(rows))
	for _, r := range rows {
		m[r.Key] = r
	}
	return m, nil
}

// Get returns key->value for the schema; secret values are masked.
func (s *Service) Get(ctx context.Context, pluginID string, schema []SettingField) (map[string]string, error) {
	stored, err := s.load(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range schema {
		r, ok := stored[f.Key]
		if !ok {
			out[f.Key] = ""
			continue
		}
		if f.Secret {
			out[f.Key] = MaskedSentinel
		} else {
			out[f.Key] = r.Value
		}
	}
	return out, nil
}

func validateValue(f SettingField, v string) error {
	switch f.Type {
	case "int":
		if _, err := strconv.Atoi(v); err != nil {
			return fmt.Errorf("%w: field %q requires an integer, got %q", ErrInvalidValue, f.Key, v)
		}
	case "bool":
		if v != "true" && v != "false" {
			return fmt.Errorf("%w: field %q requires \"true\" or \"false\", got %q", ErrInvalidValue, f.Key, v)
		}
	case "url":
		u, parseErr := url.ParseRequestURI(v)
		if parseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: field %q requires an http(s) URL with host, got %q", ErrInvalidValue, f.Key, v)
		}
	case "enum":
		for _, opt := range f.Enum {
			if v == opt {
				return nil
			}
		}
		return fmt.Errorf("%w: field %q value %q not in allowed set %v", ErrInvalidValue, f.Key, v, f.Enum)
	}
	// "string" and unrecognised types: accept any value.
	return nil
}

// Put persists values. Secret fields are encrypted; a secret submitted as the
// masked sentinel is skipped (left unchanged). Unknown keys (not in schema) are
// rejected. Values are type-checked against the field's declared Type before any
// persistence — validation failure persists nothing.
func (s *Service) Put(ctx context.Context, pluginID string, schema []SettingField, values map[string]string) error {
	schemaMap := make(map[string]SettingField, len(schema))
	for _, f := range schema {
		schemaMap[f.Key] = f
	}
	// Validate all submitted entries before writing anything.
	for k, v := range values {
		f, ok := schemaMap[k]
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownKey, k)
		}
		// Masked sentinel means "leave unchanged" — skip validation.
		if f.Secret && v == MaskedSentinel {
			continue
		}
		if err := validateValue(f, v); err != nil {
			return err
		}
	}
	for _, f := range schema {
		v, ok := values[f.Key]
		if !ok {
			continue
		}
		if f.Secret {
			if v == MaskedSentinel {
				continue // unchanged
			}
			ct, nonce, err := s.box.Encrypt(v)
			if err != nil {
				return err
			}
			if err := s.repo.Upsert(ctx, pluginID, Stored{Key: f.Key, Value: ct, Nonce: nonce, Secret: true}); err != nil {
				return err
			}
			continue
		}
		if err := s.repo.Upsert(ctx, pluginID, Stored{Key: f.Key, Value: v, Secret: false}); err != nil {
			return err
		}
	}
	return nil
}

// Decrypted returns key->plaintext (secrets decrypted) for env injection (SP2).
func (s *Service) Decrypted(ctx context.Context, pluginID string, schema []SettingField) (map[string]string, error) {
	stored, err := s.load(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range schema {
		r, ok := stored[f.Key]
		if !ok {
			continue
		}
		if r.Secret {
			pt, derr := s.box.Decrypt(r.Value, r.Nonce)
			if derr != nil {
				return nil, derr
			}
			out[f.Key] = pt
		} else {
			out[f.Key] = r.Value
		}
	}
	return out, nil
}

// DecryptedAll returns all stored settings with secrets decrypted.
// Unlike Decrypted it requires no schema — it uses the persisted Secret flag.
// Used by the registry's SettingsProvider for env injection at spawn time.
func (s *Service) DecryptedAll(ctx context.Context, pluginID string) (map[string]string, error) {
	rows, err := s.repo.ListByPlugin(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Secret {
			pt, derr := s.box.Decrypt(r.Value, r.Nonce)
			if derr != nil {
				return nil, derr
			}
			out[r.Key] = pt
		} else {
			out[r.Key] = r.Value
		}
	}
	return out, nil
}

// Clear removes all settings for a plugin (called on uninstall).
func (s *Service) Clear(ctx context.Context, pluginID string) error {
	return s.repo.DeleteByPlugin(ctx, pluginID)
}
