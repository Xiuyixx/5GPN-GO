#!/usr/bin/env bash
# 5gpn one-shot installer / 一键安装脚本
#
# 目标：完全没接触过 5gpn 的运维一次就能装好，任何失败都告诉他下一步做什么。
# Goal: an operator who has never seen 5gpn gets a working install in one go,
#       and any failure explains what to do next.
#
# Usage
#   curl -fsSL https://raw.githubusercontent.com/Xiuyixx/5GPN-GO/main/deploy/install.sh | bash
#   curl -fsSL ... | GPN_VERSION=v0.2.7 bash
#   curl -fsSL ... | GPN_REPO=my/fork bash
#
# Env overrides
#   GPN_REPO      Github repo               (default: Xiuyixx/5GPN-GO)
#   GPN_VERSION   Release tag               (default: latest)
#   GPN_PREFIX    Unsupported here; use installer --root for controlled tests
#   GPN_TMPDIR    Parent for private staging (default: TMPDIR or /tmp)
#   GPN_NO_COLOR  Set to 1 to disable ANSI  (default: color on TTY)
#   GPN_INSECURE  Set to 1 to skip TLS verify on GitHub fetches (dev only)

set -Eeuo pipefail

# --------------------------------------------------------------------
# Presentation — colors + step counter
# --------------------------------------------------------------------

if [ -t 1 ] && [ "${GPN_NO_COLOR:-0}" != "1" ]; then
    C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'
    C_BLU=$'\033[34m'; C_CYN=$'\033[36m'; C_DIM=$'\033[2m'
    C_BOLD=$'\033[1m'; C_RST=$'\033[0m'
else
    C_RED=""; C_GRN=""; C_YEL=""; C_BLU=""; C_CYN=""; C_DIM=""; C_BOLD=""; C_RST=""
fi

STEP=0
step()  { STEP=$((STEP + 1)); printf '\n%s[%d/9] %s%s\n' "$C_BOLD$C_CYN" "$STEP" "$1" "$C_RST"; }
info()  { printf '  %s→%s %s\n' "$C_BLU" "$C_RST" "$1"; }
good()  { printf '  %s✓%s %s\n' "$C_GRN" "$C_RST" "$1"; }
warn()  { printf '  %s!%s %s\n' "$C_YEL" "$C_RST" "$1" >&2; }
fail()  { printf '\n%s✗ %s%s\n' "$C_RED$C_BOLD" "$1" "$C_RST" >&2; }
hint()  { printf '  %s%s%s\n' "$C_DIM" "$1" "$C_RST" >&2; }
die()   { fail "$1"; shift; for h in "$@"; do hint "$h"; done; exit 1; }

# Catch every unexpected exit and point at the failing line.
on_err() {
    local exit_code=$?
    local line=$1
    fail "unexpected error at line ${line} (exit ${exit_code})"
    hint "This is a bug in the installer. Copy the last 30 lines of output and open"
    hint "an issue: https://github.com/Xiuyixx/5GPN-GO/issues"
    exit "$exit_code"
}
trap 'on_err $LINENO' ERR

# --------------------------------------------------------------------
# Config
# --------------------------------------------------------------------

REPO="${GPN_REPO:-Xiuyixx/5GPN-GO}"
VERSION="${GPN_VERSION:-latest}"
PREFIX="${GPN_PREFIX:-}"
CURL_OPTS=(--fail --silent --show-error --location --retry 3 --retry-delay 2 --connect-timeout 10 --max-time 300)
if [ "${GPN_INSECURE:-0}" = "1" ]; then
    CURL_OPTS+=(--insecure)
    warn "GPN_INSECURE=1 — skipping TLS verify on GitHub fetches. Do not use in production."
fi

if [ -n "$PREFIX" ]; then
	die "GPN_PREFIX is not supported by the one-shot production installer." \
		"The Go installer's --root mode is for controlled dry-runs/tests; a real" \
		"gateway still needs host users, ownership, systemd, ports, and network access."
fi

# --------------------------------------------------------------------
# Banner
# --------------------------------------------------------------------

