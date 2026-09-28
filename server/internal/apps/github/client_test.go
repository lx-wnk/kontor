package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/apps/github"
)

func TestParseReposAcceptsOwnerNamePairsAndRejectsEverythingElse(t *testing.T) {
	got, err := github.ParseRepos(" lx-wnk/kontor , golang/go ")
	require.NoError(t, err)
	require.Equal(t, []string{"lx-wnk/kontor", "golang/go"}, got)

	empty, err := github.ParseRepos("")
	require.NoError(t, err)
	require.Empty(t, empty)

	for _, bad := range []string{"agent-dashboard", "a/b/c", "/name", "owner/", "own er/name"} {
		_, err := github.ParseRepos(bad)
		require.Errorf(t, err, "ParseRepos(%q) must refuse a malformed entry", bad)
	}
}

// TestParseReposRefusesPathTraversalShapedEntries is D4 at the parse level:
// a repository name that could be read two ways must be refused outright,
// never silently normalised into something else — the same lesson that
// produced obsidian.resolveVaultPath. "../" is not a valid owner/name
// character, so it fails the segment check rather than being cleaned into a
// different, possibly-allowed repository.
func TestParseReposRefusesPathTraversalShapedEntries(t *testing.T) {
	for _, bad := range []string{"../secret/repo", "owner/../other", "owner/..", "../..", "owner/name/../other"} {
		_, err := github.ParseRepos(bad)
		require.Errorf(t, err, "ParseRepos(%q) must refuse a path-traversal-shaped entry, not normalise it", bad)
	}
}

