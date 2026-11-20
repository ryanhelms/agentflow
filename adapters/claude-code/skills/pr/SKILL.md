---
name: pr
description: Check readiness and open a pull request for the current ticket
---
1. Call devflow_pr_ready. Fix anything it reports that you can fix, and list what you can't.
2. Call devflow_pr_open. Title in the convention, body from the repo template with why, changes,
   the acceptance criteria checklist and a test plan. Open as draft if criteria are incomplete.
3. If it returns needs_confirmation, show the summary and wait. Then give me the PR link.

CLI equivalent: `devflow pr --check`, then `devflow pr`.
