package main

import (
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
	testPluginSecret = "a-plugin-secret-that-is-at-least-32-chars"
)

// newHandler builds a handler wired to optional mock servers.
// Pass nil for servers that are not needed by the test.
func newHandler(tokenSrv, userSrv, coreSrv *httptest.Server) *handler {
	dashURL := "http://127.0.0.1:13120"
	if coreSrv != nil {
		dashURL = coreSrv.URL
	}
	tURL := githubTokenURL
	if tokenSrv != nil {
		tURL = tokenSrv.URL
	}
	uURL := githubUserURL
	if userSrv != nil {
		uURL = userSrv.URL
	}
	return &handler{
		clientID:     "test-client-id",
		clientSecret: "test-client-secret",
		dashboardURL: dashURL,
		pluginSecret: testPluginSecret,
		callbackURL:  "http://127.0.0.1:19001/callback",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		tokenURL:     tURL,
		userURL:      uURL,
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
	h := newHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rr := httptest.NewRecorder()
	h.login(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing nonce")
}

func TestLogin_SetsCookieAndRedirects(t *testing.T) {
	h := newHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/login?nonce=abc123", nil)
	rr := httptest.NewRecorder()
	h.login(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}

	// state cookie must be set
	cookies := rr.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == stateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("state cookie not set")
	}
	// cookie value must contain the nonce after the first dot
	if !strings.Contains(stateCookie.Value, ".abc123") {
		t.Errorf("nonce not embedded in state cookie: %q", stateCookie.Value)
	}

	// Location must point to GitHub auth
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, githubAuthURL) {
		t.Errorf("unexpected redirect target: %q", loc)
	}
}

// --- /callback error cases ---

func TestCallback_MissingStateCookie(t *testing.T) {
	h := newHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/callback?state=x&code=y", nil)
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing state cookie")
}

func TestCallback_StateMismatch(t *testing.T) {
	h := newHandler(nil, nil, nil)
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
	h := newHandler(nil, nil, nil)
	const state = "nodothere"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=y", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing nonce")
}

func TestCallback_MalformedState_EmptyNonce(t *testing.T) {
	h := newHandler(nil, nil, nil)
	const state = "csrf." // dot present but nothing after it
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=y", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "empty nonce")
}

func TestCallback_MissingCode(t *testing.T) {
	h := newHandler(nil, nil, nil)
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

func TestCallback_Success(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"gho_testtoken"}`)
	}))
	defer tokenSrv.Close()

	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":42,"login":"alice","name":"Alice","avatar_url":"https://example.com/a.png"}`)
	}))
	defer userSrv.Close()

	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/session" {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: "sess_abc"})
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{}")
	}))
	defer coreSrv.Close()

	h := newHandler(tokenSrv, userSrv, coreSrv)
	const state = "csrf1234.nonce5678"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=authcode", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rr.Code, rr.Body.String())
	}
	if loc := rr.Header().Get("Location"); loc != coreSrv.URL+"/" {
		t.Errorf("unexpected redirect location: %q", loc)
	}
	// auth_token cookie must be forwarded to the browser
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == "auth_token" && c.Value == "sess_abc" {
			found = true
		}
	}
	if !found {
		t.Error("auth_token cookie not forwarded in response")
	}
}

func TestCallback_ExchangeError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad_verification_code", http.StatusUnprocessableEntity)
	}))
	defer tokenSrv.Close()

	h := newHandler(tokenSrv, nil, nil)
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

func TestCallback_GetUserError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok"}`)
	}))
	defer tokenSrv.Close()

	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer userSrv.Close()

	h := newHandler(tokenSrv, userSrv, nil)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "failed to fetch user profile")
}

func TestCallback_CoreSessionError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok"}`)
	}))
	defer tokenSrv.Close()

	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":1,"login":"bob","name":"Bob","avatar_url":""}`)
	}))
	defer userSrv.Close()

	// Core returns 500 — simulate a broken core session endpoint.
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer coreSrv.Close()

	h := newHandler(tokenSrv, userSrv, coreSrv)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "failed to create session")
}

