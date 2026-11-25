---
name: remove
description: Remove devflow from this repo and stop automatic init in this clone
argument-hint: "[--force] [--keep-releases]"
disable-model-invocation: true
---
Remove devflow from this repository. Run each `devflow` command below using
`sh "${CLAUDE_PLUGIN_ROOT}/bin/devflow"` with its arguments so a different
binary on the user's PATH cannot leave newer installation assets behind.

1. Run `devflow remove --dry-run $ARGUMENTS` and show me what would be removed, what would
   be edited (AGENTS.md, client configs) and what would be kept.
2. Show which release-please files the preview will keep or remove. For a repo without
   an installation receipt, check ownership and use `--keep-releases` to retain its
   prior release configuration. Release tags and GitHub Releases remain available.
3. Ask me to confirm. Only after a yes, run `devflow remove $ARGUMENTS` (without `--dry-run`).
4. Show `git status --short` and offer to commit the removal on a new branch as
   `chore: remove devflow` and open a PR. Mention that repo variables and secrets
   (`JIRA_*`, `DEVFLOW_*`) are left for an admin to delete.
