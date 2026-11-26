#!/usr/bin/env bash
# Exercise repository selection and first-run bootstrap without real credentials or releases.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd -P)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
scratch=$(cd "$scratch" && pwd -P)
mkdir "$scratch/cache"
ln -s "$scratch/cache" "$scratch/cache alias"
export XDG_CACHE_HOME="$scratch/cache alias" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export TMPDIR="$scratch/tmp" TEST_CALLS="$scratch/calls" TEST_GH="$scratch/gh-calls"
export TEST_BINARY="$scratch/agentflow" TEST_LAUNCHER="$root/templates/repo/.agentflow/bin/agentflow"
mkdir -p "$scratch/bin" "$TMPDIR"
export PATH="$scratch/bin:$PATH"
installer="$root/install.sh"
repo="$scratch/team's repo"
git init -q "$repo"
mkdir "$repo/subdir"
git -C "$repo" -c user.name=Fixture -c user.email=fixture@example.test commit -q --allow-empty -m base
linked="$scratch/linked checkout"
git -C "$repo" worktree add -q -b linked "$linked"

cat > "$TEST_BINARY" <<'BINARY'
#!/bin/sh
set -eu
printf '%s|%s\n' "$PWD" "$*" >> "$TEST_CALLS"
if [ -n "${TEST_STDIN:-}" ]; then
  IFS= read -r answer
  [ "$answer" = "$TEST_STDIN" ]
fi
exit "${TEST_BINARY_STATUS:-0}"
BINARY
chmod +x "$TEST_BINARY"

cat > "$scratch/bin/gh" <<'GITHUB'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$TEST_GH"
case "$1 $2" in
  'release view')
    printf '%s\n' "${TEST_TAG:-v1.2.3}"
    ;;
  'api --hostname')
    [[ "$*" == *'repos/ryanhelms/agentflow/contents/templates/repo/.agentflow/bin/agentflow?ref=v1.2.3' ]]
    if [ "${TEST_FAILURE:-}" = source ]; then cat "$TEST_BINARY"; exit 42; fi
    cat "$TEST_LAUNCHER"
    ;;
  'release download')
    shift 3
    dest=''; archive=''
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --dir) dest=$2 ;;
        --pattern) if [ "$2" != checksums.txt ]; then archive=$2; fi ;;
        --repo) [ "$2" = ryanhelms/agentflow ] ;;
        *) exit 2 ;;
      esac
      shift 2
    done
    [ -n "$dest" ] && [[ "$archive" == agentflow_1.2.3_*.tar.gz ]]
    tar -czf "$dest/$archive" -C "$(dirname "$TEST_BINARY")" agentflow
    if [ "${TEST_FAILURE:-}" = archive ]; then exit 43; fi
    if [ "${TEST_FAILURE:-}" = checksum ]; then
      printf '%064d  %s\n' 0 "$archive" > "$dest/checksums.txt"
    elif command -v sha256sum >/dev/null; then
      (cd "$dest" && sha256sum "$archive" > checksums.txt)
    else
      (cd "$dest" && shasum -a 256 "$archive" > checksums.txt)
    fi
    ;;
  'attestation verify')
    [[ "$*" == *'--signer-workflow ryanhelms/agentflow/.github/workflows/release.yml' ]]
    [ "${TEST_FAILURE:-}" != signer ]
    ;;
  *) exit 97 ;;
esac
GITHUB
chmod +x "$scratch/bin/gh"

expect_status() {
  local expected=$1 actual=0
  shift
  "$@" > "$scratch/stdout" 2> "$scratch/stderr" || actual=$?
  if [ "$actual" != "$expected" ]; then
    cat "$scratch/stderr" >&2
    echo "expected exit $expected, got $actual from $*" >&2
    exit 1
  fi
}

# Validate every candidate before contacting GitHub, even with inherited Git hook variables.
git init -q --bare "$scratch/bare.git"
for invalid in "" "$scratch/missing" "$scratch/bare.git" "$repo/.git" "$repo/.git/objects" "$scratch"; do
  GIT_DIR="$repo/.git" GIT_WORK_TREE="$repo" expect_status 1 "$installer" "$invalid" </dev/null
  grep -q 'cancelled' "$scratch/stderr"
done
expect_status 1 "$installer" "$scratch/missing" <<< ''
expect_status 2 "$installer" --repo
expect_status 2 "$installer" first second
[ ! -e "$TEST_GH" ]
[ ! -e "$TEST_CALLS" ]

