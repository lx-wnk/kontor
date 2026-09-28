# Kontor Session in the Mission Tile — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Mission input talks to one interactive Claude session that can read and steer Kontor, shown in its own tile; capture into `projects[0]` goes away.

**Architecture:** A `kontorsession` service spawns a native `claude` in a server-owned cache directory with a short-lived `kontor_session` API key (every scope but `keys:manage`), `--append-system-prompt` briefing and a strict MCP config. The active key row is the session record; one `end()` path stops the process, revokes the key and removes the config, and boot reconcile re-arms or ends a surviving session. The SPA gets a shared `useKontorSession` composable, a `KontorTile` that embeds the existing `AgentTerminal`, and Spotlight hands unmatched text to it.

**Tech Stack:** Go 1.26 (chi, ent on modernc sqlite), Vue 3 + TypeScript (Vite, Vitest, Playwright), Tailwind v4.

**Spec:** `docs/superpowers/specs/2026-09-21-kontor-session-design.md`

## Global Constraints

- Work happens directly on `develop`, one commit per task, no PR. `develop` has no CI, so every task runs its gate locally and pastes the raw output.
- Gates: frontend `pnpm lint && pnpm typecheck && pnpm test`; Go `cd server && go vet ./...` plus the task's `go test -race` scope; before the last task also `task test` and `task lint`; E2E `pnpm test:e2e`.
- `go test ./...` and `task test` regenerate `server/internal/db/ent/`. Run `git checkout -- server/internal/db/ent/` before every commit except Task 1, whose change IS the regeneration.
- `pnpm build` (and the E2E server script) deletes `server/frontend/dist/.gitkeep`. Restore it with `git checkout HEAD -- server/frontend/dist/.gitkeep` before committing.
- Tests never start a real agent. Spawning goes through the existing seams (`execStart`, `lookTmuxPath`) or the service's func fields.
- Everything that ships is English. Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`), subjects describe behaviour, never task numbers or phase labels.
- Default to no comment. A comment is allowed only for lifecycle timing, an external contract, a non-obvious edge case, or a case-to-effect mapping — one short line.
- Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01CoCiMTCBePMFRJcfQfSHSe
  ```
- Wire contract shared by both halves: `GET /api/kontor-session` → `200 {"pid": n}` or `200 {"pid": null}`; `POST /api/kontor-session {"prompt"}` (prompt required) → `200 {"pid": n}`, returning a running session unchanged; `POST /api/kontor-session/renew {"prompt"}` (prompt may be empty) → `200 {"pid": n}`; `DELETE /api/kontor-session` → `204`; errors → `{"error": "..."}` with a 4xx/5xx status.

## Facts the tasks rely on

- Ent codegen: `server/internal/db/ent/generate.go:4` (`--feature sql/upsert` baked in); fallback `server/internal/db/entgen/gen_test.go` `TestRegenerateEnt`. Auto-migrate only adds (`server/internal/db/client.go:151`).
- Keys: `server/internal/db/repo/api_key_repo.go:17-22` kinds, `:45-63` interface, `:163-180` `RevokeForModule`; MCP scopes `server/internal/mcp/auth.go:18-58`; scope refusal `-32003` at `server/internal/mcp/jsonrpc.go:108-114`.
- Spawn: `server/internal/api/agents/spawn.go:95` `NewSpawnManager`, `:278` `buildSpawnArgs`, `:430` `launchInteractive`, `:529` `pollExitWatch` (gives up after 1h), `:1068` `spawnerArgsControlPermissionMode`; `processAlive` at `agents/dismiss.go:55`; seams `execStart` `:52`, `lookTmuxPath` `:55`.
- The live default spawner is `claude --permission-mode auto`. A Kontor session overrides the spawner's permission posture to `default` so writes prompt (spec decision 3); it does not refuse to start.
- Spawn policy: `server/internal/services/spawn_policy.go:207-213` blacklist (`.claude`, `.config`, …) checked before roots at `:277-318`.
- Frontend: `MissionInput.vue` (to be replaced), `useReading.ts:12,24`, `useCapture.ts:30` (`projects[0]`), `SpotlightSearch.vue:75-80,127-144,198-203,343-349`, `NextThing.vue:148-158`, `AgentTerminal.vue:12-14` (`{ pid: number }`), no shared fetch helper (plain `fetch`, errors via `src/utils/errorMessage.ts`), `src/features/agents/index.ts` does not export `AgentTerminal` and the ESLint boundary rule forbids deep cross-feature imports (dynamic ones too).

---

## Backend

### Task 1: api_key gains `session_pid` and the `kontor_session` kind
**Files:**
- Modify `server/internal/db/ent/schema/api_key.go`: `Fields()`, after the `stage_run_id` field (lines ~28-30).
- Modify `server/internal/db/repo/api_key_repo.go`: consts at lines 17-22, interface at 45-63, new methods appended after `RevokeForModule` (~line 180).
- Regenerate `server/internal/db/ent/` (generated).
- Test in `server/internal/db/repo/api_key_repo_test.go` (append).
- Find the fakes: run `grep -rln "RevokeForModule(" server --include='*_test.go'`. Every fake that implements `repo.ApiKeyRepo` gets the three new methods as `return nil, nil` / `nil` / `0, nil` stubs.

**Interfaces:** Consumes: ent `ApiKey` client. Produces:
```go
const ApiKeyKindKontorSession = "kontor_session"
ActiveKontorSession(ctx context.Context) (*ent.ApiKey, error) // nil, nil when none
SetSessionPID(ctx context.Context, id string, pid int) error
RevokeKontorSessions(ctx context.Context) (int, error)
```
**Acceptance:** A kontor_session key can be created, given a pid, found as the active session, and revoked without touching other kinds. The `session_pid` column survives a drop-and-remigrate (down then up).

- [ ] Step 1: Write the failing test (append; add `database/sql` and `path/filepath` to the imports).
```go
func TestApiKey_KontorSessionLifecycle(t *testing.T) {
	r := repo.NewApiKeyRepo(openTestDB(t))
	ctx := t.Context()

	none, err := r.ActiveKontorSession(ctx)
	require.NoError(t, err)
	require.Nil(t, none)

	k, err := r.Create(ctx, repo.CreateApiKeyInput{Name: "kontor-session", Hash: "ks", Scopes: []string{"tasks:read"}, Kind: repo.ApiKeyKindKontorSession})
	require.NoError(t, err)
	require.Nil(t, k.SessionPid)
	require.NoError(t, r.SetSessionPID(ctx, k.ID, 4242))

	got, err := r.ActiveKontorSession(ctx)
	require.NoError(t, err)
	require.Equal(t, k.ID, got.ID)
	require.Equal(t, 4242, *got.SessionPid)

	n, err := r.RevokeKontorSessions(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err = r.ActiveKontorSession(ctx)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestApiKey_RevokeKontorSessionsSparesOtherKinds(t *testing.T) {
	r := repo.NewApiKeyRepo(openTestDB(t))
	ctx := t.Context()
	_, err := r.Create(ctx, repo.CreateApiKeyInput{Name: "u", Hash: "user"})
	require.NoError(t, err)
	_, err = r.Create(ctx, repo.CreateApiKeyInput{Name: "k", Hash: "kontor", Kind: repo.ApiKeyKindKontorSession})
	require.NoError(t, err)

	n, err := r.RevokeKontorSessions(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = r.GetByHash(ctx, "user")
	require.NoError(t, err, "a user key must survive ending the session")
}

// Down path: ent auto-migrate only adds columns, so a rollback is the manual
// DROP COLUMN below. The test proves it is one statement (no index or
// constraint references the column) and that the next boot re-adds it.
func TestApiKey_SessionPidColumnDownPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kontor.db")
	b, err := db.Open(path)
	require.NoError(t, err)
	k, err := repo.NewApiKeyRepo(b.Client).Create(t.Context(), repo.CreateApiKeyInput{Name: "u", Hash: "h"})
	require.NoError(t, err)
	require.NoError(t, b.Close())

	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = raw.Exec(`ALTER TABLE api_keys DROP COLUMN session_pid`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	b, err = db.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	got, err := repo.NewApiKeyRepo(b.Client).GetByID(t.Context(), k.ID)
	require.NoError(t, err)
	require.Nil(t, got.SessionPid)
}
```
- [ ] Step 2: Run `cd server && go test ./internal/db/repo/ -run 'TestApiKey_(KontorSession|RevokeKontorSessions|SessionPid)' -v`. Expected FAIL (compile): `undefined: repo.ApiKeyKindKontorSession`, `k.SessionPid undefined`, `r.ActiveKontorSession undefined`.
- [ ] Step 3: Minimal implementation.

Schema (after `stage_run_id`):
```go
		// session_pid is the process a kontor_session key belongs to. Nil until
		// the spawn returns a pid. Not indexed: SQLite refuses DROP COLUMN on an
		// indexed column, and dropping it is the down path.
		field.Int("session_pid").Optional().Nillable(),
```
Repo constant (inside the const block):
```go
	// ApiKeyKindKontorSession is the credential of the one Kontor session. The
	// active row of this kind is the session record.
	ApiKeyKindKontorSession = "kontor_session"
```
Interface additions:
```go
	// ActiveKontorSession returns the active kontor_session key, or nil, nil.
	ActiveKontorSession(ctx context.Context) (*ent.ApiKey, error)
	SetSessionPID(ctx context.Context, id string, pid int) error
	// RevokeKontorSessions deactivates every active kontor_session key.
	RevokeKontorSessions(ctx context.Context) (int, error)
```
Methods (import `entsql "entgo.io/ent/dialect/sql"`):
```go
func (r *entApiKeyRepo) ActiveKontorSession(ctx context.Context) (*ent.ApiKey, error) {
	k, err := r.client.ApiKey.Query().
		Where(apikey.KindEQ(ApiKeyKindKontorSession), apikey.Active(true)).
		Order(apikey.ByCreatedAt(entsql.OrderDesc())).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("apikey.ActiveKontorSession: %w", err)
	}
	return k, nil
}

func (r *entApiKeyRepo) SetSessionPID(ctx context.Context, id string, pid int) error {
	if err := r.client.ApiKey.UpdateOneID(id).SetSessionPid(pid).Exec(ctx); err != nil {
		return fmt.Errorf("apikey.SetSessionPID: %w", err)
	}
	return nil
}

func (r *entApiKeyRepo) RevokeKontorSessions(ctx context.Context) (int, error) {
	n, err := r.client.ApiKey.Update().
		Where(apikey.KindEQ(ApiKeyKindKontorSession), apikey.Active(true)).
		SetActive(false).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("apikey.RevokeKontorSessions: %w", err)
	}
	return n, nil
}
```
Regenerate with `cd server && go generate ./internal/db/ent/ && go mod tidy`. The `--feature sql/upsert` flag is baked into `generate.go:4`; do not call `ent generate` by hand without it. Fallback: `cd server && go test ./internal/db/entgen/ -run TestRegenerateEnt -count=1` (it uses `gen.FeatureUpsert`). Check `git diff --stat server/internal/db/ent/` still shows the `OnConflict` builders untouched.
- [ ] Step 4: Run the same command. Expected PASS (3 tests).
- [ ] Step 5: Gate and commit.
  - Gate: `cd server && go vet ./... && go test -race ./internal/db/...`.
  - Commit: `git add server/internal/db/ent server/internal/db/repo/api_key_repo.go server/internal/db/repo/api_key_repo_test.go server/go.mod server/go.sum <fakes touched> && git commit -m "feat: record a kontor session's pid on its api key"`. The ent change is this task's own, so no ent checkout here.

**Down-path note (for the commit body or PR):** Rolling back the binary leaves `api_keys.session_pid` as an unused nullable column. Older ent code ignores it because it selects named columns. To remove it physically, run `ALTER TABLE api_keys DROP COLUMN session_pid;`. `TestApiKey_SessionPidColumnDownPath` proves that statement works and that the next boot re-adds the column.

---

### Task 2: Kontor session scopes, allow list and key issuer
**Files:** Create `server/internal/mcp/sessionkey.go`. Test in `server/internal/mcp/sessionkey_test.go` (new).
**Interfaces:** Consumes: `ToolScopeMap`, `ServerName`, `GenerateAPIToken`, `HashToken`, and from Task 1 `repo.ApiKeyRepo.{Create,ActiveKontorSession,SetSessionPID,RevokeKontorSessions}`. Produces:
```go
func KontorSessionScopes() []string
func KontorSessionAllowedTools() []string
type KontorSessionKeyIssuer struct{ Keys repo.ApiKeyRepo }
func (i KontorSessionKeyIssuer) Issue(ctx context.Context) (string, error)
func (i KontorSessionKeyIssuer) Attach(ctx context.Context, pid int) error
func (i KontorSessionKeyIssuer) Current(ctx context.Context) (pid int, ok bool, err error) // ok = active key exists; pid 0 = not attached
func (i KontorSessionKeyIssuer) Revoke(ctx context.Context) error
```
**Acceptance:** A kontor-session key has every scope except `keys:manage`, pre-approves exactly the `:read` tools, and is refused `create_api_key` at `/api/mcp`.

