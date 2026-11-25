---
name: commit
description: Commit current work as Jira-linked Conventional Commits
argument-hint: "[#verb]"
---
1. Call devflow_commit_plan. If it finds more than one group, commit each group separately.
2. For each group call devflow_commit_context, then write the message:
   type(KEY): imperative summary under 72 characters, blank line, body explaining why.
   Add the verb from "$ARGUMENTS" if I gave one.
3. Call devflow_commit_prepare with the message and paths. The exact candidate
   includes the parent, tree and task effects. Use devflow_commit_preview when
   investigating task effects on their own.
4. Follow the Devflow workflow in AGENTS.md. Apply a prepared candidate with
   devflow_commit and the returned token. Only the owner of a needs_confirmation
   response asks for approval. An awaiting_approval caller checks the operation
   ID through devflow_approval_status.

CLI equivalent: `devflow commit --prepare -m MESSAGE [--path PATH]`, then the
same message and paths with `devflow commit --confirm-token TOKEN`.