func TestCallback_CoreSessionMissingCookie(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"tok"}`)
	}))
	defer tokenSrv.Close()

	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":1,"login":"bob","name":"Bob","avatar_url":""}`)
	}))
	defer userSrv.Close()

	// Core responds 200 but does NOT set an auth_token cookie.
	coreSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "{}")
	}))
	defer coreSrv.Close()

	h := newHandler(tokenSrv, userSrv, coreSrv)
	const state = "csrf.nonce"
	req := httptest.NewRequest(http.MethodGet, "/callback?state="+state+"&code=code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: state})
	rr := httptest.NewRecorder()
	h.callback(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "failed to create session")
}

// --- /capabilities/auth/authorize-url ---

func TestAuthorizeURL_BuildsCorrectURL(t *testing.T) {
	h := newHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet,
		"/capabilities/auth/authorize-url?state=mystate&redirect_uri=http%3A%2F%2Flocalhost%2Fcb", nil)
	rr := httptest.NewRecorder()
	h.authorizeURL(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.HasPrefix(body["url"], githubAuthURL) {
		t.Errorf("unexpected url: %q", body["url"])
	}
	if !strings.Contains(body["url"], "state=mystate") {
		t.Errorf("state not in url: %q", body["url"])
	}
	if !strings.Contains(body["url"], "client_id=test-client-id") {
		t.Errorf("client_id not in url: %q", body["url"])
	}
}

// --- /capabilities/auth/exchange ---

func TestExchangeHandler_InvalidBody(t *testing.T) {
	h := newHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/capabilities/auth/exchange",
		strings.NewReader("not-json"))
	rr := httptest.NewRecorder()
	h.exchange(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "invalid request body")
}

func TestExchangeHandler_Success(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"access_token":"gho_abc"}`)
	}))
	defer tokenSrv.Close()

	h := newHandler(tokenSrv, nil, nil)
	body := `{"code":"someauthcode","redirect_uri":"http://localhost/cb"}`
	req := httptest.NewRequest(http.MethodPost, "/capabilities/auth/exchange",
		strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.exchange(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["token"] != "gho_abc" {
		t.Errorf("expected token gho_abc, got %q", resp["token"])
	}
}

func TestExchangeHandler_GitHubError(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"error":"bad_verification_code"}`)
	}))
	defer tokenSrv.Close()

	h := newHandler(tokenSrv, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/capabilities/auth/exchange",
		strings.NewReader(`{"code":"bad","redirect_uri":""}`))
	rr := httptest.NewRecorder()
	h.exchange(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "code exchange failed")
}

// --- /capabilities/auth/user ---

func TestUserHandler_MissingBearer(t *testing.T) {
	h := newHandler(nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/capabilities/auth/user", nil)
	rr := httptest.NewRecorder()
	h.user(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "missing Bearer token")
}

func TestUserHandler_Success(t *testing.T) {
	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"id":99,"login":"carol","name":"Carol","avatar_url":"https://x.com/a.png"}`)
	}))
	defer userSrv.Close()

	h := newHandler(nil, userSrv, nil)
	req := httptest.NewRequest(http.MethodGet, "/capabilities/auth/user", nil)
	req.Header.Set("Authorization", "Bearer gho_tok")
	rr := httptest.NewRecorder()
	h.user(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var profile oauthUserProfile
	if err := json.Unmarshal(rr.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if profile.Login != "carol" {
		t.Errorf("expected login carol, got %q", profile.Login)
	}
}

func TestUserHandler_GitHubError(t *testing.T) {
	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer userSrv.Close()

	h := newHandler(nil, userSrv, nil)
	req := httptest.NewRequest(http.MethodGet, "/capabilities/auth/user", nil)
	req.Header.Set("Authorization", "Bearer bad_token")
	rr := httptest.NewRecorder()
	h.user(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
	assertErrorBody(t, rr, "failed to fetch user profile")
}

// --- helpers ---

// assertErrorBody checks that the response body contains a JSON {"error":"..."} with the given substring.
func assertErrorBody(t *testing.T, rr *httptest.ResponseRecorder, contains string) {
	t.Helper()
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), contains) {
		t.Errorf("expected body to contain %q, got: %s", contains, body)
	}
}
