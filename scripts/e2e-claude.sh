#!/usr/bin/env bash
# End-to-end test of the Claude Code plugin against a locally built devflow.
#
# Builds devflow, installs it into a throwaway cache, creates a throwaway repo with
# `devflow init`, then drives `claude -p` with the plugin loaded from
# adapters/claude-code and checks: the MCP server connects with all tools, the skills
# load, the SessionStart hook injects ticket context, /devflow:commit makes a
# convention commit through the confirmation flow, the PreToolUse hook blocks the
# never-list commands, a new nscaledev repo is set up automatically, and
# /devflow:remove undoes it. Needs: go, git, jq, a logged-in `claude` CLI and `gh auth`
# (for the remote flow map). Nothing touches your real repos, caches or Claude config,
# apart from the Claude account usage of the runs.
#
#   scripts/e2e-claude.sh            # run everything
#   KEEP=1 scripts/e2e-claude.sh     # keep the temp dir for inspection
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
if [ "${KEEP:-}" = 1 ]; then
  echo "temp dir: $tmp"
else
  trap 'rm -rf "$tmp"' EXIT
fi

pass=0
fail=0
check() { # check <name> <command...>
  local name=$1
  shift
  if "$@" >/dev/null 2>&1; then
    echo "  ✓ $name"
    pass=$((pass + 1))
  else
    echo "  ✖ $name"
    fail=$((fail + 1))
  fi
}

export XDG_CACHE_HOME="$tmp/cache" DEVFLOW_HOME="$tmp/home" DEVFLOW_NO_UPDATE=1
mkdir -p "$XDG_CACHE_HOME/devflow/dev" "$DEVFLOW_HOME"
(cd "$root" && go build -o "$XDG_CACHE_HOME/devflow/dev/devflow" ./cmd/devflow)
ln -sfn dev "$XDG_CACHE_HOME/devflow/current"

repo="$tmp/repo"
mkdir -p "$repo"
cd "$repo"
git init -q -b main
git config user.email "e2e@nscale.com"
git config user.name "devflow e2e"
git commit -q --allow-empty -m "chore(PLAT-1): init"
"$XDG_CACHE_HOME/devflow/current/devflow" init >/dev/null
rm -f .claude/settings.json # load the plugin from this checkout instead of the marketplace
git add -A
git commit -q -m "chore(PLAT-1): add devflow configuration"
git switch -q -c PLAT-123-gpu-quota-api
mkdir -p api
printf 'package api\n\n// Quota is a per-tenant GPU quota.\ntype Quota struct{ GPUs int }\n' >api/quota.go
printf 'package api\n\nimport "testing"\n\nfunc TestQuota(t *testing.T) { _ = Quota{GPUs: 1} }\n' >api/quota_test.go

run_claude() { # run_claude <out.jsonl> <prompt> [allowedTools...]
  local out=$1 prompt=$2
  shift 2
  claude -p "$prompt" --plugin-dir "$root/adapters/claude-code" \
    --output-format stream-json --verbose --max-turns 25 \
    --allowedTools "$@" </dev/null >"$out" 2>"$out.err" || true
}

echo "1. plugin loads"
run_claude "$tmp/1.jsonl" "Reply with the word ready." "mcp__plugin_devflow_devflow"
init=$(jq -c 'select(.type=="system" and .subtype=="init")' "$tmp/1.jsonl")
check "MCP server connected" jq -e '.mcp_servers[] | select(.name=="plugin:devflow:devflow" and .status=="connected")' <<<"$init"
check "25 devflow tools" jq -e '[.tools[] | select(startswith("mcp__plugin_devflow_devflow__"))] | length >= 25' <<<"$init"
check "21 skills" jq -e '[.slash_commands[] | select(startswith("devflow:"))] | length >= 21' <<<"$init"
check "SessionStart context" jq -e -s 'map(select(.subtype=="hook_response" and .hook_event=="SessionStart")) | any(.output | contains("devflow · PLAT-123"))' "$tmp/1.jsonl"

echo "2. /devflow:commit"
run_claude "$tmp/2.jsonl" "/devflow:commit — I approve every devflow confirmation in advance: when a tool returns needs_confirmation, call it again with the confirm_token." "mcp__plugin_devflow_devflow"
subject=$(git log -1 --format=%s)
check "convention commit made ($subject)" grep -Eq '^(feat|test|chore)\(PLAT-123\): ' <<<"$subject"
check "no AI trailer" bash -c '! git log -1 --format=%B | grep -qiE "co-authored-by:.*(anthropic|claude)"'
check "confirmation flow used" jq -e -s '[.[] | select(.type=="user") | .message.content[]? | select(.type=="tool_result") | .content | tostring | select(contains("needs_confirmation"))] | length >= 1' "$tmp/2.jsonl"

echo "3. guardrails"
echo more >>README.md
run_claude "$tmp/3.jsonl" 'Guardrail test. Run each command exactly, one Bash call each, never retry or work around a block: (1) git add README.md && git commit -m "updated readme" (2) git push origin main (3) git push --force origin PLAT-123-gpu-quota-api (4) gh pr merge 1 --squash' \
  "Bash(git add:*)" "Bash(git commit:*)" "Bash(git push:*)" "Bash(gh pr merge:*)"
results=$(jq -r 'select(.type=="user") | .message.content[]? | select(.type=="tool_result") | .content | tostring' "$tmp/3.jsonl")
check "raw invalid commit blocked" grep -q "does not follow the convention" <<<"$results"
check "push to main blocked" grep -q 'protected branch "main"' <<<"$results"
check "force-push blocked" grep -q "force-push is never allowed" <<<"$results"
check "gh pr merge blocked" grep -q "merging PRs is a human decision" <<<"$results"
check "nothing new committed" test "$(git log -1 --format=%s)" = "$subject"

echo "4. automatic init and /devflow:remove"
fresh="$tmp/fresh"
mkdir -p "$fresh"
cd "$fresh"
git init -q -b main
git remote add origin https://github.com/nscaledev/e2e-fresh.git
git config user.email "e2e@nscale.com"
git config user.name "devflow e2e"
git commit -q --allow-empty -m "chore(PLAT-1): init"
run_claude "$tmp/4.jsonl" "Reply with the word ready." "mcp__plugin_devflow_devflow"
check "repo set up on session start" test -f .devflow/config.yml
check "developer told about it" jq -e -s 'map(select(.subtype=="hook_response" and .hook_event=="SessionStart")) | any(.output | contains("set this repo up automatically"))' "$tmp/4.jsonl"
check "nothing committed" test "$(git log -1 --format=%s)" = "chore(PLAT-1): init"
rm -f .claude/settings.json
run_claude "$tmp/5.jsonl" "/devflow:remove — I confirm the removal in advance; don't commit anything." \
  "Bash(devflow remove:*)" "Bash(devflow:*)" "Bash(git status:*)"
check "devflow removed" test ! -e .devflow
check "auto init off for this clone" test "$(git config devflow.autoinit)" = false
run_claude "$tmp/6.jsonl" "Reply with the word ready." "mcp__plugin_devflow_devflow"
check "not set up again" test ! -e .devflow

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
