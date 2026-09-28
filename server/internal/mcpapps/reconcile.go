package mcpapps

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// InitialReconcileMarker separates servers that existed before applications
// did — which kept reaching every run — from servers added afterwards, which
// reach a run only when attached. The data cannot tell the two apart: an empty
// table after an install with no servers looks exactly like a first run.
const InitialReconcileMarker = "mcp-applications-initial-reconcile"

type Markers interface {
	Has(ctx context.Context, name string) (bool, error)
	Record(ctx context.Context, name string) error
}

// ResourceSlug prefixes the server name so a server can never take over the
// registry row of a plugin application with the same slug.
func ResourceSlug(serverName string) string { return "mcp-" + serverName }

func Reconcile(ctx context.Context, servers map[string]json.RawMessage, resources repo.ResourceRepo, apps repo.MCPApplicationRepo, markers Markers) (int, error) {
	seen, err := markers.Has(ctx, InitialReconcileMarker)
	if err != nil {
		return 0, fmt.Errorf("mcpapps.Reconcile: %w", err)
	}
	firstRun := !seen

	names := make([]string, 0, len(servers))
	for name := range servers {
		if !channelconfig.IsReservedServerName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	mirrored := 0
	for _, name := range names {
		res, err := EnsureResource(ctx, resources, name)
		if err != nil {
			slog.Warn("mcpapps: server not mirrored", "server", name, "err", err)
			continue
		}
		if _, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{
			ResourceID: res.ID,
			ServerName: name,
			AttachAll:  firstRun,
		}); err != nil {
			slog.Warn("mcpapps: application state not stored", "server", name, "err", err)
			continue
		}
		mirrored++
	}

	if firstRun {
		if err := markers.Record(ctx, InitialReconcileMarker); err != nil {
			return mirrored, fmt.Errorf("mcpapps.Reconcile: %w", err)
		}
	}
	return mirrored, nil
}
