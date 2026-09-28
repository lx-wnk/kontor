package serverapp

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/lx-wnk/kontor/server/internal/channel"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
)

// HeadlessSubcommands returns the subcommand names DispatchHeadless handles.
// The spawner (server/internal/api/agents/spawn.go, server/internal/pipeline/spawner.go)
// re-executes the current binary with these names; any binary that can be
// re-executed — including the desktop GUI shell — must implement them.
func HeadlessSubcommands() []string {
	return []string{channelconfig.SubcommandChannel, channelconfig.SubcommandPtyHost}
}

// DispatchHeadless runs a headless subcommand when args names one. It reports
// handled=false when args do not name a headless subcommand, so a GUI host can
// continue with its normal startup.
//
// args is the argument list without the program name (i.e. os.Args[1:]).
func DispatchHeadless(ctx context.Context, args []string) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}

	switch args[0] {
	case channelconfig.SubcommandChannel:
		return true, runChannel(ctx)
	case channelconfig.SubcommandPtyHost:
		return true, runPtyHost(ctx, args[1:])
	default:
		return false, nil
	}
}

// runChannel runs the dashboard-channel MCP stdio server (invoked by Claude Code).
func runChannel(ctx context.Context) error {
	// Redirect slog to stderr: the channel bridge uses os.Stdin/os.Stdout
	// for MCP stdio transport — any log lines on stdout corrupt the protocol.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	return channel.Run(ctx)
}

// runPtyHost runs a command on a dashboard-owned pty for headless live
// injection, printing the child PID as the first stdout line so the spawner
// can capture it.
func runPtyHost(ctx context.Context, args []string) error {
	// pty-host proxies the child via os.Stdout; keep logs off stdout, including
	// the error this function may return moments from now. slog.SetDefault also
	// reroutes the stdlib log package, at Info level by default — a GUI host
	// reporting a failure via log.Fatalf would be silently dropped by this Warn
	// handler, so the bridge level is pinned to match it.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	})))
	slog.SetLogLoggerLevel(slog.LevelWarn)
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("pty-host: no command given")
	}
	return channel.RunHeadlessPTY(ctx, args[0], args[1:], nil, "", func(pid int) {
		fmt.Println(pid)
	})
}
