package tracker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	trackerapi "github.com/lx-wnk/kontor/server/internal/api/tracker"
	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/secretbox"
	"github.com/lx-wnk/kontor/server/internal/tracker"
)

// memRepo is an in-memory plugin.Repo for tests.
type memRepo struct {
	mu   sync.Mutex
	rows map[string]plugin.Stored // key -> row
}

func newMemRepo() *memRepo { return &memRepo{rows: make(map[string]plugin.Stored)} }

func (m *memRepo) ListByPlugin(_ context.Context, _ string) ([]plugin.Stored, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]plugin.Stored, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

func (m *memRepo) Upsert(_ context.Context, _ string, s plugin.Stored) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[s.Key] = s
	return nil
}

func (m *memRepo) DeleteByPlugin(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.rows)
	return nil
}

func newTestBox(t *testing.T) *secretbox.Box {
	t.Helper()
	key := make([]byte, 32)
	box, err := secretbox.New(key)
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	return box
}

func newTestHandler(t *testing.T) (*trackerapi.Handler, *plugin.Service) {
	t.Helper()
	repo := newMemRepo()
	svc := plugin.NewSettingsService(repo, newTestBox(t))
	h := trackerapi.NewHandler(svc, &http.Client{}, tracker.Resolve)
	return h, svc
}

// fakeTracker returns a fixed result, letting handler tests drive the
// FetchIssue error branches without a real upstream.
type fakeTracker struct {
	iss tracker.Issue
	err error
}

func (f fakeTracker) FetchIssue(context.Context, string) (tracker.Issue, error) {
	return f.iss, f.err
}

// handlerWithTracker builds a handler whose resolver always returns tr.
func handlerWithTracker(t *testing.T, tr tracker.Tracker) *trackerapi.Handler {
	t.Helper()
	svc := plugin.NewSettingsService(newMemRepo(), newTestBox(t))
	resolver := func(string, tracker.Config, *http.Client) (tracker.Tracker, error) { return tr, nil }
	return trackerapi.NewHandler(svc, &http.Client{}, resolver)
}

func doRequest(t *testing.T, h *trackerapi.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.Mount(r)
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestGetSettings_EmptyInitial(t *testing.T) {
	h, _ := newTestHandler(t)
	w := doRequest(t, h, http.MethodGet, "/api/tracker/settings", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp struct {
		Schema []plugin.SettingField `json:"schema"`
		Values map[string]string     `json:"values"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Schema) == 0 {
		t.Error("schema must be non-empty")
	}
	for _, v := range resp.Values {
		if v != "" {
			t.Errorf("unexpected non-empty initial value: %q", v)
		}
	}
}

func TestPutSettings_SecretRoundTrip(t *testing.T) {
	h, _ := newTestHandler(t)
	w := doRequest(t, h, http.MethodPut, "/api/tracker/settings", map[string]any{
		"values": map[string]string{"tracker.github.token": "ghp_test1234"},
	})
	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT status: %d body: %s", w.Code, w.Body.String())
	}
	w2 := doRequest(t, h, http.MethodGet, "/api/tracker/settings", nil)
	var resp struct {
		Values map[string]string `json:"values"`
	}
	_ = json.NewDecoder(w2.Body).Decode(&resp)
	if resp.Values["tracker.github.token"] != plugin.MaskedSentinel {
		t.Errorf("secret not masked: %q", resp.Values["tracker.github.token"])
	}
}

func TestPutSettings_SentinelPreservesExistingSecret(t *testing.T) {
	h, svc := newTestHandler(t)
	_ = doRequest(t, h, http.MethodPut, "/api/tracker/settings", map[string]any{
		"values": map[string]string{"tracker.github.token": "initial-secret"},
	})
	_ = doRequest(t, h, http.MethodPut, "/api/tracker/settings", map[string]any{
		"values": map[string]string{"tracker.github.token": plugin.MaskedSentinel},
	})
	// Assert the STORED plaintext is unchanged — GET masks unconditionally, so
	// only inspecting the decrypted value proves the secret was preserved.
	vals, err := svc.DecryptedAll(context.Background(), "tracker")
	if err != nil {
		t.Fatalf("DecryptedAll: %v", err)
	}
	if got := vals["tracker.github.token"]; got != "initial-secret" {
		t.Errorf("stored secret clobbered by sentinel write: got %q", got)
	}
}

func TestFetch_BadRefFromTracker_Returns400(t *testing.T) {
	h := handlerWithTracker(t, fakeTracker{err: tracker.ErrBadRef})
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": "owner/repo#1"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad ref from tracker: got %d, want 400", w.Code)
	}
}

func TestFetch_UpstreamError_Returns502(t *testing.T) {
	h := handlerWithTracker(t, fakeTracker{err: tracker.ErrTrackerUpstream})
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": "owner/repo#1"})
	if w.Code != http.StatusBadGateway {
		t.Errorf("upstream error: got %d, want 502", w.Code)
	}
}

func TestFetch_EmptyRef(t *testing.T) {
	h, _ := newTestHandler(t)
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": ""})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty ref: got %d", w.Code)
	}
}

func TestFetch_BadRef(t *testing.T) {
	h, _ := newTestHandler(t)
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": "not-any-ref"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad ref: got %d", w.Code)
	}
}

func TestFetch_MissingGitHubToken_Returns400(t *testing.T) {
	h, _ := newTestHandler(t)
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": "owner/repo#1"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing token: got %d", w.Code)
	}
}

func TestFetch_JiraSuccess(t *testing.T) {
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fields": map[string]any{
				"summary":     "From Jira",
				"description": nil,
				"labels":      []string{},
			},
		})
	}))
	defer jiraSrv.Close()

	h, _ := newTestHandler(t)
	_ = doRequest(t, h, http.MethodPut, "/api/tracker/settings", map[string]any{
		"values": map[string]string{
			"tracker.jira.baseUrl": jiraSrv.URL,
			"tracker.jira.email":   "u@example.com",
			"tracker.jira.token":   "jira-tok",
		},
	})
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": "PROJ-7"})
	if w.Code != http.StatusOK {
		t.Fatalf("jira fetch: %d — %s", w.Code, w.Body.String())
	}
	var iss tracker.Issue
	if err := json.NewDecoder(w.Body).Decode(&iss); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if iss.Title != "From Jira" {
		t.Errorf("title: %q", iss.Title)
	}
	if iss.Tracker != "jira" {
		t.Errorf("tracker: %q", iss.Tracker)
	}
}

func TestFetch_IssueNotFound_Returns404(t *testing.T) {
	jiraSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_ = json.NewEncoder(w).Encode(map[string]any{"errorMessages": []string{"Not Found"}})
	}))
	defer jiraSrv.Close()

	h, _ := newTestHandler(t)
	_ = doRequest(t, h, http.MethodPut, "/api/tracker/settings", map[string]any{
		"values": map[string]string{
			"tracker.jira.baseUrl": jiraSrv.URL,
			"tracker.jira.email":   "u@x.com",
			"tracker.jira.token":   "tok",
		},
	})
	w := doRequest(t, h, http.MethodPost, "/api/tracker/fetch", map[string]string{"ref": "PROJ-99"})
	if w.Code != http.StatusNotFound {
		t.Errorf("not found: got %d", w.Code)
	}
}