- [ ] Step 1: Write the failing test.
```go
package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

func newSessionIssuer(t *testing.T) (mcp.KontorSessionKeyIssuer, repo.ApiKeyRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	keys := repo.NewApiKeyRepo(bundle.Client)
	return mcp.KontorSessionKeyIssuer{Keys: keys}, keys
}

func TestKontorSessionScopes_EverythingButKeysManage(t *testing.T) {
	got := mcp.KontorSessionScopes()
	require.True(t, slices.IsSorted(got))
	require.Equal(t, len(got), len(slices.Compact(slices.Clone(got))), "no duplicates")
	require.NotContains(t, got, "keys:manage")
	for _, scope := range mcp.ToolScopeMap {
		if scope != "keys:manage" {
			require.Contains(t, got, scope)
		}
	}
}

func TestKontorSessionAllowedTools_AreExactlyTheReadTools(t *testing.T) {
	allow := mcp.KontorSessionAllowedTools()
	require.True(t, slices.IsSorted(allow))
	for tool, scope := range mcp.ToolScopeMap {
		require.Equal(t, strings.HasSuffix(scope, ":read"), slices.Contains(allow, toolPrefix+tool), "tool %q (scope %q)", tool, scope)
	}
}

func TestKontorSessionKeyIssuer_Lifecycle(t *testing.T) {
	issuer, keys := newSessionIssuer(t)
	ctx := t.Context()

	token, err := issuer.Issue(ctx)
	require.NoError(t, err)
	row, err := keys.GetByHash(ctx, mcp.HashToken(token))
	require.NoError(t, err)
	require.Equal(t, repo.ApiKeyKindKontorSession, row.Kind)
	require.Nil(t, row.ExpiresAt, "a session lives until it is ended")

	pid, ok, err := issuer.Current(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Zero(t, pid)

	require.NoError(t, issuer.Attach(ctx, 4242))
	pid, ok, err = issuer.Current(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 4242, pid)

	require.NoError(t, issuer.Revoke(ctx))
	_, ok, err = issuer.Current(ctx)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = keys.GetByHash(ctx, mcp.HashToken(token))
	require.Error(t, err)
}

func TestKontorSessionKey_IsRefusedCreateAPIKey(t *testing.T) {
	issuer, keys := newSessionIssuer(t)
	token, err := issuer.Issue(t.Context())
	require.NoError(t, err)

	called := map[string]bool{}
	registry := mcp.ToolRegistry{}
	for _, name := range []string{"create_api_key", "list_tasks"} {
		registry.Register(&mcp.ToolDef{Name: name, InputSchema: map[string]any{"type": "object"},
			Handler: func(context.Context, map[string]any) (*mcp.ToolResult, error) {
				called[name] = true
				return mcp.OK(nil)
			}})
	}
	h := mcp.McpAuthMiddleware(keys)(mcp.MCPHandler(registry, nil, nil))
	call := func(tool string) map[string]any {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{}}}`
		req := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var resp map[string]any
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		return resp
	}

	require.Nil(t, call("list_tasks")["error"], "positive control: the key authenticates")
	rpcErr := call("create_api_key")["error"].(map[string]any)
	require.EqualValues(t, -32003, rpcErr["code"])
	require.False(t, called["create_api_key"])
}
```
(`toolPrefix` already exists in `stagerun_allowlist_test.go`, same `mcp_test` package.)
- [ ] Step 2: Run `cd server && go test ./internal/mcp/ -run 'KontorSession' -v`. Expected FAIL: `undefined: mcp.KontorSessionKeyIssuer` / `undefined: mcp.KontorSessionScopes`.
- [ ] Step 3: Minimal implementation, `server/internal/mcp/sessionkey.go`.
```go
package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// KontorSessionScopes is every scope a tool needs, minus keys:manage: a
// session able to mint keys could mint one that outlives it.
func KontorSessionScopes() []string {
	set := map[string]bool{}
	for _, scope := range ToolScopeMap {
		if scope != "keys:manage" {
			set[scope] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// KontorSessionAllowedTools pre-approves the read tools. Every other Kontor
// tool raises Claude Code's permission prompt on first use.
func KontorSessionAllowedTools() []string {
	var tools []string
	for tool, scope := range ToolScopeMap {
		if strings.HasSuffix(scope, ":read") {
			tools = append(tools, "mcp__"+ServerName+"__"+tool)
		}
	}
	slices.Sort(tools)
	return tools
}

// KontorSessionKeyIssuer is the only writer of repo.ApiKeyKindKontorSession rows.
type KontorSessionKeyIssuer struct {
	Keys repo.ApiKeyRepo
}

func (i KontorSessionKeyIssuer) Issue(ctx context.Context) (string, error) {
	token := GenerateAPIToken()
	if _, err := i.Keys.Create(ctx, repo.CreateApiKeyInput{
		Name:   "kontor-session",
		Hash:   HashToken(token),
		Scopes: KontorSessionScopes(),
		Kind:   repo.ApiKeyKindKontorSession,
	}); err != nil {
		return "", fmt.Errorf("mcp: issue kontor-session key: %w", err)
	}
	return token, nil
}

func (i KontorSessionKeyIssuer) Attach(ctx context.Context, pid int) error {
	k, err := i.Keys.ActiveKontorSession(ctx)
	if err != nil {
		return fmt.Errorf("mcp: attach kontor-session pid: %w", err)
	}
	if k == nil {
		return errors.New("mcp: no active kontor-session key to attach a pid to")
	}
	return i.Keys.SetSessionPID(ctx, k.ID, pid)
}

func (i KontorSessionKeyIssuer) Current(ctx context.Context) (int, bool, error) {
	k, err := i.Keys.ActiveKontorSession(ctx)
	if err != nil || k == nil {
		return 0, false, err
	}
	if k.SessionPid == nil {
		return 0, true, nil
	}
	return *k.SessionPid, true, nil
}

func (i KontorSessionKeyIssuer) Revoke(ctx context.Context) error {
	if _, err := i.Keys.RevokeKontorSessions(ctx); err != nil {
		return fmt.Errorf("mcp: revoke kontor-session key: %w", err)
	}
	return nil
}
```
- [ ] Step 4: Run the same command. Expected PASS (4 tests).
- [ ] Step 5: Gate and commit.
  - Gate: `cd server && go vet ./... && go test -race ./internal/mcp/...`.
  - Restore ent: `git checkout -- server/internal/db/ent/`.
  - Commit: `git add server/internal/mcp/sessionkey.go server/internal/mcp/sessionkey_test.go && git commit -m "feat: mint a kontor session key with every scope except keys:manage"`.

---

### Task 3: Spawn policy accepts fixed extra roots
**Files:**
- Modify `server/internal/services/spawn_policy.go`: struct and constructor at lines 236-252, `Allow` at 277-318.
- Test in `server/internal/services/spawn_policy_extra_root_test.go` (new, `package services`).

**Interfaces:** Produces `func NewSpawnPolicy(roots RootsProvider, extraRoots ...string) SpawnPolicy`. It is variadic, so the callers at `router.go:510,512` and `spawn.go:103` compile unchanged.
**Acceptance:** A cwd under an extra root passes even when project roots are registered, and the sensitive-dir blacklist still wins over an extra root.

- [ ] Step 1: Write the failing test.
```go
package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpawnPolicy_ExtraRootAllowsCwdOutsideProjectRoots(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	session := filepath.Join(home, "cache", "kontor", "session")
	require.NoError(t, os.MkdirAll(project, 0o700))
	require.NoError(t, os.MkdirAll(session, 0o700))
	roots := func(context.Context) ([]string, error) { return []string{project}, nil }

	require.ErrorIs(t, NewSpawnPolicy(roots).Allow(t.Context(), session), ErrCwdNotAllowed)
	require.NoError(t, NewSpawnPolicy(roots, session).Allow(t.Context(), session))
	require.ErrorIs(t, NewSpawnPolicy(roots, session).Allow(t.Context(), filepath.Join(home, "elsewhere")), ErrCwdNotAllowed)
}

func TestSpawnPolicy_BlacklistBeatsExtraRoot(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	under := filepath.Join(home, ".claude", "kontor")
	require.NoError(t, os.MkdirAll(under, 0o700))

	require.ErrorIs(t, NewSpawnPolicy(nil, under).Allow(t.Context(), under), ErrCwdBlacklisted)
}
```
- [ ] Step 2: Run `cd server && go test ./internal/services/ -run 'TestSpawnPolicy_(ExtraRoot|BlacklistBeats)' -v`. Expected FAIL: `too many arguments in call to NewSpawnPolicy`.
- [ ] Step 3: Minimal implementation.
```go
type spawnPolicy struct {
	roots      RootsProvider // may be nil → no project-root restriction
	extraRoots []string
}

// NewSpawnPolicy constructs a SpawnPolicy. roots provides the set of allowed
// project root paths; pass nil to enforce only the sensitive-dir blacklist.
// extraRoots are fixed directories the server owns (the Kontor session dir);
// they pass the root check but never the blacklist.
func NewSpawnPolicy(roots RootsProvider, extraRoots ...string) SpawnPolicy {
	return &spawnPolicy{roots: roots, extraRoots: extraRoots}
}
```
In `Allow`, directly after the `checkBlacklist` block:
```go
	for _, root := range p.extraRoots {
		if rootAbs, err := canonicalize(root); err == nil && isUnder(cwdAbs, rootAbs) {
			return nil
		}
	}
```
- [ ] Step 4: Run the same command. Expected PASS. Also run `cd server && go test ./internal/services/ -run SpawnPolicy`: existing tests stay green.
- [ ] Step 5: Gate and commit.
  - Gate: `cd server && go vet ./... && go test -race ./internal/services/...`.
  - Restore ent: `git checkout -- server/internal/db/ent/`.
  - Commit: `git add server/internal/services/spawn_policy.go server/internal/services/spawn_policy_extra_root_test.go && git commit -m "feat: let the spawn policy admit fixed server-owned roots"`.

---

### Task 4: Owned Kontor spawn on SpawnManager
**Files:** Create `server/internal/api/agents/spawn_session.go`. Test in `server/internal/api/agents/spawn_session_test.go` (new, `package agents`).
**Interfaces:** Consumes: `m.spawnPolicy.Allow`, `m.spawnerRepo.GetDefault`, `buildSpawnArgs`, `nativeClaudeAdapter`, `spawnerArgsControlPermissionMode`, `resolveSpawnEnv`, `launchInteractive`, `processAlive`, `channelconfig.DiscoveryFile`. Produces:
```go
type SessionSpawnOptions struct {
	Cwd, Prompt, AppendSystemPrompt, Name, MCPConfigPath string
	AllowedTools []string
	OnExit       func(pid int)
}
func (m *SpawnManager) SpawnSession(ctx context.Context, opts SessionSpawnOptions) (int, error)
func (m *SpawnManager) TerminateSession(pid int) error
func ProcessAlive(pid int) bool
func WaitForExit(pid int)
```
**Acceptance:** `SpawnSession` launches the default native claude with `--session-id <uuid> --permission-mode default --append-system-prompt … -n Kontor --allowedTools … --mcp-config <path> --strict-mcp-config <prompt>`, through the spawn policy and `launchInteractive`, and calls `OnExit(pid)` once the process is gone. The default spawner's own permission flags are replaced by `--permission-mode default` (the live default runs `auto`), and an empty prompt adds no positional argument.

- [ ] Step 1: Write the failing test.
```go
package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// stubPIDExec replaces the transport with `sh -c 'echo $$'`: the pty path
// reads the printed pid, then the watcher sees sh exit. No agent is started.
func stubPIDExec(t *testing.T) *[]string {
	t.Helper()
	prevLook := lookTmuxPath
	lookTmuxPath = func() string { return "" }
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	var captured []string
	orig := execStart
	execStart = func(cmd *exec.Cmd) error {
		captured = slices.Clone(cmd.Args)
		cmd.Path, cmd.Args, cmd.Err = sh, []string{sh, "-c", "echo $$"}, nil
		return cmd.Start()
	}
	t.Cleanup(func() { execStart, lookTmuxPath = orig, prevLook })
	return &captured
}

func sessionHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("HOME", home)
	return home
}

func TestSpawnSession_BuildsKontorArgsAndReportsExit(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	cwd := filepath.Join(home, "session")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	exited := make(chan int, 1)
	pid, err := m.SpawnSession(t.Context(), SessionSpawnOptions{
		Cwd: cwd, Prompt: "hallo", AppendSystemPrompt: "brief", Name: "Kontor",
		MCPConfigPath: "/tmp/kontor.json",
		AllowedTools:  []string{"mcp__kontor-tasks__get_task", "mcp__kontor-tasks__list_tasks"},
		OnExit:        func(p int) { exited <- p },
	})
	require.NoError(t, err)
	require.Positive(t, pid)

	args := *captured
	i := slices.Index(args, "--session-id")
	require.GreaterOrEqual(t, i, 0)
	_, err = uuid.Parse(args[i+1])
	require.NoError(t, err)
	require.True(t, containsConsecutive(args, "--permission-mode", "default"))
	require.True(t, containsConsecutive(args, "--append-system-prompt", "brief"))
	require.True(t, containsConsecutive(args, "-n", "Kontor"))
	require.True(t, containsConsecutive(args, "--allowedTools", "mcp__kontor-tasks__get_task"))
	require.True(t, containsConsecutive(args, "mcp__kontor-tasks__get_task", "mcp__kontor-tasks__list_tasks"))
	require.True(t, containsConsecutive(args, "--mcp-config", "/tmp/kontor.json"))
	require.Equal(t, []string{"--strict-mcp-config", "hallo"}, args[len(args)-2:],
		"the prompt must follow a boolean flag, never the variadic --allowedTools/--mcp-config")

	select {
	case got := <-exited:
		require.Equal(t, pid, got)
	case <-time.After(10 * time.Second):
		t.Fatal("OnExit was not called after the process exited")
	}
}

func TestSpawnSession_PolicyRefusalSpawnsNothing(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	cwd := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	_, err := m.SpawnSession(t.Context(), SessionSpawnOptions{Cwd: cwd, Prompt: "x", Name: "Kontor", MCPConfigPath: "/tmp/k.json"})
	require.ErrorIs(t, err, services.ErrCwdBlacklisted)
	require.Nil(t, *captured)
}

// The operator's default spawner runs --permission-mode auto; a Kontor session
// must still prompt before a write, so the spawner's posture is replaced.
func TestSpawnSession_OverridesTheSpawnersPermissionPosture(t *testing.T) {
	home := sessionHome(t)
	captured := stubPIDExec(t)
	stored := []string{"--permission-mode", "auto", "--dangerously-skip-permissions", "--verbose"}
	row := &ent.Spawner{ID: "d", Name: "auto", AdapterType: "claude", Command: "claude", IsDefault: true, Args: slices.Clone(stored)}
	m := NewSpawnManager(0, 0, 0, 0, &fakeSpawnerRepo{byID: map[string]*ent.Spawner{"d": row}}, services.NewSpawnPolicy(nil))

	_, err := m.SpawnSession(t.Context(), SessionSpawnOptions{Cwd: home, Name: "Kontor", MCPConfigPath: "/tmp/k.json"})
	require.NoError(t, err)
	args := *captured
	require.True(t, containsConsecutive(args, "--permission-mode", "default"))
	require.NotContains(t, args, "auto")
	require.NotContains(t, args, "--dangerously-skip-permissions")
	require.Contains(t, args, "--verbose", "other spawner args survive")
	require.Equal(t, "--strict-mcp-config", args[len(args)-1], "an empty prompt adds no positional argument")
	require.Equal(t, stored, row.Args, "the stored spawner row is not mutated")
}

func TestTerminateSession_RefusesAPidItDidNotSpawn(t *testing.T) {
	sessionHome(t)
	sleeper := exec.Command("sleep", "30")
	require.NoError(t, sleeper.Start())
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })

	m := NewSpawnManager(0, 0, 0, 0, nil, services.NewSpawnPolicy(nil))
	require.Error(t, m.TerminateSession(sleeper.Process.Pid))
	require.True(t, ProcessAlive(sleeper.Process.Pid))
}
```
- [ ] Step 2: Run `cd server && go test ./internal/api/agents/ -run 'TestSpawnSession|TestTerminateSession' -v`. Expected FAIL: `undefined: SessionSpawnOptions` / `m.SpawnSession undefined`.
- [ ] Step 3: Minimal implementation, `server/internal/api/agents/spawn_session.go`.
```go
package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// SessionSpawnOptions describe a server-owned interactive claude session.
// MCPConfigPath belongs to the caller: the manager neither writes nor removes it.
type SessionSpawnOptions struct {
	Cwd                string
	Prompt             string
	AppendSystemPrompt string
	Name               string
	MCPConfigPath      string
	AllowedTools       []string
	OnExit             func(pid int)
}

