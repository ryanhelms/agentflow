---
name: adr
description: Draft an architecture decision record in docs/adr
argument-hint: "TITLE"
---
Call agentflow_adr_draft with the title in "$ARGUMENTS". Fill in context, decision and
consequences from our conversation and the ticket, mark anything you're unsure of, and show
me the file before committing it.

CLI equivalent: `agentflow adr new TITLE`. The `agentflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.
