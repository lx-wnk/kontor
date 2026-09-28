package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Register adds the dashboard client subcommands to root.
func Register(root *cobra.Command) {
	root.AddCommand(
		withConfig(newAgentsCmd),
		withConfig(newTasksCmd),
		withConfig(newPipelineCmd),
		withConfig(newConfigCmd),
		newSettingsCmd(),
		newPluginsCmd(),
		newModuleCmd(),
		newGrantsCmd(),
	)
}

// withConfig attaches the --url/--token flags and the config-loading
// PersistentPreRunE to a single command instead of to root: cobra runs only the
// nearest ancestor's PersistentPreRunE, so `serve` and the other server
// subcommands must not inherit it — an unreadable config.json would otherwise
// block starting the server. The direct-DB commands (settings, plugins, grants)
// need no config at all.
func withConfig(build func(*CLIConfig) *cobra.Command) *cobra.Command {
	cfg := &CLIConfig{}
	cmd := build(cfg)

	var flagURL, flagToken string
	cmd.PersistentFlags().StringVar(&flagURL, "url", "", "Dashboard server URL (overrides config)")
	cmd.PersistentFlags().StringVar(&flagToken, "token", "", "API token (overrides config)")

	cmd.PersistentPreRunE = func(_ *cobra.Command, _ []string) error {
		loaded, err := loadConfig()
		if err != nil {
			return fmt.Errorf("config error: %w", err)
		}
		*cfg = loaded
		if cmd.PersistentFlags().Changed("url") {
			cfg.Host = flagURL
		}
		if cmd.PersistentFlags().Changed("token") {
			cfg.Token = flagToken
		}
		return nil
	}
	return cmd
}
