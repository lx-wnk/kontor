package cloudsessions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/cloud"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const (
	testSessionID  = "session_01ABCdef2345"
	testSessionURL = "https://claude.ai/code/" + testSessionID
)

type fakeCLI struct {
	mu          sync.Mutex
	sendResult  cloud.SendResult
	sendErr     error
	auth        cloud.AuthStatus
	authErr     error
	sendCalls   int
	authCalls   int
	lastText    string
	lastSession string
	sendCtxErr  error
	authHook    func()
	sendHook    func()
}

func (f *fakeCLI) Send(ctx context.Context, _ *ent.Spawner, sessionID, text string) (cloud.SendResult, error) {
	f.mu.Lock()
	f.sendCalls++
	f.lastText = text
	f.lastSession = sessionID
	hook := f.sendHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCtxErr = ctx.Err()
	return f.sendResult, f.sendErr
}

func (f *fakeCLI) AuthStatus(_ context.Context, _ *ent.Spawner) (cloud.AuthStatus, error) {
	f.mu.Lock()
	f.authCalls++
	hook := f.authHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth, f.authErr
}

func (f *fakeCLI) authCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authCalls
}

type fixture struct {
	h       *Handler
	cli     *fakeCLI
	router  chi.Router
	events  chan []byte
	spawner *ent.Spawner
	dir     string
	clock   *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	spawners := repo.NewSpawnerRepo(bundle.Client)
	dir := t.TempDir()
	sp, err := spawners.Create(t.Context(), "Work", "work", "claude", nil,
		map[string]string{"CLAUDE_CONFIG_DIR": dir}, nil, nil, "claude", nil, false)
	require.NoError(t, err)

	cli := &fakeCLI{
		sendResult: cloud.SendResult{OK: true},
		auth:       cloud.AuthStatus{LoggedIn: true, AuthMethod: "claude.ai"},
	}
	bc := sse.NewTypedBroadcaster[sse.CloudSessionEvent](sse.NewBroadcaster())
	h := NewHandler(repo.NewCloudSessionRepo(bundle.Client), spawners, cli, bc)
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	h.auth = NewAuthCache(cli, func() time.Time { return clock })

	events := bc.Subscribe()
	t.Cleanup(func() { bc.Unsubscribe(events) })

	r := chi.NewRouter()
	h.Mount(r)
	return &fixture{h: h, cli: cli, router: r, events: events, spawner: sp, dir: dir, clock: &clock}
}

func (f *fixture) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func (f *fixture) importSession(t *testing.T, ref string) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]string{"spawnerId": f.spawner.ID, "url": ref})
	require.NoError(t, err)
	rr := f.do("POST", "/api/cloud-sessions/import", string(body))
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	return decodeMap(t, rr.Body.Bytes())
}

func (f *fixture) sendCtx(ctx context.Context, id, text string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"text": text})
	req := httptest.NewRequest("POST", "/api/cloud-sessions/"+id+"/messages", bytes.NewBuffer(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req)
	return rr
}

func (f *fixture) send(id, text string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"text": text})
	return f.do("POST", "/api/cloud-sessions/"+id+"/messages", string(body))
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

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m), string(raw))
	return m
}

func TestImport_CreatesSessionWithCanonicalURL(t *testing.T) {
	f := newFixture(t)
	view := f.importSession(t, testSessionURL+"/?foo=bar")

	require.Equal(t, testSessionID, view["sessionId"])
	require.Equal(t, testSessionURL, view["url"])
	require.Equal(t, f.spawner.ID, view["spawnerId"])
	require.Equal(t, testSessionID, view["title"])
	require.Equal(t, "imported", view["origin"])
	require.NotEmpty(t, view["id"])
	require.Nil(t, view["lastMessageAt"])
	require.Equal(t, "", view["lastMessage"])
	require.Equal(t, "", view["lastError"])
	for _, key := range []string{"createdAt", "updatedAt"} {
		require.NotEmpty(t, view[key], key)
	}
}

func TestImport_AcceptsBareIDAndTitle(t *testing.T) {
	f := newFixture(t)
	body := `{"spawnerId":"` + f.spawner.ID + `","url":"` + testSessionID + `","title":"Fix the build"}`
	rr := f.do("POST", "/api/cloud-sessions/import", body)
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	view := decodeMap(t, rr.Body.Bytes())
	require.Equal(t, "Fix the build", view["title"])
	require.Equal(t, testSessionURL, view["url"])
}

