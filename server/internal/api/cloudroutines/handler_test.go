package cloudroutines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/cloudsessions"
	"github.com/lx-wnk/kontor/server/internal/cloud"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const (
	testFireURL   = "https://api.anthropic.com/v1/claude_code/routines/trig_abc123/fire"
	otherFireURL  = "https://api.anthropic.com/v1/claude_code/routines/trig_def456/fire"
	testToken     = "sk-ant-oat01-SECRETSECRETSECRET"
	testSessionID = "session_01ABCdef2345"
	otherSession  = "session_01ZZZdef9999"
	waitLimit     = 5 * time.Second
	warningText   = "the routine may have started a run; check claude.ai before firing again"
)

var (
	errUnconfirmedTimeout = fmt.Errorf("routine fire timed out: %w: %w", context.DeadlineExceeded, cloud.ErrUnconfirmed)
	errUnconfirmedCancel  = fmt.Errorf("routine fire canceled: %w: %w", context.Canceled, cloud.ErrUnconfirmed)
)

type fakeFirer struct {
	mu           sync.Mutex
	result       cloud.FireResult
	sessionByURL map[string]string
	err          error
	calls        int
	lastURL      string
	lastToken    string
	lastText     string
	ctxErr       error
	entered      chan struct{}
	release      chan struct{}
}

func (f *fakeFirer) Fire(ctx context.Context, fireURL, token, text string) (cloud.FireResult, error) {
	f.mu.Lock()
	f.calls++
	f.lastURL, f.lastToken, f.lastText = fireURL, token, text
	result, err, entered, release := f.result, f.err, f.entered, f.release
	if id, ok := f.sessionByURL[fireURL]; ok {
		result.SessionID = id
	}
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		case <-time.After(waitLimit):
			return cloud.FireResult{}, errors.New("fake firer: nobody received entered")
		}
		select {
		case <-release:
		case <-time.After(waitLimit):
			return cloud.FireResult{}, errors.New("fake firer: never released")
		}
	}
	f.mu.Lock()
	f.ctxErr = ctx.Err()
	f.mu.Unlock()
	return result, err
}

func recvWithin[T any](t *testing.T, ch chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitLimit):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

func sendWithin(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case ch <- struct{}{}:
	case <-time.After(waitLimit):
		t.Fatalf("timed out %s", what)
	}
}

type failingSessions struct {
	repo.CloudSessionRepo
	err error
}

func (s failingSessions) Create(context.Context, repo.CreateCloudSessionInput) (*ent.CloudSession, error) {
	return nil, s.err
}

func (f *fakeFirer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

type fixture struct {
	router   chi.Router
	handler  *Handler
	firer    *fakeFirer
	sessions repo.CloudSessionRepo
	spawner  *ent.Spawner
	events   chan []byte
	preview  *bool
	client   *ent.Client
}

func newFixture(t *testing.T, box *secretbox.Box) *fixture {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	spawners := repo.NewSpawnerRepo(bundle.Client)
	sp, err := spawners.Create(t.Context(), "Work", "work", "claude", nil, nil, nil, nil, "claude", nil, false)
	require.NoError(t, err)

	firer := &fakeFirer{result: cloud.FireResult{SessionID: testSessionID, URL: "https://evil.example/ignored"}}
	bc := sse.NewTypedBroadcaster[sse.CloudSessionEvent](sse.NewBroadcaster())
	events := bc.Subscribe()
	t.Cleanup(func() { bc.Unsubscribe(events) })

	sessions := repo.NewCloudSessionRepo(bundle.Client)
	preview := true
	h := NewHandler(repo.NewCloudRoutineRepo(bundle.Client, box), sessions, spawners, firer, bc, func() bool { return preview })
	r := chi.NewRouter()
	h.Mount(r)
	return &fixture{router: r, handler: h, firer: firer, sessions: sessions, spawner: sp, events: events, preview: &preview, client: bundle.Client}
}

func newBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New(make([]byte, 32))
	require.NoError(t, err)
	return box
}

