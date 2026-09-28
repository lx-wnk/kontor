package providersettings

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

type fakeRepo struct{ rows map[string]bool }

func (f *fakeRepo) List(ctx context.Context) ([]*ent.ProviderSetting, error) {
	out := []*ent.ProviderSetting{}
	for id, en := range f.rows {
		out = append(out, &ent.ProviderSetting{ProviderID: id, Enabled: en})
	}
	return out, nil
}
func (f *fakeRepo) Upsert(ctx context.Context, id string, enabled bool) (*ent.ProviderSetting, error) {
	f.rows[id] = enabled
	return &ent.ProviderSetting{ProviderID: id, Enabled: enabled}, nil
}

func TestService_DBWinsOverFallback(t *testing.T) {
	repo := &fakeRepo{rows: map[string]bool{"codex": true}}
	svc := New(repo, func(string) bool { return false })
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	en := svc.EnabledFunc()
	if !en("codex") {
		t.Fatal("DB row enabled=true must win over fallback")
	}
	if en("gemini") {
		t.Fatal("no DB row → fallback (false) applies")
	}
}

func TestService_SetUpdatesSnapshotLive(t *testing.T) {
	repo := &fakeRepo{rows: map[string]bool{}}
	svc := New(repo, func(string) bool { return false })
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	en := svc.EnabledFunc()
	if en("junie") {
		t.Fatal("junie should start disabled")
	}
	if _, err := svc.Set(context.Background(), "junie", true); err != nil {
		t.Fatal(err)
	}
	if !en("junie") {
		t.Fatal("Set(true) must be visible through the same EnabledFunc immediately")
	}
}
