#!/usr/bin/env bash
# Shared helpers for agentflow workflow scripts. Source, don't execute.
# shellcheck disable=SC2034

KEY_RE='[A-Z][A-Z0-9]+-[0-9]+'

log()  { printf '%s\n' "$*" >&2; }
warn() { if [ -n "${GITHUB_ACTIONS:-}" ]; then printf '::warning::%s\n' "$*" >&2; else log "warning: $*"; fi; }
die()  { if [ -n "${GITHUB_ACTIONS:-}" ]; then printf '::error::%s\n' "$*" >&2; else log "error: $*"; fi; exit 1; }

need_env() {
  local v
  for v in "$@"; do
    [ -n "${!v:-}" ] || die "$v is not set"
  done
}

# atl METHOD URL [curl args...]  — Atlassian REST call with basic auth, retry on 429/5xx.
atl() {
  local method="$1" url="$2"; shift 2
  local attempt=0 code body
  body=$(mktemp)
  while :; do
    attempt=$((attempt + 1))
    code=$(curl -sS -o "$body" -w '%{http_code}' -X "$method" \
      -u "${JIRA_USER}:${JIRA_TOKEN}" \
      -H 'Accept: application/json' -H 'Content-Type: application/json' \
      "$@" "$url") || code=000
    case "$code" in
      2??) cat "$body"; rm -f "$body"; return 0 ;;
      429|5??|000)
        if [ "$attempt" -lt 4 ]; then sleep $((attempt * 3)); continue; fi ;;
    esac
    log "HTTP $code from $method $url"
    head -c 500 "$body" >&2 || true
    rm -f "$body"
    return 1
  done
}

# is_skipped TEXT — true when a commit or PR body opts out of agentflow.
is_skipped() {
  grep -Eqi '\[agentflow skip\]|^Agentflow:[[:space:]]*skip' <<<"$1"
}