func (f *fixture) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func (f *fixture) createBody(over map[string]any) string {
	m := map[string]any{"name": "nightly", "fireUrl": testFireURL, "spawnerId": f.spawner.ID, "token": testToken}
	for k, v := range over {
		m[k] = v
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

func (f *fixture) create(t *testing.T) map[string]any {
	t.Helper()
	rr := f.do("POST", "/api/cloud-routines", f.createBody(nil))
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	return decodeMap(t, rr.Body.Bytes())
}

func (f *fixture) nextEvent(t *testing.T) map[string]any {
	t.Helper()
	select {
	case raw := <-f.events:
		raw = bytes.TrimPrefix(raw, []byte("data: "))
		raw = bytes.TrimSuffix(raw, []byte("\n\n"))
		return decodeMap(t, raw)
	case <-time.After(time.Second):
		t.Fatal("no SSE event broadcast")
		return nil
	}
}

func (f *fixture) requireNoEvent(t *testing.T) {
	t.Helper()
	select {
	case raw := <-f.events:
		t.Fatalf("unexpected SSE event: %s", raw)
	case <-time.After(50 * time.Millisecond):
	}
}

func (f *fixture) requireNoSessions(t *testing.T) {
	t.Helper()
	rows, err := f.sessions.List(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
}

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	return m
}

func TestEveryRouteIs404WhilePreviewIsOff(t *testing.T) {
	f := newFixture(t, newBox(t))
	routine := f.create(t)
	id := routine["id"].(string)
	*f.preview = false

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/cloud-routines", ""},
		{"POST", "/api/cloud-routines", f.createBody(nil)},
		{"PATCH", "/api/cloud-routines/" + id, `{"name":"x"}`},
		{"DELETE", "/api/cloud-routines/" + id, ""},
		{"POST", "/api/cloud-routines/" + id + "/fire", `{}`},
	} {
		rr := f.do(c.method, c.path, c.body)
		assert.Equal(t, http.StatusNotFound, rr.Code, c.method+" "+c.path)
	}
	assert.Zero(t, f.firer.callCount())

	*f.preview = true
	rr := f.do("GET", "/api/cloud-routines", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Len(t, decodeSlice(t, rr.Body.Bytes()), 1, "rows stay while preview is off")
}

func decodeSlice(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var s []map[string]any
	require.NoError(t, json.Unmarshal(raw, &s), string(raw))
	return s
}

func TestCreateListPatchDelete(t *testing.T) {
	f := newFixture(t, newBox(t))
	created := f.create(t)
	id := created["id"].(string)
	assert.Equal(t, "nightly", created["name"])
	assert.Equal(t, testFireURL, created["fireUrl"])
	assert.Equal(t, f.spawner.ID, created["spawnerId"])
	assert.Equal(t, true, created["hasToken"])
	assert.NotEmpty(t, created["createdAt"])
	assert.NotEmpty(t, created["updatedAt"])

	rr := f.do("PATCH", "/api/cloud-routines/"+id, `{"name":"weekly","fireUrl":"`+otherFireURL+`"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	patched := decodeMap(t, rr.Body.Bytes())
	assert.Equal(t, "weekly", patched["name"])
	assert.Equal(t, otherFireURL, patched["fireUrl"])

	rr = f.do("GET", "/api/cloud-routines", "")
	require.Equal(t, http.StatusOK, rr.Code)
	list := decodeSlice(t, rr.Body.Bytes())
	require.Len(t, list, 1)
	assert.Equal(t, "weekly", list[0]["name"])

	require.Equal(t, http.StatusNoContent, f.do("DELETE", "/api/cloud-routines/"+id, "").Code)
	assert.Equal(t, http.StatusNotFound, f.do("DELETE", "/api/cloud-routines/"+id, "").Code)
	assert.Equal(t, http.StatusNotFound, f.do("PATCH", "/api/cloud-routines/"+id, `{"name":"x"}`).Code)
	assert.Equal(t, http.StatusNotFound, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code)
}

func TestTokenNeverAppearsInAnyResponse(t *testing.T) {
	f := newFixture(t, newBox(t))
	rr := f.do("POST", "/api/cloud-routines", f.createBody(nil))
	require.Equal(t, http.StatusCreated, rr.Code)
	assert.NotContains(t, rr.Body.String(), testToken)
	id := decodeMap(t, rr.Body.Bytes())["id"].(string)

	rr = f.do("GET", "/api/cloud-routines", "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.False(t, bytes.Contains(rr.Body.Bytes(), []byte(testToken)), "GET body must not carry the token")
	for _, key := range []string{"token", "tokenCiphertext", "tokenNonce", "token_ciphertext", "token_nonce"} {
		assert.NotContains(t, rr.Body.String(), `"`+key+`"`)
	}

	rr = f.do("PATCH", "/api/cloud-routines/"+id, `{"token":"`+testToken+`2","name":"renamed"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotContains(t, rr.Body.String(), testToken)
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	f := newFixture(t, newBox(t))
	for name, over := range map[string]map[string]any{
		"empty token":        {"token": ""},
		"missing token":      {"token": nil},
		"token whitespace":   {"token": "sk ant"},
		"token control char": {"token": "sk-ant\x00x"},
		"token too long":     {"token": strings.Repeat("a", 4097)},
		"invalid fire url":   {"fireUrl": "https://example.com/v1/claude_code/routines/trig_1/fire"},
		"http fire url":      {"fireUrl": "http://api.anthropic.com/v1/claude_code/routines/trig_1/fire"},
		"empty name":         {"name": "   "},
		"name too long":      {"name": strings.Repeat("n", 201)},
		"name control char":  {"name": "bad\nname"},
		"unknown spawner":    {"spawnerId": "nope"},
		"missing spawner":    {"spawnerId": ""},
	} {
		rr := f.do("POST", "/api/cloud-routines", f.createBody(over))
		assert.Equal(t, http.StatusBadRequest, rr.Code, name+": "+rr.Body.String())
	}
	assert.Equal(t, http.StatusBadRequest, f.do("POST", "/api/cloud-routines", "{").Code)

	rr := f.do("GET", "/api/cloud-routines", "")
	assert.Empty(t, decodeSlice(t, rr.Body.Bytes()))
}

func TestCreateRejectsNonClaudeSpawner(t *testing.T) {
	f := newFixture(t, newBox(t))
	other, err := repo.NewSpawnerRepo(f.client).Create(t.Context(), "Codex", "codex", "codex", nil, nil, nil, nil, "codex", nil, false)
	require.NoError(t, err)

	rr := f.do("POST", "/api/cloud-routines", f.createBody(map[string]any{"spawnerId": other.ID}))
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestPatchValidatesAndKeepsTokenWhenEmpty(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)

	for name, body := range map[string]string{
		"invalid fire url": `{"fireUrl":"https://example.com/x"}`,
		"empty name":       `{"name":""}`,
		"name too long":    `{"name":"` + strings.Repeat("n", 201) + `"}`,
		"token whitespace": `{"token":"a b"}`,
		"token too long":   `{"token":"` + strings.Repeat("a", 4097) + `"}`,
		"invalid json":     `{`,
	} {
		rr := f.do("PATCH", "/api/cloud-routines/"+id, body)
		assert.Equal(t, http.StatusBadRequest, rr.Code, name+": "+rr.Body.String())
	}

	rr := f.do("PATCH", "/api/cloud-routines/"+id, `{"token":"","name":"renamed"}`)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, true, decodeMap(t, rr.Body.Bytes())["hasToken"])

	require.Equal(t, http.StatusCreated, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code)
	assert.Equal(t, testToken, f.firer.lastToken, "an empty token on patch keeps the stored one")

	rr = f.do("PATCH", "/api/cloud-routines/"+id, `{"token":"replacement"}`)
	require.Equal(t, http.StatusOK, rr.Code)
	f.firer.result.SessionID = otherSession
	require.Equal(t, http.StatusCreated, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code)
	assert.Equal(t, "replacement", f.firer.lastToken)
}

func TestFireRecordsRoutineSessionAndEmitsCreated(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)

	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{"text":"check the build"}`)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	view := decodeMap(t, rr.Body.Bytes())
	assert.Equal(t, "routine", view["origin"])
	assert.Equal(t, "nightly", view["title"])
	assert.Equal(t, testSessionID, view["sessionId"])
	assert.Equal(t, cloud.SessionURL(testSessionID), view["url"], "the returned URL is ignored")
	assert.Equal(t, f.spawner.ID, view["spawnerId"])

	assert.Equal(t, testFireURL, f.firer.lastURL)
	assert.Equal(t, testToken, f.firer.lastToken)
	assert.Equal(t, "check the build", f.firer.lastText)

	ev := f.nextEvent(t)
	assert.Equal(t, sse.CloudSessionCreated, ev["type"])
	assert.Equal(t, view["id"], ev["cloudSessionId"])
	assert.Equal(t, view, ev["payload"])

	rows, err := f.sessions.List(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "routine", string(rows[0].Origin))
}

func TestFireWithoutBodyIsAllowed(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", "")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	assert.Empty(t, f.firer.lastText)
}

func TestFireURLIsStoredTrimmed(t *testing.T) {
	f := newFixture(t, newBox(t))
	rr := f.do("POST", "/api/cloud-routines", f.createBody(map[string]any{"fireUrl": "  " + testFireURL + "\n"}))
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	created := decodeMap(t, rr.Body.Bytes())
	assert.Equal(t, testFireURL, created["fireUrl"])

	patch, _ := json.Marshal(map[string]string{"fireUrl": "\t" + otherFireURL + " "})
	rr = f.do("PATCH", "/api/cloud-routines/"+created["id"].(string), string(patch))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, otherFireURL, decodeMap(t, rr.Body.Bytes())["fireUrl"])
}

func TestBodiesOverTheLimitAreRejected(t *testing.T) {
	f := newFixture(t, newBox(t))
	pad := strings.Repeat("a", maxBodyBytes)

	rr := f.do("POST", "/api/cloud-routines", f.createBody(map[string]any{"pad": pad}))
	assert.Equal(t, http.StatusBadRequest, rr.Code, "create")
	assert.Empty(t, decodeSlice(t, f.do("GET", "/api/cloud-routines", "").Body.Bytes()))

	id := f.create(t)["id"].(string)
	rr = f.do("POST", "/api/cloud-routines/"+id+"/fire", `{"pad":"`+pad+`"}`)
	assert.Equal(t, http.StatusBadRequest, rr.Code, "fire")
	assert.Zero(t, f.firer.callCount())
}

func TestFireRejectsMalformedJSON(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{`)
	assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	assert.Zero(t, f.firer.callCount())
}

func TestFireOutlivesAClientThatHangsUp(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	f.firer.entered = make(chan struct{})
	f.firer.release = make(chan struct{})

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest("POST", "/api/cloud-routines/"+id+"/fire", strings.NewReader(`{}`)).WithContext(ctx)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		f.router.ServeHTTP(rr, req)
		close(done)
	}()
	recvWithin(t, f.firer.entered, "the fire to reach the API")
	cancel()
	sendWithin(t, f.firer.release, "releasing the fire")
	recvWithin(t, done, "the fire to finish")

	f.firer.mu.Lock()
	ctxErr := f.firer.ctxErr
	f.firer.mu.Unlock()
	assert.NoError(t, ctxErr, "the firer saw the request context canceled")
	assert.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	rows, err := f.sessions.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestFireRejectsOversizedText(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 100001)})
	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", string(body))
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Zero(t, f.firer.callCount())
}

