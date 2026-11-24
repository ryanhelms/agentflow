#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
export HEAD_SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export BASE_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
export GITHUB_REPOSITORY=fixture/consumer GITHUB_REPOSITORY_ID=123 GITHUB_RUN_ID=98
export GITHUB_SERVER_URL=https://github.example.invalid RUNNER_TEMP="$fixture"
export GITHUB_EVENT_PATH="$fixture/event.json" GITHUB_EVENT_NAME=pull_request
export PATH="$fixture:$PATH"

cat > "$fixture/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == api ]]
case "$2" in
  repos/fixture/consumer) printf '%s\n' '{"id":123,"default_branch":"main"}' ;;
  repos/fixture/consumer/branches/*)
    [[ "$3" == --jq && "$4" == .commit.sha ]]
    printf '%s\n' "$HEAD_SHA"
    ;;
  repos/fixture/consumer/releases/tags/v1.0.0) printf '%s\n' '{"tag_name":"v1.0.0","draft":false}' ;;
  repos/fixture/consumer/commits/refs%2Ftags%2Fv1.0.0)
    [[ "$3" == --jq && "$4" == .sha ]]
    printf '%s\n' "$BASE_SHA"
    ;;
  "repos/fixture/consumer/compare/$BASE_SHA...$HEAD_SHA")
    status=${FIXTURE_COMPARE:-ahead}
    printf '{"status":"%s","merge_base_commit":{"sha":"%s"}}\n' "$status" "$BASE_SHA"
    ;;
  "repos/fixture/consumer/commits/$BASE_SHA") printf '%s\n' "$BASE_SHA" ;;
  "repos/fixture/consumer/commits/$HEAD_SHA") printf '%040d\n' 1 ;;
  repos/fixture/consumer/git/trees/*)
    case "${2##*/}" in
      "$(printf '%040d' 1)") entries=$(printf '[{"path":".devflow","sha":"%040d","type":"tree","mode":"040000"}]' 2) ;;
      "$(printf '%040d' 2)") entries=$(printf '[{"path":"config.yml","sha":"%040d","type":"blob","mode":"100644"},{"path":"providers","sha":"%040d","type":"tree","mode":"040000"}]' 4 3) ;;
      "$(printf '%040d' 3)") entries=$(printf '[{"path":"fixture.yaml","sha":"%040d","type":"blob","mode":"100644"},{"path":"link.yml","type":"blob","mode":"120000"},{"path":"readme.txt","type":"blob","mode":"100644"}]' 5) ;;
      *) exit 1 ;;
    esac
    printf '{"truncated":false,"tree":%s}\n' "$entries"
    ;;
  "repos/fixture/consumer/git/blobs/$(printf '%040d' 4)"|"repos/fixture/consumer/git/blobs/$(printf '%040d' 5)")
    printf '%s\n' '{"encoding":"base64","content":"e30K"}'
    ;;
  *) echo "unexpected fixture request: $2" >&2; exit 1 ;;
esac
SH
chmod 700 "$fixture/gh"

printf '{"pull_request":{"base":{"ref":"main","sha":"%s","repo":{"id":123}}}}\n' "$BASE_SHA" > "$GITHUB_EVENT_PATH"
revision="$root/bin/reviewed-revision.sh"
[[ $("$revision" default) == "$HEAD_SHA" ]]
[[ $("$revision" pr-base) == "$BASE_SHA" ]]
[[ $(GITHUB_EVENT_NAME=push "$revision" pr-base) == "$HEAD_SHA" ]]
[[ $("$revision" release v1.0.0) == "$BASE_SHA" ]]
if FIXTURE_COMPARE=diverged "$revision" release v1.0.0 > /dev/null 2> "$fixture/error"; then
  echo 'release revision outside default history was accepted' >&2; exit 1
fi
grep -q 'outside the approved branch history' "$fixture/error"
printf '{"pull_request":{"base":{"ref":"main","sha":"%s","repo":{"id":456}}}}\n' "$BASE_SHA" > "$GITHUB_EVENT_PATH"
if "$revision" pr-base > /dev/null 2> "$fixture/error"; then
  echo 'foreign pull request base was accepted' >&2; exit 1
