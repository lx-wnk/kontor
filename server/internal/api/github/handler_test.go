package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	githubapi "github.com/lx-wnk/kontor/server/internal/api/github"
	githubapp "github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

const testRepo = "lx-wnk/kontor"

// defaultUpstream is the fake GitHub every ordinary test runs against: one
// open pull request, one successful merge, one successful comment, an empty
// search result.
func defaultUpstream(called *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*called = true
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"number": 42, "title": "t", "html_url": "u", "draft": false,
				"updated_at": "2026-09-01T10:00:00Z", "user": map[string]any{"login": "lx-wnk"},
			}})
		case strings.HasSuffix(r.URL.Path, "/merge"):
			_ = json.NewEncoder(w).Encode(map[string]any{"merged": true, "sha": "deadbeef"})
		case strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "c"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		}
	}
}

// newEnvWithUpstream wires a Handler against an in-memory database with the
// GitHub capabilities catalogued (github.Register) and a fake GitHub served
// by upstream. The Gate carries no Asker, so an "ask" effect fails closed and
// a test can tell deny from ask by the error text alone.
func newEnvWithUpstream(t *testing.T, upstream http.HandlerFunc) (http.Handler, repo.GrantRepo, context.Context) {
	t.Helper()
	return newEnvWithRepos(t, upstream, []string{testRepo})
}

func newEnvWithRepos(t *testing.T, upstream http.HandlerFunc, repos []string) (http.Handler, repo.GrantRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	caps := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, githubapp.Register(ctx, resources, caps))

	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)

	client, err := githubapp.NewClient(githubapp.Config{
		Token: "ghp_supersecret", BaseURL: srv.URL,
		Repos: repos, AllowLoopback: true,
	})
	require.NoError(t, err)

	h := githubapi.NewHandler(client, memory.Gate{
		Capabilities: caps,
		Grants:       grants,
		GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
	})
	r := chi.NewRouter()
	h.Mount(r)
	return r, grants, ctx
}

// newEnv is newEnvWithUpstream fixed to defaultUpstream, plus a flag
// reporting whether it was ever reached — the proof every allow-list and
// gate test needs that GitHub was never called.
func newEnv(t *testing.T) (http.Handler, repo.GrantRepo, *bool, context.Context) {
	t.Helper()
	called := false
	h, grants, ctx := newEnvWithUpstream(t, defaultUpstream(&called))
	return h, grants, &called, ctx
}

func allowGlobally(t *testing.T, grants repo.GrantRepo, ctx context.Context, capName string) {
	t.Helper()
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: capName,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        "*",
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)
}

func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMergeIsDeniedWithNoGrantAndTheReasonNamesTheClassDefault is spec §6 row
// 1, and the reason github.merge is class "spend": with no grant, Decide's
// defaultEffect denies outright rather than asking, and the reason it gives
// names that class default rather than reading like an opaque refusal.
func TestMergeIsDeniedWithNoGrantAndTheReasonNamesTheClassDefault(t *testing.T) {
	h, _, called, _ := newEnv(t)
	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"`+testRepo+`","number":42}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "capability denied")
	require.Contains(t, rec.Body.String(), "spend")
	require.Contains(t, rec.Body.String(), "defaults to deny")
	require.False(t, *called, "GitHub must not be reached before the gate allows the merge")
}

// TestMergeIsAllowedWithAnExplicitGlobalGrant is spec §6 row 2.
func TestMergeIsAllowedWithAnExplicitGlobalGrant(t *testing.T) {
	h, grants, called, ctx := newEnv(t)
	allowGlobally(t, grants, ctx, githubapp.CapabilityMerge)
	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"`+testRepo+`","number":42}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "deadbeef")
	require.True(t, *called)
}

// TestRepoOutsideTheAllowListIsRefusedBeforeTheGate is spec §6 row 3 and
// decision D4: no capability question is asked at all.
//
// Deliberately grants NO capability, rather than granting merge globally: a
// "*" pattern grant matches "evil/repo" too (capability.Match treats a
// trailing "*" as a prefix check against everything), so if the gate were
// consulted it would answer allow — and the allow-list check, wherever it
// ran, would still produce the same 403 "allow-list" refusal either way.
// With no grant at all, a gate-first bug is distinguishable: the gate alone
// would deny with "capability denied" (class default), a different message
// than the allow-list's, and this test would catch the swap.
func TestRepoOutsideTheAllowListIsRefusedBeforeTheGate(t *testing.T) {
	h, _, called, _ := newEnv(t)
	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"evil/repo","number":1}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "allow-list")
	require.NotContains(t, rec.Body.String(), "capability denied")
	require.False(t, *called)
}