# Failures can return a complete-looking script or archive. Neither may reach init.
TEST_TAG='main' expect_status 1 "$installer" "$repo"
TEST_FAILURE=source expect_status 42 "$installer" "$repo"
TEST_FAILURE=archive expect_status 43 "$installer" "$repo"
TEST_FAILURE=checksum expect_status 1 "$installer" "$repo"
TEST_FAILURE=signer expect_status 1 "$installer" "$repo"
[ ! -e "$TEST_CALLS" ]
[ ! -e "$XDG_CACHE_HOME/agentflow/current" ]

# Recover from an invalid argument, accept a nested path, preserve stdin for the binary.
printf '%s\n' "$scratch/missing-again" "$repo/subdir" 'left for agentflow' > "$scratch/input"
TEST_STDIN='left for agentflow' expect_status 0 "$installer" --repo "$scratch/missing" < "$scratch/input"
[ "$(cat "$TEST_CALLS")" = "$repo|init" ]
[ -x "$XDG_CACHE_HOME/agentflow/current/agentflow" ]
grep -q 'attestation verify' "$TEST_GH"
: > "$TEST_GH"
: > "$TEST_CALLS"

# The generated launcher also recognizes the pinned cached release through a symlinked cache root.
(cd "$repo" && expect_status 0 sh "$TEST_LAUNCHER" --bootstrap-release v1.2.3 version)
[ "$(cat "$TEST_CALLS")" = "$repo|version" ]
[ ! -s "$TEST_GH" ]
: > "$TEST_CALLS"

# A subsequent invocation can target a linked checkout or default to its current checkout.
(cd "$linked" && expect_status 0 "$installer" </dev/null)
[ "$(cat "$TEST_CALLS")" = "$linked|init" ]
: > "$TEST_CALLS"
TEST_BINARY_STATUS=75 expect_status 75 "$installer" "$repo" </dev/null
[ "$(cat "$TEST_CALLS")" = "$repo|init" ]
if grep -q 'release download' "$TEST_GH"; then echo 'Downloaded a warm cache again' >&2; exit 1; fi

# A partial install resumes; a complete project never executes its own launcher.
mkdir -p "$repo/.agentflow/bin" "$repo/.github/workflows"
touch "$repo/.agentflow/config.yml"
: > "$TEST_CALLS"
expect_status 0 "$installer" "$repo"
[ "$(cat "$TEST_CALLS")" = "$repo|init" ]
: > "$TEST_CALLS"
printf '#!/bin/sh\nexit 99\n' > "$repo/.agentflow/bin/agentflow"
chmod +x "$repo/.agentflow/bin/agentflow"
touch "$repo/.agentflow/bin/agentflow.cmd" "$repo/.agentflow/bin/agentflow.ps1" "$repo/.github/workflows/agentflow.yml"
expect_status 0 "$installer" "$repo"
[ "$(cat "$TEST_CALLS")" = "$repo|init" ]
if grep -q 'release download' "$TEST_GH"; then echo 'Downloaded a warm cache again' >&2; exit 1; fi

# A different active version must not initialize a project using legacy behavior.
mkdir "$XDG_CACHE_HOME/agentflow/0.0.1"
cp "$repo/.agentflow/bin/agentflow" "$XDG_CACHE_HOME/agentflow/0.0.1/agentflow"
ln -sfn "$XDG_CACHE_HOME/agentflow/0.0.1" "$XDG_CACHE_HOME/agentflow/current"
: > "$TEST_GH"
: > "$TEST_CALLS"
TEST_FAILURE=archive expect_status 43 "$installer" "$repo"
[ "$XDG_CACHE_HOME/agentflow/current" -ef "$XDG_CACHE_HOME/agentflow/0.0.1" ]
[ ! -s "$TEST_CALLS" ]
if grep -q 'attestation verify' "$TEST_GH"; then echo 'Verified an incomplete download' >&2; exit 1; fi
expect_status 0 "$installer" "$repo"
grep -q 'attestation verify' "$TEST_GH"
[ "$XDG_CACHE_HOME/agentflow/current" -ef "$XDG_CACHE_HOME/agentflow/1.2.3" ]
[ "$(cat "$TEST_CALLS")" = "$repo|init" ]

echo 'PASS: project selection, verified first run, failure propagation and repeat installation'