// SpawnSession starts the default spawner's native claude for a server-owned
// session. OnExit runs once the process is gone, with no deadline: unlike
// pollExitWatch, a session has no idle timeout.
func (m *SpawnManager) SpawnSession(ctx context.Context, opts SessionSpawnOptions) (int, error) {
	if err := m.spawnPolicy.Allow(ctx, opts.Cwd); err != nil {
		return 0, err
	}
	row, err := m.defaultSpawner(ctx)
	if err != nil {
		return 0, err
	}
	if !nativeClaudeAdapter(row) {
		return 0, fmt.Errorf("default spawner %q is not a claude adapter", row.Name)
	}
	row = withoutPermissionPosture(row)
	req := &spawnRequest{cwd: opts.Cwd, permissionMode: "default"}
	if row != nil && row.ModelOverride != nil {
		req.model = *row.ModelOverride
	}
	binary, args, err := m.buildSpawnArgs(req, row)
	if err != nil {
		return 0, err
	}
	args = append(args, "--append-system-prompt", opts.AppendSystemPrompt, "-n", opts.Name)
	if len(opts.AllowedTools) > 0 {
		args = append(append(args, "--allowedTools"), opts.AllowedTools...)
	}
	args = append(args, "--mcp-config", opts.MCPConfigPath, "--strict-mcp-config")
	if opts.Prompt != "" {
		args = append(args, opts.Prompt)
	}

	pid, watch, err := m.launchInteractive(binary, args, resolveSpawnEnv(row), opts.Cwd, "")
	if err != nil {
		return 0, fmt.Errorf("spawn failed: %w", err)
	}
	if pid == 0 {
		return 0, errors.New("spawn failed: the transport reported no pid")
	}
	m.mu.Lock()
	m.spawnStore[pid] = &SpawnStatus{PID: pid, Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339),
		Prompt: opts.Prompt[:min(len(opts.Prompt), 200)], Cwd: opts.Cwd}
	m.mu.Unlock()
	go func() {
		watch()
		WaitForExit(pid)
		if opts.OnExit != nil {
			opts.OnExit(pid)
		}
	}()
	return pid, nil
}

func (m *SpawnManager) defaultSpawner(ctx context.Context) (*ent.Spawner, error) {
	if m.spawnerRepo == nil {
		return nil, nil
	}
	row, err := m.spawnerRepo.GetDefault(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("default spawner lookup failed: %w", err)
	}
	return row, nil
}

// withoutPermissionPosture returns a copy of row without the flags
// spawnerArgsControlPermissionMode recognises, so buildSpawnArgs appends the
// session's own --permission-mode. The stored row is never mutated.
func withoutPermissionPosture(row *ent.Spawner) *ent.Spawner {
	if row == nil || !spawnerArgsControlPermissionMode(row.Args) {
		return row
	}
	clone := *row
	clone.Args = nil
	for i := 0; i < len(row.Args); i++ {
		a := row.Args[i]
		switch {
		case a == "--permission-mode":
			i++ // skip its value
		case strings.HasPrefix(a, "--permission-mode="),
			a == "--dangerously-skip-permissions",
			a == "--allow-dangerously-skip-permissions":
		default:
			clone.Args = append(clone.Args, a)
		}
	}
	return &clone
}

// TerminateSession sends SIGTERM to a session this dashboard started: one
// still running in this manager, or one whose channel bridge wrote a
// discovery file (a session reattached after a restart). A bare pid could
// by then belong to an unrelated process.
func (m *SpawnManager) TerminateSession(pid int) error {
	if pid <= 1 {
		return fmt.Errorf("agents: refusing to signal pid %d", pid)
	}
	s := m.GetStatus(pid)
	if (s == nil || s.Status != "running") && !hasDiscoveryFile(pid) {
		return fmt.Errorf("agents: pid %d is not a session this dashboard spawned", pid)
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func hasDiscoveryFile(pid int) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(channelconfig.DiscoveryFile(home, pid))
	return err == nil
}

func ProcessAlive(pid int) bool { return processAlive(pid) }

func WaitForExit(pid int) {
	for processAlive(pid) {
		time.Sleep(2 * time.Second)
	}
}
```
(`-pid` reaches claude's MCP children when the pty host made claude a group leader. `ESRCH` falls back to the pid, the same as `pipeline.syscallKill`.)
- [ ] Step 4: Run the same command. Expected PASS (4 tests). Then run `cd server && go test ./internal/api/agents/`: the existing spawn tests stay green.
- [ ] Step 5: Gate and commit.
  - Gate: `cd server && go vet ./... && go test -race ./internal/api/agents/...`.
  - Restore ent: `git checkout -- server/internal/db/ent/`.
  - Commit: `git add server/internal/api/agents/spawn_session.go server/internal/api/agents/spawn_session_test.go && git commit -m "feat: spawn a server-owned claude session with an exit callback"`.

---

### Task 5: `kontorsession` service
**Files:**
- Create `server/internal/kontorsession/service.go` and `server/internal/kontorsession/briefing.md`.
- Test in `server/internal/kontorsession/service_test.go`.

**Interfaces:** Consumes: `mcp.KontorSessionKeyIssuer` (Task 2), `mcp.KontorSessionAllowedTools`, `channelconfig.SelfBinaryPath`, `channelconfig.WriteTempConfig`, `channelconfig.TaskAPI`, `claudeconfig.UserMCPServers`, `repo.AuditEventRepo.RecordAudit`. Produces:
```go
type Service struct {
	Keys       mcp.KontorSessionKeyIssuer
	Audit      repo.AuditEventRepo // nil disables auditing
	TaskAPIURL string              // e.g. http://127.0.0.1:<port>/api/mcp
	Dir        string              // os.UserCacheDir()/kontor/session
	Spawn      func(context.Context, SpawnOptions) (int, error)
	Terminate  func(pid int) error
	Alive      func(pid int) bool
	WaitExit   func(pid int)
	// unexported: mu sync.Mutex; cfgPath string
}
func (s *Service) Current(ctx context.Context) (int, bool, error)
func (s *Service) Start(ctx context.Context, prompt string) (int, error)
func (s *Service) Renew(ctx context.Context, prompt string) (int, error)
func (s *Service) End(ctx context.Context, reason string) error
func (s *Service) Reconcile(ctx context.Context) error

// SpawnOptions mirrors agents.SessionSpawnOptions field for field, so DI adapts
// with a plain conversion and this package imports nothing from internal/api.
type SpawnOptions struct {
	Cwd                string
	Prompt             string
	AppendSystemPrompt string
	Name               string
	MCPConfigPath      string
	AllowedTools       []string
	OnExit             func(pid int)
}
```
The func fields are the test seams (the pipeline's `SpawnFn`/`IssueTaskAPIKey` style). No interfaces with one implementation. The real issuer runs on an in-memory DB in tests.

**Acceptance:** One session at a time; renew ends before it starts; a failed spawn revokes the key; reconcile ends a dead pid and re-arms a live one; an old process's exit never ends a newer session.

- [ ] Step 1: Write the failing test.
```go
package kontorsession_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/kontorsession"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

type fakeProc struct {
	mu      sync.Mutex
	next    int
	alive   map[int]bool
	events  []string
	spawns  []kontorsession.SpawnOptions
	failErr error
	exits   map[int]chan struct{}
}

func (f *fakeProc) spawn(_ context.Context, o kontorsession.SpawnOptions) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return 0, f.failErr
	}
	f.next++
	pid := 1000 + f.next
	f.alive[pid] = true
	f.spawns = append(f.spawns, o)
	f.events = append(f.events, "spawn")
	return pid, nil
}
func (f *fakeProc) terminate(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive[pid] = false
	f.events = append(f.events, "terminate")
	return nil
}
func (f *fakeProc) isAlive(pid int) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.alive[pid] }
func (f *fakeProc) wait(pid int)         { <-f.exits[pid] }

