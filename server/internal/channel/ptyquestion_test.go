package channel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/askq"
)

func TestGetQuestion_DetectsModalFromScrollback(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "askq_raw_v2_1_205.bin"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	hub := newPtyHub(256 * 1024)
	hub.Write(fixture)
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/question", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var q askq.DetectedQuestion
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if q.MultiSelect {
		t.Error("expected MultiSelect=false")
	}
	if len(q.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(q.Options))
	}
	labels := []string{q.Options[0].Label, q.Options[1].Label, q.Options[2].Label}
	want := []string{"Red", "Green", "Blue"}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("labels = %v, want %v", labels, want)
			break
		}
	}
	if q.TypeSomethingIndex != 4 {
		t.Errorf("TypeSomethingIndex = %d, want 4", q.TypeSomethingIndex)
	}
	if q.ChatAboutIndex != 5 {
		t.Errorf("ChatAboutIndex = %d, want 5", q.ChatAboutIndex)
	}
}

func TestGetQuestion_NoModalReturns204(t *testing.T) {
	hub := newPtyHub(1024)
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/question", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}

func TestGetQuestion_UnauthorizedRejected(t *testing.T) {
	hub := newPtyHub(1024)
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	// No bearer token at all.
	resp, err := http.Get(srv.URL + "/question")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for missing bearer, got %d", resp.StatusCode)
	}

	// Wrong bearer token.
	req, _ := http.NewRequest("GET", srv.URL+"/question", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for wrong bearer, got %d", resp2.StatusCode)
	}
}

func TestGetScreen_ReportsQuestionModal(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "askq_raw_v2_1_205.bin"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	hub := newPtyHub(256 * 1024)
	hub.Write(fixture)
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/screen", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var screen sdk.PendingScreen
	if err := json.NewDecoder(resp.Body).Decode(&screen); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if screen.Question == nil {
		t.Fatal("expected a question screen")
	}
	if screen.Confirm != nil {
		t.Error("expected Confirm to stay nil")
	}
	if len(screen.Question.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(screen.Question.Options))
	}
}

// The review/submit screen is invisible to GET /question by design, so it is
// the reason GET /screen exists: without it the dashboard cannot finish a
// multi-question flow.
func TestGetScreen_ReportsConfirmScreenThatQuestionEndpointMisses(t *testing.T) {
	hub := newPtyHub(64 * 1024)
	hub.Write([]byte("Review your answers\r\n\r\nReady to submit your answers?\r\n\r\n❯ 1. Submit answers\r\n  2. Cancel\r\n"))
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	get := func(path string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %s: %v", path, err)
		}
		return resp
	}

	qResp := get("/question")
	_, _ = io.Copy(io.Discard, qResp.Body)
	_ = qResp.Body.Close()
	if qResp.StatusCode != http.StatusNoContent {
		t.Fatalf("/question should not match the confirm screen, got %d", qResp.StatusCode)
	}

	resp := get("/screen")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 from /screen, got %d", resp.StatusCode)
	}

	var screen sdk.PendingScreen
	if err := json.NewDecoder(resp.Body).Decode(&screen); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if screen.Confirm == nil {
		t.Fatal("expected a confirm screen")
	}
	if screen.Confirm.Question != "Ready to submit your answers?" {
		t.Errorf("Question = %q", screen.Confirm.Question)
	}
	if len(screen.Confirm.Options) != 2 || screen.Confirm.Options[0].Label != "Submit answers" {
		t.Errorf("Options = %+v", screen.Confirm.Options)
	}
}

func TestGetScreen_NoScreenReturns204(t *testing.T) {
	hub := newPtyHub(1024)
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/screen", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("want 204, got %d", resp.StatusCode)
	}
}

func TestGetScreen_UnauthorizedRejected(t *testing.T) {
	hub := newPtyHub(1024)
	tok := newRotatingToken("secret")
	srv := httptest.NewServer(ptyMux(newPtyWriter(io.Discard), hub, tok))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/screen")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401 for missing bearer, got %d", resp.StatusCode)
	}
}
