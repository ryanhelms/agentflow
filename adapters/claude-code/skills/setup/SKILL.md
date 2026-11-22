---
name: setup
description: Check provider readiness, verify provider MCP servers, install pinned companions, or register a project client
argument-hint: "[plan | verify | apply] [--companion ID] [--client NAME] [--install | --install-gh]"
---
Run `devflow setup plan` or call `devflow_setup_plan` to inspect the selected
provider requirements. Use the current client or the one requested by the user.
Explain any missing prerequisites. If the plan offers `install_gh`, show the
pinned artifact details. On request, use `devflow_setup_apply` with
`install_gh: true` or `devflow setup apply --install-gh`. Obtain approval for
the installation summary before retrying with its confirmation token.
When the user approves client registration,
use `devflow_setup_apply` or `devflow setup apply --client NAME`.

For an MCP companion in the trusted user manifest, pass `companion: ID` to
the plan. Show the declared endpoint or executable pin and credential
variable name. Use `install: true` with the ID for a local installation.
Request separate approval for the same ID with the user's explicit target
`client` (claude, cursor, gemini or vscode). Recheck the handshake, tool listing
and managed entry with `devflow_setup_plan`. Ask the user to supply any missing credential
in their runtime environment.
