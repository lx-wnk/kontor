package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// newPluginsCmd is the direct-DB plugin control group — the offline lockout
// hatch. It mutates plugin.active without HTTP, so a broken auth_provider plugin
// that prevents boot can be disabled while the server is down. Lifecycle hooks
// are NOT run (they need a live server); the change applies on next boot.
func newPluginsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plugins", Short: "Enable/disable plugins directly in the DB (offline lockout hatch)"}
	cmd.PersistentFlags().String("db", "", "Path to the dashboard SQLite DB (default: $DASHBOARD_DB_PATH or ~/.claude/dashboard-tasks.db)")

	list := &cobra.Command{Use: "list", Short: "List plugins with their active state", RunE: func(cmd *cobra.Command, _ []string) error {
		return withPluginRepo(cmd, func(ctx context.Context, pr repo.PluginRepo) error {
			rows, err := pr.List(ctx)
			if err != nil {
				return err
			}
			for _, p := range rows {
				installed := p.InstalledAt != nil
				fmt.Printf("%-24s active=%-5v installed=%v\n", p.ID, p.Active, installed)
			}
			return nil
		})
	}}

	disable := &cobra.Command{Use: "disable <id>", Short: "Disable a plugin (sets active=false)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return setPluginActive(cmd, args[0], false)
	}}

	enable := &cobra.Command{Use: "enable <id>", Short: "Enable a plugin (sets active=true; hooks skipped, applies on next boot)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return setPluginActive(cmd, args[0], true)
	}}

	cmd.AddCommand(list, disable, enable)
	return cmd
}

// setPluginActive flips active for an existing plugin row; an unknown id errors
// (enabling a never-discovered plugin is almost always a typo, and the row would
// lack path/manifest — run discovery via the server instead).
func setPluginActive(cmd *cobra.Command, id string, active bool) error {
	return withPluginRepo(cmd, func(ctx context.Context, pr repo.PluginRepo) error {
		if _, err := pr.Get(ctx, id); err != nil {
			if repo.IsNotFound(err) {
				return fmt.Errorf("unknown plugin %q", id)
			}
			return err
		}
		if err := pr.SetActive(ctx, id, active); err != nil {
			return err
		}
		verb := "disabled"
		if active {
			verb = "enabled"
		}
		fmt.Printf("%s %s — restart the server to apply\n", verb, id)
		return nil
	})
}

// withPluginRepo opens the DB (reusing the settings hatch's opener), builds a
// PluginRepo, runs fn, and closes.
func withPluginRepo(cmd *cobra.Command, fn func(ctx context.Context, pr repo.PluginRepo) error) error {
	path, err := resolveDBPath(cmd)
	if err != nil {
		return err
	}
	store, err := openDBStore(path)
	if err != nil {
		return fmt.Errorf("open db %s: %w", path, err)
	}
	defer func() { _ = store.Close() }()
	return fn(cmd.Context(), repo.NewPluginRepo(store.client))
}
