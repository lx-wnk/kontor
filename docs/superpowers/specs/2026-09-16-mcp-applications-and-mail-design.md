# MCP Applications and Mail — Design Spec

> Attach existing MCP servers to pipeline runs as registry Applications — attached per run, governed tool by tool through the capability gate, with their secrets held by the dashboard — and build mail on top of that: a new-mail trigger that costs nothing while the inbox is quiet, and agent-written mail that only the dashboard sends, after a human has approved the exact draft.

**Parent:** `2026-08-27-agenticos-overview-design.md` (unit A2 — Mail)
**Followed by:** a separate spec for a lightweight "job" task shape (see §6.1)

---

## 1. Status Quo

Every reference below was re-read on `main` at `c3034eee`.

### What works

- **The spawn owns an agent's entire MCP world.** Each stage run gets a temp config passed as `--mcp-config <file> --strict-mcp-config` (`server/internal/pipeline/spawner.go:397`). Whatever is not in that file does not exist for the agent.
- **User-scope MCP servers already reach every run.** `server/internal/pipeline/spawner.go:682` reads `~/.claude.json`, and `server/internal/channelconfig/channelconfig.go:110` copies every server into the run's config except the reserved names the dashboard re-writes with the run's own key.
- **The capability gate has the vocabulary this needs.** Classes with a default effect (`server/internal/capability/decide.go:233` — `tool`/`reach`/`resource` ask, `spend` and unknown deny), a `capability` table (`server/internal/db/ent/schema/capability.go:28`), grants with context specificity, and a spawn enforcer that renders decisions into allow lists (`server/internal/pipeline/spawner.go:169`).
- **Secrets can be encrypted at rest.** `secretbox` (`server/internal/secretbox/secretbox.go:46,56`).
- **The MCP Go SDK is already a dependency** (`server/go.mod:24`, `go-sdk v1.7.0`); the dashboard uses it as a server only.
- **A run can wait without a process.** `WaitUserTransition{AgentDone: true}` clears the PID so the reaper does not re-fail it (`server/internal/pipeline/transitions.go:159`); plan review uses this (`transitions.go:389`). `RequeueForUser` resumes the session in a new iteration (`server/internal/pipeline/orchestrator.go:1044`).
- **Two registry reconcilers exist to copy**: `ReconcilePluginResources` (`server/internal/db/repo/plugin_resource.go:24`) and `ReconcileScheduleResources` (`server/internal/db/repo/schedule_resource.go:18`), with `discovered` as a first-class state (`server/internal/db/repo/resource_repo.go:27`).

### What does not

| # | Gap | Evidence | Consequence |
| --- | --- | --- | --- |
| G1 | No foreign MCP tool can be granted | `allowedToolNames` is a closed set (`server/internal/permissions/allowlist.go:280`); its only `mcp__` entries are two channel tools; `resolvePermissionDecisions` drops everything else | An unattended agent cannot call a mail search tool without stopping for a human every time |
| G2 | Every run gets every user-scope server | `channelconfig.go:110`, no per-run filter | A coding task that reads a crafted issue can be steered into pulling mailbox content into its context |
| G3 | Permission requests carry no tool input | `server/internal/db/ent/schema/permission_request.go:18-20` stores `tool`, `pattern`, `reason` | A human would approve a send without seeing recipient or body |
| G4 | "Ask" is not a per-call prompt for headless agents | No `--permission-prompt-tool` and no permission hook in the spawn; `request_permission` returns immediately (`server/internal/channel/bridge.go:170`); a grant then covers the tool for the whole task | "Approve each mail" cannot be expressed through today's permission flow |
| G5 | Parked runs time out from run start | `anchor := run.StartedAt` unless `last_grant_at` is set (`server/internal/pipeline/sweeps.go:43`), default 4 h (`orchestrator.go:34`) | A run that works three hours and then waits has one hour left |
| G6 | Every task is a coding task | Fixed `StageOrder` (`server/internal/pipeline/types.go:203`), worktree per task (`server/internal/pipeline/progress_guards.go:73`), no task kind, no dedupe key on create | A polling routine would create a worktree and three stage runs per check, empty inbox or not |
| G7 | Dynamic secret keys have no home | The settings service rejects unknown keys (`server/internal/settings/service.go:143,180`) | `app_setting` cannot hold per-application secrets |
| G8 | Temp configs outlive a restart | `server/internal/pipeline/spawn_cleanup.go:11`: pending cleanups are in-process only | Harmless for an expiring run key; not for a mail password |

