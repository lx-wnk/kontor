package tracker_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/tracker"
)

func TestResolve_GitHubURL(t *testing.T) {
	cfg := tracker.Config{GitHubToken: "tok"}
	tr, err := tracker.Resolve("https://github.com/owner/repo/issues/1", cfg, &http.Client{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil tracker")
	}
}

func TestResolve_GitHubSlash(t *testing.T) {
	cfg := tracker.Config{GitHubToken: "tok"}
	tr, err := tracker.Resolve("owner/repo#5", cfg, &http.Client{})
	if err != nil || tr == nil {
		t.Fatalf("slash ref: err=%v tr=%v", err, tr)
	}
}

func TestResolve_JiraKey(t *testing.T) {
	cfg := tracker.Config{JiraBaseURL: "https://jira.example.com", JiraToken: "tok", JiraEmail: "u@x.com"}
	tr, err := tracker.Resolve("PROJ-42", cfg, &http.Client{})
	if err != nil || tr == nil {
		t.Fatalf("jira key: err=%v tr=%v", err, tr)
	}
}

func TestResolve_JiraURL(t *testing.T) {
	cfg := tracker.Config{JiraBaseURL: "https://j.example.com", JiraToken: "t", JiraEmail: "e"}
	tr, err := tracker.Resolve("https://j.example.com/browse/AB-7", cfg, &http.Client{})
	if err != nil || tr == nil {
		t.Fatalf("jira browse url: err=%v tr=%v", err, tr)
	}
}

func TestResolve_MissingGitHubToken(t *testing.T) {
	cfg := tracker.Config{} // no token
	_, err := tracker.Resolve("owner/repo#1", cfg, &http.Client{})
	if err == nil {
		t.Fatal("expected error for missing GitHub token")
	}
}

func TestResolve_MissingJiraConfig(t *testing.T) {
	cfg := tracker.Config{} // no jira config
	_, err := tracker.Resolve("PROJ-1", cfg, &http.Client{})
	if err == nil {
		t.Fatal("expected error for missing Jira config")
	}
}

func TestResolve_RejectsMaliciousGitHubRefs(t *testing.T) {
	cfg := tracker.Config{GitHubToken: "tok"}
	bad := []string{
		"../../other/repo#1",
		"o/r?evil=x#1",
		"o/../../../search/issues#1",
		"owner/..#1",
	}
	for _, ref := range bad {
		_, err := tracker.Resolve(ref, cfg, &http.Client{})
		if !errors.Is(err, tracker.ErrBadRef) {
			t.Errorf("ref %q: expected ErrBadRef, got %v", ref, err)
		}
	}
}

func TestResolve_AcceptsLegitGitHubRefs(t *testing.T) {
	cfg := tracker.Config{GitHubToken: "tok"}
	good := []string{
		"owner/repo#1",
		"owner/repo.js#1",
		"https://github.com/owner/repo/issues/1",
	}
	for _, ref := range good {
		tr, err := tracker.Resolve(ref, cfg, &http.Client{})
		if err != nil || tr == nil {
			t.Errorf("ref %q: err=%v tr=%v", ref, err, tr)
		}
	}
}

func TestResolve_UnrecognizedRef(t *testing.T) {
	cfg := tracker.Config{GitHubToken: "tok"}
	_, err := tracker.Resolve("not-any-tracker-ref", cfg, &http.Client{})
	if !errors.Is(err, tracker.ErrBadRef) {
		t.Errorf("expected ErrBadRef, got %v", err)
	}
}
