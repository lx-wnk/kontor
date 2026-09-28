package tools

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// ObsidianDeps holds the dependencies required by the Obsidian vault MCP
// tools. Client enforces nothing itself (see its own doc comment) — Gate is
// what makes every handler below safe to call.
//
// Unlike the manual index trigger's Gate (internal/api/obsidian/handler.go,
// built with no Asker in serverapp/di.go because nobody is watching that
// run resolve a capability question), this Gate is built WITH an Asker in
// production: an MCP tool call has an agent genuinely waiting on the
// response, so an ask decision may legitimately hold for a human's answer
// instead of having to fail closed.
type ObsidianDeps struct {
	Clients *obsidian.ClientHolder
	Gate    memory.Gate
	// Caller resolves the stage run on the request's credential into the task
	// and routine capability contexts the grant chain is ranked against. The
	// zero value resolves to nothing, which is exactly how a machine-wide key
	// behaves.
	Caller mcp.CallerResolver
}

// obsidianScope is the context every Authorize call below runs against. The
// vault is one machine-wide resource — obsidian.Register catalogues its
// application resource at repo.GlobalScope(), and there is no notion of a
// per-project vault — so there is no caller-supplied scope to parse, unlike
// the memory tools.
func obsidianScope() repo.Scope { return repo.GlobalScope() }

func obsidianAvailable(d ObsidianDeps) func() bool {
	return func() bool { return d.Clients.Get() != nil }
}

// RegisterObsidianTools registers the 4 Obsidian vault MCP tools into the
// given registry; each is listed only while a vault is configured.
func RegisterObsidianTools(registry mcp.ToolRegistry, d ObsidianDeps) {
	registerObsidianRead(registry, d)
	registerObsidianSearch(registry, d)
	registerObsidianWrite(registry, d)
	registerObsidianDelete(registry, d)
}

func registerObsidianRead(registry mcp.ToolRegistry, d ObsidianDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "obsidian_read",
		Description: "Read the raw content of a note from the configured Obsidian vault.",
		Available:   obsidianAvailable(d),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Vault-relative note path"},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			client := d.Clients.Get()
			if client == nil {
				return nil, mcp.Fail("obsidian_read: obsidian vault not configured")
			}
			rawPath, err := mcp.StringArg(args, "path")
			if err != nil {
				return nil, err
			}
			// Normalize once, before the gate, and use the SAME string for
			// the grant check and the client call — see Client.NormalizeNotePath's
			// doc comment for why passing the raw path to Authorize while the
			// client normalizes its own copy lets a ".." segment defeat a
			// pattern-narrowed grant. A normalization failure is a malformed
			// request (the client would refuse it too), not a permission
			// question, so it fails before Authorize runs.
			notePath, err := client.NormalizeNotePath(rawPath)
			if err != nil {
				return nil, mcp.Fail("obsidian_read: " + err.Error())
			}
			// The note path is the capability value, so a grant can be
			// narrowed to a subtree by pattern instead of the vault as a whole.
			if err := d.Gate.Authorize(ctx, obsidian.CapabilityRead, notePath, obsidianScope(), d.Caller.Contexts(ctx)...); err != nil {
				return nil, mcp.Fail("obsidian_read: " + err.Error())
			}
			content, err := client.Read(ctx, notePath)
			if err != nil {
				return nil, mcp.Fail("obsidian_read: " + err.Error())
			}
			return mcp.OK(map[string]any{"path": notePath, "content": content})
		},
	})
}

