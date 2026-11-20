---
name: commit
description: Commit current work as Jira-linked Conventional Commits
argument-hint: "[#verb]"
---
1. Call devflow_commit_plan. If it finds more than one group, commit each group separately.
2. For each group call devflow_commit_context, then write the message:
   type(KEY): imperative summary under 72 characters, blank line, body explaining why.
   Add the verb from "$ARGUMENTS" if I gave one.
3. Call devflow_commit_preview and tell me any Jira effect in one line.
4. Call devflow_commit. If it returns needs_confirmation, show the summary and wait.

If the MCP tools are unavailable, `devflow commit --dry-run` then `devflow commit` does the same from the CLI.
