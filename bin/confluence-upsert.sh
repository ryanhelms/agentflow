#!/usr/bin/env bash
# Create or update a Confluence page from a Markdown file.
#
#   confluence-upsert.sh <space-key> <parent-page-id|-> <title> <file.md>
#
# Env: CONFLUENCE_BASE JIRA_USER JIRA_TOKEN (same Atlassian account), GITHUB_REPOSITORY (banner)
# Needs: pandoc, jq, curl
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

space_key="${1:?usage: confluence-upsert.sh <space-key> <parent-id|-> <title> <file.md>}"
parent="${2:?}"; title="${3:?}"; src="${4:?}"
need_env CONFLUENCE_BASE JIRA_USER JIRA_TOKEN
command -v pandoc >/dev/null || die "pandoc is required"
[ -f "$src" ] || die "no such file: $src"

api="$CONFLUENCE_BASE/wiki/api/v2"
space_id=$(atl GET "$api/spaces?keys=$space_key" | jq -r '.results[0].id // empty')
[ -n "$space_id" ] || die "Confluence space $space_key not found"

origin="${GITHUB_REPOSITORY:-a git repository}"
banner="<p><em>Generated from ${origin}. Edit the source in the repo, not here.</em></p>"
html="$banner$(pandoc --from gfm --to html "$src")"

existing=$(atl GET "$api/pages" -G --data-urlencode "title=$title" --data-urlencode "space-id=$space_id" \
  | jq -c '.results[0] // empty')

if [ -z "$existing" ]; then
  payload=$(jq -cn --arg s "$space_id" --arg p "$parent" --arg t "$title" --arg b "$html" \
    '{spaceId:$s, status:"current", title:$t, body:{representation:"storage", value:$b}}
     + (if $p == "-" then {} else {parentId:$p} end)')
  id=$(atl POST "$api/pages" -d "$payload" | jq -r '.id')
  echo "created page $id: $title"
else
  id=$(jq -r '.id' <<<"$existing")
  ver=$(atl GET "$api/pages/$id" | jq '.version.number + 1')
  payload=$(jq -cn --arg id "$id" --arg t "$title" --arg b "$html" --argjson v "$ver" \
    '{id:$id, status:"current", title:$t, version:{number:$v, message:"agentflow sync"},
      body:{representation:"storage", value:$b}}')
  atl PUT "$api/pages/$id" -d "$payload" >/dev/null
  echo "updated page $id to version $ver: $title"
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "page_id=$id" >> "$GITHUB_OUTPUT"
  echo "page_url=$CONFLUENCE_BASE/wiki/pages/viewpage.action?pageId=$id" >> "$GITHUB_OUTPUT"
fi
