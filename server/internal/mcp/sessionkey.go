package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// KontorSessionScopes is every scope a tool needs, minus keys:manage: a
// session able to mint keys could mint one that outlives it.
func KontorSessionScopes() []string {
	set := map[string]bool{}
	for _, scope := range ToolScopeMap {
		if scope != "keys:manage" {
			set[scope] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// KontorSessionAllowedTools pre-approves the read tools. Every other Kontor
// tool is left to the session's auto mode.
func KontorSessionAllowedTools() []string {
	var tools []string
	for tool, scope := range ToolScopeMap {
		if strings.HasSuffix(scope, ":read") {
			tools = append(tools, "mcp__"+ServerName+"__"+tool)
		}
	}
	slices.Sort(tools)
	return tools
}

// KontorSessionKeyIssuer is the only writer of repo.ApiKeyKindKontorSession rows.
type KontorSessionKeyIssuer struct {
	Keys repo.ApiKeyRepo
}

func (i KontorSessionKeyIssuer) Issue(ctx context.Context) (string, error) {
	token := GenerateAPIToken()
	if _, err := i.Keys.Create(ctx, repo.CreateApiKeyInput{
		Name:   "kontor-session",
		Hash:   HashToken(token),
		Scopes: KontorSessionScopes(),
		Kind:   repo.ApiKeyKindKontorSession,
	}); err != nil {
		return "", fmt.Errorf("mcp: issue kontor-session key: %w", err)
	}
	return token, nil
}

func (i KontorSessionKeyIssuer) Attach(ctx context.Context, pid int) error {
	k, err := i.Keys.ActiveKontorSession(ctx)
	if err != nil {
		return fmt.Errorf("mcp: attach kontor-session pid: %w", err)
	}
	if k == nil {
		return errors.New("mcp: no active kontor-session key to attach a pid to")
	}
	return i.Keys.SetSessionPID(ctx, k.ID, pid)
}

// Current reports the active session's pid. ok is false when no session key
// is active; pid 0 with ok true means the session key exists but has not been
// attached to a process yet.
func (i KontorSessionKeyIssuer) Current(ctx context.Context) (pid int, ok bool, err error) {
	k, err := i.Keys.ActiveKontorSession(ctx)
	if err != nil || k == nil {
		return 0, false, err
	}
	if k.SessionPid == nil {
		return 0, true, nil
	}
	return *k.SessionPid, true, nil
}

func (i KontorSessionKeyIssuer) Revoke(ctx context.Context) error {
	if _, err := i.Keys.RevokeKontorSessions(ctx); err != nil {
		return fmt.Errorf("mcp: revoke kontor-session key: %w", err)
	}
	return nil
}
