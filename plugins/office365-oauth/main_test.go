package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	oauthkit "github.com/lx-wnk/kontor-plugin-oauthkit"
)

const (
	testO365PluginSecret = "a-plugin-secret-that-is-at-least-32-chars"
)

// newO365Handler builds a handler wired to optional mock servers.
// Pass nil for servers that are not needed by the test.
func newO365Handler(tokenSrv, meSrv, memberSrv, coreSrv *httptest.Server) *handler {
	dashURL := "http://127.0.0.1:13120"
	if coreSrv != nil {
		dashURL = coreSrv.URL
	}
	tURL := "https://login.microsoftonline.com/testtenant/oauth2/v2.0/token"
	if tokenSrv != nil {
		tURL = tokenSrv.URL
	}
	meURL := graphMeURL
	if meSrv != nil {
		meURL = meSrv.URL
	}
	memberURL := graphMemberOfURL
	if memberSrv != nil {
		memberURL = memberSrv.URL
	}
	return &handler{
		clientID:         "test-client-id",
		clientSecret:     "test-client-secret",
		tenantID:         "testtenant",
		dashboardURL:     dashURL,
		pluginSecret:     testO365PluginSecret,
		callbackURL:      "http://127.0.0.1:19002/callback",
		httpClient:       &http.Client{Timeout: 5 * time.Second},
		tokenURL:         tURL,
		msGraphMeURL:     meURL,
		msGraphMemberURL: memberURL,
	}
}

// --- /health ---

func TestHealth(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	oauthkit.Health(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body map[string]bool
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body["ok"] {
		t.Errorf("expected {ok:true}, got %v", body)
	}
}

// --- /login ---

func TestLogin_MissingNonce(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	h.login(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing nonce")
}

func TestLogin_SetsCookieAndRedirects(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/login?nonce=testnonce", nil)
	rr := httptest.NewRecorder()
	h.login(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}

	var stateCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == stateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("state cookie not set")
	}
	if !strings.Contains(stateCookie.Value, ".testnonce") {
		t.Errorf("nonce not embedded in state cookie: %q", stateCookie.Value)
	}

	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "login.microsoftonline.com") {
		t.Errorf("unexpected redirect target: %q", loc)
	}
	if !strings.Contains(loc, "testtenant") {
		t.Errorf("tenant not in auth URL: %q", loc)
	}
}

func TestLogin_GroupScopeAdded(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	h.allowedGroup = "some-group-id"
	req := httptest.NewRequest(http.MethodGet, "/login?nonce=n", nil)
	rr := httptest.NewRecorder()
	h.login(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "GroupMember.Read.All") {
		t.Errorf("GroupMember.Read.All scope missing from auth URL: %q", loc)
	}
}

// --- /callback error cases ---

func TestCallback_MissingStateCookie(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/callback?state=x&code=y", nil)
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing state cookie")
}

func TestCallback_StateMismatch(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/callback?state=wrong&code=y", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "correct.nonce"})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "state mismatch")
}

func TestCallback_MalformedState_NoDot(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	const state = "nodothere"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=y", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "malformed state")
}

func TestCallback_EmptyNonce(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	const state = "csrf." // dot present but nothing after
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=y", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "empty nonce")
}

func TestCallback_OAuthProviderError(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet,
		"/callback?state="+state+"&error=access_denied&error_description=User+denied", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "authentication denied by provider")
}

func TestCallback_MissingCode(t *testing.T) {
	h := newO365Handler(nil, nil, nil, nil)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state, nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing code")
}

// --- /callback integration ---

func TestCallback_TokenExchangeError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_grant", http.StatusUnprocessableEntity)
	}))
	defer tokenSrv.Close()

	h := newO365Handler(tokenSrv, nil, nil, nil)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=bad", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "code exchange failed")
}

func TestCallback_Success_NoGroupCheck(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"ms_tok"}`)
	}))
	defer tokenSrv.Close()

	meSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"ms-user-id","displayName":"Alice","userPrincipalName":"alice@example.com"}`)
	}))
	defer meSrv.Close()

	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/session" {
			http.NotFound(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: "sess_xyz"})
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{}")
	}))
	defer coreSrv.Close()

	h := newO365Handler(tokenSrv, meSrv, nil, coreSrv)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=authcode", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != coreSrv.URL+"/" {
		t.Errorf("unexpected redirect: %q", loc)
	}
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "auth_token" && c.Value == "sess_xyz" {
			found = true
		}
	}
	if !found {
		t.Error("auth_token cookie not forwarded")
	}
}

func TestCallback_GroupCheck_NotMember(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok"}`)
	}))
	defer tokenSrv.Close()

	meSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"uid","displayName":"Bob","userPrincipalName":"bob@example.com"}`)
	}))
	defer meSrv.Close()

	// memberOf returns empty list — user is not a member of the required group.
	memberSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"value":[]}`)
	}))
	defer memberSrv.Close()

	h := newO365Handler(tokenSrv, meSrv, memberSrv, nil)
	h.allowedGroup = "required-group-id"
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrorBody(t, rr, "not a member of the required group")
}

