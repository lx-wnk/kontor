package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

// ErrNoMasterKey is returned by SetValidated for a secret key when the
// secretbox master key could not be resolved. The CLI is the lockout
// recovery path, so it refuses the write instead of ever storing a secret
// in clear.
var ErrNoMasterKey = errors.New("cli: secretbox master key unavailable")

// ErrMaskedValueRejected is returned by SetValidated when the submitted
// value for a secret key is secretbox.MaskedSentinel. settings.Service.Set
// treats the sentinel as "leave unchanged" because it always has the
// previous ciphertext to fall back to; SetValidated is a raw upsert with no
// such fallback, so silently accepting it would encrypt the literal
// "********" over the real secret. An operator can reach this by
// round-tripping list/get's own masked output back into set (by hand, or by
// scripting list into set), so refusing loudly is more useful than a silent
// no-op here.
var ErrMaskedValueRejected = errors.New("cli: refusing to store the mask sentinel as a secret value")

// dbStore opens the dashboard SQLite file directly (no HTTP), so settings can
// be changed while the server is down — the lockout-safe escape hatch.
type dbStore struct {
	client     *ent.Client
	repo       repo.AppSettingRepo
	grants     repo.GrantRepo
	grantUsage repo.GrantUsageRepo
	caps       repo.CapabilityRepo
}

// openDBStore opens (and migrates) the dashboard DB at path. It does not
// touch the secretbox master key: list/get/grants/caps and a non-secret set
// never need it, and LoadOrGenerateMasterKey persists a new key file when
// none exists — a read-only command (or one run under a different HOME, e.g.
// via sudo) must not have that side effect. SetValidated resolves the key
// lazily, only for an actual secret write.
func openDBStore(path string) (*dbStore, error) {
	bundle, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	return &dbStore{
		client:     bundle.Client,
		repo:       repo.NewAppSettingRepo(bundle.Client),
		grants:     repo.NewGrantRepo(bundle.Client),
		grantUsage: repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
		caps:       repo.NewCapabilityRepo(bundle.Client),
	}, nil
}

// loadSecretBox resolves the secretbox master key exactly as the server does
// (serverapp/di.go) and builds a Box from it.
func loadSecretBox() (*secretbox.Box, error) {
	masterKey, err := secretbox.LoadOrGenerateMasterKey(os.Getenv("DASHBOARD_SECRET_KEY"))
	if err != nil {
		return nil, fmt.Errorf("resolve master key: %w", err)
	}
	box, err := secretbox.New(masterKey)
	if err != nil {
		return nil, fmt.Errorf("build secret box: %w", err)
	}
	return box, nil
}

func (s *dbStore) Close() error { return s.client.Close() }

func (s *dbStore) Get(ctx context.Context, key string) (string, bool, error) {
	return s.repo.Get(ctx, key)
}

func (s *dbStore) List(ctx context.Context) (map[string]string, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Key] = r.Value
	}
	return m, nil
}

func (s *dbStore) Set(ctx context.Context, key, value string) error {
	_, err := s.repo.Upsert(ctx, key, value)
	return err
}

// SetValidated checks the registry before writing. A secret definition is
// encrypted with the secretbox master key — resolved lazily here, not in
// openDBStore (see its doc comment) — and never written in clear. The mask
// sentinel is refused rather than treated as "leave unchanged": see
// ErrMaskedValueRejected. If the master key cannot be resolved, the write is
// refused with ErrNoMasterKey rather than silently falling back to
// plaintext.
//
// An empty value CLEARS a secret, matching settings.Service.Set exactly — the
// two surfaces write the same shape (empty ciphertext, empty nonce) so a key
// cleared here reads as unset everywhere, not as the mask. The two must agree
// or clearing through the CLI leaves a row the Settings panel then counts as
// a filled field: it would show "********", permit the trio save, and the
// next boot would fail on the apiKey it was told was there. Clearing runs
// before loadSecretBox on purpose — removing a secret must not depend on
// being able to resolve a master key, least of all in the recovery command.
func (s *dbStore) SetValidated(ctx context.Context, key, value string) error {
	def, ok := settings.Lookup(key)
	if !ok {
		return errUnknownKey(key)
	}
	if err := def.Validate(value); err != nil {
		return err
	}
	if !def.Secret {
		return s.Set(ctx, key, value)
	}
	if value == secretbox.MaskedSentinel {
		return fmt.Errorf("%w: %q", ErrMaskedValueRejected, key)
	}
	if value == "" {
		_, err := s.repo.UpsertSecret(ctx, key, "", "")
		return err
	}
	box, err := loadSecretBox()
	if err != nil {
		return fmt.Errorf("%w: cannot set %q: %v", ErrNoMasterKey, key, err)
	}
	ciphertext, nonce, err := box.Encrypt(value)
	if err != nil {
		return fmt.Errorf("encrypt %q: %w", key, err)
	}
	_, err = s.repo.UpsertSecret(ctx, key, ciphertext, nonce)
	return err
}

func errUnknownKey(key string) error { return fmt.Errorf("unknown setting key %q", key) }
