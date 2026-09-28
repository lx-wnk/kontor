# Contributing to kontor

Real-time monitoring dashboard for locally running Claude Code agents. Go 1.26 backend, Vue 3 + TypeScript frontend.

## Prerequisites

| Tool | Install |
|------|---------|
| Go 1.26+ | `brew install go` |
| Task runner | `brew install go-task/tap/go-task` |
| air (hot-reload) | `go install github.com/air-verse/air@latest` |
| Node.js 22+ + pnpm | [pnpm.io/installation](https://pnpm.io/installation) |
| GitHub CLI (`gh`, logged in) — optional | `brew install gh && gh auth login` |

`gh` is only needed when pipeline tasks may push (`git.allowPush`): finalization then opens a draft PR through it. Without it the task still reaches done and records the error as `pr_error` in its metadata.

**Platform:** macOS and Linux. Windows is unsupported.

Building the macOS desktop shell (`desktop/`) additionally requires Xcode command-line tools (`xcode-select --install`). The `wails` CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`) is needed for `task dev:desktop` (hot-reload) and for `.app`/`.dmg` bundling; a plain `task build:desktop` does not need it. See [Desktop shell (macOS)](#desktop-shell-macos) below.

## Setup

```bash
git clone https://github.com/lx-wnk/kontor.git
cd Kontor
pnpm install        # frontend dependencies only
```

Go dependencies are fetched automatically on first build. The project uses a Go workspace (`go.work`) with two modules: `./sdk` and `./server`.

## Running the dev server

```bash
task dev
```

Starts the Go backend via `air` (hot-reload on `.go` file changes) and serves the Vue SPA, both on port 13120.

To re-trigger the first-run onboarding flow (e.g. to test it) once you've already completed or skipped it, reset its setting: `kontor settings set onboarding.completed false`. The CLI writes straight to the database and a running server reads its settings once at startup, so restart the server afterwards — otherwise the command reports success and the flow stays hidden.

## Production build

```bash
pnpm build          # build the Vue SPA (embedded into the binary via go:embed)
task build          # compile → bin/kontor
DASHBOARD_JWT_SECRET=<32+ chars> ./bin/agent-dashboard serve
```

Build the SPA **before** the binary — `go:embed` bakes the compiled frontend into `bin/kontor`, so the result is self-contained (no Node.js at runtime). Deploy by copying the binary. End users do not build from source; see [docs/guides/install.md](docs/guides/install.md) for binary/Homebrew/Docker installs.

## Commands

### Backend (Go)

| Command | Description |
|---------|-------------|
| `task dev` | Start with air hot-reload |
| `task build` | Compile production binary → `bin/kontor` |
| `task build:all` | Build the SPA and embed it into the server binary |
| `task build:everything` | `build:all` plus, on macOS, the desktop shell |
| `task dev:desktop` | macOS desktop shell with wails hot-reload (needs the wails CLI) |
| `task test` | Run all tests with race detector |
| `task lint` | Run golangci-lint and the formatter check (sdk, server, every plugin) |
| `task generate` | Run ent schema + tygo TS code generation |
| `task fmt` | Format with the same formatter `task lint` checks (sdk, server, plugins) |
| `task changelog:check` | Fail on a repeated release heading or `###` heading within one `CHANGELOG.md` release section |

### Frontend (Vue)

| Command | Description |
|---------|-------------|
| `pnpm build` | Build Vue SPA for production |
| `pnpm test` | Vitest unit tests |
| `pnpm test:e2e` | Playwright end-to-end tests (starts its own server on `:13199`, against a scratch DB under `.e2e/`; also runs in CI as **E2E (Playwright)**) |
| `pnpm typecheck` | vue-tsc type checking |

`playwright.config.ts` starts the E2E server with `KONTOR_RATE_LIMIT_RPS`, which raises the per-IP sustained request rate the server allows (the F-SEC-010 control): the suite drives more requests per second than a human ever will, and on loopback every client shares one bucket. It exists for that suite — unset, which is every other run and every release build, the production default is unchanged — and `tests/e2e/cold-start-rate-limit.spec.ts` deliberately drives a second E2E server without it, so the real limit stays under test.

## Architecture

```
server/cmd/serve/main.go          Go entrypoint (cobra CLI + hand-written DI)
server/internal/api/router.go     Chi router and route registration
server/internal/pipeline/         Task pipeline state machine
server/internal/db/ent/schema/    Ent ORM schemas
sdk/                              dashboard-channel MCP stdio binary (Go module)
src/                              Vue 3 TypeScript SPA
plugins/anthropic-spawner/        Out-of-process Anthropic Messages API binary (own go.mod)
```

### Out-of-process spawner pattern

`plugins/anthropic-spawner/` is its own Go module, built with `GOWORK=off` so that `anthropic-sdk-go` is never imported by the server. The server invokes it through the standard custom-exec contract (stdin: `LLMSpawnArgs` JSON; stdout: `LLMSpawnResult` JSON). A `Stream` flag on `LLMSpawnArgs` selects between a single-shot response (`Spawn`) and server-sent events (`SpawnStream`). New out-of-process spawners follow the same contract.

The server binds exclusively to `127.0.0.1`. Never change this — the server reads sensitive Claude session data.

Frontend path alias: `@/*` maps to `./src/*`.

### Adding a provider

For a CLI that writes file-per-session JSONL, add a descriptor YAML under `server/internal/provider/providers/` (or ship one via `DASHBOARD_PROVIDER_DIR`) — no Go code is needed. The descriptor declares the exe names, config dir, session glob, token/model/cost field-paths, and the token aggregation mode (`cumulative` or `perMessage`).

IDE-embedded tools (Cursor, Copilot-in-VSCode, Windsurf) don't write file-per-session JSONL, so they need a Go `Adapter` (source `custom:<id>`) instead — not yet implemented.

### Desktop shell (macOS)

`desktop/` is its own Go module (wails v2) that wraps the dashboard server in a native WKWebView window. It is `//go:build darwin`-gated — the module does not build on other platforms; a `main_other.go` stub keeps `go build ./...` and CI green off macOS. Build and run it for a dev smoke (requires Go 1.26 + Xcode command-line tools, no `wails` CLI needed):

```bash
task desktop:run        # or: task build:desktop  (writes bin/kontor-desktop)
```

Three non-obvious build requirements the `desktop:*` tasks encapsulate — a bare `cd desktop && go build .` runs but produces a broken app:

- **`-tags production`** — without a `production` or `dev` build tag, wails compiles a fallback that errors at runtime (`Wails applications will not build without the correct build tags`).
- **`-ldflags "-extldflags '-framework UniformTypeIdentifiers'"`** — wails v2 references `UTType` on the macOS 15 SDK; a plain `go build` fails to link it (`Undefined symbols: _OBJC_CLASS_$_UTType`).
- **the SPA must be built first** (`task build:frontend`) — the shell starts the dashboard server in-process, and that server (not the shell) embeds and serves `server/frontend/dist` via `go:embed`; an empty dist makes the webview hit a `/` → `./` redirect loop instead of the app.

The `wails` CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`) is needed for `task dev:desktop` (wails hot-reload) and for producing a `.app`/`.dmg` bundle (`task desktop:dist` / `task desktop:dmg`); the plain `task build:desktop` above does not need it. See [docs/desktop-distribution.md](docs/desktop-distribution.md) for packaging plus the full signing and notarization steps. For day-to-day rebuilds, `task desktop:bundle` swaps a fresh binary into that `.app` and signs it with a stable identity so macOS privacy grants survive — setup in [docs/code-signing.md](docs/code-signing.md).

### CI matrices and the Go toolchain

Two single sources feed CI, so neither has to be edited per module:

- **`.go-version`** pins the toolchain for every `setup-go` step in `ci.yml` and `release.yml`. A Go patch bump is a one-line change here, and nothing else moves. `.go-version` is also read by `goenv`/`asdf` if you use one locally.

  The `go` directives in the individual `go.mod` files stay at a plain `go 1.26`: they are *minimum language requirements*, not the build pin. Raise one only when a module genuinely starts requiring a newer language version — never for a security patch. `govulncheck` evaluates the standard library of the toolchain in use, not the directive, so `.go-version` is what governs stdlib CVEs.

  Do not delete a `go` directive to "clean up": without one the language level falls back to 1.16, the stricter `go.sum` rules no longer apply and the build breaks — and the next `go mod tidy` writes the directive back with the full patch version of whatever toolchain you happen to run.

  Watch for a `toolchain` line appearing in a `go.mod` after a `go get`: the go command adds one whenever it raises the `go` version. That would reintroduce a second, competing toolchain pin next to `.go-version`. Drop it and keep the pin in one place.

  Both rules are enforced: the `Matrix` job's **Toolchain consistency** step fails the build if any `go.mod` (or `go.work`) drifts from the minor version in `.go-version`, or if a `toolchain` directive shows up. Go itself offers no way to inherit the directive — `go.work`'s own `go` line only governs how `go.work` is parsed, and a module has to stand alone for consumers who have no workspace — so consistency is checked rather than derived.
- **The `matrix` job** at the top of `ci.yml` holds the module lists — `WORKSPACE_MODULES` and `PLUGINS` — and every other job's matrix derives from its outputs. **Adding a plugin means adding it to `PLUGINS`, and nothing else**: the test, lint, security and build jobs all follow. The security list is computed from the other two, so it can no longer drift out of sync (it once did, and `plugins/oauthkit` went unscanned as a result).

  These lists cannot be workflow-level `env:` variables: GitHub does not expose the `env` context to `strategy.matrix` (allowed there: `github`, `needs`, `vars`, `inputs`), so they have to travel as job outputs.

  The build job skips any plugin without a `package main` — a shared library such as `plugins/oauthkit` is tested, linted and vulnerability-scanned, but has no binary to build.

## Pull Request Process

1. Branch from `main`.
2. Use feature branch naming: `feat/<short-description>`.
3. Before submitting, verify:
   - `task test` passes (race detector included)
   - `task lint` passes
   - `pnpm typecheck` passes
   - `task changelog:check` passes — add `CHANGELOG.md` entries to the existing `### Added` / `### Changed` / `### Fixed` subsections under `[Unreleased]`, never a second copy of a heading
4. Write a clear PR description explaining what changed and why.

## Commit Convention

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add worktree cleanup on task cancel
fix: correct SSE reconnect delay after server restart
refactor: extract cost estimation into shared util
```

All commit messages must be written in English.

## Code Guidelines

- Keep functions small and focused on a single responsibility.
- Every constant or validation rule used in more than one place lives in exactly one canonical location — do not duplicate.
- Server must never bind to `0.0.0.0`.
- New side effects (metrics, notifications, new channels) → add optional callback to the relevant Options struct, wire in `server/cmd/serve/`. Do not import side-effect modules from inside `server/internal/pipeline/`.

## Reporting Issues

Use [GitHub Issues](https://github.com/lx-wnk/kontor/issues/new/choose) to report bugs or request features.
