# Provider configuration

Devflow resolves provider settings from distinct sections in `.devflow/config.yml`.
Each section has a versioned schema. A value in `settings` overrides the selected
provider's default for that field. Omitted fields inherit their defaults, nested
objects merge by field, and supplied lists replace the default lists.

This reference describes the configuration accepted by the provider foundation.
The provider-specific sections below describe the CLI, MCP and workflow entry points.

| Section | Default provider | Other schema selections |
| --- | --- | --- |
| `code_hosting` | `auto` | `github`, `none` |
| `task_management` | `jira` | `asana`, `github-issues`, `filesystem`, `repository`, `none` |
| `knowledge_base` | `confluence` | `filesystem`, `repository`, `github-wiki`, `none` |
| `devops.checks` | `github` | `none` |
| `devops.deployments` | `github` | `fixture`, `none` |
| `devops.releases` | `github` | `fixture`, `none` |
| `secops` | `github` | `none` |
| `notifications` | `slack` | `webhook`, `router`, `none` |

Each selection accepts `version` (default `1`), `provider` (see the table),
and `settings` (defaults below). The enclosing `devops` section also accepts
a schema version (default `1`). A provider selection of `none` has an empty
settings object. Validation errors identify the property to correct.

| Provider | Settings and defaults |
| --- | --- |
| Code hosting | `hosts` defaults to an empty hostname-to-forge map. `source_remote` and `target_remote` default to unset, so detection can select Git remotes. |
| Jira | `site` and `projects` inherit the legacy Jira settings and defaults. |
| Asana | `project` is the default project GID. `transitions` maps each project GID to its events and destination section GIDs. Both start unset. |
| Confluence | `site` inherits the legacy Confluence site, which defaults to the Jira site. |
| GitHub | An unset `repository` defers to Git remote detection. |
| DevOps fixture | Set `path` to a repository-relative JSON file for each role. |
| GitHub Issues | `repository` defaults to unset. `labels` defaults to an empty list. `search.open_only` defaults to `true`. `search.limit` defaults to `30`. |
| GitHub Wiki | `repository` defaults to unset, so Git forge detection selects the GitHub.com target. Set an explicit `owner/name` to use another GitHub.com repository. |
| FileSystem | `root` defaults to `.devflow/tasks` for tasks and `docs` for knowledge. |
| Repository | `repository` and `branch` default to unset, allowing the current repository and its default branch. `directory` defaults to `.devflow/tasks` or `docs`. `publication` defaults to `pull-request` and accepts `push`. |
| SecOps | `findings` defaults to Dependabot and code scanning. `min_severity` defaults to `high`. `repository` and the optional incident fixture start unset. See [SecOps configuration](secops.md). |
| Slack | `destination` and `credential_ref` default to unset. |
| Webhook and router | A webhook uses an environment credential reference and can declare safe rejection codes. A router maps named destinations and signals. See [notification settings](notifications.md). |

This configuration overrides one search property and sets a different provider
for the knowledge section. The search limit stays at 30, and Repository keeps
the current repository, its default branch, and the pull-request policy.

```yaml
devflow: 1

task_management:
  provider: github-issues
  settings:
    search:
      open_only: false

knowledge_base:
  provider: repository
  settings:
    directory: docs/adr
```

Devflow treats explicit `false`, `0`, empty strings, and empty lists as supplied
values. Optional fields such as `repository` accept `null` to clear the
selection. A provider change starts with the new provider's defaults and
validates its settings against that provider's schema.

## Deployment and release providers

`devflow status` shows the newest matching deployment per environment. It uses
the open PR head SHA when available and local `HEAD` otherwise. GitHub deployment
entries retain `environment`, `ref`, `sha`, `created_at`, `state` and numeric `id`.
Select `none` to omit deployments from status.

`devflow release get TAG` reads a published release from `devops.releases`.
Compare its `revision` with the deployment commit to correlate a release with
status across providers. GitHub lookup returns an `assets` array when the release
has attached files. Each entry has a `name`, `download_url`, `size` in bytes
and a `digest` when GitHub supplies one.
Set `settings.repository` on each GitHub role to choose a specific GitHub.com
`owner/name`. With the setting unset, devflow uses the detected GitHub.com target.

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
    settings: {path: .devflow/deployments.json}
  releases:
    provider: fixture
    settings: {path: .devflow/releases.json}
