#!/usr/bin/env bash
# Regenerate the files plugins/design-system copies from website/vendor/:
#   vendor/*.tgz                           the three design packages, byte for byte
#   skills/agentflow-design/reference/     tokens.css, base.css, the patterns stylesheet
#                                          and every .d.ts, at their package paths
#   the "version" of the four plugin manifests (the components package version)
#   the tarball names in the skill's install command
#
# Run it after changing a tarball in website/vendor/. go test ./internal/setup/ fails
# while the copies are stale.
set -euo pipefail
cd "$(dirname "$0")/.."

src=website/vendor
plugin=plugins/design-system
reference=$plugin/skills/agentflow-design/reference

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# one_tarball NAME: print the only website/vendor tarball of that package.
one_tarball() {
  local found=0 match="" f
  for f in "$src/agentflow-design-$1-"*.tgz; do
    [ -f "$f" ] || continue
    found=$((found + 1))
    match=$f
  done
  if [ "$found" != 1 ]; then
    echo "✖ expected one $src/agentflow-design-$1-*.tgz, found $found" >&2
    return 1
  fi
  printf '%s\n' "$match"
}

# copy_matching PACKAGE PATTERN: copy the package files whose name matches into the
# reference directory, keeping their path below package/.
copy_matching() {
  local from="$work/$1/package" f rel
  while IFS= read -r f; do
    rel=${f#"$from"/}
    mkdir -p "$reference/$1/$(dirname "$rel")"
    cp "$f" "$reference/$1/$rel"
  done < <(find "$from" -type f -name "$2" | LC_ALL=C sort)
}

# Check and unpack every input before removing anything, so a failed run leaves
# the plugin as it was.
tarballs=()
for package in tokens components patterns; do
  tarball=$(one_tarball "$package")
  tarballs+=("$tarball")
  mkdir -p "$work/$package"
  tar -xzf "$tarball" -C "$work/$package"
done

rm -rf "$plugin/vendor" "$reference"
mkdir -p "$plugin/vendor" "$reference"
cp "${tarballs[@]}" "$plugin/vendor/"

copy_matching tokens 'tokens.css'
copy_matching components '*.d.ts'
copy_matching components 'base.css'
copy_matching patterns '*.d.ts'
copy_matching patterns 'styles.css'

version=$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$work/components/package/package.json" | head -n 1)
if [ -z "$version" ]; then
  echo "✖ no version in the components package.json" >&2
  exit 1
fi

for manifest in \
  "$plugin/.claude-plugin/plugin.json" \
  "$plugin/.codex-plugin/plugin.json" \
  "$plugin/.cursor-plugin/plugin.json" \
  "$plugin/plugin.json"; do
  sed -E "s/^(  \"version\": *\")[^\"]*\"/\\1$version\"/" "$manifest" > "$work/manifest.json"
  cat "$work/manifest.json" > "$manifest"
done

# The skill's install command names each tarball with its version.
skill=$plugin/skills/agentflow-design/SKILL.md
for tarball in "${tarballs[@]}"; do
  name=${tarball##*/}
  sed -E "s/${name%-*}-[0-9]+(\.[0-9]+)*\.tgz/$name/g" "$skill" > "$work/skill.md"
  cat "$work/skill.md" > "$skill"
done

echo "✓ $plugin copies $src at design components $version"
