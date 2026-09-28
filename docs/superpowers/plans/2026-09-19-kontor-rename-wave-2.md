# Kontor Rename, Wave 2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The names an installation has written to disk or into another program's configuration become Kontor's, and an installation that predates the change keeps working without the operator restoring anything by hand.

**Architecture:** Every surface here is read by something that already exists — a shell profile, a SQLite file, an encrypted key, a hook script installed in `~/.claude/settings.json`, an MCP entry another program reads. So each rename is a pair: write the new name, and keep reading the old one when the new one is absent. The compatibility read is not a courtesy, it is what makes the change reversible.

**Tech Stack:** Go 1.26, koanf for configuration, AES-256-GCM via `server/internal/secretbox`, SQLite through ent.

**Spec:** `docs/local/kontor/loop-ledger.md`, Phase 2, and the measurements in its Decision Log. Wave 1 is `docs/superpowers/plans/2026-09-19-kontor-rename-wave-1.md`.

## Global Constraints

- Wave 1 is merged: module paths, binary, packaging and documentation already say Kontor. This plan changes only persisted names.
- **The secret key is the data-loss path.** `server/internal/secretbox` encrypts every plugin secret with the key at `$CLAUDE_CONFIG_DIR/dashboard-secret.key`. A rename that cannot find the old key does not fail loudly — it produces a new key, and every stored secret becomes undecryptable. Task 2 therefore reads the old key, re-encrypts with the new one, and keeps a backup, and it is tested against a copy of a real installation before it is committed.
- Every task's verification runs against a **copy** of a real installation, never the operator's live one: `cp ~/.claude/dashboard-tasks.db <scratch>` and a scratch `HOME` where a path is involved.
- The gate is `pnpm lint && pnpm typecheck && pnpm test && task test && task lint`, chained with `&&`, raw output pasted. `task test` regenerates `server/internal/db/ent/`; revert it unless the regeneration is the change.
- Task 3 needs the operator's explicit go-ahead before it runs: it writes to `~/.claude.json`, which other programs read.
- Everything written is English.

---

## File Structure

| File | Responsibility after the change |
| --- | --- |
| `server/internal/config/config.go` | Reads `KONTOR_*`, falls back to `DASHBOARD_*`, warns once when it does |
| `server/internal/pipeline/spawner.go`, `server/internal/api/agents/spawn.go` | Pass both prefixes through to spawned agents |
| `server/internal/secretbox/secretbox.go` | Finds the key under either name; migrates the old one |
| `server/internal/config/hooks_secret_store.go`, `server/internal/hookscript/` | New file names, old ones still read; `hooks install` rewrites the installed script |
| `server/internal/worktree/worktree.go` | New default root, old one still used when it exists and holds worktrees |
| `server/internal/mcp/constants.go`, `server/internal/channelconfig/channelconfig.go` | New MCP keys, old keys cleaned up on boot (Task 3 only) |
| `CHANGELOG.md`, `docs/guides/configuration.md`, `.env.dist` | State what an operator must do, and what happens automatically |

---

### Task 1: The environment prefix