func newService(t *testing.T) (*kontorsession.Service, *fakeProc, repo.ApiKeyRepo) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	keys := repo.NewApiKeyRepo(bundle.Client)
	f := &fakeProc{alive: map[int]bool{}, exits: map[int]chan struct{}{}}
	return &kontorsession.Service{
		Keys: mcp.KontorSessionKeyIssuer{Keys: keys}, TaskAPIURL: "http://127.0.0.1:1/api/mcp",
		Dir: t.TempDir(), Spawn: f.spawn, Terminate: f.terminate, Alive: f.isAlive, WaitExit: f.wait,
	}, f, keys
}

func TestStart_OneSessionAtATime(t *testing.T) {
	s, f, _ := newService(t)
	var wg sync.WaitGroup
	pids := make([]int, 2)
	for i := range pids {
		wg.Go(func() {
			pid, err := s.Start(t.Context(), "hallo")
			require.NoError(t, err)
			pids[i] = pid
		})
	}
	wg.Wait()
	require.Equal(t, pids[0], pids[1])
	require.Len(t, f.spawns, 1)

	o := f.spawns[0]
	require.Equal(t, s.Dir, o.Cwd)
	require.Equal(t, "Kontor", o.Name)
	require.NotEmpty(t, o.AppendSystemPrompt)
	require.Equal(t, mcp.KontorSessionAllowedTools(), o.AllowedTools)
	cfg, err := os.ReadFile(o.MCPConfigPath)
	require.NoError(t, err)
	require.Contains(t, string(cfg), mcp.ServerName)
}

func TestStart_FailedSpawnRevokesTheKey(t *testing.T) {
	s, f, keys := newService(t)
	f.failErr = errors.New("boom")
	_, err := s.Start(t.Context(), "hallo")
	require.ErrorContains(t, err, "boom")
	k, err := keys.ActiveKontorSession(t.Context())
	require.NoError(t, err)
	require.Nil(t, k)
}

func TestRenew_EndsBeforeItStarts(t *testing.T) {
	s, f, _ := newService(t)
	first, err := s.Start(t.Context(), "a")
	require.NoError(t, err)
	cfg := f.spawns[0].MCPConfigPath
	second, err := s.Renew(t.Context(), "b")
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.Equal(t, []string{"spawn", "terminate", "spawn"}, f.events)
	_, statErr := os.Stat(cfg)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestExit_OfAnOldProcessDoesNotEndTheRenewedSession(t *testing.T) {
	s, f, _ := newService(t)
	first, err := s.Start(t.Context(), "a")
	require.NoError(t, err)
	second, err := s.Renew(t.Context(), "b")
	require.NoError(t, err)
	f.spawns[0].OnExit(first)
	pid, ok, err := s.Current(t.Context())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, second, pid)
}

func TestEnd_WithoutSessionIsNotAnError(t *testing.T) {
	s, _, _ := newService(t)
	require.NoError(t, s.End(t.Context(), "operator"))
}

func TestReconcile_EndsADeadPid(t *testing.T) {
	s, f, keys := newService(t)
	pid, err := s.Start(t.Context(), "a")
	require.NoError(t, err)
	f.alive[pid] = false
	restarted := &kontorsession.Service{Keys: s.Keys, Dir: s.Dir, Spawn: f.spawn, Terminate: f.terminate, Alive: f.isAlive, WaitExit: f.wait}
	require.NoError(t, restarted.Reconcile(t.Context()))
	k, err := keys.ActiveKontorSession(t.Context())
	require.NoError(t, err)
	require.Nil(t, k)
}

func TestReconcile_RearmsALivePid(t *testing.T) {
	s, f, keys := newService(t)
	pid, err := s.Start(t.Context(), "a")
	require.NoError(t, err)
	f.exits[pid] = make(chan struct{})
	restarted := &kontorsession.Service{Keys: s.Keys, Dir: s.Dir, Spawn: f.spawn, Terminate: f.terminate, Alive: f.isAlive, WaitExit: f.wait}
	require.NoError(t, restarted.Reconcile(t.Context()))
	_, ok, err := restarted.Current(t.Context())
	require.NoError(t, err)
	require.True(t, ok)

	f.mu.Lock()
	f.alive[pid] = false
	f.mu.Unlock()
	close(f.exits[pid])
	require.Eventually(t, func() bool {
		k, _ := keys.ActiveKontorSession(context.Background())
		return k == nil
	}, 2*time.Second, 10*time.Millisecond)
}
```
(If `claudeconfig.JSONPath` honours `CLAUDE_CONFIG_DIR`, also `t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())`.)
- [ ] Step 2: Run `cd server && go test ./internal/kontorsession/ -v`. Expected FAIL: `no non-test Go files` / `undefined: kontorsession.Service`.
- [ ] Step 3: Minimal implementation.

`briefing.md`:
```markdown
# Kontor session

You are the operator's Kontor session: a Claude Code session inside Kontor, the
dashboard that runs their task pipeline. The `kontor-tasks` MCP server is your
handle on it — tasks and projects, refinement, plans and approvals, permission
requests, schedules, memory, Obsidian and GitHub. Read tools run freely; every
write asks the operator first.

- Talk first. Spar, answer, read state. Create no task or backlog item unless the
  operator asks for one.
- When you do create a task, pick its project from context (`list_projects`).
  If you cannot tell, ask.
- Your working directory is scratch space. Work on a project through Kontor's
  tools, not by editing files here.
```
`service.go`:
```go
package kontorsession

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

//go:embed briefing.md
var briefing string

// SpawnOptions mirrors agents.SessionSpawnOptions field for field, so DI adapts
// with a plain conversion and this package imports nothing from internal/api.
type SpawnOptions struct {
	Cwd                string
	Prompt             string
	AppendSystemPrompt string
	Name               string
	MCPConfigPath      string
	AllowedTools       []string
	OnExit             func(pid int)
}

// Service owns the one Kontor session. The active kontor_session key is the
// session record; mu serialises every transition.
type Service struct {
	Keys       mcp.KontorSessionKeyIssuer
	Audit      repo.AuditEventRepo
	TaskAPIURL string
	Dir        string
	Spawn      func(context.Context, SpawnOptions) (int, error)
	Terminate  func(pid int) error
	Alive      func(pid int) bool
	WaitExit   func(pid int)

	mu      sync.Mutex
	cfgPath string // lost on restart; SweepOrphanedConfigs removes it then
}

func (s *Service) Current(ctx context.Context) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current(ctx)
}

func (s *Service) current(ctx context.Context) (int, bool, error) {
	pid, ok, err := s.Keys.Current(ctx)
	if err != nil || !ok || pid == 0 {
		return 0, false, err
	}
	return pid, true, nil
}

func (s *Service) Start(ctx context.Context, prompt string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pid, ok, err := s.current(ctx); err != nil || ok {
		return pid, err
	}
	return s.start(ctx, prompt)
}

func (s *Service) Renew(ctx context.Context, prompt string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.end(ctx, "renewed"); err != nil {
		return 0, err
	}
	return s.start(ctx, prompt)
}

func (s *Service) End(ctx context.Context, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.end(ctx, reason)
}

// Reconcile runs once at boot: a recorded pid that is gone (or never
// attached) ends the session, a live one gets its exit watcher back.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pid, ok, err := s.Keys.Current(ctx)
	if err != nil || !ok {
		return err
	}
	if pid == 0 || !s.Alive(pid) {
		return s.end(ctx, "process gone at boot")
	}
	go func() {
		s.WaitExit(pid)
		s.exited(pid)
	}()
	return nil
}

func (s *Service) start(ctx context.Context, prompt string) (int, error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return 0, fmt.Errorf("kontorsession: session dir: %w", err)
	}
	token, err := s.Keys.Issue(ctx)
	if err != nil {
		return 0, err
	}
	cfg, err := s.writeConfig(token)
	if err != nil {
		return 0, errors.Join(err, s.Keys.Revoke(ctx))
	}
	pid, err := s.Spawn(ctx, SpawnOptions{
		Cwd: s.Dir, Prompt: prompt, AppendSystemPrompt: briefing, Name: "Kontor",
		MCPConfigPath: cfg, AllowedTools: mcp.KontorSessionAllowedTools(), OnExit: s.exited,
	})
	if err == nil {
		err = s.Keys.Attach(ctx, pid)
		if err != nil {
			err = errors.Join(err, s.Terminate(pid))
		}
	}
	if err != nil {
		_ = os.Remove(cfg)
		return 0, errors.Join(err, s.Keys.Revoke(ctx))
	}
	s.cfgPath = cfg
	s.audit(ctx, "kontor_session.start", pid, "")
	return pid, nil
}

// end is the single exit path: stop the process, revoke the key, remove the
// config, audit. Ending no session is not an error.
func (s *Service) end(ctx context.Context, reason string) error {
	pid, ok, err := s.Keys.Current(ctx)
	if err != nil {
		return err
	}
	if ok && pid > 0 && s.Alive(pid) {
		if err := s.Terminate(pid); err != nil {
			return fmt.Errorf("kontorsession: stop pid %d: %w", pid, err)
		}
	}
	if ok {
		if err := s.Keys.Revoke(ctx); err != nil {
			return err
		}
		s.audit(ctx, "kontor_session.end", pid, reason)
	}
	if s.cfgPath != "" {
		_ = os.Remove(s.cfgPath)
		s.cfgPath = ""
	}
	return nil
}

// exited fires from the watcher of every session ever started, so it ends
// only the session still recorded for that pid — never a renewed one.
func (s *Service) exited(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	cur, ok, err := s.current(ctx)
	if err != nil || !ok || cur != pid {
		return
	}
	if err := s.end(ctx, "process exited"); err != nil {
		slog.Warn("kontorsession: end after exit failed", "pid", pid, "err", err)
	}
}

func (s *Service) writeConfig(token string) (string, error) {
	self, err := channelconfig.SelfBinaryPath()
	if err != nil {
		return "", fmt.Errorf("kontorsession: self binary: %w", err)
	}
	servers, err := claudeconfig.UserMCPServers()
	if err != nil {
		slog.Warn("kontorsession: user MCP servers unreadable, starting without them", "err", err)
	}
	return channelconfig.WriteTempConfig(self, &channelconfig.TaskAPI{URL: s.TaskAPIURL, Token: token}, servers)
}

func (s *Service) audit(ctx context.Context, action string, pid int, reason string) {
	if s.Audit == nil {
		return
	}
	if err := s.Audit.RecordAudit(ctx, nil, action, strconv.Itoa(pid), map[string]any{"reason": reason}); err != nil {
		slog.Warn("kontorsession: audit write failed", "action", action, "err", err)
	}
}
```
Notes for the implementer:
- **Terminate ordering.** `Terminate` failing keeps the key active, so the record matches the still-running process and the operator can retry. That is the spec's order: stop, then revoke.
- **Write order.** Every DB write here is a single statement. No transaction is open when `Spawn` runs.
- [ ] Step 4: Run the same command. Expected PASS (7 tests), and `go test -race` clean.
- [ ] Step 5: Gate and commit.
  - Gate: `cd server && go vet ./... && go test -race ./internal/kontorsession/...`.
  - Restore ent: `git checkout -- server/internal/db/ent/`.
  - Commit: `git add server/internal/kontorsession && git commit -m "feat: keep one kontor session alive until it is ended or renewed"`.

---

### Task 6: HTTP surface, route registration, DI wiring
**Files:**
- Create `server/internal/api/kontorsession/handler.go` (package `kontorsession`).
- Test in `server/internal/api/kontorsession/handler_test.go`.
- Modify `server/internal/api/router.go`: `RouterDeps` (add the field before the closing brace at ~line 206) and the protected group (mount right after the `TrackerHandler` mount, ~line 499, before the spawn block).
- Modify `server/serverapp/di.go`: build before `routerDeps` (~line 1088) and set the field in `api.RouterDeps{…}` (~1090-1150).
- Modify `server/internal/api/bypass_auth_smoke_test.go` (`buildBypassRouter`, ~line 175) and regenerate `server/internal/api/testdata/routes.golden`.

**Interfaces:** Consumes: `*kontorsession.Service` (Task 5), `agents.NewSpawnManager`, `SpawnSession`, `TerminateSession`, `ProcessAlive`, `WaitForExit` (Task 4), `services.NewSpawnPolicy(roots, extra...)` (Task 3), `services.ProjectFolderRootsProvider`. Produces:
```go
func New(s sessions) *Handler
func (h *Handler) Mount(r chi.Router)
RouterDeps.KontorSessionHandler *apikontorsession.Handler
```
**Acceptance:** `GET` returns `{"pid":n}` or `{"pid":null}`; `POST` starts the session or returns the running one; `POST /renew` replaces it (its prompt may be empty); `DELETE` ends it (204, idempotent); all four sit in the protected group and the server reconciles the session at boot.

- [ ] Step 1: Write the failing test.
```go
package kontorsession_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/kontorsession"
)

