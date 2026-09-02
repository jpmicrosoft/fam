#!/bin/sh
# install.sh — verified installer for fam (POSIX shell)
#
# Downloads the prebuilt fam release binary. It does not compile source code,
# and Go is not required to install or run the downloaded CLI.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/REPO/main/scripts/install.sh | sh
#   ./scripts/install.sh --version v0.16.1 --install-dir "$HOME/.local/bin"
#
# Environment:
#   FAM_INSTALL_TOKEN / GITHUB_TOKEN / GH_TOKEN
#                    — used for private repository access (never printed)
#   INSTALL_DIR             — alternative to --install-dir
#   REPO                    — alternative to --repo
#
set -eu

# Defaults
DEFAULT_REPO="jpmicrosoft/fam"
REPO="${REPO:-$DEFAULT_REPO}"
VERSION=""
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
MODIFY_PROFILE=false

usage() {
  cat <<EOF
Usage: install.sh [OPTIONS]

Downloads and installs the prebuilt fam release binary.
Go is not required; this script does not compile source code.

Options:
  --version VERSION     Install a specific published tag (e.g. v0.16.1).
                        Omit for latest release.
  --install-dir DIR     Destination directory (default: \$HOME/.local/bin)
  --repo OWNER/REPO    GitHub repository (default: $DEFAULT_REPO)
  --modify-profile      Append install dir to PATH in shell profile
  -h, --help            Show this help
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version)     VERSION="$2"; shift 2;;
    --install-dir) INSTALL_DIR="$2"; shift 2;;
    --repo)        REPO="$2"; shift 2;;
    --modify-profile) MODIFY_PROFILE=true; shift;;
    -h|--help)     usage; exit 0;;
    *)             echo "ERROR: Unknown option: $1" >&2; usage >&2; exit 1;;
  esac
done

