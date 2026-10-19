#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
DEFAULT_OUTPUT="${REPO_ROOT}/dist/THIRD_PARTY_LICENSES.md"
GO_LICENSES="$(go env GOPATH)/bin/go-licenses"

usage() {
  cat <<'EOF'
Usage: scripts/gen-licenses.sh [OUTPUT]
       scripts/gen-licenses.sh --check

Generates the third-party notices (summary tables plus the full license and
NOTICE texts) at release time. OUTPUT defaults to dist/THIRD_PARTY_LICENSES.md.
--check runs the whole collection into a temp file, enforces every gate
(Unknown license, missing license text, tidy modules, row floor) and leaves
no file behind.
EOF
}

CHECK_ONLY=false
OUTPUT=""
for arg in "$@"; do
  case "${arg}" in
    --check) CHECK_ONLY=true ;;
    -h|--help) usage; exit 0 ;;
    -*) echo "ERROR: unknown option ${arg}" >&2; usage >&2; exit 2 ;;
    *)
      if [[ -n "${OUTPUT}" ]]; then
        echo "ERROR: more than one OUTPUT given (${OUTPUT}, ${arg})" >&2
        exit 2
      fi
      OUTPUT="${arg}"
      ;;
  esac
done
if [[ "${CHECK_ONLY}" == "true" && -n "${OUTPUT}" ]]; then
  echo "ERROR: --check writes no file; drop OUTPUT (${OUTPUT})" >&2
  exit 2
fi
OUTPUT="${OUTPUT:-${DEFAULT_OUTPUT}}"

# go-licenses v1.6.0 errored on every stdlib package under Go 1.26 ("Package
# ... does not have module info. Non go modules projects are no longer
# supported"), which collect_go() tolerated via `|| true` — silently emptying
# the Go section. Bumped 2026-07-14 to go-licenses/v2@v2.0.1, which runs cleanly
# under Go 1.26 but fails under Go 1.27 — run with GOTOOLCHAIN=go<.go-version>.
# The v2 bump corrected one long-standing misclassification:
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

# go-licenses HEAD-probes GitHub for a /vN repo subdirectory and falls back to the repo root on any
# failure, so the URL flaps. Module path -> subdirectory to strip where LICENSE sits at the repo root only.
declare -A LICENSE_URL_SUBDIR_STRIP=([github.com/wailsapp/wails/v2]="v2/")

# ── Temp files ─────────────────────────────────────────────────────────────────

TMP_GO_RAW="$(mktemp)"
TMP_GO_FIXED="$(mktemp)"
TMP_MODULE_RAW="$(mktemp)"
TMP_FRONTEND_JSON="$(mktemp)"
TMP_FRONTEND_TABLE="$(mktemp)"
TMP_FRONTEND_TEXTS="$(mktemp)"
TMP_GO_TEXTS="$(mktemp)"
TMP_SAVE_LOG="$(mktemp)"
TMP_OUTPUT="$(mktemp)"
TMP_SAVE_ROOT="$(mktemp -d)"
trap 'rm -rf "${TMP_GO_RAW}" "${TMP_GO_FIXED}" "${TMP_MODULE_RAW}" "${TMP_FRONTEND_JSON}" "${TMP_FRONTEND_TABLE}" "${TMP_FRONTEND_TEXTS}" "${TMP_GO_TEXTS}" "${TMP_SAVE_LOG}" "${TMP_OUTPUT}" "${TMP_SAVE_ROOT}"' EXIT

# ── Collect Go deps ────────────────────────────────────────────────────────────

# The Go build list is GOOS-dependent (build constraints pull in different
# packages per platform, e.g. mattn/go-isatty on darwin only). Pin GOOS/GOARCH
# to the linux/amd64 CI target so the attribution is canonical no matter which
# platform regenerates it.
export GOOS=linux
export GOARCH=amd64

# go-licenses' exit status for the module collect_go() last ran. Its stdout is
# the CSV stream the caller redirects into TMP_MODULE_RAW, so the status cannot
# be returned normally — collect_module() reads it from here.
GO_LICENSES_STATUS=0

