#!/usr/bin/env bash
# Render Markdown release notes for a git range, grouped by commit type, with Jira details.
#
#   release-notes.sh <git-range> [title] [task-provider]
#
# Env: JIRA_BASE JIRA_USER JIRA_TOKEN
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

range="${1:?usage: release-notes.sh <git-range> [title] [task-provider]}"
title="${2:-Release notes}"
provider=${3:-none}

subjects=$(git log --no-merges --format='%s' --end-of-options "$range" --)

if [ "$#" -ge 3 ]; then
  refs=$(devflow keys --shipped --range "$range")
  keys=""
  if [ "$provider" = jira ]; then keys=$(paste -sd, - <<<"$refs"); fi
else
  keys=""
fi

issues='{}'
if [ -n "$keys" ] && [ -n "${JIRA_BASE:-}" ] && [ -n "${JIRA_USER:-}" ] && [ -n "${JIRA_TOKEN:-}" ]; then
  body=$(jq -cn --arg jql "key in ($keys)" \
    '{jql:$jql, fields:["summary","issuetype","status"], maxResults:100}')
  issues=$(atl POST "$JIRA_BASE/rest/api/3/search/jql" -d "$body" \
    | jq '[.issues[] | {key:.key, value:{summary:.fields.summary, type:.fields.issuetype.name, status:.fields.status.name}}] | from_entries') \
    || { warn "Jira search failed; notes will list keys only"; issues='{}'; }
fi

section() {
  case "$1" in
    feat) echo "Features" ;; fix) echo "Fixes" ;; perf) echo "Performance" ;;
    revert) echo "Reverts" ;; docs) echo "Documentation" ;; *) echo "Other changes" ;;
  esac
}

echo "# $title"
echo
for t in feat fix perf revert docs other; do
  lines=""
  while IFS= read -r s; do
    [ -n "$s" ] || continue
    type=$(sed -nE 's/^([a-z]+)(\([^)]*\))?!?: .*/\1/p' <<<"$s")
    case "$type" in feat|fix|perf|revert|docs) ;; *) type=other ;; esac
    [ "$type" = "$t" ] || continue
    summary=$(sed -E 's/^[a-z]+(\([^)]*\))?!?: //; s/ #[a-z]+.*$//' <<<"$s")
    k=$(grep -Eo "$KEY_RE" <<<"$s" | head -n1 || true)
    breaking=""; grep -Eq '^[a-z]+(\([^)]*\))?!:' <<<"$s" && breaking=" **(breaking)**"
    if [ "$provider" = jira ] && [ -n "${JIRA_BASE:-}" ] && [ -n "$k" ]; then
      jira=$(jq -r --arg k "$k" '.[$k] | if . then " — \(.summary) (\(.status))" else "" end' <<<"$issues")
      lines+="- [$k]($JIRA_BASE/browse/$k) $summary$breaking$jira"$'\n'
    else
      lines+="- $summary$breaking"$'\n'
    fi
  done <<<"$subjects"
  if [ -n "$lines" ]; then
    echo "## $(section "$t")"
    echo
    printf '%s\n' "$lines"
  fi
done
if [ "$provider" != jira ] && [ -n "${refs:-}" ]; then
  printf '\n## Tasks\n\n'
  while IFS= read -r ref; do printf '%s\n' "- $ref"; done <<<"$refs"
fi
