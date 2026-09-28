package obsidian

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// TLS modes for Config.TLSMode. Obsidian's Local REST API serves HTTPS with
// a self-signed certificate; the common workaround is disabling verification
// entirely (curl -k). These three modes make that a decision instead of a
// silent default.
const (
	// TLSVerify performs normal certificate verification against the system
	// trust store. Works only if the user has installed and trusted the
	// vault's certificate.
	TLSVerify = "verify"
	// TLSPinned trusts the certificate presented on the first connection (no
	// install step required) and refuses any later connection whose leaf
	// certificate has a different SHA-256 fingerprint. A changed fingerprint
	// is usually a harmless reinstall on loopback, but the user confirms
	// that, the client does not assume it. Not a default: NewClient requires
	// TLSMode to be set explicitly, empty included, so nothing defaults to
	// this or any other mode.
	TLSPinned = "pinned"
	// TLSInsecureLoopback disables certificate verification outright.
	// NewClient refuses this mode unless the configured host resolves to
	// loopback — the one case where a network attacker cannot be the party
	// presenting the certificate.
	TLSInsecureLoopback = "insecure-loopback"
)

// Config configures a vault Client. Every field is required; NewClient fails
// closed on anything it cannot parse or validate — an empty configuration,
// an unparseable host, or an unrecognised TLSMode all refuse construction
// rather than falling back to a guessed default.
type Config struct {
	BaseURL   string
	APIKey    string
	VaultRoot string
	TLSMode   string
}

// Client talks to one Obsidian vault's Local REST API.
//
// It carries its own dial policy rather than relying on the server-wide SSRF
// guard (validation.IsBlockedIP): that guard blocks loopback on purpose, and
// Obsidian's API lives on loopback, so widening the guard to accommodate it
// would weaken every other outbound call in the system. Instead this client
// resolves its one configured host exactly once at construction and its
// dialer refuses to connect anywhere else — a narrow, named, inspectable
// exception rather than a widened default.
type Client struct {
	http      *http.Client
	baseURL   *url.URL
	apiKey    string
	vaultRoot string
	tlsMode   string

	mu          sync.Mutex
	fingerprint string // TLSPinned only: SHA-256 hex of the trusted leaf cert, set on first connect.
}

// NewClient validates cfg and builds a Client whose transport is restricted
// to the single resolved host.
func NewClient(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("obsidian: BaseURL is required")
	}
	if cfg.VaultRoot == "" {
		return nil, errors.New("obsidian: VaultRoot is required")
	}
	switch cfg.TLSMode {
	case TLSVerify, TLSPinned, TLSInsecureLoopback:
	default:
		return nil, fmt.Errorf("obsidian: unknown TLSMode %q", cfg.TLSMode)
	}

	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("obsidian: parse BaseURL: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("obsidian: BaseURL scheme must be https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("obsidian: BaseURL has no host")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}

	pinnedIP, err := resolveOnce(host)
	if err != nil {
		return nil, fmt.Errorf("obsidian: resolve host %q: %w", host, err)
	}

	if cfg.TLSMode == TLSInsecureLoopback && !pinnedIP.IsLoopback() {
		return nil, fmt.Errorf("obsidian: insecure-loopback refused for non-loopback host %q", host)
	}

	c := &Client{
		baseURL:   u,
		apiKey:    cfg.APIKey,
		vaultRoot: cfg.VaultRoot,
		tlsMode:   cfg.TLSMode,
	}

	transport := &http.Transport{DialContext: dialPolicy(host, pinnedIP, port)}
	if cfg.TLSMode != TLSVerify {
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // TLSPinned enforces trust itself via VerifyConnection; TLSInsecureLoopback is restricted to loopback above.
			VerifyConnection:   c.verifyConnection,
		}
	}
	c.http = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	return c, nil
}

// PinnedFingerprint returns the SHA-256 hex fingerprint pinned on first
// connect under TLSPinned, or "" before any connection or under a different
// TLSMode. Built ahead of its consumer, the same treatment Reversible
// (apps/obsidian/app.go) and ResolveEffort (services/effort_resolver.go)
// get: nothing outside client_test.go calls it. A UI asking the user to
// confirm a vault's certificate would read this value; no such UI exists,
// and none is scheduled to build it.
func (c *Client) PinnedFingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fingerprint
}

// verifyConnection implements trust-on-first-use for TLSPinned: the first
// certificate seen is trusted and its fingerprint stored; any later
// connection presenting a different fingerprint is refused. Under any other
// TLSMode it is a no-op — TLSVerify never installs this callback, and
// TLSInsecureLoopback intentionally skips verification.
func (c *Client) verifyConnection(cs tls.ConnectionState) error {
	if c.tlsMode != TLSPinned {
		return nil
	}
	if len(cs.PeerCertificates) == 0 {
		return errors.New("obsidian: no peer certificate presented")
	}
	sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
	fp := hex.EncodeToString(sum[:])

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fingerprint == "" {
		c.fingerprint = fp
		return nil
	}
	if fp != c.fingerprint {
		return errors.New("obsidian: certificate fingerprint changed since it was pinned, refusing connection")
	}
	return nil
}

