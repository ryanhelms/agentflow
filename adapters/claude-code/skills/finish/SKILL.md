---
name: finish
description: Clean up a task's worktree and branches after its PR is merged
argument-hint: "[BRANCH]"
---
Finish a merged task with agentflow.

1. Call `agentflow_finish` with `dry_run: true`. Pass "$ARGUMENTS" as `branch` when it names one; otherwise it uses the current worktree.
2. If it reports `dirty`, `unpushed` or `unmerged`, tell me why and stop. Never force it.
3. Show me the steps, then call `agentflow_finish` without `dry_run`. If it returns needs_confirmation, show the summary and wait.
4. Tell me the primary checkout path from the result. Continue there; the worktree is gone.

From the CLI: `agentflow finish [BRANCH] [--dry-run]`.
