package cloud

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	validFireURL = "https://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire"
	testToken    = "sk-ant-oat01-SECRET-routine-token"
)

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = r.target.Scheme
	clone.URL.Host = r.target.Host
	clone.Host = r.target.Host
	return r.base.RoundTrip(clone)
}

func clientFor(t *testing.T, srv *httptest.Server) RoutineClient {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return RoutineClient{HTTP: &http.Client{Transport: rewriteTransport{target: target, base: srv.Client().Transport}}}
}

func TestValidateFireURL(t *testing.T) {
	valid := []string{
		validFireURL,
		"https://api.anthropic.com/v1/claude_code/routines/trig_a/fire",
	}
	for _, s := range valid {
		if err := ValidateFireURL(s); err != nil {
			t.Errorf("ValidateFireURL(%q) = %v, want nil", s, err)
		}
	}

	invalid := map[string]string{
		"http scheme":           "http://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire",
		"other host":            "https://example.com/v1/claude_code/routines/trig_01AbC9/fire",
		"suffix host":           "https://api.anthropic.com.evil.com/v1/claude_code/routines/trig_01AbC9/fire",
		"userinfo":              "https://x@api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire",
		"userinfo lookalike":    "https://api.anthropic.com@evil.com/v1/claude_code/routines/trig_01AbC9/fire",
		"extra path segment":    "https://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire/more",
		"extra leading segment": "https://api.anthropic.com/x/v1/claude_code/routines/trig_01AbC9/fire",
		"query string":          "https://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire?x=1",
		"fragment":              "https://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire#x",
		"trig without id":       "https://api.anthropic.com/v1/claude_code/routines/trig_/fire",
		"no trig prefix":        "https://api.anthropic.com/v1/claude_code/routines/01AbC9/fire",
		"path traversal":        "https://api.anthropic.com/v1/claude_code/routines/trig_../fire",
		"traversal segment":     "https://api.anthropic.com/v1/claude_code/routines/trig_a/../trig_b/fire",
		"encoded slash":         "https://api.anthropic.com/v1/claude_code/routines/trig_a%2Fb/fire",
		"encoded slash lower":   "https://api.anthropic.com/v1/claude_code/routines/trig_a%2fb/fire",
		"underscore in id":      "https://api.anthropic.com/v1/claude_code/routines/trig_a_b/fire",
		"uppercase host":        "https://API.ANTHROPIC.COM/v1/claude_code/routines/trig_01AbC9/fire",
		"trailing dot host":     "https://api.anthropic.com./v1/claude_code/routines/trig_01AbC9/fire",
		"port 443":              "https://api.anthropic.com:443/v1/claude_code/routines/trig_01AbC9/fire",
		"other port":            "https://api.anthropic.com:8443/v1/claude_code/routines/trig_01AbC9/fire",
		"trailing newline":      validFireURL + "\n",
		"leading space":         " " + validFireURL,
		"trailing slash":        validFireURL + "/",
		"empty":                 "",
		"newline injected":      "https://api.anthropic.com/v1/claude_code/routines/trig_a\n/fire",
		"uppercase scheme":      "HTTPS://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/fire",
		"uppercase fire":        "https://api.anthropic.com/v1/claude_code/routines/trig_01AbC9/FIRE",
		"double slash":          "https://api.anthropic.com//v1/claude_code/routines/trig_01AbC9/fire",
		"zero-width suffix":     validFireURL + "\u200b",
	}
	for name, s := range invalid {
		if err := ValidateFireURL(s); err == nil {
			t.Errorf("%s: ValidateFireURL(%q) = nil, want error", name, s)
		}
	}
}

