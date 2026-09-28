//go:build darwin

// Command desktop is the macOS wails shell for the Agent Dashboard. It starts
// the dashboard HTTP server in-process on the configured loopback address and
// opens a webview that redirects to it, so the page runs on the loopback-http
// origin the server's same-origin mutation guard accepts.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"

	"github.com/lx-wnk/kontor/server/serverapp"
)

//go:embed bootstrap/index.html
var bootstrapPage string

// dashboardURLPlaceholder is what the bootstrap page carries instead of a
// hardcoded address; it is replaced with the address the server actually bound.
const dashboardURLPlaceholder = "__DASHBOARD_URL__"

// drainTimeout is a last-resort backstop against an unquittable app, not a
// drain budget: it must stay above the server's own shutdown.timeoutSeconds
// (default 10s, user-configurable) so the server's own graceful path always
// gets to finish first. If shutdown.timeoutSeconds is ever raised above this
// value, the app can again exit mid-drain before the server closes cleanly.
const drainTimeout = 20 * time.Second

func main() {
	// A re-executed subcommand (pty-host, channel) must never boot a second
	// server or contend for the dashboard address — dispatch and return before
	// anything else starts.
	if handled, err := serverapp.DispatchHeadless(context.Background(), os.Args[1:]); handled {
		if err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	// Binding comes first and the address is held from here on. A probe that
	// released it would leave the whole config/plugin/database startup window
	// open, so two launches could both pass it and the loser would then adopt
	// the winner's server — showing that instance's frontend in a window built
	// from this one's binary.
	instance, err := serverapp.Listen("")
	if err != nil {
		fatalf("cannot start the Agent Dashboard — if one is already running, quit it first: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// The dashboard server drains when ctx is cancelled.
	serverErr := make(chan error, 1)
	go func() { serverErr <- instance.Serve(ctx) }()

	// The bootstrap page redirects to the server, so the window must not open
	// until the server accepts requests.
	healthURL := "http://" + instance.Addr() + "/api/system/health"
	if err := waitForServer(ctx, serverErr, healthURL, 30*time.Second); err != nil {
		cancel()
		fatalf("dashboard server did not become ready: %v", err)
	}

	// Only the `dev` build tag (set by `wails dev`) redirects elsewhere; a
	// production build waits on nothing extra and targets instance.Addr()
	// exactly as before the dev-desktop redirect existed.
	if err := waitForFrontend(ctx, serverErr); err != nil {
		cancel()
		fatalf("vite dev server not reachable — run `task dev:desktop`, which starts it for you: %v", err)
	}

	err = wails.Run(&options.App{
		Title:  "Agent Dashboard",
		Width:  1400,
		Height: 900,
		AssetServer: &assetserver.Options{
			Handler: bootstrapHandler("http://" + frontendAddr(instance.Addr()) + "/?shell=desktop"),
		},
		Mac: &mac.Options{},
		OnShutdown: func(_ context.Context) {
			cancel() // ask the in-process server to drain
			// A handler that ignores its context would otherwise make the app
			// unquittable, so the drain wait is bounded.
			select {
			case <-serverErr:
			case <-time.After(drainTimeout):
				log.Printf("dashboard server did not drain within %s, exiting anyway", drainTimeout)
			}
		},
	})
	if err != nil {
		cancel()
		fatalf("wails run: %v", err)
	}
}

// fatalf reports a startup failure and exits 1. A .app launched from Finder has
// no terminal attached, so stderr alone would make a refused start look like the
// app doing nothing at all: the message is also shown as a dialog.
func fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)
	if err := exec.Command("osascript", alertArgs(msg)...).Run(); err != nil {
		log.Printf("could not show the alert dialog: %v", err)
	}
	os.Exit(1)
}

// alertArgs builds the osascript invocation for a critical alert. The message is
// passed as an argument rather than spliced into the script, so quotes and
// backslashes in it cannot change the script being run.
func alertArgs(msg string) []string {
	return []string{
		"-e", "on run argv",
		"-e", "display alert (item 1 of argv) message (item 2 of argv) as critical",
		"-e", "end run",
		"Agent Dashboard", msg,
	}
}

// bootstrapHandler serves the redirect page for every webview request, pointed
// at the address the server is really listening on.
func bootstrapHandler(dashboardURL string) http.Handler {
	page := strings.ReplaceAll(bootstrapPage, dashboardURLPlaceholder, dashboardURL)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, page)
	})
}

var healthPollClient = &http.Client{Timeout: 2 * time.Second}

// waitForServer polls url until it returns 200, the timeout/ctx elapses, or
// serverErr delivers a startup failure.
//
// A 200 only proves something serves this address, not that it is ours. That is
// safe at the call sites that poll the in-process server, because
// serverapp.Listen already holds the address: nothing else can be answering on
// it. A caller polling an address this process does NOT hold must prove
// ownership some other way -- see waitForServerFunc.
func waitForServer(ctx context.Context, serverErr <-chan error, url string, timeout time.Duration) error {
	return waitForServerFunc(ctx, serverErr, url, timeout, nil)
}

// waitForServerFunc is waitForServer with an extra acceptance test on the
// response, so a caller can require evidence that the peer is the process it
// expects rather than whatever happens to hold the port. A nil accept means
// "200 is enough".
func waitForServerFunc(ctx context.Context, serverErr <-chan error, url string, timeout time.Duration, accept func(*http.Response) bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-serverErr:
			return fmt.Errorf("server failed to start: %w", err)
		default:
		}
		resp, err := healthPollClient.Get(url)
		if err == nil {
			ok := resp.StatusCode == http.StatusOK && (accept == nil || accept(resp))
			resp.Body.Close()
			if ok {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s waiting for %s", timeout, url)
}