---

## 2. Why the dashboard writes no mail code

### 2.1 The mailboxes in scope

The first accounts are iCloud, a private Gmail account, and a self-hosted IMAP account at OVH. All three speak IMAP/SMTP with a password:

| Account | Credential | Note |
| --- | --- | --- |
| iCloud | app-specific password | iCloud offers no OAuth for mail |
| Gmail (private) | app password, requires 2-Step Verification | OAuth for a private `@gmail.com` account needs an external Google Cloud app whose refresh tokens expire after 7 days while in "Testing" |
| OVH | account password | |

Outlook is **not** in the first set and would need OAuth in any case: Microsoft ended IMAP/POP app passwords for personal Outlook.com, Hotmail and Live accounts on 2024-09-16. See §6.2.

### 2.2 The wheel exists

Maintained IMAP/SMTP MCP servers already implement search, drafts, sending, provider presets and folder quirks. Verified via the GitHub API on 2026-09-16:

| Server | Stars | Last push | Licence | Runtime | Relevant |
| --- | --- | --- | --- | --- | --- |
| `nikolausm/imap-mcp-server` | 94 | 2026-09-14 | MIT | Node | per-account credentials via env (`IMAP_MCP_ACCOUNT_<NAME>_IMAP_PASSWORD`), consumed at start and removed from `process.env`; account-management tools the agent must not get |
| `codefuturist/email-mcp` | 111 | 2026-08-21 | LGPL-3.0 | Node | drafts and templates, IMAP IDLE watcher, experimental XOAUTH2; env credentials for a single account only |
| `Wh1isper/mcp-email-server` | 328 | 2026-09-14 | BSD-3 | Python | needs `uvx`, not installed on the reference machine |

Running an LGPL server as a separate, unmodified process links nothing into the dashboard.

### 2.3 Claude's own Gmail connector cannot reach pipeline agents

Measured with a one-line `claude -p` probe on the reference machine:

```
without --strict-mcp-config   mcp_servers: obsidian, claude.ai Canva, claude.ai Google Drive, … (7)
with    --strict-mcp-config   mcp_servers: []
```

claude.ai connectors are not entries in `~/.claude.json`, so `channelconfig.go:110` cannot carry them over, and strict mode drops them.

---

## 3. What Changes

### 3.1 MCP servers become registry Applications

- A reconciler — modelled on `ReconcileScheduleResources` — reads the user-scope `mcpServers` from `~/.claude.json` at startup and on demand, and upserts one `resources` row per server: `kind = application`, `origin = local`, `slug = mcp-<server name>`, state `discovered`. The prefix keeps a server from taking over the registry row of a plugin application with the same slug; a server whose name does not form a valid slug is skipped with a warning.
- Reserved dashboard server names are skipped, exactly as `channelconfig.go:110` skips them.
- **Servers present at the first reconcile** are marked *attach to all runs*, so today's behaviour is kept — but as a visible, reversible flag instead of an implicit rule.
- **Servers added later** start as *attach only when requested*.
- Server definitions stay in `~/.claude.json`. `claude mcp add --scope user` remains the way to add one, and the same server stays usable in interactive sessions.

### 3.2 Attachment per run (closes G2)

- A routine (`task_schedule`) carries the list of applications its tasks need. The scheduler copies it onto each task it materializes. A task gets applications **only** this way; the task-create body does not accept them.
- **Attachment is authority, not metadata.** It decides which mailbox a run can see. It is therefore settable only through the schedules HTTP routes and by the scheduler — never through the MCP task API, never through `task.metadata`, never through the task-create body. This is the same reasoning that kept `RoutineID` out of the task-create body (`server/internal/api/tasks/handler.go:339`).
- **Limit under `auth.mode=none`:** no authentication middleware guards `/api` routes then (`server/internal/api/router.go:312-313`), so any local process — including an agent's Bash — can call the schedules routes. With authentication enabled the routes require a session. This is the documented local-trust posture, not a gap this spec closes.
- At spawn, `channelconfig` writes only (a) the dashboard's own servers, (b) servers flagged *attach to all runs*, and (c) the run's attached applications.

