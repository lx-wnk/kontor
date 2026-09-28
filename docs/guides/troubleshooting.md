# Troubleshooting

## Restart

`POST /api/admin/restart` triggers a validated, graceful restart. The endpoint refuses with **409** if an active `auth_provider` plugin is currently unhealthy — restarting in that state would cause an auth lockout on the next boot.

**Activating an `auth_provider` plugin requires a restart to apply** (auth is boot-wired, not live-reloadable).

**Default (`KONTOR_RESTART_MODE=reexec`):** the process re-execs itself in place — same PID, no supervisor needed. Works with plain `./bin/agent-dashboard serve`.

**Rebuilding first.** A plain restart re-execs the *same binary on disk*, so it never picks up a merged change. Send `{"rebuild": true}` — or use **Rebuild and restart** in **Settings → Server** — and the server builds first and only relaunches if that succeeded. A failed build answers **500** with the last 40 lines of output and leaves the running binary untouched. The build command is fixed in Go; nothing from the request reaches it.

The status bar warns when the running build is older than the source it was built from, comparing the binary's stamped version against `git describe` in the server's working directory. If either is unknown, no warning is shown.

**Supervised (`KONTOR_RESTART_MODE=exit`):** the process exits cleanly so the supervisor relaunches it.

| Supervisor | Required config |
|---|---|
| systemd | `Restart=always` in the service unit |
| launchd | `KeepAlive` in the plist |
| Wrapper loop | `while true; do ./bin/kontor serve; done` |

## Locked out?

A bad `auth.mode` or a broken `auth_provider` plugin can lock you out of the UI. The CLI edits the SQLite database directly, so it works even while the server is down:

```bash
kontor settings set auth.mode none   # reset auth, then restart the server
kontor grants add memory.read --pattern '*' --scope global --mode allow
```

See [Configuration](configuration.md) for the full settings/grants/plugins CLI reference.
