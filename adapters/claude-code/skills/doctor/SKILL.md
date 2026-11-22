---
name: doctor
description: Check devflow's binary, git hooks, Atlassian login, client config and API access
argument-hint: "[--fix | --providers]"
---
The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

Run `devflow doctor $ARGUMENTS`. List each failing check with its fix in one line. Offer to
run `devflow doctor --fix` for fixes it can apply (git hooks path), `/devflow:init` when
`.devflow/config.yml` is missing and `/devflow:login` when Atlassian isn't connected.

Use `devflow doctor --providers` to check configured provider readiness.
