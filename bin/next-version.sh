#!/usr/bin/env bash
# Print the semantic version a build of HEAD gets for one deployable unit.
# Mirrors `devflow release version` (internal/release) so CI doesn't need the binary.
#
#   next-version.sh --path services/api --prefix api-v [--strategy trunk|gitflow] \
#                   [--default-branch main] [--branch NAME]
#
# Default branch: the next release version (X.Y.Z), from Conventional Commits that
# touch the unit since its last tag. Other branches: X.Y.Z-<id>.<n>+<sha>, where id is
# alpha (develop, gitflow), rc (release/*), hotfix (hotfix/*) or pr (anything else).
# Prints KEY=VALUE lines with --env (version, next, current, bump, last_tag, kind).
set -euo pipefail

path=.
prefix=v
strategy=trunk
default_branch=""
branch=""
env_out=false
while [ $# -gt 0 ]; do
  case "$1" in
    --path) path=$2; shift 2 ;;
    --prefix) prefix=$2; shift 2 ;;
    --strategy) strategy=$2; shift 2 ;;
    --default-branch) default_branch=$2; shift 2 ;;
    --branch) branch=$2; shift 2 ;;
    --env) env_out=true; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ -z "$default_branch" ]; then
  default_branch=$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##' || true)
  default_branch=${default_branch:-main}
fi
if [ -z "$branch" ]; then
  branch=${GITHUB_HEAD_REF:-${GITHUB_REF_NAME:-$(git symbolic-ref --quiet --short HEAD 2>/dev/null || echo HEAD)}}
fi

last_tag=$(git describe --tags --abbrev=0 --match "${prefix}[0-9]*" HEAD 2>/dev/null || true)
current=0.0.0
if [ -n "$last_tag" ]; then
  current=${last_tag#"$prefix"}
  if ! [[ "$current" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then current=0.0.0; fi
fi
IFS=. read -r major minor patch <<<"$current"

range=HEAD
[ -n "$last_tag" ] && range="$last_tag..HEAD"
pathspec=()
[ "$path" != "." ] && pathspec=(-- "$path")

bump=none
commits=0
while IFS= read -r -d '' msg; do
  msg=${msg#$'\n'}
  [ -n "${msg//[[:space:]]/}" ] || continue
  commits=$((commits + 1))
  subject=${msg%%$'\n'*}
  if [[ "$subject" =~ ^[a-z]+(\([^\)]*\))?!: ]] || [[ "$msg" == *$'\nBREAKING CHANGE:'* ]] || [[ "$msg" == *$'\nBREAKING-CHANGE:'* ]]; then
    bump=major
  elif [[ "$subject" =~ ^feat(\([^\)]*\))?: ]]; then
    [ "$bump" = major ] || bump=minor
  elif [[ "$subject" =~ ^(fix|perf)(\([^\)]*\))?: ]]; then
    [ "$bump" = major ] || [ "$bump" = minor ] || bump="patch"
  fi
done < <(git log --format='%B%x00' "$range" "${pathspec[@]}")

case "$bump" in
  major)
    if [ "$major" -eq 0 ]; then minor=$((minor + 1)); patch=0; else major=$((major + 1)); minor=0; patch=0; fi ;;
  minor) minor=$((minor + 1)); patch=0 ;;
  patch) patch=$((patch + 1)) ;;
  none)
    if [ -z "$last_tag" ] && [ "$commits" -gt 0 ]; then major=0; minor=1; patch=0; fi ;;
esac
next="$major.$minor.$patch"

kind=feature
if [ "$branch" = "$default_branch" ]; then kind=default
elif [ "$strategy" = gitflow ] && [ "$branch" = develop ]; then kind=develop
elif [[ "$branch" == release/* ]]; then kind=release
elif [[ "$branch" == hotfix/* ]]; then kind=hotfix
fi
case "$kind" in
  default) version=$next ;;
  *)
    case "$kind" in develop) id=alpha ;; release) id=rc ;; hotfix) id=hotfix ;; *) id="pr" ;; esac
    sha=$(git rev-parse --short=7 HEAD)
    version="$next-$id.$commits+$sha" ;;
esac

if $env_out; then
  printf 'version=%s\nnext=%s\ncurrent=%s\nbump=%s\nlast_tag=%s\nkind=%s\n' \
    "$version" "$next" "$current" "$bump" "$last_tag" "$kind"
else
  echo "$version"
fi
