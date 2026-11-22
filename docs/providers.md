# Provider configuration

Devflow resolves provider settings from distinct sections in `.devflow/config.yml`.
Each section has a versioned schema. A value in `settings` overrides the selected
provider's default for that field. Omitted fields inherit their defaults, nested
objects merge by field, and supplied lists replace the default lists.

This reference describes the configuration accepted by the provider foundation.
The migration of commands and workflows to these selections is tracked separately.

| Section | Default provider | Other schema selections |
| --- | --- | --- |
| `code_hosting` | `auto` | `github`, `none` |
| `task_management` | `jira` | `github-issues`, `filesystem`, `repository`, `none` |
| `knowledge_base` | `confluence` | `filesystem`, `repository`, `none` |
| `devops.checks` | `github` | `none` |
| `devops.deployments` | `github` | `none` |
| `devops.releases` | `github` | `none` |
| `secops` | `github` | `none` |
| `notifications` | `slack` | `none` |

Each selection accepts `version` (default `1`), `provider` (see the table),
and `settings` (defaults below). The enclosing `devops` section also accepts
a schema version (default `1`). A provider selection of `none` has an empty
settings object. Validation errors identify the property to correct.

| Provider | Settings and defaults |
| --- | --- |
| Code hosting | `hosts` defaults to an empty hostname-to-forge map. `source_remote` and `target_remote` default to unset, so detection can select Git remotes. |
| Jira | `site` and `projects` inherit the legacy Jira settings and defaults. |
| Confluence | `site` inherits the legacy Confluence site, which defaults to the Jira site. |
| GitHub | An unset `repository` defers to Git remote detection. |
| GitHub Issues | `repository` defaults to unset. `labels` defaults to an empty list. `search.open_only` defaults to `true`. `search.limit` defaults to `30`. |
| FileSystem | `root` defaults to `.devflow/tasks` for tasks and `docs` for knowledge. |
| Repository | `repository` and `branch` default to unset, allowing the current repository and its default branch. `directory` defaults to `.devflow/tasks` or `docs`. `publication` defaults to `pull-request` and accepts `push`. |
| Slack | `destination` and `credential_ref` default to unset. |

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

## Check workstation and runner readiness

`devflow setup plan --json` reports the selected roles, detected forge, installed
tools, and prerequisite checks. `devflow_setup_plan` presents the same plan to an
MCP client. Use `--client NAME` to select a desktop client even if its executable
is absent from the current shell.

`devflow setup verify` and `devflow doctor --providers` report failed checks
as errors. Use provider credentials in the environment where the command runs.
`devflow setup apply --client NAME` adds the project client entry after approval.
Readiness diagnostics identify missing companions and hosted MCP prerequisites.

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
   GitHub reads. If authentication is needed, supply the usual credentials or run
   the verified path shown in the plan with `auth login --hostname HOST`.
   Approve project registration separately with `setup apply --client NAME`.

The cache lives under `DEVFLOW_HOME/companions/<os>-<arch>/gh/`. Devflow
rechecks the installed digest against the current pin and invokes the
version-and-digest-specific path for provider probes and app token lookup.
An already verified copy works with the source offline. `GH_TOKEN` and
`GITHUB_TOKEN` retain priority for app token lookup. Shell PATH and unmanaged
tools stay as configured. The project launcher and devflow update verification
still use the system `gh` for release bootstrap.

## GitHub Issues task lifecycle

Use the `github-issues` task provider with the detected GitHub repository or set
`settings.repository` to `owner/repo`. The current GitHub Issues adapter uses
GitHub.com. Task references appear in branch bindings and explicit `Task-Ref`
trailers. Existing commit rules still apply, including when the repository
explicitly requires Jira-style subject scopes.

`settings.transitions` defaults to closing issues on `pr.merged` and `#done`.
Set an event to `none` to disable its effect. Devflow previews transitions and
only attempts to move open issues forward.

## Task templates

Task object templates apply independently of the task provider. Configure a
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

GitHub Issues resolves the configured default when creating an issue. Application
callers can select another exact revision with `App.CreateTicketWithTemplate`.
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
Use the `github-issues` task provider with the detected GitHub repository or set
`settings.repository` to `owner/repo`. The current GitHub Issues adapter uses
GitHub.com. Task references appear in branch bindings and explicit `Task-Ref`
trailers. Existing commit rules still apply, including when the repository
explicitly requires Jira-style subject scopes.

`settings.transitions` defaults to closing issues on `pr.merged` and `#done`.
Set an event to `none` to disable its effect. Devflow previews transitions and
only attempts to move open issues forward.

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
