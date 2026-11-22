---
name: release
description: Plan, inspect or explicitly publish releases for this repo's deployable units
argument-hint: "[plan|preview|init|sync|version --unit U|get TAG|publish TAG --revision SHA]"
---
The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

- No argument or `plan`: call devflow_release_plan and show each unit's current and next
  version and why (feat, fix, breaking).
- `preview`: call devflow_release_preview and list unreleased commits by type.
- `init` or `sync`: run `devflow release $ARGUMENTS`, show what it wrote, and offer to commit
  it on a branch.
- `version --unit U`: run `devflow release $ARGUMENTS` and print the version.
- Run `devflow release get TAG` and report the selected provider's release.
- When the user requests a manual release with `publish TAG --revision SHA`, use
  `--dry-run` first and show the destination, tag, full commit SHA and metadata.
  After explicit approval, run the same command with `--yes` in place of `--dry-run`.