// resolveOnce resolves host to a single IP. Called exactly once, at
// construction — see dialPolicy for why a second resolution never happens.
func resolveOnce(host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses found for %q", host)
	}
	return ips[0], nil
}

// dialPolicy returns a DialContext that connects only to pinnedIP:port. DNS
// is resolved exactly once, by resolveOnce at construction; this function
// never re-resolves host, so a DNS answer that changes for it between
// resolve and connect (rebinding) cannot matter — it is never consulted
// again. Any address whose host is neither the originally configured
// hostname nor the pinned IP, or whose port differs, is refused outright.
func dialPolicy(host string, pinnedIP net.IP, port string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	pinnedAddr := net.JoinHostPort(pinnedIP.String(), port)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		reqHost, reqPort, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("obsidian: dial policy: %w", err)
		}
		if reqHost != host && reqHost != pinnedIP.String() {
			return nil, fmt.Errorf("obsidian: dial policy refuses host %q, only %q is permitted", reqHost, host)
		}
		if reqPort != port {
			return nil, fmt.Errorf("obsidian: dial policy refuses port %q, only %q is permitted", reqPort, port)
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, pinnedAddr)
	}
}

// resolveVaultPath joins root and notePath and refuses the result if it
// escapes root. The root is a boundary, not a suggestion: this runs before
// any request is built, so a traversal never reaches the network.
func resolveVaultPath(root, notePath string) (string, error) {
	if notePath == "" {
		return "", errors.New("obsidian: note path must not be empty")
	}
	if root == "" {
		return "", errors.New("obsidian: vault root must not be empty")
	}
	rootClean := path.Clean("/" + root)
	joined := path.Clean("/" + path.Join(root, notePath))
	if joined != rootClean && !strings.HasPrefix(joined, rootClean+"/") {
		return "", fmt.Errorf("obsidian: note path %q escapes vault root %q", notePath, root)
	}
	return strings.TrimPrefix(joined, "/"), nil
}

// NormalizeNotePath resolves notePath against VaultRoot exactly as
// newRequest does before building a vault HTTP request, and returns the
// canonical vault-relative form: any ".." segment collapsed, with the
// VaultRoot prefix stripped back off. It fails on the same conditions
// resolveVaultPath fails on — an empty notePath, an empty VaultRoot, or a
// path that escapes VaultRoot — plus the degenerate case where notePath
// resolves to VaultRoot itself (no note to name).
//
// A caller that must check a note path against a capability grant before
// reaching the vault (the MCP tools in server/internal/mcp/tools) MUST call
// this first and use its result for both the grant check and the
// Read/Write/Delete call. Passing the raw, un-normalized path to a
// capability check while this client independently normalizes its own copy
// lets a "notes/../secrets/x.md" pass a check written against a "notes/*"
// grant pattern (a plain strings.HasPrefix — see capability.Match) and then
// resolve to "secrets/x.md" once it reaches here: the grant and the
// request would be judging two different targets.
func (c *Client) NormalizeNotePath(notePath string) (string, error) {
	resolved, err := resolveVaultPath(c.vaultRoot, notePath)
	if err != nil {
		return "", err
	}
	rootClean := strings.TrimPrefix(path.Clean("/"+c.vaultRoot), "/")
	rel := strings.TrimPrefix(resolved, rootClean+"/")
	if rel == resolved {
		return "", fmt.Errorf("obsidian: note path %q resolves to the vault root itself", notePath)
	}
	return rel, nil
}

