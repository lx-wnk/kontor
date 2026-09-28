# Privacy Policy

**Claude Agent Overview** is a local developer tool that runs entirely on your machine and binds exclusively to `127.0.0.1`. This document describes what data the tool reads, what it persists to disk, what (if anything) leaves your machine, and what it deliberately does not collect.

---

## 1. Data the Tool Reads

The dashboard reads the following data from your local filesystem at runtime. Nothing is transmitted unless you opt in to an integration described in section 3.

| Source | What is read | Why |
|---|---|---|
| `~/.claude/projects/{encoded_path}/*.jsonl` | Full Claude Code session transcripts — prompts, responses, tool calls, token counts | Real-time agent monitoring |
| `~/.claude/usage-data/session-meta/*.json` | Session metadata — model, start time, token totals | Cost and status display |
| `ps`, `lsof` (macOS) / `/proc/<pid>/cwd` (Linux) | Running process list, working directories | Matching PIDs to session files |
| `ps ewww` (macOS) / `/proc/<pid>/environ` (Linux) | The environment block of monitored agent processes, read once per process scan. Only `CLAUDE_CONFIG_DIR` is extracted — nothing else is retained, logged, persisted, or transmitted. The whole block is read transiently because neither OS offers a single-variable read. | Resolving which config root a session's commands/skills load from, and attributing it to its configured spawner profile |
| `~/.claude/dashboard-tasks.db` | Task pipeline state (see section 2) | Pipeline orchestration |
| Provider session logs (`~/.codex`, `~/.gemini`, `~/.junie`, `~/.pi/agent/sessions`, or the path from the provider's config-dir/env) | Local JSONL session logs of an enabled provider — tokens, model, cost | Monitoring Codex/Gemini/Junie/pi.dev agents (only when that provider is enabled) |
| Obsidian vault (Local REST API, once configured) | Note content read over HTTPS from the vault's Local REST API plugin, and (via the `obsidian_write` MCP tool) note content sent to it — `obsidian_delete` sends only the note's path, never content | Two production paths reach the vault, both gated through a `memory.Gate` before any request leaves the machine: `POST /api/obsidian/index` reads and searches notes to build memory pointers (`obsidian.search`/`obsidian.read` plus `memory.write` for the pointer itself), and four MCP tools (`obsidian_read`, `obsidian_search`, `obsidian_write`, `obsidian_delete`) let a connected agent read, search, write, or delete a note directly, gated by the matching capability. The vault client's `Read`/`Write`/`Search`/`Delete` methods themselves take no capability repos, so a future caller reaching them directly instead of through one of these two paths would bypass the gate entirely |

**Obsidian is off until you configure it.** Set `obsidian.baseURL`, `obsidian.vaultRoot`, `obsidian.apiKey`, and `obsidian.tlsMode` under **Settings → Obsidian**. All four apply only after a restart, and the first three are a required trio — the integration stays off until `baseURL`, `vaultRoot`, and `apiKey` are all set, not just one or two of them; a half-set trio fails the server's boot instead of silently starting disabled. `obsidian.apiKey` is stored encrypted (AES-256-GCM via `internal/secretbox`) and reads back as `********` on every surface except the one accessor that decrypts it to build the client. To turn the integration back off, clear all three in the panel and save — an emptied API key field clears the stored key (the `********` sentinel is what means "leave unchanged"), and a cleared secret reads back as unset, not as the mask. See [`docs/guides/security.md`](docs/guides/security.md#obsidians-tls-trust-model) for the TLS trust model.

When a provider is enabled, the dashboard additionally performs a local `GET http://localhost:11434/api/tags` request to Ollama (when reachable) to classify local models and report them at $0 cost. This request stays on your machine; no data leaves it.

Custom provider descriptors placed in `DASHBOARD_PROVIDER_DIR` are trusted configuration: a descriptor's `configDir` and `sessionGlob` can direct the dashboard to read files anywhere your user account can access. Only add descriptors you trust, the same way you would trust any local config file.

**Important:** JSONL session files may contain sensitive information — API keys typed into prompts, file contents, credentials pasted during a session, or other PII. The dashboard does not sanitize or redact these files. You are responsible for protecting them using standard host-OS file permissions (e.g. `chmod 700 ~/.claude/projects/`).

---

## 2. Data the Tool Persists

All persistent data stays on your machine. No data is synced to a cloud service automatically.

### SQLite database (`~/.claude/dashboard-tasks.db`, override via `DASHBOARD_DB_PATH`)

| Table | What is stored |
|---|---|
| `tasks`, `stage_runs` | Task definitions, stage outputs (LLM-generated text), audit entries, status history |
| `permission_requests` | Tool permission requests from spawned stage agents |
| `users` | GitHub user ID, login, and avatar URL (only when GitHub OAuth is configured) |
| `api_keys` | SHA-256 hashes of bearer tokens — never the plaintext token |
| `web_push_subscriptions` | Web-Push endpoint URLs and VAPID keys (see section 3) |
| `spawners`, `presets`, `remotes`, `refine` | User-configured pipeline settings |
| `cost_history` | Aggregated per-session token and cost data imported from JSONL files |
| `capabilities` | The permission catalogue — one row per grantable capability (a Claude Code tool name, or a coarser named action such as `memory.read`/`memory.write`) with its class and which enforcement points may apply it. Seeded on boot from the built-in tool list plus a small fixed set of non-tool actions; no user content. |
| `grants` | Capability grants bound to a context (global, project, task, routine, or agent session): pattern, mode (allow/deny/ask), optional expiry and rate limit, and who granted or revoked it. Existing `task_permissions` and `permission_presets` rows are backfilled into this table on boot (`granted_by = "migration:legacy"`). Also created and revoked directly via the `kontor grants` CLI; a revoked grant's row is kept (tombstoned via `revoked_at`/`revoked_by`), not deleted, so revoking a grant does not remove data. |
| `grant_usages` | Timestamps of successful uses of a rate-limited grant, counted toward that grant's limit. No call content — only the grant id and when it was used. |
| Memory spaces (`resource` rows, `kind = "memory_space"`) | A memory space's identity: slug, display name, and scope (global/project/application). Not a separate table — a space is one row in the same `resource` table used for plugins, routines, and applications. No entry content is stored here. |
| `memory_entries` | Facts, preferences, and lessons written by agents (via the `memory_write` MCP tool) or you (via `POST /api/memory/entries`): a short `summary` (what gets pushed into a spawn's prompt, see below) and a full `content` (retrievable on demand), plus `kind`, `source_kind`/`source_ref`, `confidence`, a validity window, and an optional `user_id`. When Obsidian is configured (see section 1), an on-demand indexing pass (**Settings → Obsidian → Index now**, or `POST /api/obsidian/index`) writes rows here too, of `kind = "pointer"`: `summary` is the note's first line (capped at 200 characters) and `content` is the note's path relative to the configured `VaultRoot` — **never the note's body**. The vault stays the content; memory only ever holds a pointer into it. Both `summary` and `content` are sanitized before being written — invisible/control characters (e.g. bidi overrides) are stripped, then known secret patterns (API keys, tokens, PEM blocks, JWTs, and similar) are redacted to `[REDACTED]`. This is a best-effort, pattern-based scrub, not a guarantee that no sensitive text survives it. The two fields are sanitized asymmetrically: `summary` is also collapsed to a single line, since it is concatenated into a prompt and must never be able to forge a section boundary there; `content` keeps its original line structure (code blocks, PEM bodies, numbered steps) intact. |
| `memory_injections` | An audit record of what memory was offered to a stage's spawn: the stage-run id, which entry ids were selected, the character budget, how many characters were used, and how many candidates existed. No entry content — only ids and counts. |

### Hooks secret

Set via the `DASHBOARD_HOOKS_SECRET` environment variable. The secret is **always required** — if you do not set it explicitly, one is auto-generated and persisted on first boot (see `config.Load`). `hooks.New` panics at startup if a secret cannot be supplied, so there is no unauthenticated hook path.

### VAPID private key

The VAPID private key used for Web-Push is stored in the SQLite database under a configuration key. It never leaves your machine unless you explicitly copy it. Treat database access as equivalent to key access.

### Retention & deletion

Most persisted data has **no automatic expiry** — rows live until you delete them via the paths below. Memory is the one exception, and it needs a more careful description than "delete": `DELETE /api/memory/entries/{id}` does not remove the row. It sets the entry's `valid_until` to the current time (`server/internal/db/repo/memory_repo.go`, `ExpireEntry`), which hides the entry from future retrieval but leaves its `summary`/`content` in the database indefinitely. Nothing runs on a schedule to expire an entry by age — expiry only happens when a caller with a `memory.write` grant makes that call. `PATCH /api/memory/entries/{id}` behaves the same way for the opposite reason: it marks an entry superseded rather than removing it, so the chain of what replaced what survives as an audit trail. Concrete deletion paths:

| Data | How to delete |
|---|---|
| `users` (account record) | `DELETE /api/me` — permanently removes the authenticated user's account (GDPR right-to-erasure; `server/internal/api/auth/handler.go`, `DeleteMe`). |
| `tasks` + their `stage_runs`, `permission_requests` | `DELETE /api/tasks/{id}` — deletes the task and cascades its stage history. |
| `web_push_subscriptions` | Unsubscribe in the browser, or delete the row. |
| `audit_events`, `agent_cost_trends` | **No API delete path.** Prune manually with a SQLite client (e.g. `DELETE FROM audit_events;`) or delete the entire database file at `DASHBOARD_DB_PATH` (default `~/.claude/dashboard-tasks.db`). |
| `memory_entries`, `memory_injections`, and memory spaces (`resource` rows) | **No delete path of any kind exists today**, not even the manual-prune kind above. `DELETE`/`PATCH /api/memory/entries/{id}` mark a row expired or superseded (see above) but never remove it, and there is no path at all for a memory space or an injection record. A future deletion path for a space must first refuse while any `memory_entries` still reference it — `memory_entry.space_id` is a loose string with no foreign key, so deleting the space row out from under existing entries would silently orphan them instead of failing loudly (`CreateSpace`'s own comment in `memory_repo.go` records this constraint). Until such a path exists, the only way to remove memory content is to delete the entire database file, which erases everything else in it too. |

To erase everything at once, stop the dashboard and delete the database file at `DASHBOARD_DB_PATH`. Filesystem-derived monitoring data (the JSONL session logs under `~/.claude/projects/`) is never written by the dashboard — delete those files directly if needed.

---

## 3. Data That Leaves the Machine (Opt-In Only)

None of the following integrations are active by default. Data only leaves your machine when you explicitly configure and enable them.

### GitHub OAuth (`GITHUB_CLIENT_ID` + `GITHUB_CLIENT_SECRET`)

- OAuth flow redirects your browser to `github.com` (US). GitHub's privacy policy applies.
- Scope requested: `read:user` only.
- The access token is used once to fetch your GitHub user ID, login name, and avatar URL, then discarded. It is never stored.
- Only the GitHub user ID, login, and avatar URL are persisted locally (in the `users` table).
- **Transfer basis:** GitHub Inc. is a US entity covered by the EU–US Data Privacy Framework (DPF).

### Web-Push notifications (VAPID)

- If you subscribe to browser push notifications, your browser generates a push endpoint URL (hosted by Mozilla Autopush, Google FCM, or Apple APNs depending on your browser) and sends it to the dashboard.
- This endpoint URL is stored locally in the SQLite database and sent to the push service when a notification is dispatched.
- Push endpoint URLs are device-specific and qualify as pseudonymous personal data under GDPR.
- **Retention:** Push subscriptions persist until you unsubscribe in the browser or delete the database row. There is no automatic expiry — delete entries manually if no longer needed.
- **Transfer basis:** Mozilla (IE/EU entity), Google LLC (US, DPF), Apple Inc. (US, DPF).

### LLM adapters (OpenAI / Ollama / Anthropic / custom)

**OpenAI** — If you configure an OpenAI-backed spawner, stage agent prompts (including task descriptions and stage outputs) are sent to `api.openai.com` (US). **Transfer basis:** OpenAI Inc. is a US entity covered by the EU–US Data Privacy Framework (DPF).

**Ollama** — Defaults to `http://localhost:11434`; in the default configuration no data leaves the machine. If you configure a remote `base_url`, full stage-agent prompts (task descriptions, stage outputs, possibly source-code excerpts) are sent to that host. You are responsible for that host's data-residency and applicable transfer basis.

**Anthropic** — If you configure an `anthropic` spawner (`adapter_type: "anthropic"`), stage-agent prompts (task descriptions, stage outputs, and possibly source-code excerpts) are sent to `api.anthropic.com` (US) via the `anthropic-spawner` binary, which reads the `ANTHROPIC_API_KEY` environment variable from the server process. **Transfer basis:** Anthropic PBC is a US entity covered by the EU–US Data Privacy Framework (DPF) where applicable.

**Memory content in a stage prompt:** every one of the three bullets above says "stage agent prompts" leave the machine when a remote adapter is configured. As of the memory store (section 2), that prompt can include up to 2,000 characters of ranked memory-entry summaries — text you or an agent previously wrote into the memory store — appended to the stage's own instructions before the spawn happens. This travels to whichever provider that stage's spawner targets, under the same transfer basis named above; it never goes anywhere on its own, and a stage run against a local adapter (default Ollama, or no adapter at all) keeps that memory content on your machine like everything else in the prompt.

### Office365 OAuth plugin (if configured)

- If you configure the Office365 OAuth plugin, the OAuth flow redirects your browser to Microsoft identity endpoints. Data residency depends on your Microsoft tenant region; DPF applies for US-based tenants.
- **Scopes requested:** `openid profile email User.Read` (always). `GroupMember.Read.All` is added when `OFFICE365_ALLOWED_GROUP_ID` is set to gate access to a specific Azure AD group.
- When `GroupMember.Read.All` is requested, the plugin calls `/me/memberOf` on Microsoft Graph and reads the signed-in user's Azure AD group memberships to check for the required group ID. Group IDs are used only to evaluate membership in memory and are **not persisted**.
- The user's `UserPrincipalName` (an email address) is persisted as `login` in the `users` table, alongside the Microsoft user ID and display name. The access token is used once and then discarded — it is never stored.
- **What is persisted:** Microsoft user ID, UserPrincipalName (email address as `login`), and display name — in the `users` table. No group IDs are stored.

### Webhook and email notifications (forward-looking)

The notification-config API accepts and persists `webhook_url` (with HMAC authentication) and an `email` channel. **No delivery code for these channels exists yet** — as of the current release, configuring them stores the URL or address locally but nothing is sent via them. When and if delivery is implemented, task notification payloads (title, status, and stage output, which may contain source-code excerpts or other content from the task) will be sent to the configured SMTP server or webhook endpoint. You will be responsible for that endpoint's data handling and applicable transfer basis. This document will be updated when delivery is shipped.

### Issue-tracker import (Jira / GitHub)

- If you paste a GitHub or Jira issue reference into the **Import from issue** field and click **Fetch**, the dashboard sends the configured GitHub personal-access token or Jira API token, together with the issue reference, to `api.github.com` or your Jira Cloud tenant (`*.atlassian.net`) to resolve the issue.
- The response (issue title, body, URL, labels) is pulled into the local task description — this content may include personal data of third parties (e.g. issue reporters or commenters quoted in the body).
- Tokens are stored encrypted at rest (AES-GCM secretbox) in the SQLite database and masked in the settings UI (**Settings → Tracker**).
- **Transfer basis:** GitHub Inc. is a US entity covered by the EU–US Data Privacy Framework (DPF). Jira Cloud's transfer basis depends on your Atlassian tenant's data-residency region — you are responsible for confirming it meets your compliance requirements.

### Remote dashboard proxy (`DASHBOARD_REMOTES`)

- If you configure remote instances, the local dashboard acts as a proxy and aggregates session data from those instances over your network.
- No data flows to a third-party service — only between self-hosted instances you control.
- **Security requirement:** Remote instances must be reachable only via a VPN or SSH tunnel. Never bind a remote instance to `0.0.0.0` on an untrusted network. The local dashboard logs a security warning if a non-loopback bind address is detected.

---

## 4. What Is NOT Collected

The following is explicitly absent from the codebase and has been grep-verified during an audit (2026-07-12):

- No telemetry or usage analytics
- No phone-home pings or update-check requests
- No Sentry, Datadog, or error reporting services
- No third-party analytics pixels or tracking scripts
- No advertising identifiers
- No background sync to any cloud service

The dashboard makes no outbound network connections unless you configure an integration in section 3 — this now explicitly includes the issue-tracker import feature (GitHub / Jira), which is opt-in and only triggered when you fetch an issue.

---

## 5. Multi-User and Multi-Machine Responsibilities

### Multi-user machines

If you run this dashboard on a shared machine (e.g. a shared CI runner or team server) and other users can access it, you are acting as a **data controller** under GDPR for any personal data processed on their behalf. You must:

- Inform those users about the data practices described in sections 1–3.
- Configure GitHub OAuth (`GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET`) to enforce per-user authentication. Without OAuth, any process with access to `127.0.0.1:13120` has full dashboard access.
- Ensure appropriate access controls on the SQLite database file.

### Multi-machine mode

When using `DASHBOARD_REMOTES` to aggregate agents across machines:

- Session data (transcripts, tokens, costs) from remote instances is fetched and displayed locally.
- Remote instances must only be reachable via a VPN or SSH tunnel — never via a public IP or by binding to `0.0.0.0`.
- Each remote instance operator is independently responsible for the data practices described in this document.

---

## Questions

This is an open-source tool (MIT license). If you have questions about data handling, open an issue in the repository or inspect the source code directly — the relevant files are `server/internal/parser/`, `server/internal/merger/`, `server/internal/memory/`, `server/internal/api/`, and the plugin directories.
