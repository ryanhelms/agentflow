#!/usr/bin/env bash
# Print unique Jira keys, one per line.
#   jira-keys.sh <git-range>      keys from commit messages in the range (skips opted-out commits)
#   jira-keys.sh -                keys from text on stdin
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

range="${1:?usage: jira-keys.sh <git-range|->}"

if [ "$range" = "-" ]; then
  grep -Eo "$KEY_RE" || true
else
  git log --format='%H' "$range" | while read -r sha; do
    msg=$(git log -1 --format='%B' "$sha")
    is_skipped "$msg" && continue
    grep -Eo "$KEY_RE" <<<"$msg" || true
  done
fi | sort -u
