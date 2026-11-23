---
name: setup
description: Check provider readiness, verify provider MCP servers, install pinned companions, or register a project client
argument-hint: "[providers | plan | verify | apply | login] [--provider NAME] [--companion ID] [--client NAME]"
---
To list the providers each role can use, run `devflow setup providers` or call
`devflow_setup_providers`. Add `--explain` (or `explain: true`) to show which definition
file or config set each provider setting and any release constraint. Run `devflow setup plan` or call `devflow_setup_plan` to inspect the selected
provider requirements. Use the current client or the one requested by the user.
For a selected provider in `provider_tools`, discover its native MCP in the
current client and try a small read for the configured site, repository or
workspace. Reuse a working connector. Check Jira and Confluence access
independently using the same Atlassian connection. Report permission denials
as access issues.

If the connector is missing, offer to install or register the official provider
integration in the current client. After approval, run the plan's
`register_args` with `devflow` when available, or use the vendor guide.
Prefer OAuth whenever available. Start the client's Connect or Sign in action
when the tool requires authentication. Keep personal tokens out of the setup
workflow, client config, and environment. With Claude Code, run
`devflow setup login --provider NAME --client claude --server SERVER` using
the registered MCP server name. Use `claude-desktop` for Claude Code Desktop.
Allow the user to complete consent in the browser, then retry the read.
For GitHub Issues, use the registration command from the plan. The pinned
local GitHub MCP server uses browser OAuth on its first GitHub tool call.
After registration, use a fresh session in the same workspace and retry the
read to start sign-in. The registered connector is read-only.

For Slack in Claude Code Desktop, when the user opts into workspace registration,
run `devflow setup login --provider slack --client claude-desktop --register` from
that workspace. Reuse an existing Slack connector. If sign-in is deferred, direct
the user to Claude’s `/mcp` action. Report a workspace or channel access denial
to the user. A connected status alone verifies only the host connection.

For Asana in Claude Code Desktop, start
`devflow setup login --provider asana --client claude-desktop --register`.
Use the existing V2 connector when available. For a new registration, add
`--client-id ID` from the approved Asana MCP app and let the user enter the
app secret directly at Claude's hidden terminal prompt. Asana app or project
access denials require the workspace owner's permission. Wait for browser
consent before retrying the operation.

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
`devflow setup login --provider jira --client devflow` for Jira comments and web links or
`devflow setup login --provider confluence --client devflow` for Confluence
page replacement when the user selects that connection. Each provider requires
its own browser consent. Wait for consent before requesting a new preview.

For Devflow-owned Asana writes, start
`devflow setup login --provider asana --client devflow` in the user's terminal
when the workspace owner has authorized the app. Let the user enter its app
secret directly in the prompt and complete browser consent. Request a fresh
write preview afterward. Report any app, workspace, or project access denial.

When the user selects Devflow’s own Slack connection and authorizes sign-in,
use the approved PKCE-enabled app’s public client ID and the selected Slack
destination’s `native.workspace_id`. Start the browser from the local workspace
by running `devflow setup login --provider slack --client devflow --public-client-id CLIENT_ID --team-id TEAM_ID`
with those values. Let the user approve in Slack and wait for the command to
verify and save the grant before requesting a preview. The app must register
`http://localhost:53668/oauth/slack/callback`. For a confidential registration,
the user enters the certificate paths and app credentials at hidden prompts
in their terminal with `devflow setup login --provider slack --client devflow`.

When asked to disconnect, run `devflow setup logout --provider NAME --client devflow`
with the selected provider name.
