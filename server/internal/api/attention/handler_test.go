package attention_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/api/attention"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

func setup(t *testing.T) (http.Handler, chan []byte) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	b := sse.NewTypedBroadcaster[sse.AttentionEvent](sse.NewBroadcaster())
	ch := b.Subscribe()
	r := chi.NewRouter()
	attention.NewHandler(repo.NewAttentionMarkRepo(bundle.Client, bundle.WriteClient), b).Mount(r)
	return r, ch
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func put(h http.Handler, key, at string) *httptest.ResponseRecorder {
	return do(h, http.MethodPut, "/api/attention/marks", `{"key":"`+key+`","seenThrough":"`+at+`"}`)
}

func frames(ch chan []byte) int {
	n := 0
	for {
		select {
		case f := <-ch:
			if !strings.Contains(string(f), `"type":"attention_marked"`) {
				return -1
			}
			n++
		default:
			return n
		}
	}
}

func TestPut_Rejects(t *testing.T) {
	const ts = "2026-11-19T10:00:00Z"
	cases := map[string]string{
		"empty object":     `{}`,
		"empty key":        `{"key":"","seenThrough":"` + ts + `"}`,
		"uppercase kind":   `{"key":"Finished:s1","seenThrough":"` + ts + `"}`,
		"space in id":      `{"key":"finished:a b","seenThrough":"` + ts + `"}`,
		"id too long":      `{"key":"finished:` + strings.Repeat("a", 129) + `","seenThrough":"` + ts + `"}`,
		"bad time":         `{"key":"finished:s1","seenThrough":"yesterday"}`,
		"zero time":        `{"key":"finished:s1","seenThrough":"0001-01-01T00:00:00Z"}`,
		"trailing garbage": `{"key":"finished:s1","seenThrough":"` + ts + `"}garbage`,
		"two objects":      `{"key":"finished:s1","seenThrough":"` + ts + `"}{"key":"finished:s1","seenThrough":"` + ts + `"}`,
		"unknown field":    `{"key":"finished:s1","seenThrough":"` + ts + `","x":1}`,
		"oversized body":   `{"key":"finished:s1","seenThrough":"` + ts + `","pad":"` + strings.Repeat("a", 5000) + `"}`,
		"oversized plain":  strings.Repeat("a", 5000),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			h, ch := setup(t)
			rec := do(h, http.MethodPut, "/api/attention/marks", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
			if n := frames(ch); n != 0 {
				t.Fatalf("frames = %d, want 0", n)
			}
		})
	}
}

func TestPut_StoresAndBroadcastsOnce(t *testing.T) {
	h, ch := setup(t)
	want := `{"key":"failed:s1","seenThrough":"2026-11-19T10:00:00Z"}`
	for i := 0; i < 2; i++ {
		rec := put(h, "failed:s1", "2026-11-19T10:00:00Z")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Fatalf("body = %s, want %s", got, want)
		}
	}
	if n := frames(ch); n != 1 {
		t.Fatalf("frames = %d, want 1", n)
	}
}

func TestPut_OlderDoesNotRegress(t *testing.T) {
	h, ch := setup(t)
	put(h, "failed:s1", "2026-11-19T10:00:05Z")
	rec := put(h, "failed:s1", "2026-11-19T10:00:00Z")
	if !strings.Contains(rec.Body.String(), "2026-11-19T10:00:05Z") {
		t.Fatalf("body = %s, want 10:00:05Z", rec.Body)
	}
	if n := frames(ch); n != 1 {
		t.Fatalf("frames = %d, want 1", n)
	}
}

type listResp struct {
	Baseline string `json:"baseline"`
	Marks    []struct {
		Key         string `json:"key"`
		SeenThrough string `json:"seenThrough"`
	} `json:"marks"`
}

func get(t *testing.T, h http.Handler) listResp {
	t.Helper()
	rec := do(h, http.MethodGet, "/api/attention/marks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
	}
	var out listResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestList(t *testing.T) {
	h, _ := setup(t)
	put(h, "b:2", "2026-11-19T10:00:00Z")
	put(h, "a:1", "2026-11-19T10:00:00Z")
	out := get(t, h)
	if len(out.Marks) != 2 || out.Marks[0].Key != "a:1" || out.Marks[1].Key != "b:2" {
		t.Fatalf("marks = %+v", out.Marks)
	}
	bl, err := time.Parse(time.RFC3339Nano, out.Baseline)
	if err != nil || bl.IsZero() {
		t.Fatalf("baseline %q: %v", out.Baseline, err)
	}
}

func TestList_BaselineStable(t *testing.T) {
	h, _ := setup(t)
	if a, b := get(t, h).Baseline, get(t, h).Baseline; a != b {
		t.Fatalf("baseline changed: %s vs %s", a, b)
	}
}
