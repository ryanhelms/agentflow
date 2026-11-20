---
name: start
description: Pick a Jira ticket and start work on it
argument-hint: "[JIRA-KEY]"
---
Start work on a Jira ticket with devflow.

1. If "$ARGUMENTS" contains a Jira key, use it. Otherwise call devflow_my_work and ask me which ticket.
2. Call devflow_collisions for that ticket and tell me if anyone else is on it.
3. Call devflow_start with the key. If it returns needs_confirmation, show the summary and wait.
4. Call devflow_context and give me a three-line brief: goal, acceptance criteria, next status.

CLI equivalent: `devflow start KEY [--worktree]`.
