#!/usr/bin/env bash
# Statusline wrapper for the agent dashboard.
#
# Claude Code's statusLine command pipes JSON containing rate_limits on stdin.
# This script:
#   1. Reads stdin into $payload.
#   2. Prints the status line: $payload piped through $KONTOR_STATUSLINE_CMD if
#      set, otherwise a minimal "[<model>] <dir>" line (empty without jq).
#   3. POSTs the plan-usage sample to Kontor in the background, fire-and-forget.
#      curl -m 1 ensures it never blocks, and all output goes to /dev/null.
#
# Environment:
#   KONTOR_URL                default http://127.0.0.1:13120
#   KONTOR_STATUSLINE_CMD     when set, the payload is piped into this command
#                             for the original statusline chain (replaces the
#                             default line).
#                             May include arguments (e.g. "my-status --flag");
#                             executed via sh -c.
#   KONTOR_HOOKS_SECRET       overrides the secret file below
#   DASHBOARD_URL             legacy alias for KONTOR_URL
#   DASHBOARD_HOOKS_SECRET    legacy alias for KONTOR_HOOKS_SECRET
#   CLAUDE_CONFIG_DIR         identifies the account; passed through to Kontor
set -u

payload="$(cat)"

# Chain stdout: pass the payload to the next statusline command unchanged.
if [ -n "${KONTOR_STATUSLINE_CMD:-}" ]; then
  printf '%s' "$payload" | sh -c "$KONTOR_STATUSLINE_CMD"
elif command -v jq >/dev/null 2>&1; then
  printf '%s' "$payload" | jq -r '[(.model.display_name // empty | "[\(.)]"), ((.workspace.current_dir // .cwd // empty) | split("/") | last)] | join(" ")' 2>/dev/null
fi

# Resolve Kontor URL and secret.
url="${KONTOR_URL:-${DASHBOARD_URL:-http://127.0.0.1:13120}}"
secret="${KONTOR_HOOKS_SECRET:-${DASHBOARD_HOOKS_SECRET:-}}"
for secret_file in "${HOME}/.claude/kontor-hooks-secret" "${HOME}/.claude/dashboard-hooks-secret"; do
  if [ -z "$secret" ] && [ -r "$secret_file" ]; then
    secret="$(tr -d '[:space:]' < "$secret_file")"
  fi
done

# No secret or no curl: nothing to report.
[ -n "$secret" ] || exit 0
command -v curl >/dev/null 2>&1 || exit 0

# Build the POST body using jq to safely encode config_dir and the payload's
# rate_limits block. If jq is missing, the payload is not valid JSON, or it
# carries no rate_limits, skip the POST silently.
command -v jq >/dev/null 2>&1 || exit 0
config_dir="${CLAUDE_CONFIG_DIR:-${HOME}/.claude}"
post_body="$(printf '%s' "$payload" | jq -e --arg cd "$config_dir" 'select(.rate_limits != null) | {"config_dir":$cd,"rate_limits":.rate_limits}' 2>/dev/null)" || exit 0

# Fire-and-forget: -m 1 caps connect+transfer, background + redirect ensures
# the statusline never waits on Kontor. The secret goes via --config stdin,
# never in argv.
(
  printf 'header = "Authorization: Bearer %s"\nnoproxy = "*"\n' "$secret" \
    | curl -sS --config - \
        -H 'Content-Type: application/json' \
        -m 1 \
        --data-binary "$post_body" \
        "$url/api/hooks/plan-usage" \
        >/dev/null 2>&1
) &
exit 0
