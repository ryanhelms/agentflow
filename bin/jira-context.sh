#!/usr/bin/env bash
# Look up the first Jira key in some text and write its context to $GITHUB_OUTPUT
# (or stdout): key, summary, status, criteria.
#
#   jira-context.sh "<text containing a key, e.g. a PR title>"
#
# Env: JIRA_BASE JIRA_USER JIRA_TOKEN
#      JIRA_AC_FIELD   optional custom field id holding acceptance criteria (e.g. customfield_10050)
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

text="${1:?usage: jira-context.sh <text>}"
need_env JIRA_BASE JIRA_USER JIRA_TOKEN

key=$(grep -Eo "$KEY_RE" <<<"$text" | head -n1 || true)
[ -n "$key" ] || die "no Jira key found in: $text"

fields="summary,status,description"
[ -n "${JIRA_AC_FIELD:-}" ] && fields="$fields,$JIRA_AC_FIELD"
issue=$(atl GET "$JIRA_BASE/rest/api/3/issue/$key?fields=$fields") || die "$key not found"

# Flatten Atlassian Document Format to plain text, one block per line.
adf_text='def txt: if type == "object" then
    (if .type == "text" then .text
     elif .type == "hardBreak" then "\n"
     else ((.content // []) | map(txt) | join(""))
          + (if (.type | IN("paragraph","heading","listItem","codeBlock")) then "\n" else "" end)
     end)
  elif type == "array" then map(txt) | join("") else "" end; txt'

summary=$(jq -r '.fields.summary' <<<"$issue")
status=$(jq -r '.fields.status.name' <<<"$issue")
if [ -n "${JIRA_AC_FIELD:-}" ]; then
  criteria=$(jq -r --arg f "$JIRA_AC_FIELD" ".fields[\$f] // \"\" | if type == \"string\" then . else ($adf_text) end" <<<"$issue")
else
  # Fall back to the "Acceptance criteria" part of the description, else the whole description.
  desc=$(jq -r ".fields.description // \"\" | if type == \"string\" then . else ($adf_text) end" <<<"$issue")
  criteria=$(awk 'BEGIN{IGNORECASE=1} /acceptance criteria/{f=1; next} f' <<<"$desc")
  [ -n "$criteria" ] || criteria="$desc"
fi
criteria=$(head -c 4000 <<<"$criteria")

emit() { # name value — multiline-safe GITHUB_OUTPUT
  local name="$1" value="$2" delim
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    delim="EOF_$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
    { echo "$name<<$delim"; printf '%s\n' "$value"; echo "$delim"; } >> "$GITHUB_OUTPUT"
  else
    printf '%s: %s\n' "$name" "$value"
  fi
}

emit key "$key"
emit summary "$summary"
emit status "$status"
emit criteria "$criteria"
