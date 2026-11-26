#!/usr/bin/env bash
# Tests for scripts/changelog.sh against a throwaway git repository.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT

git -C "$repo" init -q
git -C "$repo" config user.name test
git -C "$repo" config user.email test@example.invalid
git -C "$repo" config commit.gpgsign false
commit() { git -C "$repo" commit -q --allow-empty -m "$@"; }
commit "chore: initial"
git -C "$repo" tag v0.1.0
commit "fix!: drop the old flag"
commit "feat!: rename the config key"
commit "perf: cache lookups" -m "BREAKING CHANGE: cache is now required"
commit "fix: handle empty input"
commit "feat: add a new command"
commit "docs: explain things"

out=$(cd "$repo" && "$root/scripts/changelog.sh" --version 0.2.0 --from v0.1.0 --date 2026-01-01)

fail=0
section() { printf '%s\n' "$out" | awk -v h="### $1" '$0 == h { on = 1; next } /^### / { on = 0 } on && /^- /'; }
expect() {
  local name=$1 want=$2 got
  got=$(section "$name" | sed -E 's/ \(\[[0-9a-f]{7}\].*$//')
  if [ "$got" != "$want" ]; then
    printf 'FAIL: %s\n--- want\n%s\n--- got\n%s\n' "$name" "$want" "$got" >&2
    fail=1
  fi
}

expect "Breaking changes" "- drop the old flag
- rename the config key
- cache lookups"
expect "Features" "- add a new command"
expect "Fixes" "- handle empty input"
expect "Performance" ""

for s in "drop the old flag" "rename the config key" "cache lookups"; do
  n=$(printf '%s\n' "$out" | grep -c -- "- $s (" || true)
  if [ "$n" != 1 ]; then
    printf 'FAIL: %q listed %s times, want once\n' "$s" "$n" >&2
    fail=1
  fi
done

if printf '%s\n' "$out" | grep -q "explain things"; then
  echo "FAIL: docs commit should not be listed" >&2
  fail=1
fi

[ "$fail" = 0 ] || { printf '%s\n' "$out" >&2; exit 1; }
echo "changelog tests passed"
