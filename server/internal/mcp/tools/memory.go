package tools

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// MemoryDeps holds the dependencies required by the memory MCP tools. Gate is
// the second, independent authorization layer: the MCP scope on
// ToolScopeMap only authorises the caller's key to reach the transport, Gate
// authorises the action itself.
type MemoryDeps struct {
	Repo      repo.MemoryRepo
	Retriever *memory.Retriever
	Gate      memory.Gate
	// Caller resolves the stage run on the request's credential into the task
	// and routine capability contexts the grant chain is ranked against. The
	// zero value resolves to nothing, which is exactly how a machine-wide key
	// behaves.
	Caller mcp.CallerResolver
}

// RegisterMemoryTools registers the 2 memory MCP tools into the given registry.
func RegisterMemoryTools(registry mcp.ToolRegistry, d MemoryDeps) {
	registerMemorySearch(registry, d)
	registerMemoryWrite(registry, d)
}

var memoryScopeEnum = memory.ScopeKinds

// parseMemoryScope builds a repo.Scope from the tool's "scope"/"scopeRef"
// arguments. Delegates to memory.ParseScope, the transport-agnostic core
// shared with the HTTP API, and wraps its error as an mcp.Fail.
func parseMemoryScope(args map[string]any) (repo.Scope, error) {
	scope, err := memory.ParseScope(mcp.OptionalString(args, "scope"), mcp.OptionalString(args, "scopeRef"))
	if err != nil {
		return repo.Scope{}, mcp.Fail(err.Error())
	}
	return scope, nil
}

func registerMemorySearch(registry mcp.ToolRegistry, d MemoryDeps) {
	registry.Register(&mcp.ToolDef{
		Name: "memory_search",
		Description: "Search the system memory store for entries relevant to a query, ranked by " +
			"lexical match, scope specificity, recency, confidence and kind.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":    map[string]any{"type": "string", "description": "Text to search for"},
				"scope":    map[string]any{"type": "string", "enum": memoryScopeEnum, "description": "Visibility scope to search within (default global)"},
				"scopeRef": map[string]any{"type": "string", "description": "Scope reference (project path or application id); required unless scope is global"},
				"limit":    map[string]any{"type": "number", "description": "Max results to return (default 10, max 50)"},
			},
			"required": []string{"query"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			query, err := mcp.StringArg(args, "query")
			if err != nil {
				return nil, err
			}
			scope, err := parseMemoryScope(args)
			if err != nil {
				return nil, err
			}
			// Read access is granted per scope, not per space — a search
			// fans out across every space visible in scope, so there is no
			// single space identity to match a grant pattern against. The
			// empty value is capability.Match's documented wildcard.
			if err := d.Gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope, d.Caller.Contexts(ctx)...); err != nil {
				return nil, mcp.Fail("memory_search: " + err.Error())
			}

			limit := 0
			if f, ok := mcp.OptionalFloat64(args, "limit"); ok {
				limit = int(f)
			}
			entries, err := d.Retriever.Retrieve(ctx, memory.Query{Text: query, Scope: scope, Limit: limit})
			if err != nil {
				return nil, mcp.Fail("memory_search: " + err.Error())
			}
			return mcp.OK(map[string]any{"entries": entries})
		},
	})
}

func registerMemoryWrite(registry mcp.ToolRegistry, d MemoryDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "memory_write",
		Description: "Write a new entry into an existing system memory space.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spaceSlug":  map[string]any{"type": "string", "description": "Slug of the memory space to write into"},
				"scope":      map[string]any{"type": "string", "enum": memoryScopeEnum, "description": "Scope the space lives in (default global)"},
				"scopeRef":   map[string]any{"type": "string", "description": "Scope reference (project path or application id); required unless scope is global"},
				"summary":    map[string]any{"type": "string", "description": "Short text pushed into a spawn's prompt"},
				"content":    map[string]any{"type": "string", "description": "Full text retrievable on demand"},
				"kind":       map[string]any{"type": "string", "enum": repo.MemoryKinds},
				"sourceKind": map[string]any{"type": "string", "enum": repo.MemorySourceKinds},
				"sourceRef":  map[string]any{"type": "string", "description": "Origin identifier: stage-run id, application id, file path"},
				"confidence": map[string]any{"type": "number", "description": "Confidence 0..1 (default 1)"},
			},
			"required": []string{"spaceSlug", "summary", "content", "kind", "sourceKind"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			spaceSlug, err := mcp.StringArg(args, "spaceSlug")
			if err != nil {
				return nil, err
			}
			summary, err := mcp.StringArg(args, "summary")
			if err != nil {
				return nil, err
			}
			content, err := mcp.StringArg(args, "content")
			if err != nil {
				return nil, err
			}
			kind, err := mcp.StringArg(args, "kind")
			if err != nil {
				return nil, err
			}
			sourceKind, err := mcp.StringArg(args, "sourceKind")
			if err != nil {
				return nil, err
			}
			scope, err := parseMemoryScope(args)
			if err != nil {
				return nil, err
			}

			// Authorize before resolving the space: resolving first would let
			// an ungranted caller distinguish "unknown space" (404-shaped
			// error) from "denied" (403-shaped error) without ever holding a
			// grant — a space-existence oracle ahead of the gate.
			if err := d.Gate.Authorize(ctx, repo.CapabilityMemoryWrite, spaceSlug, scope, d.Caller.Contexts(ctx)...); err != nil {
				return nil, mcp.Fail("memory_write: " + err.Error())
			}

			// The space must already exist — memory_write never creates one
			// on the fly. Auto-creating here would let any caller with the
			// MCP transport scope invent an arbitrary space identity, which
			// no grant could have been written against in advance.
			space, err := d.Repo.GetSpace(ctx, scope, spaceSlug)
			if err != nil {
				return nil, mcp.Fail("memory_write: unknown space " + spaceSlug + ": " + err.Error())
			}

			cleanSummary, cleanContent, err := memory.SanitizeForStore(summary, content)
			if err != nil {
				return nil, mcp.Fail("memory_write: " + err.Error())
			}

			confidence := 1.0
			if f, ok := mcp.OptionalFloat64(args, "confidence"); ok {
				confidence = f
			}
			var sourceRef *string
			if s := mcp.OptionalString(args, "sourceRef"); s != "" {
				sourceRef = &s
			}

			entry, err := d.Repo.CreateEntry(ctx, repo.CreateEntryInput{
				SpaceID:    space.ID,
				Summary:    cleanSummary,
				Content:    cleanContent,
				Kind:       kind,
				SourceKind: sourceKind,
				SourceRef:  sourceRef,
				Confidence: confidence,
			})
			if err != nil {
				return nil, mcp.Fail("memory_write: " + err.Error())
			}
			return mcp.OK(entry)
		},
	})
}
