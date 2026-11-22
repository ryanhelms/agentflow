---
name: scope
description: Check whether the current changes drift outside the task
---
Call devflow_scope_check and list the files and new TODOs that look outside the current
task. If there are any, offer to split them into a linked task with
devflow_ticket_create (it needs confirmation).

CLI equivalent: `devflow scope [--json]`. The `devflow` command is on your Bash PATH.
