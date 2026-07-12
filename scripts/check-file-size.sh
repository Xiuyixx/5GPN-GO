#!/usr/bin/env bash
# M4 file-size guard.
#
# Every first-party non-test Go/TypeScript source file must be <= 900 lines. Enforced
# by both pre-commit (staged files) and CI (whole tree). See
# docs/tech-debt.md for the "why" — the ceiling was 800 in v0.4.1; bumped to
# 900 in v0.4.2 to accommodate internal/core/applier.go (806) without a
# forced refactor mid-release. Splitting applier.go is still tracked as tech debt.

set -euo pipefail

LIMIT="${MAX_LINES:-900}"

usage() {
  cat <<EOF
usage: $0 [--staged | --tree]

  --staged   Only look at files currently staged for commit (default in
             pre-commit hook).
  --tree     Walk internal/, cmd/, and web/src/ (used by CI).
  -h, --help Show this help.

env:
  MAX_LINES  Override the 800-line limit.
EOF
}

mode="${1:---tree}"
case "$mode" in
  --staged) ;;
  --tree) ;;
  -h|--help) usage; exit 0 ;;
  *) echo "unknown flag: $mode" >&2; usage; exit 2 ;;
esac

collect() {
  if [ "$mode" = "--staged" ]; then
    git diff --cached --name-only --diff-filter=ACMR \
      | grep -E '^((internal|cmd)/.*\.go|web/src/.*\.(ts|tsx))$' \
      | grep -vE '(_test\.go|\.test\.(ts|tsx)|\.spec\.(ts|tsx))$' \
      || true
  else
    find internal cmd web/src -type f \
      \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' \) \
      -not -name '*_test.go' -not -name '*.test.ts' -not -name '*.test.tsx' \
      -not -name '*.spec.ts' -not -name '*.spec.tsx'
  fi
}

fail=0
while IFS= read -r file; do
  [ -z "$file" ] && continue
  [ -f "$file" ] || continue
  lines=$(wc -l < "$file")
  if [ "$lines" -gt "$LIMIT" ]; then
    printf '  %s: %d lines (limit %d)\n' "$file" "$lines" "$LIMIT" >&2
    fail=1
  fi
done < <(collect)

if [ "$fail" -ne 0 ]; then
  echo "" >&2
  echo "file-size guard failed. Split the offending file(s) or bump MAX_LINES" >&2
  echo "in scripts/check-file-size.sh with a note in docs/tech-debt.md." >&2
  exit 1
fi

echo "file-size guard: ok (limit ${LIMIT})"
