---
name: notify
description: Preview or send a delivery signal to the repository's configured notification destinations
argument-hint: "SIGNAL --operation-id ID --body TEXT [--dry-run]"
---
Use `agentflow_notify` when available, or run `agentflow notify $ARGUMENTS` with
shell-safe arguments. Preview the destination list with `dry_run` or
`--dry-run` and obtain approval before sending on the user's behalf. For a
native provider sign-in error, start the tool's OAuth flow through the setup
skill. For a CLI session with Codex's provider MCP configured, use
`agentflow --native-client codex notify` to reuse its OAuth connection. Show each destination's outcome. Reuse the same operation
ID and message for replay. Refer an `unconfirmed` outcome to the user for
reconciliation with the receiver. The `agentflow` command is on your Bash PATH.
