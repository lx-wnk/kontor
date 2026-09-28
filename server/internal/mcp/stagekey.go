package mcp

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// StageRunScopes is the fixed transport scope set every stage-run key gets.
//
// It is deliberately narrow, and narrower than the first draft. Only the
// memory and obsidian tools resolve through capability.Decide; every other
// MCP tool is gated by its scope alone. So a scope handed out here is a
// capability granted outright, not one the gate will narrow later —
// "pipeline:control" would let a spawned agent approve its own spec and
// resolve its own permission requests, and "tasks:write" would let it widen
// its own permissions through manage_task. Neither is something an agent
// should be able to do to itself.
//
// keys:manage is excluded for the same family of reason: an agent able to
// mint keys could mint one with no stage run and escape its own attribution.
var StageRunScopes = []string{
	"tasks:read", "agent:coord",
	"memory:read", "memory:write", "obsidian:read", "obsidian:write",
}

// StageRunAllowedTools renders the spawn allow-list entries for the tools a
// stage-run key can actually call: every tool ToolScopeMap assigns to a scope
// in StageRunScopes (expanded through ResolveScopes, so memory:write also
// carries memory:read). The client-side allow list and the server-side scope
// check therefore have one source, and adding or removing a scope moves both.
//
// The entries matter because a spawn runs --permission-mode default: an MCP
// tool that is not pre-approved raises a permission request on its first call,
// which parks the stage run in awaiting_user instead of doing the work.
func StageRunAllowedTools() []string {
	granted := ResolveScopes(StageRunScopes)
	tools := make([]string, 0, len(ToolScopeMap))
	for tool, scope := range ToolScopeMap {
		if granted[scope] {
			tools = append(tools, "mcp__"+ServerName+"__"+tool)
		}
	}
	sort.Strings(tools) // map iteration order is random; the written file must not churn
	return tools
}

// StageKeyTTLBuffer is added to a stage's timeout when setting expires_at. It
// covers the window between an agent hitting its timeout and the orchestrator
// recording the transition, so the key does not die under a run that is still
// being wound down.
const StageKeyTTLBuffer = 5 * time.Minute

// MaxStageKeyTTL bounds a key whose stage has no timeout configured.
const MaxStageKeyTTL = 24 * time.Hour

// StageKeyIssuer mints and revokes the ephemeral MCP credentials a pipeline
// stage run presents to /api/mcp. It is the only writer of
// repo.ApiKeyKindStageRun rows.
type StageKeyIssuer struct {
	Keys repo.ApiKeyRepo
}

// Issue mints a bearer token for stageRunID and returns it. The token is
// returned once and never stored in clear — only its hash reaches the row.
func (i StageKeyIssuer) Issue(ctx context.Context, stageRunID string, stageTimeout time.Duration) (string, error) {
	if i.Keys == nil {
		return "", fmt.Errorf("mcp: StageKeyIssuer has no key repo")
	}
	if stageRunID == "" {
		return "", fmt.Errorf("mcp: refusing to issue a key with no stage run — it would be unattributable and unrevocable")
	}
	token := GenerateAPIToken()
	ttl := stageTimeout + StageKeyTTLBuffer
	if stageTimeout <= 0 {
		ttl = MaxStageKeyTTL
	}
	expires := time.Now().Add(ttl)
	if _, err := i.Keys.Create(ctx, repo.CreateApiKeyInput{
		Name:       "stage-run " + stageRunID,
		Hash:       HashToken(token),
		Scopes:     StageRunScopes,
		Kind:       repo.ApiKeyKindStageRun,
		StageRunID: stageRunID,
		ExpiresAt:  &expires,
	}); err != nil {
		return "", fmt.Errorf("mcp: issue stage-run key: %w", err)
	}
	return token, nil
}

// Revoke deactivates every key issued for stageRunID. A stage run that never
// received a key is not an error — the orchestrator calls this on every
// terminal transition, including spawns that ran without a credential.
func (i StageKeyIssuer) Revoke(ctx context.Context, stageRunID string) error {
	if i.Keys == nil || stageRunID == "" {
		return nil
	}
	if _, err := i.Keys.RevokeForStageRun(ctx, stageRunID); err != nil {
		return fmt.Errorf("mcp: revoke stage-run keys: %w", err)
	}
	return nil
}
