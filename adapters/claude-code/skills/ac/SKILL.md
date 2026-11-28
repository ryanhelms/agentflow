---
name: ac
description: Show the task's acceptance criteria checklist, or tick and untick criteria with evidence
argument-hint: "[KEY] | done|undo ITEM [--evidence SHA] [--post]"
---
With no action in "$ARGUMENTS", call agentflow_ac_checklist and show each criterion with its
done state and evidence. For `done` or `undo`, call agentflow_ac_update with the item text,
`done` true or false and the evidence (a commit SHA or test name). Only set `post` when
asked; it comments progress on the task, so show the summary and wait for my yes on
needs_confirmation.

CLI equivalent: `agentflow ac [KEY]` and `agentflow ac done|undo ITEM [--key KEY] [--evidence E] [--post]`. The `agentflow` command is on your Bash PATH.

`agentflow ac --ai [KEY]` is for a person at a terminal: their harness checks the
criteria against the branch, and agentflow asks before each tick; it never posts. As an
agent, call agentflow_ai_draft with feature `ac`. Each candidate is only a claim that a
criterion is met: check it yourself before you pass it to agentflow_ac_update.
