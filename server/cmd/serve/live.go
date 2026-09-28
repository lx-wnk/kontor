package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/lx-wnk/kontor/server/internal/channel"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/spf13/cobra"
)

// transport enumerates the three live-injection strategies `agent-dashboard live` can use.
type transport int

const (
	// transportInsideTmux is selected when the process is already running inside
	// a tmux session ($TMUX is set). Claude inherits the current pane, and tmux
	// send-keys injection works through the channel bridge discovery.
	transportInsideTmux transport = iota
	// transportNewTmux is selected when tmux is available on PATH but we are not
	// inside tmux. A new tmux session is created and attached.
	transportNewTmux
	// transportPTY is the zero-install fallback: the built-in pty broker
	// (agent-dashboard ptyhost) owns the pseudo-terminal.
	transportPTY
)

// selectTransport is a pure function that decides which live-injection transport
// to use. tmuxEnv should be os.Getenv("TMUX"); tmuxPath should be the result of
// exec.LookPath("tmux") (empty string on lookup failure).
//
// The function is extracted as a pure helper so it can be unit-tested without a
// real tmux binary or environment variable.
func selectTransport(tmuxEnv string, tmuxPath string) transport {
	if tmuxEnv != "" {
		return transportInsideTmux
	}
	if tmuxPath != "" {
		return transportNewTmux
	}
	return transportPTY
}

// newLiveCmd builds the `agent-dashboard live` cobra command.
func newLiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "live [-- claude-args...]",
		Short: "Run an interactive Claude session the dashboard can monitor and live-drive",
		Long: `Run an interactive Claude session the dashboard can monitor and live-drive.

The channel MCP is always loaded so the session can report back to the dashboard
(agent→dashboard replies, permission requests). Transport is selected automatically:

  • Inside tmux   — runs directly in the current pane (tmux send-keys injection)
  • tmux on PATH  — creates a new tmux session and attaches
  • Otherwise     — uses the built-in pty broker (no tmux required)

Pass --yolo to skip permission prompts (equivalent to --dangerously-skip-permissions).
All other flags are forwarded to claude unchanged.`,
		// DisableFlagParsing lets all flags pass through to claude uninterpreted.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLive(cmd.Context(), args)
		},
	}
}

// runLive is the implementation of the `live` subcommand.
func runLive(ctx context.Context, args []string) error {
	// Drop a leading "--" separator if cobra passed it through (mirrors ptyhost behaviour).
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}

	binPath, err := channelconfig.SelfBinaryPath()
	if err != nil {
		return fmt.Errorf("live: resolve binary: %w", err)
	}

	mcpJSON, err := channelconfig.ConfigJSON(binPath, nil)
	if err != nil {
		return fmt.Errorf("live: build mcp config: %w", err)
	}

	// Extract --yolo from passthrough args; everything else is forwarded.
	yolo := false
	var passthroughArgs []string
	for _, a := range args {
		if a == "--yolo" {
			yolo = true
		} else {
			passthroughArgs = append(passthroughArgs, a)
		}
	}

	// Build the claude argument list: --mcp-config <json> [--dangerously-skip-permissions] [passthrough...]
	claudeArgs := []string{"--mcp-config", mcpJSON}
	if yolo {
		claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
	}
	claudeArgs = append(claudeArgs, passthroughArgs...)

	// Resolve tmux path; treat lookup error as "no tmux".
	tmuxPath, _ := exec.LookPath("tmux")

	switch selectTransport(os.Getenv("TMUX"), tmuxPath) {
	case transportInsideTmux:
		// Already inside tmux — run claude directly; the channel bridge discovers the pane.
		return execDirect(ctx, "claude", claudeArgs)

	case transportNewTmux:
		// Spin up a new named tmux session. tmux runs the command+args directly (no
		// shell), so arguments containing spaces are preserved.
		sessionName := "claude-live-" + strconv.Itoa(os.Getpid())
		tmuxArgs := []string{"new-session", "-s", sessionName, "claude"}
		tmuxArgs = append(tmuxArgs, claudeArgs...)
		return execDirect(ctx, tmuxPath, tmuxArgs)

	default: // transportPTY
		// tmux-free path: the pty broker owns the pseudo-terminal.
		// Passing --mcp-config here closes the gap where the old claude-channel-pty.sh
		// launched bare claude without the channel MCP.
		command := append([]string{"claude"}, claudeArgs...)
		return channel.RunPTY(ctx, command)
	}
}

// execDirect starts the named binary with args, inheriting stdin/stdout/stderr,
// and blocks until the child exits. The child's exit error is returned faithfully.
func execDirect(ctx context.Context, name string, args []string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