func TestImport_BadRefIs400(t *testing.T) {
	f := newFixture(t)
	for _, ref := range []string{"", "not a session", "https://example.com/code/" + testSessionID, "http://claude.ai/code/" + testSessionID} {
		body, _ := json.Marshal(map[string]string{"spawnerId": f.spawner.ID, "url": ref})
		rr := f.do("POST", "/api/cloud-sessions/import", string(body))
		require.Equal(t, http.StatusBadRequest, rr.Code, ref)
	}
}

func TestImport_InvalidJSONIs400(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusBadRequest, f.do("POST", "/api/cloud-sessions/import", "{nope").Code)
}

func TestImport_NonClaudeSpawnerIs400(t *testing.T) {
	f := newFixture(t)
	codex, err := f.h.spawners.Create(t.Context(), "Codex", "codex", "codex", nil, nil, nil, nil, "codex", nil, false)
	require.NoError(t, err)

	body := `{"spawnerId":"` + codex.ID + `","url":"` + testSessionID + `"}`
	rr := f.do("POST", "/api/cloud-sessions/import", body)
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestImport_UnknownSpawnerIs400(t *testing.T) {
	f := newFixture(t)
	body := `{"spawnerId":"missing","url":"` + testSessionID + `"}`
	require.Equal(t, http.StatusBadRequest, f.do("POST", "/api/cloud-sessions/import", body).Code)
}

func TestImport_TitleLimitIs200Runes(t *testing.T) {
	f := newFixture(t)
	post := func(title string) int {
		body, _ := json.Marshal(map[string]string{"spawnerId": f.spawner.ID, "url": testSessionID, "title": title})
		return f.do("POST", "/api/cloud-sessions/import", string(body)).Code
	}
	require.Equal(t, http.StatusBadRequest, post(strings.Repeat("a", 201)))
	require.Equal(t, http.StatusCreated, post(strings.Repeat("ä", 200)))
}

func TestImport_DuplicateIs409(t *testing.T) {
	f := newFixture(t)
	f.importSession(t, testSessionID)

	body := `{"spawnerId":"` + f.spawner.ID + `","url":"` + testSessionURL + `"}`
	rr := f.do("POST", "/api/cloud-sessions/import", body)
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())

	list := f.do("GET", "/api/cloud-sessions", "")
	var views []map[string]any
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &views))
	require.Len(t, views, 1)
}

func TestList_EmptyIsArray(t *testing.T) {
	f := newFixture(t)
	rr := f.do("GET", "/api/cloud-sessions", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.JSONEq(t, `[]`, rr.Body.String())
}

func TestList_ReturnsViews(t *testing.T) {
	f := newFixture(t)
	imported := f.importSession(t, testSessionID)

	rr := f.do("GET", "/api/cloud-sessions", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var views []map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &views))
	require.Len(t, views, 1)
	require.Equal(t, imported["id"], views[0]["id"])
}

func TestSend_SuccessStoresMessageAndClearsError(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	_, err := f.h.sessions.RecordSend(t.Context(), id, "old", time.Now(), "earlier failure")
	require.NoError(t, err)

	rr := f.send(id, "  hello cloud  ")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	view := decodeMap(t, rr.Body.Bytes())
	require.Equal(t, "  hello cloud  ", view["lastMessage"])
	require.NotNil(t, view["lastMessageAt"])
	require.Equal(t, "", view["lastError"])
	require.Equal(t, 1, f.cli.sendCalls)
	require.Equal(t, testSessionID, f.cli.lastSession)
	require.Equal(t, "  hello cloud  ", f.cli.lastText)
}

func TestSend_RejectsBadText(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	for name, text := range map[string]string{
		"empty":      "",
		"whitespace": " \n\t ",
		"too long":   strings.Repeat("a", 100001),
	} {
		require.Equal(t, http.StatusBadRequest, f.send(id, text).Code, name)
	}
	require.Equal(t, http.StatusOK, f.send(id, strings.Repeat("a", 100000)).Code)
	require.Equal(t, http.StatusBadRequest, f.do("POST", "/api/cloud-sessions/"+id+"/messages", "{nope").Code)
	require.Equal(t, 1, f.cli.sendCalls)
}

func TestSend_UnknownSessionIs404(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusNotFound, f.send("missing", "hi").Code)
	require.Zero(t, f.cli.sendCalls)
}

func TestSend_MissingSpawnerIs404(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	require.NoError(t, f.h.spawners.Delete(t.Context(), f.spawner.ID))

	require.Equal(t, http.StatusNotFound, f.send(id, "hi").Code)
	require.Zero(t, f.cli.sendCalls)
}