cat <<EOF
${C_BOLD}${C_CYN}
   ┌─────────────────────────────────────────────┐
   │   5GPN Personal Gateway — Installer         │
   │   5GPN 个人网关安装器                       │
   └─────────────────────────────────────────────┘${C_RST}
${C_DIM}   repo:    ${REPO}
   version: ${VERSION}
   docs:    https://github.com/${REPO}${C_RST}

EOF

# --------------------------------------------------------------------
# Step 1 — preflight: dependencies
# --------------------------------------------------------------------

step "Preflight: checking required commands / 环境依赖"

missing=()
for tool in curl uname tar mktemp; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        missing+=("$tool")
    fi
done
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    missing+=("sha256sum-or-shasum")
fi
if [ ${#missing[@]} -gt 0 ]; then
    die "missing required tool(s): ${missing[*]}" \
        "Debian/Ubuntu:  apt-get update && apt-get install -y curl coreutils tar" \
        "Rocky/Alma:     dnf install -y curl coreutils tar" \
        "Alpine:         apk add curl coreutils tar"
fi
good "found curl / uname / tar / sha256sum"

# --------------------------------------------------------------------
# Step 2 — preflight: OS + arch
# --------------------------------------------------------------------

step "Preflight: detecting OS + architecture / 检测系统与架构"

os_raw=$(uname -s)
arch_raw=$(uname -m)
os=$(printf '%s' "$os_raw" | tr '[:upper:]' '[:lower:]')
case "$arch_raw" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "unsupported architecture: $arch_raw" \
           "5gpn ships pre-built binaries for x86_64 (amd64) and aarch64 (arm64) only." \
           "If your box is a Raspberry Pi 2/3 (armv7) or similar, build from source:" \
           "  git clone https://github.com/${REPO} && cd 5GPN-Go && make" ;;
esac
case "$os" in
    linux)  ;;
    darwin) warn "macOS detected — installer sets up a launchd-free dev install only." ;;
    *) die "unsupported OS: $os_raw" \
           "5gpn officially supports Linux (systemd) and macOS (dev-only)." ;;
esac
good "target: ${os}-${arch}"

# --------------------------------------------------------------------
# Step 3 — preflight: privileges + disk
# --------------------------------------------------------------------

step "Preflight: privileges + disk / 权限与磁盘"

if [ "$os" = "linux" ] && [ "$(id -u)" -ne 0 ]; then
	if command -v sudo >/dev/null 2>&1; then
        info "not root — this script will re-exec the installer under sudo."
    else
        die "not root and 'sudo' is not available." \
			"Run as root:  sudo bash -c \"\$(curl -fsSL ...)\"" \
			"For an unprivileged development boot, build from source and use" \
			"  5gpn --orchestrator=noop --listen 127.0.0.1:8443 --insecure"
    fi
fi

# ~60 MB is a very generous ceiling: two binaries + spare.
tmp_root="${TMPDIR:-/tmp}"
if command -v df >/dev/null 2>&1; then
    avail_kb=$(df -Pk "$tmp_root" 2>/dev/null | awk 'NR==2 {print $4}' || echo 0)
    if [ "${avail_kb:-0}" -lt 65536 ]; then
        warn "less than 64 MiB free on ${tmp_root} (have ${avail_kb} KiB). Downloads may fail."
    fi
fi
good "privileges + disk look OK"

# --------------------------------------------------------------------
# Step 4 — preflight: network
# --------------------------------------------------------------------

step "Preflight: network reachability / 网络连通性"

