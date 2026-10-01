package agents

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// tmuxPaneRE matches a tmux pane id (e.g. "%3"). The bridge records $TMUX_PANE,
// which is always of this form; restricting to it keeps the value safe to pass
// as a `tmux send-keys -t` target.
var tmuxPaneRE = regexp.MustCompile(`^%\d+$`)

// tmuxRunner executes a tmux command; indirected for tests.
var tmuxRunner = func(ctx context.Context, args ...string) error {
	return exec.CommandContext(ctx, "tmux", args...).Run()
}

// tmuxStdinRunner executes a tmux command with stdin piped; indirected for tests.
var tmuxStdinRunner = func(ctx context.Context, stdin io.Reader, args ...string) error {
	cmd := exec.CommandContext(ctx, "tmux", args...)
	cmd.Stdin = stdin
	return cmd.Run()
}

// validTmuxPane reports whether pane is a well-formed tmux pane id.
func validTmuxPane(pane string) bool {
	return tmuxPaneRE.MatchString(pane)
}

// tmuxSendArgs builds the two tmux arg vectors that inject message as real
// keyboard input into the pane: the literal text, then a separate Enter.
// socket may be "" (default tmux server). The message is passed literally (-l)
// after "--" so it is never interpreted as options or key names.
func tmuxSendArgs(socket, pane, message string) (textArgs, enterArgs []string) {
	var base []string
	if socket != "" {
		base = []string{"-S", socket}
	}
	textArgs = append(append([]string{}, base...), "send-keys", "-t", pane, "-l", "--", message)
	enterArgs = append(append([]string{}, base...), "send-keys", "-t", pane, "Enter")
	return textArgs, enterArgs
}

// tmuxLookPath resolves the tmux binary; indirected for tests.
var tmuxLookPath = func() (string, error) { return exec.LookPath("tmux") }

// tmuxBufferName returns a per-pane buffer name for load-buffer/paste-buffer,
// so concurrent injections to different panes don't collide.
func tmuxBufferName(pane string) string {
	return "kontor-" + pane
}

// sendKeysToTmux injects message into the given tmux pane using bracketed
// paste (load-buffer + paste-buffer), followed by Enter. Bracketed paste
// delivers the entire payload atomically — unlike send-keys -l, which splits
// long content across multiple events and causes Claude Code to see them as
// separate inputs.
func sendKeysToTmux(ctx context.Context, socket, pane, message string) error {
	if !validTmuxPane(pane) {
		return fmt.Errorf("invalid tmux pane %q", pane)
	}
	if strings.ContainsAny(socket, "\n\x00") {
		return fmt.Errorf("invalid tmux socket")
	}
	if _, err := tmuxLookPath(); err != nil {
		return fmt.Errorf("tmux is required for live prompt injection but was not found on the server PATH; install tmux or send will resume the session instead")
	}

	var base []string
	if socket != "" {
		base = []string{"-S", socket}
	}
	bufName := tmuxBufferName(pane)

	// Step 1: load the message into a named tmux buffer via stdin.
	// "-b <name>" names the buffer; "-" reads from stdin.
	loadArgs := append(append([]string{}, base...), "load-buffer", "-b", bufName, "-")
	if err := tmuxStdinRunner(ctx, strings.NewReader(message), loadArgs...); err != nil {
		return fmt.Errorf("tmux load-buffer: %w", err)
	}

	// Step 2: paste the buffer into the pane. -p = bracketed paste mode,
	// -d = delete the buffer after pasting (cleanup).
	pasteArgs := append(append([]string{}, base...), "paste-buffer", "-p", "-d", "-b", bufName, "-t", pane)
	if err := tmuxRunner(ctx, pasteArgs...); err != nil {
		return fmt.Errorf("tmux paste-buffer: %w", err)
	}

	// Step 3: send Enter to submit the pasted prompt.
	enterArgs := append(append([]string{}, base...), "send-keys", "-t", pane, "Enter")
	if err := tmuxRunner(ctx, enterArgs...); err != nil {
		return fmt.Errorf("tmux send-keys (enter): %w", err)
	}
	return nil
}

// tmuxKeyName maps a raw answer-keystroke token to the tmux send-keys named
// key it represents, or "" if the token is literal text to send with -l.
func tmuxKeyName(token string) string {
	switch token {
	case "\t":
		return "Tab"
	case "\r":
		return "Enter"
	default:
		return ""
	}
}

// tmuxSendRawArgs builds the tmux send-keys arg vector for a single raw
// answer-keystroke token: a named key (Tab/Enter) or literal text (digits,
// free-form text) sent via -l after "--" so it is never parsed as an option.
func tmuxSendRawArgs(socket, pane, token string) []string {
	var base []string
	if socket != "" {
		base = []string{"-S", socket}
	}
	if name := tmuxKeyName(token); name != "" {
		return append(append([]string{}, base...), "send-keys", "-t", pane, name)
	}
	return append(append([]string{}, base...), "send-keys", "-t", pane, "-l", "--", token)
}

// sendAnswerKeysToTmux delivers a sequence of raw answer-keystroke tokens
// (digits, literal text, "\t", "\r" — the output of EncodeAnswer) to a tmux
// pane as separate send-keys invocations, one per token, translating "\t" and
// "\r" to the named tmux keys Tab and Enter.
func sendAnswerKeysToTmux(ctx context.Context, socket, pane string, tokens []string) error {
	if !validTmuxPane(pane) {
		return fmt.Errorf("invalid tmux pane %q", pane)
	}
	if strings.ContainsAny(socket, "\n\x00") {
		return fmt.Errorf("invalid tmux socket")
	}
	if _, err := tmuxLookPath(); err != nil {
		return fmt.Errorf("tmux is required for answer delivery but was not found on the server PATH")
	}
	for _, tok := range tokens {
		if err := tmuxRunner(ctx, tmuxSendRawArgs(socket, pane, tok)...); err != nil {
			return fmt.Errorf("tmux send-keys: %w", err)
		}
	}
	return nil
}
