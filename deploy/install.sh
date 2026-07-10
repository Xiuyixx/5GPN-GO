#!/usr/bin/env bash
# 5gpn bootstrap installer (M0 skeleton).
#
# M0: prints intent + exits. M3 will download the 5gpn-installer binary
# (with sha256 verification), then hand off. All the heavy logic that used
# to live in the 130KB install.sh moves into `cmd/5gpn-installer/`.

set -euo pipefail

REPO="${5GPN_REPO:-https://github.com/Xiuyixx/5gpn}"
VERSION="${5GPN_VERSION:-latest}"

cat <<EOF
[5gpn] M0 bootstrap installer skeleton.
       Repo:    ${REPO}
       Version: ${VERSION}
       See .omc/plans/5gpn-refactor-consensus-plan.md M3 for the real installer.
EOF