if ! gh_code=$(curl "${CURL_OPTS[@]}" -o /dev/null -w '%{http_code}' https://api.github.com/rate_limit); then
	die "cannot reach api.github.com" \
        "Check outbound HTTPS: curl -v https://api.github.com/rate_limit" \
        "In mainland China, a proxy / mirror may be required:" \
        "  HTTPS_PROXY=http://127.0.0.1:7890 curl -fsSL ... | bash"
fi
if [ "$gh_code" != "200" ] && [ "$gh_code" != "403" ]; then
    warn "GitHub API returned HTTP ${gh_code} (rate-limited or unusual). Continuing anyway."
fi
good "network to github.com is OK"

# --------------------------------------------------------------------
# Step 5 — resolve version tag
# --------------------------------------------------------------------

step "Resolving release tag / 解析版本号"

tag="$VERSION"
if [ "$tag" = "latest" ]; then
    info "asking GitHub for the latest release of ${REPO}"
    resolved=""
    resolved=$(curl "${CURL_OPTS[@]}" "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep -o '"tag_name":[[:space:]]*"[^"]*"' | head -n1 | cut -d'"' -f4 || true)
    if [ -z "$resolved" ]; then
        die "GitHub returned no 'latest' release for ${REPO}" \
            "Either no releases are published yet, or the repo requires auth." \
            "Pin a version explicitly:" \
            "  curl -fsSL ... | GPN_VERSION=v0.2.6 bash"
    fi
    tag="$resolved"
fi
good "installing ${C_BOLD}${tag}${C_RST}"

# --------------------------------------------------------------------
# Step 6 — download
# --------------------------------------------------------------------

step "Downloading binaries + checksums / 下载"

staging_parent="${GPN_TMPDIR:-${TMPDIR:-/tmp}}"
if [ ! -d "$staging_parent" ] || [ ! -w "$staging_parent" ]; then
	die "staging parent is not a writable directory: ${staging_parent}" \
		"Set GPN_TMPDIR to an existing private or system temporary directory."
fi
tmpdir=$(mktemp -d "${staging_parent%/}/5gpn-install.XXXXXX")
chmod 700 "$tmpdir"
cleanup() { rm -rf -- "$tmpdir"; }
trap cleanup EXIT
info "staging in ${tmpdir}"

installer_asset="5gpn-installer-${os}-${arch}"
daemon_asset="5gpn-${os}-${arch}"
base_url="https://github.com/${REPO}/releases/download/${tag}"

download() {
    local name="$1"
    info "fetching ${name}"
    if ! curl "${CURL_OPTS[@]}" -o "${tmpdir}/${name}" "${base_url}/${name}"; then
        die "download failed: ${name}" \
            "URL: ${base_url}/${name}" \
            "Common causes:" \
            "  1. tag ${tag} has no ${name} asset (release build failed) — check the release page" \
            "  2. corporate proxy / firewall blocking objects.githubusercontent.com" \
            "  3. IPv6 misconfig; retry with:  curl -4 -fsSL ${base_url}/${name}"
    fi
}
download "SHA256SUMS"
download "${installer_asset}"
download "${daemon_asset}"
good "downloaded 3 files"

# --------------------------------------------------------------------
# Step 7 — verify sha256
# --------------------------------------------------------------------

step "Verifying SHA256 checksums / 校验完整性"

verify_sha() {
    local name="$1"
    local expected got
    expected=$(grep " ${name}\$" "${tmpdir}/SHA256SUMS" | awk '{print $1}' | head -n1 || true)
    if [ -z "$expected" ]; then
        die "no SHA256 line for ${name} in the checksum manifest" \
            "This usually means the release was published incomplete." \
            "Try a different tag (e.g. the previous release)."
    fi
    if command -v sha256sum >/dev/null 2>&1; then
        got=$(sha256sum "${tmpdir}/${name}" | awk '{print $1}')
    else
        got=$(shasum -a 256 "${tmpdir}/${name}" | awk '{print $1}')
    fi
    if [ "$expected" != "$got" ]; then
        die "SHA256 mismatch for ${name}" \
            "  want: ${expected}" \
            "  got:  ${got}" \
            "The download was corrupted or tampered with. Re-run the installer; if it" \
            "reproduces, do NOT proceed — file an issue with both values."
    fi
    good "${name}: ${expected:0:12}…"
}
verify_sha "${installer_asset}"
verify_sha "${daemon_asset}"

chmod +x "${tmpdir}/${installer_asset}" "${tmpdir}/${daemon_asset}"

# --------------------------------------------------------------------
# Step 8 — hand off to the Go installer
# --------------------------------------------------------------------

step "Running installer / 执行安装"

install_flags=(--source-binary "${tmpdir}/${daemon_asset}")

# Re-invoke under sudo if we're not root on Linux system-wide installs.
installed_via_sudo=0
if [ "$os" = "linux" ] && [ "$(id -u)" -ne 0 ]; then
    info "elevating with sudo (you may be prompted for a password)"
    if ! sudo "${tmpdir}/${installer_asset}" install "${install_flags[@]}" "$@"; then
        die "sudo installer failed" \
            "See installer output above. Common fixes:" \
            "  - systemctl daemon-reload; systemctl status 5gpn" \
            "  - inspect /etc/5gpn/config.yaml — port 80/443 already in use?" \
            "  - re-run with GPN_VERSION pinned to a known-good tag"
    fi
    installed_via_sudo=1
fi

if [ "$installed_via_sudo" -eq 0 ] && ! "${tmpdir}/${installer_asset}" install "${install_flags[@]}" "$@"; then
    die "installer step failed" \
        "See the last lines of installer output for the specific error." \
        "If it's about port 80/443 being taken, stop caddy/nginx/apache first:" \
        "  systemctl stop caddy nginx apache2 httpd 2>/dev/null || true"
fi

# --------------------------------------------------------------------
# Step 9 — post-install verification
# --------------------------------------------------------------------

step "Post-install verification / 收尾自检"

if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1; then
    # Give the unit a beat to start before probing.
    sleep 2
    if systemctl is-active --quiet 5gpn; then
        good "systemd unit 5gpn is active"
    else
        warn "5gpn unit is NOT active"
        hint "diagnose with:  journalctl -u 5gpn -n 60 --no-pager"
        hint "if boot fails on TLS port, re-run wizard with Auto-SSL enabled or"
        hint "move server.panel_port to something like 8443."
    fi

    # Loopback health probe — try HTTPS first, HTTP fallback.
    port=$(sed -n 's/^ *panel_port: *\([0-9]*\).*/\1/p' /etc/5gpn/config.yaml 2>/dev/null | head -n1 || echo "")
    port="${port:-8443}"
    if curl -sk --max-time 3 "https://127.0.0.1:${port}/api/v1/health" | grep -q '"ok":true'; then
        good "panel responds on https://127.0.0.1:${port}/api/v1/health"
    elif curl -s --max-time 3 "http://127.0.0.1:${port}/api/v1/health" | grep -q '"ok":true'; then
        good "panel responds on http://127.0.0.1:${port}/api/v1/health (plain HTTP mode)"
    else
        warn "panel health check timed out on port ${port}"
        hint "give it another 10s and try:  curl -sk https://127.0.0.1:${port}/api/v1/health"
    fi
else
    info "non-systemd host — skipping unit / health checks"
fi

# --------------------------------------------------------------------
# Done
# --------------------------------------------------------------------

cat <<EOF

${C_GRN}${C_BOLD}✓ Install complete / 安装完成${C_RST}

${C_BOLD}Next steps / 下一步${C_RST}
  1. Read the setup token in /var/log/syslog or:
       ${C_DIM}journalctl -u 5gpn -n 100 | grep -A2 'SETUP TOKEN'${C_RST}
  2. The fresh panel is loopback-only. From your workstation, open a tunnel:
       ${C_DIM}ssh -L 8443:127.0.0.1:8443 root@<server-ip>${C_RST}
  3. Open ${C_CYN}http://127.0.0.1:8443/${C_RST}, claim the panel, then configure
     the public domain and HTTPS in the wizard before changing panel_bind.

${C_BOLD}If HTTPS is broken / 如果 HTTPS 打不开${C_RST}
  ${C_DIM}- confirm your DNS A record points at this box${C_RST}
  ${C_DIM}- confirm ports 80 and 443 are open publicly (nftables/iptables/cloud SG)${C_RST}
  ${C_DIM}- check Let's Encrypt issuance:${C_RST}
      ${C_DIM}journalctl -u 5gpn -g acme -n 40 --no-pager${C_RST}

${C_BOLD}Uninstall / 卸载${C_RST}
  ${C_DIM}sudo systemctl stop 5gpn && sudo systemctl disable 5gpn${C_RST}
  ${C_DIM}sudo rm -rf /etc/5gpn /var/lib/5gpn /usr/local/bin/5gpn${C_RST}

EOF