func TestFire_SendsDocumentedRequest(t *testing.T) {
	var gotReq *http.Request
	var gotBody string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"type":"routine_fire","claude_code_session_id":"session_01ABC","claude_code_session_url":"https://claude.ai/code/session_01ABC"}`)
	}))
	defer srv.Close()

	res, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "do the thing")
	if err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if res.SessionID != "session_01ABC" || res.URL != "https://claude.ai/code/session_01ABC" {
		t.Errorf("result = %+v", res)
	}
	if gotReq.Method != http.MethodPost {
		t.Errorf("method = %s", gotReq.Method)
	}
	if gotReq.URL.Path != "/v1/claude_code/routines/trig_01AbC9/fire" {
		t.Errorf("path = %s", gotReq.URL.Path)
	}
	wantHeaders := map[string]string{
		"Authorization":     "Bearer " + testToken,
		"Anthropic-Beta":    RoutineBetaHeader,
		"Anthropic-Version": AnthropicVersion,
		"Content-Type":      "application/json",
	}
	for k, v := range wantHeaders {
		if got := gotReq.Header.Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
	if gotBody != `{"text":"do the thing"}` {
		t.Errorf("body = %q", gotBody)
	}
}

func TestFire_EmptyTextSendsNoBody(t *testing.T) {
	var gotBody string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, `{"claude_code_session_id":"session_01ABC","claude_code_session_url":"https://claude.ai/code/session_01ABC"}`)
	}))
	defer srv.Close()

	if _, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, ""); err != nil {
		t.Fatalf("Fire: %v", err)
	}
	if gotBody != "" {
		t.Errorf("body = %q, want empty", gotBody)
	}
}

func TestFire_429IsFireError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
	var fe *FireError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FireError", err)
	}
	if fe.Status != http.StatusTooManyRequests || fe.Body != `{"error":"rate limited"}` {
		t.Errorf("FireError = %+v", fe)
	}
}

func TestFire_DoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	var targetAuth atomic.Value
	target := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		targetAuth.Store(r.Header.Get("Authorization"))
	}))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
	}))
	defer srv.Close()

	// A plain client without CheckRedirect: Fire itself must refuse to follow.
	_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
	var fe *FireError
	if !errors.As(err, &fe) || fe.Status != http.StatusFound {
		t.Fatalf("err = %v, want FireError with status 302", err)
	}
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target received %d requests, auth=%v", n, targetAuth.Load())
	}
}

func TestFire_RejectsInvalidURLWithoutSending(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	_, err := clientFor(t, srv).Fire(context.Background(), "https://evil.example/v1/claude_code/routines/trig_a/fire", testToken, "x")
	if err == nil {
		t.Fatal("want error")
	}
	if hits.Load() != 0 {
		t.Error("request was sent for an invalid URL")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Error("error contains the token")
	}
}

func TestFire_FireErrorBodyCappedAt4KiB(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("a", 10*1024))
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
	var fe *FireError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FireError", err)
	}
	if len(fe.Body) != 4096 {
		t.Errorf("body length = %d, want 4096", len(fe.Body))
	}
}

func TestFire_TokenNeverInErrors(t *testing.T) {
	t.Run("echoed in error body", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "bad credential: "+r.Header.Get("Authorization"))
		}))
		defer srv.Close()
		_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
		var fe *FireError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(fe.Body, testToken) || strings.Contains(err.Error(), testToken) {
			t.Errorf("token leaked: body=%q err=%q", fe.Body, err.Error())
		}
	})

	t.Run("echoed across the 4 KiB cut", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, strings.Repeat("a", 4096-10)+testToken)
		}))
		defer srv.Close()
		_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
		var fe *FireError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(fe.Body, testToken[:8]) {
			t.Errorf("token prefix leaked across the cap: %q", fe.Body[len(fe.Body)-30:])
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		c := RoutineClient{HTTP: &http.Client{Transport: failingTransport{}}}
		_, err := c.Fire(context.Background(), validFireURL, testToken, "x")
		if err == nil || strings.Contains(err.Error(), testToken) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("invalid header value", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, "tok\nen"+testToken, "x")
		if err == nil || strings.Contains(err.Error(), testToken) {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("malformed success body", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "not json "+r.Header.Get("Authorization"))
		}))
		defer srv.Close()
		_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
		if err == nil || strings.Contains(err.Error(), testToken) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestFire_RejectsEmptyToken(t *testing.T) {
	if _, err := (RoutineClient{}).Fire(context.Background(), validFireURL, "", "x"); err == nil {
		t.Fatal("want error for empty token")
	}
}

func TestFire_RejectsIncompleteSuccessBody(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	if _, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x"); err == nil {
		t.Fatal("want error when the session id is missing")
	}
}

func TestFireError_ErrorOmitsBody(t *testing.T) {
	msg := (&FireError{Status: 429, Body: "secret-ish"}).Error()
	if !strings.Contains(msg, "429") || strings.Contains(msg, "secret-ish") {
		t.Errorf("Error() = %q", msg)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed")
}
