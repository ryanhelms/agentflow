---
name: push
description: Publish the current feature branch to its configured remote
---
Use devflow_push when asked to push work on the current feature branch.
Check the returned branch, destination and head SHA. Follow the Devflow
approval response in AGENTS.md and report the published revision on success.
Use `devflow_push_prepare` or `devflow push --prepare` for an exact preview.
The apply commands are `devflow_push` with `confirm_token` and
`devflow push --confirm-token TOKEN`.
