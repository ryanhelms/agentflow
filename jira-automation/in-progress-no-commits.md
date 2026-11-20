# In Progress with no code

| | |
|---|---|
| Trigger | Scheduled, weekdays 09:00 (Europe/London) |
| JQL | `project in (PLAT) AND statusCategory = "In Progress" AND updated <= -5d AND development[commits].all = 0` |
| Action 1 | **Comment on issue** mentioning `{{issue.assignee}}`: "This has been in progress for 5 days with no linked commits. Still active? Branch names and commits need the key `{{issue.key}}`." |
| Action 2 | **Send Slack message** (team webhook) with a digest: `{{#lookupIssues}}{{key}} {{summary}} ({{assignee.displayName}}){{/}}` (use a *Lookup issues* action first) |
| Actor | svc-devflow |

`development[commits]` needs the GitHub for Jira app. Add projects to the JQL as teams join (P8).
