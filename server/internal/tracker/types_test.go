package tracker_test

import (
	"errors"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/tracker"
)

func TestErrorsAreDistinct(t *testing.T) {
	if errors.Is(tracker.ErrTrackerAuth, tracker.ErrIssueNotFound) {
		t.Fatal("ErrTrackerAuth must not match ErrIssueNotFound")
	}
	if errors.Is(tracker.ErrTrackerAuth, tracker.ErrTrackerUpstream) {
		t.Fatal("ErrTrackerAuth must not match ErrTrackerUpstream")
	}
	if errors.Is(tracker.ErrBadRef, tracker.ErrTrackerAuth) {
		t.Fatal("ErrBadRef must not match ErrTrackerAuth")
	}
}

func TestIssueFields(t *testing.T) {
	iss := tracker.Issue{Tracker: "github", Key: "#1", Title: "T", Body: "B", URL: "https://x", Labels: []string{"bug"}}
	if iss.Tracker != "github" || iss.Key != "#1" {
		t.Fatal("unexpected field values")
	}
}
