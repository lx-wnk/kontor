package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
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
	if req.URL.Hostname() != "api.anthropic.com" {
		return r.base.RoundTrip(req)
	}
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
		"host dot as letter":    "https://apixanthropic.com/v1/claude_code/routines/trig_01AbC9/fire",
		"host dot as dash":      "https://api-anthropic.com/v1/claude_code/routines/trig_01AbC9/fire",
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
		"bare query":            validFireURL + "?",
		"bare fragment":         validFireURL + "#",
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
	if errors.Is(err, ErrUnconfirmed) {
		t.Error("an upstream refusal is reported as unconfirmed")
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

	// The rewrite transport only rewrites api.anthropic.com, so a followed redirect would reach target.
	_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
	if n := targetHits.Load(); n != 0 {
		t.Errorf("redirect target received %d requests, auth=%v", n, targetAuth.Load())
	}
	if auth, _ := targetAuth.Load().(string); auth != "" {
		t.Errorf("redirect target saw Authorization %q", auth)
	}
	var fe *FireError
	if !errors.As(err, &fe) || fe.Status != http.StatusFound {
		t.Fatalf("err = %v, want FireError with status 302", err)
	}
}

func TestFire_RejectsInvalidURLWithoutSending(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	// Both hosts are api.anthropic.com, so the rewrite transport would deliver them.
	for _, u := range []string{
		"http://api.anthropic.com/v1/claude_code/routines/trig_a/fire",
		validFireURL + "/extra",
	} {
		_, err := clientFor(t, srv).Fire(context.Background(), u, testToken, "x")
		if !errors.Is(err, ErrInvalidFireURL) {
			t.Errorf("%s: err = %v, want ErrInvalidFireURL", u, err)
		}
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Errorf("%s: error contains the token", u)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests were sent for invalid URLs", n)
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
		c := RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: errors.New("dial " + testToken)}}}
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
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()

	if _, err := clientFor(t, srv).Fire(context.Background(), validFireURL, "", "x"); err == nil {
		t.Fatal("want error for empty token")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d requests were sent with an empty token", n)
	}
}

func TestFire_SuccessBodyOver1MiBIsCut(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"claude_code_session_id":"session_01ABC","pad":"`+strings.Repeat("a", maxFireResponse)+`"}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := clientFor(t, srv).Fire(ctx, validFireURL, testToken, "x")
	if err == nil {
		t.Fatal("want a decode error for a success body over 1 MiB")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a decode error, not a hang", err)
	}
	if !errors.Is(err, ErrUnconfirmed) {
		t.Errorf("err = %v, want ErrUnconfirmed", err)
	}
}

func TestFire_UnconfirmedAfterTheRequestWasSent(t *testing.T) {
	respond := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }
	}
	cases := map[string]http.HandlerFunc{
		"2xx body not JSON":        respond("not json"),
		"2xx body without session": respond(`{"claude_code_session_url":"https://claude.ai/code/session_01ABC"}`),
		"connection dropped": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewTLSServer(handler)
			defer srv.Close()
			_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, testToken, "x")
			if !errors.Is(err, ErrUnconfirmed) {
				t.Errorf("err = %v, want ErrUnconfirmed", err)
			}
			if err != nil && strings.Contains(err.Error(), testToken) {
				t.Error("error contains the token")
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		defer srv.Close()
		defer close(release)
		c := clientFor(t, srv)
		c.HTTP.Timeout = 50 * time.Millisecond
		_, err := c.Fire(context.Background(), validFireURL, testToken, "x")
		if !errors.Is(err, ErrUnconfirmed) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("err = %v, want ErrUnconfirmed and both deadline errors", err)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		received, release := make(chan struct{}), make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(received)
			<-release
		}))
		defer srv.Close()
		defer close(release)
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-received:
			case <-time.After(5 * time.Second):
			}
			cancel()
		}()
		_, err := clientFor(t, srv).Fire(ctx, validFireURL, testToken, "x")
		if !errors.Is(err, ErrUnconfirmed) || !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want ErrUnconfirmed and Canceled", err)
		}
	})
}

