#!/usr/bin/env sh
# install.sh — download the latest Kontor release binary for this platform.
#
#   curl -fsSL https://raw.githubusercontent.com/lx-wnk/kontor/main/install.sh | sh
#
# Environment overrides:
#   KONTOR_VERSION   pin a specific tag (default: latest release)
#   KONTOR_BIN_DIR   install directory (default: ~/.local/bin, or /usr/local/bin if writable)
#
# Kontor reads your local Claude Code sessions and binds to 127.0.0.1 only.
# See https://github.com/lx-wnk/kontor/blob/main/docs/guides/security.md
set -eu

REPO="lx-wnk/kontor"
BINARY="kontor"

info() { printf '\033[36m==>\033[0m %s\n' "$1"; }
err()  { printf '\033[31merror:\033[0m %s\n' "$1" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || err "required tool '$1' not found"; }
need uname
need tar

# Prefer curl, fall back to wget.
if command -v curl >/dev/null 2>&1; then
  dl() { curl -fsSL "$1"; }
  dlo() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  dl() { wget -qO- "$1"; }
  dlo() { wget -qO "$2" "$1"; }
else
  err "need either curl or wget"
fi

# --- detect platform -------------------------------------------------------
os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) err "unsupported OS '$os' — only macOS and Linux have prebuilt binaries (build from source instead)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) err "unsupported architecture '$arch'" ;;
esac

# --- resolve version -------------------------------------------------------
version="${KONTOR_VERSION:-}"
if [ -z "$version" ]; then
  info "Resolving latest release…"
  version=$(dl "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name":' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
  [ -n "$version" ] || err "could not determine latest version (GitHub API rate-limited? set KONTOR_VERSION)"
fi
ver_no_v="${version#v}"

# Archive name must match .goreleaser.yml name_template:
#   {{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}
asset="${BINARY}_${ver_no_v}_${os}_${arch}.tar.gz"
base="https://github.com/${REPO}/releases/download/${version}"

# --- download + verify -----------------------------------------------------
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

info "Downloading ${asset} (${version})…"
dlo "${base}/${asset}" "${tmp}/${asset}" || err "download failed: ${base}/${asset}"

if dlo "${base}/checksums.txt" "${tmp}/checksums.txt" 2>/dev/null; then
  info "Verifying checksum…"
  if command -v sha256sum >/dev/null 2>&1; then sumcmd="sha256sum";
  elif command -v shasum >/dev/null 2>&1; then sumcmd="shasum -a 256";
  else sumcmd=""; fi
  if [ -n "$sumcmd" ]; then
    want=$(grep "  ${asset}\$" "${tmp}/checksums.txt" | awk '{print $1}')
    got=$(cd "$tmp" && $sumcmd "$asset" | awk '{print $1}')
    [ -n "$want" ] && [ "$want" = "$got" ] || err "checksum mismatch for ${asset}"
  else
    info "no sha256 tool found — skipping checksum verification"
  fi
else
  info "checksums.txt not found — skipping verification"
fi

info "Extracting…"
tar -xzf "${tmp}/${asset}" -C "$tmp"
[ -f "${tmp}/${BINARY}" ] || err "archive did not contain '${BINARY}'"
chmod +x "${tmp}/${BINARY}"

# --- choose install dir ----------------------------------------------------
bindir="${KONTOR_BIN_DIR:-}"
if [ -z "$bindir" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then bindir="/usr/local/bin"; else bindir="${HOME}/.local/bin"; fi
fi
mkdir -p "$bindir"

if mv "${tmp}/${BINARY}" "${bindir}/${BINARY}" 2>/dev/null; then
  :
elif command -v sudo >/dev/null 2>&1; then
  info "Installing to ${bindir} (requires sudo)…"
  sudo mv "${tmp}/${BINARY}" "${bindir}/${BINARY}"
else
  err "cannot write to ${bindir} and sudo is unavailable — set KONTOR_BIN_DIR to a writable path"
fi

info "Installed ${BINARY} ${version} → ${bindir}/${BINARY}"
case ":${PATH}:" in
  *":${bindir}:"*) ;;
  *) printf '\033[33mnote:\033[0m %s is not on your PATH — add: export PATH="%s:$PATH"\n' "$bindir" "$bindir" ;;
esac
echo
echo "Run:  ${BINARY} serve   then open http://localhost:13120"
