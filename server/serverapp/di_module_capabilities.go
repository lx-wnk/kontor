package serverapp

import (
	"context"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// registerModuleToolCapabilities gives every tool a loaded module declares a row
// in the capability catalogue. Without one the gate denies it — correctly, since
// it fails closed — but nobody could grant it either: the catalogue is what the
// grants surface lists. Registering is idempotent, so a module that declares the
// same tools on every boot produces no churn.
func registerModuleToolCapabilities(ctx context.Context, registry *plugin.Registry, caps repo.CapabilityRepo) {
	if registry == nil || caps == nil {
		return
	}
	for _, entry := range registry.All() {
		for _, tool := range entry.Descriptor.Tools {
			name := mcp.ModuleToolScope(entry.Descriptor.ID, tool.Name)
			description := tool.Description
			if description == "" {
				description = "Tool offered by the " + entry.Descriptor.ID + " module."
			}
			if _, err := caps.Upsert(ctx, repo.UpsertCapabilityInput{
				Name:          name,
				Class:         repo.CapClassTool,
				EnforceableBy: []string{capability.EnforcerServer},
				Description:   description,
			}); err != nil {
				slog.Warn("module tool capability not registered", "capability", name, "err", err)
			}
		}
	}
}
