# Provider configuration

Agentflow resolves provider settings from distinct sections in `.agentflow/config.yml`.
Each section has a versioned schema. A value in `settings` overrides the selected
provider's default for that field. Omitted fields inherit their defaults, nested
objects merge by field, and supplied lists replace the default lists.

This reference describes the configuration accepted by the provider foundation.
The provider-specific sections below describe the CLI, MCP and workflow entry points.

`agentflow init` and `agentflow install` give agents the same workflow guidance in a
managed `AGENTS.md` block. Project-specific instructions remain outside that
block. The guidance tells agents to use the effective provider configuration.
When a session starts, the MCP server and Claude Code hook report the selected
task and knowledge providers. Run `agentflow setup providers --explain` to inspect
how Agentflow resolved their settings. Existing repositories can refresh just
this guidance with `agentflow init --guidance-only`.

| Section | Default provider | Other schema selections |
| --- | --- | --- |
| `code_hosting` | `auto` | `github`, `none` |
| `task_management` | Disabled | `jira`, `asana`, `github-issues`, `filesystem`, `repository`, `none` |
| `knowledge_base` | Disabled | `confluence`, `filesystem`, `repository`, `github-wiki`, `none` |
| `devops.checks` | `github` | `none` |
| `devops.deployments` | `github` | `fixture`, `none` |
| `devops.releases` | `github` | `fixture`, `none` |
| `secops` | `github` | `none` |
| `notifications` | `slack` | `webhook`, `router`, `none` |

Each selection accepts `version` (default `1`), `provider` (see the table),
and `settings` (defaults below). The enclosing `devops` section also accepts
a schema version (default `1`). A provider selection of `none` has an empty
settings object. Validation errors identify the property to correct.

Task management and knowledge base are enabled independently. Omit
`provider` or select `none` to disable that role. Legacy Jira and Confluence
fields and inline settings cannot activate a role by themselves. For a repository
that relied on the previous implicit selection, set `task_management.provider`
to `jira` or `knowledge_base.provider` to `confluence` for the roles you use.
Those providers continue to use their legacy settings where applicable.

Setup shows detected task and knowledge providers as suggestions. A provider
becomes active once you select it in guided init or set it in the repository
config. Code hosting, commit conventions, and other configured roles can run
with either role disabled. Commit messages may omit a task key when task
management is disabled, unless the repository sets `commit.require_key: true`.
Hosted workflows skip task actions and knowledge publishing for unassigned
roles, including the legacy Jira and Confluence jobs.

| Provider | Settings and defaults |
| --- | --- |
| Code hosting | `hosts` defaults to an empty hostname-to-forge map. `source_remote` and `target_remote` default to unset, so detection can select Git remotes. |
| Jira | `site` and `projects` inherit the legacy Jira settings and defaults. |
| Asana | `project` is the default project GID. `transitions` maps each project GID to its events and destination section GIDs. Both start unset. |
| Confluence | `site` inherits the legacy Confluence site, which defaults to the Jira site. Optional `publish_spaces` lists the exact space keys permitted for document publication. |
| GitHub | An unset `repository` defers to Git remote detection. |
| DevOps fixture | Set `path` to a repository-relative JSON file for each role. |
| GitHub Issues | `repository` defaults to unset. `labels` defaults to an empty list. `search.open_only` defaults to `true`. `search.limit` defaults to `30`. |
| GitHub Wiki | `repository` defaults to unset, so Git forge detection selects the GitHub.com target. Set an explicit `owner/name` to use another GitHub.com repository. |
| FileSystem | `root` defaults to `.agentflow/tasks` for tasks and `docs` for knowledge. For knowledge, `format` defaults to `okf` and accepts `legacy`. |
| Repository | `repository` and `branch` default to unset, allowing the current repository and its default branch. `directory` defaults to `.agentflow/tasks` or `docs`. `publication` defaults to `pull-request` and accepts `push`. For knowledge, `format` defaults to `okf` and accepts `legacy`. |
| SecOps | `findings` defaults to Dependabot and code scanning. `min_severity` defaults to `high`. `repository` and the optional incident fixture start unset. See [SecOps configuration](secops.md). |
| Slack | `destination` and `credential_ref` default to unset. |
| Webhook and router | A webhook uses an environment credential reference and can declare safe rejection codes. A router maps named destinations and signals. See [notification settings](notifications.md). |

This configuration overrides one search property and sets a different provider
for the knowledge section. The search limit stays at 30, and Repository keeps
the current repository, its default branch, and the pull-request policy.

```yaml
agentflow: 1

task_management:
  provider: github-issues
  settings:
    search:
      open_only: false

knowledge_base:
  provider: repository
  settings:
    directory: docs/knowledge
```

Agentflow treats explicit `false`, `0`, empty strings, and empty lists as supplied
values. Optional fields such as `repository` accept `null` to clear the
selection. A provider change starts with the new provider's defaults and
validates its settings against that provider's schema.

## Deployment and release providers

`agentflow status` shows the newest matching deployment per environment. It uses
the open PR head SHA when available and local `HEAD` otherwise. GitHub deployment
entries retain `environment`, `ref`, `sha`, `created_at`, `state` and numeric `id`.
Select `none` to omit deployments from status.

`agentflow release get TAG` reads a published release from `devops.releases`.
Compare its `revision` with the deployment commit to correlate a release with
status across providers. GitHub lookup returns an `assets` array when the release
has attached files. Each entry has a `name`, `download_url`, `size` in bytes
and a `digest` when GitHub supplies one.
Set `settings.repository` on each GitHub role to choose a specific GitHub.com
`owner/name`. With the setting unset, agentflow uses the detected GitHub.com target.

This configuration uses two read-only fixtures in the local checkout. The
fixtures can be inspected without service credentials.

```yaml
code_hosting: {provider: none}
task_management: {provider: none}
knowledge_base: {provider: none}
devops:
  checks: {provider: none}
  deployments:
    provider: fixture
    settings: {path: .agentflow/deployments.json}
  releases:
    provider: fixture
    settings: {path: .agentflow/releases.json}
secops: {provider: none}
notifications: {provider: none}
```

The deployment file is a JSON array ordered newest first. Each record needs
`environment` and `revision` and can include `state` and `url`. Use the full
commit ID from `git rev-parse HEAD` as `revision` to see a record in `agentflow status`.
The release file is a JSON array with unique `tag` values, each with a `revision`
and optional `title`, `notes`, `url` and `assets`. Use `agentflow release get TAG` to read a
record. Setup readiness checks that both selected files can be read and decoded.

## Provider definitions

Provider settings can also come from layered definition files. Each layer
sets settings for a provider and role. Later layers win:

| Layer | Location | May set |
| --- | --- | --- |
| release | embedded in the agentflow binary | settings and constraints |
| project | `.agentflow/providers/*.yml` | settings |
| project | `.agentflow/config.yml` role `settings` | settings |
| user | `~/.agentflow/providers/*.yml` (or `$AGENTFLOW_HOME/providers`) | settings |

`.agentflow/config.yml` still selects the provider for each role. A definition
only applies when its provider is the one selected for that role. The shipped
release catalog is empty. Each layer accepts `.yml` and `.yaml` files, with one YAML
document per file and one definition per provider and role in that layer.
Legacy Jira projects and site seed Jira preferences before the release layer.
Confluence inherits the legacy Jira site unless its own legacy site is set.
Inline role settings take precedence over project definition files. The legacy
login and doctor commands still consult the top-level Jira configuration.

```yaml
# .agentflow/providers/issues.yml
version: 1
providers:
  github-issues:
    task_management:
      contract: tasks/v1          # optional; defaults to the current contract
      settings:
        labels: [agentflow]
        search: {limit: 50}
```