func TestSummaryAndSearchAndCommentEachGateOnTheirOwnCapability(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		target   string
		body     string
		grant    string
		otherCap string
	}{
		{"summary", http.MethodGet, "/api/github/summary", "", githubapp.CapabilityRead, githubapp.CapabilityMerge},
		{"search", http.MethodGet, "/api/github/search?q=flaky", "", githubapp.CapabilitySearch, githubapp.CapabilityRead},
		{"comment", http.MethodPost, "/api/github/comment", `{"repo":"` + testRepo + `","number":42,"body":"hi"}`, githubapp.CapabilityComment, githubapp.CapabilityRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A grant on a DIFFERENT capability must not open this route.
			h, grants, _, ctx := newEnv(t)
			allowGlobally(t, grants, ctx, tc.otherCap)
			rec := do(t, h, tc.method, tc.target, tc.body)
			require.Equal(t, http.StatusForbidden, rec.Code, "the wrong grant must not open %s", tc.target)

			h2, grants2, _, ctx2 := newEnv(t)
			allowGlobally(t, grants2, ctx2, tc.grant)
			rec2 := do(t, h2, tc.method, tc.target, tc.body)
			require.Equal(t, http.StatusOK, rec2.Code, "body: %s", rec2.Body.String())
		})
	}
}

// checksField is the subset of pullRequestView's JSON shape these tests read.
type checksField struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Checks struct {
		State  string `json:"state"`
		Passed int    `json:"passed"`
		Failed int    `json:"failed"`
		Total  int    `json:"total"`
	} `json:"checks"`
}

