---
name: policy
description: Explain Agentflow autonomy or inspect a pending approval
argument-hint: "[explain ACTION | status ID | approve ID]"
---
For an autonomy question, use agentflow_policy_explain and name the effective
level, contributing user and repository settings and any mandatory confirmation.
For an existing operation, read agentflow_approval_status. Its operation ID can be
shared with workers. Keep its owner's confirmation token private.
When `approval.previously_issued` is set and the owner already presented the
preview, reuse the existing question.
Stop waiting when status returns `cancelled`, `consumed` or `expired`.

After the user approves the exact summary, the owner may use
agentflow_approval_approve with its operation ID and confirmation token to hand
the approved action to a worker. The grant records approval. The original
operation still needs to run. Use `agentflow policy explain [ACTION]`,
`agentflow policy status ID` or `agentflow policy approve ID --confirm-token TOKEN`
when using the CLI.
