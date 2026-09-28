package providersettings

import (
	"context"
	"fmt"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/provider"
)

// repoIface is the subset of repo.ProviderSettingRepo the service needs,
// declared locally so tests can fake it without an ent client.
type repoIface interface {
	List(ctx context.Context) ([]*ent.ProviderSetting, error)
	Upsert(ctx context.Context, providerID string, enabled bool) (*ent.ProviderSetting, error)
}

// Service holds the DB-backed per-provider enable snapshot. The snapshot is
// read by the scan path through EnabledFunc on every tick and updated on Set,
// so a UI toggle takes effect on the next scan with no restart.
type Service struct {
	repo     repoIface
	fallback provider.EnabledFunc

	mu   sync.RWMutex
	rows map[string]bool // provider_id -> enabled; presence means a DB row exists
}

// New builds a Service. fallback is consulted for providers with no DB row.
func New(repo repoIface, fallback provider.EnabledFunc) *Service {
	if fallback == nil {
		fallback = func(string) bool { return false }
	}
	return &Service{repo: repo, fallback: fallback, rows: map[string]bool{}}
}

// Load reads all persisted rows into the snapshot. Call once at startup.
func (s *Service) Load(ctx context.Context) error {
	if s.repo == nil {
		return fmt.Errorf("providersettings.Load: no repository configured")
	}
	rows, err := s.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("providersettings.Load: %w", err)
	}
	m := make(map[string]bool, len(rows))
	for _, r := range rows {
		m[r.ProviderID] = r.Enabled
	}
	s.mu.Lock()
	s.rows = m
	s.mu.Unlock()
	return nil
}

// IsEnabled reports whether a provider is enabled: a DB row wins; otherwise the
// fallback decides.
func (s *Service) IsEnabled(id string) bool {
	s.mu.RLock()
	en, ok := s.rows[id]
	s.mu.RUnlock()
	if ok {
		return en
	}
	return s.fallback(id)
}

// EnabledFunc returns a live provider.EnabledFunc bound to this service.
func (s *Service) EnabledFunc() provider.EnabledFunc {
	return s.IsEnabled
}

// Set persists enabled-state for a provider and updates the live snapshot.
func (s *Service) Set(ctx context.Context, id string, enabled bool) (*ent.ProviderSetting, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("providersettings.Set: no repository configured")
	}
	row, err := s.repo.Upsert(ctx, id, enabled)
	if err != nil {
		return nil, fmt.Errorf("providersettings.Set: %w", err)
	}
	s.mu.Lock()
	s.rows[id] = enabled
	s.mu.Unlock()
	return row, nil
}
