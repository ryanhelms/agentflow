#!/usr/bin/env bash
# Tests for scripts/promote-guard.sh against a throwaway git repository.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
guard="$root/scripts/promote-guard.sh"
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT

g() { git -C "$repo" "$@"; }
g init -q -b main
g config user.name test
g config user.email test@example.invalid
g config commit.gpgsign false
commit() { g commit -q --allow-empty -m "$@"; }
bot_commit() {
  GIT_AUTHOR_NAME='github-actions[bot]' GIT_COMMITTER_NAME='github-actions[bot]' commit "$@"
}
run() { (cd "$repo" && "$guard" "$@"); }

fail=0
check() {
  local name=$1 want_status=$2 want_out=$3 got_out got_status=0
  shift 3
  got_out=$(run "$@" 2>&1) || got_status=$?
  got_out=$(printf '%s\n' "$got_out" | sed -E 's/^[0-9a-f]{12} //')
  if [ "$got_status" != "$want_status" ] || [ "$got_out" != "$want_out" ]; then
    printf 'FAIL: %s\n--- want (exit %s)\n%s\n--- got (exit %s)\n%s\n' \
      "$name" "$want_status" "$want_out" "$got_status" "$got_out" >&2
    fail=1
  fi
}

commit "chore: initial"
g tag v0.1.0
g checkout -q -b develop
commit "feat: add a command"
commit "fix(cli): handle empty input"
commit "docs: mention the exclamation mark! in prose"
commit "feat: rename a flag" -m "Body text." -m "Refs: #1"
g tag safe

check "no breaking commits" 0 "" breaking main..safe

commit "feat(PLAT-1)!: drop the old flag"
commit "perf: cache lookups" -m "BREAKING CHANGE: cache is now required"
commit "fix: tidy" -m "BREAKING-CHANGE: the output format changed"
commit "refactor!: move a package"
commit "feat: mention BREAKING CHANGE: in the subject only"
g tag risky

check "breaking commits" 1 "refactor!: move a package
fix: tidy
perf: cache lookups
feat(PLAT-1)!: drop the old flag" breaking safe..risky

# main has only release commits: allowed.
g checkout -q main
bot_commit "chore(release): v0.2.0"
check "release commits only" 0 "" main-extra safe main

# A human commit that looks like a release commit is not.
commit "chore(release): v0.2.1"
check "release commit by a person" 1 "chore(release): v0.2.1 (test)" main-extra safe main
g reset -q --hard HEAD~1

# An earlier promotion merge whose develop side the SHA contains: allowed.
g merge -q --no-ff -m "chore(release): promote develop to main (develop-p1)" safe
bot_commit "chore(release): v0.3.0"
g checkout -q develop
commit "feat: next phase"
g tag next
check "earlier promotion and release commits" 0 "" main-extra next main

# A promotion merge of something the SHA does not contain is not allowed.
g checkout -q -b side main~2
commit "feat: not on develop"
g checkout -q main
g merge -q --no-ff -m "chore(release): promote develop to main (develop-p9)" side
check "promotion of other work" 1 "chore(release): promote develop to main (develop-p9) (test)
feat: not on develop (test)" main-extra next main
g reset -q --hard HEAD~1

# Any other commit on main stops promotion.
commit "fix: hotfix straight on main"
check "other commit on main" 1 "fix: hotfix straight on main (test)" main-extra next main

check "usage" 2 "usage: $guard breaking RANGE | main-extra SHA [MAIN]" nope x

[ "$fail" = 0 ] || exit 1
echo "promote guard tests passed"
