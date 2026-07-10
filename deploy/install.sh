#!/usr/bin/env bash
# 5gpn bootstrap installer.
#
# Fetches the 5gpn-installer binary from a GitHub Release, verifies its
# sha256, and hands off to `5gpn-installer install`. All the real logic
# lives in cmd/5gpn-installer/ (Go); this script exists so operators can
# `curl … | bash` on a fresh host and stay tiny (~90 lines).
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

artefact="5gpn-installer-${os}-${arch}"
base="https://github.com/${REPO}/releases/download/${tag}"

echo "[5gpn] downloading ${artefact} @ ${tag}"
curl -fsSL "${base}/${artefact}" -o "${tmpdir}/${artefact}"
curl -fsSL "${base}/SHA256SUMS" -o "${tmpdir}/SHA256SUMS"

if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$tmpdir" && grep " ${artefact}$" SHA256SUMS | sha256sum -c - )
else
  # macOS shasum path
  expected=$(grep " ${artefact}$" "${tmpdir}/SHA256SUMS" | awk '{print $1}')
  got=$(shasum -a 256 "${tmpdir}/${artefact}" | awk '{print $1}')
  if [ "$expected" != "$got" ]; then
    echo "sha256 mismatch: want $expected got $got" >&2
    exit 1
  fi
fi

chmod +x "${tmpdir}/${artefact}"

install_flags=()
if [ -n "$PREFIX" ]; then
  install_flags+=(--root "$PREFIX")
fi

if [ "$(id -u)" -ne 0 ] && [ -z "$PREFIX" ]; then
  echo "[5gpn] not root — re-invoking under sudo"
  exec sudo "${tmpdir}/${artefact}" install "${install_flags[@]}" "$@"
fi

exec "${tmpdir}/${artefact}" install "${install_flags[@]}" "$@"
