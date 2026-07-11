#!/usr/bin/env bash
# 5gpn bootstrap installer.
#
# Fetches the 5gpn-installer AND the daemon binary from a GitHub Release,
# verifies both sha256s, and hands off to `5gpn-installer install
# --source-binary <daemon>`. All the real logic lives in
# cmd/5gpn-installer/ (Go); this script exists so operators can
# `curl … | bash` on a fresh host and stay tiny.
#
# Env overrides:
#   GPN_REPO      Github repo (default: Xiuyixx/5GPN-GO)
#   GPN_VERSION   Release tag (default: latest)
#   GPN_PREFIX    Install root; forwarded via --root when set
#   GPN_TMPDIR    Where to stage downloads (default: mktemp -d)

set -euo pipefail

REPO="${GPN_REPO:-Xiuyixx/5GPN-GO}"
VERSION="${GPN_VERSION:-latest}"
PREFIX="${GPN_PREFIX:-}"

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing dependency: $1" >&2; exit 1; }; }
need curl
need uname
need sha256sum || need shasum

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in
  linux|darwin) ;;
  *) echo "unsupported os: $os" >&2; exit 1 ;;
esac

tag="$VERSION"
if [ "$tag" = "latest" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -o '"tag_name":[[:space:]]*"[^"]*"' \
    | head -n1 | cut -d'"' -f4)
  if [ -z "$tag" ]; then
    echo "could not resolve latest tag for ${REPO}" >&2
    exit 1
  fi
fi

tmpdir="${GPN_TMPDIR:-$(mktemp -d)}"
trap 'rm -rf "$tmpdir"' EXIT

installer_asset="5gpn-installer-${os}-${arch}"
daemon_asset="5gpn-${os}-${arch}"
base="https://github.com/${REPO}/releases/download/${tag}"

echo "[5gpn] downloading ${installer_asset} @ ${tag}"
curl -fsSL "${base}/${installer_asset}" -o "${tmpdir}/${installer_asset}"
echo "[5gpn] downloading ${daemon_asset} @ ${tag}"
curl -fsSL "${base}/${daemon_asset}"    -o "${tmpdir}/${daemon_asset}"
echo "[5gpn] downloading SHA256SUMS @ ${tag}"
curl -fsSL "${base}/SHA256SUMS"          -o "${tmpdir}/SHA256SUMS"

verify_sha() {
  local asset="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    ( cd "$tmpdir" && grep " ${asset}$" SHA256SUMS | sha256sum -c - )
  else
    local expected got
    expected=$(grep " ${asset}$" "${tmpdir}/SHA256SUMS" | awk '{print $1}')
    got=$(shasum -a 256 "${tmpdir}/${asset}" | awk '{print $1}')
    if [ "$expected" != "$got" ]; then
      echo "sha256 mismatch for ${asset}: want $expected got $got" >&2
      exit 1
    fi
  fi
}
verify_sha "${installer_asset}"
verify_sha "${daemon_asset}"

chmod +x "${tmpdir}/${installer_asset}" "${tmpdir}/${daemon_asset}"

install_flags=(--source-binary "${tmpdir}/${daemon_asset}")
if [ -n "$PREFIX" ]; then
  install_flags+=(--root "$PREFIX")
fi

if [ "$(id -u)" -ne 0 ] && [ -z "$PREFIX" ]; then
  echo "[5gpn] not root — re-invoking under sudo"
  exec sudo "${tmpdir}/${installer_asset}" install "${install_flags[@]}" "$@"
fi

exec "${tmpdir}/${installer_asset}" install "${install_flags[@]}" "$@"