type fakeSessions struct {
	pid      int
	running  bool
	startErr error
	started  []string
	ended    []string
}

func (f *fakeSessions) Current(context.Context) (int, bool, error) { return f.pid, f.running, nil }
func (f *fakeSessions) Start(_ context.Context, p string) (int, error) {
	if f.startErr != nil {
		return 0, f.startErr
	}
	f.started = append(f.started, p)
	f.pid, f.running = 7, true
	return 7, nil
}
func (f *fakeSessions) Renew(_ context.Context, p string) (int, error) {
	f.started = append(f.started, "renew:"+p)
	f.pid, f.running = 8, true
	return 8, nil
}
func (f *fakeSessions) End(_ context.Context, reason string) error {
	f.ended = append(f.ended, reason)
	f.running = false
	return nil
}

func do(f *fakeSessions, method, path, body string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	kontorsession.New(f).Mount(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestGet_NoSessionIsNull(t *testing.T) {
	rec := do(&fakeSessions{}, http.MethodGet, "/api/kontor-session", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":null}`, rec.Body.String())
}

func TestPost_StartsWithThePrompt(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session", `{"prompt":"hallo"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":7}`, rec.Body.String())
	require.Equal(t, []string{"hallo"}, f.started)
	require.JSONEq(t, `{"pid":7}`, do(f, http.MethodGet, "/api/kontor-session", "").Body.String())
}

func TestPost_BlankPromptIsRejected(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session", `{"prompt":"  "}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, f.started)
}

func TestPost_StartFailureCarriesTheMessage(t *testing.T) {
	rec := do(&fakeSessions{startErr: errors.New("mint failed")}, http.MethodPost, "/api/kontor-session", `{"prompt":"x"}`)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "mint failed")
}

func TestRenew_ReplacesTheSession(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session/renew", `{"prompt":"neu"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"pid":8}`, rec.Body.String())
}

func TestRenew_AcceptsABlankPrompt(t *testing.T) {
	f := &fakeSessions{}
	rec := do(f, http.MethodPost, "/api/kontor-session/renew", `{"prompt":""}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"renew:"}, f.started)
}

func TestDelete_EndsAndIsIdempotent(t *testing.T) {
	f := &fakeSessions{}
	require.Equal(t, http.StatusNoContent, do(f, http.MethodDelete, "/api/kontor-session", "").Code)
	require.Equal(t, []string{"ended by operator"}, f.ended)
}
```
- [ ] Step 2: Run `cd server && go test ./internal/api/kontorsession/ -v`. Expected FAIL: `no non-test Go files in …/api/kontorsession`.
- [ ] Step 3: Minimal implementation.

`handler.go`:
```go
// Package kontorsession serves the Kontor session's HTTP surface.
package kontorsession

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// sessions is the test seam over *kontorsession.Service.
type sessions interface {
	Current(ctx context.Context) (int, bool, error)
	Start(ctx context.Context, prompt string) (int, error)
	Renew(ctx context.Context, prompt string) (int, error)
	End(ctx context.Context, reason string) error
}

type Handler struct{ svc sessions }

func New(s sessions) *Handler { return &Handler{svc: s} }

func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/kontor-session", apierr.ErrorMiddleware(h.get))
	r.Post("/api/kontor-session", apierr.ErrorMiddleware(h.start))
	r.Post("/api/kontor-session/renew", apierr.ErrorMiddleware(h.renew))
	r.Delete("/api/kontor-session", apierr.ErrorMiddleware(h.end))
}

type view struct {
	PID *int `json:"pid"`
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	pid, ok, err := h.svc.Current(r.Context())
	if err != nil {
		return err
	}
	if !ok {
		apierr.WriteJSON(w, http.StatusOK, view{})
		return nil
	}
	apierr.WriteJSON(w, http.StatusOK, view{PID: &pid})
	return nil
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) error {
	return h.spawn(w, r, h.svc.Start, true)
}

func (h *Handler) renew(w http.ResponseWriter, r *http.Request) error {
	return h.spawn(w, r, h.svc.Renew, false)
}

func (h *Handler) spawn(w http.ResponseWriter, r *http.Request, fn func(context.Context, string) (int, error), promptRequired bool) error {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if promptRequired && strings.TrimSpace(body.Prompt) == "" {
		return apierr.NewAppError(http.StatusBadRequest, "prompt is required")
	}
	pid, err := fn(r.Context(), body.Prompt)
	if err != nil {
		return apierr.NewAppError(http.StatusInternalServerError, "kontor session: "+err.Error())
	}
	apierr.WriteJSON(w, http.StatusOK, view{PID: &pid})
	return nil
}

func (h *Handler) end(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.End(r.Context(), "ended by operator"); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
```
(Check how `apierr.ErrorMiddleware` renders an `*AppError`, and adjust the `Contains` assertion to its JSON shape.)

`router.go`: add the import `apikontorsession "github.com/lx-wnk/kontor/server/internal/api/kontorsession"` and the field `KontorSessionHandler *apikontorsession.Handler` in `RouterDeps`. In the protected group, after the `TrackerHandler` mount:
```go
		if deps.KontorSessionHandler != nil {
			deps.KontorSessionHandler.Mount(r)
		}
```
`di.go`, before `routerDeps := api.RouterDeps{` (imports: `kontorsessionapi ".../internal/api/kontorsession"`, `".../internal/kontorsession"`, `".../internal/mcp"`, `".../internal/services"`, `os`, `path/filepath`):
```go
	var kontorSessionHandler *kontorsessionapi.Handler
	if apiKeyRepo != nil {
		if cacheDir, err := os.UserCacheDir(); err != nil {
			slog.Warn("kontor session disabled: no user cache dir", "err", err)
		} else {
			sessionDir := filepath.Join(cacheDir, "kontor", "session")
			policy := services.NewSpawnPolicy(services.ProjectFolderRootsProvider(projectRepo, projectFolderRepo), sessionDir)
			kontorSpawns := agents.NewSpawnManager(0, 0, 0, 0, spawnerRepo, policy)
			kontorSvc := &kontorsession.Service{
				Keys:       mcp.KontorSessionKeyIssuer{Keys: apiKeyRepo},
				Audit:      auditEventRepo,
				TaskAPIURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.Port) + mcp.EndpointPath,
				Dir:        sessionDir,
				Spawn: func(ctx context.Context, o kontorsession.SpawnOptions) (int, error) {
					return kontorSpawns.SpawnSession(ctx, agents.SessionSpawnOptions(o))
				},
				Terminate:  kontorSpawns.TerminateSession,
				Alive:      agents.ProcessAlive,
				WaitExit:   agents.WaitForExit,
			}
			if err := kontorSvc.Reconcile(ctx); err != nil {
				slog.Warn("kontor session: reconcile failed", "err", err)
			}
			kontorSessionHandler = kontorsessionapi.New(kontorSvc)
		}
	}
```
Then add `KontorSessionHandler: kontorSessionHandler,` to `api.RouterDeps{…}`.

Why a dedicated `SpawnManager`: the router builds its own inside `NewRouter` (`router.go:508-517`), out of DI's reach. Only the Kontor manager's policy carries the extra root, so "+ New Agent" cannot target the cache dir. Terminal and message routes resolve by pid through discovery files, so they work for this pid. Zero rate-limit args fall back to the defaults, and `SpawnSession` never consults the limiter.

`bypass_auth_smoke_test.go`, in `buildBypassRouter` before `return NewRouter(deps)`, wire a stub so the walk covers the routes and nothing can spawn:
```go
	deps.KontorSessionHandler = apikontorsession.New(stubKontorSessions{})
```
with, in the same file:
```go
type stubKontorSessions struct{}

func (stubKontorSessions) Current(context.Context) (int, bool, error)     { return 0, false, nil }
func (stubKontorSessions) Start(context.Context, string) (int, error)     { return 1, nil }
func (stubKontorSessions) Renew(context.Context, string) (int, error)     { return 1, nil }
func (stubKontorSessions) End(context.Context, string) error              { return nil }
```
The smoke test's `{}` body gets 400 (prompt required), which is neither 401 nor 403, so the walk passes. Regenerate the golden with `cd server && go test ./internal/api/ -run TestRouteGolden -update-golden` and check that the diff adds exactly the four lines: `DELETE /api/kontor-session`, `GET /api/kontor-session`, `POST /api/kontor-session`, `POST /api/kontor-session/renew`.
- [ ] Step 4: Run `cd server && go test ./internal/api/kontorsession/ -v && go test ./internal/api/ -run 'TestRouteGolden|TestBypassAuth' -v && go build ./...`. Expected PASS.
- [ ] Step 5: Gate and commit.
  - Gate: `cd server && go vet ./... && go test -race ./internal/api/... ./internal/kontorsession/... ./serverapp/...`.
  - Restore ent: `git checkout -- server/internal/db/ent/`.
  - Commit: `git add server/internal/api/kontorsession server/internal/api/router.go server/internal/api/bypass_auth_smoke_test.go server/internal/api/testdata/routes.golden server/serverapp/di.go && git commit -m "feat: serve the kontor session over /api/kontor-session"`.


---

## Frontend

Ordering keeps every commit green: Task 9 adds `KontorTile` without deleting `MissionInput`; Task 10 swaps them and deletes `MissionInput`; Task 11 deletes `useCapture.ts` once Spotlight is its last importer. Between Task 8 and Task 10 the old input's badge already reads START KONTOR while it still captures — accepted for one commit.

### Task 7: Shared Kontor session state

**Files:**
- Create: `src/features/mission/composables/useKontorSession.ts`
- Test: `src/features/mission/__tests__/useKontorSession.test.ts`

**Interfaces:**
- Consumes: the wire contract in Global Constraints; `POST /api/agents/{pid}/message` body `{ "message": string }` (`spawn.go:903-918`); `useAgents({ autoStart: false }).agents` from `@/features/agents` (finished agents stay listed with `status === 'finished'`); `errorMessage`, `readErrorMessage` from `@/utils/errorMessage`.
- Produces:
```ts
export type KontorStatus = 'idle' | 'starting' | 'running' | 'error'
export function useKontorSession(): {
  pid: Ref<number | null>; status: Ref<KontorStatus>; error: Ref<string>
  refresh: () => Promise<void>
  start: (text: string) => Promise<boolean>   // true = delivered
  send: (text: string) => Promise<boolean>
  end: () => Promise<void>
  renew: (text: string) => Promise<boolean>
}
```

**Acceptance:** Every caller sees one session; `send` starts a session when none runs and messages it when one does (including one another window started); the pid clears when the agent goes away.

- [ ] **Step 1: Write the failing test**

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'

const agents = ref<Array<{ pid: number, status: string }>>([])
vi.mock('@/features/agents', () => ({ useAgents: () => ({ agents }) }))

type Mod = typeof import('../composables/useKontorSession')
let useKontorSession: Mod['useKontorSession']
const fetchMock = vi.fn()

function reply(body: unknown, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: async () => body }
}
function body(call: number) {
  return JSON.parse((fetchMock.mock.calls[call][1] as RequestInit).body as string)
}

beforeEach(async () => {
  // The state is module-level on purpose; a fresh module per test keeps it from leaking.
  vi.resetModules()
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
  agents.value = []
  ;({ useKontorSession } = await import('../composables/useKontorSession'))
})
afterEach(() => vi.unstubAllGlobals())

describe('useKontorSession', () => {
  it('reattaches to the running session and shares it with every caller', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    await s.refresh()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/kontor-session')
    expect(s.pid.value).toBe(1234)
    expect(s.status.value).toBe('running')
    expect(useKontorSession().pid.value).toBe(1234)
  })

  it('starts a session with the text as its first prompt when none runs', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: null })).mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    expect(await s.send('plan phase 4')).toBe(true)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/kontor-session')
    expect((fetchMock.mock.calls[1][1] as RequestInit).method).toBe('POST')
    expect(body(1)).toEqual({ prompt: 'plan phase 4' })
    expect(s.pid.value).toBe(1234)
    expect(s.status.value).toBe('running')
  })

  // POST returns a running session unchanged and drops its prompt, so text
  // for a session this window never saw must go as a message.
  it('sends to a session another window started instead of starting one', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 99 })).mockResolvedValue(reply({}))
    const s = useKontorSession()
    expect(await s.send('/compact')).toBe(true)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/agents/99/message')
    expect(body(1)).toEqual({ message: '/compact' })
    await s.send('again')
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('reports a start the server refused and holds no pid', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: null })).mockResolvedValueOnce(reply({ error: 'could not mint key' }, 500))
    const s = useKontorSession()
    expect(await s.send('hi')).toBe(false)
    expect(s.status.value).toBe('error')
    expect(s.error.value).toBe('could not mint key')
    expect(s.pid.value).toBeNull()
  })

  it('keeps the session when a message fails and says why', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 })).mockResolvedValueOnce(reply({ error: 'rate limited' }, 429))
    const s = useKontorSession()
    await s.refresh()
    expect(await s.send('hi')).toBe(false)
    expect(s.status.value).toBe('running')
    expect(s.error.value).toBe('rate limited')
  })

  it('ends the session', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 })).mockResolvedValueOnce(reply(null, 204))
    const s = useKontorSession()
    await s.refresh()
    await s.end()
    expect(fetchMock.mock.calls[1]).toEqual(['/api/kontor-session', { method: 'DELETE' }])
    expect(s.pid.value).toBeNull()
    expect(s.status.value).toBe('idle')
  })

  it('renews into a fresh pid and does not mistake the old one leaving for an exit', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 })).mockResolvedValueOnce(reply({ pid: 5678 }))
    const s = useKontorSession()
    await s.refresh()
    agents.value = [{ pid: 1234, status: 'active' }]
    await nextTick()
    expect(await s.renew('')).toBe(true)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/kontor-session/renew')
    expect(body(1)).toEqual({ prompt: '' })
    agents.value = []
    await nextTick()
    expect(s.pid.value).toBe(5678)
  })

  it('returns to idle when the session agent exits', async () => {
    fetchMock.mockResolvedValueOnce(reply({ pid: 1234 }))
    const s = useKontorSession()
    await s.refresh()
    agents.value = [{ pid: 1234, status: 'active' }]
    await nextTick()
    agents.value = [{ pid: 1234, status: 'finished' }]
    await nextTick()
    expect(s.pid.value).toBeNull()
    expect(s.status.value).toBe('idle')
  })
})
```

- [ ] **Step 2: Run it** — `pnpm vitest run src/features/mission/__tests__/useKontorSession.test.ts`. Expected: FAIL, the module cannot be resolved.

- [ ] **Step 3: Minimal implementation** (full file)

```ts
import { ref, watch } from 'vue'
import { useAgents } from '@/features/agents'
import { errorMessage, readErrorMessage } from '@/utils/errorMessage'

