---
name: install
description: Configure AI clients (Claude, Codex, Grok, Cursor, VS Code, Gemini) for devflow in this repo
argument-hint: "[--client all|claude,codex,...]"
disable-model-invocation: true
---
The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

Run `devflow install $ARGUMENTS` (default `--client all`, which configures the clients found
on PATH) and list the files it wrote or updated, plus its notes. Offer to commit the repo
files on a branch; files under `~` (Codex prompts) are personal and not committed.