func TestFireInvalidSessionIDIs502WithoutRow(t *testing.T) {
	for _, bad := range []string{"", "not-a-session", "https://evil.example/code/session_01ABCdef2345", "session_x"} {
		f := newFixture(t, newBox(t))
		id := f.create(t)["id"].(string)
		f.firer.result = cloud.FireResult{SessionID: bad, URL: "https://claude.ai/code/" + testSessionID}

		rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
		assert.Equal(t, http.StatusBadGateway, rr.Code, "session id %q: %s", bad, rr.Body.String())
		f.requireNoSessions(t)
		f.requireNoEvent(t)
	}
}

func TestFireErrorIs502WithStatusAndBodyAndNoRow(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	f.firer.err = &cloud.FireError{Status: 429, Body: "slow down"}

	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	got := decodeMap(t, rr.Body.Bytes())
	assert.EqualValues(t, 429, got["status"])
	assert.Equal(t, "slow down", got["body"])
	assert.NotContains(t, got, "mayHaveStarted", "an upstream refusal did not start a run")
	f.requireNoSessions(t)
	f.requireNoEvent(t)
}

func TestFireThatMayHaveStartedARunIs502WithAWarning(t *testing.T) {
	cases := map[string]func(f *fixture){
		"timeout after send": func(f *fixture) { f.firer.err = errUnconfirmedTimeout },
		"cancel after send":  func(f *fixture) { f.firer.err = errUnconfirmedCancel },
		"unusable 2xx body": func(f *fixture) {
			f.firer.err = fmt.Errorf("routine fire response is not valid JSON: %w", cloud.ErrUnconfirmed)
		},
		"connection lost":    func(f *fixture) { f.firer.err = fmt.Errorf("routine fire request failed: %w", cloud.ErrUnconfirmed) },
		"invalid session id": func(f *fixture) { f.firer.result.SessionID = "not-a-session" },
		"row save failed": func(f *fixture) {
			f.handler.sessions = failingSessions{CloudSessionRepo: f.sessions, err: errors.New("disk I/O error")}
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, newBox(t))
			id := f.create(t)["id"].(string)
			arrange(f)

			rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
			require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
			assert.Equal(t, map[string]any{"error": warningText, "mayHaveStarted": true}, decodeMap(t, rr.Body.Bytes()))
			f.requireNoSessions(t)
			f.requireNoEvent(t)
		})
	}
}

