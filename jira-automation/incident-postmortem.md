# Incident → postmortem + Slack

| | |
|---|---|
| Trigger | Issue created |
| Condition | Issue type = Incident AND priority in (P1, P2) |
| Action 1 | **Send Slack message** (incoming webhook for `#incidents`): `:rotating_light: {{issue.priority.name}} {{issue.key}} {{issue.summary}} — {{issue.url}}` |
| Action 2 | **Create page** from template *Postmortem* in the owning team's space, title `Postmortem · {{issue.key}} · {{issue.summary}}`; comment the link on the incident |
| Actor | svc-agentflow |

Second rule in the same file:

| | |
|---|---|
| Trigger | Issue transitioned to Resolved |
| Condition | Issue type = Incident AND priority in (P1, P2) |
| Action | **Create sub-task** `Postmortem review` assigned to the incident reporter, due `{{now.plusBusinessDays(5)}}` |

Hotfix branches named `hotfix/INC-123-…` get the `fast-track` label from the repo's own
labeller config; agentflow's key check accepts `INC-` keys like any other.
