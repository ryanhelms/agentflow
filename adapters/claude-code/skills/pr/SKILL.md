---
name: pr
description: Check readiness and open a pull request for the current ticket
---
1. Call agentflow_pr_ready. Fix anything it reports that you can fix, and list what you can't.
2. Call agentflow_pr_prepare. Use a conventional title and the repo's PR template
   with the rationale, changes, acceptance criteria and test plan. Set draft
   when criteria are incomplete. The preview includes both the push and PR.
   A repository can opt into automatic PR publication while standalone pushes
   require approval. Use the combined preview when asked to open a PR.
3. Apply through agentflow_pr_open with the same arguments and token according
   to the approval response in AGENTS.md. Give me the PR link.

Use agentflow_pr_mark_ready_prepare for an exact preview of draft promotion,
and agentflow_pr_mark_ready to apply it when authorized. The CLI
commands are `agentflow pr --check`, `agentflow pr --prepare`,
`agentflow pr --confirm-token TOKEN` and `agentflow pr mark-ready`. Draft promotion
also accepts `--prepare` or `--confirm-token TOKEN`.
