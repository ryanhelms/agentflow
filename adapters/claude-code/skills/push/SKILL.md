---
name: push
description: Publish the current feature branch to its configured remote
---
Use agentflow_push when asked to push work on the current feature branch.
Check the returned branch, destination and head SHA. Follow the Agentflow
approval response in AGENTS.md and report the published revision on success.
Use `agentflow_push_prepare` or `agentflow push --prepare` for an exact preview.
The apply commands are `agentflow_push` with `confirm_token` and
`agentflow push --confirm-token TOKEN`.