fi
grep -q 'base repository did not match' "$fixture/error"

export PATH="$root/bin:$PATH"
config=$(reviewed-config.sh)
grep -Fx '{}' "$config/.devflow/config.yml" > /dev/null
grep -Fx '{}' "$config/.devflow/providers/fixture.yaml" > /dev/null
[[ ! -e "$config/.devflow/providers/link.yml" && ! -e "$config/.devflow/providers/readme.txt" ]]
[[ $(git -C "$config" remote get-url origin) == "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY.git" ]]


(
  export GITHUB_SERVER_URL="file://$fixture"
  remote="$fixture/$GITHUB_REPOSITORY.git"
  git init --quiet --initial-branch=main "$remote"
  cd "$remote"
  git config user.name 'Workflow Fixture'
  git config user.email 'workflow@example.invalid'
  git config core.hooksPath /dev/null
  mkdir -p .devflow/tasks
  printf '%s\n' 'reviewed task fixture' > .devflow/tasks/task.md
  git add .devflow/tasks/task.md
  git commit --quiet -m 'fixture base'
  export BASE_SHA
  BASE_SHA=$(git rev-parse HEAD)
  printf '%s\n' 'later task fixture' > .devflow/tasks/task.md
  git commit --quiet -am 'fixture branch advances'
  export HEAD_SHA
  HEAD_SHA=$(git rev-parse HEAD)
  printf '{"pull_request":{"base":{"ref":"main","sha":"%s","repo":{"id":123}}}}\n' "$BASE_SHA" > "$GITHUB_EVENT_PATH"
  source=$(reviewed-worktree.sh)
  [[ $(git -C "$source" rev-parse HEAD) == "$BASE_SHA" ]]
  grep -Fx 'reviewed task fixture' "$source/.devflow/tasks/task.md" > /dev/null
  if git -C "$source" config --local --get credential.helper; then
    echo 'the fetch credential helper was persisted' >&2; exit 1
  fi
)

artifacts="$fixture/artifacts"
mkdir -p "$artifacts/binaries"
printf '%s\n' 'fixture archive data' > "$artifacts/binaries/example.tar.gz"
guard="$root/bin/release-artifacts.sh"
export DEVFLOW_RELEASE_SHA="$BASE_SHA" TAG=v1.0.0
"$guard" record "$artifacts"
"$guard" verify "$artifacts"
touch "$artifacts/binaries/unexpected.txt"
if "$guard" verify "$artifacts" 2> "$fixture/error"; then
  echo 'unexpected artifact file was accepted' >&2; exit 1
fi
grep -q 'unexpected file in release artifacts' "$fixture/error"
rm "$artifacts/binaries/unexpected.txt"
touch "$artifacts/unexpected.txt"
if "$guard" verify "$artifacts" 2> "$fixture/error"; then
  echo 'unexpected artifact root entry was accepted' >&2; exit 1
fi
grep -q 'unexpected file in release artifacts' "$fixture/error"
rm "$artifacts/unexpected.txt"
if DEVFLOW_RELEASE_SHA="$HEAD_SHA" "$guard" verify "$artifacts" 2> "$fixture/error"; then
  echo 'artifact from a different revision was accepted' >&2; exit 1
fi
grep -q 'source or checksums did not match' "$fixture/error"
if GITHUB_RUN_ID=99 "$guard" verify "$artifacts" 2> "$fixture/error"; then
  echo 'artifact from a different run was accepted' >&2; exit 1
fi
printf '%s\n' 'changed fixture data' >> "$artifacts/binaries/example.tar.gz"
if "$guard" verify "$artifacts" 2> "$fixture/error"; then
  echo 'changed artifact was accepted' >&2; exit 1
fi
rm "$artifacts/binaries/example.tar.gz"
if "$guard" verify "$artifacts" 2> "$fixture/error"; then
  echo 'missing artifact was accepted' >&2; exit 1
fi
"$guard" record "$artifacts"
"$guard" verify "$artifacts"
printf '%s\n' 'PASS: reviewed revisions and release artifact source and hash validation'