### 3.3 Secrets held by the dashboard

- New table `application_secret`: `resource_id`, `env_name`, `ciphertext`, `nonce`, `updated_at`, unique on (`resource_id`, `env_name`). Encrypted with the existing `secretbox` key. `app_setting` is not reused — see G7.
- **Write-only surface.** HTTP, UI and CLI can set or delete a value. Reads return only whether a value is set and when it was last changed.
- **Injected per run.** For each attached application, the spawn decrypts its secrets into the `env` of that server's entry in the temp config — and of no other entry. The file is `0600`, per run, and removed on cleanup (`spawner.go:707`).
- **Startup sweep (closes G8).** On start the dashboard deletes leftover `dashboard-channel-mcp-*.json` files in its `dashboard-<uid>` temp directory.
- Account settings other than secrets — host, port, user name, the "take the password from the environment" choice — stay in the server's own setup.

### 3.4 Tool catalogue and policy (closes G1)

- The dashboard connects to each application as an MCP **client** (`go-sdk`) and reads `tools/list`. Every tool becomes a `capability` row named exactly as Claude Code names it — `mcp__<server>__<tool>` — so the spawn enforcer renders it into `--allowedTools` / `--disallowedTools` without translation.
- **Class defaults to `tool`**: no grant, no silent allow (`decide.go:233`).
- `readOnlyHint` / `destructiveHint` from the server are shown in the UI as hints only. The MCP specification requires clients to treat tool annotations as untrusted unless the server is trusted.
- The spawn's permission filter (rule 3 in `resolvePermissionDecisions`, `spawner.go`) additionally accepts tools present in an attached application's catalogue, so a human's task-level approval of such a tool is honoured.
- The spawn enforcer resolves decisions in the run's full context chain — task, routine, project, global. Today it resolves a task context only (`spawner.go:190`).
- **Allow-all autonomy still does not allow MCP tools wholesale.** `PermissiveAllowList` (`server/internal/taskcontrol/autonomy.go:29`) stays built-in tools only.
- **A tool that appears after a server update** gets class `tool` and therefore never slips into an allow list.
- **Policy is grants.** No new rule system. The recommended mail preset ships as data:

| Tools | Grant | Scope |
| --- | --- | --- |
| search, read, create draft | allow | the routine that attaches the application |
| every tool that sends | deny | global |
| account management (e.g. `imap_add_account`, `imap_update_account`) | deny | global |

### 3.5 Mail trigger without an LLM (addresses G6 for the empty inbox)

- New table `mail_watch`: `id`, `resource_id`, `folder` (default `INBOX`), optional `from_contains` / `subject_contains`, `schedule_id` to fire, `poll_interval_seconds` (default 300), `enabled`, plus cursor and health columns (`uidvalidity`, `last_uid` or `last_seen_at`, `consecutive_failures`, `last_error`).
- The dashboard keeps **one long-lived MCP client process per watched application**, started with that application's secrets and restarted with backoff. Each tick it calls the server's search tool for mail newer than the cursor.
- **Cursor:** highest UID per folder together with `UIDVALIDITY` if the server exposes UIDs; otherwise Message-IDs newer than `last_seen_at`.
- **One task per tick, not per mail.** When new mail matches, the dashboard materializes one task from the watch's routine, listing every new mail (sender, subject, Message-ID) in the task description.
- **Dedupe.** New table `task_source` (`source_key` unique, `task_id`) with keys `mail:<resource_id>:<message-id>`. A mail already mapped to a task is never listed again.
- No match, no task, no LLM call.
- A watch can only be created for a routine that attaches the watched application; otherwise the tasks it fires could not read the mail they are about.

### 3.6 Sending: the agent drafts, the dashboard sends (closes G3 and G4 for mail)

