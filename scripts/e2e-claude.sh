#!/usr/bin/env bash
# End-to-end test of the Claude Code plugin against a locally built agentflow.
#
# Builds agentflow, installs it into a throwaway cache, creates a throwaway repo with
# `agentflow init`, then drives `claude -p` with the plugin loaded from
# adapters/claude-code and checks: the MCP server connects with all tools, the skills
# load, the SessionStart hook injects ticket context, /agentflow:commit makes a
# convention commit through the confirmation flow, the PreToolUse hook blocks the
# never-list commands, the harness tools report without starting any harness, the AI
# session tools refuse or ask for confirmation without starting a session, a new
# nscaledev repo is set up automatically, and /agentflow:remove undoes it. Needs: go, git, jq, a logged-in `claude` CLI and `gh auth`
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
# The fixture repos use only their own hooks: no global or system git config
# (a global core.hooksPath would replace the hooks `agentflow init` installs),
# and no session or bypass variables inherited from the shell running this.
printf '[init]\n\tdefaultBranch = main\n' >"$tmp/gitconfig"
export GIT_CONFIG_GLOBAL="$tmp/gitconfig" GIT_CONFIG_NOSYSTEM=1
unset AGENTFLOW_SESSION_REPO AGENTFLOW_ALLOW_PRIMARY
mkdir -p "$XDG_CACHE_HOME/agentflow/dev" "$AGENTFLOW_HOME"
# Harness probes, sign-in checks and agent-started AI sessions are on by
# default, and `claude` itself is on PATH as a harness's base CLI: turn all
# three off before anything starts, so no run starts a harness, a sign-in
# command or a session. The only harness the session checks may ever reach is
# the fake agent, built here and named e2e-fake (ADR "Phase 7 test guard",
# step 1). Its --log file records every message it receives.
mkdir -p "$tmp/bin"
(cd "$root" && go build -o "$tmp/bin/fake-acp" ./internal/acp/acptest/cmd/fake-acp)
fake_log="$tmp/fake-acp.log"
write_config() { # write_config <auto_probe> <agent_start> [disabled IDs...]
  local probe=$1 start=$2 id
  shift 2
  {
    printf 'ai:\n  harnesses:\n    auto_probe: %s\n    sign_in_check: false\n' "$probe"
    printf '    overrides:\n      e2e-fake:\n        name: E2E fake\n        command: "%s"\n' "$tmp/bin/fake-acp"
    printf '        args: ["--mode=session-demo", "--log=%s"]\n' "$fake_log"
    printf '    disabled:'
    if [ "$#" -eq 0 ]; then printf ' []'; fi
    printf '\n'
    for id in "$@"; do printf '      - "%s"\n' "$id"; done
    printf '  sessions:\n    agent_start: %s\n' "$start"
  } >"$AGENTFLOW_HOME/config.yml"
}
write_config false false
# The console daemon's registry is the OS account's own (~/.agentflow-ui), not
# AGENTFLOW_HOME, so a console opened here would start or reuse the person's
# daemon, which probes with the person's real settings and PATH. claude -p run
# from inside a desktop app can look like an interactive host, so turn the
# automatic console off explicitly (CONTINUOUS_INTEGRATION suppresses only
# that), and check at the end that no fixture repository was registered.
export CONTINUOUS_INTEGRATION=1
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
# The pre-commit hook refuses commits in the primary checkout; committing
# `agentflow init` there is the bypass it documents. Task work below runs in a
# linked worktree, as `agentflow start --worktree` sets up, with the hook on.
AGENTFLOW_ALLOW_PRIMARY=1 git commit -q -m "chore(PLAT-1): add agentflow configuration"
git worktree add -q -b PLAT-123-gpu-quota-api "$tmp/task"
cd "$tmp/task"
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
commit_refused() { ! git -C "$1" commit -q --allow-empty -m "chore(PLAT-1): blocked"; }
check "commit in the primary checkout refused" commit_refused "$repo"