export type KontorStatus = 'idle' | 'starting' | 'running' | 'error'

const pid = ref<number | null>(null)
const status = ref<KontorStatus>('idle')
const error = ref('')
let watchingAgents = false

const SESSION_URL = '/api/kontor-session'
const JSON_HEADERS = { 'Content-Type': 'application/json' }

async function request(url: string, fallback: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(url, init)
  if (!res.ok)
    throw new Error(await readErrorMessage(res, fallback))
  return res
}

function settle(next: number | null) {
  pid.value = next
  status.value = next === null ? 'idle' : 'running'
}

async function refresh(): Promise<void> {
  const fallback = 'Could not read the Kontor session.'
  try {
    const res = await request(SESSION_URL, fallback)
    settle((await res.json() as { pid: number | null }).pid)
  }
  catch (e) {
    status.value = 'error'
    error.value = errorMessage(e, fallback)
  }
}

async function spawn(url: string, prompt: string): Promise<boolean> {
  const fallback = 'Could not start a Kontor session.'
  status.value = 'starting'
  error.value = ''
  try {
    const res = await request(url, fallback, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ prompt }) })
    settle((await res.json() as { pid: number | null }).pid)
    return pid.value !== null
  }
  catch (e) {
    pid.value = null
    status.value = 'error'
    error.value = errorMessage(e, fallback)
    return false
  }
}

const start = (text: string) => spawn(SESSION_URL, text)
const renew = (text: string) => spawn(`${SESSION_URL}/renew`, text)

async function send(text: string): Promise<boolean> {
  // POST returns an already-running session unchanged and drops the prompt, so
  // a session this window has not seen yet must be found first or the text is lost.
  if (pid.value === null)
    await refresh()
  if (pid.value === null)
    return start(text)
  const fallback = 'Could not reach the Kontor session.'
  error.value = ''
  try {
    await request(`/api/agents/${pid.value}/message`, fallback, { method: 'POST', headers: JSON_HEADERS, body: JSON.stringify({ message: text }) })
    return true
  }
  catch (e) {
    error.value = errorMessage(e, fallback)
    return false
  }
}

async function end(): Promise<void> {
  const fallback = 'Could not end the Kontor session.'
  error.value = ''
  try {
    await request(SESSION_URL, fallback, { method: 'DELETE' })
    settle(null)
  }
  catch (e) {
    error.value = errorMessage(e, fallback)
  }
}

function watchAgentExit() {
  if (watchingAgents)
    return
  watchingAgents = true
  const { agents } = useAgents({ autoStart: false })
  // Only a live-then-gone flip of the SAME pid counts: a freshly spawned or
  // renewed pid is absent until the scanner lists it.
  watch(
    () => [pid.value, agents.value.some(a => a.pid === pid.value && a.status !== 'finished')] as const,
    ([now, live], [before, wasLive]) => {
      if (now !== null && now === before && wasLive && !live)
        settle(null)
    },
  )
}

export function useKontorSession() {
  watchAgentExit()
  return { pid, status, error, refresh, start, send, end, renew }
}
```

- [ ] **Step 4: Run it** — same command. Expected: PASS (8 tests).

- [ ] **Step 5: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`, then `git add src/features/mission/composables/useKontorSession.ts src/features/mission/__tests__/useKontorSession.test.ts && git commit` with subject `feat: the client holds one Kontor session that the Mission tile and Spotlight share`.

---

### Task 8: The reading offers Kontor instead of capture

**Files:**
- Modify: `src/features/mission/composables/useReading.ts` (full rewrite below)
- Test (create): `src/features/mission/__tests__/useReading.test.ts`
- Test (modify): `src/features/mission/__tests__/MissionInput.test.ts:56-66` (label and copy only)

**Interfaces:**
- Consumes: `ACTIVE_VIEWS`, `ActiveView` from `@/composables/useViewState`
- Produces: `export type ReadingKind = 'navigate' | 'command' | 'ask' | 'empty'` and `export function readInput(raw: string, sessionRunning = false): Reading`. The default keeps the only caller (`MissionInput.vue:18`) compiling.

**Acceptance:** Free text reads as `ask` (START KONTOR without a session, SEND TO KONTOR with one); `/command` reads as `ask` only while a session runs; nothing reads as `capture`.

- [ ] **Step 1: Write the failing test** (`useReading.test.ts`)

```ts
import { describe, expect, it } from 'vitest'
import { readInput } from '../composables/useReading'

describe('readInput', () => {
  it('reads nothing from blank input', () => {
    expect(readInput('   ').kind).toBe('empty')
  })

  it('navigates on an exact view name whether or not a session runs', () => {
    expect(readInput('go to pipeline')).toMatchObject({ kind: 'navigate', view: 'pipeline' })
    expect(readInput('pipeline', true)).toMatchObject({ kind: 'navigate', view: 'pipeline' })
  })

  it('offers to start Kontor for free text when no session runs', () => {
    expect(readInput('plan phase 4')).toMatchObject({ kind: 'ask', label: 'START KONTOR' })
  })

  it('sends free text to a running session', () => {
    expect(readInput('plan phase 4', true)).toMatchObject({ kind: 'ask', label: 'SEND TO KONTOR' })
  })

  it('refuses a slash command without a session and sends it to a running one', () => {
    expect(readInput('/compact').kind).toBe('command')
    expect(readInput('/compact', true)).toMatchObject({ kind: 'ask', label: 'SEND TO KONTOR' })
  })
})
```

In `MissionInput.test.ts:56-66`: `'CAPTURE'` → `'START KONTOR'`, `toContain('Nothing runs yet')` → `toContain('first prompt')`, `/CAPTURE:\s\S/` → `/START KONTOR:\s\S/`. Nothing else — the file is deleted in Task 10.

- [ ] **Step 2: Run it** — `pnpm vitest run src/features/mission/__tests__/useReading.test.ts src/features/mission/__tests__/MissionInput.test.ts`. Expected: FAIL (kind `'capture'`, label `'CAPTURE'`).

- [ ] **Step 3: Minimal implementation** (full file)

```ts
import type { ActiveView } from '@/composables/useViewState'
import { ACTIVE_VIEWS } from '@/composables/useViewState'

/**
 * What the input will do with what you typed, decided BEFORE you press Enter.
 * A reading shown here is a promise: navigate, hand the text to Kontor, or
 * refuse a slash command there is no session to run it in.
 */
export type ReadingKind = 'navigate' | 'command' | 'ask' | 'empty'

export interface Reading {
  kind: ReadingKind
  /** The badge, in the user's words. */
  label: string
  /** What pressing Enter does, stated as a consequence. */
  will: string
  /** For navigate: the view to switch to. */
  view?: ActiveView
}

export function readInput(raw: string, sessionRunning = false): Reading {
  const text = raw.trim()
  if (!text)
    return { kind: 'empty', label: '', will: '' }

  const ask: Reading = sessionRunning
    ? { kind: 'ask', label: 'SEND TO KONTOR', will: 'Sends this to the running Kontor session.' }
    : { kind: 'ask', label: 'START KONTOR', will: 'Starts a Kontor session with this as its first prompt. It creates a task only when you ask for one.' }

  if (text.startsWith('/')) {
    return sessionRunning
      ? ask
      : { kind: 'command', label: 'COMMAND', will: 'Needs a running Kontor session. Start one, or type it in an agent’s own prompt.' }
  }

  const lower = text.toLowerCase()
  const view = ACTIVE_VIEWS.find(v => lower === v || lower === `go to ${v}` || lower === `open ${v}`)
  if (view)
    return { kind: 'navigate', label: 'GO TO', will: `Switches to the ${view} view. Nothing is created.`, view }

  return ask
}
```

Before writing, diff this against the current file: keep any navigate phrasing the current `readInput` accepts that this version lacks.

- [ ] **Step 4: Run it** — Step 2 command. Expected: PASS.