func TestFireThatCannotHaveStartedHasNoWarning(t *testing.T) {
	for name, err := range map[string]error{
		"failure before send": errors.New("routine fire request failed"),
		"timeout before send": fmt.Errorf("routine fire timed out: %w", context.DeadlineExceeded),
		"cancel before send":  fmt.Errorf("routine fire canceled: %w", context.Canceled),
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, newBox(t))
			id := f.create(t)["id"].(string)
			f.firer.err = err

			rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
			require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
			got := decodeMap(t, rr.Body.Bytes())
			assert.Equal(t, err.Error(), got["error"])
			assert.NotContains(t, got, "mayHaveStarted")
		})
	}
}

func TestFireTransportErrorIs502WithoutRow(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	f.firer.err = errors.New("routine fire request failed")

	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	require.Equal(t, http.StatusBadGateway, rr.Code)
	assert.NotContains(t, rr.Body.String(), testToken)
	f.requireNoSessions(t)
}

func TestFireWhenSpawnerIsGoneIs409WithoutFiring(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	require.NoError(t, f.client.Spawner.DeleteOneID(f.spawner.ID).Exec(t.Context()))

	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	assert.Zero(t, f.firer.callCount())
	f.requireNoSessions(t)
}

func TestNoSecretKeyMapsTo503(t *testing.T) {
	f := newFixture(t, nil)

	rr := f.do("POST", "/api/cloud-routines", f.createBody(nil))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "routine tokens are unavailable: no secret key configured")

	assert.Equal(t, http.StatusOK, f.do("GET", "/api/cloud-routines", "").Code)
}

