package tools

import (
	"context"
	"fmt"

	githubapp "github.com/lx-wnk/kontor/server/internal/apps/github"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// GitHubDeps holds the dependencies required by the GitHub MCP tools. Client
// enforces no capability of its own (see its doc comment) — Gate is what makes
// every handler below safe to call.
//
// Gate is built WITH an Asker in production (serverapp/di_mcp.go), like the
// Obsidian tools: an MCP call has an agent genuinely waiting on the response,
// so an ask decision may hold for a human's answer. github_merge never reaches
// the asker regardless — its "spend" class resolves to deny in
// capability.Decide, and ServerEnforcer returns ErrDenied before the ask
// branch, so nobody can be prompted into a merge.
type GitHubDeps struct {
	Client *githubapp.Client
	Gate   memory.Gate
}

// githubScope: one machine-wide credential, so global scope, no caller-supplied
// scope to parse. Same reasoning as obsidianScope.
func githubScope() repo.Scope { return repo.GlobalScope() }

// authorize runs the allow-list check and the gate in the order decision D4
// fixes: the repository FIRST, without a capability question, then the gate on
// the very same owner/name string the client will act on. repoName is "" for
// calls that name no single repository.
//
// Gate.Authorize is called with four arguments, not with caller contexts:
// mcp.CallerResolver does not exist on this branch (it belongs to the
// stage-run-credentials work). memory.Gate.Authorize's variadic
// `extra ...capability.Context` already accepts them, so when that lands these
// four call sites take the same one-line edit as the Obsidian ones.
func (d GitHubDeps) authorize(ctx context.Context, capName, repoName string) error {
	if repoName != "" {
		configured, ok := d.Client.CanonicalRepo(repoName)
		if !ok {
			return mcp.Fail(fmt.Sprintf("%s is not in the configured github.repos allow-list", repoName))
		}
		// The gate matches grant patterns on the configured spelling, so a
		// case variant cannot slip past a deny grant.
		repoName = configured
	}
	if err := d.Gate.Authorize(ctx, capName, repoName, githubScope()); err != nil {
		return mcp.Fail(err.Error())
	}
	return nil
}

// RegisterGitHubTools registers the 4 GitHub MCP tools. When d.Client is nil —
// GitHub is unconfigured — no tool is registered at all, mirroring
// RegisterObsidianTools.
func RegisterGitHubTools(registry mcp.ToolRegistry, d GitHubDeps) {
	if d.Client == nil {
		return
	}
	registerGitHubRead(registry, d)
	registerGitHubSearch(registry, d)
	registerGitHubComment(registry, d)
	registerGitHubMerge(registry, d)
}

// numberArg reads a positive issue or pull-request number. JSON numbers decode
// as float64, so an int argument cannot be read with StringArg's shape.
func numberArg(args map[string]any, key string) (int, error) {
	raw, ok := args[key]
	if !ok {
		return 0, mcp.Fail(key + " is required")
	}
	f, ok := raw.(float64)
	if !ok {
		return 0, mcp.Fail(key + " must be a number")
	}
	n := int(f)
	if float64(n) != f || n <= 0 {
		return 0, mcp.Fail(key + " must be a positive whole number")
	}
	return n, nil
}

func registerGitHubRead(registry mcp.ToolRegistry, d GitHubDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "github_read",
		Description: "List the most recently updated open pull requests in one of the configured GitHub repositories.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":  map[string]any{"type": "string", "description": "owner/name, and it must be listed in the github.repos setting"},
				"limit": map[string]any{"type": "number", "description": "How many pull requests to return (default 5)"},
			},
			"required": []string{"repo"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			repoName, err := mcp.StringArg(args, "repo")
			if err != nil {
				return nil, err
			}
			// The repository is the capability value, so a grant can be narrowed
			// to one repository by pattern instead of opening all of them.
			if err := d.authorize(ctx, githubapp.CapabilityRead, repoName); err != nil {
				return nil, err
			}
			limit := 5
			if n, ok := args["limit"].(float64); ok && n > 0 {
				limit = int(n)
			}
			prs, err := d.Client.OpenPullRequests(ctx, repoName, limit)
			if err != nil {
				return nil, mcp.Fail("github_read: " + err.Error())
			}
			return mcp.OK(map[string]any{"repo": repoName, "pullRequests": prs})
		},
	})
}