secops: {provider: none}
notifications: {provider: none}
```

The deployment file is a JSON array ordered newest first. Each record needs
`environment` and `revision` and can include `state` and `url`. Use the full
commit ID from `git rev-parse HEAD` as `revision` to see a record in `devflow status`.
The release file is a JSON array with unique `tag` values, each with a `revision`
and optional `title`, `notes`, `url` and `assets`. Use `devflow release get TAG` to read a
record. Setup readiness checks that both selected files can be read and decoded.

## Jira, Confluence and GitHub

Jira task operations use `task_management.settings.site`. The project list in
`task_management.settings.projects` scopes My Work, which looks in active sprints
first. Existing Jira keys, acceptance checklists, and per-project event and verb
mappings in `flow-map.yml` continue to work. A transition uses the destination
status name and rechecks the current status before writing. When a provider
overrides the legacy Jira site, pass its URL to `devflow login --site URL` to
store the credential for that site.

Confluence search uses `knowledge_base.settings.site` and returns page IDs, space
keys, titles, URLs, excerpts and published revisions. Set `CONFLUENCE_USER`
and `CONFLUENCE_TOKEN` together to supply knowledge credentials. With those variables unset, a separate
site uses its own Atlassian keychain entry. A shared site also accepts the existing
`JIRA_USER` and `JIRA_TOKEN` environment pair.

For GitHub pull requests, forge detection selects the source remote for the
push and the target repository for the PR. The source must have one GitHub.com
push destination matching its repository. Review thread replies verify the PR
repository before requesting approval. Jira write confirmations include the
effective Jira site, so switching sites requires fresh approval.

## Check workstation and runner readiness

`devflow setup plan --json` reports the selected roles, detected forge, installed
tools, and prerequisite checks. `devflow_setup_plan` presents the same plan to an
MCP client. Use `--client NAME` to select a desktop client even if its executable
is absent from the current shell.

`devflow setup verify` and `devflow doctor --providers` report failed checks
as errors. Use provider credentials in the environment where the command runs.
`devflow setup apply --client NAME` adds the project client entry after approval.
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
devflow setup login --provider jira --client claude-desktop --server atlassian
devflow setup login --provider slack --client codex --server slack
devflow setup login --provider jira  # starts the installed Teamwork Graph CLI login
```

The command delegates to the native tool and lets it store the login.
Use the host's Connect or Sign in action for clients without a native login
command. Retry the original read in the chosen tool after sign-in. Jira and
Confluence require separate product access checks even with the same connector.
A permission denial calls for access to the affected product, project or space.

A standalone plan cannot list the tools or sign-in state of a sibling MCP
server owned by the host. Validate the available read capabilities in the
agent session. Interactive setup works without exporting personal credentials.

Devflow routes Jira work and issue context, Confluence search, and individual
Asana and GitHub issue reads through the selected provider's MCP where supported.
Codex can call a provider tool directly through App Server. In Claude Code and
other MCP hosts, the Devflow read can return `native_read_required`. The host
uses its existing provider connection for the exact requested tool and inputs,
then passes the result back to the original Devflow tool in `native_results`.
Other clients can opt into the same flow with `provider_mcp: true`.

Jira and Confluence can use the signed-in Teamwork Graph CLI when the MCP
capability is missing. Interactive GitHub API calls use the signed-in `gh`
client. Explicit GitHub workflow tokens retain the existing service-account
path. When a provider rejects access, report the rejected site or scope and
check its permissions. When authentication is required, start the provider
client's sign-in and retry after consent. Other existing REST or webhook
operations retain their current credential behavior during migration.
Devflow-managed writes continue through its confirmation and audit policy.

### Claude Code Desktop

The `claude` setup mode configures the Claude Code plugin marketplace. If a
trusted desktop session still lacks the devflow tools, register the project
server from the enrolled checkout in a POSIX launcher environment.

```bash
devflow setup plan --client claude-desktop
devflow setup apply --client claude-desktop
devflow setup verify --client claude-desktop
```

Setup adds `mcpServers.devflow` to the checkout's `.mcp.json`. It uses `sh`
and Git to find the checkout's executable `.devflow/bin/devflow` and run
`mcp --client claude` over stdio. Restart Claude Code Desktop, open the trusted
project, and approve the server if prompted. Call `mcp__devflow__devflow_setup_plan`
with `{"client":"claude-desktop"}` and compare the result with
`devflow doctor --providers --client claude-desktop --json`.
For a configured knowledge provider, search
for a known document with `mcp__devflow__devflow_docs_search` and the same
query in `devflow docs search`.

