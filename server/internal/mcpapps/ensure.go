package mcpapps

import (
	"context"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// EnsureResource creates or updates the registry row that anchors an MCP
// application, so a server added in the app and one discovered on disk get the
// identical identity.
func EnsureResource(ctx context.Context, resources repo.ResourceRepo, name string) (*ent.Resource, error) {
	return resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind:      repo.ResourceKindApplication,
		Slug:      ResourceSlug(name),
		Name:      name,
		Scope:     repo.GlobalScope(),
		State:     repo.ResourceStateDiscovered,
		Origin:    repo.ResourceOriginLocal,
		OriginRef: name,
	})
}
