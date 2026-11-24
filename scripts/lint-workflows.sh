#!/usr/bin/env bash
# Validate GitHub Cloud's same-repository setup reference with actionlint versions
# that do not yet parse $/. The local lint reference resolves the real action
# metadata in this tree. Workflow files on disk retain same-commit semantics.
set -euo pipefail
cd "$(dirname "$0")/.."
lint=${1:-actionlint}
test -f actions/setup/action.yml
ordinary=()
for workflow in .github/workflows/*.yml .github/workflows/*.yaml; do
  [[ -f "$workflow" ]] || continue
  if grep -Eq '^[[:space:]]*-[[:space:]]+uses:[[:space:]]+\$/actions/setup([[:space:]]*(#.*)?)?$' "$workflow"; then
    sed -E 's|^([[:space:]]*-[[:space:]]+uses:[[:space:]]+)\$/actions/setup([[:space:]]*(#.*)?)$|\1./actions/setup\2|' "$workflow" \
      | "$lint" -stdin-filename "$workflow" -
  else
    ordinary+=("$workflow")
  fi
done
if ((${#ordinary[@]})); then
  "$lint" "${ordinary[@]}"
fi
