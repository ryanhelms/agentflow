#!/usr/bin/env bash
# Generate agentflow's CHANGELOG.md section for a release from Conventional Commits.
# Used by .github/workflows/release.yml; CHANGELOG.md is never edited by hand.
#
#   scripts/changelog.sh --version 0.2.0 [--from v0.1.0] [--to HEAD] \
#                        [--repo ryanhelms/agentflow] [--date 2026-11-20] \
#                        [--notes notes.md] [--update CHANGELOG.md]
#
# Prints the section to stdout. --notes also writes it (without the version heading)
# for the GitHub Release body. --update inserts it into CHANGELOG.md below the header,
# replacing any existing section for the same version, so re-running is safe.
set -euo pipefail

version=""
from=""
to=HEAD
repo=${GITHUB_REPOSITORY:-ryanhelms/agentflow}
date=$(date -u +%Y-%m-%d)
notes=""
update=""
jira=${JIRA_SITE:-} # without a site, ticket keys are listed unlinked
while [ $# -gt 0 ]; do
  case "$1" in
    --version) version=$2; shift 2 ;;
    --from) from=$2; shift 2 ;;
    --to) to=$2; shift 2 ;;
    --repo) repo=$2; shift 2 ;;
    --date) date=$2; shift 2 ;;
    --notes) notes=$2; shift 2 ;;
    --update) update=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[ -n "$version" ] || { echo "--version is required" >&2; exit 2; }

range=$to
[ -n "$from" ] && range="$from..$to"

breaking=""
features=""
fixes=""
perf=""
reverts=""
key_re='^[A-Z][A-Z0-9]+-[0-9]+(, ?[A-Z][A-Z0-9]+-[0-9]+)*$'
header_re='^([a-z]+)(\(([^()]*)\))?(!)?: (.+)$'

while IFS=$'\x1f' read -r -d $'\x1e' sha subject body; do
  sha=${sha#$'\n'}
  [ -n "$sha" ] || continue
  [[ "$subject" =~ $header_re ]] || continue
  type=${BASH_REMATCH[1]}
  scope=${BASH_REMATCH[3]}
  bang=${BASH_REMATCH[4]}
  summary=${BASH_REMATCH[5]}
  summary=${summary%% #*} # drop trailing #verbs
  [ "$type" = chore ] && [[ "$scope" == release* ]] && continue

  prefix=""
  if [ -n "$scope" ]; then
    if [[ "$scope" =~ $key_re ]]; then
      links=""
      IFS=', ' read -r -a keys <<<"$scope"
      for k in "${keys[@]}"; do
        [ -n "$k" ] || continue
        link=$k
        [ -z "$jira" ] || link="[$k]($jira/browse/$k)"
        links+="${links:+, }$link"
      done
      prefix="**$links:** "
    else
      prefix="**$scope:** "
    fi
  fi
  entry="- $prefix$summary ([${sha:0:7}](https://github.com/$repo/commit/$sha))"

  if [ -n "$bang" ] || [[ "$body" == *"BREAKING CHANGE:"* ]] || [[ "$body" == *"BREAKING-CHANGE:"* ]]; then
    # A breaking change is listed once, under Breaking changes only.
    breaking+="$entry"$'\n'
    continue
  fi
  case "$type" in
    feat) features+="$entry"$'\n' ;;
    fix) fixes+="$entry"$'\n' ;;
    perf) perf+="$entry"$'\n' ;;
    revert) reverts+="$entry"$'\n' ;;
  esac
done < <(git log --reverse --no-merges --format='%H%x1f%s%x1f%b%x1e' "$range")

body=""
add() { [ -n "$2" ] && body+=$'### '"$1"$'\n\n'"$2"$'\n'; return 0; }
add "Breaking changes" "$breaking"
add "Features" "$features"
add "Fixes" "$fixes"
add "Performance" "$perf"
add "Reverts" "$reverts"
[ -n "$body" ] || body=$'Maintenance release: no user-facing changes.\n\n'

if [ -n "$from" ]; then
  heading="## [$version](https://github.com/$repo/compare/$from...v$version) - $date"
else
  heading="## [$version](https://github.com/$repo/releases/tag/v$version) - $date"
fi
section="$heading"$'\n\n'"$body"

printf '%s' "$section"
if [ -n "$notes" ]; then printf '%s' "$body" >"$notes"; fi

if [ -n "$update" ]; then
  header=$'# Changelog\n\nAll notable changes to agentflow. This file is generated on release from\nConventional Commit messages by `scripts/changelog.sh`; do not edit it by hand.\nFeatures, fixes, performance changes, reverts and breaking changes are listed;\ndocs, tests, CI and chores are not.\n\n'
  existing=""
  [ -f "$update" ] && existing=$(cat "$update")
  # Keep everything from the first release heading down, minus any old copy of this version.
  rest=$(printf '%s\n' "$existing" | awk -v v="## [$version]" '
    /^## \[/ { started = 1; skip = (index($0, v) == 1) }
    started && !skip { print }
  ')
  {
    printf '%s' "$header"
    printf '%s' "$section"
    if [ -n "$rest" ]; then printf '%s\n' "$rest"; fi
  } >"$update"
fi