From an existing MCP session, pass `{"client":"claude-desktop"}` to
`devflow_setup_plan` and `devflow_setup_apply`. Review the apply preview and
repeat with its confirmation token. Keep the generated
`.mcp.json.devflow-companions.json` receipt with the project file.
`devflow remove` uses it to remove an unchanged launcher that setup wrote.
An edited launcher or a matching entry supplied by the team stays in place.
On native Windows, configure the project server manually with the verified
`devflow.exe` and arguments `mcp --client claude`.

### Install a pinned gh companion

On native macOS and Linux, providers that need GitHub CLI first try
`gh --version` on PATH. If that check fails, setup looks for
`companions.yml` in your devflow home (normally `~/.devflow`, or an absolute
`DEVFLOW_HOME`). Keep this directory outside the checkout, under stable
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

1. Run `devflow setup plan --json` or call `devflow_setup_plan`. Review
   `install_gh` for the source, version, platform, digest and destination home.
   Planning reads local state and leaves the source unopened.
2. Run `devflow setup apply --install-gh` and confirm, or pass `--yes` for
   noninteractive approval. In MCP, call `devflow_setup_apply` with
   `{"install_gh":true}`, review the returned summary, then repeat the call with
   its `confirm_token` after approval. The local autonomy setting applies.
3. Read the returned readiness checks. The installer verifies the digest before
   publishing. Setup then checks the executable, authentication and selected
   GitHub reads. If authentication is needed, start browser login using
   the verified path shown in the plan with `auth login --hostname HOST --web`.
   Approve project registration separately with `setup apply --client NAME`.

The cache lives under `DEVFLOW_HOME/companions/<os>-<arch>/gh/`. Devflow
rechecks the installed digest against the current pin and invokes the
version-and-digest-specific path for provider probes and app token lookup.
An already verified copy works with the source offline. `GH_TOKEN` and
`GITHUB_TOKEN` retain priority for app token lookup. Shell PATH and unmanaged
tools stay as configured. The project launcher and devflow update verification
still use the system `gh` for release bootstrap.

### Register a provider MCP companion

Create `mcp-companions.yml` in your devflow home, outside the workspace, under
stable directories you control. The home and manifest must be ordinary,
user-owned filesystem objects. On macOS and Linux, remove group and other
write permission from both. On Windows, use a local NTFS directory under
your profile with write access limited to you, Administrators and SYSTEM.
Devflow checks the owner and DACL before using the declaration.

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
`DEVFLOW_HOME/mcp-companions/<os>-<arch>/<id>/`. The installer locks the
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

1. Run `devflow setup plan --companion documents --json`. Review the version,
   source, digest and destination. Approve the install with
   `devflow setup apply --companion documents --install`. Hosted companions
   start at the verification step.
2. Run `devflow setup verify --companion documents` or
   `devflow setup verify --companion work-items`. Both transports negotiate
   an MCP session and request `tools/list`. The check discards tool metadata
   and server diagnostics. A missing credential or a failed handshake or list
   request gives a nonzero exit code.
3. Review `devflow setup plan --companion documents --client vscode --json`.
   Approve `devflow setup apply --companion documents --client vscode` and
   rerun `devflow setup verify --companion documents --client vscode`. Pass
   `--client cursor`, `--client claude` or `--client gemini` for those clients.
   The CLI accepts `--yes` for explicit noninteractive approval. Reload the
   client and complete its project trust prompts.

Registration reserves `devflow-<id>` in one project file.

| Client | Project file | Credential reference |
| --- | --- | --- |
| VS Code | `.vscode/mcp.json` | `${env:NAME}` |
| Cursor | `.cursor/mcp.json` | `${env:NAME}` |
| Claude Code (`claude` or `claude-desktop`) | `.mcp.json` | `${NAME}` |
| Gemini CLI | `.gemini/settings.json` | `${NAME}` |

