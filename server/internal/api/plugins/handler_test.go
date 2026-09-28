package plugins_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/plugins"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

const testJWTSecret = "test-secret-plugins"

// withAuth adds a valid JWT session cookie so auth.RequireAuth passes.
func withAuth(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1", Login: "testuser"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	return r
}

// TestLegacySettingsPluginsRouteGone asserts the legacy /api/settings/plugins
// route no longer exists after consolidation onto /api/plugins.
func TestLegacySettingsPluginsRouteGone(t *testing.T) {
	h := plugins.NewLifecycle(&fakeLifecycle{})
	r := chi.NewRouter()
	h.MountList(r)
	h.Mount(r)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/settings/plugins", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for removed route /api/settings/plugins, got %d", rr.Code)
	}
}

// fakeLifecycle stands in for *pluginmgmt.Controller.
type fakeLifecycle struct {
	views        []plugins.PluginView
	transition   plugins.PluginView
	transErr     error
	gotID        string
	gotAction    string
	schema       []plugin.SettingField
	values       map[string]string
	getErr       error
	putErr       error
	putGotID     string
	putGotValues map[string]string
}

func (f *fakeLifecycle) List(context.Context) ([]plugins.PluginView, error) { return f.views, nil }

func (f *fakeLifecycle) Transition(_ context.Context, id, action string) (plugins.PluginView, error) {
	f.gotID, f.gotAction = id, action
	if f.transErr != nil {
		return plugins.PluginView{}, f.transErr
	}
	return f.transition, nil
}

func (f *fakeLifecycle) GetSettings(_ context.Context, id string) ([]plugin.SettingField, map[string]string, error) {
	f.gotID = id
	if f.getErr != nil {
		return nil, nil, f.getErr
	}
	return f.schema, f.values, nil
}

func (f *fakeLifecycle) PutSettings(_ context.Context, id string, values map[string]string) error {
	f.putGotID, f.putGotValues = id, values
	return f.putErr
}

func mountLifecycle(t *testing.T, ctl *fakeLifecycle) http.Handler {
	t.Helper()
	h := plugins.NewLifecycle(ctl)
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.MountList(r)
	r.Group(func(r chi.Router) {
		h.Mount(r)
	})
	return r
}

// TestLifecycleList_NonAdminJWTAllowed verifies GET /api/plugins is accessible to
// any authenticated user, not only admins.
func TestLifecycleList_NonAdminJWTAllowed(t *testing.T) {
	h := plugins.NewLifecycle(&fakeLifecycle{views: []plugins.PluginView{}})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.MountList(r)

	nonAdminToken, err := auth.SignJWT(auth.JWTPayload{Sub: "u2", Login: "reader"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: nonAdminToken})
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for non-admin list, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestLifecycleList_ShapeAndLeakGuard verifies the lifecycle DTO carries exactly
// the documented keys and never leaks BaseURL/Env/Addr/Command.
func TestLifecycleList_ShapeAndLeakGuard(t *testing.T) {
	ctl := &fakeLifecycle{views: []plugins.PluginView{{
		ID:              "fake-plugin",
		Name:            "Fake",
		Version:         "1.2.3",
		State:           "active",
		UpdateAvailable: true,
		Capabilities:    []string{"route_extension"},
		HasSettings:     true,
	}}}

	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(items))
	}
	item := items[0]
	for _, required := range []string{"id", "name", "version", "state", "updateAvailable", "healthy", "capabilities", "hasSettings"} {
		if _, ok := item[required]; !ok {
			t.Errorf("response item missing required key %q", required)
		}
	}
	for _, forbidden := range []string{"env", "baseURL", "addr", "command", "descriptor"} {
		if _, ok := item[forbidden]; ok {
			t.Errorf("response item must not contain key %q (F028 leak guard)", forbidden)
		}
	}
	if len(item) != 8 {
		t.Errorf("response item has %d keys, want exactly 8; got: %v", len(item), item)
	}
	if item["id"] != "fake-plugin" || item["state"] != "active" || item["updateAvailable"] != true || item["hasSettings"] != true {
		t.Errorf("fields wrong: %v", item)
	}
}

func TestLifecycleTransition_ActivateReturnsState(t *testing.T) {
	ctl := &fakeLifecycle{transition: plugins.PluginView{ID: "p1", State: "active", Capabilities: []string{}}}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/p1/activate", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if ctl.gotID != "p1" || ctl.gotAction != "activate" {
		t.Errorf("controller got id=%q action=%q", ctl.gotID, ctl.gotAction)
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["state"] != "active" {
		t.Errorf("state: got %v, want active", resp["state"])
	}
}