func TestFireWithoutSecretKeyMapsTo503(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	spawners := repo.NewSpawnerRepo(bundle.Client)
	sp, err := spawners.Create(t.Context(), "Work", "work", "claude", nil, nil, nil, nil, "claude", nil, false)
	require.NoError(t, err)

	withKey := repo.NewCloudRoutineRepo(bundle.Client, newBox(t))
	routine, err := withKey.Create(t.Context(), repo.CreateCloudRoutineInput{Name: "n", FireURL: testFireURL, SpawnerID: sp.ID, Token: testToken})
	require.NoError(t, err)

	firer := &fakeFirer{}
	h := NewHandler(repo.NewCloudRoutineRepo(bundle.Client, nil), repo.NewCloudSessionRepo(bundle.Client), spawners, firer,
		sse.NewTypedBroadcaster[sse.CloudSessionEvent](sse.NewBroadcaster()), func() bool { return true })
	r := chi.NewRouter()
	h.Mount(r)

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest("POST", "/api/cloud-routines/"+routine.ID+"/fire", strings.NewReader(`{}`)))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "routine tokens are unavailable: no secret key configured")
	assert.Zero(t, firer.callCount())
}

func TestConcurrentFireOfOneRoutineCallsTheAPIOnce(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	f.firer.entered = make(chan struct{})
	f.firer.release = make(chan struct{})

	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`) }()
	recvWithin(t, f.firer.entered, "the first fire to reach the API")

	second := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	assert.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	assert.Contains(t, second.Body.String(), "routine is already firing")

	sendWithin(t, f.firer.release, "releasing the first fire")
	assert.Equal(t, http.StatusCreated, recvWithin(t, first, "the first fire to finish").Code)
	assert.Equal(t, 1, f.firer.callCount())

	f.firer.mu.Lock()
	f.firer.entered, f.firer.release = nil, nil
	f.firer.result.SessionID = otherSession
	f.firer.mu.Unlock()
	assert.Equal(t, http.StatusCreated, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code, "the guard is released after a fire")
}

func TestFireGuardIsReleasedOnEveryExitPath(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)

	f.firer.err = &cloud.FireError{Status: 500, Body: "boom"}
	assert.Equal(t, http.StatusBadGateway, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code)
	f.firer.err = nil
	f.firer.result.SessionID = "bad"
	assert.Equal(t, http.StatusBadGateway, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code)
	f.firer.result.SessionID = testSessionID
	assert.Equal(t, http.StatusCreated, f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`).Code)
}