# The `|| true` below keeps a failing module from aborting the run before
# collect_module() can produce a diagnostic; it is not tolerance. Stderr is left
# visible so genuine failures surface in CI logs instead of being silently
# swallowed. `go build -o /dev/null` still downloads and compiles the module's
# dependencies but never writes the binaries of the plugin main packages (which
# are tracked in git) into the working tree.
collect_go() {
  local dir="$1"
  local gowork_off="${2:-false}"
  # Optional GOOS override for platform-gated modules (e.g. desktop/ is
  # //go:build darwin). go-licenses cross-lists via packages.Load, so a fixed
  # non-host GOOS stays deterministic across CI/dev. Defaults to the global pin.
  local goos="${3:-${GOOS}}"
  local st=0

  if [[ "${gowork_off}" == "true" ]]; then
    (cd "${dir}" && GOWORK=off GOOS="${goos}" go build -o /dev/null ./... 2>/dev/null || true)
    (cd "${dir}" && GOWORK=off GOOS="${goos}" "${GO_LICENSES}" report ./... \
      --ignore github.com/lx-wnk/kontor) || st=$?
  else
    (cd "${dir}" && GOOS="${goos}" go build -o /dev/null ./... 2>/dev/null || true)
    (cd "${dir}" && GOOS="${goos}" "${GO_LICENSES}" report ./... \
      --ignore github.com/lx-wnk/kontor) || st=$?
  fi
  GO_LICENSES_STATUS=${st}
}

# A module is expected to contribute at least one row when the package graph
# go-licenses walks — `./...` without tests — reaches a module outside our own
# module family (the same "github.com/lx-wnk/kontor" prefix passed to
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

  # No trailing '/': the plugin modules are named kontor-plugin-*, and
  # go-licenses' --ignore is a bare HasPrefix too. Adding one would reclassify
  # every plugin as third-party and hard-fail the five that have no deps.
  grep -qv '^github\.com/lx-wnk/kontor' <<<"${dep_modules}"
}

# Prints the hand-runnable go-licenses invocation for one module, so both
# guards below point at the same reproduction command.
print_module_repro_cmd() {
  local dir="$1" gowork_off="$2" goos="$3"
  if [[ "${gowork_off}" == "true" ]]; then
    echo "  (cd ${dir} && GOWORK=off GOOS=${goos} ${GO_LICENSES} report ./... --ignore github.com/lx-wnk/kontor)" >&2
  else
    echo "  (cd ${dir} && GOOS=${goos} ${GO_LICENSES} report ./... --ignore github.com/lx-wnk/kontor)" >&2
  fi
}

