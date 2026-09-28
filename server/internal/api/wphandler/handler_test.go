package wphandler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/wphandler"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/webpush"
)

const testJWTSecret = "wphandler-test-secret"

func withAdminAuth(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	token, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1", Login: "admin"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
	return r
}

func newTestRouter(t *testing.T) *chi.Mux {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	cfgRepo := rawrepo.NewNotificationConfigRepo(bundle.DB)
	subRepo := rawrepo.NewPushSubscriptionRepo(bundle.DB)
	svc := webpush.NewService(cfgRepo, subRepo)
	h := wphandler.NewHandler(svc)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth(testJWTSecret))
		h.Mount(r)
	})
	// Wrap POST /vapid outside the group helper so we can test errors too
	return r
}

// TestHandler_VAPIDFlow: POST generates, GET returns key, POST again alreadyGenerated=true.
func TestHandler_VAPIDFlow(t *testing.T) {
	r := newTestRouter(t)

	// 1. POST /api/settings/webpush/vapid — generates keys.
	body, _ := json.Marshal(map[string]string{"subject": "mailto:test@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/webpush/vapid", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("POST /vapid: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var genResp struct {
		PublicKey        string `json:"publicKey"`
		AlreadyGenerated bool   `json:"alreadyGenerated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &genResp); err != nil {
		t.Fatalf("decode POST /vapid response: %v", err)
	}
	if genResp.PublicKey == "" {
		t.Fatal("expected non-empty publicKey after generation")
	}
	if genResp.AlreadyGenerated {
		t.Fatal("expected alreadyGenerated=false on first call")
	}

	// 2. GET /api/settings/webpush/vapid — returns the same public key.
	req = httptest.NewRequest(http.MethodGet, "/api/settings/webpush/vapid", nil)
	req = withAdminAuth(t, req)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /vapid: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var getResp struct {
		PublicKey string `json:"publicKey"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET /vapid response: %v", err)
	}
	if getResp.PublicKey != genResp.PublicKey {
		t.Errorf("GET returned different publicKey: %q vs %q", getResp.PublicKey, genResp.PublicKey)
	}

	// 3. POST /api/settings/webpush/vapid again — alreadyGenerated=true.
	body, _ = json.Marshal(map[string]string{"subject": "mailto:test@example.com"})
	req = httptest.NewRequest(http.MethodPost, "/api/settings/webpush/vapid", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminAuth(t, req)
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("POST /vapid 2nd: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var gen2Resp struct {
		PublicKey        string `json:"publicKey"`
		AlreadyGenerated bool   `json:"alreadyGenerated"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &gen2Resp); err != nil {
		t.Fatalf("decode 2nd POST /vapid response: %v", err)
	}
	if !gen2Resp.AlreadyGenerated {
		t.Fatal("expected alreadyGenerated=true on second call")
	}
	if gen2Resp.PublicKey != genResp.PublicKey {
		t.Errorf("2nd POST returned different publicKey: %q vs %q", gen2Resp.PublicKey, genResp.PublicKey)
	}
}

// TestHandler_GetVAPID_NotFound verifies GET /vapid returns 404 before generation.
func TestHandler_GetVAPID_NotFound(t *testing.T) {
	r := newTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/webpush/vapid", nil)
	req = withAdminAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("expected error field in 404 body")
	}
}

// TestHandler_Subscribe_MissingFields verifies 400 when endpoint is absent.
func TestHandler_Subscribe_MissingFields(t *testing.T) {
	r := newTestRouter(t)

	tests := []struct {
		name string
		body map[string]any
	}{
		{"missing endpoint", map[string]any{"keys": map[string]string{"p256dh": "k1", "auth": "a1"}}},
		{"missing p256dh", map[string]any{"endpoint": "https://example.com", "keys": map[string]string{"auth": "a1"}}},
		{"missing auth", map[string]any{"endpoint": "https://example.com", "keys": map[string]string{"p256dh": "k1"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.body)
			req := httptest.NewRequest(http.MethodPost, "/api/settings/webpush/subscribe", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req = withAdminAuth(t, req)
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Errorf("expected 400, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestHandler_Subscribe_OK verifies a well-formed subscribe request returns 200 {"ok":true}.
func TestHandler_Subscribe_OK(t *testing.T) {
	r := newTestRouter(t)

	body, _ := json.Marshal(map[string]any{
		"endpoint": "https://push.example.com/sub1",
		"keys":     map[string]string{"p256dh": "pubkey", "auth": "authtoken"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/settings/webpush/subscribe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = withAdminAuth(t, req)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode subscribe response: %v", err)
	}
	if !resp["ok"] {
		t.Error("expected ok=true")
	}
}

// Ensure the apierr import is used via ErrorMiddleware internally; this also prevents unused import.
var _ = apierr.ErrForbidden
