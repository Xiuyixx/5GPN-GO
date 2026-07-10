#!/usr/bin/env bash
# M4 file-size guard.
#
# Every non-test .go file under internal/ must be < 800 lines. Enforced
# by both pre-commit (staged files) and CI (whole tree). See
# docs/tech-debt.md for the "why" — 800 lines is where a single file
# stops being a unit of reasoning and starts hiding coupling.

set -euo pipefail

LIMIT="${MAX_LINES:-800}"

usage() {
  cat <<EOF
usage: $0 [--staged | --tree]

  --staged   Only look at files currently staged for commit (default in
             pre-commit hook).
  --tree     Walk internal/**/*.go (used by CI).
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
      | grep -E '^internal/.*\.go$' \
      | grep -vE '_test\.go$' \
      || true
  else
    find internal -type f -name '*.go' -not -name '*_test.go'
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