**Files:**
- Modify: `server/internal/config/config.go:99-104` (the koanf env provider), `server/internal/pipeline/spawner.go:420`, `server/internal/api/agents/spawn.go:1001` (the pass-through allow-lists)
- Test: `server/internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `KONTOR_*` as the documented prefix; `DASHBOARD_*` keeps working.

- [ ] **Step 1: Write the failing test**

```go
func TestLoadPrefersKontorPrefixAndFallsBackToDashboard(t *testing.T) {
	t.Setenv("DASHBOARD_PORT", "13500")
	old, err := config.Load("")
	if err != nil {
		t.Fatalf("load with the old prefix: %v", err)
	}
	if old.Port != 13500 {
		t.Fatalf("port = %d, want the value from the old prefix", old.Port)
	}

	t.Setenv("KONTOR_PORT", "13600")
	both, err := config.Load("")
	if err != nil {
		t.Fatalf("load with both prefixes: %v", err)
	}
	if both.Port != 13600 {
		t.Errorf("port = %d, want the new prefix to win", both.Port)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd server && go test ./internal/config/ -run TestLoadPrefersKontorPrefix -v`
Expected: FAIL — `KONTOR_PORT` is not read at all, so the port stays 13500.

- [ ] **Step 3: Load both prefixes, new one last**

In `config.go`, keep the existing `DASHBOARD_` provider and add a second load after it with the `KONTOR_` prefix, so a value under the new name overrides the old. Record whether any `DASHBOARD_`-prefixed variable was present and no `KONTOR_` one was, and `slog.Warn` once, naming the prefix and the release in which the old one stops being read.

- [ ] **Step 4: Run the test again**

Run: `cd server && go test ./internal/config/ -run TestLoadPrefersKontorPrefix -v`
Expected: PASS.

- [ ] **Step 5: Extend the spawn allow-lists**

`spawner.go:420` and `spawn.go:1001` let `CLAUDE_` and `DASHBOARD_` through to a spawned agent. Add `KONTOR_`. The blocklist that strips `DASHBOARD_*` secrets from plugin environments (`plugin/registry.go:829-838`) must strip `KONTOR_*` too — otherwise the rename reopens a closed hole.

- [ ] **Step 6: Red-test that hole**

Add a test asserting a `KONTOR_MCP_TOKEN` in the server's environment does not reach a plugin process, mirroring the existing `DASHBOARD_MCP_TOKEN` test. Remove the blocklist entry, watch it fail, restore it.

- [ ] **Step 7: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git checkout -- server/internal/db/ent/
git add -A && git commit -m "feat: configuration reads KONTOR_ and still accepts DASHBOARD_"
```

---

### Task 2: Files on disk

**Files:**
- Modify: `server/internal/config/config.go:58` (database default), `server/internal/secretbox/secretbox.go:19`, `server/internal/config/hooks_secret_store.go:12`, `server/internal/hookscript/hookscript.go:23` and `server/internal/hookscript/dashboard-permission.sh:27`, `server/internal/worktree/worktree.go:19`
- Test: `server/internal/secretbox/secretbox_test.go`, `server/internal/config/config_test.go`, `server/internal/hookscript/hookscript_test.go`

**Interfaces:**
- Consumes: Task 1's configuration loading.
- Produces: `~/.claude/kontor-tasks.db`, `kontor-secret.key`, `kontor-hooks/`, `kontor-worktrees` — each with a read of the old name when the new file is absent.

- [ ] **Step 1: Write the failing test for the key, the one that can lose data**

```go
func TestKeyMigratesFromTheOldNameWithoutLosingSecrets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	oldPath := filepath.Join(dir, "dashboard-secret.key")
	key := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(oldPath, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		t.Fatalf("seed the old key: %v", err)
	}
	sealed, err := secretbox.New().Seal("a stored plugin secret")
	if err != nil {
		t.Fatalf("seal with the old key: %v", err)
	}

	got, err := secretbox.New().Open(sealed)
	if err != nil {
		t.Fatalf("open after migration: %v", err)
	}
	if got != "a stored plugin secret" {
		t.Errorf("secret = %q, want it to survive the rename", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "kontor-secret.key")); err != nil {
		t.Errorf("the new key file was not written: %v", err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd server && go test ./internal/secretbox/ -run TestKeyMigrates -v`
Expected: FAIL — the new name is unknown, so a fresh key is generated and the sealed value cannot be opened.

- [ ] **Step 3: Resolve the key under either name**

Look for `kontor-secret.key`; if it is absent and `dashboard-secret.key` exists, copy it to the new name (0600), keep the old file as the backup, and log once at Info that the key was migrated and where the backup is. Never delete the old file in this release.

- [ ] **Step 4: Run the test again**

Run: `cd server && go test ./internal/secretbox/ -run TestKeyMigrates -v`
Expected: PASS.

- [ ] **Step 5: Do the same for the other three, each with its own test**

Database: use `kontor-tasks.db`, but when it does not exist and `dashboard-tasks.db` does, keep using the old file and log once — a database is not copied behind the operator's back.
Hooks secret and script directory: write the new names; `hooks install` rewrites the entry in `~/.claude/settings.json`; the reader accepts either name so an installation that has not re-run the installer keeps working.
Worktree root: default to `kontor-worktrees`, but when `dashboard-worktrees` exists and holds directories, keep using it — moving checkouts would orphan running agents.

- [ ] **Step 6: Verify against a copy of a real installation**

```bash
SCRATCH=$(mktemp -d)
cp ~/.claude/dashboard-tasks.db "$SCRATCH/db"
HOME="$SCRATCH" CLAUDE_CONFIG_DIR="$SCRATCH" KONTOR_DB_PATH="$SCRATCH/db" ./bin/kontor serve &
curl -s localhost:13120/api/plugins   # a plugin with a stored secret still reports its settings
```

Paste the output. A decryption failure here is the failure this task exists to prevent.

- [ ] **Step 7: Gate and commit**

```bash
pnpm lint && pnpm typecheck && pnpm test && task test && task lint
git checkout -- server/internal/db/ent/
git add -A && git commit -m "feat: the files on disk are called kontor, and the old ones still open"
```

---

### Task 3: The MCP registrations — needs the operator's go-ahead

**Files:**
- Modify: `server/internal/mcp/constants.go:5`, `server/internal/channelconfig/channelconfig.go:49,61`, `server/internal/api/applications/handler.go:319,378`
- Test: `server/internal/channelconfig/channelconfig_test.go`

**Interfaces:**
- Consumes: Tasks 1 and 2.
- Produces: the keys `kontor-tasks` and `kontor-channel`.

**This task writes to `~/.claude.json`, which other programs read.** Do not run it until the operator has said so in the session. Until then it stays `BLOCKED` in the ledger, and the rest of the plan proceeds without it.

- [ ] **Step 1: Write the failing test**

A config that already contains `mcpServers.dashboard-channel` must, after one boot, contain `kontor-channel` with the current binary path and no `dashboard-channel` entry — and an unrelated third-party server in the same file must be untouched.

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Rename on write, clean up the old key**

Write the new key; when the old key is present and points at this binary, remove it. A key pointing somewhere else belongs to another installation and is left alone.

- [ ] **Step 4: Run the test again**

- [ ] **Step 5: Verify with a scratch config**

Copy `~/.claude.json` to a scratch `HOME`, boot against it, and diff the result. Paste the diff.

- [ ] **Step 6: Gate and commit**

---

### Task 4: Say what changed

**Files:**
- Modify: `CHANGELOG.md`, `docs/guides/configuration.md`, `docs/guides/install.md`, `docs/guides/hooks-setup.md`, `.env.dist` (operator decides — the permission rules block reading it)

- [ ] **Step 1: Document both names**

Every variable and path is documented under its new name, with one sentence stating that the old name is still read and in which release that stops.

- [ ] **Step 2: Write the CHANGELOG entry**

Under `### Changed`: the new variable prefix, the new file names, the automatic key migration and where the backup is, and the one manual step — re-running `kontor hooks install` — for an installation that wants the hook scripts renamed too.

- [ ] **Step 3: Verify every claim against the code**

For each documented name, point at the line that reads it. A doc that names a fallback the code does not implement is worse than no doc.

- [ ] **Step 4: Commit**

---

## Self-Review

**Spec coverage.** Ledger Phase 2 lists 2.1 the environment prefix (Task 1), 2.2 the files on disk (Task 2), 2.3 the MCP registrations (Task 3, gated on the operator) and 2.4 release identity and documentation (Task 4). All four are covered.

**Placeholders.** None: each step names the file, the old and new value, and the command that proves it.

**Type consistency.** Task 1's loader is what Task 2's path defaults are read through; Task 2's binary path is what Task 3 writes into the MCP entry.

**The risk this plan is shaped around.** Task 2, Step 1 is deliberately the first test written in the whole plan: it is the only failure here that destroys something the operator cannot recreate.