Settings merge the same way as `config.yml` settings: object keys merge one at
a time, and a list replaces the whole list. Each definition declares the role
contract it targets. agentflow accepts the current version of a contract and the
one before it.

The release layer can constrain any settings property for every later layer:

```yaml
# Embedded release definition
version: 1
providers:
  jira:
    task_management:
      settings: {site: https://nscale.atlassian.net, projects: [PLAT]}
      constraints:
        site: {lock: true}                  # no later change
        projects: {allow: [PLAT, INFRA]}    # values from this set only
  github-issues:
    task_management:
      settings: {labels: [triage]}
      constraints:
        labels: {lock: append}              # may add, never remove
        repository: {pattern: '^nscaledev/'}
```

Constraints apply to merged settings after each layer. Quote literal dots in
map keys with a backslash, for example `'transitions.pr\.merged'` and
`'hosts.forge\.example\.com'`. Use `\\` for a literal backslash.

Write authorization also checks targets supplied by the repository. Jira
requires the project to be in both the repository and effective project lists
when specified. An explicit empty list allows zero write targets. Project keys
are scoped to the repository's Jira site. Asana checks the default project
and the project keys in its transition map. GitHub Issues checks the resolved
repository, including template and qualified inputs. Confluence checks the
repository site and validates the requested space and page. Slack checks the
configured route, workspace, channel and optional actor before preparing
delivery. User defaults can supply an otherwise unspecified target. The same
checks apply when a native operation uses a CLI or REST fallback.

An invalid definition or constraint violation stops provider operations.
Core hooks and update policy still read the repository's commit rules,
protected branches and version settings from `.agentflow/config.yml`.
`agentflow setup providers --explain` (MCP: `agentflow_setup_providers` with
`explain: true`) lists the definition files loaded and every effective
setting of the selected providers, with the layer that set it and any
constraint. The MCP tool describes the running server’s configuration. Restart
the server after editing a definition. Each CLI invocation reads the current
files. Definitions cannot add new providers yet. That arrives with the
operation engine described in the [design](design/provider-definitions.md).

## Jira, Confluence and GitHub

Jira task operations use `task_management.settings.site`. The project list in
`task_management.settings.projects` scopes My Work, which looks in active sprints
first. Existing Jira keys, acceptance checklists, and per-project event and verb
mappings in `flow-map.yml` continue to work. A transition uses the destination
status name and rechecks the current status before writing. Jira issue context
includes web links from the issue and its parent. For a legacy API connection,
`agentflow login --site URL` stores its credential for the configured site.

Confluence search uses `knowledge_base.settings.site` and returns page IDs, space
keys, titles, URLs, excerpts and published revisions. Existing REST automation
can set `CONFLUENCE_USER` and `CONFLUENCE_TOKEN` together. With those variables
unset, the legacy adapter can use its Atlassian keychain entry. A shared site
also accepts the existing `JIRA_USER` and `JIRA_TOKEN` environment pair.

For GitHub pull requests, forge detection selects the source remote for the
push and the target repository for the PR. The source must have one GitHub.com
push destination matching its repository. Review thread replies verify the PR
repository before requesting approval. Jira write confirmations include the
effective Jira site, so switching sites requires fresh approval.

## Check workstation and runner readiness

`agentflow setup providers` lists every provider each role accepts, marks the
default and the provider this workspace selects, and describes the available readiness checks.
It reads local configuration only. `--json` and the `agentflow_setup_providers` MCP
tool return the same list.

| Readiness | How to verify |
| --- | --- |
| `built-in` | Setup checks the provider directly. |
| `client` | Use the client's Atlassian MCP for Jira or Confluence, or its Slack MCP for Slack. Jira and Confluence also support the signed-in Teamwork Graph CLI. Setup checks a matching declared companion when present. |
| `client-or-headless` | For Asana, configure a project and verify with the client's Asana MCP or a declared companion. Headless setup with an explicit `ASANA_ACCESS_TOKEN` probes the project directly. |
| `companion` | Setup needs a declared MCP companion for this provider. |
| `none` | The role is disabled. |

`agentflow setup plan --json` reports the selected roles, detected forge, installed
tools, and prerequisite checks. `agentflow_setup_plan` presents the same plan to an
MCP client. Use `--client NAME` to select a desktop client even if its executable
is absent from the current shell.

`agentflow setup verify` and `agentflow doctor --providers` report failed checks
as errors. The plan reports service-account prerequisites for headless runners.
`agentflow setup apply --client NAME` adds the project client entry after approval.
Readiness diagnostics identify missing companions and hosted MCP prerequisites.

### Interactive provider setup

The plan lists official provider tools in `provider_tools`. The MCP client
checks its own connected servers and uses the provider MCP when it supports the
requested operation. Jira and Confluence share the Atlassian connector. Slack
and Asana have their own OAuth connectors. GitHub has an official MCP and the
existing managed `gh` fallback. Atlassian's Teamwork Graph CLI is available
as a fallback for Jira and Confluence.

If a connector is absent, the setup agent offers to install or register the
official integration using the guide from the plan. When the provider reports
sign-in required, the agent starts the client's OAuth action, then waits for
the user to finish the consent flow. With Claude Code and Codex, it can use
the configured server's name:

```bash
agentflow setup login --provider jira --client claude-desktop --server atlassian
agentflow setup login --provider slack --client codex --server slack
agentflow setup login --provider jira  # starts the installed Teamwork Graph CLI login
```

The command delegates to the native tool and lets it store the login.
Use the host's Connect or Sign in action for clients without a native login
command. Retry the original operation in the chosen tool after sign-in. Jira and
Confluence require separate product access checks even with the same connector.
A permission denial calls for access to the affected product, project or space.

For Slack in Claude Code Desktop, run this from the workspace directory when
you want to register the connector and sign in.

```bash
agentflow setup login --provider slack --client claude-desktop --register
```

Agentflow checks `claude mcp list` for the server named `slack` or the official
endpoint. Use `--server NAME` to select an existing custom registration.
A connected entry is ready to reuse. An entry requiring authentication uses
Claude’s sign-in flow. If Claude reports a broken registration, inspect it
with `/mcp`. For an access denial, check workspace approval and access to
the requested Slack resource.

