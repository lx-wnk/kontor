package serverapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

// buildObsidianClient returns nil, nil when Obsidian is unconfigured: the
// application is optional, and an absent vault must leave the rest of the
// server running. baseURL, vaultRoot and apiKey are a required trio — if any
// one of them is set, all three must be, because a client built from a
// partial trio would look configured and then fail every request (a 401
// pointing at nothing) instead of refusing to boot. tlsMode is not part of
// the trio: it always has a registry default. obsidian.NewClient validates
// baseURL, vaultRoot and tlsMode, but never apiKey — an empty bearer token
// is valid HTTP — so the trio check below is this function's job, not
// NewClient's.
func buildObsidianClient(ctx context.Context, settingsSvc *settings.Service) (*obsidian.Client, error) {
	baseURL := settingsSvc.String("obsidian.baseURL")
	vaultRoot := settingsSvc.String("obsidian.vaultRoot")
	apiKey, err := settingsSvc.Secret(ctx, "obsidian.apiKey")
	if err != nil {
		return nil, fmt.Errorf("read obsidian.apiKey: %w", err)
	}

	if baseURL == "" && vaultRoot == "" && apiKey == "" {
		slog.Info("obsidian: vault not configured, integration disabled")
		return nil, nil
	}

	var missing []string
	if baseURL == "" {
		missing = append(missing, "obsidian.baseURL")
	}
	if vaultRoot == "" {
		missing = append(missing, "obsidian.vaultRoot")
	}
	if apiKey == "" {
		missing = append(missing, "obsidian.apiKey")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}

	client, err := obsidian.NewClient(obsidian.Config{
		BaseURL:   baseURL,
		APIKey:    apiKey,
		VaultRoot: vaultRoot,
		TLSMode:   settingsSvc.String("obsidian.tlsMode"),
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

// bootObsidianClient builds the vault client for boot, tolerating a partial
// trio the same way buildGitHubClient's caller (di.go) tolerates a broken
// GitHub configuration: log at Warn and start with the vault off instead of
// failing the whole server. A read-only integration does not get to take the
// server down over a typo in its own settings.
func bootObsidianClient(ctx context.Context, settingsSvc *settings.Service) *obsidian.Client {
	client, err := buildObsidianClient(ctx, settingsSvc)
	if err != nil {
		slog.Warn("obsidian: integration disabled", "err", err)
		return nil
	}
	return client
}

// watchObsidianSettings rebuilds the vault client whenever an obsidian.* setting
// is saved. The Settings panel saves the keys one at a time, so a partial trio
// is a normal intermediate state: it turns the vault off instead of failing the save.
func watchObsidianSettings(settingsSvc *settings.Service, clients *obsidian.ClientHolder, notifier *mcp.Notifier) {
	validateVaultRootOnSave(settingsSvc, clients)
	settingsSvc.OnChange(func(ctx context.Context, key string) {
		if !strings.HasPrefix(key, "obsidian.") {
			return
		}
		client, err := buildObsidianClient(ctx, settingsSvc)
		if err != nil {
			slog.Info("obsidian: vault off until its settings are complete", "err", err)
		}
		clients.Set(client)
		// The tool set changed: a configured vault adds obsidian_* tools, an
		// unconfigured one removes them. Notify SSE subscribers per the MCP spec.
		if notifier != nil {
			notifier.NotifyToolsChanged()
		}
	})
}

// validateVaultRootOnSave rejects a non-existent folder; an unreachable vault is allowed through.
func validateVaultRootOnSave(settingsSvc *settings.Service, clients *obsidian.ClientHolder) {
	settingsSvc.OnPreSave(func(ctx context.Context, key, value string) error {
		if key != "obsidian.vaultRoot" || value == "" {
			return nil
		}
		client := clients.Get()
		if client == nil {
			// Vault not configured yet — can't probe, don't block.
			return nil
		}
		err := client.ProbeFolder(ctx, value)
		if err == nil {
			return nil
		}
		if errors.Is(err, obsidian.ErrNotFound) {
			return fmt.Errorf("folder %q not found in vault", value)
		}
		// Network/TLS error: vault unreachable — don't block the save.
		slog.Warn("obsidian: could not verify vault root", "root", value, "err", err)
		return nil
	})
}

// obsidianSpaceSlug is the fixed slug of the memory space IndexNotes writes
// its pointer entries into. Global scope: the vault is one machine-wide
// resource, not scoped to any single project.
const obsidianSpaceSlug = "obsidian"

// ensureObsidianSpace creates the obsidian memory space's resource row if it
// does not already exist, and returns it either way. Memory spaces are
// never auto-created — repo.MemoryRepo.CreateSpace is always caller-driven —
// so without this call obsidian.IndexNotes' spaceID argument would have
// nothing to resolve on a fresh install. resources.Upsert resolves on
// conflict, the same idempotent shape obsidian.Register (called right
// before this, in di.go) uses for the application's own resource row: a
// second call returns the identical row, id included, never a duplicate.
// Origin is Builtin, not the default Local a human-created space via
// POST /api/memory/spaces gets: this space is server-managed, so it must
// be exempt from ResourceRepo.Delete the same way the Obsidian application
// resource itself is.
func ensureObsidianSpace(ctx context.Context, resources repo.ResourceRepo) (*ent.Resource, error) {
	space, err := resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind:   repo.ResourceKindMemorySpace,
		Slug:   obsidianSpaceSlug,
		Name:   "Obsidian",
		Scope:  repo.GlobalScope(),
		Origin: repo.ResourceOriginBuiltin,
	})
	if err != nil {
		return nil, fmt.Errorf("ensureObsidianSpace: %w", err)
	}
	return space, nil
}
