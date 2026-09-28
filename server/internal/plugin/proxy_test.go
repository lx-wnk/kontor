package plugin_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// TestNewReverseProxy_ProxiesAndStripsPrefixAndHeaders is a regression test for the
// bug where NewReverseProxy set both Director (via httputil.NewSingleHostReverseProxy)
// and Rewrite, which net/http rejects and returns 502 for every proxied request.
// The fix uses &httputil.ReverseProxy{Rewrite: ...} only (no Director).
func TestNewReverseProxy_ProxiesAndStripsPrefixAndHeaders(t *testing.T) {
	// Track what the backend actually received.
	var backendPath string
	var cookieHeader string
	var authHeader string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendPath = r.URL.Path
		cookieHeader = r.Header.Get("Cookie")
		authHeader = r.Header.Get("Authorization")
		// Set an explicit Content-Length so the test can assert the proxy strips it
		// (the gzip middleware recompresses the body downstream — a forwarded
		// upstream Content-Length would yield ERR_CONTENT_LENGTH_MISMATCH).
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer backend.Close()

	entry := plugin.Entry{
		Descriptor: plugin.Descriptor{
			Contract: plugin.CurrentContract,
			Name:     "Test Plugin",
			Addr:     strings.TrimPrefix(backend.URL, "http://"),
		},
	}

	const stripPrefix = "/api/settings/plugins/test"
	h := plugin.NewReverseProxy(entry, stripPrefix)

	req := httptest.NewRequest(http.MethodGet, stripPrefix+"/health", nil)
	req.Header.Set("Cookie", "session=abc123")
	req.Header.Set("Authorization", "Bearer secret-token")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Core regression assertion: the old code (Director + Rewrite) caused 502.
	require.Equal(t, http.StatusOK, rec.Code, "expected 200; old buggy code returned 502")

	// Prefix must be stripped so the backend sees only the suffix.
	assert.Equal(t, "/health", backendPath, "backend should receive path with prefix stripped")

	// Sensitive headers must not reach the plugin backend.
	assert.Empty(t, cookieHeader, "Cookie header must be stripped before forwarding")
	assert.Empty(t, authHeader, "Authorization header must be stripped before forwarding")

	// Upstream Content-Length must be stripped so the downstream gzip middleware
	// can recompress without advertising a stale length (ERR_CONTENT_LENGTH_MISMATCH).
	assert.Empty(t, rec.Header().Get("Content-Length"), "proxy must drop upstream Content-Length")
}