Review any pending replacement of that key. The merge preserves other entries,
including the existing `devflow` launcher. It writes the absolute verified command or the declared
URL and an environment reference. The resolved credential stays in memory.
A matching entry makes repeat apply a no-op. Re-register after a pin,
endpoint or credential reference changes. An absolute native path is specific
to the host where the editor launches servers. See the editor configuration
references for environment interpolation and project file scope.
[VS Code MCP configuration](https://code.visualstudio.com/docs/agents/reference/mcp-configuration),
[Cursor MCP configuration](https://cursor.com/docs/mcp),
[Claude Code project scope](https://code.claude.com/docs/en/mcp#project-scope)
and [Gemini CLI configuration](https://geminicli.com/docs/reference/configuration/).

`devflow_setup_plan` accepts `companion` and an optional `client`.
Use `devflow_setup_apply` with `{"companion":"documents","install":true}`
for installation or `{"companion":"documents","client":"vscode"}` for
registration. Review each summary, then repeat the same request with its
`confirm_token`. Each approval covers one action and its reviewed inputs.
Omit `companion` from `setup verify` or use `doctor --providers` to check
all selected providers and their declared MCP prerequisites.

### Companion cleanup and repair

When setup writes a companion entry, it saves the entry name and a SHA256
digest beside the project config, for example in
`.vscode/mcp.json.devflow-companions.json`. Keep the receipt with the
corresponding config. A matching entry already present at setup time keeps
its existing ownership.

Run `devflow remove --dry-run` to preview removal for the project. Then run
`devflow remove` to delete recorded entries whose full JSON value still
matches and discard their project receipts. Both commands work offline.
Even with `--force`, edited entries and entries without a receipt stay in
the client config. For registrations made before receipts were introduced,
review the command or endpoint in the project file listed above and delete
only the server entry you recognize. The user manifest, cached executables
and active version selections remain available to other projects.

To repair a registration, run `devflow setup plan --companion ID --client NAME`,
review the proposed change, then run `devflow setup apply --companion ID --client NAME`.
This updates the selected entry and its receipt. Use the separate `--install`
action when the shared executable itself needs repair.

## GitHub Issues task lifecycle

Use the `github-issues` task provider with the detected GitHub repository or set
`settings.repository` to `owner/repo`. The current GitHub Issues adapter uses
GitHub.com. Task references appear in branch bindings and explicit `Task-Ref`
trailers. Existing commit rules still apply, including when the repository
explicitly requires Jira-style subject scopes.

`settings.transitions` defaults to closing issues on `pr.merged` and `#done`.
Set an event to `none` to disable its effect. Devflow previews transitions and
only attempts to move open issues forward.

## Asana project tasks

Supply `ASANA_ACCESS_TOKEN` in the environment that runs the CLI or MCP server.
Quote GIDs in YAML. A task can belong to several projects, each with its own
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

`devflow work` lists up to 30 incomplete tasks in the default project, including
unassigned work. Use `devflow context 900` for task GID `900` in that project,
or `devflow context asana/200/900` for its membership in project `200`. The
default project and the keys in `transitions` identify configured projects.
An empty event map enables a project for lookup and creation. Branch bindings,
PR bodies and commit trailers use the qualified form, e.g. `Task-Ref: asana/100/900`.

`devflow start 900` creates a branch or worktree and applies the project's
`branch.created` mapping if configured. Preview a later move with
`devflow transition 900 pr.opened --dry-run`. The adapter checks task and
destination section memberships before moving the task. Unmapped events and
`none` have no effect. Mapping an event to `completed` completes the **whole
task across all its projects**. Completion is terminal. Section GIDs have
no inherent order. Assignment, authenticated-user filtering, comments,
operation-ID deduplication and conditional writes are unsupported. After an
uncertain create response, check the project before trying again.

`devflow setup verify` probes read access to each configured project's task
list and checks that each destination section belongs to the intended project.
Write access depends on the Asana identity's permissions.

Asana maps template scope to project, title to name, and description,
sections and checklist items to plain-text notes. Qualified links and the
exact template pin occupy a reserved `devflow-task-v1: ` JSON line at the
start of the notes. Keep this line when editing task content. Before a state
change devflow rereads and validates the stored revision and current fields.
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
    catalog: .devflow/task-templates.json
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

devflow task create --summary "Review capacity" --description "$body" \
  --template engineering-task --template-revision 1 --dry-run
devflow task create --summary "Review capacity" --description "$body" \
  --template engineering-task --template-revision 1 --yes
```

Supply required section content with `--description`. The preview shows the
resolved draft and its pin. For MCP, call `devflow_ticket_create` with
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

Devflow stores the exact template name and revision in a leading HTML comment.
Keep that marker and the corresponding catalog revision when editing the issue.
Before posting a comment or changing state, devflow validates the current issue
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
revision while tasks reference it. Before a configured transition, devflow
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
    root: .devflow/tasks
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
Run `devflow start <qualified-key>` to bind a local branch to the task under
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

For create retries, reuse the same `DEVFLOW_OPERATION_ID` and task inputs.
An interactive PR preview includes a generated `operation_id` when no
operation ID is supplied. Use that ID to reconcile an interrupted publication
with fresh approval. For direct publication, set `publication: push` and
choose an unprotected target branch. Protected push targets are rejected in
both modes.

## Devflow repository pilot

This repository selects the `engineering-task` template at revision `1`
for task creation. Its catalog lives in [.devflow/task-templates.json](../.devflow/task-templates.json).
The template requires Objective, Scope, Dependencies, Acceptance Criteria,
and Verification sections. It supplies a default statement for Dependencies.

This repository uses GitHub Issues for task management and the Repository
provider for knowledge under `docs/adr/`. The `provider-pilot.yml` job
enrolls one issue through the `DEVFLOW_PROVIDER_PILOT_ISSUE` repository
variable. It applies `pr.merged` to that issue after a same-repository PR
with a qualified `Task-Ref` merges into the default branch. The job builds
devflow at that merge commit and uses it to find the published ADR before
closing the enrolled issue.

The pilot uses the repository's existing GoReleaser release process.

## Search a local knowledge directory

Choose a dedicated directory for knowledge records. Relative roots resolve from
the repository checkout. Use `devflow docs search quota --space engineering` to
search the scope in the following example.

```yaml
knowledge_base:
  provider: filesystem
  settings:
    root: handbook
```

Place a Markdown document in `handbook/` with devflow front matter.

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

## Replace a Confluence page

With `knowledge_base.provider: confluence`, search for the current page ID,
space key and revision. For example, to replace published version 7 of page
42 in the ENG space, supply the complete new title and body.

```bash
devflow docs search quota --space ENG --json
devflow docs publish confluence/ENG/42 \
  --title "Quota policy" --body "Each team owns its quota." \
  --expected-revision 7 --operation-id quota-policy-1
```

The CLI and `devflow_docs_publish` apply the remote autonomy policy. Confirmation
covers the selected backend, configured site, qualified reference, expected
revision, operation ID and complete replacement. Interactive publishing can use
the authenticated Atlassian Rovo MCP connection, with Teamwork Graph CLI
available when the MCP write capability is missing. The existing REST path
continues to serve automation. Devflow checks the published page and space
before the update and verifies their identity when confirming an outcome.
Interactive transports require a snapshot token for the expected page version.
In this example the successor is version 8. If another edit wins, review the
newer version before publishing a replacement.

Retry an uncertain result with the same operation ID, revision, title and body.
Devflow checks the message on the exact successor version, including in page
history after later edits. A matching receipt returns `already_applied` with
the original published revision. An inaccessible version history can prevent
verification. For a new edit, use a new operation ID and the current revision.
After an attempted write, devflow keeps the outcome tied to that backend.

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
devflow docs publish repository/engineering/quota \
  --title "Quota policy" --body "The policy and its rationale." \
  --operation-id quota-policy-1
```

Pass the revision from search when updating an existing document. Reuse the
operation ID for retries. The CLI and `devflow_docs_publish` present the
resolved destination and document for approval and record the outcome.

Use `devflow docs search QUERY --json` to check a qualified document in CI.

## Publish a GitHub Wiki page

Enable Wikis in the target repository's GitHub settings and create the first
page in GitHub's web UI. Configure Git's HTTPS credential helper for any reads
that need authentication and for writes to
`https://github.com/owner/name.wiki.git`. Use credentials with permission to
edit the wiki when publishing.
Use the direct GitHub wiki remote so the configured URL and Git's push
destination match.

```yaml
devflow: 1
knowledge_base:
  provider: github-wiki
  settings:
    repository: docs-team/handbook
```

Omit `settings` to use the detected GitHub.com target. In a fork checkout this
uses the upstream target when forge detection selects it. You can set
`code_hosting.settings.target_remote` to select a different remote.

Devflow publishes directly to the wiki's default branch. The resolved wiki
remote and branch appear in the confirmation summary, and the remote
autonomy policy and `policy.protected_branches` apply. If your wiki uses
`master` and your code repository doesn't use that branch, you can set
`policy.protected_branches: [main, develop, 'release/*']` to keep those code
branches protected while allowing Wiki publication.

```bash
devflow docs publish github-wiki/engineering/quota \
  --title "Quota policy" --body "The policy and its rationale." \
  --operation-id quota-policy-1
devflow docs search quota --space engineering
```

The equivalent MCP tools are `devflow_docs_publish` and `devflow_docs_search`.
Search and publication operate on managed Markdown under `devflow-pages/`.
Use the same operation ID to retry a publish. To update a page, use a new
operation ID and pass the revision from search with `--expected-revision`
or the MCP field `expected_revision`. Ordinary wiki pages such as `Home.md`
are edited in GitHub. Publish the body under a new qualified reference to
adopt an existing page as a managed document.