func TestLifecycleTransition_UnknownID_400(t *testing.T) {
	ctl := &fakeLifecycle{transErr: fmt.Errorf("ctl: %w: %q", plugin.ErrUnknownPlugin, "nope")}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/nope/activate", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLifecycleTransition_InvalidAction_400(t *testing.T) {
	ctl := &fakeLifecycle{}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/p1/frobnicate", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if ctl.gotAction != "" {
		t.Errorf("invalid action must not reach controller, got %q", ctl.gotAction)
	}
}

func TestLifecycleTransition_IllegalTransition_409(t *testing.T) {
	ctl := &fakeLifecycle{transErr: fmt.Errorf("%w: p1 already installed", plugin.ErrIllegalTransition)}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/p1/install", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLifecycleTransition_UpdateAccepted(t *testing.T) {
	ctl := &fakeLifecycle{transition: plugins.PluginView{ID: "p1", State: "active", Capabilities: []string{}}}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/p1/update", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for update action, got %d: %s", rr.Code, rr.Body.String())
	}
	if ctl.gotAction != "update" {
		t.Errorf("controller action: got %q, want update", ctl.gotAction)
	}
}

func TestLifecycleTransition_UpdateIllegalTransition_409(t *testing.T) {
	ctl := &fakeLifecycle{transErr: fmt.Errorf("%w: p1 not installed", plugin.ErrIllegalTransition)}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/p1/update", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409 for illegal update, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLifecycleTransition_HookFailure_500(t *testing.T) {
	ctl := &fakeLifecycle{transErr: errors.New("activate hook: connection refused")}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/p1/activate", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLifecycleGetSettings_MasksSecrets(t *testing.T) {
	ctl := &fakeLifecycle{
		schema: []plugin.SettingField{
			{Key: "endpoint", Type: "url", Label: "Endpoint"},
			{Key: "token", Type: "string", Label: "Token", Secret: true},
		},
		values: map[string]string{"endpoint": "https://x", "token": pluginsettingsMasked},
	}
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/plugins/p1/settings", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Schema []plugin.SettingField `json:"schema"`
		Values map[string]string     `json:"values"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Schema) != 2 {
		t.Fatalf("expected 2 schema fields, got %d", len(resp.Schema))
	}
	if resp.Values["token"] != pluginsettingsMasked {
		t.Errorf("secret token must be masked, got %q", resp.Values["token"])
	}
	if resp.Values["endpoint"] != "https://x" {
		t.Errorf("non-secret value wrong: %q", resp.Values["endpoint"])
	}
}

func TestLifecyclePutSettings_Persists(t *testing.T) {
	ctl := &fakeLifecycle{}
	body := `{"values":{"endpoint":"https://y","token":"new-secret"}}`
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/plugins/p1/settings", strings.NewReader(body)))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if ctl.putGotID != "p1" {
		t.Errorf("put got id=%q", ctl.putGotID)
	}
	if ctl.putGotValues["endpoint"] != "https://y" || ctl.putGotValues["token"] != "new-secret" {
		t.Errorf("put values wrong: %v", ctl.putGotValues)
	}
}

func TestLifecyclePutSettings_UnknownKey_400(t *testing.T) {
	ctl := &fakeLifecycle{putErr: fmt.Errorf("%w: %q", plugin.ErrUnknownKey, "bogus")}
	body := `{"values":{"bogus":"x"}}`
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/plugins/p1/settings", strings.NewReader(body)))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLifecyclePutSettings_InvalidValue_400(t *testing.T) {
	ctl := &fakeLifecycle{putErr: fmt.Errorf("%w: field %q requires an integer", plugin.ErrInvalidValue, "count")}
	body := `{"values":{"count":"abc"}}`
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/plugins/p1/settings", strings.NewReader(body)))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

const pluginsettingsMasked = plugin.MaskedSentinel

func TestLifecycleTransition_MalformedID_400(t *testing.T) {
	ctl := &fakeLifecycle{}
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/plugins/My-PLUGIN/activate", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed id, got %d: %s", rr.Code, rr.Body.String())
	}
	if ctl.gotID != "" {
		t.Errorf("malformed id must not reach controller, got %q", ctl.gotID)
	}
}

func TestLifecycleGetSettings_MalformedID_400(t *testing.T) {
	ctl := &fakeLifecycle{}
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/plugins/UPPER/settings", nil))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed id, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestLifecyclePutSettings_MalformedID_400(t *testing.T) {
	ctl := &fakeLifecycle{}
	body := `{"values":{}}`
	req := withAuth(t, httptest.NewRequest(http.MethodPut, "/api/plugins/BAD_ID/settings", strings.NewReader(body)))
	rr := httptest.NewRecorder()
	mountLifecycle(t, ctl).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed id, got %d: %s", rr.Code, rr.Body.String())
	}
}
