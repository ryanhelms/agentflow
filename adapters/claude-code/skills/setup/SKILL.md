---
name: setup
description: Check provider readiness, verify provider MCP servers, install pinned companions, or register a project client
argument-hint: "[providers | plan | verify | apply | login] [--provider NAME] [--companion ID] [--client NAME]"
---
To list the providers each role can use, run `devflow setup providers` or call
`devflow_setup_providers`. Run `devflow setup plan` or call `devflow_setup_plan` to inspect the selected
provider requirements. Use the current client or the one requested by the user.
For a selected provider in `provider_tools`, discover its native MCP in the
current client and try a small read for the configured site, repository or
workspace. Reuse a working connector. Check Jira and Confluence access
independently using the same Atlassian connection. Report permission denials
as access issues.

If the connector is missing, offer to install or register the official provider
integration in the current client. Use the plan's vendor guide when the user
authorizes setup. Start the client's Connect or Sign in action when the tool
requires authentication. With Claude Code, run
`devflow setup login --provider NAME --client claude --server SERVER` using
the registered MCP server name. Use `claude-desktop` for Claude Code Desktop.
Allow the user to complete consent in the browser, then retry the read.
If the MCP is unavailable for the operation, use the provider's installed CLI,
or offer to install it. To start its sign-in, use
`devflow setup login --provider NAME` when the plan lists a CLI. Use the
provider's native CLI directly if the delegated command cannot run. Keep user
credentials in the vendor tool's login flow. If the host blocks the launch,
show the user the specific host action it requires.

If the plan offers `install_gh`, show the pinned artifact details. On request, use `devflow_setup_apply` with
`install_gh: true` or `devflow setup apply --install-gh`. Obtain approval for
the installation summary before retrying with its confirmation token.
When the user approves client registration,
use `devflow_setup_apply` or `devflow setup apply --client NAME`.
Select `claude-desktop` for the Claude Code Desktop project server in `.mcp.json`
or `claude` for plugin marketplace enrollment. After desktop registration,
restart Claude Code Desktop and check for `mcp__devflow__devflow_setup_plan`.

For an MCP companion in the trusted user manifest, pass `companion: ID` to
the plan. Show the declared endpoint or executable pin. Use `install: true`
with the ID for a local installation.
Request separate approval for the same ID with the user's explicit target
`client` (claude, claude-desktop, cursor, gemini or vscode). Recheck the handshake, tool listing
and managed entry with `devflow_setup_plan`. For a custom integration that
requires an environment credential, offer the vendor's interactive connector
or CLI when available.

For the Devflow Rovo pilot in Claude Code Desktop, use
`devflow setup login --provider jira --client devflow` for Jira web links or
`devflow setup login --provider confluence --client devflow` for Confluence
page replacement when the user selects that connection. Each provider requires
its own browser consent. Wait for consent before requesting a new preview.

When asked to disconnect, run `devflow setup logout --provider NAME --client devflow`
with the selected provider name.
