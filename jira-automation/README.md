# Jira Automation rules

agentflow uses Jira Automation for events that start in Jira. Each rule has a spec here;
the live rule is built in **Jira settings › System › Automation** as a global rule,
scoped to opted-in projects, with the actor set to the `svc-agentflow` service account.

| Rule | Spec | Phase |
|---|---|---|
| Epic → design doc | [epic-design-doc.md](epic-design-doc.md) | P6 |
| Incident → postmortem + Slack | [incident-postmortem.md](incident-postmortem.md) | P7 |
| In Progress with no commits | [in-progress-no-commits.md](in-progress-no-commits.md) | P7 |
| Blocked → Slack | [blocked-to-slack.md](blocked-to-slack.md) | P7 |

## Keeping rules reviewed

After creating or changing a rule:

1. In the rule list, select the rule › **Export** (or *Export rules* for several).
2. Save the JSON here as `exports/<rule-name>.json`, replacing the previous export.
3. Open a PR. A maintainer reviews rule changes like code.

Exports contain project and field IDs specific to one Jira site; don't import them into
another site without editing those IDs.
