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

// adfDoc is a minimal ADF (Atlassian Document Format) document with two paragraphs.
var adfDoc = map[string]any{
	"type": "doc",
	"content": []any{
		map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": "First paragraph."},
			},
		},
		map[string]any{
			"type": "paragraph",
			"content": []any{
				map[string]any{"type": "text", "text": "Second paragraph."},
			},
		},
	},
}

func jiraServer(t *testing.T, statusCode int, body any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(body)
	}))
}

func TestJiraFetchIssue_KeyRef(t *testing.T) {
	srv := jiraServer(t, 200, map[string]any{
		"fields": map[string]any{
			"summary":     "Login timeout",
			"description": adfDoc,
			"labels":      []string{"backend"},
		},
	})
	defer srv.Close()

	cli := tracker.NewJiraClient(srv.URL, "user@example.com", "jira-tok", &http.Client{})
	iss, err := cli.FetchIssue(context.Background(), "PROJ-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iss.Title != "Login timeout" {
		t.Errorf("title: %q", iss.Title)
	}
	if iss.Key != "PROJ-42" {
		t.Errorf("key: %q", iss.Key)
	}
	if iss.Tracker != "jira" {
		t.Errorf("tracker: %q", iss.Tracker)
	}
	if iss.Body == "" {
		t.Error("body must not be empty after ADF flatten")
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "backend" {
		t.Errorf("labels: %v", iss.Labels)
	}
	if iss.URL == "" {
		t.Error("URL must not be empty")
	}
}

func TestJiraFetchIssue_BrowseURLRef(t *testing.T) {
	srv := jiraServer(t, 200, map[string]any{
		"fields": map[string]any{"summary": "Browse URL ref", "description": nil, "labels": []string{}},
	})
	defer srv.Close()
	cli := tracker.NewJiraClient(srv.URL, "u", "t", &http.Client{})
	iss, err := cli.FetchIssue(context.Background(), srv.URL+"/browse/AB-7")
	if err != nil {
		t.Fatalf("browse url ref: %v", err)
	}
	if iss.Key != "AB-7" {
		t.Errorf("key: %q", iss.Key)
	}
}

func TestJiraFetchIssue_BasicAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"fields": map[string]any{"summary": "X", "description": nil, "labels": []string{}}})
	}))
	defer srv.Close()
	cli := tracker.NewJiraClient(srv.URL, "alice@example.com", "secret", &http.Client{})
	_, _ = cli.FetchIssue(context.Background(), "AB-1")
	// Basic base64("alice@example.com:secret")
	want64 := "YWxpY2VAZXhhbXBsZS5jb206c2VjcmV0"
	if gotAuth != "Basic "+want64 {
		t.Errorf("auth header: %q (want Basic %s)", gotAuth, want64)
	}
}

func TestJiraFetchIssue_404(t *testing.T) {
	srv := jiraServer(t, 404, map[string]any{"errorMessages": []string{"Issue not found"}})
	defer srv.Close()
	cli := tracker.NewJiraClient(srv.URL, "u", "t", &http.Client{})
	_, err := cli.FetchIssue(context.Background(), "PROJ-99")
	if !errors.Is(err, tracker.ErrIssueNotFound) {
		t.Errorf("expected ErrIssueNotFound, got %v", err)
	}
}

func TestJiraFetchIssue_401(t *testing.T) {
	srv := jiraServer(t, 401, map[string]any{"errorMessages": []string{"Unauthorized"}})
	defer srv.Close()
	cli := tracker.NewJiraClient(srv.URL, "u", "badtok", &http.Client{})
	_, err := cli.FetchIssue(context.Background(), "PROJ-1")
	if !errors.Is(err, tracker.ErrTrackerAuth) {
		t.Errorf("expected ErrTrackerAuth, got %v", err)
	}
}

func TestJiraFetchIssue_NilADF(t *testing.T) {
	srv := jiraServer(t, 200, map[string]any{
		"fields": map[string]any{"summary": "No body", "description": nil, "labels": []string{}},
	})
	defer srv.Close()
	cli := tracker.NewJiraClient(srv.URL, "u", "t", &http.Client{})
	iss, err := cli.FetchIssue(context.Background(), "X-1")
	if err != nil {
		t.Fatalf("nil ADF: %v", err)
	}
	if iss.Body != "" {
		t.Errorf("body should be empty for nil ADF, got %q", iss.Body)
	}
}
