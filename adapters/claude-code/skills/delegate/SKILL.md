---
name: delegate
description: Hand a task to an agent session in another repository instead of changing it from here
argument-hint: "--repo NAME <task>"
---
Hand work in another repository to a session there. This session never changes another
repository itself.

1. Take the repository and the task from "$ARGUMENTS". If either is missing, ask me.
2. Call `agentflow_delegate` with `repo`, `task` and `dry_run: true`. Show me where it
   would go: a running session's tmux pane, a new session, or `native`.
3. On yes, call it again without `dry_run`. If it returns needs_confirmation, show the
   summary and wait.
4. For `status: native`, use your own session tools: send the returned `message` to a
   session whose folder is the returned `checkout` (or one of its worktrees), or start a
   new session there with it.
5. Report where the task went and how to follow it up.

From the CLI: `agentflow delegate --repo NAME [--agent claude|codex] [--start] [--dry-run] TASK`.