case "$REPO" in
  */*) ;;
  *) echo "ERROR: --repo must use OWNER/REPO format" >&2; exit 1;;
esac
if printf '%s' "$REPO" | grep -Eq '[^A-Za-z0-9_.\-/]'; then
  echo "ERROR: --repo must use OWNER/REPO format" >&2
  exit 1
fi

# --- Detect platform and architecture ---
detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux";;
    Darwin*) echo "darwin";;
    *)       echo "UNSUPPORTED";;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "amd64";;
    aarch64|arm64) echo "arm64";;
    *)             echo "UNSUPPORTED";;
  esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

if [ "$OS" = "UNSUPPORTED" ]; then
  echo "ERROR: Unsupported operating system: $(uname -s)" >&2
  exit 1
fi
if [ "$ARCH" = "UNSUPPORTED" ]; then
  echo "ERROR: Unsupported architecture: $(uname -m)" >&2
  exit 1
fi

# --- Resolve authentication header ---
AUTH_HEADER=""
TOKEN="${FAM_INSTALL_TOKEN:-${GITHUB_TOKEN:-${GH_TOKEN:-}}}"
if [ -z "$TOKEN" ] && command -v gh >/dev/null 2>&1; then
  TOKEN="$(gh auth token 2>/dev/null || true)"
fi
if [ -n "$TOKEN" ]; then
  AUTH_HEADER="Authorization: token ${TOKEN}"
fi

http_get() {
  url="$1"
  dest="$2"
  if [ -n "$AUTH_HEADER" ]; then
    curl -fsSL -H "$AUTH_HEADER" -H "Accept: application/octet-stream" -o "$dest" "$url"
  else
    curl -fsSL -H "Accept: application/octet-stream" -o "$dest" "$url"
  fi
}

api_get() {
  url="$1"
  if [ -n "$AUTH_HEADER" ]; then
    curl -fsSL -H "$AUTH_HEADER" -H "Accept: application/vnd.github+json" "$url"
  else
    curl -fsSL -H "Accept: application/vnd.github+json" "$url"
  fi
}

# --- Resolve version and release assets ---
RELEASE_JSON=""
if [ -z "$VERSION" ] || [ "$VERSION" = "latest" ]; then
  echo "Resolving latest release..."
  RELEASE_JSON="$(api_get "https://api.github.com/repos/${REPO}/releases/latest")"
  VERSION="$(printf '%s' "$RELEASE_JSON" | \
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
  if [ -z "$VERSION" ]; then
    echo "ERROR: Could not determine latest release for ${REPO}" >&2
    exit 1
  fi
fi
if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-+][A-Za-z0-9_.-]+)?$'; then
  echo "ERROR: --version must be 'latest' or a v-prefixed semantic version tag" >&2
  exit 1
fi
if [ -z "$RELEASE_JSON" ]; then
  RELEASE_JSON="$(api_get "https://api.github.com/repos/${REPO}/releases/tags/${VERSION}")"
fi

VERSION_NUM="${VERSION#v}"
PREFERRED_ARCHIVE="fam_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
LEGACY_ARCHIVE="foundry-agent-manager_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
if printf '%s' "$RELEASE_JSON" | grep -Fq "\"${PREFERRED_ARCHIVE}\""; then
  ARCHIVE="$PREFERRED_ARCHIVE"
elif printf '%s' "$RELEASE_JSON" | grep -Fq "\"${LEGACY_ARCHIVE}\""; then
  ARCHIVE="$LEGACY_ARCHIVE"
  echo "Using the historical archive name for pre-transition release ${VERSION}; only fam will be installed."
else
  echo "ERROR: Release ${VERSION} does not contain ${PREFERRED_ARCHIVE} or its supported historical equivalent." >&2
  exit 1
fi
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

echo "Installing prebuilt fam ${VERSION} (${OS}/${ARCH})..."
echo "Go is not required; this installer downloads a compiled release binary."

# --- Download archive and checksums ---
TMPDIR_INST="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_INST"' EXIT

echo "Downloading ${ARCHIVE}..."
http_get "${BASE_URL}/${ARCHIVE}" "${TMPDIR_INST}/${ARCHIVE}"

echo "Downloading SHA256SUMS..."
http_get "${BASE_URL}/SHA256SUMS" "${TMPDIR_INST}/SHA256SUMS"

# --- Verify checksum ---
EXPECTED="$(grep "  ${ARCHIVE}\$" "${TMPDIR_INST}/SHA256SUMS" | awk '{print $1}' || true)"
if [ -z "$EXPECTED" ]; then
  # Also try single-space separator
  EXPECTED="$(grep " ${ARCHIVE}\$" "${TMPDIR_INST}/SHA256SUMS" | awk '{print $1}' || true)"
fi
if [ -z "$EXPECTED" ]; then
  echo "ERROR: Checksum for ${ARCHIVE} not found in SHA256SUMS" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL="$(sha256sum "${TMPDIR_INST}/${ARCHIVE}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL="$(shasum -a 256 "${TMPDIR_INST}/${ARCHIVE}" | awk '{print $1}')"
else
  echo "ERROR: No sha256sum or shasum command found" >&2
  exit 1
fi

if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "ERROR: Checksum mismatch for ${ARCHIVE}" >&2
  echo "  Expected: ${EXPECTED}" >&2
  echo "  Actual:   ${ACTUAL}" >&2
  exit 1
fi
echo "Checksum verified."

# --- Extract and install ---
tar -xzf "${TMPDIR_INST}/${ARCHIVE}" -C "${TMPDIR_INST}"

mkdir -p "$INSTALL_DIR"
mv "${TMPDIR_INST}/fam" "${INSTALL_DIR}/fam"
chmod +x "${INSTALL_DIR}/fam"

# Remove the executable name installed by releases before the fam-only transition.
rm -f "${INSTALL_DIR}/foundry-agent-manager"

echo "Installed fam to ${INSTALL_DIR}"

# --- Optionally modify profile ---
if [ "$MODIFY_PROFILE" = true ]; then
  PROFILE_LINE="export PATH=\"${INSTALL_DIR}:\$PATH\""
  for profile_file in "$HOME/.profile" "$HOME/.bashrc" "$HOME/.zshrc"; do
    if [ -f "$profile_file" ]; then
      if ! grep -qF "$INSTALL_DIR" "$profile_file" 2>/dev/null; then
        echo "$PROFILE_LINE" >> "$profile_file"
        echo "Added ${INSTALL_DIR} to PATH in ${profile_file}"
      fi
      break
    fi
  done
fi

echo "Done. Run 'fam --version' to verify."
