---
name: setup
description: Check provider readiness, verify provider MCP servers, install pinned companions, or register a project client
argument-hint: "[providers | plan | verify | apply] [--companion ID] [--client NAME] [--install | --install-gh]"
---
To list the providers each role can use, run `devflow setup providers` or call
`devflow_setup_providers`. Add `--explain` (or `explain: true`) to show which definition
file or config set each provider setting and any release constraint. Run `devflow setup plan` or call `devflow_setup_plan` to inspect the selected
provider requirements. Use the current client or the one requested by the user.
Explain any missing prerequisites. If the plan offers `install_gh`, show the
pinned artifact details. On request, use `devflow_setup_apply` with
`install_gh: true` or `devflow setup apply --install-gh`. Obtain approval for
the installation summary before retrying with its confirmation token.
When the user approves client registration,
use `devflow_setup_apply` or `devflow setup apply --client NAME`.
Select `claude-desktop` for the Claude Code Desktop project server in `.mcp.json`
or `claude` for plugin marketplace enrollment. After desktop registration,
restart Claude Code Desktop and check for `mcp__devflow__devflow_setup_plan`.

For an MCP companion in the trusted user manifest, pass `companion: ID` to
the plan. Show the declared endpoint or executable pin and credential
variable name. Use `install: true` with the ID for a local installation.
Request separate approval for the same ID with the user's explicit target
`client` (claude, claude-desktop, cursor, gemini or vscode). Recheck the handshake, tool listing
and managed entry with `devflow_setup_plan`. Ask the user to supply any missing credential
in their runtime environment.
