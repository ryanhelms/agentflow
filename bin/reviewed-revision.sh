#!/usr/bin/env bash
# Resolve the caller repository's reviewed source through GitHub before checkout.
set -euo pipefail

fail() { printf 'agentflow: %s\n' "$*" >&2; exit 1; }
uri() { jq -rn --arg value "$1" '$value | @uri'; }
is_sha() { [[ "$1" =~ ^[a-f0-9]{40}$ ]]; }

mode=${1:-default}
repo="repos/${GITHUB_REPOSITORY:?}"
repo_id=${GITHUB_REPOSITORY_ID:?}
repository=$(gh api "$repo")
[[ $(jq -r '.id' <<<"$repository") == "$repo_id" ]] || fail 'caller repository ID did not match'
default_branch=$(jq -er '.default_branch | select(type == "string" and length > 0)' <<<"$repository")

branch_head() {
  local sha
  sha=$(gh api "$repo/branches/$(uri "$1")" --jq .commit.sha)
  is_sha "$sha" || fail 'GitHub returned an invalid branch revision'
  printf '%s\n' "$sha"
}

verify_ancestor() {
  local ancestor=$1 head=$2 comparison
  if ! is_sha "$ancestor" || ! is_sha "$head"; then
    fail 'expected full commit SHAs'
  fi
  if [[ "$ancestor" != "$head" ]]; then
    comparison=$(gh api "$repo/compare/$ancestor...$head")
    jq -e --arg expected "$ancestor" '.status == "ahead" and .merge_base_commit.sha == $expected' \
      <<<"$comparison" >/dev/null || fail 'revision is outside the approved branch history'
  fi
  gh api "$repo/commits/$ancestor" --jq .sha
}

case "$mode" in
  default)
    sha=$(branch_head "$default_branch")
    ;;
  pr-base)
    if [[ "${GITHUB_EVENT_NAME:-}" == pull_request || "${GITHUB_EVENT_NAME:-}" == pull_request_target ]]; then
      base=$(jq -ce --arg id "$repo_id" '.pull_request.base | select((.repo.id | tostring) == $id)' "$GITHUB_EVENT_PATH") \
        || fail 'pull request base repository did not match the caller'
      branch=$(jq -er '.ref | select(type == "string" and length > 0)' <<<"$base")
      expected=$(jq -er '.sha' <<<"$base")
      sha=$(verify_ancestor "$expected" "$(branch_head "$branch")")
      [[ "$sha" == "$expected" ]] || fail 'GitHub resolved a different pull request base revision'
    else
      sha=$(branch_head "$default_branch")
    fi
    ;;
  release)
    tag=${2:?expected a release tag}
    [[ "$tag" =~ ^[[:alnum:]_][[:alnum:]_./-]*$ ]] || fail 'invalid release tag'
    release=$(gh api "$repo/releases/tags/$(uri "$tag")")
    jq -e --arg tag "$tag" '.tag_name == $tag and .draft == false' <<<"$release" >/dev/null \
      || fail 'tag does not identify a published release'
    expected=$(gh api "$repo/commits/$(uri "refs/tags/$tag")" --jq .sha)
    sha=$(verify_ancestor "$expected" "$(branch_head "$default_branch")")
    [[ "$sha" == "$expected" ]] || fail 'GitHub resolved a different release revision'
    ;;
  *) fail 'unknown revision source' ;;
esac

is_sha "$sha" || fail 'GitHub returned an invalid source revision'
printf '%s\n' "$sha"
