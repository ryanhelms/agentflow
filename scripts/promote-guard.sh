#!/usr/bin/env bash
# Guards for .github/workflows/auto-promote.yml, kept here so they can be tested
# (scripts/test-promote-guard.sh).
#
#   promote-guard.sh breaking RANGE
#     Lists every commit in RANGE whose subject has "!" after the type or scope, or whose
#     body has a "BREAKING CHANGE:" or "BREAKING-CHANGE:" footer. Exits 1 if there is one.
#
#   promote-guard.sh main-extra SHA [MAIN]
#     Lists every commit on MAIN (default origin/main) that SHA does not contain, other
#     than the commits auto-promotion expects there:
#       - release commits: "chore(release): vX.Y.Z" by github-actions[bot] (release.yml)
#       - earlier promotions: merge commits "chore(release): promote develop to main (...)"
#         whose merged parents SHA already contains
#     Exits 1 if there is any other commit.
set -euo pipefail

usage() {
  echo "usage: $0 breaking RANGE | main-extra SHA [MAIN]" >&2
  exit 2
}

release_re='^chore\(release\): v[0-9]+\.[0-9]+\.[0-9]+$'
promote_re='^chore\(release\): promote develop to main \(.*\)$'
breaking_subject_re='^[A-Za-z]+(\([^)]*\))?!:'
breaking_footer_re='^BREAKING[ -]CHANGE:'

breaking() {
  local range=$1 found=0 sha subject body
  while IFS=$'\x1f' read -r -d $'\x1e' sha subject body; do
    sha=${sha#$'\n'}
    [ -n "$sha" ] || continue
    if [[ "$subject" =~ $breaking_subject_re ]] || grep -Eq "$breaking_footer_re" <<<"$body"; then
      printf '%s %s\n' "${sha:0:12}" "$subject"
      found=1
    fi
  done < <(git log --format='%H%x1f%s%x1f%b%x1e' "$range")
  [ "$found" = 0 ]
}

main_extra() {
  local sha=$1 main=${2:-origin/main} found=0 commit parents subject author p ok
  while read -r commit parents; do
    [ -n "$commit" ] || continue
    subject=$(git log -1 --format=%s "$commit")
    author=$(git log -1 --format=%an "$commit")
    ok=0
    # shellcheck disable=SC2086 # parents is a space-separated list
    set -- $parents
    if [ "$#" = 1 ] && [[ "$subject" =~ $release_re ]] && [ "$author" = "github-actions[bot]" ]; then
      ok=1
    elif [ "$#" -ge 2 ] && [[ "$subject" =~ $promote_re ]]; then
      ok=1
      shift
      for p in "$@"; do
        git merge-base --is-ancestor "$p" "$sha" || ok=0
      done
    fi
    if [ "$ok" = 0 ]; then
      printf '%s %s (%s)\n' "${commit:0:12}" "$subject" "$author"
      found=1
    fi
  done < <(git rev-list --parents "$sha..$main")
  [ "$found" = 0 ]
}

[ "$#" -ge 2 ] || usage
cmd=$1
shift
case "$cmd" in
  breaking) [ "$#" = 1 ] || usage; breaking "$1" ;;
  main-extra) [ "$#" -le 2 ] || usage; main_extra "$@" ;;
  *) usage ;;
esac
