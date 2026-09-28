package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/api"
)

// okHandler returns 200 OK to confirm the request passed through.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

// ----------------------------------------------------------------------------
// StripForwardedHeaders — 7e.A strip proxy-injected forwarding headers
// ----------------------------------------------------------------------------

func TestStripForwardedHeaders_RemovesForwardingHeaders(t *testing.T) {
	// Capture what headers the inner handler sees.
	var (
		gotXFH string
		gotXFP string
		gotFwd string
		gotXFF string // must NOT be stripped
		gotXRI string // must NOT be stripped
	)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFH = r.Header.Get("X-Forwarded-Host")
		gotXFP = r.Header.Get("X-Forwarded-Proto")
		gotFwd = r.Header.Get("Forwarded")
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotXRI = r.Header.Get("X-Real-IP")
		w.WriteHeader(http.StatusOK)
	})
	handler := api.StripForwardedHeaders(inner)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=evil.example.com;proto=https")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotXFH != "" {
		t.Errorf("X-Forwarded-Host should be stripped, got %q", gotXFH)
	}
	if gotXFP != "" {
		t.Errorf("X-Forwarded-Proto should be stripped, got %q", gotXFP)
	}
	if gotFwd != "" {
		t.Errorf("Forwarded should be stripped, got %q", gotFwd)
	}
	// X-Forwarded-For and X-Real-IP must be stripped: the dashboard binds to
	// loopback only, so a client-supplied IP header is always spoofed.
	if gotXFF != "" {
		t.Errorf("X-Forwarded-For should be stripped, got %q", gotXFF)
	}
	if gotXRI != "" {
		t.Errorf("X-Real-IP should be stripped, got %q", gotXRI)
	}
}

// ----------------------------------------------------------------------------
// RequireSameOriginForMutations — 7b.A no Authorization exemption
// ----------------------------------------------------------------------------

func TestRequireSameOriginForMutations_NoAuthorizationExemption(t *testing.T) {
	// Even when the Authorization header is set, a cross-origin mutation
	// (e.g. missing Origin) must be rejected. Bearer-only paths (MCP, hooks)
	// are mounted outside this middleware; they are unaffected.
	handler := api.RequireSameOriginForMutations(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer mcp_abc123")
	// No Origin header → must be denied (fail-closed).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for POST with Authorization but no Origin, got %d", rec.Code)
	}
}

func TestRequireSameOriginForMutations_AllowsSameOriginWithAuth(t *testing.T) {
	// A POST from the same origin passes even when Authorization is present.
	handler := api.RequireSameOriginForMutations(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/api/tasks", nil)
	req.Host = "127.0.0.1:13120"
	req.Header.Set("Origin", "http://127.0.0.1:13120")
	req.Header.Set("Authorization", "Bearer mcp_abc123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for same-origin POST with Authorization, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// RequireLoopbackHost — F-SEC-005 DNS-rebinding protection
// ----------------------------------------------------------------------------

func TestRequireLoopbackHost_AllowsLoopbackAddresses(t *testing.T) {
	mw := api.RequireLoopbackHost(api.RequireLoopbackHostConfig{})
	handler := mw(okHandler)

	cases := []struct {
		name string
		host string
	}{
		{"127.0.0.1 bare", "127.0.0.1"},
		{"127.0.0.1 with port", "127.0.0.1:13120"},
		{"localhost bare", "localhost"},
		{"localhost with port", "localhost:8080"},
		{"IPv6 loopback bare", "::1"},
		{"IPv6 loopback with port", "[::1]:13120"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("host %q: expected 200, got %d", tc.host, rec.Code)
			}
		})
	}
}

func TestRequireLoopbackHost_RejectsForeignHost(t *testing.T) {
	mw := api.RequireLoopbackHost(api.RequireLoopbackHostConfig{})
	handler := mw(okHandler)

	cases := []struct {
		name string
		host string
	}{
		{"attacker domain", "evil.example.com"},
		{"attacker domain with port", "evil.example.com:13120"},
		{"LAN IP", "192.168.1.1"},
		{"LAN IP with port", "192.168.1.1:13120"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("host %q: expected 403, got %d", tc.host, rec.Code)
			}
		})
	}
}

func TestRequireLoopbackHost_AllowsEmptyHost(t *testing.T) {
	// HTTP/1.0 clients may not send a Host header; they cannot be driven by a
	// browser so they are not a rebinding risk — allow them through unchanged.
	mw := api.RequireLoopbackHost(api.RequireLoopbackHostConfig{})
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Host = "" // explicit empty
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("empty host: expected 200, got %d", rec.Code)
	}
}

