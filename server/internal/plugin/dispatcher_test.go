package plugin_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// fakeResolver implements plugin.ProxyResolver.
type fakeResolver struct {
	entry plugin.Entry
	ok    bool
}

func (f fakeResolver) Lookup(string) (plugin.Entry, bool) { return f.entry, f.ok }

func mountDispatcher(res plugin.ProxyResolver) http.Handler {
	r := chi.NewRouter()
	r.Handle("/api/plugins/{id}/proxy/*", plugin.NewDispatcher(res))
	return r
}

func TestDispatcherMalformedIDReturns400(t *testing.T) {
	h := mountDispatcher(fakeResolver{ok: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/Bad_ID/proxy/x", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDispatcherUnknownOrStoppedReturns503(t *testing.T) {
	h := mountDispatcher(fakeResolver{ok: false})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/voice/proxy/x", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestDispatcherUnhealthyReturns503(t *testing.T) {
	res := fakeResolver{ok: true, entry: plugin.Entry{
		Descriptor: plugin.Descriptor{ID: "voice", Addr: "127.0.0.1:1", Capabilities: []string{plugin.CapRouteExtension}},
	}} // healthy defaults false
	h := mountDispatcher(res)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/voice/proxy/x", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestDispatcherNonProxyableReturns503(t *testing.T) {
	res := fakeResolver{ok: true, entry: plugin.NewHealthyEntryForTest(
		plugin.Descriptor{ID: "authonly", Addr: "127.0.0.1:1", Capabilities: []string{plugin.CapAuthProvider}})}
	h := mountDispatcher(res)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/plugins/authonly/proxy/x", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestDispatcherProxiesHealthyRouteExtension(t *testing.T) {
	var gotPath string
	var gotCookie string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	addr := strings.TrimPrefix(upstream.URL, "http://")

	res := fakeResolver{ok: true, entry: plugin.NewHealthyEntryForTest(
		plugin.Descriptor{ID: "voice", Addr: addr, Capabilities: []string{plugin.CapRouteExtension}})}
	h := mountDispatcher(res)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/plugins/voice/proxy/hello", nil)
	req.Header.Set("Cookie", "session=secret")
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
	require.Equal(t, "/hello", gotPath, "prefix must be stripped to plugin-relative path")
	require.Empty(t, gotCookie, "Cookie must be stripped before forwarding")
}