// newFakeGitHub serves the four endpoints the client calls and records the
// last request it saw, so a test can prove both what was sent and that
// nothing was sent at all.
func newFakeGitHub(t *testing.T) (*httptest.Server, *http.Request, *bool) {
	t.Helper()
	var last http.Request
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/lx-wnk/kontor/pulls", func(w http.ResponseWriter, r *http.Request) {
		called, last = true, *r
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"number": 42, "title": "Add the cockpit", "draft": false,
			"html_url":   "https://github.com/lx-wnk/kontor/pull/42",
			"updated_at": "2026-09-01T10:00:00Z",
			"user":       map[string]any{"login": "lx-wnk"},
		}})
	})
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		called, last = true, *r
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
			"number": 7, "title": "Flaky test",
			"html_url":       "https://github.com/lx-wnk/kontor/issues/7",
			"repository_url": "https://api.github.com/repos/lx-wnk/kontor",
		}}})
	})
	mux.HandleFunc("/repos/lx-wnk/kontor/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
		called, last = true, *r
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "https://github.com/lx-wnk/kontor/pull/42#issuecomment-1"})
	})
	mux.HandleFunc("/repos/lx-wnk/kontor/pulls/42/merge", func(w http.ResponseWriter, r *http.Request) {
		called, last = true, *r
		_ = json.NewEncoder(w).Encode(map[string]any{"merged": true, "sha": "deadbeef"})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, &last, &called
}

func newTestClient(t *testing.T, ts *httptest.Server) *github.Client {
	t.Helper()
	c, err := github.NewClient(github.Config{
		Token:   "ghp_test",
		BaseURL: ts.URL,
		Repos:   []string{"lx-wnk/kontor"},
		// The fake server listens on loopback, which validation.SafeDialContext
		// refuses by design. Tests opt out of the guard; production never does.
		AllowLoopback: true,
	})
	require.NoError(t, err)
	return c
}

// clientWithRepos builds a client whose allow-list is exactly repos. BoundQuery
// never issues a request, so no fake server is needed.
func clientWithRepos(t *testing.T, repos ...string) *github.Client {
	t.Helper()
	c, err := github.NewClient(github.Config{Token: "ghp_test", BaseURL: "https://api.github.test", Repos: repos})
	require.NoError(t, err)
	return c
}

func TestOpenPullRequestsSendsTheTokenAndParsesTheAnswer(t *testing.T) {
	ts, last, _ := newFakeGitHub(t)
	prs, err := newTestClient(t, ts).OpenPullRequests(context.Background(), "lx-wnk/kontor", 5)
	require.NoError(t, err)
	require.Len(t, prs, 1)
	require.Equal(t, 42, prs[0].Number)
	require.Equal(t, "Add the cockpit", prs[0].Title)
	require.Equal(t, "lx-wnk", prs[0].Author)
	require.Equal(t, "Bearer ghp_test", last.Header.Get("Authorization"))
	require.Equal(t, "open", last.URL.Query().Get("state"))
	require.Equal(t, "5", last.URL.Query().Get("per_page"))
}

// TestEveryRepoScopedCallRefusesARepoOutsideTheAllowList is D4 at the client
// level: the allow-list is a property of the configured client, so it holds
// even for a caller that reached the client without asking the gate.
func TestEveryRepoScopedCallRefusesARepoOutsideTheAllowList(t *testing.T) {
	ts, _, called := newFakeGitHub(t)
	c := newTestClient(t, ts)
	ctx := context.Background()

	_, err := c.OpenPullRequests(ctx, "evil/repo", 5)
	require.ErrorIs(t, err, github.ErrRepoNotAllowed)
	_, err = c.Comment(ctx, "evil/repo", 1, "hi")
	require.ErrorIs(t, err, github.ErrRepoNotAllowed)
	_, err = c.MergePullRequest(ctx, "evil/repo", 1, "squash")
	require.ErrorIs(t, err, github.ErrRepoNotAllowed)

	require.False(t, *called, "no request may reach GitHub for a repository outside the allow-list")
	require.False(t, c.AllowsRepo("evil/repo"))
	require.True(t, c.AllowsRepo("lx-wnk/kontor"))
}

func TestCommentAndMergeReturnTheirResultURLs(t *testing.T) {
	ts, last, _ := newFakeGitHub(t)
	c := newTestClient(t, ts)
	ctx := context.Background()

	url, err := c.Comment(ctx, "lx-wnk/kontor", 42, "looks good")
	require.NoError(t, err)
	require.Contains(t, url, "issuecomment")
	require.Equal(t, http.MethodPost, last.Method)

	sha, err := c.MergePullRequest(ctx, "lx-wnk/kontor", 42, "squash")
	require.NoError(t, err)
	require.Equal(t, "deadbeef", sha)
	require.Equal(t, http.MethodPut, last.Method)
}

func TestSearchIssuesReportsTheOwningRepository(t *testing.T) {
	ts, last, _ := newFakeGitHub(t)
	hits, err := newTestClient(t, ts).SearchIssues(context.Background(), "is:open flaky")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "lx-wnk/kontor", hits[0].Repo)
	require.Equal(t, 7, hits[0].Number)
	require.Contains(t, last.URL.Query().Get("q"), "flaky")
}

// TestClientErrorsNeverCarryTheToken: an upstream 401 is the single most
// likely error a user will paste into an issue.
func TestClientErrorsNeverCarryTheToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	t.Cleanup(ts.Close)
	c, err := github.NewClient(github.Config{Token: "ghp_supersecret", BaseURL: ts.URL, Repos: []string{"lx-wnk/kontor"}, AllowLoopback: true})
	require.NoError(t, err)

	_, err = c.OpenPullRequests(context.Background(), "lx-wnk/kontor", 5)
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "ghp_supersecret"), "the token must never appear in an error: %v", err)
}

func TestNewClientRefusesAnUnparseableBaseURL(t *testing.T) {
	_, err := github.NewClient(github.Config{Token: "t", BaseURL: "://nope", Repos: []string{"a/b"}})
	require.Error(t, err)
}