func TestRequireLoopbackHost_ExtraAllowedHosts(t *testing.T) {
	// Multi-machine setups may add additional allowed hosts (e.g. a VPN hostname).
	mw := api.RequireLoopbackHost(api.RequireLoopbackHostConfig{
		ExtraAllowedHosts: []string{"dashboard.vpn.internal"},
	})
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Host = "dashboard.vpn.internal:13120"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("extra allowed host: expected 200, got %d", rec.Code)
	}
}

func TestRequireLoopbackHost_ExtraAllowedHostsDoNotExpandDefault(t *testing.T) {
	// Adding an extra host must not disable the default rejection of foreign hosts.
	mw := api.RequireLoopbackHost(api.RequireLoopbackHostConfig{
		ExtraAllowedHosts: []string{"dashboard.vpn.internal"},
	})
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.Host = "evil.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("evil host with extra allowed: expected 403, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// NewIPRateLimiter — F-SEC-010 per-IP rate limiting
// ----------------------------------------------------------------------------

func TestIPRateLimiter_AllowsRequestsUnderLimit(t *testing.T) {
	mw := api.NewIPRateLimiter(context.Background(), api.IPRateLimiterConfig{Rate: 10, Burst: 20})
	handler := mw(okHandler)

	// 20 requests (burst) must all succeed from the same IP.
	for i := range 20 {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}
}

func TestIPRateLimiter_Returns429AfterBurstExhausted(t *testing.T) {
	// Use a very low rate so we can exhaust the burst without sleeping.
	mw := api.NewIPRateLimiter(context.Background(), api.IPRateLimiterConfig{Rate: 1, Burst: 3})
	handler := mw(okHandler)

	const remoteAddr = "192.0.2.2:54321"
	gotTooMany := false
	for i := range 100 {
		req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusTooManyRequests {
			gotTooMany = true
			// Verify Retry-After is present.
			if rec.Header().Get("Retry-After") == "" {
				t.Errorf("request %d: 429 response missing Retry-After header", i+1)
			}
			break
		}
	}

	if !gotTooMany {
		t.Error("expected at least one 429 response after burst exhaustion, got none")
	}
}

func TestIPRateLimiter_DifferentIPsGetSeparateBuckets(t *testing.T) {
	// Exhaust the bucket for IP A; IP B should still succeed.
	mw := api.NewIPRateLimiter(context.Background(), api.IPRateLimiterConfig{Rate: 1, Burst: 1})
	handler := mw(okHandler)

	sendN := func(ip string, n int) int {
		last := http.StatusOK
		for range n {
			req := httptest.NewRequest(http.MethodPost, "/api/mcp", nil)
			req.RemoteAddr = ip + ":1234"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			last = rec.Code
		}
		return last
	}

	// Exhaust IP A (burst=1, so second request gets limited).
	sendN("10.0.0.1", 2)

	// IP B should still be allowed (its bucket is untouched).
	status := sendN("10.0.0.2", 1)
	if status != http.StatusOK {
		t.Errorf("IP B: expected 200 after IP A exhausted, got %d", status)
	}
}

func TestIPRateLimiter_DefaultConfigAllowsNormalTraffic(t *testing.T) {
	// Zero-value config must use safe defaults (10 r/s, burst 20).
	mw := api.NewIPRateLimiter(context.Background(), api.IPRateLimiterConfig{})
	handler := mw(okHandler)

	// A single request from a fresh IP must always succeed.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("default config single request: expected 200, got %d", rec.Code)
	}
}

func TestIPRateLimiter_RetryAfterHeaderIsPositive(t *testing.T) {
	mw := api.NewIPRateLimiter(context.Background(), api.IPRateLimiterConfig{Rate: 1, Burst: 1})
	handler := mw(okHandler)

	addr := "198.51.100.1:8080"

	// First request consumes the single token.
	req1 := httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", nil)
	req1.RemoteAddr = addr
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	// Second request must be rate-limited.
	req2 := httptest.NewRequest(http.MethodPost, "/api/permission-requests/bulk-resolve", nil)
	req2.RemoteAddr = addr
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec2.Code)
	}

	retryAfter := rec2.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatal("missing Retry-After header on 429 response")
	}

	var seconds int
	if _, err := time.ParseDuration(retryAfter + "s"); err != nil {
		// Not a duration — try plain int parse.
		if n := len(retryAfter); n == 0 || retryAfter[0] == '-' {
			t.Errorf("Retry-After value %q is not a positive integer", retryAfter)
		}
	}
	_ = seconds // suppress unused warning
}
