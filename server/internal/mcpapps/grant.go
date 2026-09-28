package mcpapps

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// EnsureGrant creates the grant unless a live, unlimited grant with the same
// capability, pattern, mode and context already exists.
func EnsureGrant(ctx context.Context, grants repo.GrantRepo, in repo.CreateGrantInput) (created bool, err error) {
	rows, err := grants.ListForCapability(ctx, in.CapabilityName)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.RevokedAt == nil && row.ExpiresAt == nil && row.Pattern == in.Pattern && row.LimitCount == 0 &&
			row.Mode == in.Mode && row.ContextKind == in.Context.Kind && row.ContextRef == in.Context.Ref {
			return false, nil
		}
	}
	if _, err := grants.Create(ctx, in); err != nil {
		return false, err
	}
	return true, nil
}
