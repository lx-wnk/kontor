// Package github implements the HTTP surface over the GitHub Application:
// one route per capability, each gated by memory.Gate.
//
// Every capability the application declares must be reachable both here and
// as an MCP tool. That is not a preference: a seam wired on one surface only
// is a hole, and this project has shipped one twice. A surface-parity test in
// internal/mcp/tools (the companion task that adds the tools) asserts the
// pairing holds.
package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	githubapp "github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// Handler serves /api/github/*.
type Handler struct {
	client *githubapp.Client
	gate   memory.Gate
}

// NewHandler creates a Handler. client is nil when GitHub is unconfigured
// (see serverapp.buildGitHubClient); every route then answers 503 rather than
// the route not existing at all, mirroring api/obsidian.
func NewHandler(client *githubapp.Client, gate memory.Gate) *Handler {
	return &Handler{client: client, gate: gate}
}

// Mount registers the /api/github/* routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/github/summary", apierr.ErrorMiddleware(h.summary))
	r.Get("/api/github/search", apierr.ErrorMiddleware(h.search))
	r.Post("/api/github/comment", apierr.ErrorMiddleware(h.comment))
	r.Post("/api/github/merge", apierr.ErrorMiddleware(h.merge))
}

// githubScope is the context every Authorize call below runs against. A
// personal access token is one machine-wide credential — github.Register
// catalogues the application at repo.GlobalScope() — so there is no
// caller-supplied scope to parse, matching the Obsidian tools.
func githubScope() repo.Scope { return repo.GlobalScope() }

func (h *Handler) ready() error {
	if h.client == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "github is not configured")
	}
	return nil
}

// allow runs the two checks in the order decision D4 fixes: the repository
// allow-list FIRST, without a capability question, then the gate on the very
// same owner/name string the client will act on.
//
// Both capability.ErrDenied and capability.ErrAskRequired mean "forbidden" to
// this route's caller, so both map to 403 rather than the 500 ErrorMiddleware
// would give an unrecognised error. Their message ("capability denied: ..."
// or "capability requires approval but no asker is configured: ...") never
// reads like githubStatusError's 403 below — that distinction is what lets a
// caller tell "the dashboard refused you" from "GitHub refused the
// dashboard".
func (h *Handler) allow(r *http.Request, capName, repoName string) error {
	if repoName != "" {
		configured, ok := h.client.CanonicalRepo(repoName)
		if !ok {
			return apierr.NewAppError(http.StatusForbidden,
				fmt.Sprintf("%s is not in the configured github.repos allow-list", repoName))
		}
		// The gate matches grant patterns on the configured spelling, so a
		// case variant cannot slip past a deny grant.
		repoName = configured
	}
	if err := h.gate.Authorize(r.Context(), capName, repoName, githubScope()); err != nil {
		if errors.Is(err, capability.ErrDenied) || errors.Is(err, capability.ErrAskRequired) {
			return apierr.NewAppError(http.StatusForbidden, err.Error())
		}
		return err
	}
	return nil
}

// githubStatusError translates a failed client call into the exact HTTP
// status a caller needs to tell three different failures apart:
//
//   - a repository or pull request that does not exist (GitHub answered 404)
//   - a token that GitHub itself refused as lacking scope (GitHub answered
//     403) — worded so the message never contains "capability denied",
//     because that 403 must read differently from allow()'s: one is the
//     dashboard refusing the caller, the other is GitHub refusing the
//     dashboard
//   - anything else GitHub answered with, relayed as 502 (Bad Gateway):
//     GitHub responded, just not usefully
//
// errors.As is required rather than a message match because a transport
// failure — the request never got a response at all (DNS, dial, TLS,
// timeout) — produces no *githubapp.StatusError; that case falls through to
// 503, since the token itself never rides in that error (see
// githubapp.Client.do's own doc comment).
func githubStatusError(err error) error {
	// An allow-list refusal is this server's own decision, not GitHub's. Without
	// this it would fall past the errors.As below to the 503 line and read as
	// "could not reach github" — a refusal reported as an outage, which the
	// cockpit then draws as "not configured".
	if errors.Is(err, githubapp.ErrRepoNotAllowed) {
		return apierr.NewAppError(http.StatusForbidden, err.Error())
	}
	var statusErr *githubapp.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusNotFound:
			return apierr.NewAppError(http.StatusNotFound, statusErr.Error())
		case http.StatusForbidden:
			return apierr.NewAppError(http.StatusForbidden, "github refused the request: "+statusErr.Error())
		// A 401 is a token problem — revoked, expired, mistyped — which is the
		// most likely real failure of all. Relaying it as 502 would say "GitHub
		// is broken" about a setting the operator can fix, so it takes the same
		// 503 the unconfigured case takes: both mean configure it, not repair it.
		case http.StatusUnauthorized:
			return apierr.NewAppError(http.StatusServiceUnavailable, "github rejected the configured token (HTTP 401): check github.token")
		default:
			return apierr.NewAppError(http.StatusBadGateway, statusErr.Error())
		}
	}
	return apierr.NewAppError(http.StatusServiceUnavailable, "github: could not reach github: "+err.Error())
}

