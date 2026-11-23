---
type: Guide
title: Work on a Devflow task
description: Create or pick a GitHub issue and carry its task reference through a reviewed change.
tags: [tasks, github, contributor]
x-devflow-ref: {scope: engineering, id: task-workflow}
---
# Work on a Devflow task

Use the Devflow checkout's committed provider settings. Run
`devflow setup providers --explain` to check that GitHub Issues is selected for
Task Management and Repository is selected for Knowledge Base. Interactive work
uses the existing GitHub sign-in. Run `devflow setup login --provider github-issues`
if the signed-in CLI needs authentication.

1. Create an issue with `devflow task create`, or pick an existing issue using
   `devflow work`. The `engineering-task@1` template defines Objective, Scope,
   Dependencies, Acceptance Criteria, and Verification. Use `--dry-run` to
   inspect the draft before approving it.
2. Run `devflow start '#123'` with the issue number to bind a branch. The CLI
   `devflow context` and Claude's `devflow_context` read the bound task.
3. Put linked knowledge references under `## Related Knowledge` in the issue,
   one qualified reference per bullet. For example, use
   `repository/engineering/knowledge-publication`. Read the full document with
   `devflow docs get repository/engineering/knowledge-publication`.
4. Include the qualified reference in the PR body on its own line, for example
   `Task-Ref: github-issues/nscaledev%2Fdevflow/123`. Share verification evidence
   in the issue's acceptance checklist and PR description.
5. Review the merge preview. If the issue is explicitly enrolled in the scoped
   workflow, the approved merge can close it. Close other issues after reviewing
   acceptance criteria through the usual GitHub issue workflow.

See [provider architecture](../decisions/provider-architecture.md) and the
[publication runbook](../runbooks/knowledge-publication.md).
