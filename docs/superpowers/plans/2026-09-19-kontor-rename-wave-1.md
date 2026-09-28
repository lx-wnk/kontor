# Kontor Rename, Wave 1 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The project identifies itself as Kontor everywhere that carries no persisted user state — module paths, binary, package and desktop identity, documentation — while every existing installation keeps working untouched.

**Architecture:** Wave 1 changes only names the build produces. Nothing here reads or writes an existing installation's database, secret key, hook scripts, MCP registrations or environment: those are Wave 2, which needs a compatibility layer. The split exists so this wave can be verified by building and running, and so a mistake here cannot destroy user data.

**Tech Stack:** Go 1.26 workspace (`./sdk`, `./server`, `./desktop`, plus six independent plugin modules built with `GOWORK=off`), Vue 3 + Vite (pnpm), Taskfile, GoReleaser, wails for the macOS shell.

**Spec:** `docs/local/kontor/loop-ledger.md`, Phase 1. The naming decision and its evidence are in the Decision Log there and in `docs/superpowers/specs/2026-09-19-module-contract-v1-design.md` (the contract is built on the new name, which is why the rename runs first).

## Global Constraints

- The new name is `kontor`, lowercase, in every identifier. Display name: `Kontor`.
- Go module paths become `github.com/lx-wnk/kontor/{sdk,server,desktop}`; plugin modules become `github.com/lx-wnk/kontor-plugin-<name>`.
- **No Wave 2 surface is touched in this plan**: `DASHBOARD_*` environment variables, `~/.claude/dashboard-tasks.db`, `dashboard-secret.key`, `dashboard-hooks*`, the `dashboard-tasks` / `dashboard-channel` MCP keys and `dashboard-worktrees` keep their names here. Renaming any of them without the compatibility layer breaks an existing install silently.
- Work happens directly on `develop`, no CI runs there, so the local gate is the only gate: `pnpm lint && pnpm typecheck && pnpm test && task test && task lint`, chained with `&&`, raw output pasted.
- `task test` regenerates `server/internal/db/ent/` — `git checkout -- server/internal/db/ent/` before committing. `pnpm build` deletes `server/frontend/dist/.gitkeep` — restore it with `git checkout HEAD -- server/frontend/dist/.gitkeep`.
- Everything written is English: code, comments, commit messages, docs.
- The GitHub repository rename (`lx-wnk/Agent-Dashboard` → `lx-wnk/kontor`) is the operator's action and is **not** part of any task here. Task 4 writes the new URLs; they resolve once the operator renames. GitHub keeps redirects for the old ones.

---

## File Structure

| File | Responsibility after the change |
| --- | --- |
| `go.work` | Unchanged — it lists relative paths, not module names |
| `sdk/go.mod`, `server/go.mod`, `desktop/go.mod` | Declare the `kontor` module paths |
| `plugins/*/go.mod` (6) | Declare `kontor-plugin-*` module paths |
| ~871 `.go` files | Import from the new paths |
| `tygo.yaml`, `.golangci.yml`, `Taskfile.yml` | Reference the new module path in codegen, lint config and ldflags |
| `server/cmd/serve/main.go` | Cobra root command is `kontor` |
| `.goreleaser.yml`, `Dockerfile.goreleaser`, `install.sh` | Build, package and install `kontor` |
| `package.json`, `channel/package.json` | Package identity |
| `desktop/wails.json`, `desktop/build/darwin/Info.plist` | Desktop product name and bundle id |
| `README.md`, `CONTRIBUTING.md`, `docs/**`, `CHANGELOG.md` | Call the project Kontor and tell an existing installation what to expect |

---

### Task 1: Go module paths

**Files:**
- Modify: `sdk/go.mod:1`, `server/go.mod:1`, `desktop/go.mod:1`, `plugins/*/go.mod:1` (6 files)
- Modify: every `.go` file importing the old path (871 files), plus `tygo.yaml:2`, `.golangci.yml`, `Taskfile.yml:132`, `Taskfile.yml:156`
- Test: the existing suites; no new test — a wrong import path cannot compile

**Interfaces:**
- Consumes: nothing.
- Produces: the import prefix `github.com/lx-wnk/kontor/` that every later task's code and config refers to.

- [ ] **Step 1: Confirm the starting point**

```bash
cd /Users/alexanderwink/code/_privat/projects/agent-dashboard
grep -rl "lx-wnk/agent-dashboard" --include='*.go' . | wc -l   # expect 871
git status --short                                              # expect clean
```

- [ ] **Step 2: Rewrite the three workspace module declarations**

```bash
(cd sdk && go mod edit -module github.com/lx-wnk/kontor/sdk)
(cd server && go mod edit -module github.com/lx-wnk/kontor/server)
(cd desktop && go mod edit -module github.com/lx-wnk/kontor/desktop)
```