// pullRequestView is the camelCase JSON shape of one open pull request.
// Hand-written rather than encoding the client's struct: the wire format is
// this package's contract, and a field added to the client later must not
// silently become public.
type pullRequestView struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Author    string     `json:"author"`
	URL       string     `json:"url"`
	Draft     bool       `json:"draft"`
	UpdatedAt time.Time  `json:"updatedAt"`
	Checks    checksView `json:"checks"`
}

// checksView is the pull request's check-run state, collapsed to what the
// cockpit panel draws: a state plus the counts behind it. State is "none"
// both when GitHub reports no checks for the commit and when the check-run
// lookup itself failed — see summary(), which never lets that lookup turn a
// working 200 summary into an error or blank the other pull requests. State is
// "not_tracked" for a repository outside the allow-list, which is never looked up.
type checksView struct {
	State  string `json:"state"`
	Passed int    `json:"passed"`
	Failed int    `json:"failed"`
	Total  int    `json:"total"`
	URL    string `json:"url"`
}

// noChecksView is the checksView every allow-listed pull request starts with:
// "none", pointing at the checks tab a human would open to look for themselves.
func noChecksView(prURL string) checksView {
	return checksView{State: string(githubapp.CheckStateNone), URL: prURL + "/checks"}
}

// notTrackedChecksView is the checksView for a pull request whose repository
// is outside the configured allow-list: the check-run API was never called.
func notTrackedChecksView(prURL string) checksView {
	return checksView{State: string(githubapp.CheckStateNotTracked), URL: prURL + "/checks"}
}

// repoSummary carries one repository's open pull requests, or the reason that
// one repository could not be read. A per-repository Error, rather than one
// failed request for the whole panel: with three repositories configured, one
// rate-limited repository must not blank the other two.
type repoSummary struct {
	Repo         string            `json:"repo"`
	PullRequests []pullRequestView `json:"pullRequests"`
	Error        string            `json:"error,omitempty"`
	// False for a repository only the involves:@me search reached: merge refuses it.
	Mergeable bool `json:"mergeable"`
}

type summaryResponse struct {
	Repos []repoSummary `json:"repos"`
}

// summaryPRLimit is how many open pull requests each configured repository
// contributes on its own. summaryPRCap can still raise a repository above
// this once the involves:@me search adds more of that repository's pull
// requests to the merge — see summary().
const summaryPRLimit = 5

// summaryPRCap is the most pull requests the summary route returns in total,
// across every configured repository plus whatever the involves:@me search
// adds. A cockpit panel is a glance, not a list view.
const summaryPRCap = 20

// summaryCandidate is one pull request still competing for a place in the
// capped, merged summary — either returned by a configured repository's own
// pull list, or found by the involves:@me search. Checks is looked up only
// after the merge settles, so a PR the cap drops never pays for one.
type summaryCandidate struct {
	repo string
	pr   githubapp.PullRequest
}

