#!/usr/bin/env bash
# Boot the real dashboard server for Playwright E2E: the embedded Vue SPA + the
# live /api — the same shape the wails desktop app runs at runtime. Rebuild only
# when the binary is missing or a tracked source is newer, so warm runs start fast.
#
# Deliberately NOT on the dashboard's own port and DB: a desktop app or a plain
# `serve` running on 13120 would otherwise be reused as the test server (serving
# its own, older embedded SPA), and both processes would write the same SQLite
# file — which the specs mutate. Keep E2E_PORT in sync with playwright.config.ts.
set -euo pipefail
cd "$(dirname "$0")/.."

BIN=bin/kontor
E2E_PORT="${E2E_PORT:-13199}"
E2E_DB="${E2E_DB_PATH:-$PWD/.e2e/dashboard-e2e.db}"

# The scanned set must cover everything that ends up in the embedded bundle, not
# just the languages: a change to index.html, a stylesheet or a file in public/
# would otherwise leave the old SPA embedded, and the suite would report a green
# run against a bundle that never contained the change.
if [[ ! -x "$BIN" ]] || [[ -n "$(find server sdk src public index.html -type f \( -name '*.go' -o -name '*.vue' -o -name '*.ts' -o -name '*.css' -o -name '*.html' -o -name '*.js' \) -newer "$BIN" -print -quit 2>/dev/null)" ]]; then
  echo "e2e-server: building (missing or stale binary)…"
  task build:all
fi

mkdir -p "$(dirname "$E2E_DB")"
export DASHBOARD_PORT="$E2E_PORT"
export DASHBOARD_DB_PATH="$E2E_DB"

exec "$BIN" serve
