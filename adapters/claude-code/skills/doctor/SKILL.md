---
name: doctor
description: Check agentflow's binary, git hooks, Atlassian login, client config and API access
argument-hint: "[--fix | --providers]"
---
The `agentflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

Run `agentflow doctor $ARGUMENTS`. List each failing check with its fix in one line. Offer to
run `agentflow doctor --fix` for fixes it can apply (git hooks path), `/agentflow:init` when
`.agentflow/config.yml` is missing and `/agentflow:login` when Atlassian isn't connected.

Use `agentflow doctor --providers` to check configured provider readiness.
