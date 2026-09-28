package providers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/provider"
	"github.com/lx-wnk/kontor/server/internal/providersettings"
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

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	reg, err := provider.NewRegistry(provider.Options{Ollama: provider.NewOllamaClassifier("http://127.0.0.1:1")})
	if err != nil {
		t.Fatal(err)
	}
	svc := providersettings.New(&fakeRepo{rows: map[string]bool{}}, func(string) bool { return false })
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewHandler(reg, svc)
}

func TestHandler_ListIncludesCodexDisabled(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest("GET", "/api/providers", nil)
	w := httptest.NewRecorder()
	if err := h.List(w, req); err != nil {
		t.Fatal(err)
	}
	var got []providerView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range got {
		if p.ID == "codex" {
			found = true
			if p.Enabled {
				t.Fatal("codex should default disabled")
			}
		}
	}
	if !found {
		t.Fatal("codex missing from provider list")
	}
}

func TestHandler_PatchEnables(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest("PATCH", "/api/providers/codex", strings.NewReader(`{"enabled":true}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "codex")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	if err := h.Patch(w, req); err != nil {
		t.Fatal(err)
	}
	var got providerView
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "codex" || !got.Enabled {
		t.Fatalf("expected codex enabled, got %+v", got)
	}
}