func decodeSummaryPullRequests(t *testing.T, rec *httptest.ResponseRecorder) []checksField {
	t.Helper()
	var body struct {
		Repos []struct {
			PullRequests []checksField `json:"pullRequests"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Repos, 1)
	return body.Repos[0].PullRequests
}

func summaryPullFixture(number int, title, sha string) map[string]any {
	return map[string]any{
		"number": number, "title": title, "html_url": "https://example.test/" + title, "draft": false,
		"updated_at": "2026-09-01T10:00:00Z", "user": map[string]any{"login": "lx-wnk"},
		"head": map[string]any{"sha": sha},
	}
}

// TestSummaryChecksReportsMixedResultsWithCounts proves the state/count
// collapse reaches the wire: one failed check run makes checks.state
// "failure", and passed/failed/total are exact, not just the state.
func TestSummaryChecksReportsMixedResultsWithCounts(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{summaryPullFixture(42, "t", "deadbeef")})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 2,
				"check_runs": []map[string]any{
					{"status": "completed", "conclusion": "success"},
					{"status": "completed", "conclusion": "failure"},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)
	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code)

	prs := decodeSummaryPullRequests(t, rec)
	require.Len(t, prs, 1)
	require.Equal(t, "failure", prs[0].Checks.State)
	require.Equal(t, 1, prs[0].Checks.Passed)
	require.Equal(t, 1, prs[0].Checks.Failed)
	require.Equal(t, 2, prs[0].Checks.Total)
}

// TestSummaryChecksReportsNoneWhenThePullRequestHasNoChecks proves a commit
// with zero check runs reads as "none", the same state a failed lookup uses —
// both mean "the panel has nothing to say about CI here".
func TestSummaryChecksReportsNoneWhenThePullRequestHasNoChecks(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{summaryPullFixture(42, "t", "deadbeef")})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "check_runs": []any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)
	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code)

	prs := decodeSummaryPullRequests(t, rec)
	require.Len(t, prs, 1)
	require.Equal(t, "none", prs[0].Checks.State)
	require.Zero(t, prs[0].Checks.Total)
}

// TestSummaryChecksLookupFailureDoesNotBlankThePullRequest is the case that
// matters: a check-run lookup that itself fails (GitHub 500s the check-runs
// endpoint for one pull request) must not turn the summary route's 200 into
// an error, must not drop that pull request from the list, and must not
// affect the other pull request's own checks — only that one PR's checks
// read as "none".
func TestSummaryChecksLookupFailureDoesNotBlankThePullRequest(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				summaryPullFixture(1, "good", "good-sha"),
				summaryPullFixture(2, "broken lookup", "bad-sha"),
			})
		case strings.Contains(r.URL.Path, "/commits/bad-sha/"):
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "server error"})
		case strings.Contains(r.URL.Path, "/commits/good-sha/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"check_runs":  []map[string]any{{"status": "completed", "conclusion": "success"}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)
	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code, "a failed check-run lookup must not turn the whole summary into an error: %s", rec.Body.String())

	prs := decodeSummaryPullRequests(t, rec)
	require.Len(t, prs, 2, "the pull request whose check lookup failed must still be listed")

	byNumber := map[int]string{}
	for _, pr := range prs {
		byNumber[pr.Number] = pr.Checks.State
	}
	require.Equal(t, "none", byNumber[2], "a failed check-run lookup must read as 'none', not an error")
	require.Equal(t, "success", byNumber[1], "the other pull request's checks must be unaffected")
}

// TestSummaryMergesInvolvedPullRequestsDedupedAndCapped proves the
// involves:@me search adds pull requests from repositories beyond the
// configured allow-list, that a search hit duplicating a configured
// repository's own pull request is dropped in favour of the richer answer,
// and that the merged total never exceeds summaryPRCap.
func TestSummaryMergesInvolvedPullRequestsDedupedAndCapped(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{summaryPullFixture(42, "configured", "sha-42")})
		case strings.HasSuffix(r.URL.Path, "/search/issues"):
			items := []map[string]any{{
				// Duplicates the configured repo's own PR #42: must not be
				// counted twice, and the configured repo's title must win.
				"number": 42, "title": "duplicate", "html_url": "https://example.test/dup",
				"repository_url": "https://api.github.com/repos/" + testRepo,
				"updated_at":     "2026-09-02T00:00:00Z",
			}}
			for i := range 24 {
				items = append(items, map[string]any{
					"number": 100 + i, "title": fmt.Sprintf("involved %d", i),
					"html_url":       fmt.Sprintf("https://example.test/other/%d", i),
					"repository_url": "https://api.github.com/repos/other/repo",
					"updated_at":     fmt.Sprintf("2026-09-01T00:%02d:00Z", i),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)
	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Repos []struct {
			Repo         string `json:"repo"`
			Mergeable    bool   `json:"mergeable"`
			PullRequests []struct {
				Number int    `json:"number"`
				Title  string `json:"title"`
			} `json:"pullRequests"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	total := 0
	foundOtherRepo := false
	for _, repo := range body.Repos {
		total += len(repo.PullRequests)
		require.Equal(t, repo.Repo == testRepo, repo.Mergeable, "only an allow-listed repository offers a merge: %s", repo.Repo)
		if repo.Repo == "other/repo" {
			foundOtherRepo = true
		}
		for _, pr := range repo.PullRequests {
			if repo.Repo == testRepo && pr.Number == 42 {
				require.Equal(t, "configured", pr.Title, "the configured repo's own PR must win over a search hit for the same repo#number")
			}
		}
	}
	require.Equal(t, summaryPRCapForTest, total, "the merged summary must be capped at 20 pull requests")
	require.True(t, foundOtherRepo, "a search hit outside the configured allow-list must still be merged in")
}

// summaryPRCapForTest mirrors the unexported summaryPRCap constant so this
// test breaks loudly, not silently, if that cap ever changes.
const summaryPRCapForTest = 20

// TestNoResponseEverCarriesTheToken is spec §6 row 5, on the HTTP surface.
//
// It drives the FAILURE paths on purpose. A token can only escape through a
// string this server builds about a request it made, and a 200 builds none —
// an earlier version of this test asked only for successful answers and passed
// even with the route deleted from Mount, since a 404 with an empty body
// contains no token either. Each case therefore asserts the error it provoked
// actually happened before asserting the token is absent from it.
func TestNoResponseEverCarriesTheToken(t *testing.T) {
	t.Run("an upstream error whose body is echoed back", func(t *testing.T) {
		h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
		})
		for _, c := range githubapp.Capabilities() {
			allowGlobally(t, grants, ctx, c.Name)
		}
		rec := do(t, h, http.MethodGet, "/api/github/search?q=x", "")

		require.Equal(t, http.StatusForbidden, rec.Code, "the upstream failure must reach the client, or this test guards nothing")
		require.Contains(t, rec.Body.String(), "Resource not accessible", "the upstream message must be echoed, or there is no string to leak into")
		require.NotContains(t, rec.Body.String(), "ghp_supersecret")
	})

	t.Run("a transport failure whose cause is concatenated into the message", func(t *testing.T) {
		h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
			// Hijack and drop the connection: the client sees a transport
			// error, not a status, which is the other message-building path.
			hijacker, ok := w.(http.Hijacker)
			require.True(t, ok)
			conn, _, err := hijacker.Hijack()
			require.NoError(t, err)
			_ = conn.Close()
		})
		for _, c := range githubapp.Capabilities() {
			allowGlobally(t, grants, ctx, c.Name)
		}
		rec := do(t, h, http.MethodGet, "/api/github/search?q=x", "")

		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		require.Contains(t, rec.Body.String(), "could not reach github", "the transport path must be the one taken")
		require.NotContains(t, rec.Body.String(), "ghp_supersecret")
	})

	t.Run("the success path", func(t *testing.T) {
		h, grants, _, ctx := newEnv(t)
		for _, c := range githubapp.Capabilities() {
			allowGlobally(t, grants, ctx, c.Name)
		}
		for _, target := range []string{"/api/github/summary", "/api/github/search?q=x"} {
			rec := do(t, h, http.MethodGet, target, "")
			require.Equal(t, http.StatusOK, rec.Code, "%s: %s", target, rec.Body.String())
			require.NotContains(t, rec.Body.String(), "ghp_supersecret", "%s leaked the token", target)
		}
	})
}

