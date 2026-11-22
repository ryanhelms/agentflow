---
name: transition
description: Move a work item by a configured event or verb (for example pr.merged or #qa)
argument-hint: "[TASK] EVENT"
---
Call devflow_transition with the key and event from "$ARGUMENTS" (key defaults to the
current branch's ticket). Do a dry run first and tell me the from and to status. If it
returns needs_confirmation, show the summary and wait for my yes. For pending_review, report the task key, review branch and
PR URL. The task state changes when the PR is merged.

CLI equivalent: `devflow transition [KEY] EVENT [--dry-run]`. The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.
