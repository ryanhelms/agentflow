---
name: scope
description: Check whether the current changes drift outside the task
---
Call agentflow_scope_check and list the files and new TODOs that look outside the current
task. If there are any, offer to split them into a linked task with
agentflow_ticket_create (it needs confirmation).

CLI equivalent: `agentflow scope [--json]`. The `agentflow` command is on your Bash PATH.