- [ ] **Step 3: Rewrite the six plugin module declarations**

```bash
for d in plugins/*/; do
  name=$(basename "$d")
  [ -f "$d/go.mod" ] || continue
  (cd "$d" && GOWORK=off go mod edit -module "github.com/lx-wnk/kontor-plugin-$name")
done
```

- [ ] **Step 4: Rewrite every import and config reference**

The plugin prefix must be replaced before the workspace prefix, otherwise
`agent-dashboard-plugin-x` is first turned into `kontor/-plugin-x`.

```bash
find . -type f \( -name '*.go' -o -name '*.mod' -o -name '*.yml' -o -name '*.yaml' \) \
  -not -path './node_modules/*' -not -path './.git/*' -print0 \
| xargs -0 perl -pi -e 's{github\.com/lx-wnk/agent-dashboard-plugin-}{github.com/lx-wnk/kontor-plugin-}g; s{github\.com/lx-wnk/agent-dashboard/}{github.com/lx-wnk/kontor/}g'
```

- [ ] **Step 5: Verify nothing references the old path and everything builds**

```bash
grep -rn "lx-wnk/agent-dashboard" --include='*.go' . | wc -l   # expect 0
(cd server && go build ./... && go vet ./...)
(cd sdk && go build ./... && go vet ./...)
(cd desktop && go build ./...)
for d in plugins/*/; do (cd "$d" && GOWORK=off go build ./...) || echo "FAILED $d"; done
```

- [ ] **Step 6: Run the gate**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git checkout -- server/internal/db/ent/
```

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor: the Go modules are called kontor"
```

---

### Task 2: Binary and distribution names

**Files:**
- Modify: `server/cmd/serve/main.go:35` (`Use: "agent-dashboard"`), `Taskfile.yml:101,132,139,156-167`, `.goreleaser.yml:9,13,15,48-51,77-79,96`, `Dockerfile.goreleaser:3-5`, `install.sh:4,8,11,14-15,52,94,106`
- Test: `tests/e2e/` runs the built binary through `scripts/e2e-server.sh`, which must point at the new name

**Interfaces:**
- Consumes: the module paths from Task 1 (the ldflags path in `Taskfile.yml`).
- Produces: `bin/kontor` and `bin/kontor-desktop`, the names every script and the E2E harness use.

- [ ] **Step 1: Rename the CLI root command**

In `server/cmd/serve/main.go:35`, `Use: "agent-dashboard"` becomes `Use: "kontor"`. Subcommands (`serve`, `channel`, `ptyhost`, `hooks`, `live`, `pty-host`) are unchanged.

- [ ] **Step 2: Rename the build outputs**

In `Taskfile.yml`: `-o ../bin/agent-dashboard` → `-o ../bin/kontor`; `bin/agent-dashboard-desktop` → `bin/kontor-desktop`; the `.app`/`.dmg`/volume names `Agent Dashboard` → `Kontor`.

- [ ] **Step 3: Rename the harness reference**

In `scripts/e2e-server.sh`, `BIN=bin/agent-dashboard` → `BIN=bin/kontor`.

- [ ] **Step 4: Rename packaging**

`.goreleaser.yml`: `project_name`, the build id and `binary`, `homebrew_casks[0].name` and its binaries, the `dockers_v2` id and image `ghcr.io/lx-wnk/agent-dashboard` → `ghcr.io/lx-wnk/kontor`, the release owner/name, and the caveats/footer text.
`Dockerfile.goreleaser:3-5`: the copied path and `ENTRYPOINT` become `/kontor`.
`install.sh`: `REPO=lx-wnk/kontor`, `BINARY=kontor`, and the variables `AGENT_DASHBOARD_BIN_DIR` / `AGENT_DASHBOARD_VERSION` become `KONTOR_BIN_DIR` / `KONTOR_VERSION`.

- [ ] **Step 5: Verify the binary builds and answers to its new name**

```bash
task build && ./bin/kontor --help | head -3        # usage line says "kontor"
shellcheck scripts/e2e-server.sh install.sh
```

- [ ] **Step 6: Run the gate, then one E2E spec to prove the harness still boots**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git checkout -- server/internal/db/ent/
pnpm exec playwright test tests/e2e/csp-self-violations.spec.ts --reporter=line
```

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor: the binary and its packaging are called kontor"
```

---

### Task 3: Package and desktop identity

**Files:**
- Modify: `package.json:2` (`claude-agent-overview` → `kontor`) and its `homepage`/`repository` URLs, `channel/package.json:2` (`dashboard-channel` → `kontor-channel`) and the script that runs `channel/dashboard-channel.ts`
- Rename: `channel/dashboard-channel.ts` → `channel/kontor-channel.ts`
- Modify: `vite.config.ts:57-59` (PWA `name`, `short_name`, `description`), `desktop/wails.json:3,4,15` , `desktop/build/darwin/Info.plist:10`
- Test: `src/` unit suites cover the PWA config indirectly; the desktop build is the check for wails