// newRequest builds a request against /vault/{resolved path}, containing
// notePath within c.vaultRoot first. The API key travels only in the
// Authorization header, never in the URL, so it never ends up in a
// transport-level error (those report the URL, not headers).
func (c *Client) newRequest(ctx context.Context, method, notePath string, body io.Reader) (*http.Request, error) {
	resolved, err := resolveVaultPath(c.vaultRoot, notePath)
	if err != nil {
		return nil, err
	}
	u := *c.baseURL
	u.Path = "/vault/" + resolved

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("obsidian: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	return req, nil
}

// ErrNotFound indicates the note does not exist at the requested path — a
// 404 from the vault. Read's other error returns (a network failure, a 500,
// a decode error) are transient vault conditions instead: distinguishing the
// two is what lets a caller expire a pointer only on an actual deletion, not
// on a blip.
var ErrNotFound = errors.New("obsidian: note not found")

// Read returns the raw content of the note at notePath.
func (c *Client) Read(ctx context.Context, notePath string) (string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, notePath, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("obsidian: read %s: %w", notePath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("obsidian: read %s: %w", notePath, ErrNotFound)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("obsidian: read %s: %w", notePath, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("obsidian: read %s: unexpected status %d", notePath, resp.StatusCode)
	}
	return string(body), nil
}

// Write creates or overwrites the note at notePath with content.
func (c *Client) Write(ctx context.Context, notePath, content string) error {
	req, err := c.newRequest(ctx, http.MethodPut, notePath, strings.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/markdown")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("obsidian: write %s: %w", notePath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("obsidian: write %s: unexpected status %d", notePath, resp.StatusCode)
	}
	return nil
}

// Delete removes the note at notePath.
func (c *Client) Delete(ctx context.Context, notePath string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, notePath, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("obsidian: delete %s: %w", notePath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("obsidian: delete %s: unexpected status %d", notePath, resp.StatusCode)
	}
	return nil
}

// SearchResult is one match from Search.
type SearchResult struct {
	Path  string
	Score float64
}

type searchResponseItem struct {
	Filename string  `json:"filename"`
	Score    float64 `json:"score"`
}

// Search runs the Local REST API's simple search across the whole vault.
//
// Unlike Read/Write/Delete, results are not confined to VaultRoot — the
// upstream endpoint searches the entire vault by design, and this method
// reports what it returns verbatim. Both callers that surface results
// outside this package — IndexNotes (index.go) and the obsidian_search MCP
// tool (internal/mcp/tools/obsidian.go) — go through SearchUnderRoot
// instead, and any new one must too: a bare path outside VaultRoot is
// already a disclosure (the note exists, and it is called this), even
// though a follow-up Read of it would be refused by resolveVaultPath.
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	u := *c.baseURL
	u.Path = "/search/simple/"
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("obsidian: build search request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("obsidian: search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("obsidian: search: unexpected status %d", resp.StatusCode)
	}

	var items []searchResponseItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("obsidian: search: decode response: %w", err)
	}
	results := make([]SearchResult, len(items))
	for i, it := range items {
		results[i] = SearchResult{Path: it.Filename, Score: it.Score}
	}
	return results, nil
}

// SearchUnderRoot runs Search and keeps only the results that fall under
// VaultRoot, with each Path rewritten to the root-relative form
// Read/Write/Delete accept. This is the confinement Search itself does not
// apply, and it is the method every caller outside this package must use:
// the configured root is the only boundary this application models, so a
// result outside it is neither readable nor namable.
//
// The rewrite is not cosmetic — a caller that hands a raw Search path back
// to an agent hands it a path Read would then refuse, because Read resolves
// its argument inside VaultRoot.
func (c *Client) SearchUnderRoot(ctx context.Context, query string) ([]SearchResult, error) {
	all, err := c.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	confined := make([]SearchResult, 0, len(all))
	for _, r := range all {
		rel, ok := pathUnderRoot(c.vaultRoot, r.Path)
		if !ok {
			continue
		}
		confined = append(confined, SearchResult{Path: rel, Score: r.Score})
	}
	return confined, nil
}

// JSONLogicHit is one note a JsonLogic search matched, with the query's value for it.
type JSONLogicHit struct {
	Filename string          `json:"filename"`
	Result   json.RawMessage `json:"result"`
}

// SearchJSONLogic runs a JsonLogic query against every note; the REST API returns only non-falsy results.
func (c *Client) SearchJSONLogic(ctx context.Context, logic string) ([]JSONLogicHit, error) {
	u := *c.baseURL
	u.Path = "/search/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(logic))
	if err != nil {
		return nil, fmt.Errorf("obsidian: build jsonlogic search: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/vnd.olrapi.jsonlogic+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("obsidian: jsonlogic search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("obsidian: jsonlogic search: unexpected status %d", resp.StatusCode)
	}
	var hits []JSONLogicHit
	if err := json.NewDecoder(resp.Body).Decode(&hits); err != nil {
		return nil, fmt.Errorf("obsidian: jsonlogic search: decode response: %w", err)
	}
	return hits, nil
}

// OpenNote asks Obsidian to show the note; the REST API creates a missing note, so callers pass only paths they know exist.
func (c *Client) OpenNote(ctx context.Context, notePath string) error {
	resolved, err := resolveVaultPath(c.vaultRoot, notePath)
	if err != nil {
		return err
	}
	u := *c.baseURL
	u.Path = "/open/" + resolved
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), nil)
	if err != nil {
		return fmt.Errorf("obsidian: build open request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("obsidian: open: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("obsidian: open: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// pathUnderRoot reports whether fullPath — a vault-relative path as returned
// by Client.Search, which searches the whole vault — falls under root, and
// if so returns the path relative to root that Read/Write/Delete expect.
func pathUnderRoot(root, fullPath string) (string, bool) {
	rootClean := path.Clean("/" + root)
	fullClean := path.Clean("/" + fullPath)
	prefix := rootClean + "/"
	if !strings.HasPrefix(fullClean, prefix) {
		return "", false
	}
	return strings.TrimPrefix(fullClean, prefix), true
}
