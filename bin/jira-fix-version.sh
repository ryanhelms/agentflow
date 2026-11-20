#!/usr/bin/env bash
# Add a Fix Version to an issue, creating the version in the project if needed.
#
#   jira-fix-version.sh <KEY> <version-name>
#
# Env: JIRA_BASE JIRA_USER JIRA_TOKEN
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

key="${1:?usage: jira-fix-version.sh <KEY> <version>}"
name="${2:?usage: jira-fix-version.sh <KEY> <version>}"
project="${key%%-*}"
need_env JIRA_BASE JIRA_USER JIRA_TOKEN

exists=$(atl GET "$JIRA_BASE/rest/api/3/project/$project/versions" \
  | jq -r --arg n "$name" 'any(.[]; .name == $n)')
if [ "$exists" != true ]; then
  atl POST "$JIRA_BASE/rest/api/3/version" \
    -d "$(jq -cn --arg n "$name" --arg p "$project" '{name:$n, project:$p, released:false}')" >/dev/null
fi
atl PUT "$JIRA_BASE/rest/api/3/issue/$key" \
  -d "$(jq -cn --arg n "$name" '{update:{fixVersions:[{add:{name:$n}}]}}')" >/dev/null
echo "$key: fix version $name"