# Wraps collect_go() with the per-module row-count guard: a module whose
# package graph reaches external modules (per module_expects_go_rows above) but whose
# collection produced zero rows means the collection silently failed for that
# module alone — the exact way #329 dropped the Go dependency count from 72 to
# 63 without the script noticing. MIN_GO_DEP_ROWS below only catches a total
# collapse; this catches one module vanishing.
#
# Every row is tagged with the module's registry index as a 4th CSV field; it
# later locates the `go-licenses save` output holding that row's license text.
collect_module() {
  local dir="$1" gowork_off="$2" goos="$3" label="$4" idx="$5"
  echo "Collecting Go deps: ${label}..."

  local row_count
  collect_go "${dir}" "${gowork_off}" "${goos}" > "${TMP_MODULE_RAW}"
  row_count="$(wc -l < "${TMP_MODULE_RAW}")"

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

  sed "s/\$/,${idx}/" "${TMP_MODULE_RAW}" >> "${TMP_GO_RAW}"
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
  collect_module "${MODULE_DIRS[$i]}" "${MODULE_GOWORK_OFF[$i]}" "${MODULE_GOOS[$i]}" "${MODULE_LABELS[$i]}" "${i}"
done

# ── Apply license overrides ────────────────────────────────────────────────────

while IFS= read -r line; do
  module="$(echo "${line}" | cut -d',' -f1)"
  strip="${LICENSE_URL_SUBDIR_STRIP[${module}]-}"
  [[ -n "${strip}" ]] && line="${line/\/${strip}LICENSE,//LICENSE,}"
  if [[ -n "${LICENSE_OVERRIDES[${module}]+x}" ]]; then
    url="$(echo "${line}" | cut -d',' -f2)"
    [[ "${url}" == "Unknown" ]] && url="https://pkg.go.dev/${module}"
    echo "${module},${url},${LICENSE_OVERRIDES[${module}]},$(echo "${line}" | cut -d',' -f4)"
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

# Writes the summary table rows and the full license/NOTICE texts of every
# production package. A package whose license is Unknown, or whose directory
# holds no license file, is a hard error — never a silently thinner notice.
python3 -I - "${TMP_FRONTEND_JSON}" "${TMP_FRONTEND_TABLE}" "${TMP_FRONTEND_TEXTS}" <<'PYEOF'
import json
import os
import re
import sys

LICENSE_FILE_RE = re.compile(r'^(licen[sc]e|copying|notice)', re.IGNORECASE)
FENCE = '~' * 6

# Packages that publish no license file. Text copied from the upstream repository
# and valid only while the package still declares the same license; otherwise the
# package is reported as a problem again.
UPSTREAM_LICENSE_FALLBACKS = {
    'change-case': ('MIT', 'https://github.com/blakeembrey/change-case/blob/master/LICENSE', """The MIT License (MIT)

Copyright (c) 2014 Blake Embrey (hello@blakeembrey.com)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE."""),
}

json_path, table_path, texts_path = sys.argv[1:4]
with open(json_path) as f:
    data = json.load(f)

rows = []
entries = {}
problems = []
for license_type, packages in data.items():
    for pkg in packages:
        name = pkg.get('name', '')
        versions = pkg.get('versions', [])
        paths = pkg.get('paths', [])
        rows.append((name, ', '.join(versions), license_type))
        if 'unknown' in license_type.lower():
            problems.append(f'{name}: license type is {license_type}')
        if len(versions) != len(paths):
            problems.append(f'{name}: {len(versions)} version(s) but {len(paths)} path(s)')
            continue
        for version, path in zip(versions, paths):
            entries[(name, version)] = (license_type, path)


def license_files(directory):
    try:
        names = os.listdir(directory)
    except OSError as err:
        problems.append(f'{directory}: {err}')
        return []
    found = []
    for fname in sorted(names):
        full = os.path.join(directory, fname)
        if LICENSE_FILE_RE.match(fname) and os.path.isfile(full) and os.path.getsize(full) > 0:
            found.append((fname, full))
    return found


rows.sort(key=lambda r: r[0].lower())
with open(table_path, 'w') as out:
    for name, versions, lic in rows:
        out.write(f'| {name} | {versions} | {lic} |\n')

with open(texts_path, 'w') as out:
    for name, version in sorted(entries, key=lambda k: (k[0].lower(), k[1])):
        lic, path = entries[(name, version)]
        files = license_files(path)
        fallback = UPSTREAM_LICENSE_FALLBACKS.get(name)
        if not files and fallback and fallback[0] == lic:
            out.write(f'### {name}@{version} ({lic})\n\nThe package ships no license file; text from {fallback[1]}\n\n')
            out.write(f'#### LICENSE\n\n{FENCE}text\n{fallback[2]}\n{FENCE}\n\n')
            continue
        if not files:
            problems.append(f'{name}@{version}: no LICENSE/COPYING/NOTICE file in {path}')
            continue
        out.write(f'### {name}@{version} ({lic})\n\n')
        for fname, full in files:
            with open(full, encoding='utf-8', errors='replace') as src:
                text = src.read().rstrip('\n')
            out.write(f'#### {fname}\n\n{FENCE}text\n{text}\n{FENCE}\n\n')

if problems:
    print('', file=sys.stderr)
    print('ERROR: frontend license problem(s) — resolve before shipping:', file=sys.stderr)
    for problem in problems:
        print(f'  {problem}', file=sys.stderr)
    sys.exit(1)
PYEOF

# ── Counts ─────────────────────────────────────────────────────────────────────

GO_COUNT="$(echo "${GO_SORTED}" | grep -c ',' || true)"
FE_COUNT="$(grep -c '^|' "${TMP_FRONTEND_TABLE}" || true)"

# ── Guard: refuse to ship a broken run ────────────────────────────────────────
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
  echo "current Go toolchain (known incompatible with Go 1.27, and with Go 1.26 for v1.6.0:" >&2
  echo "'Package ... does not have module info. Non go modules projects are no longer" >&2
  echo "supported'). Run with GOTOOLCHAIN=go<version in .go-version>." >&2
  echo "Refusing to emit a notices file with a wiped Go section." >&2
  exit 1
fi

# ── Collect Go license texts ───────────────────────────────────────────────────
# `go-licenses save` writes the license and NOTICE files of every dependency
# package to <save_path>/<package path>/ — and, for copyleft licenses, the
# module's source as well, which a notices file does not want. Only
# LICENSE/COPYING/NOTICE-named files are read back from those directories.

# One `go-licenses save` per module that still owns at least one row after the
# dedupe above.
save_module_texts() {
  local i="$1" st=0
  local -a run_env=("GOOS=${MODULE_GOOS[$i]}")
  if [[ "${MODULE_GOWORK_OFF[$i]}" == "true" ]]; then
    run_env+=("GOWORK=off")
  fi

  (cd "${MODULE_DIRS[$i]}" && env "${run_env[@]}" "${GO_LICENSES}" save ./... \
    --ignore github.com/lx-wnk/kontor --save_path "${TMP_SAVE_ROOT}/${i}") 2>"${TMP_SAVE_LOG}" || st=$?

  if (( st != 0 )); then
    echo "" >&2
    echo "ERROR: go-licenses save exited ${st} for ${MODULE_LABELS[$i]} (${MODULE_DIRS[$i]}):" >&2
    cat "${TMP_SAVE_LOG}" >&2
    exit 1
  fi
}

# Non-empty LICENSE/LICENCE/COPYING/NOTICE files directly inside a directory.
list_license_files() {
  { find "$1" -maxdepth 1 -type f -size +0c \
    \( -iname 'LICEN[SC]E*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) 2>/dev/null || true; } | LC_ALL=C sort
}

# Writes one section per distinct license URL (= module@version; go-licenses
# derives the URL from the module version) into TMP_GO_TEXTS. A package whose
# saved directory holds no license text is a hard error.
build_go_texts() {
  local pkg url license_type idx pkgs file found
  local -a missing=()

  : > "${TMP_GO_TEXTS}"
  while IFS=$'\t' read -r pkg url license_type idx pkgs; do
    found=false
    {
      printf '### %s (%s)\n\n' "${pkg}" "${license_type}"
      printf 'Source: %s\n\n' "${url}"
      if [[ "${pkgs}" != "${pkg}" ]]; then
        printf 'Applies to: %s\n\n' "${pkgs}"
      fi
    } >> "${TMP_GO_TEXTS}"

    while IFS= read -r file; do
      [[ -n "${file}" ]] || continue
      found=true
      {
        printf '#### %s\n\n~~~~~~text\n' "$(basename "${file}")"
        printf '%s\n' "$(cat "${file}")"
        printf '~~~~~~\n\n'
      } >> "${TMP_GO_TEXTS}"
    done < <(list_license_files "${TMP_SAVE_ROOT}/${idx}/${pkg}")

    if [[ "${found}" == "false" ]]; then
      missing+=("${pkg} (${url})")
    fi
  done < <(printf '%s\n' "${GO_SORTED}" | awk -F',' '
    !($2 in order) { n++; key[n] = $2; first[$2] = $1; lic[$2] = $3; mod[$2] = $4; pk[$2] = $1; order[$2] = n; next }
    { pk[$2] = pk[$2] ", " $1 }
    END { for (i = 1; i <= n; i++) { k = key[i]; printf "%s\t%s\t%s\t%s\t%s\n", first[k], k, lic[k], mod[k], pk[k] } }')

  if (( ${#missing[@]} > 0 )); then
    echo "" >&2
    echo "ERROR: no license text found for ${#missing[@]} Go package(s) — a notice without" >&2
    echo "the license text does not satisfy MIT/BSD/ISC/Apache attribution:" >&2
    printf '  %s\n' "${missing[@]}" >&2
    exit 1
  fi
}

echo "Collecting Go license texts..."
while IFS= read -r idx; do
  save_module_texts "${idx}"
done < <(printf '%s\n' "${GO_SORTED}" | cut -d',' -f4 | sort -un)
build_go_texts

# ── Emit output file ───────────────────────────────────────────────────────────

if [[ "${CHECK_ONLY}" == "true" ]]; then
  echo "Checking only (${GO_COUNT} Go deps, ${FE_COUNT} frontend deps) — no file written."
else
  echo "Writing ${OUTPUT} (${GO_COUNT} Go deps, ${FE_COUNT} frontend deps)..."
fi

{
  printf '<!-- AUTO-GENERATED at release time — do not edit by hand. -->\n'
  printf '<!-- Script: scripts/gen-licenses.sh -->\n'
  printf '\n'
  printf '# Third-Party License Attribution\n'
  printf '\n'
  printf 'This project is released under the MIT License (see the LICENSE file in the repository root).\n'
  printf 'The following third-party packages are used as transitive dependencies.\n'
  printf 'Summary tables come first; the full license and NOTICE texts follow below them.\n'
  printf '\n'
  printf '## Go Dependencies\n'
  printf '\n'
  printf '| Module | License | License URL |\n'
  printf '|--------|---------|-------------|\n'
  echo "${GO_SORTED}" | while IFS=',' read -r module url license_type _; do
    printf '| %s | %s | %s |\n' "${module}" "${license_type}" "${url}"
  done
  printf '\n'
  printf '## Frontend Dependencies\n'
  printf '\n'
  printf '| Package | Version | License |\n'
  printf '|---------|---------|----------|\n'
  cat "${TMP_FRONTEND_TABLE}"
  printf '\n'
  printf '## Go License Texts\n'
  printf '\n'
  cat "${TMP_GO_TEXTS}"
  printf '## Frontend License Texts\n'
  printf '\n'
  cat "${TMP_FRONTEND_TEXTS}"
} > "${TMP_OUTPUT}"

if [[ "${CHECK_ONLY}" == "true" ]]; then
  echo "Check passed."
  exit 0
fi

mkdir -p "$(dirname "${OUTPUT}")"
chmod 0644 "${TMP_OUTPUT}"
mv "${TMP_OUTPUT}" "${OUTPUT}"

echo "Done: ${OUTPUT}"