func TestCallback_GroupCheck_Member(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok"}`)
	}))
	defer tokenSrv.Close()

	meSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":"uid","displayName":"Carol","userPrincipalName":"carol@example.com"}`)
	}))
	defer meSrv.Close()

	// memberOf returns the required group.
	memberSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"value":[{"id":"required-group-id"}]}`)
	}))
	defer memberSrv.Close()

	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: "sess_grp"})
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{}")
	}))
	defer coreSrv.Close()

	h := newO365Handler(tokenSrv, meSrv, memberSrv, coreSrv)
	h.allowedGroup = "required-group-id"
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rr.Code, rr.Body.String())
	}
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "auth_token" && c.Value == "sess_grp" {
			found = true
		}
	}
	if !found {
		t.Error("auth_token cookie not forwarded after group-membership approval")
	}
}

// --- isMember unit tests ---

func TestIsMember_Found(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"value":[{"id":"group-a"},{"id":"target-group"}]}`)
	}))
	defer srv.Close()

	h := newO365Handler(nil, nil, srv, nil)
	ok, err := h.isMember(context.Background(), "tok", "target-group")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected member=true")
	}
}

func TestIsMember_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"value":[{"id":"other-group"}]}`)
	}))
	defer srv.Close()

	h := newO365Handler(nil, nil, srv, nil)
	ok, err := h.isMember(context.Background(), "tok", "target-group")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected member=false")
	}
}

func TestIsMember_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	h := newO365Handler(nil, nil, srv, nil)
	_, err := h.isMember(context.Background(), "tok", "group")
	if err == nil {
		t.Fatal("expected error for HTTP 403")
	}
}

func TestIsMember_InsecureNextLink(t *testing.T) {
	// First page contains an @odata.nextLink pointing to a non-Microsoft host.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"value":[],"@odata.nextLink":"http://evil.example.com/steal-tokens"}`)
	}))
	defer srv.Close()

	h := newO365Handler(nil, nil, srv, nil)
	ok, err := h.isMember(context.Background(), "tok", "group")
	if err == nil {
		t.Fatal("expected error for insecure nextLink")
	}
	if !strings.Contains(err.Error(), "unexpected nextLink host") {
		t.Errorf("unexpected error message: %v", err)
	}
	if ok {
		t.Error("expected member=false on error")
	}
}

func TestIsMember_PaginationAcrossPages(t *testing.T) {
	t.Run("target found on page 2", func(t *testing.T) {
		var reqCount int

		fakeRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			reqCount++
			var body string
			switch req.URL.String() {
			case "https://graph.microsoft.com/v1.0/me/memberOf?$select=id&$top=999":
				body = `{"value":[{"id":"other-1"},{"id":"other-2"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/memberOf?$skiptoken=PAGE2"}`
			case "https://graph.microsoft.com/v1.0/me/memberOf?$skiptoken=PAGE2":
				body = `{"value":[{"id":"target-group"}]}`
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected URL"}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		})

		h := newO365Handler(nil, nil, nil, nil)
		h.httpClient = &http.Client{Transport: fakeRT}
		h.msGraphMemberURL = "https://graph.microsoft.com/v1.0/me/memberOf?$select=id&$top=999"

		ok, err := h.isMember(context.Background(), "tok", "target-group")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Error("expected member=true (target found on page 2)")
		}
		if reqCount != 2 {
			t.Errorf("expected 2 requests (one per page), got %d", reqCount)
		}
	})

	t.Run("target absent on both pages", func(t *testing.T) {
		var reqCount int

		fakeRT := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			reqCount++
			var body string
			switch req.URL.String() {
			case "https://graph.microsoft.com/v1.0/me/memberOf?$select=id&$top=999":
				body = `{"value":[{"id":"other-1"},{"id":"other-2"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/memberOf?$skiptoken=PAGE2"}`
			case "https://graph.microsoft.com/v1.0/me/memberOf?$skiptoken=PAGE2":
				body = `{"value":[{"id":"yet-another-group"}]}`
			default:
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected URL"}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		})

		h := newO365Handler(nil, nil, nil, nil)
		h.httpClient = &http.Client{Transport: fakeRT}
		h.msGraphMemberURL = "https://graph.microsoft.com/v1.0/me/memberOf?$select=id&$top=999"

		ok, err := h.isMember(context.Background(), "tok", "target-group")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Error("expected member=false (target absent on both pages)")
		}
		if reqCount != 2 {
			t.Errorf("expected 2 requests (one per page), got %d", reqCount)
		}
	})
}

// roundTripperFunc allows an inline func to implement http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// --- helpers ---

func assertErrorBody(t *testing.T, rr *httptest.ResponseRecorder, contains string) {
	t.Helper()
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), contains) {
		t.Errorf("expected body to contain %q, got: %s", contains, body)
	}
}
