package main

import (
	"github.com/spf13/cobra"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/serverapp"
)

// newPtyHostCmd builds `agent-dashboard pty-host -- <binary> <args…>`. It runs
// the command on a dashboard-owned pty (headless), serving live injection, and
// prints the child PID as the first stdout line so the spawner can capture it.
func newPtyHostCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "pty-host [-- command args...]",
		Short:              "Run a command on a dashboard-owned pty for headless live injection",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := serverapp.DispatchHeadless(cmd.Context(), append([]string{channelconfig.SubcommandPtyHost}, args...))
			return err
		},
	}
}
