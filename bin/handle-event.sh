#!/usr/bin/env bash
# Translate the current GitHub event into flow-map transitions.
# Reads GITHUB_EVENT_NAME and GITHUB_EVENT_PATH (as set by Actions).
#
#   create (branch)                     -> branch.created  for keys in the branch name
#   pull_request opened/reopened/
#                ready_for_review       -> pr.opened       for keys in title + head branch
#   pull_request closed (merged into
#                the default branch)    -> pr.merged
#   push                                -> #verbs in each commit (#done only on the default branch)
#
# Commits or PR bodies containing "[agentflow skip]" or a "Agentflow: skip" trailer are ignored.
set -euo pipefail
here=$(dirname "$0")
# shellcheck source=bin/lib.sh
source "$here/lib.sh"

name="${GITHUB_EVENT_NAME:?}"
ev="${GITHUB_EVENT_PATH:?}"
default_branch=$(jq -r '.repository.default_branch' "$ev")

keys_in() { grep -Eo "$KEY_RE" <<<"$*" | sort -u || true; }

move() { # key event link
  TRANSITION_COMMENT="agentflow: $2 — $3" "$here/transition.sh" "$1" "$2" \
    || warn "$1: $2 failed"
}

case "$name" in
  create)
    [ "$(jq -r '.ref_type' "$ev")" = branch ] || exit 0
    branch=$(jq -r '.ref' "$ev")
    link="$(jq -r '.repository.html_url' "$ev")/tree/$branch"
    for k in $(keys_in "$branch"); do move "$k" branch.created "$link"; done
    ;;

  pull_request|pull_request_target)
    action=$(jq -r '.action' "$ev")
    title=$(jq -r '.pull_request.title' "$ev")
    body=$(jq -r '.pull_request.body // ""' "$ev")
    head=$(jq -r '.pull_request.head.ref' "$ev")
    base=$(jq -r '.pull_request.base.ref' "$ev")
    link=$(jq -r '.pull_request.html_url' "$ev")
    if is_skipped "$body"; then log "PR opted out of agentflow"; exit 0; fi
    keys=$(keys_in "$title" "$head")
    case "$action" in
      opened|reopened|ready_for_review)
        [ "$(jq -r '.pull_request.draft' "$ev")" = true ] && { log "draft PR, waiting"; exit 0; }
        for k in $keys; do move "$k" pr.opened "$link"; done
        ;;
      closed)
        [ "$(jq -r '.pull_request.merged' "$ev")" = true ] || exit 0
        [ "$base" = "$default_branch" ] || { log "merged into $base, not $default_branch"; exit 0; }
        for k in $keys; do move "$k" pr.merged "$link"; done
        ;;
    esac
    ;;

  push)
    branch=$(jq -r '.ref | sub("^refs/heads/"; "")' "$ev")
    jq -c '.commits[]?' "$ev" | while read -r c; do
      msg=$(jq -r '.message' <<<"$c")
      link=$(jq -r '.url' <<<"$c")
      is_skipped "$msg" && continue
      subject=${msg%%$'\n'*}
      verbs=$(grep -Eo '#(start|review|qa|done|block)\b' <<<"$subject" | sort -u || true)
      [ -n "$verbs" ] || continue
      for k in $(keys_in "$subject"); do
        for v in $verbs; do
          if [ "$v" = "#done" ] && [ "$branch" != "$default_branch" ]; then
            log "$k: #done ignored on $branch"; continue
          fi
          if [ "$v" = "#block" ]; then
            reason=$(tail -n +3 <<<"$msg" | grep -v -E '^[A-Za-z-]+: ' || true)
            TRANSITION_COMMENT="Blocked: ${reason:-see $link}" "$here/transition.sh" "$k" "$v" || warn "$k: $v failed"
          else
            move "$k" "$v" "$link"
          fi
        done
      done
    done
    ;;

  *)
    log "event $name is not handled by agentflow transitions"
    ;;
esac