func TestFireGuardIsPerRoutine(t *testing.T) {
	f := newFixture(t, newBox(t))
	first := f.create(t)["id"].(string)
	second := decodeMap(t, f.do("POST", "/api/cloud-routines",
		f.createBody(map[string]any{"name": "other", "fireUrl": otherFireURL})).Body.Bytes())["id"].(string)
	f.firer.sessionByURL = map[string]string{testFireURL: testSessionID, otherFireURL: otherSession}
	f.firer.entered = make(chan struct{})
	f.firer.release = make(chan struct{})

	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- f.do("POST", "/api/cloud-routines/"+first+"/fire", `{}`) }()
	recvWithin(t, f.firer.entered, "the first routine to reach the API")

	go func() { done <- f.do("POST", "/api/cloud-routines/"+second+"/fire", `{}`) }()
	recvWithin(t, f.firer.entered, "the second routine to reach the API")
	sendWithin(t, f.firer.release, "releasing the first fire")
	sendWithin(t, f.firer.release, "releasing the second fire")
	codes := []int{recvWithin(t, done, "a fire to finish").Code, recvWithin(t, done, "a fire to finish").Code}
	assert.Equal(t, []int{http.StatusCreated, http.StatusCreated}, codes)
	assert.Equal(t, 2, f.firer.callCount())
}

