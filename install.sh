#!/usr/bin/env bash
# Install Agentflow in the selected Git worktree via the official release launcher.
set -euo pipefail

usage() { echo 'Usage: install.sh [--repo] [PATH]' >&2; }
case ${1-} in
  --help|-h) usage; exit 0 ;;
  --repo|--) shift; if [ "$#" -ne 1 ]; then usage; exit 2; fi ;;
  -*) usage; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then usage; exit 2; fi
candidate=${1-.}

command -v git >/dev/null || { echo 'agentflow: install Git and try again' >&2; exit 1; }
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
  printf 'agentflow: %s is not a Git working directory.\n' "${candidate:-<empty path>}" >&2
  printf 'Enter the path to an existing checkout (blank to cancel): ' >&2
  if ! IFS= read -r candidate || [ -z "$candidate" ]; then
    echo 'agentflow: installation cancelled' >&2
    exit 1
  fi
done
cd -- "$root"

markers=(.agentflow/config.yml .agentflow/bin/agentflow .agentflow/bin/agentflow.cmd .agentflow/bin/agentflow.ps1 .github/workflows/agentflow.yml)
for file in "${markers[@]}"; do
  if [ -L "$file" ] || { [ -e "$file" ] && [ ! -f "$file" ]; }; then
    printf 'agentflow: expected a regular project file at %s\n' "$file" >&2
    exit 1
  fi
done
printf 'Initializing Agentflow in %s.\n' "$root"
command -v gh >/dev/null || { echo 'agentflow: resolving the latest release requires the GitHub CLI (gh) signed in to github.com' >&2; exit 1; }
tag=$(gh release view --repo ryanhelms/agentflow --json tagName -q .tagName)
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'agentflow: expected a stable Agentflow release tag, got %s\n' "$tag" >&2
  exit 1
fi

# >>> agentflow cache root (the same block is in every script that finds the binary)
# A repository's environment (a client's per-project settings, direnv) can set HOME or
# XDG_CACHE_HOME, so the install cache is never taken from inside a git working tree or
# git directory, unless that tree's root is the account's own home (a dotfiles
# repository). Links are followed first. Only the file system is read, never git, so
# GIT_DIR and the other git variables cannot change the answer. Messages go to stderr.
af_account_home() { # the account's home from the user database, never $HOME
  af_u=$(id -un 2>/dev/null) || return 1
  case $af_u in '' | -* | *[!A-Za-z0-9._-]*) return 1 ;; esac
  af_h=
  eval "af_h=~$af_u"
  case $af_h in /*) printf '%s\n' "$af_h" ;; *) return 1 ;; esac
}
af_physical() { # the absolute, physical form of $1, which need not exist
  case $1 in /*) ;; *) return 1 ;; esac
  case $1/ in */./* | */../*) return 1 ;; esac
  af_p=$1 af_t=
  while [ ! -d "$af_p" ]; do
    if [ -e "$af_p" ]; then return 1; fi
    af_b=${af_p##*/}
    af_p=${af_p%/*}
    if [ -n "$af_b" ]; then af_t=/$af_b$af_t; fi
    if [ -z "$af_p" ]; then af_p=/; fi
  done
  af_p=$(cd -P -- "$af_p" 2>/dev/null && pwd -P) || return 1
  af_p=${af_p%/}$af_t
  printf '%s\n' "${af_p:-/}"
}
af_repo() { # the nearest git working tree or git directory at or above $1 (physical)
  af_d=$1
  while :; do
    if [ -d "$af_d" ] && [ ! -x "$af_d" ]; then return 2; fi
    if [ -e "$af_d/.git" ] || [ -L "$af_d/.git" ] || { [ -f "$af_d/HEAD" ] &&
      [ ! -L "$af_d/HEAD" ] && [ -d "$af_d/objects" ] && [ -d "$af_d/refs" ]; }; then
      printf '%s\n' "$af_d"
      return 0
    fi
    if [ "$af_d" = / ]; then return 1; fi
    af_d=${af_d%/*}
    if [ -z "$af_d" ]; then af_d=/; fi
  done
}
af_inside() { # succeeds, with the reason in af_why, when $1 is inside a repository
  af_r=$(af_physical "$1") || { af_why='cannot be checked (its path is not absolute and physical)'; return 0; }
  af_s=0
  af_root=$(af_repo "$af_r") || af_s=$?
  case $af_s in
    0) ;;
    1) return 1 ;;
    *) af_why='cannot be checked (a folder above it cannot be read)'; return 0 ;;
  esac
  if [ -n "$2" ] && [ "$af_root" = "$(af_physical "$2" || :)" ]; then return 1; fi
  af_why="is inside the repository $af_root"
}
af_cache_root() { # prints the install cache root: ${XDG_CACHE_HOME:-$HOME/.cache}/agentflow
  af_src=XDG_CACHE_HOME af_c=${XDG_CACHE_HOME:-}
  if [ -z "$af_c" ]; then af_src=HOME af_c=${HOME:-}; fi
  if [ -n "$af_c" ]; then
    if [ "$af_src" = HOME ]; then af_c=$af_c/.cache; fi
    af_c=$af_c/agentflow
  fi
  af_a=$(af_account_home) || af_a=
  af_own=
  if [ -n "$af_a" ]; then af_own=$af_a/.cache/agentflow; fi
  if [ -z "$af_c" ] && [ -n "$af_own" ]; then
    printf '%s\n' "$af_own"
    return 0
  elif [ -z "$af_c" ]; then
    af_why='is not set'
  elif [ -n "$af_own" ] && [ "$(af_physical "$af_c" || :)" = "$(af_physical "$af_own" || echo -)" ]; then
    printf '%s\n' "$af_c"
    return 0
  elif ! af_inside "$af_c" "$af_a"; then
    printf '%s\n' "$af_c"
    return 0
  fi
  if [ -z "$af_own" ]; then
    printf 'agentflow: not using the cache %s (from %s): it %s, and the account home cannot be found to use instead; set %s to a folder outside every repository\n' "$af_c" "$af_src" "$af_why" "$af_src" >&2
    return 1
  fi
  printf 'agentflow: ignoring the cache %s (from %s): it %s, so a repository could supply the agentflow binary. Using %s instead.\n' "$af_c" "$af_src" "$af_why" "$af_own" >&2
  printf '%s\n' "$af_own"
}
af_installed() { # prints $1/current/agentflow when it is executable and outside every repository
  if [ ! -x "$1/current/agentflow" ]; then return 1; fi
  if af_inside "$1/current" "$(af_account_home || :)"; then
    printf 'agentflow: not running %s: it %s\n' "$1/current/agentflow" "$af_why" >&2
    return 1
  fi
  printf '%s\n' "$1/current/agentflow"
}
# <<< agentflow cache root
home=$(af_cache_root) || exit 1
if bin=$(af_installed "$home") && [ "$home/current" -ef "$home/${tag#v}" ]; then
  exec "$bin" init
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# Fetch atomically from our release source rather than running code from the target checkout.
gh api --hostname github.com -H 'Accept: application/vnd.github.raw+json' \
  "repos/ryanhelms/agentflow/contents/templates/repo/.agentflow/bin/agentflow?ref=$tag" > "$tmp/launcher"
if [ ! -s "$tmp/launcher" ]; then echo 'agentflow: GitHub returned an empty launcher' >&2; exit 1; fi
sh "$tmp/launcher" --bootstrap-release "$tag" init