func TestFire_NotUnconfirmedBeforeAConnection(t *testing.T) {
	closed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachable := clientFor(t, closed)
	closed.Close()

	cases := map[string]struct {
		client RoutineClient
		want   error
	}{
		"dial refused":        {client: unreachable},
		"transport failure":   {client: RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: errors.New("boom")}}}},
		"timeout before send": {client: RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: context.DeadlineExceeded}}}, want: context.DeadlineExceeded},
		"cancel before send":  {client: RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: context.Canceled}}}, want: context.Canceled},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tc.client.Fire(context.Background(), validFireURL, testToken, "x")
			if err == nil || errors.Is(err, ErrUnconfirmed) {
				t.Errorf("err = %v, want a failure that is not ErrUnconfirmed", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}

	if _, err := unreachable.Fire(context.Background(), validFireURL+"/extra", testToken, "x"); err == nil || errors.Is(err, ErrUnconfirmed) {
		t.Errorf("invalid URL: err = %v, want a failure that is not ErrUnconfirmed", err)
	}
	if _, err := unreachable.Fire(context.Background(), validFireURL, "", "x"); err == nil || errors.Is(err, ErrUnconfirmed) {
		t.Errorf("empty token: err = %v, want a failure that is not ErrUnconfirmed", err)
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

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func assertNoTokenFragment(t *testing.T, label, got, token string) {
	t.Helper()
	const minLeak = 8
	for i := 0; i+minLeak <= len(token); i++ {
		if frag := token[i : i+minLeak]; strings.Contains(got, frag) {
			t.Errorf("%s: body contains token fragment %q (token offset %d)", label, frag, i)
			return
		}
	}
}

func fireWithBody(t *testing.T, token, body string) *FireError {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	_, err := clientFor(t, srv).Fire(context.Background(), validFireURL, token, "x")
	var fe *FireError
	if !errors.As(err, &fe) {
		t.Fatalf("err = %v, want *FireError", err)
	}
	if len(fe.Body) > 4096 {
		t.Errorf("body length = %d, want <= 4096", len(fe.Body))
	}
	return fe
}

func TestFire_BodyNeverHoldsTokenFragments(t *testing.T) {
	filler := func(n int) string { return strings.Repeat("a", n) }
	const periodic = "abcabcabcabcabc"
	cases := map[string]struct{ token, body string }{
		"many copies":                       {testToken, strings.Repeat(testToken+" ", 300)},
		"many copies ending mid-token":      {testToken, strings.Repeat(testToken+" ", 200) + testToken[:20]},
		"copy followed by longer text":      {testToken, testToken + "-suffix-" + testToken + "more"},
		"overlapping copies":                {"aaaaaaaaaaaa", filler(19)},
		"periodic token, shifted copies":    {periodic, "x" + periodic + periodic[3:] + periodic[6:] + filler(4096)},
		"token prefix of a later substring": {testToken, testToken[:12] + " " + testToken[:30] + " " + testToken + testToken[5:]},
		"partial token at the very end":     {testToken, filler(4096-15) + testToken},
		"fragment only":                     {testToken, "echo: " + testToken[10:30]},
		"short token":                       {"abc123", "x abc123 y abc123"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fe := fireWithBody(t, tc.token, tc.body)
			assertNoTokenFragment(t, name, fe.Body, tc.token)
			if len(tc.token) < 8 && strings.Contains(fe.Body, tc.token) {
				t.Errorf("short token survived: %q", fe.Body)
			}
		})
	}
}

func TestFire_MasksATokenPrefixLeftAtTheEnd(t *testing.T) {
	cases := map[string]struct{ token, body, want string }{
		"prefix shorter than the leak window": {testToken, "echo: " + testToken[:5], "echo: [redacted]"},
		"short token missing its last byte":   {"abc123", "x abc12", "x [redacted]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := fireWithBody(t, tc.token, tc.body).Body; got != tc.want {
				t.Errorf("body = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFire_RedactedBodyIsCutBeforeAPartialMarkerOrRune(t *testing.T) {
	// Each unit grows from 10 to 12 bytes once redacted; the pads move the 4096-byte cut through every offset of a unit.
	unit := testToken[:minLeakWindow] + "é"
	for pad := range len(redactedToken) + len("é") {
		body := strings.Repeat("a", pad) + strings.Repeat(unit, maxFireErrorBody/len(unit)+1)
		got := fireWithBody(t, testToken, body).Body
		tail := got[max(0, len(got)-16):]
		if !utf8.ValidString(got) {
			t.Errorf("pad %d: body ends in a split rune: %q", pad, tail)
		}
		if strings.Count(got, "[") != strings.Count(got, redactedToken) {
			t.Errorf("pad %d: body ends in a partial marker: %q", pad, tail)
		}
		if len(got) <= maxFireErrorBody-len(redactedToken) {
			t.Errorf("pad %d: body length = %d, want the cut close to %d", pad, len(got), maxFireErrorBody)
		}
	}
}

func TestFire_SecondCopyStraddlingTheCutNeverLeaks(t *testing.T) {
	for pad := 4096 - 4*len(testToken); pad <= 4096+len(testToken); pad++ {
		body := testToken + strings.Repeat("a", pad) + testToken
		fe := fireWithBody(t, testToken, body)
		assertNoTokenFragment(t, fmt.Sprintf("pad %d", pad), fe.Body, testToken)
		if t.Failed() {
			return
		}
	}
}

func TestFire_ForcesDefaultTimeoutOnSuppliedClient(t *testing.T) {
	if got := (RoutineClient{HTTP: &http.Client{}}).client().Timeout; got != routineTimeout {
		t.Errorf("zero timeout became %v, want %v", got, routineTimeout)
	}
	if got := (RoutineClient{}).client().Timeout; got != routineTimeout {
		t.Errorf("nil client timeout = %v, want %v", got, routineTimeout)
	}
	if got := (RoutineClient{HTTP: &http.Client{Timeout: 3 * time.Second}}).client().Timeout; got != 3*time.Second {
		t.Errorf("explicit timeout became %v, want 3s", got)
	}
	if got := (RoutineClient{HTTP: &http.Client{Timeout: time.Minute}}).client().Timeout; got != routineTimeout {
		t.Errorf("longer timeout became %v, want it capped to %v", got, routineTimeout)
	}
}

func TestFire_ClassifiesTransportErrors(t *testing.T) {
	t.Run("client timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
		defer srv.Close()
		defer close(release)
		c := clientFor(t, srv)
		c.HTTP.Timeout = 50 * time.Millisecond
		_, err := c.Fire(context.Background(), validFireURL, testToken, "x")
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded and os.ErrDeadlineExceeded", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Error("timeout classified as canceled")
		}
	})

	t.Run("context deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		_, err := RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: context.DeadlineExceeded}}}.Fire(ctx, validFireURL, testToken, "x")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: context.Canceled}}}.Fire(ctx, validFireURL, testToken, "x")
		if !errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want Canceled only", err)
		}
	})

	t.Run("other", func(t *testing.T) {
		_, err := RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: errors.New("tls: " + testToken)}}}.Fire(context.Background(), validFireURL, testToken, "x")
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("err = %v, want a plain failure", err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Error("error contains the token")
		}
	})

	for name, err := range map[string]error{"timeout": context.DeadlineExceeded, "canceled": context.Canceled} {
		t.Run("no token in "+name, func(t *testing.T) {
			wrapped := fmt.Errorf("%s %w", testToken, err)
			_, got := RoutineClient{HTTP: &http.Client{Transport: failingTransport{err: wrapped}}}.Fire(context.Background(), validFireURL, testToken, "x")
			if got == nil || strings.Contains(got.Error(), testToken) {
				t.Errorf("err = %v", got)
			}
		})
	}
}
