---
name: remove
description: Remove devflow from this repo and stop automatic init in this clone
argument-hint: "[--force] [--keep-releases]"
disable-model-invocation: true
---
Remove devflow from this repository. The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

1. Run `devflow remove --dry-run $ARGUMENTS` and show me what would be removed, what would
   be edited (AGENTS.md, client configs) and what would be kept.
2. Point out `release-please-config.json` and `.release-please-manifest.json`: they are
   removed unless I pass `--keep-releases`. Release tags and GitHub Releases are never touched.
3. Ask me to confirm. Only after a yes, run `devflow remove $ARGUMENTS` (without `--dry-run`).
4. Show `git status --short` and offer to commit the removal on a new branch as
   `chore: remove devflow` and open a PR. Mention that repo variables and secrets
   (`JIRA_*`, `DEVFLOW_*`) are left for an admin to delete.
