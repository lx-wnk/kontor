# Shell Statusline

[`scripts/statusline.py`](../../scripts/statusline.py) integrates the dashboard into your shell prompt, showing live agent count, active cost rate, and total tokens.

```bash
# zsh — add to ~/.zshrc
_agent_status() {
    local out
    out=$(python3 /path/to/kontor/scripts/statusline.py 2>/dev/null)
    [[ -n "$out" ]] && echo " [$out]"
}
PROMPT='%n@%m %~$(_agent_status) %# '

# bash — add to ~/.bashrc
_agent_status() {
    python3 /path/to/kontor/scripts/statusline.py 2>/dev/null
}
PROMPT_COMMAND='export PS1="\u@\h \w [$(_agent_status)] \$ "'
```

## Options

| Flag | Default | Description |
|---|---|---|
| `--port PORT` | `13120` | Dashboard HTTP port |
| `--timeout SECS` | `0.5` | Request timeout (keep low for PS1 responsiveness) |
| `--format text\|json` | `text` | Output format |

## Environment

| Variable | Description |
|---|---|
| `DASHBOARD_API_URL` | Override base URL (default: `http://127.0.0.1:<port>`) |
| `DASHBOARD_API_TOKEN` | Bearer token if auth is enabled |

If the dashboard is not running or the request times out, the script exits silently — your prompt is never stalled.
