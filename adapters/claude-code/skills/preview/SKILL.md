---
name: preview
description: Preview what Jira will do for a commit message or for my unpushed commits
argument-hint: "[commit message]"
---
Call devflow_commit_preview with the message in "$ARGUMENTS", or with no message to preview
my unpushed commits. Tell me in plain words which tickets move, get comments or time.

CLI equivalent: `devflow preview [MSG]`. The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.
