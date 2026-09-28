package settings

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/secretbox"
)

// Repo is the persistence the service needs (subset of repo.AppSettingRepo,
// declared locally so tests can fake it).
type Repo interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
	SetSecret(ctx context.Context, key, ciphertext, nonce string) error
	GetSecret(ctx context.Context, key string) (string, string, bool, error)
	// ListAll returns every stored key/value pair. The returned map may be
	// an implementation's own backing store rather than a copy — callers
	// must treat it as read-only and never write into it.
	ListAll(ctx context.Context) (map[string]string, error)
}

// ValidationError marks a Set failure caused by an invalid value or unknown key
// (client error), as opposed to a persistence/apply failure (server error).
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// ErrNoSecretBox is returned by Secret and by Set for a secret definition
// when the service was built without a *secretbox.Box — e.g. no database is
// configured, so no master key was ever resolved.
var ErrNoSecretBox = errors.New("settings: no secret box configured")

// Service reads settings DB-first with registry-default fallback.
type Service struct {
	repo Repo
	box  *secretbox.Box

	mu        sync.RWMutex
	snapshot  map[string]string // key -> raw DB value (present only if a row exists)
	onChange  []func(ctx context.Context, key string)
	onPreSave []func(ctx context.Context, key, value string) error
}

// New builds a Service. box may be nil when no database is configured; in
// that case secret reads and writes return ErrNoSecretBox instead of
// panicking.
func New(repo Repo, box *secretbox.Box) *Service {
	return &Service{repo: repo, box: box, snapshot: map[string]string{}}
}

// Load reads all rows into the snapshot. Call once at startup.
//
// A row for a secret definition is stored in clear ciphertext by the repo, so
// it is replaced with secretbox.MaskedSentinel here — otherwise Effective()
// would publish base64 ciphertext, which is not a leak of the plaintext but
// is still a value no consumer should see. The replacement is written into a
// snapshot map built here, never into the map ListAll returned — that map may
// be a Repo implementation's own backing store, per its doc comment, and
// writing into it would let a masked read corrupt the stored ciphertext.
//
// An empty row value is a cleared secret (Set writes that shape, see its own
// doc comment) and is left alone: masking it would make an unset secret
// indistinguishable from a configured one on every surface, and would keep
// buildObsidianClient from ever seeing the all-empty "vault off" state again.
func (s *Service) Load(ctx context.Context) error {
	all, err := s.repo.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("settings.Load: %w", err)
	}
	snapshot := make(map[string]string, len(all))
	for k, v := range all {
		snapshot[k] = v
	}
	for _, d := range All() {
		if !d.Secret {
			continue
		}
		if v, ok := snapshot[d.Key]; ok && v != "" {
			snapshot[d.Key] = secretbox.MaskedSentinel
		}
	}
	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
	return nil
}

// raw returns the effective string value: DB row if present, else registry default.
func (s *Service) raw(key string) string {
	s.mu.RLock()
	v, ok := s.snapshot[key]
	s.mu.RUnlock()
	if ok {
		return v
	}
	if d, ok := Lookup(key); ok {
		return d.Default
	}
	return ""
}

// Typed accessors. They assume the key exists in the registry (programmer error otherwise).
func (s *Service) String(key string) string { return s.raw(key) }
func (s *Service) Bool(key string) bool     { b, _ := strconv.ParseBool(s.raw(key)); return b }
func (s *Service) Int(key string) int       { n, _ := strconv.Atoi(s.raw(key)); return n }
func (s *Service) Float(key string) float64 { f, _ := strconv.ParseFloat(s.raw(key), 64); return f }

