#!/usr/bin/env bash
# Permission bridge for the agent dashboard.
#
# Registered as a PreToolUse and a Notification hook. Claude Code writes the
# event as JSON on stdin and reads this script's stdout, which it interprets as
# the permission decision for the tool call about to run.
#
# Because stdout IS a security decision, this script prints only a response it
# has checked: HTTP 200, and a body that is one of the two shapes the bridge is
# allowed to emit. Everything else — a 401 from a rotated secret, an HTML page
# from an unrelated process on the port, a transport failure, a missing secret,
# no curl — prints nothing, and Claude Code then does exactly what it does
# without this hook installed: it asks in the terminal. Silence is the safe
# answer, so every path that is not a verified decision takes it.
#
# Environment:
#   DASHBOARD_URL           default http://127.0.0.1:13120
#   DASHBOARD_HOOKS_SECRET  overrides the secret file below
#
# The secret is read from ~/.claude/kontor-hooks-secret, or the pre-rename
# dashboard-hooks-secret (0600, written by the server on first boot) rather than required in the environment: a session
# started by hand in a terminal inherits no dashboard variables, and putting the
# secret in settings.json would leave it in a file meant to be shared.
set -u

url="${KONTOR_URL:-${DASHBOARD_URL:-http://127.0.0.1:13120}}"
secret="${KONTOR_HOOKS_SECRET:-${DASHBOARD_HOOKS_SECRET:-}}"
# Both file names are read: the server keeps using whichever already exists, so
# a script installed after the rename still has to find a secret written before
# it.
for secret_file in "${HOME}/.claude/kontor-hooks-secret" "${HOME}/.claude/dashboard-hooks-secret"; do
  if [ -z "$secret" ] && [ -r "$secret_file" ]; then
    secret="$(tr -d '[:space:]' < "$secret_file")"
  fi
done
payload="$(cat)"

[ -n "$secret" ] || exit 0
command -v curl >/dev/null 2>&1 || exit 0

# The secret goes to curl over stdin, never in argv: process arguments are
# readable by any process of the same user, and world-readable on a default
# Linux. --noproxy keeps a loopback call on loopback — curl otherwise honours an
# inherited http_proxy and would send the tool input and this header to it.
curl_common() {
  printf 'header = "Authorization: Bearer %s"\nnoproxy = "*"\n' "$secret" \
    | curl -sS --config - \
        -H 'Content-Type: application/json' \
        --data-binary "$payload" \
        "$@"
}

if [ "${1:-permission}" = notification ]; then
  # Fire-and-forget: this only records that the terminal is asking. Any other
  # argument falls through to the permission path below, so the dispatch is a
  # single explicit test rather than a case with no default arm.
  curl_common -m 5 -o /dev/null "$url/api/hooks/notification" >/dev/null 2>&1
  exit 0
fi

# --max-time must outlast the server's own hold (25s) so the server is the one
# that decides to give up, and must stay under the `timeout` configured for this
# hook in settings so Claude Code's fallback is not what cuts us off.
#
# --fail makes curl exit non-zero on 4xx/5xx AND print nothing. Without it a 401
# body was assigned to $response and printed as the hook's decision. --fail
# rather than --fail-with-body: the error body is of no use here, and --fail has
# been in curl since long before the versions on older LTS distributions, where
# an unknown option would silently disable the bridge.
response="$(curl_common --fail -m 28 "$url/api/hooks/permission" 2>/dev/null)" || exit 0

# Print only a decision the bridge is allowed to make. An empty object is its
# "no decision"; anything unrecognised is treated the same way, so a spoofed or
# confused endpoint cannot put arbitrary text on the hook's stdout.
case "$response" in
  *'"permissionDecision"'*'"allow"'*|*'"permissionDecision"'*'"deny"'*)
    printf '%s\n' "$response"
    ;;
esac
exit 0
