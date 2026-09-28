# Controlling & Spawning Agents

## Controlling running agents

Agents spawned from the dashboard get the `dashboard-channel` MCP server automatically. When a channel is active, a green **CH** badge appears in the agent table. Open the agent modal to send follow-up messages, send `/btw` interrupts, and view replies.

For agents started **manually** outside the dashboard, inject the channel binary yourself:

```bash
claude --mcp-config '{"mcpServers":{"dashboard-channel":{"command":"/path/to/bin/dashboard-channel"}}}'
```

Use the built-in `kontor live` command, which loads the channel MCP automatically and selects the best transport (tmux if available, pty broker otherwise):

```bash
kontor live
kontor live --resume <session-id>
kontor live --yolo   # adds --dangerously-skip-permissions
```

## Spawning new agents

Click **"+ New Agent"** in the header to open the spawn dialog.

| Field | Required | Description |
|---|---|---|
| Prompt | Yes | What the agent should do |
| Working Directory | Yes | Project path the agent runs in |
| Model | No | e.g. `claude-opus-5`, `claude-sonnet-5`, `claude-haiku-4-5` |
| System Prompt | No | Custom system instructions |
| Enable Channel | No | Dashboard control channel (default: on) |

Spawned agents run **detached** — they survive dashboard restarts and appear in the table within ~3 seconds.

## Slash commands

Typing `/` in the prompt input opens a menu with two kinds of command.

**Dashboard commands** are executed by the dashboard itself against its own API — the agent never
sees them:

| Command | Arguments | Needs a linked task |
| --- | --- | --- |
| `/spawn` | `<slug> <description>` | no |
| `/grant` | `<toolName>` | yes |
| `/cancel` | — | yes |
| `/retry` | — | yes |
| `/promote` | — | yes |
| `/help` | — | no |

**Session commands** are everything the connected Claude session itself knows — its built-ins, your
`~/.claude/commands`, project commands, plugin commands, and every installed skill (each skill is
typeable as `/<name>`). They are discovered per session via `GET /api/slash-commands` and forwarded
to the agent verbatim, so what works is whatever that session supports.

Claude's own built-in commands are the one group the dashboard cannot discover — the CLI exposes no
machine-readable listing, so they are curated per version (`CuratedBuiltinsVersion`). When a session
reports a different version, the menu says so on every `/` query — including one that matches no
command at all, which is the case the note exists for: a command missing from the list may still
work if you type it in full. Re-curating means checking both directions — the CLI binary ships a "Recently
changed surfaces" document naming removed and renamed commands, while additions have to come from
the release notes.

Each entry shows its argument template next to the name, read from the command file's
`argument-hint:` frontmatter — `/branch-review` displays `[base-branch] [--apply-fixes]`, for
example. Commands without that key show no template; built-ins never carry one, since they have no
file on disk to read it from.

That template is file content, and the file may belong to an installed plugin rather than to you, so
it is sanitised server-side before it reaches the API: a value that is not valid UTF-8 is dropped,
control characters and Unicode bidi overrides are stripped, and the hint is capped at 120
characters. The menu clips it to 60 characters for display and shows the full value on hover. Treat
a hint as what the command's author suggests you type, not as advice from the dashboard.

## Permissions

Stage agents run with an allow-list derived from `task_permissions` rows. Grants flow through a single validated path (`bulkGrantPermissions`) checked against an allow-list and a dangerous-bash block-list. Permission templates provide quick presets: `feature_implementation`, `research_only`, `test_only`, `review_only`.

Spawned agents request anything missing via the channel's `request_permission` MCP tool — prefer the bulk form so the user grants everything as one batch decision. The full self-service flow is documented in [`.agent-context/permissions.md`](../../.agent-context/permissions.md).

### Answering a permission prompt from the dashboard

By default a session that needs approval stops and asks in its own terminal, and
the dashboard can only watch. The **permission bridge** moves that decision into
the dashboard for any session — including ones you started by hand — by
registering two Claude Code hooks:

```bash
kontor hooks install
```

That writes the hook script to `~/.claude/dashboard-hooks/` (mode `0700`,
extracted from the binary — nothing needs to stay in a checkout) and registers a
`PreToolUse` and a `Notification` entry in `~/.claude/settings.json` (or
`$CLAUDE_CONFIG_DIR/settings.json`). Existing hooks are kept; re-running the
command rewrites the script and repairs the registered path if the binary moved.
Settings are read when a session starts, so restart anything already running.
`kontor hooks uninstall` removes the entries that point at that script
and leaves every other hook alone.

`docs/hooks-setup.md` describes registering hooks by hand for the notification
receiver — that is a separate mechanism. `hooks install` manages only the two
entries it wrote — the ones running the script out of `~/.claude/dashboard-hooks/`
— and preserves whatever else is in the file. An entry running your own copy of
the same script from somewhere else is left alone: install refuses rather than
replacing it, and uninstall names it on stderr rather than deleting it.

**Then arm the sessions you want intercepted.** Nothing is held by default: the
`PreToolUse` hook fires *before* Claude Code decides whether to prompt at all, so
holding every call would stall every session on the machine. Click the lock on an
agent's card, or **Intercept next** on a card whose prompt already reached its
terminal. Arming lasts 30 minutes per session.

With it installed:

- Claude Code calls the hook **before** it draws its own prompt. The dashboard
  holds that call open for 25 seconds and shows **Allow** / **Deny** on the
  agent's card in the needs-you band.
- Answering there releases the run immediately. The terminal never prompts.
- If nobody answers in time, the hold lapses and the session falls back to
  asking in its terminal exactly as it does without the bridge — the card then
  reads **Answer in terminal** for up to 15 minutes, which is how long someone
  who stepped away is given before the dashboard stops claiming a prompt is on
  screen.
- The standing rule for future runs is offered beside that only when the bridge
  can name the call the prompt is about, which it can when it held that call.
  The terminal notice fires once when a prompt opens and never when it is
  answered, and the trail's own pending tool call is reconstructed separately —
  so without a name, the rule would be written for whichever tool the trail
  happens to show, not the one on screen.

**Your own deny rules stay the floor.** A hook answering "allow" short-circuits
Claude Code's permission evaluation entirely, deny rules included — so the bridge
checks them first. When a held call is covered by a `permissions.deny` entry in
your user or project `settings.json`, the card shows the rule instead of an
**Allow** button and only **Deny** is offered. The server refuses an allow for
such a call regardless of what the client sends. A rule shape the bridge cannot
parse is treated as a match: it declines to offer rather than release something
it did not understand.

**The bridge is off under `DASHBOARD_AUTH=none`.** That mode drops JWT, leaving
loopback and an `Origin` header any non-browser process sets for itself — so
"a human decided" would reduce to "any local process decided", while a hook
allow short-circuits Claude Code's own evaluation. The arm and respond endpoints
are not mounted at all in that mode. Nothing is held, sessions prompt in their
terminals as they do without the bridge, and the dashboard still reports that
one is waiting there.

The lapse is the important property: the hook answers "no decision", never
"allow". A dashboard that is stopped, slow, or unreachable, a missing secret, a
machine without `curl` — every one of those paths degrades to the behaviour you
have today. Nothing is approved because something failed.

The hook authenticates with the secret in `~/.claude/dashboard-hooks-secret`
(mode `0600`, generated on first boot). It is deliberately not written into
`settings.json`, which is a file people share and check in.

Why hooks rather than ACP or the MCP endpoint: both of those are established
when a session **starts**, so they cannot reach a session that is already
running or one launched outside the dashboard. Hooks are ambient configuration —
a file, not a handshake — which is what makes a foreign terminal session
reachable at all.
