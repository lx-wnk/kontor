package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// ToolScopeMap maps every MCP tool name to the minimum scope required to call it.
var ToolScopeMap = map[string]string{
	// tasks:read
	"list_tasks": "tasks:read", "get_task": "tasks:read",
	"list_stage_runs": "tasks:read", "list_audit": "tasks:read",
	"list_permission_requests": "tasks:read",
	"list_projects":            "tasks:read",
	"list_spawners":            "tasks:read",
	"list_schedules":           "tasks:read",
	// tasks:write
	"create_task": "tasks:write", "update_task": "tasks:write",
	"delete_task": "tasks:write", "manage_task": "tasks:write",
	"add_dependency": "tasks:write", "remove_dependency": "tasks:write",
	"create_project":  "tasks:write",
	"manage_schedule": "tasks:write",
	// agent:coord
	"write_scratchpad": "agent:coord", "read_scratchpad": "agent:coord",
	"list_scratchpad": "agent:coord",
	"acquire_lock":    "agent:coord", "release_lock": "agent:coord",
	"wait_for_port": "agent:coord",
	// pipeline:control
	"advance_task": "pipeline:control", "hold_task": "pipeline:control",
	"resume_task":   "pipeline:control",
	"progress_task": "pipeline:control", "cancel_task": "pipeline:control",
	"retry_task": "pipeline:control", "grant_permission": "pipeline:control",
	"resolve_permission_request": "pipeline:control",
	"approve_all_pending":        "pipeline:control",
	"get_refine_status":          "pipeline:control", "approve_spec": "pipeline:control",
	"refine_task": "pipeline:control", "inject_concept": "pipeline:control",
	"approve_plan": "pipeline:control", "reject_plan": "pipeline:control", "get_plan_status": "pipeline:control",
	// keys:manage
	"list_api_keys": "keys:manage", "create_api_key": "keys:manage",
	"revoke_api_key": "keys:manage",
	// memory:read / memory:write
	"memory_search": "memory:read", "memory_write": "memory:write",
	// obsidian:read / obsidian:write
	"obsidian_read": "obsidian:read", "obsidian_search": "obsidian:read",
	"obsidian_write": "obsidian:write", "obsidian_delete": "obsidian:write",
	// github:read / github:write / github:merge
	"github_read": "github:read", "github_search": "github:read",
	"github_comment": "github:write",
	"github_merge":   "github:merge",
}

var scopeImplies = map[string][]string{
	"tasks:read":       {},
	"tasks:write":      {"tasks:read"},
	"agent:coord":      {},
	"memory:read":      {},
	"memory:write":     {"memory:read"},
	"obsidian:read":    {},
	"obsidian:write":   {"obsidian:read"},
	"pipeline:control": {"tasks:read", "agent:coord"},
	"github:read":      {},
	"github:write":     {"github:read"},
	// merge implies read, deliberately NOT write: a key that may comment must
	// not be able to merge, and a key that may merge has no business editing
	// discussions. The capability gate is what actually decides a merge; this
	// is the coarser net above it.
	"github:merge": {"github:read"},
	"keys:manage": {
		"tasks:read", "tasks:write", "pipeline:control", "agent:coord",
		"memory:read", "memory:write", "obsidian:read", "obsidian:write",
		"github:read", "github:write", "github:merge",
	},
}

// ResolveScopes expands scopes with their implied scopes.
func ResolveScopes(scopes []string) map[string]bool {
	result := make(map[string]bool)
	for _, s := range scopes {
		result[s] = true
		for _, implied := range scopeImplies[s] {
			result[implied] = true
		}
	}
	return result
}

// HashToken returns the SHA-256 hex digest of token.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// GenerateAPIToken generates a cryptographically random "mcp_<hex>" token.
func GenerateAPIToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("mcp: cannot generate token: " + err.Error())
	}
	return "mcp_" + hex.EncodeToString(b)
}

type mcpAuthKey struct{}

// MCPAuthInfo carries resolved auth info attached to the request context.
type MCPAuthInfo struct {
	KeyID  string
	Scopes map[string]bool
	// StageRunID names the pipeline stage run this key was issued for, empty
	// for a key a person created. CallerResolver turns it into the capability
	// contexts the request resolves against.
	StageRunID string
}

// AuthFromContext retrieves MCPAuthInfo from ctx; returns nil if absent.
func AuthFromContext(ctx context.Context) *MCPAuthInfo {
	v, _ := ctx.Value(mcpAuthKey{}).(*MCPAuthInfo)
	return v
}

// ContextWithAuth attaches MCPAuthInfo to ctx. Used by the auth middleware and
// by tests that exercise auth-scoped tool handlers.
func ContextWithAuth(ctx context.Context, info *MCPAuthInfo) context.Context {
	return context.WithValue(ctx, mcpAuthKey{}, info)
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// lastTouched stores the last time TouchLastUsed was called per API key ID.
var lastTouched sync.Map // map[keyID string]time.Time

// touchDebounce is the minimum interval between TouchLastUsed DB writes per key.
const touchDebounce = 60 * time.Second

// shouldTouch returns true (and records the current time) only when at least
// touchDebounce has elapsed since the last successful call for keyID.
func shouldTouch(keyID string) bool {
	if v, ok := lastTouched.Load(keyID); ok {
		if time.Since(v.(time.Time)) < touchDebounce {
			return false
		}
	}
	lastTouched.Store(keyID, time.Now())
	return true
}

// McpAuthMiddleware is a chi-compatible middleware that authenticates MCP requests.
// It reads Bearer token → SHA-256 hash → DB lookup → resolved scopes → context.
func McpAuthMiddleware(keyRepo repo.ApiKeyRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			rest, ok := strings.CutPrefix(header, "Bearer ")
			if !ok {
				writeAuthError(w, "Missing or invalid Authorization header")
				return
			}
			token := strings.TrimSpace(rest)
			hash := HashToken(token)
			key, err := keyRepo.GetByHash(r.Context(), hash)
			if err != nil {
				writeAuthError(w, "Invalid or revoked API key")
				return
			}
			// Fire-and-forget: detach from request ctx so cancel/timeout doesn't suppress the write; failures are non-critical.
			// Debounced: skip the DB write if called within touchDebounce of the last write for this key.
			if shouldTouch(key.ID) {
				go func() { _ = keyRepo.TouchLastUsed(context.Background(), key.ID) }() //nolint:errcheck,gosec
			}
			info := &MCPAuthInfo{
				KeyID:      key.ID,
				Scopes:     ResolveScopes(key.Scopes),
				StageRunID: key.StageRunID,
			}
			next.ServeHTTP(w, r.WithContext(ContextWithAuth(r.Context(), info)))
		})
	}
}
