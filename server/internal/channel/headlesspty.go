package channel

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

// RunHeadlessPTY runs `name args…` on a pseudo-terminal the dashboard owns (no
// controlling terminal to proxy), serving the same loopback-token injection HTTP
// and {pid}.pty.json discovery as RunPTY so the dashboard's existing /message
// delivery works. Output is drained (the agent has no human-facing terminal).
// onPid, when non-nil, is called once with the child's PID. Returns when the
// child exits or ctx is cancelled.
func RunHeadlessPTY(ctx context.Context, name string, args, env []string, cwd string, onPid func(int)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("headlesspty: start: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	childPid := cmd.Process.Pid
	if onPid != nil {
		onPid(childPid)
	}

	initialToken, err := generateToken()
	if err != nil {
		return fmt.Errorf("headlesspty: token: %w", err)
	}
	token := newRotatingToken(initialToken)
	hub := newPtyHub(256 * 1024)
	srv, port, err := startPtyHTTPServer(newPtyWriter(ptmx), hub, token)
	if err != nil {
		return fmt.Errorf("headlesspty: http: %w", err)
	}
	disc := newPtyDiscovery(childPid, port)
	if _, derr := disc.write(token.value()); derr != nil {
		slog.Warn("headlesspty: discovery write failed", "err", derr)
	}
	go startTokenRotation(ctx, token, injectTokenRotateInterval(), func(newToken string) error {
		_, werr := disc.write(newToken)
		return werr
	})
	go disc.refreshOnOutput(ctx, token)
	defer func() {
		disc.remove()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	// Drain output so the child never blocks on a full pty buffer, recording the
	// time of each chunk so the dashboard can tell the agent is generating.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				_, _ = disc.Write(buf[:n])
				_, _ = hub.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	return cmd.Wait()
}
