#!/usr/bin/env bash
# Move a Jira issue for a canonical devflow event, using flow-map.yml.
#
#   transition.sh <KEY> <event|#verb>
#
# Events: branch.created pr.opened pr.merged deploy.staging deploy.production
# Verbs:  #start #review #qa #done #block
#
# Env: JIRA_BASE JIRA_USER JIRA_TOKEN   required
#      FLOW_MAP      path to flow-map.yml (default: flow-map.yml next to this script's repo)
#      DRY_RUN=1     print what would happen, change nothing
#      TRANSITION_COMMENT   optional comment to add after a successful move
#
# Rules: projects not in the flow map are ignored; an explicit null means "do nothing";
# tickets in a Done-category status are never moved; transitions only go forward
# (by status category, and by the project's optional `order:` list).
set -euo pipefail
# shellcheck source=bin/lib.sh
source "$(dirname "$0")/lib.sh"

key="${1:?usage: transition.sh <KEY> <event|#verb>}"
event="${2:?usage: transition.sh <KEY> <event|#verb>}"
project="${key%%-*}"
map="${FLOW_MAP:-$(dirname "$0")/../flow-map.yml}"
need_env JIRA_BASE JIRA_USER JIRA_TOKEN
[ -f "$map" ] || die "flow map not found: $map"

if [[ "$event" == \#* ]]; then section=verbs; name="${event#\#}"; else section=events; name="$event"; fi

map_json=$(yq -o=json '.' "$map")

if [ "$(jq -r --arg p "$project" '.projects | has($p)' <<<"$map_json")" != true ]; then
  log "$key: project $project is not in the flow map, skipping"
  exit 0
fi

target=$(jq -r --arg p "$project" --arg e "$name" --arg sec "$section" '
  def pick(o): if $sec == "verbs" then (o.verbs // {}) else o end;
  (pick(.projects[$p] // {})) as $pj
  | (pick(.defaults // {})) as $df
  | if ($pj | has($e)) then ($pj[$e] // "")
    elif ($df | has($e)) then ($df[$e] // "")
    else "" end' <<<"$map_json")

if [ -z "$target" ]; then
  log "$key: no mapping for $event in $project"
  exit 0
fi

issue=$(atl GET "$JIRA_BASE/rest/api/3/issue/$key?fields=status") || die "$key: cannot read issue"
current=$(jq -r '.fields.status.name' <<<"$issue")
cur_cat=$(jq -r '.fields.status.statusCategory.key' <<<"$issue")

[ "$current" = "$target" ] && { log "$key: already in $target"; exit 0; }
[ "$cur_cat" = "done" ] && { log "$key: already done ($current), not moving"; exit 0; }

transitions=$(atl GET "$JIRA_BASE/rest/api/3/issue/$key/transitions") || die "$key: cannot list transitions"
tid=$(jq -r --arg t "$target" '[.transitions[] | select(.to.name == $t)][0].id // empty' <<<"$transitions")
to_cat=$(jq -r --arg t "$target" '[.transitions[] | select(.to.name == $t)][0].to.statusCategory.key // empty' <<<"$transitions")

if [ -z "$tid" ]; then
  warn "$key: no transition from '$current' to '$target' ($event)"
  exit 0
fi

# Forward-only by category: new(0) < indeterminate(1) < done(2).
rank() { case "$1" in new) echo 0 ;; indeterminate) echo 1 ;; done) echo 2 ;; *) echo 1 ;; esac; }
if [ "$(rank "$to_cat")" -lt "$(rank "$cur_cat")" ]; then
  log "$key: '$target' is behind '$current', not moving backwards"
  exit 0
fi

# Forward-only within a category, when the project lists its status order.
pos() { jq -r --arg p "$project" --arg s "$1" '(.projects[$p].order // []) | index($s) // -1' <<<"$map_json"; }
cur_pos=$(pos "$current"); tgt_pos=$(pos "$target")
if [ "$cur_pos" -ge 0 ] && [ "$tgt_pos" -ge 0 ] && [ "$tgt_pos" -lt "$cur_pos" ]; then
  log "$key: '$target' comes before '$current' in $project order, not moving backwards"
  exit 0
fi

if [ "${DRY_RUN:-0}" = 1 ]; then
  echo "$key: $current → $target ($event) [dry run]"
  exit 0
fi

atl POST "$JIRA_BASE/rest/api/3/issue/$key/transitions" \
  -d "$(jq -cn --arg id "$tid" '{transition:{id:$id}}')" >/dev/null || die "$key: transition failed"
echo "$key: $current → $target ($event)"

if [ -n "${TRANSITION_COMMENT:-}" ]; then
  body=$(jq -cn --arg t "$TRANSITION_COMMENT" \
    '{body:{type:"doc",version:1,content:[{type:"paragraph",content:[{type:"text",text:$t}]}]}}')
  atl POST "$JIRA_BASE/rest/api/3/issue/$key/comment" -d "$body" >/dev/null || warn "$key: comment failed"
fi
