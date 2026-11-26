---
name: init
description: Set agentflow up in this repo (config, hooks, workflow, client configs, release units)
argument-hint: "[--detect-providers | --guided | --non-interactive] [--provider ROLE=ID ...] [--force | --guidance-only]"
disable-model-invocation: true
---
Set this repository up for agentflow. The `agentflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

1. Run `git status --short` and `git branch --show-current` to identify existing work.
2. For `--guidance-only`, run `agentflow init --guidance-only`, summarize the managed
   instructions, and finish. Otherwise, run `agentflow init --detect-providers`
   from this checkout. This returns JSON describing each role, its options,
   default, saved selection or detected candidates, and the local evidence.
   For a detection-only request, show those results and finish.
3. Show the saved and detected selections together. Honor a saved `none` and give
   the user a chance to change any role. Ask about unknown or conflicting roles
   using the options returned by detection. A catalog default is a suggestion.
   When switching a provider, explain that its previous inline settings will
   be replaced. An interactive terminal can run `agentflow init --guided` for the
   same review. In an agent session, collect the answers in the conversation.
4. Pass the confirmed choices as separate arguments to
   `agentflow init --non-interactive --provider ROLE=ID`, repeating `--provider`
   for each choice. Reuse explicit choices already supplied by the user. An
   explicit `--non-interactive` request can proceed with saved selections and
   defaults for omitted roles. Pass `--force` only when the user requested it.
   Summarize the files Agentflow wrote, updated, or kept.
5. Show the detected release units and strategy. Use `agentflow setup providers --explain`
   to report the selected providers and check `policy.test_cmd` and `policy.lint_cmd`
   in `.agentflow/config.yml`.
6. Run `agentflow setup plan` and summarize any setup still needed for the selected providers.
7. Offer to commit the setup on a branch and open a PR using the configured commit
   convention. Ask for a task key only when that convention requires one.

Repos owned by the orgs in `init.orgs` (default `nscaledev` and `ryanhelms`) are set up automatically when
a session starts; set `init.auto: ask` or `off` in `~/.agentflow/config.yml` to change that.