// TestNewClientRequiresLoopbackOptOut proves the SSRF guard is actually wired
// in production mode: with AllowLoopback left false (the zero value, and what
// serverapp.buildGitHubClient always uses), a BaseURL pointing at loopback is
// refused by validation.SafeDialContext on the first request, exactly as the
// GitHub Enterprise-on-LAN limitation documented on NewClient describes.
func TestNewClientRequiresLoopbackOptOut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	t.Cleanup(ts.Close)

	c, err := github.NewClient(github.Config{Token: "t", BaseURL: ts.URL, Repos: []string{"lx-wnk/kontor"}})
	require.NoError(t, err)

	_, err = c.OpenPullRequests(context.Background(), "lx-wnk/kontor", 5)
	require.Error(t, err)
}

// TestChecksSummarisesMixedCheckRuns proves the state/count collapse: one
// failed run makes the whole commit "failure", and the counts are exact.
func TestChecksSummarisesMixedCheckRuns(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/lx-wnk/kontor/commits/deadbeef/check-runs", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 3,
			"check_runs": []map[string]any{
				{"status": "completed", "conclusion": "success"},
				{"status": "completed", "conclusion": "failure"},
				{"status": "in_progress", "conclusion": nil},
			},
		})
	}))
	t.Cleanup(ts.Close)

	summary, err := newTestClient(t, ts).Checks(context.Background(), "lx-wnk/kontor", "deadbeef")
	require.NoError(t, err)
	require.Equal(t, github.CheckStateFailure, summary.State)
	require.Equal(t, 1, summary.Passed)
	require.Equal(t, 1, summary.Failed)
	require.Equal(t, 3, summary.Total)
}

// TestChecksReportsNoneWhenGitHubHasNoCheckRuns proves the zero-total case:
// a commit nobody configured CI for must read as "none", not "success" —
// zero checks is not the same claim as zero failures.
func TestChecksReportsNoneWhenGitHubHasNoCheckRuns(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "check_runs": []any{}})
	}))
	t.Cleanup(ts.Close)

	summary, err := newTestClient(t, ts).Checks(context.Background(), "lx-wnk/kontor", "deadbeef")
	require.NoError(t, err)
	require.Equal(t, github.CheckStateNone, summary.State)
	require.Zero(t, summary.Total)
}

// TestChecksRefusesARepoOutsideTheAllowList is D4 at the client level, same
// as TestEveryRepoScopedCallRefusesARepoOutsideTheAllowList.
func TestChecksRefusesARepoOutsideTheAllowList(t *testing.T) {
	ts, _, called := newFakeGitHub(t)
	c := newTestClient(t, ts)
	_, err := c.Checks(context.Background(), "evil/repo", "deadbeef")
	require.ErrorIs(t, err, github.ErrRepoNotAllowed)
	require.False(t, *called)
}

// TestStatusErrorDistinguishesNotFoundFromForbidden proves a caller can tell
// "no such repository/PR" (404) apart from "token lacks scope" (403) via
// errors.As, instead of parsing the error string — the distinction Task 5/6
// need to turn into honest HTTP status codes and MCP tool errors.
func TestStatusErrorDistinguishesNotFoundFromForbidden(t *testing.T) {
	status := http.StatusNotFound
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "test message"})
	}))
	t.Cleanup(ts.Close)
	c, err := github.NewClient(github.Config{Token: "t", BaseURL: ts.URL, Repos: []string{"lx-wnk/kontor"}, AllowLoopback: true})
	require.NoError(t, err)

	_, err = c.OpenPullRequests(context.Background(), "lx-wnk/kontor", 5)
	var notFound *github.StatusError
	require.ErrorAs(t, err, &notFound)
	require.Equal(t, http.StatusNotFound, notFound.StatusCode)

	status = http.StatusForbidden
	_, err = c.OpenPullRequests(context.Background(), "lx-wnk/kontor", 5)
	var forbidden *github.StatusError
	require.ErrorAs(t, err, &forbidden)
	require.Equal(t, http.StatusForbidden, forbidden.StatusCode)

	require.NotEqual(t, notFound.StatusCode, forbidden.StatusCode, "404 and 403 must be distinguishable, not collapsed into one opaque error")
}

