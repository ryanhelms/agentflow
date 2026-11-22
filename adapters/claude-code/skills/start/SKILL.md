---
name: start
description: Pick a task and start work on it
argument-hint: "[TASK]"
---
Start work on a task with devflow.

1. If "$ARGUMENTS" contains a task key, use it. Otherwise call devflow_my_work and ask me which ticket.
2. For a Jira ticket, call devflow_collisions and tell me if anyone else is on it.
3. Call devflow_start with the key. If it returns needs_confirmation, show the summary and wait. File-backed tasks bind a local branch or worktree.
4. Call devflow_context and give me a three-line brief: goal, acceptance criteria, next status.

CLI equivalent: `devflow start KEY [--worktree]`.
