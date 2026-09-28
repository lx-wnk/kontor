package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/lx-wnk/kontor/server/internal/channel"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/cli"
	versionpkg "github.com/lx-wnk/kontor/server/internal/version"
	"github.com/lx-wnk/kontor/server/serverapp"
)

// version is the dashboard version. It defaults to "dev" for local builds and is
// overridden at release time via -ldflags "-X main.version=<tag>" (see .goreleaser.yml).
var version = "dev"

func main() {
	// goreleaser stamps main.version; hand it to the shared package so the
	// health endpoint reports the same string the CLI prints.
	versionpkg.Set(version)

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	var cfgFile string

	root := &cobra.Command{
		Use:     "kontor",
		Short:   "Monitor and orchestrate local coding agents",
		Version: version,
	}

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Start the HTTP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return serverapp.Serve(ctx, cfgFile)
		},
	}
	serve.Flags().StringVar(&cfgFile, "config", "", "path to JSON config file")

	// channel subcommand: runs the dashboard-channel MCP stdio server.
	// Claude Code spawns this when it reads the --mcp-config written by the pipeline spawner.
	channelCmd := &cobra.Command{
		Use:   "channel",
		Short: "Run the dashboard-channel MCP stdio server (invoked by Claude Code)",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := serverapp.DispatchHeadless(cmd.Context(), []string{channelconfig.SubcommandChannel})
			return err
		},
		// Hide from help output — this is an internal subcommand for Claude Code.
		Hidden: true,
	}
	root.AddCommand(channelCmd)

	ptyhostCmd := &cobra.Command{
		Use:   "ptyhost -- <command> [args...]",
		Short: "Run a command under a pty with dashboard live prompt injection (tmux-free)",
		// Pass every argument through to the child unchanged (e.g. claude's own flags).
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// ptyhost proxies the child via os.Stdout; keep logs off stdout.
			slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelWarn,
			})))
			// Drop a leading "--" separator if cobra passed it through.
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			return channel.RunPTY(cmd.Context(), args)
		},
		Hidden: true,
	}
	root.AddCommand(ptyhostCmd)

	root.AddCommand(newLiveCmd(), newPtyHostCmd(), newHooksCmd())
	cli.Register(root)

	root.AddCommand(serve)

	if err := root.Execute(); err != nil {
		// cobra has already printed "Error: <err>" for the command that
		// failed. Repeating it as "startup failed" mislabels every subcommand
		// — nothing was starting up when `module add` rejected a manifest —
		// so the exit code carries the failure and the message does not lie.
		os.Exit(1)
	}
}