1. The agent creates drafts with the server's draft tool (allowed by grant).
2. The agent calls a new tool on the dashboard's own MCP server, authenticated with the run's key: `request_send(application, draft_ref)`. The dashboard rejects the call unless `application` is attached to the calling run. The dashboard writes an `outbound_mail` row — `task_id`, `stage_run_id`, `resource_id`, `draft_ref`, `status = pending`, `content_hash`, `requested_at`, `decided_at`, `decided_by`, `note`, `sent_message_id`, `error`.
3. The human is notified through the existing push channel (`server/internal/webpush`) and sees an outbox view.
4. **The dashboard fetches the draft itself** from the mailbox — recipients, CC, subject, body, attachment names — shows it, and stores a hash of exactly what it showed. The agent's own description of the mail is never shown as the content.
5. **Approve:** the dashboard fetches the draft again, compares the hash, and sends only on a match, through the server with that application's secrets. A mismatch sets `changed` and sends nothing.
6. **Reject:** status `rejected` with an optional note; the draft stays in the mailbox.
7. Request, decision and send are written as audit events attributed to task and stage run.

**Guarantee:** no agent can send. Every send tool is denied to agents (§3.4), and the only sender is the dashboard process acting on a human decision.

### 3.7 The agent waits for the decision (closes G5 for mail)

- `request_send` marks the run as waiting for mail approval and tells the agent to end its turn. The agent's process exit is then parked as `WaitUserTransition{AgentDone: true}` — the plan-review shape — and not read as a missing stage output.
- An agent may request several drafts in one run and should batch them.
- **Resume when every request of the run is decided.** `RequeueForUser` continues the session with one line per mail: *sent (time, Message-ID)* · *rejected (note)* · *not sent — draft changed after approval* · *send failed (error)*.
- **Own clock.** The wait is anchored at the first `request_send`, not at run start, with its own limit `mailApprovalTimeoutSeconds` (default 259200, 72 h), configurable like `awaitingUserTimeoutSeconds`. On expiry, pending requests become `expired` — **never sent** — and the task is **resumed** with *not approved in time*, not failed.
- **Each approval round costs one iteration**, because `RequeueForUser` increments it. Iteration counting is not changed; routines that send mail need headroom in `max_iterations`.

---

## 4. Design Decisions

### 4.1 Reuse MCP servers instead of writing a mail core

Rejected alternatives:

- **An own IMAP/SMTP core** would re-implement search, drafts, sending, provider presets and special-use folders, all of which maintained servers already have (§2.2).
- **Provider-native APIs** (Gmail API, Microsoft Graph) would require an OAuth refresh-token subsystem before any account in scope needs one, and would put a private Gmail account on a 7-day re-consent cycle.
- **Claude's Gmail connector** cannot reach pipeline agents (§2.3).
- **Dropping `--strict-mcp-config`** to let claude.ai connectors in would trade a known-good run isolation for one provider.

### 4.2 `~/.claude.json` stays the source of server definitions

- The registry mirrors servers instead of owning them, so `claude mcp add` stays the one way to add a server, and the registry still provides the stable IDs that grants, attachment and audit need.
- **Rejected — `~/.claude.json` alone:** policy and attachment would hang off a name in a foreign file, with no ID.
- **Rejected — a registry-only definition with its own UI:** re-implements `claude mcp add` and hides the servers from interactive sessions.

### 4.3 Secrets in the dashboard — and what that does not protect

Chosen over leaving credentials in each server's own store, so that mail passwords live under one encryption key and reach a process only when a run has attached the application.

Stated limit: `secretbox` protects the database, not a run. An agent with Bash runs as the same OS user as the temp config file and as the MCP server process it talks to. This spec does not separate OS users per run. The mitigation is operational: use app-specific passwords, which are scoped to mail and revocable one by one. This matches the documented local-trust posture.

### 4.4 Sending through the dashboard, not through a hook

- **Rejected — a `PreToolUse` hook that holds each send for approval.** It blocks the agent for the whole wait, and in `claude -p` a hook that times out lets the call continue through the normal permission flow. A measurement stored on 2026-08-19 against Claude Code 2.1.235 recorded that as failing open.
- Sending from the dashboard fails closed by construction and allows approval hours later.

