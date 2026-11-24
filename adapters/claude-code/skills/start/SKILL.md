---
name: start
description: Pick a task and start work on it
argument-hint: "[TASK]"
---
Start work on a task with agentflow.

1. If "$ARGUMENTS" contains a task key, use it. Otherwise call agentflow_my_work and ask me which ticket.
2. For a Jira ticket, call agentflow_collisions and tell me if anyone else is on it.
3. Call agentflow_start with the key. If it returns needs_confirmation, show the summary and wait. File-backed tasks bind a local branch or worktree.
4. For a partial Jira start, report the created branch or worktree and the verified assignment or transition from the result. Stop on an uncertain write and inspect the issue.
5. Call agentflow_context and give me a three-line brief: goal, acceptance criteria, next status.

Use `agentflow start KEY [--worktree]` from the CLI. For the Desktop Jira enrollment, use `agentflow --native-client claude-desktop start KEY [--worktree]`.
