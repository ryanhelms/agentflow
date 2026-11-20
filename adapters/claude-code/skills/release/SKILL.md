---
name: release
description: Plan, preview or set up semantic versioning releases for this repo's deployable units
argument-hint: "[plan|preview|init|sync|version --unit U]"
---
The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

- No argument or `plan`: call devflow_release_plan and show each unit's current and next
  version and why (feat, fix, breaking).
- `preview`: call devflow_release_preview and list unreleased commits by type.
- `init` or `sync`: run `devflow release $ARGUMENTS`, show what it wrote, and offer to commit
  it on a branch.
- `version --unit U`: run `devflow release $ARGUMENTS` and print the version.

Releases themselves are created by CI on merge, never from here.