// summary answers GET /api/github/summary: the cockpit panel's data in one
// request, per spec §4.2.
//
// It merges two sources: each configured repository's own open pull requests
// (summaryPRLimit each), and the involves:@me search, which reaches pull
// requests anywhere on GitHub the token can see — including ones in a
// configured repository beyond that repository's own limit. The merge is
// deduped by repo#number (a repository's own answer always wins over a
// search hit for the same pull request, since it carries the head SHA a
// checks lookup needs), sorted by updatedAt descending, and capped at
// summaryPRCap before any check-run lookup runs.
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) error {
	if err := h.ready(); err != nil {
		return err
	}
	repoNames := h.client.Repos()

	// One capability check for the whole summary, against "" — the request
	// names no single repository. A grant narrowed by pattern to one
	// repository therefore does NOT open the summary; that is deliberate, and
	// the same rule obsidian_search documents: "" is not the wildcard, an
	// empty or "*" grant pattern is.
	if err := h.allow(r, githubapp.CapabilityRead, ""); err != nil {
		return err
	}

	var merged []summaryCandidate
	seen := make(map[string]bool)
	repoErrors := make(map[string]string, len(repoNames))

	for _, name := range repoNames {
		prs, err := h.client.OpenPullRequests(r.Context(), name, summaryPRLimit)
		if err != nil {
			// A per-repository failure is embedded as text, not surfaced as
			// an HTTP status: the summary as a whole still answers 200 so the
			// other repositories are not blanked by one that failed.
			repoErrors[name] = err.Error()
			continue
		}
		for _, p := range prs {
			seen[name+"#"+strconv.Itoa(p.Number)] = true
			merged = append(merged, summaryCandidate{repo: name, pr: p})
		}
	}

	// A failed search does not fail the summary either — the same
	// partial-failure rule the repository loop above and the Checks lookup
	// below both follow. It just means no pull requests are added from it.
	if involved, err := h.client.InvolvedPullRequests(r.Context()); err == nil {
		for _, ip := range involved {
			key := ip.Repo + "#" + strconv.Itoa(ip.Number)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, summaryCandidate{repo: ip.Repo, pr: githubapp.PullRequest{
				Number: ip.Number, Title: ip.Title, URL: ip.URL, UpdatedAt: ip.UpdatedAt,
			}})
		}
	}

	sort.SliceStable(merged, func(i, j int) bool { return merged[i].pr.UpdatedAt.After(merged[j].pr.UpdatedAt) })
	if len(merged) > summaryPRCap {
		merged = merged[:summaryPRCap]
	}

	byRepo := make(map[string][]pullRequestView, len(repoNames))
	order := append([]string(nil), repoNames...)
	orderSeen := make(map[string]bool, len(repoNames))
	for _, name := range repoNames {
		orderSeen[name] = true
	}

	for _, m := range merged {
		// A repository outside the allow-list never gets a Checks call: the
		// client would refuse it (checkRepo), and the search hit carries no
		// head SHA anyway.
		var checks checksView
		if !h.client.AllowsRepo(m.repo) {
			checks = notTrackedChecksView(m.pr.URL)
		} else {
			// A failed check-run lookup falls back to noChecksView rather
			// than propagating the error: the PR this loop already has must
			// not be dropped by a lookup that merely enriches it.
			checks = noChecksView(m.pr.URL)
			if summary, err := h.client.Checks(r.Context(), m.repo, m.pr.HeadSHA); err == nil {
				checks = checksView{
					State: string(summary.State), Passed: summary.Passed,
					Failed: summary.Failed, Total: summary.Total, URL: m.pr.URL + "/checks",
				}
			}
		}
		byRepo[m.repo] = append(byRepo[m.repo], pullRequestView{
			Number: m.pr.Number, Title: m.pr.Title, Author: m.pr.Author,
			URL: m.pr.URL, Draft: m.pr.Draft, UpdatedAt: m.pr.UpdatedAt,
			Checks: checks,
		})
		if !orderSeen[m.repo] {
			orderSeen[m.repo] = true
			order = append(order, m.repo)
		}
	}

	out := summaryResponse{Repos: make([]repoSummary, 0, len(order))}
	for _, name := range order {
		views := byRepo[name]
		if views == nil {
			views = []pullRequestView{}
		}
		out.Repos = append(out.Repos, repoSummary{Repo: name, PullRequests: views, Error: repoErrors[name], Mergeable: h.client.AllowsRepo(name)})
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type searchHitView struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// search answers GET /api/github/search?q=.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) error {
	if err := h.ready(); err != nil {
		return err
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		return apierr.NewAppError(http.StatusBadRequest, "q is required")
	}
	if err := h.allow(r, githubapp.CapabilitySearch, ""); err != nil {
		return err
	}
	bounded, err := h.client.BoundQuery(query)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	hits, err := h.client.SearchIssues(r.Context(), bounded)
	if err != nil {
		return githubStatusError(err)
	}
	out := make([]searchHitView, 0, len(hits))
	for _, hit := range hits {
		out = append(out, searchHitView{Repo: hit.Repo, Number: hit.Number, Title: hit.Title, URL: hit.URL})
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type repoActionRequest struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Body   string `json:"body"`
	Method string `json:"method"`
}

func decodeAction(r *http.Request) (repoActionRequest, error) {
	var req repoActionRequest
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&req); err != nil {
		return req, apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	req.Repo = strings.TrimSpace(req.Repo)
	if req.Repo == "" {
		return req, apierr.NewAppError(http.StatusBadRequest, "repo is required")
	}
	if req.Number <= 0 {
		return req, apierr.NewAppError(http.StatusBadRequest, "number must be a positive issue or pull-request number")
	}
	return req, nil
}

// comment answers POST /api/github/comment.
func (h *Handler) comment(w http.ResponseWriter, r *http.Request) error {
	if err := h.ready(); err != nil {
		return err
	}
	req, err := decodeAction(r)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.Body) == "" {
		return apierr.NewAppError(http.StatusBadRequest, "body is required")
	}
	if err := h.allow(r, githubapp.CapabilityComment, req.Repo); err != nil {
		return err
	}
	url, err := h.client.Comment(r.Context(), req.Repo, req.Number, req.Body)
	if err != nil {
		return githubStatusError(err)
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]string{"url": url})
	return nil
}

// merge answers POST /api/github/merge.
//
// Registered exactly like the other three. Its capability class does the
// work: github.merge is class "spend", so with no grant capability.Decide
// returns deny — not ask — and no human is ever prompted into a merge.
func (h *Handler) merge(w http.ResponseWriter, r *http.Request) error {
	if err := h.ready(); err != nil {
		return err
	}
	req, err := decodeAction(r)
	if err != nil {
		return err
	}
	if err := h.allow(r, githubapp.CapabilityMerge, req.Repo); err != nil {
		return err
	}
	sha, err := h.client.MergePullRequest(r.Context(), req.Repo, req.Number, req.Method)
	if err != nil {
		return githubStatusError(err)
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]string{"sha": sha})
	return nil
}