- [ ] **Step 5: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`, then `git add src/features/mission/composables/useReading.ts src/features/mission/__tests__/useReading.test.ts src/features/mission/__tests__/MissionInput.test.ts && git commit` with subject `feat: the mission reading offers to start or talk to Kontor instead of capturing`.

---

### Task 9: KontorTile

**Files:**
- Create: `src/features/mission/components/KontorTile.vue`
- Modify: `src/features/agents/index.ts` (add a lazy `AgentTerminal` export)
- Test: `src/features/mission/__tests__/KontorTile.test.ts`
- Do NOT delete `MissionInput.vue` here.

**Interfaces:**
- Consumes: `useKontorSession()`, `KontorStatus` (Task 7); `readInput(raw, sessionRunning)` (Task 8); `useViewState().activeView`; `AgentTerminal` (props `{ pid: number }`) from `@/features/agents`.
- Produces: `KontorTile.vue` (no props, no emits; root takes the parent's size through class fallthrough). Barrel export `export const AgentTerminal = defineAsyncComponent(() => import('./components/AgentTerminal.vue'))` — async so xterm stays out of the entry chunk (`App.vue:34` imports MissionControlView eagerly), through the barrel because the boundary rule rejects deep dynamic imports.

**Acceptance:** The tile shows Kontor, its state, New and End, the running session's terminal, and the input with its reading; it handles empty, starting, running and failed states; failed text stays in the input; New works with an empty input.

- [ ] **Step 1: Write the failing test**

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

const session = {
  pid: ref<number | null>(null),
  status: ref<'idle' | 'starting' | 'running' | 'error'>('idle'),
  error: ref(''),
  refresh: vi.fn(),
  send: vi.fn(),
  end: vi.fn(),
  renew: vi.fn(),
}
const activeView = ref('mission')

vi.mock('../composables/useKontorSession', () => ({ useKontorSession: () => session }))
vi.mock('@/features/agents', () => ({
  AgentTerminal: { props: ['pid'], template: '<div data-testid="stub-terminal" :data-pid="pid" />' },
}))
vi.mock('@/composables/useViewState', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/composables/useViewState')>()
  return { ...actual, useViewState: () => ({ activeView }) }
})

const { default: KontorTile } = await import('../components/KontorTile.vue')

async function typeInto(wrapper: ReturnType<typeof mount>, text: string) {
  await wrapper.get('[data-testid="mission-input"]').setValue(text)
  await flushPromises()
}
function inputValue(wrapper: ReturnType<typeof mount>) {
  return (wrapper.get('[data-testid="mission-input"]').element as HTMLInputElement).value
}

beforeEach(() => {
  session.pid.value = null
  session.status.value = 'idle'
  session.error.value = ''
  for (const fn of [session.refresh, session.send, session.end, session.renew])
    fn.mockReset()
  session.send.mockResolvedValue(true)
  session.renew.mockResolvedValue(true)
  activeView.value = 'mission'
})

describe('kontorTile', () => {
  it('reattaches on mount and shows the empty state when no session runs', () => {
    const w = mount(KontorTile)
    expect(session.refresh).toHaveBeenCalledOnce()
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('No session')
    expect(w.find('[data-testid="kontor-empty"]').exists()).toBe(true)
    expect(w.find('[data-testid="stub-terminal"]').exists()).toBe(false)
    expect(w.get('[data-testid="kontor-end"]').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('starts a session with free text and clears the input once delivered', async () => {
    const w = mount(KontorTile)
    await typeInto(w, 'plan phase 4')
    expect(w.get('[data-testid="mission-reading-label"]').text()).toBe('START KONTOR')
    // Badge and sentence must stay apart in the text layer, not only on screen.
    expect(w.get('[data-testid="mission-reading"]').text()).toMatch(/START KONTOR:\s\S/)
    await w.get('[data-testid="mission-input-submit"]').trigger('click')
    await flushPromises()
    expect(session.send).toHaveBeenCalledWith('plan phase 4')
    expect(inputValue(w)).toBe('')
    w.unmount()
  })

  it('shows the starting state and blocks a second submit', async () => {
    session.status.value = 'starting'
    const w = mount(KontorTile)
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('Starting…')
    await typeInto(w, 'again')
    expect(w.get('[data-testid="mission-input-submit"]').attributes('disabled')).toBeDefined()
    w.unmount()
  })

  it('shows the running session terminal and sends slash commands to it', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    const w = mount(KontorTile)
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('Running')
    expect(w.get('[data-testid="stub-terminal"]').attributes('data-pid')).toBe('1234')
    await typeInto(w, '/compact')
    expect(w.get('[data-testid="mission-reading-label"]').text()).toBe('SEND TO KONTOR')
    await w.get('[data-testid="mission-input-submit"]').trigger('click')
    await flushPromises()
    expect(session.send).toHaveBeenCalledWith('/compact')
    w.unmount()
  })

  it('keeps the text and shows the error when the session could not take it', async () => {
    session.send.mockImplementation(async () => {
      session.status.value = 'error'
      session.error.value = 'Could not mint a session key'
      return false
    })
    const w = mount(KontorTile)
    await typeInto(w, 'plan phase 4')
    await w.get('[data-testid="mission-input-submit"]').trigger('click')
    await flushPromises()
    expect(w.get('[data-testid="kontor-state"]').text()).toBe('Failed')
    expect(w.get('[data-testid="mission-input-problem"]').text()).toContain('Could not mint a session key')
    expect(inputValue(w)).toBe('plan phase 4')
    w.unmount()
  })

  it('navigates on a view name without touching the session', async () => {
    const w = mount(KontorTile)
    await typeInto(w, 'go to pipeline')
    await w.get('[data-testid="mission-input-submit"]').trigger('click')
    await flushPromises()
    expect(activeView.value).toBe('pipeline')
    expect(session.send).not.toHaveBeenCalled()
    w.unmount()
  })

  it('refuses a slash command when no session runs', async () => {
    const w = mount(KontorTile)
    await typeInto(w, '/grant Bash')
    await w.get('[data-testid="mission-input-submit"]').trigger('click')
    await flushPromises()
    expect(session.send).not.toHaveBeenCalled()
    expect(w.get('[data-testid="mission-input-problem"]').text()).toContain('running Kontor session')
    w.unmount()
  })

  it('ends the session, and renews it with or without a first prompt', async () => {
    session.pid.value = 1234
    session.status.value = 'running'
    const w = mount(KontorTile)
    await w.get('[data-testid="kontor-end"]').trigger('click')
    expect(session.end).toHaveBeenCalledOnce()
    await w.get('[data-testid="kontor-new"]').trigger('click')
    await flushPromises()
    expect(session.renew).toHaveBeenCalledWith('')
    await typeInto(w, 'fresh start')
    await w.get('[data-testid="kontor-new"]').trigger('click')
    await flushPromises()
    expect(session.renew).toHaveBeenLastCalledWith('fresh start')
    expect(inputValue(w)).toBe('')
    w.unmount()
  })
})
```

- [ ] **Step 2: Run it** — `pnpm vitest run src/features/mission/__tests__/KontorTile.test.ts`. Expected: FAIL, `KontorTile.vue` cannot be resolved.

- [ ] **Step 3: Minimal implementation**

`src/features/agents/index.ts`: add `import { defineAsyncComponent } from 'vue'` and append the export below (run `pnpm lint:fix` if perfectionist reorders):

```ts
export const AgentTerminal = defineAsyncComponent(() => import('./components/AgentTerminal.vue'))
```

`src/features/mission/components/KontorTile.vue` — port the input markup from `MissionInput.vue` (keep its testids and the `sr-only` separator) and add the header and terminal:

```vue
<script setup lang="ts">
import type { KontorStatus } from '../composables/useKontorSession'
import { computed, onMounted, ref } from 'vue'
import { useViewState } from '@/composables/useViewState'
import { AgentTerminal } from '@/features/agents'
import { useKontorSession } from '../composables/useKontorSession'
import { readInput } from '../composables/useReading'

const { pid, status, error, refresh, send, end, renew } = useKontorSession()
const { activeView } = useViewState()

const STATE_LABELS: Record<KontorStatus, string> = {
  idle: 'No session',
  starting: 'Starting…',
  running: 'Running',
  error: 'Failed',
}

const text = ref('')
const problem = ref('')
const running = computed(() => status.value === 'running')
const busy = computed(() => status.value === 'starting')
const reading = computed(() => readInput(text.value, running.value))

// Reattaches after a reload, a view switch or a server restart.
onMounted(refresh)

async function submit() {
  const r = reading.value
  if (r.kind === 'empty' || busy.value)
    return
  problem.value = ''

  if (r.kind === 'navigate' && r.view) {
    activeView.value = r.view
    text.value = ''
    return
  }
  if (r.kind === 'command') {
    problem.value = 'Slash commands need a running Kontor session. Start one, or type it in an agent’s own prompt.'
    return
  }
  if (await send(text.value.trim()))
    text.value = ''
}

async function renewSession() {
  if (await renew(text.value.trim()))
    text.value = ''
}
</script>

<template>
  <section
    data-testid="kontor-tile"
    aria-labelledby="kontor-title"
    class="flex min-h-0 flex-col gap-3 rounded-xl border border-line bg-card p-4"
  >
    <header class="flex items-center gap-3">
      <h2 id="kontor-title" class="text-[14px] font-medium text-fg">
        Kontor
      </h2>
      <span data-testid="kontor-state" class="font-mono text-[10px] uppercase tracking-widest text-fg-mute">
        {{ STATE_LABELS[status] }}
      </span>
      <span class="flex-grow" />
      <button
        type="button"
        data-testid="kontor-new"
        title="Ends this session and starts a fresh one; text in the input becomes its first prompt"
        :disabled="!running"
        class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
        @click="renewSession"
      >
        New
      </button>
      <button
        type="button"
        data-testid="kontor-end"
        :disabled="!running"
        class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
        @click="end"
      >
        End
      </button>
    </header>

    <div class="min-h-0 flex-1 overflow-hidden rounded-lg border border-line bg-app">
      <AgentTerminal v-if="pid !== null" :key="pid" :pid="pid" />
      <p v-else data-testid="kontor-empty" class="p-4 text-[12.5px] text-fg-faint">
        {{ busy ? 'Starting a Kontor session…' : 'No session. Ask for anything below to start one.' }}
      </p>
    </div>

    <div class="flex flex-col gap-2">
      <label for="mission-input" class="text-[12.5px] text-fg-mute">
        Or ask for anything — tasks, this interface, the system itself
      </label>

      <div class="rounded-xl border border-line-strong bg-app overflow-hidden">
        <div class="flex items-center gap-2.5 px-3.5 h-11">
          <span aria-hidden="true" class="font-mono text-[13px] text-accent">›</span>
          <input
            id="mission-input"
            v-model="text"
            data-testid="mission-input"
            type="text"
            placeholder="Ask Kontor, or go to pipeline"
            class="flex-grow bg-transparent text-[14.5px] text-fg outline-none"
            @keydown.enter.prevent="submit"
          >
          <button
            type="button"
            data-testid="mission-input-submit"
            :disabled="reading.kind === 'empty' || busy"
            class="h-7 rounded-md border border-line-strong px-2.5 text-[12px] text-fg-soft disabled:opacity-50"
            @click="submit"
          >
            {{ busy ? 'Working…' : 'Enter' }}
          </button>
        </div>

        <div
          v-if="reading.kind !== 'empty'"
          data-testid="mission-reading"
          class="border-t border-line px-3.5 py-2.5 flex items-center gap-2.5"
        >
          <span
            data-testid="mission-reading-label"
            class="font-mono text-[10px] rounded px-1.5 py-0.5 border border-line-strong text-fg-soft shrink-0"
          >{{ reading.label }}</span>
          <!--
            Flex gap separates badge and sentence on screen but not in the text
            layer; without this, textContent read "GO TOSwitches to…".
          -->
          <span class="sr-only">: </span>
          <span data-testid="mission-reading-will" class="text-[12.5px] text-fg-mute leading-snug">{{ reading.will }}</span>
        </div>
      </div>

      <p v-if="problem || error" data-testid="mission-input-problem" role="alert" class="text-[12.5px] text-warning-text">
        {{ problem || error }}
      </p>
      <p v-else class="text-[12px] text-fg-faint">
        The reading above is what Enter does. Only readings this screen can carry out are offered.
      </p>
    </div>
  </section>
</template>
```

Before finishing, compare every Tailwind token used here (`bg-card`, `text-fg-soft`, `text-fg-faint`, `text-warning-text`, `text-accent`) against `MissionInput.vue` and the theme; replace any token the theme does not define with the one the neighbouring components use.

- [ ] **Step 4: Run it** — same command. Expected: PASS (8 tests).

- [ ] **Step 5: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`, then `git add src/features/mission/components/KontorTile.vue src/features/mission/__tests__/KontorTile.test.ts src/features/agents/index.ts && git commit` with subject `feat: a Kontor tile shows the session's terminal, state and New/End controls`.

---

### Task 10: Mission control layout

**Files:**
- Modify: `src/features/mission/MissionControlView.vue` (import at `:8`, centre column `:40-48`)
- Modify: `src/features/mission/components/NextThing.vue:148-158` (calm state)
- Delete: `src/features/mission/components/MissionInput.vue`, `src/features/mission/__tests__/MissionInput.test.ts`
- Test: `src/features/mission/__tests__/NextThing.test.ts:70-74`

**Interfaces:**
- Consumes: `KontorTile.vue` (Task 9)
- Produces: nothing new; `openTask` stays (NextThing `@open`), the `captured` wiring is gone.

**Acceptance:** The centre column stacks NextThing (sized to content) over KontorTile (fills the rest); "Nothing needs you" is one faint line.

- [ ] **Step 1: Write the failing test** — replace `NextThing.test.ts:70-74` with:

```ts
  it('says nothing needs you in one faint line so the Kontor tile gets the height', () => {
    const w = mountNext(null)
    const calm = w.get('[data-testid="mission-calm"]')
    expect(calm.element.tagName).toBe('P')
    expect(calm.text()).toContain('Nothing needs you')
    expect(w.find('h2').exists()).toBe(false)
    expect(w.find('[data-testid="mission-next"]').exists()).toBe(false)
    w.unmount()
  })
```

