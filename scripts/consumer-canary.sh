#!/usr/bin/env bash
# Replay only fixture data and read/plan steps. No remote repository is configured.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
export AGENTFLOW_HOME="$temp/profile" AGENTFLOW_NO_UPDATE=1 GOPROXY=off
export GIT_ALLOW_PROTOCOL=file GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1
unset GH_TOKEN GITHUB_TOKEN GITHUB_SERVER_URL JIRA_BASE JIRA_USER JIRA_TOKEN AGENTFLOW_APPROVED_ISSUE
mkdir -p "$temp/bin" "$temp/consumer"
pkg=github.com/nscaledev/agentflow/internal/version
pin=2222222222222222222222222222222222222222
go build -o "$temp/bin/agentflow" -ldflags "-X $pkg.Commit=$pin -X $pkg.Version=0.2.0" ./cmd/agentflow
export PATH="$temp/bin:$PATH"

# These adapters use fake HTTP transports and temporary local Git repositories.
go test -count=1 ./internal/event ./internal/app ./internal/setup ./internal/provider/githubdevops ./internal/provider/githubissues ./internal/provider/notifications \
  -run 'TestLegacyHandleEventFixtures|TestApplyGitHubTaskEventReplay|TestConsumerTaskEventCannotApply|TestWorkflowGitHubTaskReads|TestWorkflowJiraTaskReads|TestDocsRepositoryPublishPR|TestProviderWorkflowCompatibility|TestCreateReconcilesAndPreservesBody|TestPublishRelease$|TestFanOutAndReplay'
export GIT_CONFIG_GLOBAL=/dev/null
cd "$temp/consumer"
git init -qb main
git config user.name Fixture
git config user.email fixture@example.invalid
git config core.hooksPath /dev/null
mkdir -p .github/workflows .agentflow/tasks cli
caller=.github/workflows/agentflow.yml
config=.agentflow/config.yml
old=1111111111111111111111111111111111111111
# Older callers declare only JIRA_USER and JIRA_TOKEN for the task jobs.
sed -e '/^      TASK_READ_TOKEN:/d' -e "s/@v0$/@$old # v0.1.0/" "$root/templates/repo/$caller" > "$caller"
printf '%s\n' 'agentflow: 1' 'task_management: {provider: jira}' > "$config"
cat > .agentflow/releases.yml <<'YAML'
strategy: trunk
units:
  - {name: widget, path: ., release_type: simple, builds: []}
  - {name: cli, path: cli, release_type: go, builds: [binary]}
YAML
echo 'module example.invalid/cli' > cli/go.mod
echo '{".":"1.0.0","cli":"2.0.0"}' > .release-please-manifest.json
agentflow release sync
jq -e '.packages["."].component=="widget" and .packages.cli["release-type"]=="go"' release-please-config.json >/dev/null
jq -e '. == {".":"1.0.0","cli":"2.0.0"}' .release-please-manifest.json >/dev/null
git add .
git commit -qm 'chore: establish legacy baseline'
baseline=$(git rev-parse HEAD)
git tag v1.0.0
git tag widget-v1.0.0
git tag cli-v2.0.0
agentflow release plan --json | jq -e 'length==2 and all(.[]; .bump=="none")' >/dev/null
if agentflow pin --check; then echo "expected stale caller pin" >&2; exit 1; fi
git diff --exit-code -- "$caller"
agentflow pin
agentflow pin --check
sed "s/$old # v0.1.0/$pin # v0.2.0/g" < <(git show "$baseline:$caller") | cmp - "$caller"

cat > "$config" <<'YAML'
agentflow: 1
commit: {require_key: false}
task_management: {provider: filesystem, settings: {scope: platform, transitions: {deploy.production: closed}}}
YAML
cat > .agentflow/tasks/quota.md <<'MD'
---
version: 1
kind: task
instance: filesystem
scope: platform
id: quota
title: Quota
state: {id: open, name: Open}
---
MD
git add .
git commit -qm 'feat: implement quota' -m 'Task-Ref: filesystem/platform/quota'
touch cli/quota
git add cli/quota
git commit -qm 'fix: handle cli quotas' -m 'Task-Ref: filesystem/platform/quota'
git commit --allow-empty -qm 'fix: skip unrelated task' -m $'Task-Ref: filesystem/platform/unused\nAgentflow: skip'
export GITHUB_REPOSITORY=acme/widget GITHUB_REPOSITORY_ID=100 GITHUB_EVENT_NAME=pull_request
export GITHUB_SERVER_URL=https://github.com GITHUB_RUN_ID=105 AGENTFLOW_DEFAULT_BRANCH=main GITHUB_EVENT_PATH="$temp/pr.json"
jq --arg base "$baseline" --arg head "$(git rev-parse HEAD)" \
  '.pull_request += {body:"Task-Ref: filesystem/platform/quota",title:"feat: implement quota"} |
   .pull_request.base.sha=$base | .pull_request.head.sha=$head' \
  "$root/internal/event/testdata/pr-merged.json" > "$GITHUB_EVENT_PATH"
