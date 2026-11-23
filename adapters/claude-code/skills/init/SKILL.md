---
name: init
description: Set devflow up in this repo (config, hooks, workflow, client configs, release units)
argument-hint: "[--force | --guidance-only]"
disable-model-invocation: true
---
Set this repository up for devflow. The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

1. Run `git status --short` and `git branch --show-current` to identify existing work.
2. Run `devflow init $ARGUMENTS` and show me what it wrote, updated, and kept.
   When called with `--guidance-only`, summarize the `AGENTS.md` update and finish.
3. Show the detected release units and strategy. Use `devflow setup providers --explain`
   to report the selected providers and check `policy.test_cmd` and `policy.lint_cmd`
   in `.devflow/config.yml`.
4. Run `devflow setup plan` and summarize any setup still needed for the selected providers.
5. Offer to commit the setup on a branch and open a PR using the configured commit
   convention. Ask for a task key only when that convention requires one.

Repos owned by the orgs in `init.orgs` (default `nscaledev`) are set up automatically when
a session starts; set `init.auto: ask` or `off` in `~/.devflow/config.yml` to change that.