(Use the file's existing mount helper; if it is not called `mountNext`, use its real name.)

- [ ] **Step 2: Run it** — `pnpm vitest run src/features/mission/__tests__/NextThing.test.ts`. Expected: FAIL (tag is `DIV`, an `h2` exists).

- [ ] **Step 3: Minimal implementation**

`NextThing.vue:148-158` — replace the `v-else` calm block with:

```html
  <p v-else data-testid="mission-calm" class="flex items-center gap-2 text-[12.5px] text-fg-faint">
    <span aria-hidden="true" class="h-1.5 w-1.5 rounded-full bg-success" />
    Nothing needs you — agents interrupt here if they get stuck.
  </p>
```

`MissionControlView.vue`: `import MissionInput from './components/MissionInput.vue'` → `import KontorTile from './components/KontorTile.vue'`; replace the centre column with:

```html
    <div class="flex-grow min-w-0 flex flex-col gap-6 px-10 py-8">
      <NextThing
        :next="next"
        :remaining="remaining"
        @resolved="refreshPending"
        @open="(id) => emit('openTask', id)"
      />
      <KontorTile class="flex-1" />
    </div>
```

Then `git rm src/features/mission/components/MissionInput.vue src/features/mission/__tests__/MissionInput.test.ts`.

- [ ] **Step 4: Run it** — `pnpm vitest run src/features/mission`. Expected: PASS.

- [ ] **Step 5: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`, then `git add src/features/mission/MissionControlView.vue src/features/mission/components/NextThing.vue src/features/mission/__tests__/NextThing.test.ts && git commit` with subject `feat: Mission control gives the Kontor tile the centre column below what needs you` (deletions already staged by `git rm`).

---

### Task 11: Spotlight hands unmatched text to Kontor

**Files:**
- Modify: `src/components/SpotlightSearch.vue` (`:4-5`, `:9-13`, `:16`, `:75-80`, `:127-144`, `:200-201`, `:343-349`, `:353`)
- Modify: `src/App.vue:391` (delete the `@captured` line)
- Delete: `src/composables/useCapture.ts` (no test file; last importer after Task 10 is Spotlight)
- Test: `src/components/SpotlightSearch.test.ts` (`:6-24`, `:47-56`, `:110-159`)

**Interfaces:**
- Consumes: `useKontorSession().send/error` via `@/features/mission/composables/useKontorSession` (`src/components` sits outside the feature-boundary rule; `App.vue:34` deep-imports mission the same way); `useViewState().activeView`.
- Produces: Spotlight's emits shrink to `{ navigateTask: [task: PipelineTask], navigateAgent: [agent: Agent] }`.

**Acceptance:** When nothing matches, Enter switches to Mission, calls `send(text)` and closes the dialog; on failure the dialog stays open with the reason; no `/api/tasks` POST.

- [ ] **Step 1: Write the failing test** — in `SpotlightSearch.test.ts`:
  - delete the `createTask`, `suggestFolders`, `projects` consts, their three `vi.mock`s (`:6-8`, `:12-20`) and their lines in `beforeEach` (`:49-54`);
  - add:
```ts
const send = vi.fn()
const kontorError = ref('')
vi.mock('@/features/mission/composables/useKontorSession', () => ({
  useKontorSession: () => ({ send: (...a: unknown[]) => send(...a), error: kontorError }),
}))
```
  - in `beforeEach`: `send.mockReset().mockResolvedValue(true); kontorError.value = ''`;
  - replace the three capture tests (`:121-158`) with:
```ts
  // Text that matched nothing goes to the Kontor session, never to the backlog.
  it('hands free text that matched nothing to Kontor and switches to mission', async () => {
    const wrapper = await openSpotlight('plan phase 4 of the dashboard')
    expect(document.querySelector('[data-testid="spotlight-kontor"]')?.textContent).toContain('Kontor')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).toHaveBeenCalledWith('plan phase 4 of the dashboard')
    expect(activeView.value).toBe('mission')
    expect(document.querySelector('input[placeholder]')).toBeNull()
    wrapper.unmount()
  })

  it('keeps the dialog open with the reason when Kontor cannot take the text', async () => {
    send.mockImplementation(async () => {
      kontorError.value = 'Could not start a Kontor session.'
      return false
    })
    const wrapper = await openSpotlight('something')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(document.querySelector('[data-testid="spotlight-problem"]')?.textContent).toContain('Could not start a Kontor session.')
    expect(document.querySelector('input[placeholder]')).not.toBeNull()
    wrapper.unmount()
  })

  // A search hit and a hand-off are mutually exclusive.
  it('does not hand off when the search found a result', async () => {
    searchBody = { tasks: [{ id: 'x1', title: 'existing', currentStage: 'ready' }], agents: [] }
    const wrapper = await openSpotlight('existing')
    expect(document.querySelector('[data-testid="spotlight-kontor"]')).toBeNull()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    await flushPromises()
    expect(send).not.toHaveBeenCalled()
    wrapper.unmount()
  })
```
  - rename `describe('spotlightSearch commands and capture'` → `'spotlightSearch commands and hand-off'`.
  (`openSpotlight`, `searchBody`, `activeView` and the `spotlight-problem` testid are the file's existing helpers; if a name differs, use the real one.)

- [ ] **Step 2: Run it** — `pnpm vitest run src/components/SpotlightSearch.test.ts`. Expected: FAIL (no `spotlight-kontor`, `send` not called).

- [ ] **Step 3: Minimal implementation** (`SpotlightSearch.vue`)
  - replace the `captureTask`/`useProjects` imports (`:4-5`) so the imports include:
```ts
import { ACTIVE_VIEWS, useViewState } from '@/composables/useViewState'
import { useKontorSession } from '@/features/mission/composables/useKontorSession'
```
  - drop `captured: [taskId: string]` from the emits (`:9-13`);
  - `:16` `const { projects } = useProjects()` → `const kontor = useKontorSession()`;
  - `:75-80` →
```ts
// Text that matches nothing goes to the Kontor session, which creates a task only when asked.
const handingOff = computed(() =>
  query.value.trim().length > 0 && !loading.value && flatResults.value.length === 0,
)
```
  - `:127-144` (`capture()`) →
```ts
async function handOff() {
  const text = query.value.trim()
  if (!text || busy.value)
    return
  busy.value = true
  problem.value = ''
  activeView.value = 'mission'
  if (await kontor.send(text))
    closeDialog()
  else
    problem.value = kontor.error.value
  busy.value = false
}
```
  - Enter handler `:200-201` → `if (handingOff.value) {` / `void handOff()`;
  - hint `:343-349` →
```html
        <p
          v-else-if="handingOff"
          data-testid="spotlight-kontor"
          class="px-4 py-2 text-[12px] text-fg-faint"
        >
          {{ busy ? 'Handing to Kontor…' : 'Nothing matched — ↵ hands this to Kontor.' }}
        </p>
```
  - footer `:353` → `<span>↵ open or ask Kontor</span>`;
  - `App.vue`: delete `@captured="(taskId: string) => navigateTo({ taskId })"` (`:391`);
  - `git rm src/composables/useCapture.ts`;
  - verify: `grep -rn "useCapture\|captureTask\|CaptureUnavailable\|@captured" src tests` prints nothing.
  (Keep `useViewState` usage consistent with how the file already reads `activeView`; if `closeDialog`, `busy`, `problem`, `loading`, `flatResults` are named differently, use the real names.)

- [ ] **Step 4: Run it** — same command. Expected: PASS.

- [ ] **Step 5: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`, then `git add src/components/SpotlightSearch.vue src/components/SpotlightSearch.test.ts src/App.vue && git commit` with subject `feat: Spotlight hands text that matched nothing to Kontor instead of filing a backlog item` (deletion staged by `git rm`).

---

### Task 12: E2E, README, CHANGELOG

**Files:**
- Create: `tests/e2e/kontor-session.spec.ts`
- Modify: `README.md:36`, `README.md:38`
- Modify: `CHANGELOG.md` (`### Added` at `:54`, `### Removed` at `:375`)

**Interfaces:**
- Consumes: `stubAuthDisabled`, `stubJson`, `stubEmptyStream` from `tests/e2e/helpers.ts`; testids from Task 9.
- Produces: nothing.

**Acceptance:** Text + Enter on Mission POSTs `/api/kontor-session` with `{ prompt }`, never POSTs `/api/tasks`, and the tile then reads Running.

- [ ] **Step 1: Write the test**

```ts
import { expect, test } from '@playwright/test'
import { stubAuthDisabled, stubEmptyStream, stubJson } from './helpers'

const PID = 4242

test('Mission input starts a Kontor session instead of creating a task', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('agent-active-view', 'mission'))
  await stubAuthDisabled(page)
  await stubJson(page, '/api/agents', [])
  await stubEmptyStream(page, '/api/agents/stream')
  await stubEmptyStream(page, '/api/tasks/stream')
  await stubJson(page, '/api/tasks', [])
  await stubJson(page, '/api/config', { mcpServerName: 'agent-dashboard', mcpEndpoint: '/api/mcp' })
  await stubJson(page, '/api/spawners', [])
  await stubJson(page, '/api/github/summary', { error: 'github is not configured' }, 503)

  let running = false
  const prompts: unknown[] = []
  await page.route(/\/api\/kontor-session$/, async (route) => {
    if (route.request().method() === 'POST') {
      prompts.push(route.request().postDataJSON())
      running = true
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ pid: running ? PID : null }) })
  })
  // The tile's terminal attaches over a WebSocket; the backend knows no such pid.
  await page.routeWebSocket(/\/api\/agents\/\d+\/terminal$/, () => {})

  const taskPosts: string[] = []
  page.on('request', (req) => {
    if (req.method() === 'POST' && new URL(req.url()).pathname === '/api/tasks')
      taskPosts.push(req.url())
  })

  await page.goto('/')
  await expect(page.getByTestId('kontor-state')).toHaveText('No session')

  const input = page.getByTestId('mission-input')
  await input.fill('Plan phase 4 of the dashboard')
  await expect(page.getByTestId('mission-reading-label')).toHaveText('START KONTOR')
  await input.press('Enter')

  await expect(page.getByTestId('kontor-state')).toHaveText('Running')
  await expect(input).toHaveValue('')
  expect(prompts).toEqual([{ prompt: 'Plan phase 4 of the dashboard' }])
  expect(taskPosts).toEqual([])
})
```

Check the stubbed endpoints against `tests/e2e/cockpit.spec.ts:24-32` and add any the Mission view additionally calls (e.g. pending permissions) if the page errors.

- [ ] **Step 2: Run it** — `pnpm test:e2e tests/e2e/kontor-session.spec.ts`. Expected: PASS (Tasks 7–11 have landed).

- [ ] **Step 3: Docs**
  - `README.md:36` — replace "Its input shows the reading it will act on — navigate, command, or capture — before you press Enter." with: "Below it sits the **Kontor session**: a Claude session that can read and steer Kontor through its task API, shown as its real terminal. The input shows the reading it will act on before you press Enter — navigate, START KONTOR, or SEND TO KONTOR. Text that is not a view name starts the session as its first prompt, or goes to the running one; the session creates a backlog item only when asked and picks the project from context. **New** ends the session and starts a fresh one, **End** ends it. There is one session per installation; it survives a view switch, a reload and a server restart."
  - `README.md:38` — replace "captures it as a backlog item, deriving the slug from the title and the working directory from the first project's default folder, so an idea can be written down without first choosing a project." with "hands it to the Kontor session and switches to Mission control."
  - `CHANGELOG.md` first bullet under `### Added`: "- **A Kontor session in the Mission tile.** The Mission input talks to one interactive Claude session that can read and steer Kontor through the task API, with a short-lived key carrying every scope except `keys:manage`. Free text starts it as the first prompt or goes to the running session; so does a `/command` once a session runs. The tile shows the session's own terminal, its state, and **New**/**End**; `GET/POST/DELETE /api/kontor-session` and `POST /api/kontor-session/renew` serve it. \"Nothing needs you\" shrinks to one line so the tile gets the height."
  - `CHANGELOG.md` first bullet under `### Removed`: "- **Capture from the Mission input and Spotlight.** Both filed the text as a backlog item in the oldest project, whatever the text was about. Spotlight now hands text that matched nothing to the Kontor session and switches to Mission. `useCapture` (`captureTask`, `CaptureUnavailable`) is gone."
  - Leave historical bullets untouched.

- [ ] **Step 4: Run it** — `pnpm test:e2e tests/e2e/kontor-session.spec.ts`. Expected: PASS.

- [ ] **Step 5: Gate and commit** — `pnpm lint && pnpm typecheck && pnpm test`; `git checkout HEAD -- server/frontend/dist/.gitkeep`; then two commits:
  - `git add tests/e2e/kontor-session.spec.ts` → `test: the Mission input starts a Kontor session and files no task`
  - `git add README.md CHANGELOG.md` → `docs: the Mission input talks to a Kontor session; capture is gone`

---

### Task 13: Verification in the running app (coordinator, not delegated)

- [ ] Full gate: `pnpm lint && pnpm typecheck && pnpm test`, `(cd sdk && go vet ./...)`, `(cd server && go vet ./...)`, `task test`, `task lint`, `pnpm test:e2e`; restore ent and `.gitkeep`; paste raw output.
- [ ] Build the desktop app / server binary, stop the running instance on :13120, start the new one, print the binary's mtime and confirm the listening pid belongs to it.
- [ ] In the app: Mission → type "Lies das Handoff für Phase 4 im Dashboard und starte mit der Umsetzung bzw. deren Planung" → Enter. Expected: tile shows Running with a live terminal; `GET /api/tasks` count unchanged; the session lists `kontor-tasks` tools (`/mcp`).
- [ ] End → key revoked (`/api/kontor-session` → `{"pid":null}`), process gone.
- [ ] Push `develop`; `git log origin/develop..HEAD` empty.
