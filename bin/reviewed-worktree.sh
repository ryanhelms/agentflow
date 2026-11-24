#!/usr/bin/env bash
# Fetch the verified caller commit into an isolated runner directory.
set -euo pipefail

revision=$(reviewed-revision.sh pr-base)
runner_root=$(cd "${RUNNER_TEMP:?}" && pwd -P)
workspace=$(mktemp -d "$runner_root/devflow-source.XXXXXXXX")
if [[ "$workspace" == *$'\r'* || "$workspace" == *$'\n'* ]]; then
  echo 'devflow: invalid runner source directory' >&2
  exit 1
fi
cd "$workspace"
git init --quiet
git remote add origin "${GITHUB_SERVER_URL:?}/${GITHUB_REPOSITORY:?}.git"
# Limit the credential helper to this fetch. GitHub CLI uses the step's token.
GIT_TERMINAL_PROMPT=0 git -c credential.helper= -c 'credential.helper=!gh auth git-credential' \
  fetch --quiet --no-tags --depth=1 origin "$revision"
if [[ $(git rev-parse --verify FETCH_HEAD) != "$revision" ]]; then
  echo 'devflow: fetched source did not match the reviewed revision' >&2
  exit 1
fi
git -c core.hooksPath=/dev/null checkout --quiet --detach "$revision"
printf '%s\n' "$workspace"