func registerObsidianSearch(registry mcp.ToolRegistry, d ObsidianDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "obsidian_search",
		Description: "Search the whole Obsidian vault for notes matching a query.",
		Available:   obsidianAvailable(d),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Text to search for"},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			client := d.Clients.Get()
			if client == nil {
				return nil, mcp.Fail("obsidian_search: obsidian vault not configured")
			}
			query, err := mcp.StringArg(args, "query")
			if err != nil {
				return nil, err
			}
			// A search fans out across the whole vault rather than naming
			// one target, so there is no single path to pass as the
			// capability value. "" is NOT capability.Match's wildcard here —
			// that wildcard is an empty GRANT PATTERN (pattern.go's Match),
			// not an empty requested value — so a grant narrowed to a literal
			// prefix ("notes/" say) never matches "" and leaves search denied.
			// An empty pattern and a "*" pattern both authorize it (Match's
			// prefix branch: every string carries the empty prefix), which is
			// what README's `grants add obsidian.search --pattern '*'` relies
			// on. Matches IndexNotes' own use of "" for the same capability.
			if err := d.Gate.Authorize(ctx, obsidian.CapabilitySearch, "", obsidianScope(), d.Caller.Contexts(ctx)...); err != nil {
				return nil, mcp.Fail("obsidian_search: " + err.Error())
			}
			// SearchUnderRoot, never Search: Client.Search is vault-wide, so
			// its raw results would hand the agent the names of notes outside
			// VaultRoot — an existence disclosure past the boundary
			// resolveVaultPath enforces on every other call, and against paths
			// a follow-up obsidian_read would then refuse anyway.
			results, err := client.SearchUnderRoot(ctx, query)
			if err != nil {
				return nil, mcp.Fail("obsidian_search: " + err.Error())
			}
			return mcp.OK(map[string]any{"results": results})
		},
	})
}

func registerObsidianWrite(registry mcp.ToolRegistry, d ObsidianDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "obsidian_write",
		Description: "Create or overwrite a note in the configured Obsidian vault. Destructive: overwrites any existing content at the path.",
		Available:   obsidianAvailable(d),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Vault-relative note path"},
				"content": map[string]any{"type": "string", "description": "Full note content (Markdown)"},
			},
			"required": []string{"path", "content"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			client := d.Clients.Get()
			if client == nil {
				return nil, mcp.Fail("obsidian_write: obsidian vault not configured")
			}
			rawPath, err := mcp.StringArg(args, "path")
			if err != nil {
				return nil, err
			}
			content, err := mcp.StringArg(args, "content")
			if err != nil {
				return nil, err
			}
			// See registerObsidianRead's identical comment: normalize
			// before the gate, use the same value for both calls.
			notePath, err := client.NormalizeNotePath(rawPath)
			if err != nil {
				return nil, mcp.Fail("obsidian_write: " + err.Error())
			}
			if err := d.Gate.Authorize(ctx, obsidian.CapabilityWrite, notePath, obsidianScope(), d.Caller.Contexts(ctx)...); err != nil {
				return nil, mcp.Fail("obsidian_write: " + err.Error())
			}
			if err := client.Write(ctx, notePath, content); err != nil {
				return nil, mcp.Fail("obsidian_write: " + err.Error())
			}
			return mcp.OK(map[string]any{"path": notePath})
		},
	})
}

func registerObsidianDelete(registry mcp.ToolRegistry, d ObsidianDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "obsidian_delete",
		Description: "Delete a note from the configured Obsidian vault. Irreversible.",
		Available:   obsidianAvailable(d),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Vault-relative note path"},
			},
			"required": []string{"path"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			client := d.Clients.Get()
			if client == nil {
				return nil, mcp.Fail("obsidian_delete: obsidian vault not configured")
			}
			rawPath, err := mcp.StringArg(args, "path")
			if err != nil {
				return nil, err
			}
			// See registerObsidianRead's identical comment: normalize
			// before the gate, use the same value for both calls.
			notePath, err := client.NormalizeNotePath(rawPath)
			if err != nil {
				return nil, mcp.Fail("obsidian_delete: " + err.Error())
			}
			if err := d.Gate.Authorize(ctx, obsidian.CapabilityDelete, notePath, obsidianScope(), d.Caller.Contexts(ctx)...); err != nil {
				return nil, mcp.Fail("obsidian_delete: " + err.Error())
			}
			if err := client.Delete(ctx, notePath); err != nil {
				return nil, mcp.Fail("obsidian_delete: " + err.Error())
			}
			return mcp.OK(map[string]any{"path": notePath, "deleted": true})
		},
	})
}
