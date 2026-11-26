#!/usr/bin/env bash
# End-to-end test of the Claude Code plugin against a locally built agentflow.
#
# Builds agentflow, installs it into a throwaway cache, creates a throwaway repo with
# `agentflow init`, then drives `claude -p` with the plugin loaded from
# adapters/claude-code and checks: the MCP server connects with all tools, the skills
# load, the SessionStart hook injects ticket context, /agentflow:commit makes a
# convention commit through the confirmation flow, the PreToolUse hook blocks the
# never-list commands, a new nscaledev repo is set up automatically, and
# /agentflow:remove undoes it. Needs: go, git, jq, a logged-in `claude` CLI and `gh auth`
# (for the remote flow map). Nothing touches your real repos, caches or Claude config,
# apart from the Claude account usage of the runs.
#
#   scripts/e2e-claude.sh            # run everything
#   KEEP=1 scripts/e2e-claude.sh     # keep the temp dir for inspection
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
jira_pid=
cleanup() {
  if [ -n "$jira_pid" ]; then
    kill "$jira_pid" 2>/dev/null || true
    wait "$jira_pid" 2>/dev/null || true
  fi
  if [ "${KEEP:-}" != 1 ]; then rm -rf "$tmp"; fi
}
trap cleanup EXIT
if [ "${KEEP:-}" = 1 ]; then
  echo "temp dir: $tmp"
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

export XDG_CACHE_HOME="$tmp/cache" AGENTFLOW_HOME="$tmp/home" AGENTFLOW_NO_UPDATE=1
mkdir -p "$XDG_CACHE_HOME/agentflow/dev" "$AGENTFLOW_HOME"
(cd "$root" && go build -o "$XDG_CACHE_HOME/agentflow/dev/agentflow" ./cmd/agentflow)
ln -sfn dev "$XDG_CACHE_HOME/agentflow/current"
# Keep incidental CLI calls in the test on the fixture binary even if the
# developer already has a different Agentflow release on PATH.
export PATH="$XDG_CACHE_HOME/agentflow/current:$PATH"

# Supply a real issue to the hook without consulting the developer's Jira account.
(cd "$root" && go build -o "$tmp/jira-fixture" ./scripts/testdata/jira)
mkfifo "$tmp/jira-url"
"$tmp/jira-fixture" >"$tmp/jira-url" &
jira_pid=$!
IFS= read -r -t 10 jira_url <"$tmp/jira-url"
mkdir -p "$AGENTFLOW_HOME/providers"
cat >"$AGENTFLOW_HOME/providers/jira.yml" <<YAML
version: 1
providers:
  jira:
    task_management:
      settings: {site: "$jira_url"}
YAML
export JIRA_USER=fixture@example.invalid JIRA_TOKEN=e2e-fixture

repo="$tmp/repo"
mkdir -p "$repo"
cd "$repo"
git init -q -b main
git config user.email "e2e@nscale.com"
git config user.name "agentflow e2e"
git commit -q --allow-empty -m "chore(PLAT-1): init"
"$XDG_CACHE_HOME/agentflow/current/agentflow" init --non-interactive --provider task_management=jira >/dev/null
rm -f .claude/settings.json # load the plugin from this checkout instead of the marketplace
git add -A
# The pre-commit hook refuses commits in the primary checkout; this setup commit is the exception.
AGENTFLOW_ALLOW_PRIMARY=1 git commit -q -m "chore(PLAT-1): add agentflow configuration"
primary="$repo"
repo="$tmp/worktree"
git worktree add -q -b PLAT-123-gpu-quota-api "$repo"
cd "$repo"
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

echo "0. worktree hooks"
check "commit in the primary checkout refused" bash -c '! git -C "$1" commit -q --allow-empty -m "chore(PLAT-1): blocked"' _ "$primary"

echo "1. plugin loads"
run_claude "$tmp/1.jsonl" "Reply with the word ready." "mcp__plugin_agentflow_agentflow"
init=$(jq -c 'select(.type=="system" and .subtype=="init")' "$tmp/1.jsonl")
check "MCP server connected" jq -e '.mcp_servers[] | select(.name=="plugin:agentflow:agentflow" and .status=="connected")' <<<"$init"
check "25 agentflow tools" jq -e '[.tools[] | select(startswith("mcp__plugin_agentflow_agentflow__"))] | length >= 25' <<<"$init"
check "21 skills" jq -e '[.slash_commands[] | select(startswith("agentflow:"))] | length >= 21' <<<"$init"
check "manager skill available" jq -e '.slash_commands | index("agentflow:manager") != null' <<<"$init"
check "SessionStart context" jq -e -s 'map(select(.subtype=="hook_response" and .hook_event=="SessionStart")) | any(.output | contains("agentflow · PLAT-123") and contains("Fixture GPU quota task"))' "$tmp/1.jsonl"

echo "2. /agentflow:commit"
run_claude "$tmp/2.jsonl" "/agentflow:commit — I approve every agentflow confirmation in advance: when a tool returns needs_confirmation, call it again with the confirm_token." "mcp__plugin_agentflow_agentflow"
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

echo "4. automatic init and /agentflow:remove"
fresh="$tmp/fresh"
mkdir -p "$fresh"
cd "$fresh"
git init -q -b main
git remote add origin https://github.com/nscaledev/e2e-fresh.git
git config user.email "e2e@nscale.com"
git config user.name "agentflow e2e"
git commit -q --allow-empty -m "chore(PLAT-1): init"
run_claude "$tmp/4.jsonl" "Reply with the word ready." "mcp__plugin_agentflow_agentflow"
check "repo set up on session start" test -f .agentflow/config.yml
check "local installation receipt created" test -f .agentflow/installation.yml
check "developer told about it" jq -e -s 'map(select(.subtype=="hook_response" and .hook_event=="SessionStart")) | any(.output | contains("set this repo up automatically"))' "$tmp/4.jsonl"
check "nothing committed" test "$(git log -1 --format=%s)" = "chore(PLAT-1): init"
rm -f .claude/settings.json
plugin_launcher="$root/adapters/claude-code/bin/agentflow"
# Claude's Bash permission matcher distinguishes quoted paths in the literal command.
run_claude "$tmp/5.jsonl" "/agentflow:remove — I confirm the removal in advance; run the packaged launcher's preview and removal as separate Bash calls, and don't commit anything." \
  "Bash(sh \"$plugin_launcher\" remove --dry-run)" "Bash(sh \"$plugin_launcher\" remove)" \
  "Bash(sh $plugin_launcher remove --dry-run)" "Bash(sh $plugin_launcher remove)" "Bash(git status:*)"
check "agentflow removed" test ! -e .agentflow
check "auto init off for this clone" test "$(git config agentflow.autoinit)" = false
run_claude "$tmp/6.jsonl" "Reply with the word ready." "mcp__plugin_agentflow_agentflow"
check "not set up again" test ! -e .agentflow

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
