---
name: init
description: Set devflow up in this repo (config, hooks, workflow, client configs, release units)
argument-hint: "[--force]"
disable-model-invocation: true
---
Set this repository up for devflow. The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

1. Run `git status --short` and `git branch --show-current`. If the working tree has
   unrelated changes, say so and ask before continuing.
2. Run `devflow init $ARGUMENTS` and show me what it wrote, updated and kept.
3. Show me the detected release units and strategy, and the two settings teams usually
   change: `jira.projects` and `policy.test_cmd`/`lint_cmd` in `.devflow/config.yml`. Ask
   if I want them changed.
4. Run `devflow doctor` and summarise anything that is not ✓ with the fix.
5. Offer to commit everything on a new branch as `chore(KEY): set up devflow` and open a
   PR. Ask me for the Jira key: the new commit-msg hook requires one unless
   `commit.require_key: false` is set in `.devflow/config.yml`. Don't commit or push
   without my yes.

Repos owned by the orgs in `init.orgs` (default `nscaledev`) are set up automatically when
a session starts; set `init.auto: ask` or `off` in `~/.devflow/config.yml` to change that.