// TestBoundQueryRefusesACallerSuppliedScopeQualifier pins the security
// property, not the string-building. GitHub unions repeated repo:/org:/user:
// qualifiers — verified against the live API: `test repo:golang/go` returns
// 31445 hits, `test repo:nodejs/node` 46437, and both together 77882, the
// exact sum — so a caller-supplied one is a second branch of the same OR and
// reaches a repository the allow-list never named. Appending cannot bound a
// query that is free to widen itself.
func TestBoundQueryRefusesACallerSuppliedScopeQualifier(t *testing.T) {
	c := clientWithRepos(t, "lx-wnk/kontor")

	for _, query := range []string{
		"secret repo:othercorp/private",
		"secret org:othercorp",
		"secret user:someone",
		// owner: unions exactly like repo: and was missing from the first
		// version of this list, so a caller reached repositories the operator
		// never named. Measured live: `test repo:golang/go` = 31451 hits,
		// `test owner:nodejs` = 80920, both together = 112371 — the exact sum.
		"secret owner:othercorp",
		"secret REPO:OtherCorp/Private",
		"repo:othercorp/private",
	} {
		got, err := c.BoundQuery(query)
		require.ErrorIs(t, err, github.ErrQueryWidensScope, "query %q must be refused", query)
		require.Empty(t, got, "a refused query must yield no string a caller could send anyway")
	}
}

func TestBoundQueryAppendsEveryAllowedRepository(t *testing.T) {
	c := clientWithRepos(t, "lx-wnk/kontor", "lx-wnk/other")

	got, err := c.BoundQuery("crash on startup")
	require.NoError(t, err)
	require.Equal(t, "crash on startup repo:lx-wnk/kontor repo:lx-wnk/other", got)
}

// A bare word that merely contains a qualifier's letters is not a qualifier —
// the check is per whitespace-separated field, so "superuser:x" would be a
// false positive and "repository" must not trip it either.
func TestBoundQueryDoesNotRefuseAnOrdinaryWord(t *testing.T) {
	c := clientWithRepos(t, "lx-wnk/kontor")

	got, err := c.BoundQuery("repository refactor")
	require.NoError(t, err)
	require.Equal(t, "repository refactor repo:lx-wnk/kontor", got)
}

// A qualifier naming a repository the operator already listed narrows a
// multi-repo search rather than widening it — the commonest refinement there
// is — so refusing it would be a usability regression with no security gain.
func TestBoundQueryAllowsNarrowingToAnAllowedRepository(t *testing.T) {
	c := clientWithRepos(t, "lx-wnk/kontor", "lx-wnk/other")

	got, err := c.BoundQuery("bug repo:lx-wnk/other")
	require.NoError(t, err)
	require.Contains(t, got, "repo:lx-wnk/other")
}

// The allow-list must hold on the answer, not only on the question. Refusing
// self-widening queries relies on a denylist of GitHub's scope qualifiers being
// complete, and it was not — `owner:` was missing. This pins the boundary that
// does not depend on that list: a hit outside the allow-list is dropped however
// it entered the result set.
func TestSearchIssuesDropsAHitOutsideTheAllowList(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"number":1,"title":"listed","html_url":"https://x.test/1","repository_url":"https://api.github.com/repos/lx-wnk/kontor"},
			{"number":2,"title":"NOT listed","html_url":"https://x.test/2","repository_url":"https://api.github.com/repos/othercorp/private"}
		]}`))
	}))
	t.Cleanup(ts.Close)

	hits, err := newTestClient(t, ts).SearchIssues(context.Background(), "anything")
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "lx-wnk/kontor", hits[0].Repo)
}