func (s *Service) StringSlice(key string) []string {
	raw := s.raw(key)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Effective returns key -> current effective value for every registry key
// (DB value or default). Used by the API to render the settings UI.
func (s *Service) Effective() map[string]string {
	out := map[string]string{}
	for _, d := range All() {
		out[d.Key] = s.raw(d.Key)
	}
	return out
}

// Secret returns the decrypted value of a secret setting. It is the only read
// path that does not mask; every other accessor returns secretbox.MaskedSentinel
// so a secret cannot leak through a surface that was written for plain values.
func (s *Service) Secret(ctx context.Context, key string) (string, error) {
	def, ok := Lookup(key)
	if !ok {
		return "", fmt.Errorf("settings: unknown setting %q", key)
	}
	if !def.Secret {
		return "", fmt.Errorf("settings: %q is not a secret setting", key)
	}
	if s.box == nil {
		return "", ErrNoSecretBox
	}
	ct, nonce, found, err := s.repo.GetSecret(ctx, key)
	if err != nil {
		return "", fmt.Errorf("settings.Secret: %w", err)
	}
	// An empty nonce is a row that was never written as a secret, or one Set
	// cleared (it writes exactly that shape) — either way there is nothing to
	// decrypt and the secret reads as unset.
	if !found || nonce == "" {
		return "", nil
	}
	return s.box.Decrypt(ct, nonce)
}

// Set validates against the registry, persists, and updates the snapshot.
// Validation failures are wrapped as *ValidationError (client error);
// persistence failures are returned as plain wrapped errors (server error).
//
// An empty value on a secret definition CLEARS the secret rather than
// encrypting the empty string: empty means "no value", and encrypting it
// would leave a row that still reads back as secretbox.MaskedSentinel on
// every surface while decrypting to "". Clearing writes an empty ciphertext
// and an empty nonce over the row — Secret and repo.GetSecret both treat an
// empty nonce as "not a secret row", so no stale ciphertext survives — which
// is also the only way a caller can return obsidian's baseURL/vaultRoot/
// apiKey trio to the all-empty state buildObsidianClient reads as "vault
// off". Clearing needs no master key, so it is allowed even when box is nil.
func (s *Service) Set(ctx context.Context, key, value string) error {
	if err := s.set(ctx, key, value); err != nil {
		return err
	}
	s.mu.RLock()
	hooks := s.onChange
	s.mu.RUnlock()
	for _, fn := range hooks {
		fn(ctx, key)
	}
	return nil
}

// OnChange registers fn to run after every successful Set, in the caller's goroutine.
func (s *Service) OnChange(fn func(ctx context.Context, key string)) {
	s.mu.Lock()
	s.onChange = append(s.onChange, fn)
	s.mu.Unlock()
}

// OnPreSave registers fn to run after def.Validate succeeds but before the
// value is persisted, for non-secret keys only. Returning an error aborts the
// Set as a *ValidationError, before repo.Set is ever called. This is the seam
// a caller needing an HTTP probe (e.g. obsidian.vaultRoot) hooks into, since
// Definition.Validate is synchronous and cannot make a network call itself.
func (s *Service) OnPreSave(fn func(ctx context.Context, key, value string) error) {
	s.mu.Lock()
	s.onPreSave = append(s.onPreSave, fn)
	s.mu.Unlock()
}

func (s *Service) set(ctx context.Context, key, value string) error {
	def, ok := Lookup(key)
	if !ok {
		return &ValidationError{Err: fmt.Errorf("unknown key %q", key)}
	}
	if def.Secret {
		if value == secretbox.MaskedSentinel {
			return nil // the caller is echoing back what it was shown
		}
		if value == "" {
			if err := s.repo.SetSecret(ctx, key, "", ""); err != nil {
				return fmt.Errorf("settings.Set: clear %q: %w", key, err)
			}
			s.mu.Lock()
			s.snapshot[key] = ""
			s.mu.Unlock()
			return nil
		}
		if s.box == nil {
			return ErrNoSecretBox
		}
		ct, nonce, err := s.box.Encrypt(value)
		if err != nil {
			return fmt.Errorf("settings.Set: encrypt %q: %w", key, err)
		}
		if err := s.repo.SetSecret(ctx, key, ct, nonce); err != nil {
			return fmt.Errorf("settings.Set: %w", err)
		}
		s.mu.Lock()
		s.snapshot[key] = secretbox.MaskedSentinel
		s.mu.Unlock()
		return nil
	}
	if err := def.Validate(value); err != nil {
		return &ValidationError{Err: err}
	}
	s.mu.RLock()
	preSaveHooks := s.onPreSave
	s.mu.RUnlock()
	for _, fn := range preSaveHooks {
		if err := fn(ctx, key, value); err != nil {
			return &ValidationError{Err: err}
		}
	}
	if err := s.repo.Set(ctx, key, value); err != nil {
		return fmt.Errorf("settings.Set: %w", err)
	}
	s.mu.Lock()
	s.snapshot[key] = value
	s.mu.Unlock()
	return nil
}

// ApplyOf reports the apply-semantics of a key (for API responses).
func (s *Service) ApplyOf(key string) Apply {
	if d, ok := Lookup(key); ok {
		return d.Apply
	}
	return ApplyRestart
}