When the connector is absent, Agentflow registers `slack` with Claude’s local
workspace scope using the public OAuth client ID and callback port 3118.
Claude manages browser consent and credentials. If sign-in is deferred
because the command has no terminal, open `/mcp` in the same workspace or
rerun the command in a terminal. A connected status verifies the host
connection only. [Slack’s Claude Code configuration](https://docs.slack.dev/ai/slack-mcp-server/connect-to-harnesses/#connect-to-claude-code).

### GitHub OAuth in Claude Code

Prefer provider OAuth whenever supported. If the GitHub connector is absent
in Claude Code, approve the setup offer and let the agent run the provided
registration command. For Claude Code Desktop it is

```sh
agentflow setup login --provider github-issues --client claude-desktop --register
```

Agentflow installs the checksum-pinned official GitHub MCP release using the
GitHub CLI and registers it in the current Claude workspace. The local server
exposes read-only issue tools on github.com. In a fresh Claude Code session,
the first GitHub tool call opens the browser for consent. The official server
holds its OAuth grant in memory, so a new MCP process can ask for consent
again. The signed-in GitHub CLI can still handle reads when the MCP is
unavailable. [GitHub's local OAuth guide](https://github.com/github/github-mcp-server/blob/main/docs/oauth-login.md).

### Asana in Claude Code Desktop

For Asana in Claude Code Desktop, first reuse the registered V2 connector.
Run the command in the enrolled workspace so Claude starts its OAuth flow
when sign-in is needed.

```bash
agentflow setup login --provider asana --client claude-desktop --register
```

For a new connection, register `http://localhost:8080/callback` on an approved
Asana MCP app and add `--client-id ID` to the command. Claude prompts for the
app secret in the terminal, stores it in the system keychain, and manages browser
consent. The command uses the official Asana V2 endpoint and Claude’s local
workspace scope. Use `--server NAME` to select a particular V2 registration or
give a new registration a different name when a legacy connector already uses
`asana`. A workspace denial calls for app approval or project access.
[Asana’s Claude Code setup guide](https://developers.asana.com/docs/connecting-mcp-clients-to-asanas-v2-server#claude-code).

A standalone plan cannot list the tools or sign-in state of a sibling MCP
server owned by the host. Validate the available read capabilities in the
agent session. Interactive setup works without exporting personal credentials.

Agentflow routes Jira work and issue context, Confluence search, and individual
Asana and GitHub issue reads through the selected provider's MCP where supported.
Codex can call a provider tool directly through App Server. In Claude Code and
other MCP hosts, the Agentflow read can return `native_read_required`. The host
uses its existing provider connection for the exact requested tool and inputs,
then passes the result back to the original Agentflow tool in `native_results`.
Other clients can opt into the same flow with `provider_mcp: true`.

For supported providers the request includes `setup_offer` with official
connector details and `cli_fallback` when Agentflow implements a vendor CLI.
Resume with `unavailable` for a missing MCP or operation, `sign_in_required`
if connector OAuth cannot complete, `connection_failed` for transport
failures, or `oauth_app_restricted` for an explicit OAuth application approval
restriction. Use `access_denied` for a denied user or resource.

Agentflow checks the vendor CLI automatically and installs its pinned release
in the user cache if needed. It then tests access by retrying the same read
with the CLI's saved identity. The automatic installation covers GitHub CLI
and Atlassian Teamwork Graph CLI on macOS, Linux, and Windows (amd64 and
arm64). These are the providers with CLI adapters in Agentflow. Successful
fallback needs no intervention. If the CLI still needs sign-in, the agent can
start the vendor's OAuth flow, or offer MCP setup when appropriate.

Standalone workstation commands also prepare the CLI for supported task and
knowledge reads. CI and explicit service-account routes keep their existing
behavior.

Interactive GitHub API calls without a provider MCP use the signed-in `gh`
client. Explicit GitHub workflow tokens retain the existing service-account
path. When a provider rejects user or resource access, report the rejected
site or scope. When authentication is still required after fallback, start
the vendor's sign-in and retry after consent. Other existing REST or webhook
operations retain their current credential behavior during migration.
Agentflow-managed writes continue through its confirmation and audit policy.

Supported native writes use a connected Codex MCP client. On the CLI, use
`agentflow --native-client codex COMMAND` to select its provider connections.
Agentflow retains the OAuth session from preview until dispatch and binds the
approval to the exact operation, arguments and host. If the provider needs
sign-in, the setup agent launches its OAuth flow. After consent, request a
fresh preview. A permission denial or uncertain write stops the operation
on the selected route. Host-mediated read continuations also work in Claude
Code and other clients. Codex App Server binds writes to the provider connection it owns. Claude Code
Desktop can also use Agentflow-owned connections for unlinked Jira issue creation,
comments, start, forward transitions and web links, and for Confluence page
replacement. Separate Asana and Slack enrollments enable scoped Asana task
creation, completion and comments, and Slack delivery to a configured
conversation. Each preview identifies Agentflow as the connection owner.

| Provider | Native writes |
| --- | --- |
| Jira | Unlinked issue creation, comments, self-assignment, forward transitions and web links |
| Confluence | Version-checked replacement of an existing published page |
| Asana | Task creation, completion and comments |
| GitHub Issues | Untemplated issue creation, closing and comments. Existing-task template validation also uses the authenticated issue adapter when verifying raw issue body markers. |
| Slack | Top-level delivery to a configured workspace and conversation after scope verification |

For example, `agentflow --native-client codex comment APP-42 "Ready for review"`
uses the same task comment operation as `agentflow_comment`. To post Jira end-of-day
notes with the same client, run `agentflow --native-client codex eod post "APP-42=Reviewed the change"`.
When Agentflow prompts, review the target and text before approving. The CLI also
accepts `--yes` for that approval.

To add a Jira web link, use `agentflow_jira_remote_link` or
`agentflow --native-client codex task link APP-42 --url https://docs.example.org/runbook --title Runbook`.
Atlassian's full catalog endpoint, `https://mcp.atlassian.com/v2/mcp?tools=all`,
exposes the named Jira remote-link write capability. If that capability is
unavailable, Agentflow can use the signed-in Teamwork Graph CLI with a stable
remote link ID. A matching existing link returns `already_applied`.

For the Claude Code Desktop pilot, launch Agentflow's Atlassian OAuth setup from
the enrolled workspace and complete consent in the browser:

```bash
agentflow setup login --provider jira --client agentflow
```

The OS credential store holds the grant. The workspace configuration continues
to specify the Jira site and allowed projects. Reopen the session if it uses a
previous Agentflow binary. Run `agentflow_jira_remote_link` to preview the exact
target and connected owner before confirming. When no Agentflow enrollment
exists, the Jira link tool can use the signed-in CLI. To disconnect the local
Agentflow Rovo grant, run `agentflow setup logout --provider jira --client agentflow`.
Use Atlassian account settings if you also want to revoke the vendor's grant.

With that Jira enrollment, use `agentflow_ticket_create` to preview an unlinked
issue in an allowed project. Agentflow verifies that the requested standard issue
type exists in the project and binds the title, description and labels to one
approval. A successful result includes the new issue key. A request with a parent
or other issue link uses the existing linked-creation route. If the outcome is
uncertain, inspect the project before approving another creation.

When MCP creation is unavailable, the same interactive callers that can post
Jira comments can create an unlinked issue through a saved Teamwork Graph CLI
OAuth profile. Sign in with `agentflow setup login --provider jira`. For example,
on a branch without a Jira key, preview the draft with
`agentflow --native-client claude-desktop task create --project ENG --type Task --summary "Verify kiosk check-in" --dry-run`.
The ordinary `agentflow task create` command also supports this route in a terminal.

Agentflow looks up the project and standard issue type with `twg jira space issue-types`.
The confirmation binds both IDs, the selected profile and account, the exact title
and plain-text description, and the `agentflow` label. TWG trims surrounding
whitespace and expands account mentions in descriptions. Agentflow rejects those
transformations before approval. Confirmation spends one approval before running
`twg jira workitem create`. Agentflow checks the acknowledgement and reads the issue
back on the same site and profile to verify its key, creator and fields.

A sign-in or permission failure stops the operation. An uncertain create or
failed readback requires inspecting the project before approving again.
An explicit link or a link inferred from the current branch uses the existing
linked-creation route. Unattended calls, CI, operations with
`AGENTFLOW_OPERATION_ID`, and configurations with both `JIRA_USER` and
`JIRA_TOKEN` retain the existing REST fallback. The TWG create flow has been
exercised with offline fixtures and installed TWG 1.4.6 command help.
[Atlassian work-item reference](https://github.com/atlassian/twg-cli/blob/main/skills/twg-jira/references/workitems.md).

Use `agentflow_transition` to apply the flow-map event for an issue. For example,
`agentflow --native-client claude-desktop transition APP-42 pr.opened --dry-run`
shows the mapped destination. Omit `--dry-run` for the approval preview, which
binds the site, issue key, source and target statuses, and available transition
ID. Done issues, backward moves and issues already at the target skip the write.

Use `agentflow_start` or `agentflow --native-client claude-desktop start APP-42` to
preview the local branch or worktree, the authenticated Jira account and any
mapped start transition together. Both operations require confirmation even at
`auto`. Start creates the branch first, verifies self-assignment, then applies
the transition. An issue already assigned to that account skips the edit.
If a later step fails, the branch stays available and the error identifies
which Jira change was verified. Inspect the issue before approving more work.
A provider denial or uncertain result ends the operation on that connection.
The input schemas were captured from Rovo. The state reads and write outcomes
have been exercised with an offline provider.

With that Jira enrollment, `agentflow_comment` can add one standard issue
comment. The CLI uses the same grant and policy with
`agentflow --native-client claude-desktop comment APP-42 "Ready for review"`.
Agentflow checks the issue key and project on the enrolled connection.
Review the site, key and full text, then confirm even if remote autonomy is
`auto`. A successful result includes `commented: true` and `comment_id`.
The input schema comes from the Rovo flat catalog. The receipt is fixture-tested
and its hosted response shape still needs verification.

Each approval authorizes one append. Agentflow records a durable claim before
sending it. An old token cannot authorize a second post or a different route.
If the result is uncertain, inspect the issue before approving a new append.
A new approval can intentionally post the same text again.

When the MCP comment capability is unavailable, a single interactive
`agentflow_comment` or `agentflow comment` can use Atlassian's Teamwork Graph CLI.
Eligible callers are the supported named MCP clients, the CLI with an explicit
`--native-client`, and a terminal with interactive standard input.
Run `agentflow setup login --provider jira` to sign in with OAuth. Agentflow checks
a saved OAuth profile for the Jira site using `twg auth profile list` and
passes the configured site to every command. The subprocess receives no environment credentials.

Agentflow resolves the issue key and verifies its project before showing the
preview. Confirmation binds the CLI profile, site, project, issue key, numeric ID
and literal text, including the attribution when remote autonomy is `auto`.
The write uses `twg jira workitem comment create --issue-id ID --body TEXT --body-format plain`.
The CLI trims surrounding whitespace and expands Jira account mentions, so
Agentflow rejects these inputs before dispatch. A verified response includes
`comment_id`. Inspect the issue before approving again if the CLI fails
after dispatch or the receipt cannot be verified.
[Atlassian CLI command catalog](https://developer.atlassian.com/platform/teamwork-graph/twg-cli/commands/commands-catalog/).

An absent CLI permits the existing REST route. Sign-in and permission errors
stop on the selected route. CI, calls with `AGENTFLOW_OPERATION_ID`, unattended
CLI calls, and configured Jira service accounts retain the existing REST fallback. Batch EOD
posts and checklist updates keep their existing routing.

Confluence page replacement uses a separate enrollment. From the enrolled
workspace, run `agentflow setup login --provider confluence --client agentflow`
and complete Atlassian consent for Confluence read and write access. To disconnect
that profile, run `agentflow setup logout --provider confluence --client agentflow`.
Each provider has its own OS credential-store entry and approval generation.

Use `agentflow_docs_publish` with the page reference, expected revision,
replacement title and body, and stable operation ID. The Desktop candidate
uses the authenticated Rovo flat catalog to read the current page, resolve
its numeric space ID with `getConfluenceSpace`, update the page with its
snapshot token, and verify the successor version message. The space lookup
must return the same ID, the requested key and the configured site. A missing grant permits the existing
pre-dispatch fallback. An enrolled connection with a denied or incompatible
catalog stops the operation. Offline tests exercise the captured Rovo schemas and page, space and
history response shapes. Successful publication on a hosted tenant still
requires a confirmed write and a verified successor.

[Atlassian's tool catalog](https://developer.atlassian.com/cloud/rovo-mcp/guides/supported-tools/#how-the-server-exposes-tools)

### Agentflow-owned Asana writes in Claude Code Desktop

A workspace owner must approve an Asana V2 MCP app for the intended workspace.
For this Agentflow enrollment, register the exact callback
`http://127.0.0.1:53669/oauth/asana/callback`. Agentflow uses this app registration
for bound writes. Claude’s own V2 connector remains available for host-mediated
reads.

Run the following command to open browser sign-in. At the terminal prompts,
supply the approved local app registration and intended workspace GID. Review
the app and workspace in Asana. Agentflow holds the registration and refresh
grant in the OS credential store. The Asana task provider's repository settings
continue to specify the allowed projects.
[Asana V2 OAuth setup](https://developers.asana.com/docs/integrating-with-asanas-mcp-server).

```bash
agentflow setup login --provider asana --client agentflow
```

Use `agentflow_ticket_create`, `agentflow_comment`, or `agentflow_transition` in
Desktop. The CLI uses `agentflow --native-client claude-desktop task create` or
the same flag with `comment` and `transition`. Completion affects the whole
task. The confirmation shows the configured project and completion effect.
Agentflow verifies project membership, grant owner, and workspace before dispatch
and verifies the result on the same connection. A workspace denial stops the
operation. After sign-in or enrollment replacement, request a new preview.

The write schemas and receipts have been tested against provider fixtures.
Verification in an authenticated Asana workspace is pending.

Run `agentflow setup logout --provider asana --client agentflow` to remove local
enrollment. Existing unattended Asana service workflows use their configured
service authentication.

### Agentflow Slack enrollment

For desktop browser consent, an administrator provisions a dedicated Slack app
approved for internal use or published in the Slack Marketplace. In the app’s
**OAuth & Permissions** settings, enable **PKCE** (`oauth_config.pkce_enabled`
in the manifest) and register the exact redirect URL
`http://localhost:53668/oauth/slack/callback`. Slack treats a localhost redirect
as a desktop redirect when the app has opted into PKCE. Enabling PKCE converts
the app to a public client and requires Slack Support to reverse. Use this app’s
fixed public client ID for Agentflow. Claude’s workspace connector has its own
registration and callback. [Slack PKCE settings](https://docs.slack.dev/authentication/using-pkce/) · [Slack MCP app eligibility](https://docs.slack.dev/ai/slack-mcp-server/).

Configure the following **user scopes** on the approved app. Agentflow requires
the returned scope set to match the request exactly.

| Operation | Scopes |
| --- | --- |
| Verify the authenticated Slack profile | `users:read`, `users:read.email` |
| Verify membership in the configured conversation | `channels:read`, `groups:read`, `im:read`, `mpim:read` |
| Request permission to send a message | `chat:write` |

Give the agent the approved app’s public client ID and the expected team ID
from the destination’s `native.workspace_id`. An authorized agent or the user
runs this command on the machine where the browser will open, substituting
those two IDs.

```sh
agentflow setup login --provider slack --client agentflow --public-client-id CLIENT_ID --team-id TEAM_ID
```

Review the app, workspace and user permissions in Slack and approve in the
browser. Agentflow listens on IPv4 loopback during login, checks the callback
authority, path and random state, and exchanges the code with its S256 verifier.
The public exchange and refresh use the client ID alone as the app identifier.
The user needs neither a client secret nor a locally trusted certificate.

Slack must return the exact requested scopes and a `team.id` matching the
expected workspace on initial consent. Agentflow verifies the authenticated
user and workspace with Slack’s `auth.test` when the public exchange returns
a generic bearer response.
New consent replaces the per-user enrollment in the OS keychain and invalidates
previews bound to the prior grant. Enable token rotation in the app settings
if rotating tokens are required for this localhost registration. Slack’s
unconditional rotation guarantee applies to custom URI schemes. For rotating
grants, Agentflow verifies each replacement and preserves the approval generation.
An expired refresh token or an ambiguous exchange requires fresh consent.
[Slack user token response](https://docs.slack.dev/reference/methods/oauth.v2.user.access/) · [Slack identity verification](https://docs.slack.dev/reference/methods/auth.test/) · [Desktop PKCE and refresh](https://docs.slack.dev/authentication/using-pkce/) · [Token rotation](https://docs.slack.dev/authentication/using-token-rotation/).

For a separately approved **confidential app**, register
`https://127.0.0.1:53668/oauth/slack/callback` and run
`agentflow setup login --provider slack --client agentflow` in a terminal.
At the hidden prompts, enter the paths to the PEM certificate chain trusted by the browser
for `127.0.0.1` and its matching private key, and the app’s client ID and
secret. Use a dedicated loopback certificate and keep the app confidential.

Run `agentflow setup logout --provider slack --client agentflow` to delete the
local app credentials and grant. To revoke the app's authorization in Slack,
remove its installation or authorization through Slack.

### Claude Code Desktop

The `claude` setup mode configures the Claude Code plugin marketplace. If a
trusted desktop session still lacks the agentflow tools, register the project
server from the enrolled checkout in a POSIX launcher environment.

```bash
agentflow setup plan --client claude-desktop
agentflow setup apply --client claude-desktop
agentflow setup verify --client claude-desktop
```

Setup adds `mcpServers.agentflow` to the checkout's `.mcp.json`. It uses `sh`
and Git to find the checkout's executable `.agentflow/bin/agentflow` and run
`mcp --client claude` over stdio. Restart Claude Code Desktop, open the trusted
project, and approve the server if prompted. Call `mcp__agentflow__agentflow_setup_plan`
with `{"client":"claude-desktop"}` and compare the result with
`agentflow doctor --providers --client claude-desktop --json`.
For a configured knowledge provider, search
for a known document with `mcp__agentflow__agentflow_docs_search` and the same
query in `agentflow docs search`.

From an existing MCP session, pass `{"client":"claude-desktop"}` to
`agentflow_setup_plan` and `agentflow_setup_apply`. Review the apply preview and
repeat with its confirmation token. Keep the generated
`.mcp.json.agentflow-companions.json` receipt with the project file.
`agentflow remove` uses it to remove an unchanged launcher that setup wrote.
An edited launcher or a matching entry supplied by the team stays in place.
On native Windows, configure the project server manually with the verified
`agentflow.exe` and arguments `mcp --client claude`.

### Install a pinned gh companion

On native macOS and Linux, providers that need GitHub CLI first try
`gh --version` on PATH. If that check fails, setup looks for
`companions.yml` in your agentflow home (normally `~/.agentflow`, or an absolute
`AGENTFLOW_HOME`). Keep this directory outside the checkout, under stable
directories you trust. The directory and manifest must be owned by your user
and writable only by you. Use an ordinary directory and a regular manifest file.

Supply one pin per OS and architecture. Use the SHA256 of the **raw executable**
from a source you trust. If your distributor supplies an archive, verify and
extract it yourself, then pin the executable bytes. This example shows the
shape of a local pin. Replace the path and digest with your own values.

```yaml
gh:
  - platform: {os: linux, arch: amd64}
    version: "2.70.0"
    source: file:///opt/approved-tools/gh-2.70.0-linux-amd64
    sha256: "<64 lowercase hexadecimal digits>"
```

An absolute local file URL or a direct HTTPS URL serving the raw executable
is supported. HTTPS sources must respond directly with HTTP 200. Source URLs
must be free of credentials, query strings and fragments. Repository provider
settings select services. The user manifest supplies artifacts for the built-in
`gh` command.

1. Run `agentflow setup plan --json` or call `agentflow_setup_plan`. Review
   `install_gh` for the source, version, platform, digest and destination home.
   Planning reads local state and leaves the source unopened.
2. Run `agentflow setup apply --install-gh` and confirm, or pass `--yes` for
   noninteractive approval. In MCP, call `agentflow_setup_apply` with
   `{"install_gh":true}`, review the returned summary, then repeat the call with
   its `confirm_token` after approval. The local autonomy setting applies.
3. Read the returned readiness checks. The installer verifies the digest before
   publishing. Setup then checks the executable, authentication and selected
   GitHub reads. If authentication is needed, start browser login using
   the verified path shown in the plan with `auth login --hostname HOST --web`.
   Approve project registration separately with `setup apply --client NAME`.

The cache lives under `AGENTFLOW_HOME/companions/<os>-<arch>/gh/`. Agentflow
rechecks the installed digest against the current pin and invokes the
version-and-digest-specific path for provider probes and app token lookup.
An already verified copy works with the source offline. `GH_TOKEN` and
`GITHUB_TOKEN` retain priority for app token lookup. Shell PATH and unmanaged
tools stay as configured. The project launcher and agentflow update verification
still use the system `gh` for release bootstrap.

### Register a provider MCP companion

Create `mcp-companions.yml` in your agentflow home, outside the workspace, under
stable directories you control. The home and manifest must be ordinary,
user-owned filesystem objects. On macOS and Linux, remove group and other
write permission from both. On Windows, use a local NTFS directory under
your profile with write access limited to you, Administrators and SYSTEM.
Agentflow checks the owner and DACL before using the declaration.

Each ID identifies one companion. The `providers` list selects the provider
IDs that need it. A shared companion is checked once when several roles use
it. The example configures a local document server for FileSystem and a hosted
tool server for Jira or Confluence. Replace the sample endpoint, executable
source and digest with values from the distributor you trust.

```yaml
mcp:
  - id: documents
    providers: [filesystem]
    kind: stdio-mcp
    args: [stdio]
    auth_env: DOCS_API_TOKEN
    artifacts:
      - platform: {os: linux, arch: amd64}
        version: "1.4.0"
        source: file:///opt/approved-tools/document-server
        sha256: "<64 lowercase hexadecimal digits>"
  - id: work-items
    providers: [jira, confluence]
    kind: remote-mcp
    endpoint: https://tools.example.org/mcp
    auth_env: WORK_ITEMS_TOKEN
```

Use one raw executable pin per native platform as in the gh example. Windows
pins use `os: windows`, raw PE executables and an HTTPS source or a local
URL such as `file:///C:/Approved/document-server.exe`. Supported optional
launch arguments are `mcp`, `serve`, `stdio`, `--stdio`, `--transport` and
`--transport=stdio`. Supply arguments in the order your server expects.
The verified binary is published under
`AGENTFLOW_HOME/mcp-companions/<os>-<arch>/<id>/`. The installer locks the
cache and atomically selects a digest-specific file. Windows uses a
`current.txt` selection and an `.exe` suffix. A new pin can be installed
while the client still uses the previous version.

Use a direct HTTPS Streamable HTTP endpoint without URL credentials, a query
or a fragment. The optional `auth_env` names a variable available to setup
and the target editor process. A local probe passes this variable to the
server. A hosted probe sends it as a bearer token to the declared endpoint
and refuses redirects. Configure the credential through the operating
environment on the machine or remote development host running the client.
A GUI process may have a different environment from a terminal.

1. Run `agentflow setup plan --companion documents --json`. Review the version,
   source, digest and destination. Approve the install with
   `agentflow setup apply --companion documents --install`. Hosted companions
   start at the verification step.
2. Run `agentflow setup verify --companion documents` or
   `agentflow setup verify --companion work-items`. Both transports negotiate
   an MCP session and request `tools/list`. The check discards tool metadata
   and server diagnostics. A missing credential or a failed handshake or list
   request gives a nonzero exit code.
3. Review `agentflow setup plan --companion documents --client vscode --json`.
   Approve `agentflow setup apply --companion documents --client vscode` and
   rerun `agentflow setup verify --companion documents --client vscode`. Pass
   `--client cursor`, `--client claude` or `--client gemini` for those clients.
   The CLI accepts `--yes` for explicit noninteractive approval. Reload the
   client and complete its project trust prompts.

Registration reserves `agentflow-<id>` in one project file.

| Client | Project file | Credential reference |
| --- | --- | --- |
| VS Code | `.vscode/mcp.json` | `${env:NAME}` |
| Cursor | `.cursor/mcp.json` | `${env:NAME}` |
| Claude Code (`claude` or `claude-desktop`) | `.mcp.json` | `${NAME}` |
| Gemini CLI | `.gemini/settings.json` | `${NAME}` |

Review any pending replacement of that key. The merge preserves other entries,
including the existing `agentflow` launcher. It writes the absolute verified command or the declared
URL and an environment reference. The resolved credential stays in memory.
A matching entry makes repeat apply a no-op. Re-register after a pin,
endpoint or credential reference changes. An absolute native path is specific
to the host where the editor launches servers. See the editor configuration
references for environment interpolation and project file scope.
[VS Code MCP configuration](https://code.visualstudio.com/docs/agents/reference/mcp-configuration),
[Cursor MCP configuration](https://cursor.com/docs/mcp),
[Claude Code project scope](https://code.claude.com/docs/en/mcp#project-scope)
and [Gemini CLI configuration](https://geminicli.com/docs/reference/configuration/).

`agentflow_setup_plan` accepts `companion` and an optional `client`.
Use `agentflow_setup_apply` with `{"companion":"documents","install":true}`
for installation or `{"companion":"documents","client":"vscode"}` for
registration. Review each summary, then repeat the same request with its
`confirm_token`. Each approval covers one action and its reviewed inputs.
Omit `companion` from `setup verify` or use `doctor --providers` to check
all selected providers and their declared MCP prerequisites.

### Companion cleanup and repair

When setup writes a companion entry, it saves the entry name and a SHA256
digest beside the project config, for example in
`.vscode/mcp.json.agentflow-companions.json`. Keep the receipt with the
corresponding config. A matching entry already present at setup time keeps
its existing ownership.

Run `agentflow remove --dry-run` to preview removal for the project. Then run
`agentflow remove` to delete recorded entries whose full JSON value still
matches and discard their project receipts. Both commands work offline.
Even with `--force`, edited entries and entries without a receipt stay in
the client config. For registrations made before receipts were introduced,
review the command or endpoint in the project file listed above and delete
only the server entry you recognize. The user manifest, cached executables
and active version selections remain available to other projects.

To repair a registration, run `agentflow setup plan --companion ID --client NAME`,
review the proposed change, then run `agentflow setup apply --companion ID --client NAME`.
This updates the selected entry and its receipt. Use the separate `--install`
action when the shared executable itself needs repair.

## GitHub Issues task lifecycle

Use the `github-issues` task provider with the detected GitHub repository or set
`settings.repository` to `owner/repo`. The current GitHub Issues adapter uses
GitHub.com. Task references appear in branch bindings and explicit `Task-Ref`
trailers. Existing commit rules still apply, including when the repository
explicitly requires Jira-style subject scopes.

In Claude Code Desktop, call `agentflow_ticket_create` with the issue summary and
description. When the interactive create uses the saved GitHub CLI login, its
confirmation summary includes `backend: gh`, the GitHub account ID and login,
the repository, and the proposed issue. After approval, submit the same inputs
and `confirm_token`. Agentflow checks the CLI account again when validating the
confirmation. Each API command uses the account active in `gh` when it runs.
For sign-in, run `agentflow setup login --provider github-issues` from the
checkout and complete GitHub’s browser flow. Setup uses the same working PATH
or verified managed executable as the issue adapter.

`settings.transitions` defaults to closing issues on `pr.merged` and `#done`.
Set an event to `none` to disable its effect. Agentflow previews transitions and
only attempts to move open issues forward.

## Asana project tasks

Connect the Asana MCP for supported interactive operations. The existing
service adapter uses `ASANA_ACCESS_TOKEN` for operations such as project
listing and section moves. Quote GIDs in YAML. A task can belong to several projects, each with its own
current section and event mappings.

```yaml
task_management:
  provider: asana
  settings:
    project: "100"
    transitions:
      "100":
        branch.created: "101"
        pr.opened: "102"
      "200":
        pr.opened: "202"
knowledge_base:
  provider: filesystem
  settings: {root: docs}
```

`agentflow work` lists up to 30 incomplete tasks in the default project, including
unassigned work. Use `agentflow context 900` for task GID `900` in that project,
or `agentflow context asana/200/900` for its membership in project `200`. The
default project and the keys in `transitions` identify configured projects.
An empty event map enables a project for lookup and creation. Branch bindings,
PR bodies and commit trailers use the qualified form, e.g. `Task-Ref: asana/100/900`.

`agentflow start 900` creates a branch or worktree and applies the project's
`branch.created` mapping if configured. Preview a later move with
`agentflow transition 900 pr.opened --dry-run`. The adapter checks task and
destination section memberships before moving the task. Unmapped events and
`none` have no effect. Mapping an event to `completed` completes the **whole
task across all its projects**. Completion is terminal. Section GIDs have
no inherent order. The Asana MCP supports top-level comments on scope-verified
tasks. After an uncertain create response, check the project before requesting
a new creation.

`agentflow setup verify` probes read access to each configured project's task
list and checks that each destination section belongs to the intended project.
Write access depends on the Asana identity's permissions.

Asana maps template scope to project, title to name, and description,
sections and checklist items to plain-text notes. Qualified links and the
exact template pin occupy a reserved `agentflow-task-v1: ` JSON line at the
start of the notes. Keep this line when editing task content. Before a state
change agentflow rereads and validates the stored revision and current fields.
A task without a pin keeps its native behavior. Asana rejects templates that
require provider-specific types, priority or labels, and rejects populated
defaults that its adapter cannot carry.

To link a managed document, pass `--link-to filesystem/design/capacity` or
set a template default such as
`"links":[{"ref":{"instance":"filesystem","scope":"design","id":"capacity"}}]`.
An explicit link takes precedence over template defaults. Context resolves
the qualified reference through the selected knowledge provider. Confluence,
FileSystem, Repository and GitHub Wiki support that lookup. When lookup fails, context
includes the reference and a warning. Link resolution uses the provider,
scope and ID, independent of the document's URL.

## Task templates

Asana, GitHub Issues, FileSystem and Repository support task templates. Configure a
JSON catalog file relative to the checkout and an optional default template.
Use an exact name and revision for a reproducible task definition.

```yaml
task_management:
  templates:
    catalog: .agentflow/task-templates.json
    default:
      name: engineering-task
      revision: "1"
```

The templates section is unset by default. If it is present, `catalog` is
required and `default` may be omitted. The catalog defines each revision's
required fields, defaults, body sections, and acceptance checklist. See the
[task template format](../internal/tasktemplate/README.md).

Use the same create operation from a terminal, a runner or an MCP client.

```sh
body='## Objective
Check remaining cluster capacity.

## Scope
Review the production pool.

## Acceptance Criteria
- [ ] Record available GPUs.

## Verification
Compare the inventory against scheduler metrics.'

agentflow task create --summary "Review capacity" --description "$body" \
  --template engineering-task --template-revision 1 --dry-run
agentflow task create --summary "Review capacity" --description "$body" \
  --template engineering-task --template-revision 1 --yes
```

Supply required section content with `--description`. The preview shows the
resolved draft and its pin. For MCP, call `agentflow_ticket_create` with
`summary`, `description`, `template` and `template_revision`. Review the
returned summary, then repeat the call with the same inputs and its
`confirm_token` after approval. Omit both template fields to use the repository
default. A template can supply the title. Existing Jira creation accepts its
usual project, type, summary, description and linked key. It reports template
selection as unsupported.

Headless CLI writes require `--yes` even at `auto` autonomy. With `suggest`
autonomy the CLI prints the draft and exits with an error on a create request.
`--dry-run` succeeds for a valid preview at any autonomy level. Use the
provider's retry behavior when resubmitting an interrupted create.

GitHub Issues resolves the configured default when creating an issue.
The confirmation shows the selected revision, repository, title, Markdown body
and labels. A template can require any of those four issue fields. Type,
priority and structured links are unsupported. Template defaults for repository
and labels take precedence over the provider settings. An explicit repository,
title or description takes precedence over the template default.

Agentflow stores the exact template name and revision in a leading HTML comment.
Keep that marker and the corresponding catalog revision when editing the issue.
Before posting a comment or changing state, agentflow validates the current issue
against that revision. Issues without a template marker keep their existing
workflow even when a default is configured later.

FileSystem and Repository use the same catalog and default for Markdown tasks.
Templates can supply or require `scope`, `title`, `description`, `type`,
`priority`, `labels` and structured `links`. Sections and checklist items go
in the Markdown body. A supplied project, summary, description or type takes
precedence over its template default. A linked task (explicit or bound to the
branch) takes precedence over default links. When neither the caller nor the
template supplies a scope or type, creation uses the configured scope and
the type `Task`.

The create preview includes the resolved draft and pin. The approved record
stores the pin in version 1 YAML front matter, for example
`template: {name: engineering-task, revision: "01"}`. Retain that catalog
revision while tasks reference it. Before a configured transition, agentflow
validates the current fields, sections and checklist against the stored
revision and checks the approved file revision before writing. Older tasks
without a pin continue through their original transition flow.

## File-backed tasks

Select `filesystem` or `repository` under `task_management` to create, list,
read and transition Markdown tasks. The default scope is `default`. Set
`settings.scope` to choose the scope for My Work and new tasks, or pass a
project when creating a task to override its scope. My Work lists up to 30
open tasks in that scope. Both adapters lack an authenticated assignee.

```yaml
task_management:
  provider: filesystem
  settings:
    root: .agentflow/tasks
    scope: platform
```

FileSystem resolves a relative root from the checkout and accepts an absolute
directory. The first approved create makes the directory if needed. Writes
use local autonomy. Ticket creation always requires confirmation.

Use the qualified key returned by create or My Work when requesting context
or a transition. For example, a commit body or PR body can contain
`Task-Ref: filesystem/platform/quota`. This identifies the record by its
front matter and survives a file rename. A feature branch can recover the
reference from its commits. The commit subject and ordinary issue mentions
do not bind a task. The `transitions` setting uses the GitHub Issues event
mapping above. File tasks start open and a close marks them terminal.
Run `agentflow start <qualified-key>` to bind a local branch to the task under
local autonomy. Use `--worktree` for a linked checkout. To share a FileSystem
task directory across worktrees, configure an absolute root. Start records the
binding in local Git configuration. The file-backed adapters do not support
assignment or comments.

For Repository, set `repository` to a Git remote name, URL or local repository
path. Relative paths resolve from the checkout. The defaults use `origin`
and the remote HEAD. Set `directory` to the record directory within the
branch tree. Reads always use this base branch.

Repository creates and transitions require remote confirmation, including at
the `auto` level. The preview identifies the origin, directory, base and push
target. The default `publication: pull-request` requires a GitHub.com origin
and GitHub authentication. Approval publishes a review branch and opens or
reuses its PR. The result carries `status: pending_review`, the qualified task
`key`, PR `url` and review `branch`. A pending transition returns
`applied: false`. The new task or state becomes visible once the PR is merged.

For create retries, reuse the same `AGENTFLOW_OPERATION_ID` and task inputs.
An interactive PR preview includes a generated `operation_id` when no
operation ID is supplied. Use that ID to reconcile an interrupted publication
with fresh approval. For direct publication, set `publication: push` and
choose an unprotected target branch. Protected push targets are rejected in
both modes.

## Agentflow repository pilot

This repository selects the `engineering-task` template at revision `1`
for task creation. Its catalog lives in [.agentflow/task-templates.json](../.agentflow/task-templates.json).
The template requires Objective, Scope, Dependencies, Acceptance Criteria,
and Verification sections. It supplies a default statement for Dependencies.

The pilot uses GitHub Issues for tasks and the Repository provider for the
[Agentflow OKF bundle](knowledge/index.md). The `provider-pilot.yml` workflow calls
`task-events.yml` when a same-repository PR targeting `main` opens, reopens,
becomes ready for review, or closes. The preview checks out the PR base SHA and uses read-only contents
and issue permissions. A draft review event, an unmerged close, or a PR with
`[agentflow skip]` produces no transition. Fork PRs skip both jobs.

To enroll one issue, set the repository variable `AGENTFLOW_PROVIDER_PILOT_ISSUE`
to its decimal number. Link it on its own line in the PR body, for example
`Task-Ref: github-issues/ryanhelms%2Fagentflow/25` for issue 25. Only a
default-branch merge whose preview resolves to that single issue can start
the issue-write job. It builds agentflow from the reviewed merge commit, checks
the configured knowledge provider for `repository/engineering/provider-architecture`,
and revalidates the event before closing the issue. Rerunning the job while
the issue is closed reports `applied: false`.

Clear `AGENTFLOW_PROVIDER_PILOT_ISSUE` to stop the writer and keep previews.
Set the repository variable `AGENTFLOW_ENABLED=false` to skip both jobs on
subsequent runs. Agentflow releases continue through GoReleaser.

## Link knowledge to a GitHub issue

During issue creation, pass a qualified document reference with `--link-to`
or the MCP `link_to` field. Agentflow validates it against the selected knowledge
provider and adds it to the issue body before showing the approval preview.
A task reference in the same field still links the new issue to that task.

For an existing issue, put a top-level section in the issue body and list one
qualified document per bullet. This example uses the Repository knowledge provider.

```markdown
## Related Knowledge

- repository/engineering/quota
- repository/engineering%2Fplatform/scheduling
```

Use the literal heading and unindented `- ` bullets. Each bullet takes a bare
reference or a reference enclosed in backticks. Use the escaped
`instance/scope/id` form that Agentflow prints. For example, `%2F` represents
a slash within a scope or ID. The next heading beginning with `#` ends the list.
Issues without the section retain their existing context.

During review, run `agentflow context 'owner/repo#42'` or call `agentflow_context`
with the issue key. On a review branch with the issue's `Task-Ref` trailer,
you can omit the key. Context lists each resolved document once, in issue
order. FileSystem reads the configured root. Repository reads the configured
directory on the base branch. Both match the provider, scope and ID exactly.
Context warns about invalid entries, provider mismatches and missing targets.
Markdown links and page content are reference data. The configured provider
supplies the lookup destination.

Read the full document by passing its listed reference to `docs get`.

```sh
agentflow docs get repository/engineering/quota --json
```

The equivalent read-only MCP call is `agentflow_docs_get` with
`{"ref":{"instance":"repository","scope":"engineering","id":"quota"}}`.
Both return the document's identity, title, body and revision when available.

## Search a local knowledge directory

Choose a dedicated directory for knowledge records. Relative roots resolve from
the repository checkout. Use `agentflow docs search quota --space engineering` to
search the scope in the following example.

```yaml
knowledge_base:
  provider: filesystem
  settings:
    root: handbook
```

Search also finds existing legacy records such as this Markdown file in `handbook/`.

```markdown
---
version: 1
kind: document
instance: filesystem
scope: engineering
id: quota
title: Quota design
---
The decision and its rationale.
```

The document's qualified reference survives a rename. Generated setup files
and authored records have separate lifecycles during removal.

## Read an OKF knowledge bundle

The FileSystem and Repository knowledge providers can search [OKF v0.3
bundles](https://github.com/open-knowledge-format/open-knowledge-format/blob/527e81aff55992be21507e97004286789126f88c/SPEC.md)
and legacy Agentflow document records together. Point FileSystem at the bundle's
`root`, or use the bundle as Repository's `directory` on its configured branch.
Search and task document lookups use the same provider configuration.

Within an OKF bundle, Agentflow discovers nested Markdown concepts with a `type`
in their YAML frontmatter. Use the first directory as the search scope, or
`default` for a concept at the bundle root. The unmanaged document ID is its
bundle-relative path without `.md`. For example, its qualified Repository
reference might be `repository/engineering/engineering%2Fquota`. `index.md`
and `log.md` are bundle navigation and history files.

A managed concept can pin a scope and ID with
`x-agentflow-ref: {scope: engineering, id: quota}`. Moving that file preserves its
qualified Agentflow reference. Duplicate references are reported as conflicts.
A migration can carry existing publication receipts in `x-agentflow-operations`.

### Publish and update concepts

For FileSystem and Repository knowledge, `knowledge_base.settings.format`
selects the encoding for new documents. The default `okf` writes Markdown
with YAML frontmatter and a concept type of `Document` when you omit the type.
Set `format: legacy` to create Agentflow v1 records. Task management and GitHub
Wiki keep their existing schemas.

```bash
agentflow docs publish filesystem/engineering/quota \
  --title "Quota policy" --body "Each team owns its quota." \
  --type Policy --description "How teams allocate GPU quota" \
  --tags '["gpu","capacity"]' --operation-id quota-policy-1
```

For MCP, pass the optional strings `type` and `description` and the string
array `tags` to `agentflow_docs_publish`. These fields require an OKF document.
The approval shows the configured format for new documents, the destination,
and the requested changes. GitHub pull-request publication uses the saved `gh`
sign-in and Git’s configured access to the repository.

To update a document, supply its current revision from search. An update uses
the file’s existing encoding even when the configured default differs. OKF
updates preserve omitted concept metadata and unknown YAML fields. Pass an
empty tags array to clear the tags. Retry the same request with the same
operation ID.

To convert a legacy record, migrate it explicitly in a reviewed change.
Carry its scope and ID into `x-agentflow-ref`, its operation receipts into
`x-agentflow-operations`, and its structured links into `x-agentflow-links`.
Preserve its title and Markdown body and choose a concept type. See the
[OKF rollout plan](okf-rollout.md) for the reader verification and migration order.

## Replace a Confluence page

With `knowledge_base.provider: confluence`, search for the current page ID,
space key and revision. For example, to replace published version 7 of page
42 in the ENG space, supply the complete new title and body. To restrict
publication across all three transports, set `knowledge_base.settings.publish_spaces`
to a list such as `[ENG]`. An explicit empty list disables publication. If the
setting is omitted, the qualified reference selects the space and the provider
verifies page membership. A repository allow-list binds its space keys to the
repository site. User settings may narrow the list. Each publication must
satisfy both lists and the release constraints.

```bash
agentflow docs search quota --space ENG --json
agentflow docs publish confluence/ENG/42 \
  --title "Quota policy" --body "Each team owns its quota." \
  --expected-revision 7 --operation-id quota-policy-1
```

The CLI and `agentflow_docs_publish` apply the remote autonomy policy. An
interactive write can use the configured Codex MCP or a separately enrolled
Claude Code Desktop Confluence connection. The signed-in Teamwork Graph CLI
is available when native preparation permits fallback. Confirmation
covers the transport, configured site, qualified reference, expected revision, operation ID
and complete replacement. Agentflow checks the space key immediately before the
update and verifies the space ID in the response. Confluence uses the page
version as its write precondition. In this example the requested version is 8.
If another edit wins, review the newer version before publishing a replacement.

Retry an uncertain result with the same operation ID, revision, title and body.
Agentflow checks the message on the exact successor version, including in page
history after later edits. A matching receipt returns `already_applied` with
the original published revision. An inaccessible version history can prevent
verification. For a new edit, use a new operation ID and the current revision.

Lookup renders readable text from the page's Atlassian Document Format.
Publication escapes Markdown and HTML as literal text and preserves line breaks.
Replacing the body removes its previous rich formatting and embedded macros.
Structured `links` are unsupported. Parent, owner, labels and properties are
outside the update. Confluence reconciles the published body with open drafts
and may overwrite a diverged draft. Create the page in Confluence first to obtain
its assigned numeric ID. CI docs sync and release notes use their existing
Pandoc publishing path. [Confluence page API](https://developer.atlassian.com/cloud/confluence/rest/v2/api-group-page/#api-pages-id-put)

## Publish a Repository document

Select `knowledge_base.provider: repository` and configure the destination
directory. Supply a Git origin when publishing outside the current repository.
Use `publication: pull-request` for GitHub review or `publication: push` for
an explicitly selected unprotected branch.

```bash
agentflow docs publish repository/engineering/quota \
  --title "Quota policy" --body "The policy and its rationale." \
  --operation-id quota-policy-1
```

Pass the revision from search when updating an existing document. Reuse the
operation ID for retries. The CLI and `agentflow_docs_publish` present the
resolved destination and document for approval and record the outcome.

Use `agentflow docs get repository/engineering/quota --json` to check that exact document in CI.

## Publish a GitHub Wiki page

Enable Wikis in the target repository's GitHub settings and create the first
page in GitHub's web UI. Configure Git's HTTPS credential helper for any reads
that need authentication and for writes to
`https://github.com/owner/name.wiki.git`. Use credentials with permission to
edit the wiki when publishing.
Use the direct GitHub wiki remote so the configured URL and Git's push
destination match.

```yaml
agentflow: 1
knowledge_base:
  provider: github-wiki
  settings:
    repository: docs-team/handbook
```

Omit `settings` to use the detected GitHub.com target. In a fork checkout this
uses the upstream target when forge detection selects it. You can set
`code_hosting.settings.target_remote` to select a different remote.

Agentflow publishes directly to the wiki's default branch. The resolved wiki
remote and branch appear in the confirmation summary, and the remote
autonomy policy and `policy.protected_branches` apply. If your wiki uses
`master` and your code repository doesn't use that branch, you can set
`policy.protected_branches: [main, develop, 'release/*']` to keep those code
branches protected while allowing Wiki publication.

```bash
agentflow docs publish github-wiki/engineering/quota \
  --title "Quota policy" --body "The policy and its rationale." \
  --operation-id quota-policy-1
agentflow docs search quota --space engineering
```

The equivalent MCP tools are `agentflow_docs_publish` and `agentflow_docs_search`.
Search and publication operate on managed Markdown under `agentflow-pages/`.
Use the same operation ID to retry a publish. To update a page, use a new
operation ID and pass the revision from search with `--expected-revision`
or the MCP field `expected_revision`. Ordinary wiki pages such as `Home.md`
are edited in GitHub. Publish the body under a new qualified reference to
adopt an existing page as a managed document.
