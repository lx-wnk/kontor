#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
OUTPUT="${REPO_ROOT}/THIRD_PARTY_LICENSES.md"
GO_LICENSES="$(go env GOPATH)/bin/go-licenses"

# go-licenses v1.6.0 errored on every stdlib package under Go 1.26 ("Package
# ... does not have module info. Non go modules projects are no longer
# supported"), which collect_go() tolerated via `|| true` — silently emptying
# the Go section. Bumped 2026-07-14 to go-licenses/v2@v2.0.1, which runs cleanly
# under Go 1.26. The v2 bump corrected one long-standing misclassification:
# modernc.org/libc was reported MIT by v1.6.0 (it had classified the bundled
# LICENSE-3RD-PARTY.md), but the module's own LICENSE is 3-clause BSD — v2
# reads the right file and reports BSD-3-Clause. Install with:
#   go install github.com/google/go-licenses/v2@v2.0.1
GO_LICENSES_VERSION="v2.0.1"
MIN_GO_DEP_ROWS=20

# ── Tool check ─────────────────────────────────────────────────────────────────

if [[ ! -x "${GO_LICENSES}" ]]; then
  echo "ERROR: go-licenses not found at ${GO_LICENSES}" >&2
  echo "Install it with: go install github.com/google/go-licenses/v2@${GO_LICENSES_VERSION}" >&2
  exit 1
fi

# ── Known license overrides ────────────────────────────────────────────────────
# Manual classifications for modules go-licenses cannot identify itself, keyed
# by module path. Empty on purpose: go-licenses v2.0.1 classifies every module
# in the current graph, so nothing needs overriding. The v1.6.0-era entry for
# modernc.org/mathutil was removed once v2 started reporting it correctly as
# BSD-3-Clause on its own — an override that no longer overrides anything would
# silently mask a future regression instead of letting the ',Unknown,' gate
# below report it.
declare -A LICENSE_OVERRIDES=()

# ── Temp files ─────────────────────────────────────────────────────────────────

TMP_GO_RAW="$(mktemp)"
TMP_GO_FIXED="$(mktemp)"
TMP_FRONTEND_JSON="$(mktemp)"
TMP_OUTPUT="$(mktemp)"
trap 'rm -f "${TMP_GO_RAW}" "${TMP_GO_FIXED}" "${TMP_FRONTEND_JSON}" "${TMP_OUTPUT}"' EXIT

# ── Collect Go deps ────────────────────────────────────────────────────────────

# The Go build list is GOOS-dependent (build constraints pull in different
# packages per platform, e.g. mattn/go-isatty on darwin only). Pin GOOS/GOARCH
# to the linux/amd64 CI target so the attribution is canonical no matter which
# platform regenerates it.
export GOOS=linux
export GOARCH=amd64

# go-licenses' exit status for the module collect_go() last ran. Its stdout is
# the CSV stream the caller redirects into TMP_GO_RAW, so the status cannot be
# returned normally — collect_module() reads it from here.
GO_LICENSES_STATUS=0

# The `|| true` below keeps a failing module from aborting the run before
# collect_module() can produce a diagnostic; it is not tolerance. Stderr is left
# visible so genuine failures surface in CI logs instead of being silently
# swallowed.
collect_go() {
  local dir="$1"
  local gowork_off="${2:-false}"
  # Optional GOOS override for platform-gated modules (e.g. desktop/ is
  # //go:build darwin). go-licenses cross-lists via packages.Load, so a fixed
  # non-host GOOS stays deterministic across CI/dev. Defaults to the global pin.
  local goos="${3:-${GOOS}}"
  local st=0

  if [[ "${gowork_off}" == "true" ]]; then
    (cd "${dir}" && GOWORK=off GOOS="${goos}" go build ./... 2>/dev/null || true)
    (cd "${dir}" && GOWORK=off GOOS="${goos}" "${GO_LICENSES}" report ./... \
      --ignore github.com/lx-wnk/agent-dashboard) || st=$?
  else
    (cd "${dir}" && GOOS="${goos}" go build ./... 2>/dev/null || true)
    (cd "${dir}" && GOOS="${goos}" "${GO_LICENSES}" report ./... \
      --ignore github.com/lx-wnk/agent-dashboard) || st=$?
  fi
  GO_LICENSES_STATUS=${st}
}