func TestUnconfiguredAnswers503NotAnEmptyList(t *testing.T) {
	h := githubapi.NewHandler(nil, memory.Gate{})
	r := chi.NewRouter()
	h.Mount(r)
	rec := do(t, r, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestUpstreamNotFoundMapsTo404 proves the first of the three status-honesty
// cases: a pull request GitHub does not have answers 404, not 502 or 500.
func TestUpstreamNotFoundMapsTo404(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityMerge)
	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"`+testRepo+`","number":42}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), "ghp_supersecret")
}

// TestUpstreamForbiddenReadsDifferentlyFromCapabilityDenial proves the second
// case: GitHub itself refusing the token (403) must be distinguishable from
// the gate refusing the caller (also 403) — a caller has to be able to tell
// "the dashboard refused you" from "GitHub refused the dashboard".
func TestUpstreamForbiddenReadsDifferentlyFromCapabilityDenial(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Resource not accessible by personal access token"})
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityMerge)
	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"`+testRepo+`","number":42}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.NotContains(t, rec.Body.String(), "capability denied")
	require.Contains(t, rec.Body.String(), "github refused")
	require.NotContains(t, rec.Body.String(), "ghp_supersecret")
}

// TestUpstreamServerErrorMapsToBadGateway proves the third StatusError case:
// any other status GitHub answers with is relayed as 502, not 500 — GitHub
// responded, it just did not respond usefully.
func TestUpstreamServerErrorMapsToBadGateway(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "server error"})
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityMerge)
	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"`+testRepo+`","number":42}`)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.NotContains(t, rec.Body.String(), "ghp_supersecret")
}

// TestTransportFailureMapsToServiceUnavailable proves a failure that never
// reaches GitHub at all (dial refused: the port is closed) produces no
// *githubapp.StatusError, and answers 503 rather than 500 — and still never
// leaks the token, since it is the same do() codepath TestNoResponseEverCarriesTheToken covers for the success case.
func TestTransportFailureMapsToServiceUnavailable(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	caps := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, githubapp.Register(ctx, resources, caps))

	// A loopback server opened then immediately closed: the URL is
	// well-formed but nothing listens on it, so the request fails to dial —
	// a genuine transport failure, distinct from every StatusError case above.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	client, err := githubapp.NewClient(githubapp.Config{
		Token: "ghp_supersecret", BaseURL: srv.URL,
		Repos: []string{testRepo}, AllowLoopback: true,
	})
	require.NoError(t, err)
	h := githubapi.NewHandler(client, memory.Gate{
		Capabilities: caps,
		Grants:       grants,
		GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
	})
	r := chi.NewRouter()
	h.Mount(r)
	allowGlobally(t, grants, ctx, githubapp.CapabilityMerge)

	rec := do(t, r, http.MethodPost, "/api/github/merge", `{"repo":"`+testRepo+`","number":42}`)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.NotContains(t, rec.Body.String(), "ghp_supersecret")
}

// A revoked or expired token is the most likely real failure of the whole
// integration, and it is a configuration problem. Relayed as 502 it would read
// as "GitHub is broken"; the frontend renders 502 as `failed` ("repair it") and
// 503 as `notAsked` ("configure it"), so the status is the whole message.
func TestUpstreamUnauthorizedReadsAsAConfigurationProblem(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})
	for _, c := range githubapp.Capabilities() {
		allowGlobally(t, grants, ctx, c.Name)
	}

	rec := do(t, h, http.MethodGet, "/api/github/search?q=x", "")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), "github.token", "the answer must name the setting to fix")
	require.NotContains(t, rec.Body.String(), "ghp_supersecret")
}

// TestSummaryNotTrackedForSearchOnlyPROutsideAllowList proves that a pull
// request the involves:@me search found in a repository outside the
// configured allow-list gets checks.state="not_tracked", not "none", while an
// allow-listed pull request still gets its real check state.
func TestSummaryNotTrackedForSearchOnlyPROutsideAllowList(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{summaryPullFixture(1, "allow-listed", "sha-1")})
		case strings.HasSuffix(r.URL.Path, "/search/issues"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{
				"number": 99, "title": "external", "html_url": "https://example.test/external/99",
				"repository_url": "https://api.github.com/repos/other/repo",
				"updated_at":     "2026-09-02T00:00:00Z",
			}}})
		case strings.Contains(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"check_runs":  []map[string]any{{"status": "completed", "conclusion": "success"}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)

	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Repos []struct {
			Repo         string        `json:"repo"`
			PullRequests []checksField `json:"pullRequests"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	byRepo := map[string][]checksField{}
	for _, repo := range body.Repos {
		byRepo[repo.Repo] = repo.PullRequests
	}

	require.Len(t, byRepo[testRepo], 1)
	require.Equal(t, "success", byRepo[testRepo][0].Checks.State, "allow-listed PR must have real checks")

	require.Len(t, byRepo["other/repo"], 1)
	require.Equal(t, "not_tracked", byRepo["other/repo"][0].Checks.State, "non-allow-listed PR must show not_tracked, not none")
}