### 4.5 Trigger through the MCP client, not a polling agent

- **Rejected — a routine that spawns an agent every N minutes.** Because every task is a coding task (G6), each check would cost a worktree and three stage runs whether or not mail arrived.
- The MCP client is needed anyway for the catalogue (§3.4) and for sending (§3.6); polling is a timer on top of it.

### 4.6 Server choice is made by the first plan step

Leaning towards `nikolausm/imap-mcp-server` because it takes credentials for several accounts from the environment; `codefuturist/email-mcp` does so for one account only. The final choice depends on the questions in §8.

---

## 5. Error Handling

| Case | Behaviour |
| --- | --- |
| Server does not start or `tools/list` fails during reconcile | Registry row stays; catalogue keeps its last state; the error is shown on the application; no capability row is deleted, so grants keep resolving |
| Application attached but a secret is missing or cannot be decrypted | **The run fails before spawn**, non-retryable, naming application and variable. A run never starts silently without its mailbox — an agent without a mailbox reports "no new mail", which is indistinguishable from an empty inbox |
| Trigger: authentication or network failure | Per-watch backoff, status visible, no task; a notification after repeated consecutive failures |
| Trigger: the server's search tool changed shape | Watch status "trigger broken", notification, no crash; agents are unaffected |
| Same mail seen twice | `task_source` rejects the second mapping |
| Send fails at the server | `outbound_mail.status = failed` with the error; draft stays; the task resumes with the error |
| Draft changed after approval | `changed`, nothing sent, task resumes |
| Dashboard restarts while runs wait | `outbound_mail` rows and parked runs are in the database and stay decidable; the startup sweep removes orphaned temp configs |

---

## 6. What This Deliberately Does Not Do

### 6.1 A lightweight task shape

- Tasks created by the mail trigger still run the full pipeline (G6). That costs runs only when mail arrives, but it is still a coding pipeline for non-coding work.
- A "job" task shape — one agent stage, no worktree, no finalization — is a pipeline change, not a mail change, and gets its own spec.

### 6.2 Outlook / Microsoft 365

- Nothing is built for it. In this design Outlook needs **no dashboard change** once an MCP server handles Microsoft OAuth itself: the application is mirrored, attached, governed and fed secrets like any other.
- `codefuturist/email-mcp` lists XOAUTH2 for Microsoft 365 as experimental.
- It becomes a unit when an Outlook account is in scope.

### 6.3 Tool arguments in permission requests for other applications

G3 stays open for every application except mail, which avoids it through §3.6.

### 6.4 OS-level isolation of runs

See §4.3.

### 6.5 The plan-review timeout

Plan review parks the same way and is subject to the same run-start anchor (G5). Changing it is out of scope and recorded as a follow-up.

---

## 7. Verification

### 7.1 Test fixture

- No real mailbox and no network in the test suite.
- A small MCP server in the repository (Go, `go-sdk`) fakes search, draft, send-draft and account management, and counts calls, so tests can assert what was **not** called.

### 7.2 Unit tests

- `tools/list` → capability rows with name `mcp__<server>__<tool>` and class `tool`.
- `IsAllowedTool` accepts catalogue tools and still rejects unknown ones.
- Allow and deny lists render MCP tools; allow-all autonomy renders none.
- Spawn config contains only dashboard servers, attach-to-all servers and attached applications.
- Secrets appear only in the `env` of their own server entry.
- The startup sweep deletes only matching orphaned files.
- The reconciler is idempotent (run twice, assert unchanged) and never mirrors a reserved name.

### 7.3 Integration tests against the fixture

- Trigger: cursor advances; one task per tick; a mail is never mapped twice; no task without new mail.
- `request_send` parks the run; approval sends and resumes with the outcome; rejection resumes without sending.
- A draft changed after display is not sent.
- Expiry never sends and resumes instead of failing.
- A run whose attached application lacks a secret fails before spawn.

### 7.4 Mutation tests (required: permission paths)

Each guard is removed, the named test shown red, then restored:

1. Send tools denied to agents
2. Hash comparison before sending
3. Per-run attachment filter
4. Secrets only in their own server entry
5. Expiry never sends

