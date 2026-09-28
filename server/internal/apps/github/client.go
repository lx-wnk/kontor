package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/validation"
)

// ErrRepoNotAllowed is returned for any repository outside github.repos. It is
// a sentinel so the HTTP handler and the MCP tools can answer 403/refuse
// without string-matching, and so a test can prove the refusal happened before
// any request was made.
var ErrRepoNotAllowed = errors.New("github: repository is not in the configured allow-list")

// StatusError is a non-2xx response from the GitHub API. It carries the code
// so a caller can distinguish absence from refusal — an HTTP route and an MCP
// tool have to answer those two differently, and a caller that has to parse a
// message to tell them apart will get it wrong.
//
// Message is GitHub's own response body text (or, absent one, resp.Status).
// It can never carry the request's token: it originates from the server's
// response, not from anything this client sent, and is size-limited before
// decoding (see do()).
type StatusError struct {
	StatusCode int
	Method     string
	Path       string
	Message    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("github: %s %s: unexpected status %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// ParseRepos splits a comma-separated github.repos setting into owner/name
// pairs, refusing anything that is not exactly one owner and one name. An
// empty string parses to an empty list, which is how the application is
// switched off.
func ParseRepos(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(trimmed, ",") {
		entry := strings.TrimSpace(part)
		if entry == "" {
			continue
		}
		owner, name, ok := strings.Cut(entry, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("github.repos: %q is not an owner/name pair", entry)
		}
		if !validSegment(owner) || !validSegment(name) {
			return nil, fmt.Errorf("github.repos: %q contains characters that are not valid in a repository path", entry)
		}
		out = append(out, entry)
	}
	return out, nil
}

// validSegment is the shape of one owner or name segment of a github.repos
// entry. Deliberately stricter than GitHub's own rules: it exists to make the
// allow-list unambiguous, so a value that could be read two ways is refused
// rather than normalised.
//
// "." and ".." are refused outright even though '.' is otherwise an allowed
// character (real repository names such as "my.repo" contain one) — a
// segment that is ENTIRELY dots is never a legitimate owner or name, and
// allowing it would accept a path-traversal-shaped entry such as "owner/.."
// unchanged instead of refusing it (D4: the allow-list must be unambiguous,
// not normalised).
func validSegment(s string) bool {
	if s == "" || strings.Trim(s, ".") == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Config configures a GitHub Client. Token and Repos are required; BaseURL
// defaults to the public API when empty.
type Config struct {
	Token   string
	BaseURL string
	Repos   []string
	// AllowLoopback disables validation.SafeDialContext for this client. It
	// exists for httptest servers, which listen on loopback — the address the
	// guard refuses by design. Production wiring (serverapp.buildGitHubClient)
	// never sets it.
	AllowLoopback bool
}

// Client talks to one GitHub API host on behalf of the configured token.
//
// It enforces the repository allow-list and nothing else. Capability checks
// belong to the callers (the HTTP handler and the MCP tools), which run them
// BEFORE calling in: a caller reaching this client directly bypasses the
// capability gate, exactly as apps/obsidian's Client does, and that is a
// stated property rather than an oversight.
type Client struct {
	http    *http.Client
	baseURL *url.URL
	token   string
	repos   map[string]bool
	order   []string
}

// NewClient validates cfg and builds a Client.
//
// The transport dials through validation.SafeDialContext, which re-resolves
// the host at connection time and refuses loopback, private, link-local and
// CGNAT addresses — the DNS-rebinding guard api/remotes already uses. The
// consequence is that a GitHub Enterprise host on a LAN address (10.x,
// 192.168.x) cannot be reached, and NewClient will not tell you why until the
// first request fails to dial. This is not widened here — loosening the
// shared SSRF guard for one application is exactly the trade the Obsidian
// client refused, and it solved the mirror-image problem with a narrow
// per-client dial policy instead. The follow-up, if GHE-on-LAN is ever
// wanted, is that narrow policy, not a change to validation.IsBlockedIP.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, errors.New("github: Token is required")
	}
	raw := cfg.BaseURL
	if raw == "" {
		raw = "https://api.github.com"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("github: BaseURL %q is not an absolute URL", cfg.BaseURL)
	}
	if len(cfg.Repos) == 0 {
		return nil, errors.New("github: at least one repository is required")
	}

	transport := &http.Transport{}
	if cfg.AllowLoopback {
		transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	} else {
		transport.DialContext = validation.SafeDialContext
	}

	c := &Client{
		http:    &http.Client{Timeout: 20 * time.Second, Transport: transport},
		baseURL: u,
		token:   cfg.Token,
		repos:   make(map[string]bool, len(cfg.Repos)),
		order:   append([]string(nil), cfg.Repos...),
	}
	for _, r := range cfg.Repos {
		c.repos[r] = true
	}
	return c, nil
}

// Repos returns the configured allow-list in its configured order. A copy, so
// a caller cannot reorder the client's own list.
func (c *Client) Repos() []string {
	return append([]string(nil), c.order...)
}

// AllowsRepo reports whether name is in the allow-list. Callers use this to
// refuse a repository BEFORE consulting the capability gate (spec D4): a
// repository outside the list is refused without a capability question ever
// being asked, and the same owner/name string then goes to both the gate and
// the client.
func (c *Client) AllowsRepo(name string) bool { return c.repos[name] }

func (c *Client) checkRepo(name string) error {
	if !c.repos[name] {
		return fmt.Errorf("%w: %s", ErrRepoNotAllowed, name)
	}
	return nil
}

// do issues one authenticated request and decodes a JSON answer into out.
//
// The error it builds carries the status and the API's own message, never the
// request headers: a 401 is the error a user is most likely to paste
// somewhere public, and the Authorization header is on that request.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	u := *c.baseURL
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github: encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// err from http.Client wraps *url.Error, whose Error() prints the URL
		// but never a header, so the token cannot ride along here.
		return fmt.Errorf("github: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8<<10)).Decode(&apiErr)
		if apiErr.Message == "" {
			apiErr.Message = resp.Status
		}
		return &StatusError{StatusCode: resp.StatusCode, Method: method, Path: path, Message: apiErr.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("github: decode %s %s: %w", method, path, err)
	}
	return nil
}

// PullRequest is one open pull request as the cockpit panel shows it.
type PullRequest struct {
	Number    int
	Title     string
	Author    string
	URL       string
	Draft     bool
	UpdatedAt time.Time
	// HeadSHA is the pull request's head commit. It is not part of the
	// panel's own wire shape — it exists only so a caller can look up the
	// commit's check-run state with Checks.
	HeadSHA string
}

// OpenPullRequests lists the most recently updated open pull requests in
// repoName, newest first.
//
// One call per repository, rather than a single /search/issues query across
// all of them: search has its own much lower rate limit and its own eventual
// consistency, and the summary is the panel a user refreshes most.
func (c *Client) OpenPullRequests(ctx context.Context, repoName string, limit int) ([]PullRequest, error) {
	if err := c.checkRepo(repoName); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	var raw []struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		HTMLURL   string    `json:"html_url"`
		Draft     bool      `json:"draft"`
		UpdatedAt time.Time `json:"updated_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	q := url.Values{
		"state":     {"open"},
		"sort":      {"updated"},
		"direction": {"desc"},
		"per_page":  {strconv.Itoa(limit)},
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+repoName+"/pulls", q, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]PullRequest, 0, len(raw))
	for _, r := range raw {
		out = append(out, PullRequest{
			Number: r.Number, Title: r.Title, Author: r.User.Login,
			URL: r.HTMLURL, Draft: r.Draft, UpdatedAt: r.UpdatedAt,
			HeadSHA: r.Head.SHA,
		})
	}
	return out, nil
}

// InvolvedPullRequest is one open pull request the authenticated user is
// involved in, found by search rather than one repository's own pull list. It
// carries its own owner/repo because — unlike OpenPullRequests — the search
// spans every repository GitHub will show this token, not only the
// configured allow-list.
type InvolvedPullRequest struct {
	Repo      string
	Number    int
	Title     string
	URL       string
	UpdatedAt time.Time
}

// InvolvedPullRequests lists open pull requests the authenticated user is
// involved in anywhere on GitHub, newest first. It is the counterpart to
// OpenPullRequests: that method answers "what's open in the repositories
// this operator configured", this one answers "what's mine to look at", and
// the caller merges the two. Like SearchIssues, the allow-list plays no role
// in the request itself — a caller that wants the answer bounded to the
// configured repositories must filter it after the fact.
func (c *Client) InvolvedPullRequests(ctx context.Context) ([]InvolvedPullRequest, error) {
	var raw struct {
		Items []struct {
			Number        int       `json:"number"`
			Title         string    `json:"title"`
			HTMLURL       string    `json:"html_url"`
			RepositoryURL string    `json:"repository_url"`
			UpdatedAt     time.Time `json:"updated_at"`
		} `json:"items"`
	}
	q := url.Values{
		"q":        {"is:pr is:open involves:@me"},
		"sort":     {"updated"},
		"order":    {"desc"},
		"per_page": {"20"},
	}
	if err := c.do(ctx, http.MethodGet, "/search/issues", q, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]InvolvedPullRequest, 0, len(raw.Items))
	for _, item := range raw.Items {
		repo := repoFromAPIURL(item.RepositoryURL)
		if repo == "" {
			continue
		}
		out = append(out, InvolvedPullRequest{
			Repo: repo, Number: item.Number, Title: item.Title,
			URL: item.HTMLURL, UpdatedAt: item.UpdatedAt,
		})
	}
	return out, nil
}

// CheckState is the coarse state of a commit's check runs, collapsed from
// GitHub's per-run status/conclusion pairs into the four states the cockpit
// panel draws.
type CheckState string

const (
	CheckStateSuccess CheckState = "success"
	CheckStateFailure CheckState = "failure"
	CheckStatePending CheckState = "pending"
	// CheckStateNone means either GitHub reports no check runs for the
	// commit, or the lookup itself failed — see Checks and handler.summary,
	// which never lets a check-run failure blank an otherwise-working PR.
	CheckStateNone CheckState = "none"
)

// CheckSummary is the aggregate check-run state of one commit.
type CheckSummary struct {
	State  CheckState
	Passed int
	Failed int
	Total  int
}

// Checks summarises the check runs GitHub has recorded against sha (normally
// a pull request's head commit). Total counts every run GitHub reports,
// including ones still queued or in progress; Passed and Failed count only
// completed runs, so Passed+Failed can be less than Total while checks are
// still running.
func (c *Client) Checks(ctx context.Context, repoName, sha string) (CheckSummary, error) {
	if err := c.checkRepo(repoName); err != nil {
		return CheckSummary{}, err
	}
	var raw struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	path := "/repos/" + repoName + "/commits/" + sha + "/check-runs"
	if err := c.do(ctx, http.MethodGet, path, url.Values{"per_page": {"100"}}, nil, &raw); err != nil {
		return CheckSummary{}, err
	}
	if raw.TotalCount == 0 {
		return CheckSummary{State: CheckStateNone}, nil
	}
	summary := CheckSummary{Total: raw.TotalCount}
	pending := false
	for _, run := range raw.CheckRuns {
		if run.Status != "completed" {
			pending = true
			continue
		}
		switch run.Conclusion {
		case "success":
			summary.Passed++
		case "neutral", "skipped":
			// Neither a pass nor a failure: excluded from both counts but
			// still part of Total.
		default:
			summary.Failed++
		}
	}
	switch {
	case summary.Failed > 0:
		summary.State = CheckStateFailure
	case pending:
		summary.State = CheckStatePending
	default:
		summary.State = CheckStateSuccess
	}
	return summary, nil
}

// SearchHit is one issue or pull request matched by a search.
type SearchHit struct {
	Repo   string
	Number int
	Title  string
	URL    string
}

// SearchIssues runs one GitHub issue search.
//
// Unlike every other call here it names no single repository, so there is no
// allow-list check to make against a target — the query itself decides what it
// reaches. The callers narrow it instead: the HTTP route and the MCP tool both
// append a repo: qualifier per configured repository before calling in, so a
// search can never report a repository the operator did not list.
func (c *Client) SearchIssues(ctx context.Context, query string) ([]SearchHit, error) {
	var raw struct {
		Items []struct {
			Number        int    `json:"number"`
			Title         string `json:"title"`
			HTMLURL       string `json:"html_url"`
			RepositoryURL string `json:"repository_url"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/search/issues", url.Values{"q": {query}, "per_page": {"20"}}, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]SearchHit, 0, len(raw.Items))
	for _, item := range raw.Items {
		repo := repoFromAPIURL(item.RepositoryURL)
		// The allow-list is enforced on the ANSWER, not only on the question.
		// Bounding the query alone means trusting a denylist of GitHub's scope
		// qualifiers to be complete, and it was not: `owner:` unions exactly
		// like `repo:` and was missed, so a caller reached repositories the
		// operator never listed. Every qualifier GitHub adds next would reopen
		// that. A hit whose repository is not in the allow-list is dropped here
		// no matter how it got into the result set.
		if !c.repos[repo] {
			continue
		}
		out = append(out, SearchHit{
			Repo:   repo,
			Number: item.Number,
			Title:  item.Title,
			URL:    item.HTMLURL,
		})
	}
	return out, nil
}

// repoFromAPIURL turns "https://api.github.com/repos/owner/name" into
// "owner/name". The search API reports the owning repository only as this
// URL, and owner/name is the string every other surface here speaks.
func repoFromAPIURL(raw string) string {
	_, after, ok := strings.Cut(raw, "/repos/")
	if !ok {
		return ""
	}
	return strings.Trim(after, "/")
}

// Comment posts one comment on an issue or pull request and returns its URL.
// GitHub's issue-comment endpoint serves pull requests too — a pull request is
// an issue with a branch — so there is one method here, not two.
func (c *Client) Comment(ctx context.Context, repoName string, number int, body string) (string, error) {
	if err := c.checkRepo(repoName); err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("github: comment body must not be empty")
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	path := "/repos/" + repoName + "/issues/" + strconv.Itoa(number) + "/comments"
	if err := c.do(ctx, http.MethodPost, path, nil, map[string]string{"body": body}, &out); err != nil {
		return "", err
	}
	return out.HTMLURL, nil
}

// MergeMethods are the merge strategies GitHub accepts.
var MergeMethods = []string{"merge", "squash", "rebase"}

// MergePullRequest merges a pull request and returns the resulting commit SHA.
func (c *Client) MergePullRequest(ctx context.Context, repoName string, number int, method string) (string, error) {
	if err := c.checkRepo(repoName); err != nil {
		return "", err
	}
	if method == "" {
		method = "squash"
	}
	valid := false
	for _, m := range MergeMethods {
		if m == method {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("github: unknown merge method %q (valid: %s)", method, strings.Join(MergeMethods, ", "))
	}
	var out struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"sha"`
	}
	path := "/repos/" + repoName + "/pulls/" + strconv.Itoa(number) + "/merge"
	if err := c.do(ctx, http.MethodPut, path, nil, map[string]string{"merge_method": method}, &out); err != nil {
		return "", err
	}
	if !out.Merged {
		return "", fmt.Errorf("github: %s#%d was not merged", repoName, number)
	}
	return out.SHA, nil
}

// ErrQueryWidensScope is returned for a search query that carries its own
// scope qualifier. Callers map it to a 4xx: it is the caller's input that is
// wrong, not the operator's configuration.
var ErrQueryWidensScope = errors.New("github: the search query may not carry its own repo:, org: or user: qualifier")

// scopeQualifiers are the search qualifiers that name where to look. GitHub
// unions repeated occurrences of these, so one supplied by the caller is a
// second branch of the same OR, not a narrowing of the first.
var scopeQualifiers = []string{"repo:", "org:", "user:", "owner:"}

// BoundQuery bounds a search to the allow-list by appending one repo:
// qualifier per configured repository.
//
// It refuses a query that already carries a scope qualifier, and that refusal
// is the security-relevant half. GitHub ORs repeated repo:/org:/user:
// qualifiers, so a caller-supplied `repo:other/private` does not get
// intersected by the appended ones — it joins them, and the search reaches a
// repository the operator never listed. Appending cannot bound a query that is
// free to widen itself, so the widening is rejected instead.
//
// Verified against the live API rather than assumed: `test repo:golang/go`
// returns 31445 hits, `test repo:nodejs/node` 46437, and the two qualifiers
// together 77882 — the exact sum, which is a union.
func (c *Client) BoundQuery(query string) (string, error) {
	for _, field := range strings.Fields(query) {
		bare := strings.ToLower(strings.TrimPrefix(field, "-"))
		for _, qualifier := range scopeQualifiers {
			if !strings.HasPrefix(bare, qualifier) {
				continue
			}
			// Narrowing to a repository the operator already listed is the
			// commonest refinement of a multi-repo search and widens nothing,
			// so it passes; anything else names a scope outside the list.
			if qualifier == "repo:" && c.repos[strings.TrimPrefix(bare, "repo:")] {
				continue
			}
			return "", fmt.Errorf("%w (found %q)", ErrQueryWidensScope, field)
		}
	}
	var b strings.Builder
	b.WriteString(query)
	for _, name := range c.order {
		b.WriteString(" repo:")
		b.WriteString(name)
	}
	return b.String(), nil
}
