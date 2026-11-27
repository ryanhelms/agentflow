#!/usr/bin/env bash
# Rebuild docs/blueprint.html (a standalone page) from the published artifact source
# docs/artifact/agentflow-delivery-loop.html, which has no <html>/<head>/<body> skeleton.
set -euo pipefail
cd "$(dirname "$0")/.."
src=docs/artifact/agentflow-delivery-loop.html
out=docs/blueprint.html

split=$(grep -n '^</style>' "$src" | head -n1 | cut -d: -f1)
{
  cat <<'HEAD'
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<style>:root{color-scheme:light;padding-top:env(safe-area-inset-top,0px);padding-bottom:env(safe-area-inset-bottom,0px)}body{margin:0;font:14px system-ui,sans-serif}img{max-width:100%}[hidden]{display:none!important}</style>
HEAD
  head -n "$split" "$src"
  printf '</head>\n<body>\n'
  tail -n +"$((split + 1))" "$src"
  printf '</body>\n</html>\n'
} >"$out"
echo "wrote $out"