### 7.5 Acceptance against real accounts

On an isolated instance — never the production database — with iCloud, Gmail and OVH:

- a draft created by an agent appears in the account's `\Drafts` folder;
- an approved mail arrives;
- the sent copy is in `\Sent`;
- ~~a new matching mail creates exactly one task.~~ **DEFERRED to slice 2** (mail trigger)

Slice 1 acceptance status (2026-10-02): tools/list confirmed (40 tools, three accounts), preset deny-list verified against live fixture (16 denied, no gaps), `TestPresetDenyGlobal_AllNamesExistInLiveFixture` added. Items 1–3 and saveToSent per-provider decision PENDING — require operator to enter account credentials on the isolated instance (steps 6–8 of the acceptance plan).

---

## 8. Open Questions for the First Plan Step

Each is settled against the chosen server before anything is built, because a design choice above depends on it.

| Question | Depends on it | Cheapest check | Answer (probe 2026-09-16, `imap-mcp-server@2.0.0`, no accounts) | Answer (live probe 2026-10-02, iCloud + Gmail + OVH) |
| --- | --- | --- | --- | --- |
| Can the server send an existing draft? | §3.6. Fallback: read the draft's fields, call the send tool with them, delete the draft — which may drop attachments | `tools/list` | **No.** 40 tools; `imap_save_draft` stores a draft, `imap_send_email` sends new content, nothing sends a draft by id. Slice 3 takes the fallback: read the draft (`imap_get_email`), send its fields with `imap_send_email`, then delete the draft. Attachments need `imap_download_attachment` and re-attaching — to be designed in the slice 3 plan | **Confirmed.** 40 tools on the live catalogue refresh (with three accounts in env), same count and same names as the no-account probe. No send-by-draft-id tool. |
| Does it expose UIDs and `UIDVALIDITY`? | §3.5 cursor | one search call | **Yes, from code.** Message tools address mail by `uid`; `imap_folder_status` returns `uidValidity` and `uidNext` (`dist/setup.js:742-750`). Live values still to be seen | PENDING — live UID stability check requires account credentials (§7.5 probe steps 6–8) |
| Does it accept credentials for several accounts from the environment? | §3.3 | start it with two env-managed accounts | **Yes, from code** — the published build reads `IMAP_MCP_ACCOUNT_<ACCOUNT NAME>_…` (`dist/index.js`). Live test with two accounts still needs the human's credentials | **VERIFIED live.** Catalogue refresh on isolated instance with three env-managed accounts (iCloud, Gmail, OVH) returned 40 tools; `tools/list` answered with all three accounts configured. Evidence: `docs/local/agenticOs/evidence/4.3-slice1/02-tools-list.json` |
| Does `tools/list` answer without credentials? | §3.4 catalogue before secrets are set | start it with none | **Yes, live** — full list with no accounts configured | **Confirmed.** Same 40 tools with and without accounts — catalogue is credential-independent. Fixture committed as `server/internal/mcpapps/testdata/imap-mcp-server-2.0.0-tools.txt` |
| Does it save a copy to `\Sent` after SMTP sending, or must the client append one? | §7.5 | send one mail per account and list `\Sent` | **The server appends itself, from code** — `appendToSentFolder` resolves the folder by configured name, then SPECIAL-USE `\Sent`, then localized names; per-account `saveToSent`. `HYPOTHESIS`: Gmail then keeps two copies unless `saveToSent` is off — check with a real send | PENDING — live send probe per account requires credentials (§7.5 probe step 8) |

---

## 9. Delivery Slices

One design, three deliveries. Each is useful on its own and leaves nothing half-wired.

| Slice | Contains | Useful alone because |
| --- | --- | --- |
| 1 — MCP applications | §3.1–§3.4: mirror, attachment, secrets, catalogue and policy | An agent in a routine can search, read and draft mail under grants, and every other MCP server becomes governable the same way |
| 2 — Mail trigger | §3.5 | New mail starts work without polling cost |
| 3 — Send and wait | §3.6–§3.7 | Agents can get mail sent, with a human approving each one |

The open questions in §8 are answered before slice 1, because they decide details in all three.
