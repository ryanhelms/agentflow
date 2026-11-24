#!/usr/bin/env bash
# Bind release artifacts to the verified source and workflow run before publication.
set -euo pipefail

mode=${1:?expected record or verify}
cd "${2:?expected an artifact directory}"
revision=${AGENTFLOW_RELEASE_SHA:?}
[[ "$revision" =~ ^[a-f0-9]{40}$ ]] || exit 1
context=$(jq -cnS \
  --arg repository "${GITHUB_REPOSITORY_ID:?}" --arg run "${GITHUB_RUN_ID:?}" \
  --arg tag "${TAG:?}" --arg revision "$revision" \
  '{repository: $repository, run: $run, tag: $tag, revision: $revision}')

shopt -s nullglob dotglob
export LC_ALL=C
for entry in *; do
  case "$entry" in
    binaries|charts|libraries) [[ -d "$entry" && ! -L "$entry" ]] || exit 1 ;;
    image.tar|source.json|checksums.sha256) [[ -f "$entry" && ! -L "$entry" ]] || exit 1 ;;
    *) printf 'agentflow: unexpected file in release artifacts\n' >&2; exit 1 ;;
  esac
done
files=(binaries/* charts/* libraries/*)
if [[ -e image.tar || -L image.tar ]]; then
  files+=(image.tar)
fi
checksums=''
if ((${#files[@]})); then
  for file in "${files[@]}"; do
    case "$file" in
      binaries/*.tar.gz|binaries/*.zip|binaries/checksums.txt|image.tar|charts/*.tgz|libraries/*.tgz|libraries/*.tar.gz|libraries/*.whl) ;;
      *) printf 'agentflow: unexpected file in release artifacts\n' >&2; exit 1 ;;
    esac
    [[ -f "$file" && ! -L "$file" ]] || exit 1
  done
  checksums=$(sha256sum -- "${files[@]}")
fi

case "$mode" in
  record)
    printf '%s\n' "$context" > source.json
    printf '%s\n' "$checksums" > checksums.sha256
    ;;
  verify)
    if [[ ! -f source.json || -L source.json || ! -f checksums.sha256 || -L checksums.sha256 ]] ||
       [[ $(cat source.json) != "$context" || $(cat checksums.sha256) != "$checksums" ]]; then
      printf 'agentflow: release artifact source or checksums did not match\n' >&2
      exit 1
    fi
    ;;
  *) exit 1 ;;
esac
