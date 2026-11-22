---
name: eod
description: Post end-of-day progress comments on my tasks
argument-hint: "[KEY=NOTE ...]"
---
Use the notes in "$ARGUMENTS", or draft a one-line note per task from devflow_standup and
show them to me first. Call devflow_eod_post with a map of task key to note. On
needs_confirmation, show the summary and wait for my yes.

CLI equivalent: `devflow eod post KEY=NOTE [KEY=NOTE...]`. The `devflow` command is on your Bash PATH.
