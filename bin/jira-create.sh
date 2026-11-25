#!/usr/bin/env bash
# Create a Jira issue unless an open one already carries the de-duplication label.
# Prints the issue key (existing or new).
#
#   jira-create.sh <project> <issue-type> <dedupe-label> <summary> [description] [extra-label...]
#
# Env: JIRA_BASE JIRA_USER JIRA_TOKEN
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

project="${1:?usage}"; type="${2:?usage}"; label="${3:?usage}"; summary="${4:?usage}"
description="${5:-}"; shift 4; [ $# -gt 0 ] && shift
need_env JIRA_BASE JIRA_USER JIRA_TOKEN

search=$(jq -cn --arg jql "project = \"$project\" AND labels = \"$label\" AND statusCategory != Done" \
  '{jql:$jql, fields:["key"], maxResults:1}')
existing=$(atl POST "$JIRA_BASE/rest/api/3/search/jql" -d "$search" | jq -r '.issues[0].key // empty')
if [ -n "$existing" ]; then
  log "open issue $existing already has label $label"
  echo "$existing"
  exit 0
fi

labels=$(printf '%s\n' "$label" agentflow "$@" | jq -R . | jq -cs .)
payload=$(jq -cn --arg p "$project" --arg t "$type" --arg s "${summary:0:250}" --arg d "$description" --argjson l "$labels" '
  {fields:{project:{key:$p}, issuetype:{name:$t}, summary:$s, labels:$l,
    description:{type:"doc", version:1, content:
      [$d | split("\n")[] | {type:"paragraph", content:(if . == "" then [] else [{type:"text", text:.}] end)}]}}}')
atl POST "$JIRA_BASE/rest/api/3/issue" -d "$payload" | jq -r '.key'
