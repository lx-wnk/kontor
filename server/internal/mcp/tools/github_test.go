package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	githubapp "github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

const ghTestRepo = "lx-wnk/kontor"

// TestEveryGitHubCapabilityIsOnBothSurfaces is the rule this project has
// broken twice: a gated action wired on one surface only. It reads the
// application's own capability declarations — not a retyped list — and
// asserts each has both an HTTP route in the golden and an MCP tool with a
// scope entry. Adding a fifth capability without both surfaces fails here.
//
// The route half of this test reads server/internal/api/testdata/routes.golden,
// which TestRouteGolden (internal/api/route_golden_test.go) regenerates from
// buildBypassRouter (internal/api/bypass_auth_smoke_test.go) — a
// hand-maintained mirror of serverapp/di.go's RouterDeps construction, not the
// production DI graph itself. NewRouter (internal/api/router.go), the function
// that actually mounts routes onto RouterDeps, IS production code and is what
// this golden proves routes against — so a route present here is guaranteed
// to mount at that exact path in the real server, given a populated
// RouterDeps. What the golden does NOT prove is that serverapp/di.go actually
// populates RouterDeps.GitHubHandler in the first place; a standalone router
// close enough to production to prove that would mean booting the whole
// server (entClient, every other handler's dependencies), which is out of
// reach for a unit test. That di.go wiring was checked by hand for this task
// (di.go:955, `GitHubHandler: githubHandler`) rather than asserted here — this
// test's route half is only as good as the mirror it reads.
func TestEveryGitHubCapabilityIsOnBothSurfaces(t *testing.T) {
	byCapability := map[string]struct {
		tool  string
		route string
	}{
		githubapp.CapabilityRead:    {"github_read", "GET /api/github/summary"},
		githubapp.CapabilitySearch:  {"github_search", "GET /api/github/search"},
		githubapp.CapabilityComment: {"github_comment", "POST /api/github/comment"},
		githubapp.CapabilityMerge:   {"github_merge", "POST /api/github/merge"},
	}

	golden, err := os.ReadFile(filepath.Join("..", "..", "api", "testdata", "routes.golden"))
	require.NoError(t, err)

	deps, _, _, _ := newGitHubDepsForTest(t)
	registry := mcp.ToolRegistry{}
	RegisterGitHubTools(registry, deps)

	for _, decl := range githubapp.Capabilities() {
		pair, ok := byCapability[decl.Name]
		require.Truef(t, ok, "capability %s has no surface pair — add both an MCP tool and an HTTP route, never one", decl.Name)

		require.NotNilf(t, registry[pair.tool], "%s: MCP tool %s is not registered", decl.Name, pair.tool)
		_, hasScope := mcp.ToolScopeMap[pair.tool]
		require.Truef(t, hasScope, "%s: tool %s has no ToolScopeMap entry — Register panics at construction without one", decl.Name, pair.tool)
		require.Containsf(t, string(golden), pair.route+"\n", "%s: HTTP route %q is missing from the route golden", decl.Name, pair.route)
	}
}

func TestGitHubScopesAreGrantableAndImplyCorrectly(t *testing.T) {
	require.True(t, validKeyScopes["github:read"])
	require.True(t, validKeyScopes["github:write"])
	require.True(t, validKeyScopes["github:merge"])

	require.True(t, mcp.ResolveScopes([]string{"github:write"})["github:read"], "github:write must imply github:read")
	require.False(t, mcp.ResolveScopes([]string{"github:write"})["github:merge"], "a key that may comment must NOT be able to merge")
	require.True(t, mcp.ResolveScopes([]string{"github:merge"})["github:read"], "github:merge must imply github:read")

	all := mcp.ResolveScopes([]string{"keys:manage"})
	for _, s := range []string{"github:read", "github:write", "github:merge"} {
		require.Truef(t, all[s], "keys:manage must imply %s — scopeImplies is one level deep, so it must list them by hand", s)
	}
}

func newGitHubDepsForTest(t *testing.T) (GitHubDeps, repo.GrantRepo, *bool, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	caps := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, githubapp.Register(ctx, resources, caps))

	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		switch {
		case strings.HasSuffix(r.URL.Path, "/merge"):
			_ = json.NewEncoder(w).Encode(map[string]any{"merged": true, "sha": "deadbeef"})
		case strings.HasSuffix(r.URL.Path, "/comments"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"html_url": "c"})
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		}
	}))
	t.Cleanup(upstream.Close)

	client, err := githubapp.NewClient(githubapp.Config{
		Token: "ghp_supersecret", BaseURL: upstream.URL,
		Repos: []string{ghTestRepo}, AllowLoopback: true,
	})
	require.NoError(t, err)

	// No Asker, so the ask effect fails closed and a test can tell a denied
	// merge from an ask-required comment by the error text.
	return GitHubDeps{Client: client, Gate: memory.Gate{
		Capabilities: caps, Grants: grants,
		GrantUsage: repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
	}}, grants, &called, ctx
}

