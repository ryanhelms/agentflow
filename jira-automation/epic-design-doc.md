# Epic → design doc

**Purpose:** every epic starts with a design doc from the shared template, linked both ways.

| | |
|---|---|
| Trigger | Issue created |
| Scope | Opted-in projects (pilot: PLAT) |
| Condition | Issue type = Epic |
| Action 1 | **Create page** (Confluence) in the team space, from template *Design Doc* (shared set, Engineering space). Title: `{{issue.key}} · {{issue.summary}}` |
| Action 2 | **Comment on issue**: `Design doc: {{createdPage.url}}` |
| Action 3 | **Edit issue** › add a web link to the page (optional; needs the Confluence link field) |
| Actor | svc-devflow |

Template ownership: engineering managers (see `confluence-templates/design-doc.md`).
If `{{createdPage.url}}` isn't available in your Automation version, use the page link the
Create page action writes to the audit log, or a lookup with a *Lookup pages* action.
