#!/usr/bin/env bash
# Load only provider configuration from the caller's reviewed default branch.
set -euo pipefail
umask 077

repo="repos/${GITHUB_REPOSITORY:?}"
revision=$(reviewed-revision.sh default)
root=$(gh api "$repo/commits/$revision" --jq .commit.tree.sha)

read_tree() {
  [[ "$1" =~ ^[a-f0-9]{40}$ ]]
  gh api "$repo/git/trees/$1" | jq -ce 'select(.truncated == false) | .tree'
}
entry_sha() {
  jq -r --arg name "$1" --arg type "$2" \
    '.[] | select(.path == $name and .type == $type and (.mode != "120000")) | .sha'
}
write_config() {
  local sha=$1 path=$2
  [[ "$sha" =~ ^[a-f0-9]{40}$ ]]
  gh api "$repo/git/blobs/$sha" | jq -er 'select(.encoding == "base64") | .content' \
    | base64 --decode > "$path"
}

root_tree=$(read_tree "$root")
cd "${RUNNER_TEMP:?}"
workspace=$(mktemp -d "$PWD/agentflow-config.XXXXXXXX")
cd "$workspace"
git init --quiet
git remote add origin "${GITHUB_SERVER_URL:?}/$GITHUB_REPOSITORY.git"
# Agentflow resolves precedence between its config and the reviewed legacy data.
for directory in .agentflow .devflow; do
  project=$(entry_sha "$directory" tree <<<"$root_tree")
  [[ -n "$project" ]] || continue
  tree=$(read_tree "$project")
  mkdir -p "$directory/providers"
  config=$(entry_sha config.yml blob <<<"$tree")
  if [[ -n "$config" ]]; then
    write_config "$config" "$directory/config.yml"
  fi
  providers=$(entry_sha providers tree <<<"$tree")
  if [[ -n "$providers" ]]; then
    files=$(read_tree "$providers" | jq -r '.[] | select(.type == "blob" and (.mode != "120000") and (.path | test("^[^/[:cntrl:]]+\\.ya?ml$"))) | [.sha, .path] | @tsv')
    while IFS=$'\t' read -r sha file; do
      [[ -n "$sha" ]] || continue
      write_config "$sha" "$directory/providers/$file"
    done <<<"$files"
  fi
done
printf '%s\n' "$workspace"
