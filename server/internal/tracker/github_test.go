package tracker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/tracker"
)

func ghServer(t *testing.T, status int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestGitHubFetchIssue_URLRef(t *testing.T) {
	srv := ghServer(t, 200, map[string]any{
		"title":    "Fix login bug",
		"body":     "Description here",
		"html_url": "https://github.com/owner/repo/issues/42",
		"number":   42,
		"labels":   []map[string]any{{"name": "bug"}, {"name": "p1"}},
	})
	defer srv.Close()

	cli := tracker.NewGitHubClientWithBase(srv.URL, "token-abc", "", &http.Client{})
	iss, err := cli.FetchIssue(context.Background(), "https://github.com/owner/repo/issues/42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iss.Title != "Fix login bug" {
		t.Errorf("title: got %q", iss.Title)
	}
	if iss.Key != "#42" {
		t.Errorf("key: got %q", iss.Key)
	}
	if len(iss.Labels) != 2 || iss.Labels[0] != "bug" {
		t.Errorf("labels: %v", iss.Labels)
	}
	if iss.Tracker != "github" {
		t.Errorf("tracker: %q", iss.Tracker)
	}
}

func TestGitHubFetchIssue_SlashRef(t *testing.T) {
	srv := ghServer(t, 200, map[string]any{"title": "Slash ref", "body": "", "html_url": "https://github.com/o/r/issues/7", "number": 7, "labels": []any{}})
	defer srv.Close()
	cli := tracker.NewGitHubClientWithBase(srv.URL, "tok", "", &http.Client{})
	iss, err := cli.FetchIssue(context.Background(), "o/r#7")
	if err != nil {
		t.Fatalf("slash ref: %v", err)
	}
	if iss.Key != "#7" {
		t.Errorf("key: %q", iss.Key)
	}
}

func TestGitHubFetchIssue_BareRef_DefaultRepo(t *testing.T) {
	srv := ghServer(t, 200, map[string]any{"title": "Bare", "body": "", "html_url": "https://github.com/d/r/issues/3", "number": 3, "labels": []any{}})
	defer srv.Close()
	cli := tracker.NewGitHubClientWithBase(srv.URL, "tok", "d/r", &http.Client{})
	iss, err := cli.FetchIssue(context.Background(), "#3")
	if err != nil {
		t.Fatalf("bare ref: %v", err)
	}
	if iss.Key != "#3" {
		t.Errorf("key: %q", iss.Key)
	}
}

func TestGitHubFetchIssue_BareRef_NoDefaultRepo(t *testing.T) {
	cli := tracker.NewGitHubClientWithBase("http://unused", "tok", "", &http.Client{})
	_, err := cli.FetchIssue(context.Background(), "#5")
	if err == nil {
		t.Fatal("expected error for bare ref without default repo")
	}
}

func TestGitHubFetchIssue_RejectsMaliciousRefs(t *testing.T) {
	bad := []string{
		"../../other/repo#1",
		"o/r?evil=x#1",
		"o/../../../search/issues#1",
		"owner/..#1",
		"https://github.com/../../other/repo/issues/1",
	}
	cli := tracker.NewGitHubClientWithBase("http://must-not-be-called", "tok", "", &http.Client{})
	for _, ref := range bad {
		_, err := cli.FetchIssue(context.Background(), ref)
		if !errors.Is(err, tracker.ErrBadRef) {
			t.Errorf("ref %q: expected ErrBadRef, got %v", ref, err)
		}
	}
}

func TestGitHubFetchIssue_AcceptsLegitDotRef(t *testing.T) {
	srv := ghServer(t, 200, map[string]any{"title": "Dot", "body": "", "html_url": "h", "number": 1, "labels": []any{}})
	defer srv.Close()
	cli := tracker.NewGitHubClientWithBase(srv.URL, "tok", "", &http.Client{})
	if _, err := cli.FetchIssue(context.Background(), "owner/repo.js#1"); err != nil {
		t.Errorf("legit dotted repo rejected: %v", err)
	}
}

func TestGitHubFetchIssue_404(t *testing.T) {
	srv := ghServer(t, 404, map[string]any{"message": "Not Found"})
	defer srv.Close()
	cli := tracker.NewGitHubClientWithBase(srv.URL, "tok", "o/r", &http.Client{})
	_, err := cli.FetchIssue(context.Background(), "o/r#99")
	if !errors.Is(err, tracker.ErrIssueNotFound) {
		t.Errorf("expected ErrIssueNotFound, got %v", err)
	}
}

func TestGitHubFetchIssue_401(t *testing.T) {
	srv := ghServer(t, 401, map[string]any{"message": "Bad credentials"})
	defer srv.Close()
	cli := tracker.NewGitHubClientWithBase(srv.URL, "bad-tok", "o/r", &http.Client{})
	_, err := cli.FetchIssue(context.Background(), "o/r#1")
	if !errors.Is(err, tracker.ErrTrackerAuth) {
		t.Errorf("expected ErrTrackerAuth, got %v", err)
	}
}

func TestGitHubFetchIssue_AuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"title": "X", "body": "", "html_url": "h", "number": 1, "labels": []any{}})
	}))
	defer srv.Close()
	cli := tracker.NewGitHubClientWithBase(srv.URL, "my-secret-token", "o/r", &http.Client{})
	_, _ = cli.FetchIssue(context.Background(), "o/r#1")
	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("auth header: %q", gotAuth)
	}
}