func TestSend_NotSignedInIs409WithLoginHint(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.cli.auth = cloud.AuthStatus{LoggedIn: true, AuthMethod: "api_key"}

	rr := f.send(id, "hi")
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	require.Equal(t,
		"account is not signed in with claude.ai; run: CLAUDE_CONFIG_DIR="+f.dir+" claude auth login",
		decodeMap(t, rr.Body.Bytes())["error"])
	require.Zero(t, f.cli.sendCalls)
}

func TestSend_AuthStatusErrorIs502(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.cli.authErr = cloud.ErrClaudeNotFound

	require.Equal(t, http.StatusBadGateway, f.send(id, "hi").Code)
	require.Zero(t, f.cli.sendCalls)
}

func TestSend_TimeoutIs502StoresErrorAndBroadcasts(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.nextEvent(t)
	f.cli.sendErr = cloud.ErrTimeout

	rr := f.send(id, "hi")
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	require.Equal(t, cloud.ErrTimeout.Error(), decodeMap(t, rr.Body.Bytes())["error"])
	require.Equal(t, 1, f.cli.sendCalls, "a timeout must never be retried")

	stored, err := f.h.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, cloud.ErrTimeout.Error(), stored.LastError)
	require.Equal(t, "hi", stored.LastMessage)

	ev := f.nextEvent(t)
	require.Equal(t, sse.CloudSessionUpdated, ev["type"])
	require.Equal(t, id, ev["cloudSessionId"])
	require.Equal(t, cloud.ErrTimeout.Error(), ev["payload"].(map[string]any)["lastError"])
}

func TestSend_NotOKStoresResultErrorAndReturns200(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.cli.sendResult = cloud.SendResult{OK: false, Error: "session is archived"}

	rr := f.send(id, "hi")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "session is archived", decodeMap(t, rr.Body.Bytes())["lastError"])

	stored, err := f.h.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "session is archived", stored.LastError)
}

func TestSend_NotOKWithoutErrorTextStillRecordsAFailure(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.cli.sendResult = cloud.SendResult{OK: false}

	rr := f.send(id, "hi")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NotEmpty(t, decodeMap(t, rr.Body.Bytes())["lastError"])
}

func TestSend_ClearsErrorAfterSuccess(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.cli.sendErr = cloud.ErrTimeout
	require.Equal(t, http.StatusBadGateway, f.send(id, "first").Code)

	f.cli.sendErr = nil
	rr := f.send(id, "second")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "", decodeMap(t, rr.Body.Bytes())["lastError"])
}

func TestAuthCache_CallsAuthStatusOncePerTTL(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)

	require.Equal(t, http.StatusOK, f.send(id, "one").Code)
	*f.clock = f.clock.Add(59 * time.Second)
	require.Equal(t, http.StatusOK, f.send(id, "two").Code)
	require.Equal(t, 1, f.cli.authCalls)

	*f.clock = f.clock.Add(2 * time.Second)
	require.Equal(t, http.StatusOK, f.send(id, "three").Code)
	require.Equal(t, 2, f.cli.authCalls)
}

func TestAuthCache_KeysByCanonicalConfigDir(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(f.dir, link))
	other, err := f.h.spawners.Create(t.Context(), "Other", "other", "claude", nil,
		map[string]string{"CLAUDE_CONFIG_DIR": link}, nil, nil, "claude", nil, false)
	require.NoError(t, err)

	_, err = f.h.auth.Status(t.Context(), f.spawner, false)
	require.NoError(t, err)
	_, err = f.h.auth.Status(t.Context(), other, false)
	require.NoError(t, err)
	require.Equal(t, 1, f.cli.authCalls)

	_, err = f.h.auth.Status(t.Context(), other, true)
	require.NoError(t, err)
	require.Equal(t, 2, f.cli.authCalls, "refresh bypasses the cache")
}

func TestAuthCache_DoesNotCacheErrors(t *testing.T) {
	f := newFixture(t)
	f.cli.authErr = cloud.ErrTimeout
	_, err := f.h.auth.Status(t.Context(), f.spawner, false)
	require.ErrorIs(t, err, cloud.ErrTimeout)

	f.cli.authErr = nil
	_, err = f.h.auth.Status(t.Context(), f.spawner, false)
	require.NoError(t, err)
	require.Equal(t, 2, f.cli.authCalls)
}

func TestDelete_Returns204AndBroadcasts(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.nextEvent(t)

	rr := f.do("DELETE", "/api/cloud-sessions/"+id, "")
	require.Equal(t, http.StatusNoContent, rr.Code)
	require.Empty(t, rr.Body.String())

	ev := f.nextEvent(t)
	require.Equal(t, sse.CloudSessionDeleted, ev["type"])
	require.Equal(t, id, ev["cloudSessionId"])

	_, err := f.h.sessions.Get(t.Context(), id)
	require.True(t, ent.IsNotFound(err))
	require.Zero(t, f.cli.sendCalls)
}

