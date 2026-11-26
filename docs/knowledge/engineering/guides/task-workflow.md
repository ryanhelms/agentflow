---
type: Guide
title: Work on a Agentflow task
description: Create or pick a GitHub issue and carry its task reference through a reviewed change.
tags: [tasks, github, contributor]
x-agentflow-ref: {scope: engineering, id: task-workflow}
---
# Work on a Agentflow task

Use the Agentflow checkout's committed provider settings. Run
`agentflow setup providers --explain` to check that GitHub Issues is selected for
Task Management and Repository is selected for Knowledge Base. Interactive work
uses the existing GitHub sign-in. Run `agentflow setup login --provider github-issues`
if the signed-in CLI needs authentication.

1. Create an issue with `agentflow task create`, or pick an existing issue using
   `agentflow work`. The `engineering-task@1` template defines Objective, Scope,
   Dependencies, Acceptance Criteria, and Verification. Use `--dry-run` to
   inspect the draft before approving it.
2. Run `agentflow start '#123'` with the issue number to bind a branch. The CLI
   `agentflow context` and Claude's `agentflow_context` read the bound task.
3. Put linked knowledge references under `## Related Knowledge` in the issue,
   one qualified reference per bullet. For example, use
   `repository/engineering/knowledge-publication`. Read the full document with
   `agentflow docs get repository/engineering/knowledge-publication`.
4. Include the qualified reference in the PR body on its own line, for example
   `Task-Ref: github-issues/ryanhelms%2Fagentflow/123`. Share verification evidence
   in the issue's acceptance checklist and PR description.
5. Review the merge preview. If the issue is explicitly enrolled in the scoped
   workflow, the approved merge can close it. Close other issues after reviewing
   acceptance criteria through the usual GitHub issue workflow.

See [provider architecture](../decisions/provider-architecture.md) and the
[publication runbook](../runbooks/knowledge-publication.md).
