---
name: ui
description: Open the local agentflow repository console to inspect providers, preview configuration edits and review repository activity
argument-hint: "[--screen SCREEN] [--role ROLE] [--no-open] [--port N] [--foreground] [directory]"
---
When the developer asks to open the console for the current worktree, call
`agentflow_ui_open`. Use `screen: "overview"` for the dashboard,
`"providers"` for the provider editor, `"autonomy"` for approval settings, `"plugins"` for installed plugins, `"harnesses"` for detected ACP harnesses, `"configuration"` for repository
files, or `"handoff"` for the handoff page. A provider role such as
`task_management` opens that role directly. The tool opens the local browser
and directs an existing tab for the clone to the requested worktree.

For an explicit directory, run `agentflow ui $ARGUMENTS` in the intended
worktree. The daemon stays running when the command returns. From an agent
session `agentflow ui` opens the browser itself and never prints the one-time
sign-in link, and `--no-open` refuses: only a person at their own terminal gets
the link. When the developer wants the link, ask them to run
`agentflow ui --no-open` in their own terminal. The developer reviews and approves configuration changes in the console.

For approval settings, direct the developer to **Autonomy**. Personal preferences
save on this workstation. Repository ceilings stage in **Configuration** for review.
