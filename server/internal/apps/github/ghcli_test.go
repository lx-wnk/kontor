package github_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/apps/github"
)

// An absent gh must be its own error, not a generic exec failure: the fix
// ("install the GitHub CLI") is different from the fix for a logged-out one.
func TestTokenFromGhCLIReportsAMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := github.TokenFromGhCLI(t.Context())

	if !errors.Is(err, github.ErrGhNotFound) {
		t.Fatalf("err = %v, want ErrGhNotFound", err)
	}
}

// A gh that exits non-zero must surface gh's own stderr, which says what to
// run. Faked by putting a failing `gh` on PATH.
func TestTokenFromGhCLISurfacesWhatGhSaid(t *testing.T) {
	dir := t.TempDir()
	writeFakeGh(t, dir, "#!/bin/sh\necho 'please run: gh auth login' >&2\nexit 1\n")
	t.Setenv("PATH", dir)

	_, err := github.TokenFromGhCLI(t.Context())

	if err == nil || !contains(err.Error(), "gh auth login") {
		t.Fatalf("err = %v, want gh's own advice", err)
	}
}

// An authenticated gh prints the token and nothing else; trailing newline must
// not travel into an Authorization header.
func TestTokenFromGhCLITrimsTheToken(t *testing.T) {
	dir := t.TempDir()
	writeFakeGh(t, dir, "#!/bin/sh\necho 'gho_abc123'\n")
	t.Setenv("PATH", dir)

	token, err := github.TokenFromGhCLI(t.Context())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "gho_abc123" {
		t.Fatalf("token = %q, want %q", token, "gho_abc123")
	}
}

// gh exiting 0 with nothing is not a token, and must not be handed to GitHub
// as an empty Authorization header.
func TestTokenFromGhCLIRejectsAnEmptyAnswer(t *testing.T) {
	dir := t.TempDir()
	writeFakeGh(t, dir, "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)

	if _, err := github.TokenFromGhCLI(t.Context()); err == nil {
		t.Fatal("empty output must be an error")
	}
}

func writeFakeGh(t *testing.T, dir, script string) {
	t.Helper()
	path := dir + "/gh"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
