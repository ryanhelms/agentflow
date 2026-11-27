#!/usr/bin/env bash
# refresh-acp-registry.sh COMMIT
#
# Replaces agentflow's pinned copy of the official ACP registry
# (https://github.com/agentclientprotocol/registry, Apache-2.0) with the files
# at COMMIT, a full 40-hex commit. Run by a maintainer, by hand, before a
# release; agentflow never fetches the registry at runtime. See the ADR,
# docs/knowledge/engineering/decisions/event-driven-platform.md, section
# "Phase 6 pinned ACP registry copy".
#
# Copies, verbatim: <id>/agent.json for every top-level directory that has one,
# quarantine.json, agent.schema.json and LICENSE. Never icons, workflows,
# snapshots or docs. Then writes SOURCE.json and runs the package's tests.
set -euo pipefail

REPO="agentclientprotocol/registry"
# The ACP schema the decoder follows. Change it only with the ADR.
SPEC_REPOSITORY="https://github.com/agentclientprotocol/agent-client-protocol"
SPEC_TAG="schema-v1.35.0"
SPEC_COMMIT="065b57951b6b2a91ac5eddd249e145d1f6d54545"

usage() {
	echo "usage: $0 COMMIT   (a full 40-hex commit of $REPO)" >&2
	exit 2
}

[ "$#" -eq 1 ] || usage
commit="$1"
if ! printf '%s' "$commit" | grep -Eq '^[0-9a-f]{40}$'; then
	echo "refresh-acp-registry: $commit is not a full 40-hex commit; a refresh is always a reviewed pin" >&2
	exit 2
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
dest="$root/internal/harness/acpregistry/v1"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fetch() { # url file
	curl -fsSL --proto '=https' --retry 3 -H 'Accept: application/vnd.github+json' -o "$2" "$1"
}

fetch "https://api.github.com/repos/$REPO/git/commits/$commit" "$tmp/commit.json"
committed_at="$(tr -d '\n' <"$tmp/commit.json" |
	sed -n 's/.*"committer": *{[^}]*"date": *"\([^"]*\)".*/\1/p')"
if [ -z "$committed_at" ]; then
	echo "refresh-acp-registry: no committer date for $commit" >&2
	exit 1
fi

fetch "https://api.github.com/repos/$REPO/git/trees/$commit?recursive=1" "$tmp/tree.json"
if tr -d '\n ' <"$tmp/tree.json" | grep -q '"truncated":true'; then
	echo "refresh-acp-registry: the tree listing is truncated" >&2
	exit 1
fi
tr ',' '\n' <"$tmp/tree.json" | sed -n 's/.*"path": *"\([^"]*\)".*/\1/p' >"$tmp/paths"

mkdir -p "$tmp/v1/agents"
count=0
while IFS= read -r path; do
	case "$path" in
	quarantine.json | agent.schema.json | LICENSE)
		fetch "https://raw.githubusercontent.com/$REPO/$commit/$path" "$tmp/v1/$path"
		;;
	*/agent.json)
		id="${path%/agent.json}"
		if ! printf '%s' "$id" | grep -Eq '^[a-z][a-z0-9-]*$'; then
			continue # nested, hidden or oddly named: not a top-level agent
		fi
		fetch "https://raw.githubusercontent.com/$REPO/$commit/$path" "$tmp/v1/agents/$id.json"
		count=$((count + 1))
		;;
	esac
done <"$tmp/paths"

for f in quarantine.json agent.schema.json LICENSE; do
	if [ ! -s "$tmp/v1/$f" ]; then
		echo "refresh-acp-registry: $f is missing at $commit" >&2
		exit 1
	fi
done
if [ "$count" -eq 0 ]; then
	echo "refresh-acp-registry: no agents at $commit" >&2
	exit 1
fi

cat >"$tmp/v1/SOURCE.json" <<EOF
{
  "repository": "https://github.com/$REPO",
  "commit": "$commit",
  "committed_at": "$committed_at",
  "agents": $count,
  "spec": {
    "repository": "$SPEC_REPOSITORY",
    "tag": "$SPEC_TAG",
    "commit": "$SPEC_COMMIT"
  }
}
EOF

rm -rf "$dest"
mkdir -p "$(dirname "$dest")"
mv "$tmp/v1" "$dest"
echo "refresh-acp-registry: $count agents at $commit ($committed_at)"

cd "$root"
go test ./internal/harness/acpregistry/