agentflow lint-msg --github-event
for attempt in first replay; do
  agentflow transition --github-event --dry-run > "$temp/$attempt.json"
done
cmp "$temp/first.json" "$temp/replay.json"
jq -e '(.plan.transitions[0].operation_id | length)>0 and .effects[0].to=="closed" and .effects[0].applied==false' "$temp/first.json" >/dev/null
agentflow transition filesystem/platform/quota pr.merged --dry-run > "$temp/cli.json"
jq -e --slurpfile cli "$temp/cli.json" '.effects==[$cli[0]]' "$temp/first.json" >/dev/null
test "$(agentflow keys --shipped --range "$baseline..HEAD" --path cli)" = filesystem/platform/quota
test "$(agentflow keys --shipped --range "$baseline..HEAD")" = filesystem/platform/quota
export GITHUB_OUTPUT="$temp/ship-output" RANGE="$baseline..HEAD" SINCE=""
ship="$root/actions/ship/action.yml"
yq -r '.runs.steps[] | select(.id=="keys").run' "$ship" | bash -eu -o pipefail
grep -Fx 'keys=filesystem/platform/quota' "$GITHUB_OUTPUT"
export KEYS=filesystem/platform/quota PROVIDER=filesystem EVENT=deploy.production DRY=false FIX_VERSION=1.1.0
yq -r '.runs.steps[-1].run' "$ship" | bash -eu -o pipefail > "$temp/ship-plan"
sed -n '/^{/,$p' "$temp/ship-plan" | jq -e '.event=="deploy.production" and .to=="closed" and .applied==false' >/dev/null
git diff --exit-code -- .agentflow/tasks
"$root/bin/release-notes.sh" "$baseline..HEAD" "Canary release" filesystem | grep -Fx -- '- filesystem/platform/quota'

# Feed recorded action outputs into the production release matrix step.
agentflow release plan --json | jq -e '
  (map({key:.unit.name,value:.next}) | from_entries) == {"widget":"1.1.0","cli":"2.0.1"}' >/dev/null
workflow="$root/.github/workflows/semver-release.yml"
export OUTPUTS REGISTRY=ghcr.io/Fixture GITHUB_OUTPUT="$temp/output"
OUTPUTS=$(cat "$root/scripts/testdata/consumer/released.json")
yq -r '.jobs.release.steps[] | select(.id == "matrix").run' "$workflow" | bash -eu -o pipefail
sed 's/^matrix=//' "$GITHUB_OUTPUT" | jq -e '
  length==2 and .[0].name=="widget" and .[0].tag=="widget-v1.1.0" and
  .[1].path=="cli" and .[1].version=="2.0.1" and .[1].builds=="binary" and
  .[1].url=="https://example.invalid/releases/cli-v2.0.1" and .[1].image=="ghcr.io/fixture/cli"' >/dev/null
# The agentflow release workflow derives a version directly from Git history.
"$root/bin/next-version.sh" --path . --prefix v --branch main --default-branch main --env | grep -Fx 'next=1.1.0'
"$root/scripts/changelog.sh" --version 1.1.0 --from v1.0.0 --to HEAD --notes "$temp/agentflow-notes" >/dev/null
grep -F 'implement quota' "$temp/agentflow-notes"

git restore --source "$baseline" -- "$config" "$caller"
git diff --exit-code "$baseline" -- "$config" "$caller" .release-please-manifest.json release-please-config.json
test "$(agentflow keys --provider)" = jira
# Run the real warn-only wrapper twice, with an explicitly configured Jira site
# and a missing token. The provider must fail before issuing any request.
jq '.pull_request.title="feat(PLAT-12): implement quota"' "$GITHUB_EVENT_PATH" > "$temp/jira.json"
export GITHUB_EVENT_PATH="$temp/jira.json" PROVIDER=jira JIRA_BASE=https://jira.invalid JIRA_USER=fixture
yq -r '.jobs.validate.steps[-1].run' "$root/.github/workflows/jira-key.yml" > "$temp/validate.sh"
if WARN_ONLY=false bash -eu "$temp/validate.sh" > "$temp/strict" 2>&1; then
  echo "expected validation failure" >&2
  exit 1
fi
grep -F 'require JIRA_TOKEN' "$temp/strict"
WARN_ONLY=true bash -eu "$temp/validate.sh" > "$temp/warn" 2>&1
grep -F '::warning::' "$temp/warn"
if agentflow pin --check; then echo "rollback failed to restore the original pin" >&2; exit 1; fi
echo 'PASS: legacy caller and config restored, manifest preserved'
