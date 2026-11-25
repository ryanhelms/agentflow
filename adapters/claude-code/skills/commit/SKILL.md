---
name: commit
description: Commit current work as Jira-linked Conventional Commits
argument-hint: "[#verb]"
---
1. Call agentflow_commit_plan. If it finds more than one group, commit each group separately.
2. For each group call agentflow_commit_context, then write the message:
   type(KEY): imperative summary under 72 characters, blank line, body explaining why.
   Add the verb from "$ARGUMENTS" if I gave one.
3. Call agentflow_commit_prepare with the message and paths. The exact candidate
   includes the parent, tree and task effects. Use agentflow_commit_preview when
   investigating task effects on their own.
4. Follow the Agentflow workflow in AGENTS.md. Apply a prepared candidate with
   agentflow_commit and the returned token. Only the owner of a needs_confirmation
   response asks for approval. An awaiting_approval caller checks the operation
   ID through agentflow_approval_status.

CLI equivalent: `agentflow commit --prepare -m MESSAGE [--path PATH]`, then the
same message and paths with `agentflow commit --confirm-token TOKEN`.
