//go:build !darwin

// The desktop shell wraps a macOS WKWebView; it is not built on other
// platforms. Non-macOS users run the server directly via `agent-dashboard
// serve`. This stub keeps the package compilable everywhere so `go build
// ./...` and CI stay green off darwin.
package main

import (
	"context"
	"log"
	"os"

	"github.com/lx-wnk/kontor/server/serverapp"
)

func main() {
	// A re-executed subcommand (pty-host, channel) must still work off
	// darwin so it does not silently fall through to the GUI-unsupported
	// fatal message below.
	if handled, err := serverapp.DispatchHeadless(context.Background(), os.Args[1:]); handled {
		if err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	log.Fatal("the Agent Dashboard desktop shell is only supported on macOS; run `agent-dashboard serve` instead")
}