func TestDelete_UnknownIs404(t *testing.T) {
	f := newFixture(t)
	require.Equal(t, http.StatusNotFound, f.do("DELETE", "/api/cloud-sessions/missing", "").Code)
	f.requireNoEvent(t)
}

func TestEvents_CreatedOnImportUpdatedAfterSend(t *testing.T) {
	f := newFixture(t)
	view := f.importSession(t, testSessionID)
	id := view["id"].(string)

	created := f.nextEvent(t)
	require.Equal(t, sse.CloudSessionCreated, created["type"])
	require.Equal(t, id, created["cloudSessionId"])
	require.Equal(t, testSessionID, created["payload"].(map[string]any)["sessionId"])

	require.Equal(t, http.StatusOK, f.send(id, "hi").Code)
	updated := f.nextEvent(t)
	require.Equal(t, sse.CloudSessionUpdated, updated["type"])
	require.Equal(t, "hi", updated["payload"].(map[string]any)["lastMessage"])
}

func TestStream_ServesEvents(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.router)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/cloud-sessions/stream", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	f.importSession(t, testSessionID)

	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)
	require.Contains(t, string(buf[:n]), sse.CloudSessionCreated)
}

func TestSend_SpawnerNoLongerClaudeIs409BeforeAuth(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	custom := "custom"
	_, err := f.h.spawners.Update(t.Context(), f.spawner.ID, nil, nil, nil, nil, nil, nil, nil, &custom, nil, false, false)
	require.NoError(t, err)

	rr := f.send(id, "hi")
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	require.Equal(t, "spawner "+f.spawner.ID+" no longer runs Claude", decodeMap(t, rr.Body.Bytes())["error"])
	require.Zero(t, f.cli.authCalls)
	require.Zero(t, f.cli.sendCalls)

	stored, err := f.h.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Empty(t, stored.LastMessage)
	require.Nil(t, stored.LastMessageAt)
}

func TestNewHandler_RequiresBroadcaster(t *testing.T) {
	require.PanicsWithValue(t, "cloudsessions.Handler requires a broadcaster", func() {
		NewHandler(nil, nil, &fakeCLI{}, nil)
	})
}

func TestAuthCache_ConcurrentLookupsShareOneCLICall(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.cli.authHook = func() { <-release }

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.h.auth.Status(t.Context(), f.spawner, false)
			assert.NoError(t, err)
		}()
		if f.cli.authCount() == 0 {
			require.Eventually(t, func() bool { return f.cli.authCount() == 1 }, time.Second, time.Millisecond)
		}
	}
	// give the second caller time to reach (and wrongly pass) the cache
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, f.cli.authCount(), "second caller must wait on the in-flight lookup")
	close(release)
	wg.Wait()
	require.Equal(t, 1, f.cli.authCount())
}

func TestAuthCache_NotReadyStatusIsNotCached(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.cli.auth = cloud.AuthStatus{LoggedIn: false}
	require.Equal(t, http.StatusConflict, f.send(id, "hi").Code)

	f.cli.auth = cloud.AuthStatus{LoggedIn: true, AuthMethod: "claude.ai"}
	require.Equal(t, http.StatusOK, f.send(id, "hi").Code, "a retry right after claude auth login must succeed")
	require.Equal(t, 2, f.cli.authCalls)
}

func TestSend_ClientDisconnectStillRecordsAndBroadcasts(t *testing.T) {
	f := newFixture(t)
	id := f.importSession(t, testSessionID)["id"].(string)
	f.nextEvent(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.cli.sendHook = cancel

	rr := f.sendCtx(ctx, id, "hi")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.NoError(t, f.cli.sendCtxErr, "the CLI call must outlive the request context")

	stored, err := f.h.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "hi", stored.LastMessage)
	require.Equal(t, sse.CloudSessionUpdated, f.nextEvent(t)["type"])
}

func TestImport_TitleWithControlCharactersIs400(t *testing.T) {
	f := newFixture(t)
	for _, title := range []string{"a\nb", "a\x1b[31mb", "a\x00b"} {
		body, _ := json.Marshal(map[string]string{"spawnerId": f.spawner.ID, "url": testSessionID, "title": title})
		rr := f.do("POST", "/api/cloud-sessions/import", string(body))
		require.Equal(t, http.StatusBadRequest, rr.Code, title)
		require.Equal(t, "title: contains control characters", decodeMap(t, rr.Body.Bytes())["error"])
	}
}