func grantGitHub(t *testing.T, grants repo.GrantRepo, ctx context.Context, capName string) {
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

// TestGitHubMergeDeniedBeforeAnyRequest: class "spend" denies with no grant,
// and the deny happens before GitHub is contacted. The reason must name the
// class default (decide.go's defaultEffect), not merely "some error" — a
// merge refused for the wrong reason (e.g. a repo outside the allow-list, or
// an ask-required class) would pass a looser assertion just as well.
func TestGitHubMergeDeniedBeforeAnyRequest(t *testing.T) {
	deps, _, called, ctx := newGitHubDepsForTest(t)
	registry := mcp.ToolRegistry{}
	RegisterGitHubTools(registry, deps)

	_, err := registry["github_merge"].Handler(ctx, map[string]any{"repo": ghTestRepo, "number": float64(42)})
	require.Error(t, err)
	require.ErrorContains(t, err, "capability denied")
	require.ErrorContains(t, err, `class "spend" defaults to deny`, "the refusal must name the class default, not just fail generically")
	require.False(t, *called, "GitHub must not be reached before the gate allows the merge")
}

// TestGitHubMergeAllowedWithAnExplicitGrant: same grant, MCP surface — the
// mirror of the HTTP test in internal/api/github. Neither surface is open and
// neither is closed when the other is not.
func TestGitHubMergeAllowedWithAnExplicitGrant(t *testing.T) {
	deps, grants, called, ctx := newGitHubDepsForTest(t)
	grantGitHub(t, grants, ctx, githubapp.CapabilityMerge)
	registry := mcp.ToolRegistry{}
	RegisterGitHubTools(registry, deps)

	res, err := registry["github_merge"].Handler(ctx, map[string]any{"repo": ghTestRepo, "number": float64(42)})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.True(t, *called)
}

// TestGitHubToolsRefuseARepoOutsideTheAllowListBeforeTheGate is D4 on the MCP
// surface: merge is granted globally and the call is still refused, with a
// message about the allow-list rather than a capability.
func TestGitHubToolsRefuseARepoOutsideTheAllowListBeforeTheGate(t *testing.T) {
	deps, grants, called, ctx := newGitHubDepsForTest(t)
	grantGitHub(t, grants, ctx, githubapp.CapabilityMerge)
	registry := mcp.ToolRegistry{}
	RegisterGitHubTools(registry, deps)

	_, err := registry["github_merge"].Handler(ctx, map[string]any{"repo": "evil/repo", "number": float64(1)})
	require.Error(t, err)
	require.ErrorContains(t, err, "allow-list")
	require.NotContains(t, err.Error(), "capability denied")
	require.False(t, *called)
}

// TestGitHubToolErrorsNeverCarryTheToken is spec §6 row 5 on the MCP surface.
//
// Every capability is granted, so the gate allows every call and each handler
// actually reaches d.Client — an ungranted call would be denied before the
// client ever runs, and the local, gate-built strings that produces (the
// allow-list message, the capability-denial reason) are structurally
// incapable of containing a token, proving nothing about this property. The
// upstream test server answers every request with a non-2xx status, so each
// handler returns the client-wrapped error built from a *githubapp.StatusError
// via mcp.Fail("github_x: " + err.Error()) — the exact path a real upstream
// error message travels down to an MCP caller.
func TestGitHubToolErrorsNeverCarryTheToken(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	caps := repo.NewCapabilityRepo(bundle.Client)
	resources := repo.NewResourceRepo(bundle.Client)
	grants := repo.NewGrantRepo(bundle.Client)
	ctx := context.Background()
	require.NoError(t, githubapp.Register(ctx, resources, caps))
	for _, capName := range []string{githubapp.CapabilityRead, githubapp.CapabilitySearch, githubapp.CapabilityComment, githubapp.CapabilityMerge} {
		grantGitHub(t, grants, ctx, capName)
	}

	// The upstream message text below never contains the token — the client
	// never sends it back a token to echo, and this handler stands in for
	// GitHub's own API. See the RED/GREEN proof in the task report: with
	// "ghp_supersecret" planted in this message, the assertions below fail;
	// removed, they pass — proving this test actually inspects the string a
	// token could appear in, unlike the version it replaces.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "upstream refused the request"})
	}))
	t.Cleanup(upstream.Close)

	client, err := githubapp.NewClient(githubapp.Config{
		Token: "ghp_supersecret", BaseURL: upstream.URL,
		Repos: []string{ghTestRepo}, AllowLoopback: true,
	})
	require.NoError(t, err)

	deps := GitHubDeps{Client: client, Gate: memory.Gate{
		Capabilities: caps, Grants: grants,
		GrantUsage: repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
	}}
	registry := mcp.ToolRegistry{}
	RegisterGitHubTools(registry, deps)

	for _, name := range []string{"github_read", "github_search", "github_comment", "github_merge"} {
		_, err := registry[name].Handler(ctx, map[string]any{"repo": ghTestRepo, "number": float64(1), "body": "x", "query": "x"})
		require.Errorf(t, err, "%s: expected the upstream 403 to surface as a client error", name)
		require.NotContains(t, err.Error(), "ghp_supersecret", "%s leaked the token", name)
	}
}

// TestNoGitHubToolIsRegisteredWhenUnconfigured mirrors
// RegisterObsidianTools: an agent discovering a tool it can never use is worse
// than not discovering it.
func TestNoGitHubToolIsRegisteredWhenUnconfigured(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterGitHubTools(registry, GitHubDeps{})
	for _, name := range []string{"github_read", "github_search", "github_comment", "github_merge"} {
		require.Nil(t, registry[name], "%s must not be registered when GitHub is unconfigured", name)
	}
}
