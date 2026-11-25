---
name: standup
description: Draft my standup from commits, PRs, reviews and Jira changes
argument-hint: "[--post]"
---
Call agentflow_standup and draft my standup as Yesterday / Today / Blockers, grouped by ticket.
If "$ARGUMENTS" includes --post, offer to post end-of-day progress comments to each ticket
(this needs confirmation).

CLI equivalent: `agentflow standup [--since 24h]`.
