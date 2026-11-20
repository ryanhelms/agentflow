---
description: Commit current work as Jira-linked Conventional Commits
---
Call devflow_commit_plan and commit each group separately. For each group call devflow_commit_context,
write `type(KEY): imperative summary` plus a body explaining why, call devflow_commit_preview and tell me
the Jira effect in one line, then call devflow_commit. If it returns needs_confirmation, show me and wait.