echo "1. plugin loads"
run_claude "$tmp/1.jsonl" "Reply with the word ready." "mcp__plugin_agentflow_agentflow"
init=$(jq -c 'select(.type=="system" and .subtype=="init")' "$tmp/1.jsonl")
check "MCP server connected" jq -e '.mcp_servers[] | select(.name=="plugin:agentflow:agentflow" and .status=="connected")' <<<"$init"
check "25 agentflow tools" jq -e '[.tools[] | select(startswith("mcp__plugin_agentflow_agentflow__"))] | length >= 25' <<<"$init"
check "harness tools listed" jq -e '[.tools[] | select(test("__agentflow_ai_(harnesses|doctor)$"))] | length == 2' <<<"$init"
check "AI session tools listed" jq -e '[.tools[] | select(test("__agentflow_ai_(run|status|respond|cancel)$"))] | length == 4' <<<"$init"
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

echo "4. harness detection, without a probe"
run_claude "$tmp/harnesses.jsonl" "Call the agentflow_ai_harnesses tool and then the agentflow_ai_doctor tool, each with no arguments at all. Never set probe. Then reply with the word done." "mcp__plugin_agentflow_agentflow"
calls=$(jq -c 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use" and (.name | test("__agentflow_ai_(harnesses|doctor)$")))' "$tmp/harnesses.jsonl")
check "both harness tools called" jq -e -s '[.[].name] | unique | length == 2' <<<"$calls"
check "no probe asked for" jq -e -s 'all(.[]; .input.probe != true)' <<<"$calls"
check "both returned a report" jq -e -s '[.[] | select(.type=="user") | .message.content[]? | select(.type=="tool_result") | .content | tostring | select(contains("checked_at"))] | length >= 2' "$tmp/harnesses.jsonl"

echo "5. automatic init and /agentflow:remove"
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

# With both toggles off and no probe asked for, nothing may have run a harness:
# no probe log, and no probe or sign-in record in the cache.
no_harness_ran() {
  local dir="$AGENTFLOW_HOME/harnesses"
  [ -d "$dir" ] || return 0
  [ -z "$(find "$dir" -name 'probe.log*')" ] || return 1
  if [ -f "$dir/cache.json" ]; then
    # Records have these keys; the daemon's published states do not.
    if grep -Eq '"(identity|probed_at|checked_at)"' "$dir/cache.json"; then
      return 1
    fi
  fi
  return 0
}
check "no harness or sign-in command ran" no_harness_ran

echo "6. AI sessions, with only the fake agent reachable"
# ADR "Phase 7 test guard", steps 2 to 5. With agent_start on by default, no
# scenario may reach a real harness: every harness ID except e2e-fake is
# disabled before probes and agent starts are turned on.
cd "$tmp/task"
ids=$(agentflow ai harnesses --all --json | jq -r '.harnesses[].id')
disabled=()
for id in $ids; do
  if [ "$id" != e2e-fake ]; then disabled+=("$id"); fi
done
if [ "${#disabled[@]}" -gt 64 ]; then
  echo "  ✖ more than 64 harness IDs to disable; stopping before anything is turned on"
  exit 1
fi
write_config false false ${disabled[@]+"${disabled[@]}"}
left=$(agentflow ai harnesses --all --json | jq -r '[.harnesses[].id] | join(",")')
if [ "$left" != e2e-fake ]; then
  echo "  ✖ harnesses still listed after disabling ($left); stopping before anything is turned on"
  exit 1
fi
write_config true true ${disabled[@]+"${disabled[@]}"}
agentflow ai harnesses --probe --id e2e-fake --json >"$tmp/probe.json"
check "e2e-fake probed" jq -e '.harnesses | length == 1 and .[0].id == "e2e-fake"' "$tmp/probe.json"
run_claude "$tmp/7.jsonl" "AI session test. Make exactly three tool calls, in order, and nothing else: (1) agentflow_ai_status with no arguments; (2) agentflow_ai_run with harness \"claude-acp\" and prompt \"say hi\"; (3) agentflow_ai_run with harness \"e2e-fake\" and prompt \"say hi\". Never pass confirm_token, never call a tool again to apply a confirmation, and do not start anything else. Then reply with the word done." \
  "mcp__plugin_agentflow_agentflow__agentflow_ai_status" "mcp__plugin_agentflow_agentflow__agentflow_ai_run"
session_calls=$(jq -c 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use" and (.name | test("__agentflow_ai_(run|status)$")))' "$tmp/7.jsonl")
# A tool result is a string or a list of text blocks; read the text either way,
# so quotes in a reply are not escaped by re-encoding it as JSON.
result_text='.content | if type == "array" then map(.text? // "") | join("\n") else tostring end'
session_results=$(jq -r "select(.type==\"user\") | .message.content[]? | select(.type==\"tool_result\") | $result_text" "$tmp/7.jsonl")
check "status and run called" jq -e -s '[.[].name | sub(".*__"; "")] | index("agentflow_ai_status") != null and index("agentflow_ai_run") != null' <<<"$session_calls"
check "no confirm_token passed" jq -e -s 'all(.[]; (.input.confirm_token // "") == "")' <<<"$session_calls"
check "status lists no sessions" grep -Eq '"sessions": \[\]' <<<"$session_results"
run_result() { # run_result <harness>: the reply to the agentflow_ai_run call for that harness
  local id
  id=$(jq -r --arg h "$1" 'select(.name | endswith("__agentflow_ai_run")) | select(.input.harness == $h) | .id' <<<"$session_calls" | head -n1)
  [ -n "$id" ] || return 0
  jq -r --arg id "$id" "select(.type==\"user\") | .message.content[]? | select(.type==\"tool_result\" and .tool_use_id == \$id) | $result_text" "$tmp/7.jsonl"
}
claude_acp_result=$(run_result claude-acp)
fake_result=$(run_result e2e-fake)
check "claude-acp refused" grep -Eq 'not_ready|not found|unknown harness' <<<"$claude_acp_result"
no_confirmation() { ! grep -q needs_confirmation <<<"$1"; }
check "claude-acp did not ask for confirmation" no_confirmation "$claude_acp_result"
check "e2e-fake needs confirmation" grep -q needs_confirmation <<<"$fake_result"
# Nothing passed the gate: no session record, no session/new in the fake's
# log (only the probe's initialize), and a probe record for e2e-fake only.
# `ai status` creates the empty session store, so look inside it.
no_session_record() { [ -z "$(ls -A "$AGENTFLOW_HOME/ai/sessions" 2>/dev/null)" ]; }
check "no session created" no_session_record
fake_only_initialize() {
  [ -s "$fake_log" ] || return 1
  jq -e -s '[.[] | select(.method != null) | .method] | length >= 1 and all(. == "initialize")' "$fake_log"
}
check "fake agent saw only the probe's initialize" fake_only_initialize
check "probe cache holds e2e-fake only" jq -e '[.records[].id] | unique == ["e2e-fake"]' "$AGENTFLOW_HOME/harnesses/cache.json"
# Turn sessions and probes off again before the last check.
write_config false false ${disabled[@]+"${disabled[@]}"}

# No console daemon served a fixture repository: the registry lists every
# worktree it serves (read only; nothing there is changed).
no_console_for_fixtures() {
  local registry real
  registry="$(cd ~ && pwd -P)/.agentflow-ui"
  real="$(cd "$tmp" && pwd -P)"
  [ -d "$registry" ] || return 0
  ! grep -qsF -e "$tmp" -e "$real" "$registry"/*.json
}
check "no console daemon served the fixtures" no_console_for_fixtures

echo
echo "$pass passed, $fail failed"
[ "$fail" -eq 0 ]
