#!/usr/bin/env bash
# Every CLAUDE.md in the repo must contain exactly "@AGENTS.md" and sit next to an
# AGENTS.md. Agent instructions live in AGENTS.md; CLAUDE.md is only a pointer.
set -euo pipefail
cd "$(dirname "$0")/.."

status=0
found=0
while IFS= read -r -d '' f; do
  found=1
  dir=$(dirname "$f")
  if [ "$(cat "$f")" != "@AGENTS.md" ]; then
    echo "✖ $f must contain exactly '@AGENTS.md'. Put instructions in $dir/AGENTS.md instead." >&2
    status=1
  fi
  if [ ! -f "$dir/AGENTS.md" ]; then
    echo "✖ $f points at $dir/AGENTS.md, which does not exist." >&2
    status=1
  fi
done < <(git ls-files -z --cached --others --exclude-standard -- 'CLAUDE.md' '**/CLAUDE.md' ':!:templates/**' ':!:adapters/**')

if [ "$found" = 0 ]; then
  echo "✖ no CLAUDE.md found at the repo root" >&2
  status=1
fi
[ "$status" = 0 ] && echo "✓ CLAUDE.md files point at AGENTS.md"
exit "$status"