func registerGitHubSearch(registry mcp.ToolRegistry, d GitHubDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "github_search",
		Description: "Search issues and pull requests across the configured GitHub repositories.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "GitHub issue-search query"},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			query, err := mcp.StringArg(args, "query")
			if err != nil {
				return nil, err
			}
			// A search names no single repository, so there is no value to pass.
			// "" is NOT a wildcard here — see obsidian_search's comment for the
			// full reasoning: a grant narrowed to a literal prefix never matches
			// "", so search stays denied; an empty or "*" pattern authorizes it.
			if err := d.authorize(ctx, githubapp.CapabilitySearch, ""); err != nil {
				return nil, err
			}
			// Bound the query to the allow-list before it leaves, the same way
			// the HTTP route does — a search must never report a repository the
			// operator did not list. BoundQuery is the single owner of that
			// rule; it also refuses a query that carries its own scope
			// qualifier, which appending alone cannot bound.
			bounded, err := d.Client.BoundQuery(query)
			if err != nil {
				return nil, mcp.Fail("github_search: " + err.Error())
			}
			hits, err := d.Client.SearchIssues(ctx, bounded)
			if err != nil {
				return nil, mcp.Fail("github_search: " + err.Error())
			}
			return mcp.OK(map[string]any{"results": hits})
		},
	})
}

func registerGitHubComment(registry mcp.ToolRegistry, d GitHubDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "github_comment",
		Description: "Post a comment on a GitHub issue or pull request. Irreversible: the comment is public the moment it posts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":   map[string]any{"type": "string", "description": "owner/name, and it must be listed in the github.repos setting"},
				"number": map[string]any{"type": "number", "description": "Issue or pull-request number"},
				"body":   map[string]any{"type": "string", "description": "Comment body (Markdown)"},
			},
			"required": []string{"repo", "number", "body"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			repoName, err := mcp.StringArg(args, "repo")
			if err != nil {
				return nil, err
			}
			number, err := numberArg(args, "number")
			if err != nil {
				return nil, err
			}
			body, err := mcp.StringArg(args, "body")
			if err != nil {
				return nil, err
			}
			if err := d.authorize(ctx, githubapp.CapabilityComment, repoName); err != nil {
				return nil, err
			}
			url, err := d.Client.Comment(ctx, repoName, number, body)
			if err != nil {
				return nil, mcp.Fail("github_comment: " + err.Error())
			}
			return mcp.OK(map[string]any{"url": url})
		},
	})
}

func registerGitHubMerge(registry mcp.ToolRegistry, d GitHubDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "github_merge",
		Description: "Merge a GitHub pull request. Irreversible, and denied unless a human created an explicit github.merge grant.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo":   map[string]any{"type": "string", "description": "owner/name, and it must be listed in the github.repos setting"},
				"number": map[string]any{"type": "number", "description": "Pull-request number"},
				"method": map[string]any{"type": "string", "description": "merge, squash or rebase (default squash)"},
			},
			"required": []string{"repo", "number"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			repoName, err := mcp.StringArg(args, "repo")
			if err != nil {
				return nil, err
			}
			number, err := numberArg(args, "number")
			if err != nil {
				return nil, err
			}
			// Registered like the other three; the class does the work. With no
			// grant, capability.Decide's defaultEffect sends "spend" to deny.
			if err := d.authorize(ctx, githubapp.CapabilityMerge, repoName); err != nil {
				return nil, err
			}
			sha, err := d.Client.MergePullRequest(ctx, repoName, number, mcp.OptionalString(args, "method"))
			if err != nil {
				return nil, mcp.Fail("github_merge: " + err.Error())
			}
			return mcp.OK(map[string]any{"repo": repoName, "number": number, "sha": sha})
		},
	})
}
