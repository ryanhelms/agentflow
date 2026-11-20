# Blocked → Slack

| | |
|---|---|
| Trigger | Issue transitioned to Blocked |
| Scope | Projects with a Blocked status (pilot: PLAT) |
| Action | **Send Slack message** to the team webhook: `:no_entry: {{issue.key}} blocked by {{initiator.displayName}}: {{issue.comments.last.body}} — {{issue.url}}` |
| Actor | svc-devflow |

The `#block` commit verb moves the ticket and posts the commit body as a comment first,
so the last comment carries the reason.