func TestFireLogsRunsThatMayHaveStartedUnrecorded(t *testing.T) {
	cases := map[string]func(f *fixture){
		"invalid session id": func(f *fixture) { f.firer.result.SessionID = "bad" },
		"timeout":            func(f *fixture) { f.firer.err = errUnconfirmedTimeout },
		"cancel":             func(f *fixture) { f.firer.err = errUnconfirmedCancel },
		"unusable 2xx body": func(f *fixture) {
			f.firer.err = fmt.Errorf("routine fire response has no session id: %w", cloud.ErrUnconfirmed)
		},
		"session row not created": func(f *fixture) {
			f.handler.sessions = failingSessions{CloudSessionRepo: f.sessions, err: errors.New("disk I/O error")}
		},
		"session exists": func(f *fixture) {
			_, err := f.sessions.Create(t.Context(), repo.CreateCloudSessionInput{
				SessionID: testSessionID, URL: cloud.SessionURL(testSessionID), SpawnerID: f.spawner.ID, Title: "x", Origin: "routine",
			})
			require.NoError(t, err)
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, newBox(t))
			id := f.create(t)["id"].(string)
			arrange(f)
			logs := captureLogs(t)

			rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{"text":"private prompt text"}`)
			require.GreaterOrEqual(t, rr.Code, 400, rr.Body.String())
			out := logs.String()
			assert.Contains(t, out, "level=WARN")
			assert.Contains(t, out, id)
			assert.NotContains(t, out, testToken)
			assert.NotContains(t, out, "private prompt text")
		})
	}
}

func TestFireErrorWithStatusIsNotLoggedAsUnrecorded(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	f.firer.err = &cloud.FireError{Status: 429, Body: "slow down"}
	logs := captureLogs(t)

	f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	assert.NotContains(t, logs.String(), "level=WARN")
}

func TestFireWhenSpawnerNoLongerRunsClaudeIs409WithoutFiring(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	require.NoError(t, f.client.Spawner.UpdateOneID(f.spawner.ID).SetAdapterType("codex").Exec(t.Context()))

	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	assert.Zero(t, f.firer.callCount())
	f.requireNoSessions(t)
}

func TestFireOfAnAlreadyRecordedSessionIs409(t *testing.T) {
	f := newFixture(t, newBox(t))
	id := f.create(t)["id"].(string)
	_, err := f.sessions.Create(t.Context(), repo.CreateCloudSessionInput{
		SessionID: testSessionID, URL: cloud.SessionURL(testSessionID), SpawnerID: f.spawner.ID, Title: "imported", Origin: "imported",
	})
	require.NoError(t, err)

	rr := f.do("POST", "/api/cloud-routines/"+id+"/fire", `{}`)
	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), "already recorded")
}

func TestRoutineNameFollowsTheSessionTitleRule(t *testing.T) {
	f := newFixture(t, newBox(t))
	for _, name := range []string{strings.Repeat("n", 201), "bad\nname"} {
		_, wantErr := cloudsessions.ValidateTitle(name)
		require.Error(t, wantErr)

		rr := f.do("POST", "/api/cloud-routines", f.createBody(map[string]any{"name": name}))
		require.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Equal(t, wantErr.Error(), decodeMap(t, rr.Body.Bytes())["error"])
	}
	title, err := cloudsessions.ValidateTitle("  padded  ")
	require.NoError(t, err)
	assert.Equal(t, "padded", title)
}

func TestNewHandlerPanicsOnNilDependencies(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	routines := repo.NewCloudRoutineRepo(bundle.Client, nil)
	sessions := repo.NewCloudSessionRepo(bundle.Client)
	spawners := repo.NewSpawnerRepo(bundle.Client)
	var firer Firer = &fakeFirer{}
	bc := sse.NewTypedBroadcaster[sse.CloudSessionEvent](sse.NewBroadcaster())
	on := func() bool { return true }

	assert.NotPanics(t, func() { NewHandler(routines, sessions, spawners, firer, bc, on) })
	assert.Panics(t, func() { NewHandler(nil, sessions, spawners, firer, bc, on) })
	assert.Panics(t, func() { NewHandler(routines, nil, spawners, firer, bc, on) })
	assert.Panics(t, func() { NewHandler(routines, sessions, nil, firer, bc, on) })
	assert.Panics(t, func() { NewHandler(routines, sessions, spawners, nil, bc, on) })
	assert.Panics(t, func() { NewHandler(routines, sessions, spawners, firer, nil, on) })
	assert.Panics(t, func() { NewHandler(routines, sessions, spawners, firer, bc, nil) })
}
