package obsidian_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
)

const graphAPIKey = "graph-secret-key"

const (
	mtimeSearchAnswer = `[{"filename":"root/b.md","result":1700000000000},{"filename":"root/a.md","result":1700000001000},{"filename":"other/x.md","result":1},{"filename":"root/pic.png","result":5}]`
	linksSearchAnswer = `[{"filename":"root/a.md","result":["root/b.md","other/x.md","root/missing.md","root/a.md"]},{"filename":"other/x.md","result":["root/a.md"]},{"filename":"root/b.md","result":"not-a-list"}]`
)

func newGraphVault(t *testing.T, searchStatus int) (*obsidian.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer "+graphAPIKey {
			t.Errorf("Authorization = %q, want the bearer key", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/search/":
			if ct := r.Header.Get("Content-Type"); ct != "application/vnd.olrapi.jsonlogic+json" {
				t.Errorf("Content-Type = %q, want the JsonLogic media type", ct)
			}
			if searchStatus != http.StatusOK {
				w.WriteHeader(searchStatus)
				return
			}
			body, _ := io.ReadAll(r.Body)
			switch string(body) {
			case `{"var":"stat.mtime"}`:
				_, _ = w.Write([]byte(mtimeSearchAnswer))
			case `{"var":"links"}`:
				_, _ = w.Write([]byte(linksSearchAnswer))
			default:
				t.Errorf("unexpected JsonLogic query %q", body)
				w.WriteHeader(http.StatusBadRequest)
			}
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/open/"):
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)

	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://" + ts.Listener.Addr().String(),
		APIKey:    graphAPIKey,
		VaultRoot: "root",
		TLSMode:   obsidian.TLSPinned,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func TestGraphKeepsOnlyMarkdownNotesUnderTheRootAndLinksBetweenThem(t *testing.T) {
	client, calls := newGraphVault(t, http.StatusOK)

	g, err := client.Graph(context.Background())
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}

	wantNotes := []obsidian.GraphNote{{Path: "a.md", MtimeMs: 1700000001000}, {Path: "b.md", MtimeMs: 1700000000000}}
	if !reflect.DeepEqual(g.Notes, wantNotes) {
		t.Errorf("Notes = %v, want %v", g.Notes, wantNotes)
	}
	if want := [][2]int{{0, 1}}; !reflect.DeepEqual(g.Links, want) {
		t.Errorf("Links = %v, want %v", g.Links, want)
	}
	if want := []string{"POST /search/", "POST /search/"}; !reflect.DeepEqual(calls(), want) {
		t.Errorf("requests = %v, want %v", calls(), want)
	}
}

// TestGraphSkipsNoteWithMalformedMtimeInsteadOfFailingTheWholeGraph pins that
// one note's malformed mtime, like one note's malformed links, must not blank
// the whole graph.
func TestGraphSkipsNoteWithMalformedMtimeInsteadOfFailingTheWholeGraph(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search/" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch string(body) {
		case `{"var":"stat.mtime"}`:
			_, _ = w.Write([]byte(`[{"filename":"root/a.md","result":1700000001000},{"filename":"root/bad.md","result":"not-a-number"}]`))
		case `{"var":"links"}`:
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(ts.Close)

	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   "https://" + ts.Listener.Addr().String(),
		APIKey:    graphAPIKey,
		VaultRoot: "root",
		TLSMode:   obsidian.TLSPinned,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	g, err := client.Graph(context.Background())
	if err != nil {
		t.Fatalf("Graph: want the malformed note skipped, not a failure: %v", err)
	}
	want := []obsidian.GraphNote{{Path: "a.md", MtimeMs: 1700000001000}}
	if !reflect.DeepEqual(g.Notes, want) {
		t.Errorf("Notes = %v, want %v (root/bad.md must be skipped, not block the rest)", g.Notes, want)
	}
}

func TestGraphFailsWithoutLeakingTheAPIKeyWhenSearchIsRefused(t *testing.T) {
	client, _ := newGraphVault(t, http.StatusInternalServerError)

	_, err := client.SearchJSONLogic(context.Background(), `{"var":"stat.mtime"}`)
	if err == nil {
		t.Fatal("SearchJSONLogic: want an error on a 500 answer, got nil")
	}
	if strings.Contains(err.Error(), graphAPIKey) {
		t.Fatalf("error %q contains the API key", err)
	}
	if _, err := client.Graph(context.Background()); err == nil {
		t.Fatal("Graph: want an error on a 500 search answer, got nil")
	}
}
