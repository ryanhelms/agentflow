---
name: collisions
description: Find other people's PRs on this task and open PRs touching the same files
argument-hint: "[KEY]"
---
Call devflow_collisions with the key from "$ARGUMENTS" (defaults to the current branch's
task). List other people's PRs on the task and PRs that touch the same files, and suggest
who to talk to before the work overlaps further.

CLI equivalent: `devflow collisions [KEY] [--json]`. The `devflow` command is on your Bash PATH.
