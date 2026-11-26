---
name: worktree
description: Regenerate go.work in the current task worktree
---
Regenerate the git-excluded `go.work` in this task worktree from `worktree.go_work` in
`.agentflow/config.yml`.

1. Call `agentflow_worktree_go_work`.
2. Report the `use` entries and each `replace` target. Point out any target that is a
   primary checkout instead of a matching worktree.

From the CLI: `agentflow worktree go-work`.
