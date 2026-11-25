#!/usr/bin/env bash
# Install Devflow in the selected Git worktree via the official release launcher.
set -euo pipefail

usage() { echo 'Usage: install.sh [--repo] [PATH]' >&2; }
case ${1-} in
  --help|-h) usage; exit 0 ;;
  --repo|--) shift; if [ "$#" -ne 1 ]; then usage; exit 2; fi ;;
  -*) usage; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then usage; exit 2; fi
candidate=${1-.}

command -v git >/dev/null || { echo 'devflow: install Git and try again' >&2; exit 1; }
# Git hooks can export the invoking repository; PATH must select the target.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE
while :; do
  case "$candidate" in
    '~') candidate=$HOME ;;
    \~/*) candidate=$HOME/${candidate#\~/} ;;
  esac
  if [ -n "$candidate" ] && [ -d "$candidate" ] &&
     root=$(git -C "$candidate" rev-parse --show-toplevel 2>/dev/null); then
    break
  fi
  printf 'devflow: %s is not a Git working directory.\n' "${candidate:-<empty path>}" >&2
  printf 'Enter the path to an existing checkout (blank to cancel): ' >&2
  if ! IFS= read -r candidate || [ -z "$candidate" ]; then
    echo 'devflow: installation cancelled' >&2
    exit 1
  fi
done
cd -- "$root"

markers=(.devflow/config.yml .devflow/bin/devflow .devflow/bin/devflow.cmd .devflow/bin/devflow.ps1 .github/workflows/devflow.yml)
for file in "${markers[@]}"; do
  if [ -L "$file" ] || { [ -e "$file" ] && [ ! -f "$file" ]; }; then
    printf 'devflow: expected a regular project file at %s\n' "$file" >&2
    exit 1
  fi
done
printf 'Initializing Devflow in %s.\n' "$root"
command -v gh >/dev/null || { echo 'devflow: resolving the latest release requires the GitHub CLI (gh) signed in to github.com' >&2; exit 1; }
tag=$(gh release view --repo nscaledev/devflow --json tagName -q .tagName)
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'devflow: expected a stable Devflow release tag, got %s\n' "$tag" >&2
  exit 1
fi

cache=${XDG_CACHE_HOME:-$HOME/.cache}
case "$cache" in /*) ;; *) echo 'devflow: the Devflow cache must use an absolute path' >&2; exit 1 ;; esac
if [ -x "$cache/devflow/current/devflow" ] &&
   [ "$cache/devflow/current" -ef "$cache/devflow/${tag#v}" ]; then
  exec "$cache/devflow/current/devflow" init
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# Fetch atomically from our release source rather than running code from the target checkout.
gh api --hostname github.com -H 'Accept: application/vnd.github.raw+json' \
  "repos/nscaledev/devflow/contents/templates/repo/.devflow/bin/devflow?ref=$tag" > "$tmp/launcher"
if [ ! -s "$tmp/launcher" ]; then echo 'devflow: GitHub returned an empty launcher' >&2; exit 1; fi
sh "$tmp/launcher" --bootstrap-release "$tag" init
