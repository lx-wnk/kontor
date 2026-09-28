package tools

import (
	"context"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
)

// KeyDeps holds dependencies required by the API key management tools.
type KeyDeps struct {
	ApiKeyRepo repo.ApiKeyRepo
}

// validKeyScopes is the set of accepted scope values for API keys.
// agent:coord is deliberately absent: it exists in mcp.ToolScopeMap and is
// reachable through keys:manage's implication, but a key may not be granted
// it directly.
var validKeyScopes = map[string]bool{
	"tasks:read":       true,
	"tasks:write":      true,
	"pipeline:control": true,
	"keys:manage":      true,
	"memory:read":      true,
	"memory:write":     true,
	"obsidian:read":    true,
	"obsidian:write":   true,
	"github:read":      true,
	"github:write":     true,
	"github:merge":     true,
}

// RegisterKeyTools registers all 3 API key tools into the given registry.
func RegisterKeyTools(registry mcp.ToolRegistry, d KeyDeps) {
	registerListAPIKeys(registry, d)
	registerCreateAPIKey(registry, d)
	registerRevokeAPIKey(registry, d)
}

func registerListAPIKeys(registry mcp.ToolRegistry, d KeyDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "list_api_keys",
		Description: "List active API keys.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			keys, err := d.ApiKeyRepo.List(ctx)
			if err != nil {
				return nil, mcp.Fail("list_api_keys: " + err.Error())
			}
			type keyView struct {
				ID        string   `json:"id"`
				Name      string   `json:"name"`
				Scopes    []string `json:"scopes"`
				Active    bool     `json:"active"`
				CreatedAt string   `json:"created_at"`
			}
			out := make([]keyView, len(keys))
			for i, k := range keys {
				out[i] = keyView{
					ID:        k.ID,
					Name:      k.Name,
					Scopes:    k.Scopes,
					Active:    k.Active,
					CreatedAt: k.CreatedAt.Format("2006-01-02T15:04:05Z"),
				}
			}
			return mcp.OK(out)
		},
	})
}

func registerCreateAPIKey(registry mcp.ToolRegistry, d KeyDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "create_api_key",
		Description: "Create a new API key. Returns the key metadata AND the raw token — save the token immediately, it is not stored.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Unique human-readable name for this key",
				},
				"scopes": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
						"enum": []string{"tasks:read", "tasks:write", "pipeline:control", "keys:manage", "memory:read", "memory:write", "obsidian:read", "obsidian:write", "github:read", "github:write", "github:merge"},
					},
					"description": "List of scopes to grant to this key",
				},
			},
			"required": []string{"name", "scopes"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			name, err := mcp.StringArg(args, "name")
			if err != nil {
				return nil, err
			}

			// Extract scopes — comes as []any from JSON decoding.
			rawScopes, ok := args["scopes"]
			if !ok || rawScopes == nil {
				return nil, mcp.Fail("scopes is required")
			}
			rawSlice, ok := rawScopes.([]any)
			if !ok {
				return nil, mcp.Fail("scopes must be an array")
			}
			scopes := make([]string, 0, len(rawSlice))
			for _, item := range rawSlice {
				s, ok := item.(string)
				if !ok {
					return nil, mcp.Fail("each scope must be a string")
				}
				s = strings.TrimSpace(s)
				if !validKeyScopes[s] {
					return nil, mcp.Fail("invalid scope: " + s + " (allowed: tasks:read, tasks:write, pipeline:control, keys:manage, memory:read, memory:write, obsidian:read, obsidian:write, github:read, github:write, github:merge)")
				}
				scopes = append(scopes, s)
			}
			if len(scopes) == 0 {
				return nil, mcp.Fail("at least one scope is required")
			}

			token := mcp.GenerateAPIToken()
			hash := mcp.HashToken(token)
			key, err := d.ApiKeyRepo.Create(ctx, repo.CreateApiKeyInput{Name: name, Hash: hash, Scopes: scopes})
			if err != nil {
				return nil, mcp.Fail("create_api_key: " + err.Error())
			}
			// Return the raw token once — it is not stored and cannot be retrieved later.
			return mcp.OK(map[string]any{"key": key, "token": token})
		},
	})
}

func registerRevokeAPIKey(registry mcp.ToolRegistry, d KeyDeps) {
	registry.Register(&mcp.ToolDef{
		Name:        "revoke_api_key",
		Description: "Revoke (soft-delete) an API key by ID. The key can no longer be used for authentication.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "API key ID"},
			},
			"required": []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (*mcp.ToolResult, error) {
			id, err := mcp.StringArg(args, "id")
			if err != nil {
				return nil, err
			}
			if _, err := d.ApiKeyRepo.GetByID(ctx, id); err != nil {
				return nil, mcp.Fail("API key not found: " + id)
			}
			if err := d.ApiKeyRepo.Delete(ctx, id); err != nil {
				return nil, mcp.Fail("revoke_api_key: " + err.Error())
			}
			return mcp.OK(map[string]bool{"success": true})
		},
	})
}
