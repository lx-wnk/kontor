// Package version holds the build identity of the running binary.
//
// It exists because the dashboard ships through three build paths — the `serve`
// CLI, the macOS desktop shell, and goreleaser — and until a version was
// reachable at runtime there was no way to tell which build a window belonged
// to. A stale desktop bundle beside a fresh CLI binary looks identical.
package version

// Version is stamped at build time with
// -ldflags "-X github.com/lx-wnk/kontor/server/internal/version.Version=<tag>".
// "dev" means an unstamped local build, which is a useful answer in itself.
var Version = "dev"

// Unstamped is the value both this package and main.version carry when no
// -ldflags stamp was applied.
const Unstamped = "dev"

// Set records a version discovered elsewhere (the serve CLI keeps its own
// main.version so the existing goreleaser ldflag stays valid).
//
// An empty or unstamped input is ignored: the two paths stamp different
// symbols, and a caller forwarding its own default would otherwise erase a
// value that was stamped here directly.
func Set(v string) {
	if v == "" || v == Unstamped {
		return
	}
	Version = v
}