// TestSummaryMatchesSearchHitsToTheAllowListCaseInsensitively proves a search
// hit whose repository_url differs from github.repos only in case lands in the
// configured repository's group, is deduped against its own listing, and is
// never marked not_tracked.
func TestSummaryMatchesSearchHitsToTheAllowListCaseInsensitively(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{summaryPullFixture(1, "listed", "sha-1")})
		case strings.HasSuffix(r.URL.Path, "/search/issues"):
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
				{
					"number": 1, "title": "listed", "html_url": "https://example.test/listed",
					"repository_url": "https://api.github.com/repos/LX-WNK/Kontor",
					"updated_at":     "2026-09-01T10:00:00Z",
				},
				{
					"number": 2, "title": "search-only", "html_url": "https://example.test/search-only",
					"repository_url": "https://api.github.com/repos/Lx-Wnk/KONTOR",
					"updated_at":     "2026-09-02T00:00:00Z",
				},
			}})
		case strings.Contains(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"check_runs":  []map[string]any{{"status": "completed", "conclusion": "success"}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	})
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)

	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Repos []struct {
			Repo         string        `json:"repo"`
			PullRequests []checksField `json:"pullRequests"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Repos, 1, rec.Body.String())
	require.Equal(t, testRepo, body.Repos[0].Repo)
	require.Len(t, body.Repos[0].PullRequests, 2, "PR #1 must not be duplicated by its search hit")
	for _, pr := range body.Repos[0].PullRequests {
		require.NotEqual(t, "not_tracked", pr.Checks.State, "PR #%d is in an allow-listed repository", pr.Number)
	}
}

func TestSummaryListsARepoConfiguredTwiceInDifferentCaseOnce(t *testing.T) {
	repos, err := githubapp.ParseRepos(testRepo + ", LX-WNK/Kontor")
	require.NoError(t, err)
	h, grants, ctx := newEnvWithRepos(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/pulls") {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}, repos)
	allowGlobally(t, grants, ctx, githubapp.CapabilityRead)

	rec := do(t, h, http.MethodGet, "/api/github/summary", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body struct {
		Repos []struct {
			Repo string `json:"repo"`
		} `json:"repos"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Repos, 1, rec.Body.String())
	require.Equal(t, testRepo, body.Repos[0].Repo)
}

// TestGateMatchesGrantPatternsOnTheConfiguredRepoSpelling proves a repository
// named in a different case is checked against grants in its configured
// spelling, so an exact-pattern grant or deny covers every case variant.
func TestGateMatchesGrantPatternsOnTheConfiguredRepoSpelling(t *testing.T) {
	h, grants, ctx := newEnvWithUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"merged": true, "sha": "deadbeef"})
	})
	_, err := grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: githubapp.CapabilityMerge,
		Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
		Pattern:        testRepo,
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	rec := do(t, h, http.MethodPost, "/api/github/merge", `{"repo":"LX-WNK/Kontor","number":42}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