# A module is expected to contribute at least one row when the package graph
# go-licenses walks — `./...` without tests — reaches a module outside our own
# module family (the same "github.com/lx-wnk/agent-dashboard" prefix passed to
# --ignore above). Deriving this from go.mod requires instead would hard-fail a
# module whose only external require is test-only (testify, go-cmp): go.mod
# lists it, go-licenses never sees it, and zero rows would be correct.
# sdk currently reaches nothing external at all, so it falls out of this on its
# own — no name-based special case needed.
#
# The probe must never fail open: a broken module, a missing python3, or a
# toolchain failure would otherwise make it answer "expects nothing" for exactly
# the module whose collection just failed for the same reason. Anything other
# than a clean yes/no is a hard error.
module_expects_go_rows() {
  local dir="$1" gowork_off="$2" goos="$3"
  local dep_modules

  if [[ "${gowork_off}" == "true" ]]; then
    dep_modules="$(cd "${dir}" && GOWORK=off GOOS="${goos}" go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./...)" || dep_modules="__FAILED__"
  else
    dep_modules="$(cd "${dir}" && GOOS="${goos}" go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./...)" || dep_modules="__FAILED__"
  fi

  if [[ "${dep_modules}" == "__FAILED__" ]]; then
    echo "" >&2
    echo "ERROR: 'go list -deps' failed in ${dir} (see the error above) — cannot tell" >&2
    echo "whether that module should have contributed license rows. Refusing to guess." >&2
    exit 1
  fi

  # No trailing '/': the plugin modules are named agent-dashboard-plugin-*, and
  # go-licenses' --ignore is a bare HasPrefix too. Adding one would reclassify
  # every plugin as third-party and hard-fail the five that have no deps.
  grep -qv '^github\.com/lx-wnk/agent-dashboard' <<<"${dep_modules}"
}

# Prints the hand-runnable go-licenses invocation for one module, so both
# guards below point at the same reproduction command.
print_module_repro_cmd() {
  local dir="$1" gowork_off="$2" goos="$3"
  if [[ "${gowork_off}" == "true" ]]; then
    echo "  (cd ${dir} && GOWORK=off GOOS=${goos} ${GO_LICENSES} report ./... --ignore github.com/lx-wnk/agent-dashboard)" >&2
  else
    echo "  (cd ${dir} && GOOS=${goos} ${GO_LICENSES} report ./... --ignore github.com/lx-wnk/agent-dashboard)" >&2
  fi
}

# Wraps collect_go() with the per-module row-count guard: a module whose
# package graph reaches external modules (per module_expects_go_rows above) but whose
# collection produced zero rows means the collection silently failed for that
# module alone — the exact way #329 dropped the Go dependency count from 72 to
# 63 without the script noticing. MIN_GO_DEP_ROWS below only catches a total
# collapse; this catches one module vanishing.
collect_module() {
  local dir="$1" gowork_off="$2" goos="$3" label="$4"
  echo "Collecting Go deps: ${label}..."

  local before after row_count
  before="$(wc -l < "${TMP_GO_RAW}")"
  collect_go "${dir}" "${gowork_off}" "${goos}" >> "${TMP_GO_RAW}"
  after="$(wc -l < "${TMP_GO_RAW}")"
  row_count=$(( after - before ))

  if (( GO_LICENSES_STATUS != 0 )); then
    echo "" >&2
    echo "ERROR: go-licenses exited ${GO_LICENSES_STATUS} for ${label} (${dir}) after emitting" >&2
    echo "${row_count} row(s). go-licenses v2 writes its CSV only once the whole walk has" >&2
    echo "succeeded, so a non-zero status means this module's rows are missing outright —" >&2
    echo "the #329 failure mode, caught per module instead of only in the total." >&2
    echo "" >&2
    echo "An unclassifiable license does NOT land here: v2 logs it, emits an 'Unknown'" >&2
    echo "row and still exits 0, which the ',Unknown,' gate below catches and which" >&2
    echo "LICENSE_OVERRIDES resolves. A non-zero status is a real failure. Rerun the" >&2
    echo "module's collection by hand to see it:" >&2
    print_module_repro_cmd "${dir}" "${gowork_off}" "${goos}"
    exit 1
  fi

  if (( row_count == 0 )) && module_expects_go_rows "${dir}" "${gowork_off}" "${goos}"; then
    echo "" >&2
    echo "ERROR: ${label} (${dir}) produced zero Go dependency rows, but its" >&2
    echo "package graph ('./...', no tests) reaches at least one external module —" >&2
    echo "its dependencies are missing from" >&2
    echo "the license attribution. This is the #329 failure mode: an untidy" >&2
    echo "module made go-licenses die outright, and the '|| true' tolerance in" >&2
    echo "collect_go() silently dropped every row it should have produced." >&2
    echo "" >&2
    echo "Investigate by rerunning this module's collection by hand to see the real" >&2
    echo "go-licenses error:" >&2
    print_module_repro_cmd "${dir}" "${gowork_off}" "${goos}"
    exit 1
  fi
}

# ── Module registry ─────────────────────────────────────────────────────────────
# Every Go module this script scans, with the GOWORK/GOOS handling each one
# needs (plugins run GOWORK=off; desktop only resolves under GOOS=darwin).
# Shared by the pre-flight tidy check and the collection loop below so the
# two lists can never drift apart.
MODULE_DIRS=()
MODULE_GOWORK_OFF=()
MODULE_GOOS=()
MODULE_LABELS=()