**Interfaces:**
- Consumes: `bin/kontor-desktop` from Task 2 (`wails.json` `outputfilename`).
- Produces: bundle id `com.lxwnk.kontor`, which Wave 2 refers to when it documents what an existing macOS install must re-approve.

**Do NOT touch** the stdio MCP server key `dashboard-channel` written into `~/.claude.json` (`server/internal/channelconfig/channelconfig.go:61`) — renaming the file and the npm package is safe, renaming the registered key orphans every existing agent session and belongs to Wave 2, Task 2.3.

- [ ] **Step 1: Rename the npm packages and the channel entry point**

```bash
git mv channel/dashboard-channel.ts channel/kontor-channel.ts
```

Then update `package.json:2`, its `homepage` and `repository.url` to the `kontor` repository, `channel/package.json:2` and the script that points at the renamed file.

- [ ] **Step 2: Rename the PWA and desktop product strings**

`vite.config.ts:57-59`: `name: 'Kontor'`, `short_name: 'Kontor'`, description updated to name Kontor.
`desktop/wails.json`: `"name": "Kontor"`, `"outputfilename": "kontor-desktop"`, `"productName": "Kontor"`.
`desktop/build/darwin/Info.plist:10`: `com.lxwnk.agent-dashboard` → `com.lxwnk.kontor`.

- [ ] **Step 3: Verify both builds**

```bash
pnpm build && git checkout HEAD -- server/frontend/dist/.gitkeep
grep -o '"name":[^,]*' server/frontend/dist/manifest.webmanifest   # says Kontor
task desktop:build
```

- [ ] **Step 4: Run the gate**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git checkout -- server/internal/db/ent/
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor: the app and the desktop shell are called Kontor"
```

---

### Task 4: Documentation and the note an existing install needs

**Files:**
- Modify: `README.md` (title, badges, clone URL, issue link, the `claude mcp add` example), `CONTRIBUTING.md`, `PRIVACY.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md` where they name the project, `docs/guides/*.md`, `.mcp.json.example`, `.env.dist`
- Modify: `CHANGELOG.md` — a `### Changed` entry under Unreleased
- Do not modify: `docs/archive/**` (1412 matching lines, historical record)

**Interfaces:**
- Consumes: every name introduced in Tasks 1-3.
- Produces: nothing code reads.

- [ ] **Step 1: Find every user-facing mention**

```bash
grep -rn "agent-dashboard\|Agent-Dashboard\|Agent Dashboard\|claude-agent-overview" \
  --include='*.md' --include='*.example' --include='*.dist' . \
  | grep -v node_modules | grep -v docs/archive
```

- [ ] **Step 2: Rewrite them, checking each claim against the code**

Every changed line is verified against what the code now does — a doc that
names a flag, a path or a command must match the file it describes. The
`DASHBOARD_*` variables and the `~/.claude/...` paths stay as they are: Wave 2
renames them, and documenting them early would be wrong twice.

- [ ] **Step 3: Write the CHANGELOG entry**

Under `## [Unreleased]` → `### Changed`, state that the project is now Kontor,
that the binary is `kontor`, that the Homebrew cask and Docker image changed
name, and that environment variables, the database path and MCP registrations
are unchanged in this release.

- [ ] **Step 4: Verify no stale name outside the archive**

```bash
grep -rn "agent-dashboard\|Agent Dashboard" --include='*.md' . \
  | grep -v node_modules | grep -v docs/archive | grep -v CHANGELOG.md | wc -l   # expect 0
```

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs: the project is called Kontor"
```

---

## Self-Review

**Spec coverage.** Ledger Phase 1 lists 1.1 module paths (Task 1), 1.2 binary and distribution (Task 2), 1.3 frontend and desktop identity (Task 3), 1.4 documentation (Task 4) and 1.5 the GitHub rename (operator, explicitly out of scope and named in the Global Constraints). No unit is unaddressed.

**Placeholders.** None: every step names the file, the exact old and new value, and the command that proves it.

**Type consistency.** The names introduced flow forward: `github.com/lx-wnk/kontor/*` (Task 1) is used by the ldflags in Task 2; `bin/kontor-desktop` (Task 2) is the `outputfilename` in Task 3; `com.lxwnk.kontor` (Task 3) is what Wave 2 will reference. The one deliberate non-rename — the `dashboard-channel` MCP key — is called out where it would otherwise be swept up.

**Ordering risk.** Task 1's replacement order (plugin prefix before workspace prefix) is stated in the step itself, because the reverse order silently produces `kontor/-plugin-*`.
