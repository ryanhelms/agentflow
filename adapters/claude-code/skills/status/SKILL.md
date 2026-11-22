---
name: status
description: Show PR, checks, deployments and Jira status for the current work
---
Call devflow_ship_status and summarise: PR state, failing checks, deployments by environment,
the ticket's status and its next flow-map transition. Keep it under eight lines.
Deployments match the open PR head or, without an open PR, the current commit.

CLI equivalent: `devflow status`.