register_module() {
  MODULE_DIRS+=("$1")
  MODULE_GOWORK_OFF+=("$2")
  MODULE_GOOS+=("$3")
  MODULE_LABELS+=("$4")
}

register_module "${REPO_ROOT}/server" false "${GOOS}" "server"
# sdk has no external deps currently; included so future additions are captured
register_module "${REPO_ROOT}/sdk" false "${GOOS}" "sdk"
for plugin_dir in "${REPO_ROOT}"/plugins/*/; do
  [[ -f "${plugin_dir}go.mod" ]] || continue
  register_module "${plugin_dir%/}" true "${GOOS}" "plugins/$(basename "${plugin_dir}")"
done
# desktop/ is a macOS-only wails app (//go:build darwin); its real dependency
# graph only exists under GOOS=darwin. go-licenses cross-lists it from any host.
register_module "${REPO_ROOT}/desktop" true darwin "desktop"

# ── Pre-flight: refuse to scan an untidy module ───────────────────────────────
# This is the actual root cause behind #329: Dependabot bumped desktop/go.mod
# without running `go mod tidy`, so the graph go.mod/go.sum describe no longer
# matched what actually builds. go-licenses died outright on that module, and
# collect_go()'s `|| true` tolerance — there for benign classification
# failures, not this — silently dropped every row it should have produced.
# Check every module before collecting anything, so a bad module fails loud
# instead of quietly shrinking the output.
module_tidy_check() {
  local dir="$1" gowork_off="$2" goos="$3" label="$4"
  local diff_output status=0

  if [[ "${gowork_off}" == "true" ]]; then
    diff_output="$(cd "${dir}" && GOWORK=off GOOS="${goos}" go mod tidy -diff 2>&1)" || status=$?
  else
    diff_output="$(cd "${dir}" && GOOS="${goos}" go mod tidy -diff 2>&1)" || status=$?
  fi

  if (( status != 0 )); then
    local fix_cmd
    if [[ "${gowork_off}" == "true" ]]; then
      fix_cmd="(cd ${dir} && GOWORK=off GOOS=${goos} go mod tidy)"
    else
      fix_cmd="(cd ${dir} && GOOS=${goos} go mod tidy)"
    fi

    echo "" >&2
    echo "ERROR: ${label} (${dir}) failed 'go mod tidy -diff' — go.mod/go.sum" >&2
    echo "don't match its import graph (or the command itself errored; see the" >&2
    echo "output below). This is the #329 root cause: a module bumped without" >&2
    echo "tidying makes its entire dependency attribution unreliable, not just" >&2
    echo "the one dependency that moved." >&2
    echo "" >&2
    echo "Fix it with:" >&2
    echo "  ${fix_cmd}" >&2
    echo "" >&2
    echo "go mod tidy -diff output (first 20 lines):" >&2
    head -20 <<<"${diff_output}" >&2
    local total_lines
    total_lines="$(printf '%s\n' "${diff_output}" | wc -l | tr -d ' ')"
    if (( total_lines > 20 )); then
      echo "  ... ${dir}: $(( total_lines - 20 )) more line(s) not shown, see the full diff via the fix command above" >&2
    fi
    exit 1
  fi
}

echo "Checking Go module tidiness (go mod tidy -diff)..."
for i in "${!MODULE_DIRS[@]}"; do
  module_tidy_check "${MODULE_DIRS[$i]}" "${MODULE_GOWORK_OFF[$i]}" "${MODULE_GOOS[$i]}" "${MODULE_LABELS[$i]}"
done

for i in "${!MODULE_DIRS[@]}"; do
  collect_module "${MODULE_DIRS[$i]}" "${MODULE_GOWORK_OFF[$i]}" "${MODULE_GOOS[$i]}" "${MODULE_LABELS[$i]}"
done

# ── Apply license overrides ────────────────────────────────────────────────────

while IFS= read -r line; do
  module="$(echo "${line}" | cut -d',' -f1)"
  if [[ -n "${LICENSE_OVERRIDES[${module}]+x}" ]]; then
    url="$(echo "${line}" | cut -d',' -f2)"
    [[ "${url}" == "Unknown" ]] && url="https://pkg.go.dev/${module}"
    echo "${module},${url},${LICENSE_OVERRIDES[${module}]}"
  else
    echo "${line}"
  fi
done < "${TMP_GO_RAW}" > "${TMP_GO_FIXED}"

# Deduplicate by module path (first occurrence wins after stable sort).
# LC_ALL=C forces byte/codepoint ordering so the output is identical across
# platforms (a locale-sensitive sort places uppercase module paths differently
# on macOS vs the Linux CI runner, producing a spurious freshness diff).
GO_SORTED="$(LC_ALL=C sort -t',' -k1,1 "${TMP_GO_FIXED}" | awk -F',' '!seen[$1]++')"

# ── Check for Unknown licenses ─────────────────────────────────────────────────

UNKNOWNS="$(echo "${GO_SORTED}" | grep ',Unknown,' || true)"
if [[ -n "${UNKNOWNS}" ]]; then
  echo "" >&2
  echo "ERROR: Unknown license type(s) detected — investigate before shipping:" >&2
  while IFS= read -r u; do
    echo "  ${u}" >&2
  done <<< "${UNKNOWNS}"
  echo "" >&2
  echo "Add a manual override to LICENSE_OVERRIDES in scripts/gen-licenses.sh" >&2
  echo "(verify by reading the module source), or resolve upstream." >&2
  exit 1
fi

# ── Collect frontend deps ──────────────────────────────────────────────────────

echo "Collecting frontend deps..."
(cd "${REPO_ROOT}" && pnpm licenses list --prod --json 2>/dev/null) > "${TMP_FRONTEND_JSON}"

FRONTEND_TABLE="$(python3 - "${TMP_FRONTEND_JSON}" <<'PYEOF'
import sys, json

with open(sys.argv[1]) as f:
    data = json.load(f)

rows = []
for license_type, packages in data.items():
    for pkg in packages:
        name = pkg.get('name', '')
        versions = ', '.join(pkg.get('versions', []))
        rows.append((name, versions, license_type))

rows.sort(key=lambda r: r[0].lower())
for name, versions, lic in rows:
    print(f"| {name} | {versions} | {lic} |")
PYEOF
)"

# ── Counts ─────────────────────────────────────────────────────────────────────

GO_COUNT="$(echo "${GO_SORTED}" | grep -c ',' || true)"
FE_COUNT="$(echo "${FRONTEND_TABLE}" | grep -c '^|' || true)"

# ── Guard: refuse to wipe a good file with a broken run ───────────────────────
# collect_go() tolerates go-licenses failures (see comment above it), so a
# fully broken toolchain — e.g. go-licenses v1.6.0 under Go 1.26, which errors
# on every stdlib package with "does not have module info. Non go modules
# projects are no longer supported" — silently produces an empty GO_SORTED
# instead of a script failure. collect_module() now catches that per module and
# exits first for any module that declares external requires, so this is a
# backstop for the remaining case: every module individually passing while the
# total still comes out implausibly small.
if (( GO_COUNT < MIN_GO_DEP_ROWS )); then
  echo "" >&2
  echo "ERROR: only ${GO_COUNT} Go dependency rows collected (expected >= ${MIN_GO_DEP_ROWS})." >&2
  echo "This almost always means go-licenses ${GO_LICENSES_VERSION} is failing under the" >&2
  echo "current Go toolchain (known incompatible with Go 1.26: 'Package ... does not" >&2
  echo "have module info. Non go modules projects are no longer supported')." >&2
  echo "Refusing to overwrite ${OUTPUT} with a wiped Go section." >&2
  echo "" >&2
  echo "Workaround: manually splice the existing Go Dependencies table from the" >&2
  echo "current ${OUTPUT} into a freshly regenerated file (see git history for prior" >&2
  echo "splices), or investigate a go-licenses version compatible with this Go version." >&2
  exit 1
fi

echo "Writing ${OUTPUT} (${GO_COUNT} Go deps, ${FE_COUNT} frontend deps)..."

# ── Emit output file ───────────────────────────────────────────────────────────

{
  printf '<!-- AUTO-GENERATED — do not edit by hand. Regenerate via: task licenses -->\n'
  printf '<!-- Script: scripts/gen-licenses.sh -->\n'
  printf '\n'
  printf '# Third-Party License Attribution\n'
  printf '\n'
  printf 'This project is released under the MIT License (see [LICENSE](LICENSE)).\n'
  printf 'The following third-party packages are used as transitive dependencies.\n'
  printf '\n'
  printf '## Go Dependencies\n'
  printf '\n'
  printf '| Module | License | License URL |\n'
  printf '|--------|---------|-------------|\n'
  echo "${GO_SORTED}" | while IFS=',' read -r module url license_type; do
    printf '| %s | %s | %s |\n' "${module}" "${license_type}" "${url}"
  done
  printf '\n'
  printf '## Frontend Dependencies\n'
  printf '\n'
  printf '| Package | Version | License |\n'
  printf '|---------|---------|----------|\n'
  echo "${FRONTEND_TABLE}"
} > "${TMP_OUTPUT}"

mv "${TMP_OUTPUT}" "${OUTPUT}"

echo "Done: ${OUTPUT}"
