---
name: sweep
description: Finish merged worktrees and report dirty, unpushed, stale and orphaned ones
argument-hint: "[--all-repos]"
---
Tidy task worktrees with agentflow.

1. Call `agentflow_sweep` with `dry_run: true` (and `all_repos: true` when "$ARGUMENTS" asks for every repo).
2. Show me a short table: repository, branch, state and detail.
3. If any are `would_finish`, offer to finish them. On yes, call `agentflow_sweep` without `dry_run`. If it returns needs_confirmation, show the summary and wait.
4. Never delete dirty, unpushed, stale or orphaned worktrees. Suggest what I can do with each.

From the CLI: `agentflow sweep [--all-repos] [--dry-run] [--stale-days N]`.
