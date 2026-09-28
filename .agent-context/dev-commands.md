# Development Commands

## Go / Backend (via Taskfile.yml)

```bash
task dev           # Start Go backend + Vite frontend in parallel (recommended)
task dev:go        # Go backend only (air hot-reload on :13120)
task dev:frontend  # Vite dev server only (HMR on :5173)
task build         # Compile binary → bin/kontor
task test          # Run all tests (sdk + server, race detector)
task lint          # golangci-lint (sdk + server)
task generate      # Regenerate ent schemas + tygo TS types (no DI codegen — DI is hand-written in cmd/serve/di.go)
task fmt           # format all Go code (same formatter task lint checks)
task vuln          # govulncheck
```

**Prerequisites:** Go 1.26+, `task` (`brew install go-task/tap/go-task`), `air` (`go install github.com/air-verse/air@latest`), `golangci-lint` (`brew install golangci-lint`).

> **PATH:** `go install` puts binaries in `$(go env GOPATH)/bin`. Add to shell: `export PATH="$PATH:$(go env GOPATH)/bin"`

## Frontend (pnpm)

```bash
pnpm install       # Install frontend deps (root only — not channel/)
pnpm dev           # Vite dev server on :5173 with HMR (proxies /api → :13120); run alongside task dev
pnpm build         # Production SPA build → dist/
pnpm test          # Vitest unit tests (single run)
pnpm test:e2e      # Playwright E2E tests
pnpm typecheck     # vue-tsc type checking
```

**Package manager:** pnpm. `pnpm install` from root installs frontend dependencies only (Go deps fetched automatically by `go build`/`task build`).

**Dev setup:** `task dev` starts both processes in parallel. Access frontend at `:5173` (HMR) or `:13120` (built SPA served by Go).
