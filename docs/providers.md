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

GitHub Issues, FileSystem and Repository support task templates. Configure a
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
