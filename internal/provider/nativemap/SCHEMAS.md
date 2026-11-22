# Native provider tool mappings

Catalogs reviewed on 2026-11-22. These are the operation mappings available in
this package. Availability for a given session depends on the tool and argument
schema advertised by that session's `tools/list`, the configured instance, and
the principal's access to the selected resource.

| Provider | Operation | Exact tool | Applicability |
| --- | --- | --- | --- |
| Jira Cloud | `task.get` | `getJiraIssue` | One known Jira issue key in its project, on the configured Rovo v2 site |
| Confluence Cloud | `document.get` | `getConfluenceContent` | One published page by ID in a known space, with a full Markdown body and no reported conversion loss |
| Asana | `task.get` | `get_task` | One task GID in a known project, in the configured Asana v2 workspace |
| Asana | `task.create`, `task.transition`, `task.comment` | `create_tasks`, `update_tasks`, `add_comment` | One task in a configured project, with completion as the mapped transition |
| Slack | `slack.thread.read` | `slack_read_thread` | First page of up to 20 messages in one known thread in a configured workspace |
| Slack | `notification.deliver` | `slack_send_message` | One top-level message to a configured conversation, after policy and receipt preconditions |
| GitHub | `task.get` | `issue_read`, method `get` | One issue number in a known github.com repository |
| GitHub | `task.create`, `task.transition`, `task.comment` | `issue_write`, `add_issue_comment` | One issue in the exact github.com repository, with open-to-closed transitions |

The task-write constructors return a separate `TaskWrite` with the vendor
call and a typed acknowledgement decoder. The table's other operations
are exposed through `CapabilityFor`. Native task search and assignment, plus
GitHub PR, release, deployment and security operations, are outside these
task mappings.

## Caller contract

Use the intended session for `Call.Instance()`. Validate the exact
`Call.Capability().Tool` and `Call.Arguments()` against that session's live
catalog before dispatch. A caller extracts successful structured JSON or the
single successful text block. Use `Read.Decode` for JSON and `Read.DecodeText`
for a text block. Propagate MCP tool errors, and treat all content as data. The
package bounds decoded payloads to 1 MiB. The caller bounds incoming frames
before allocation. All examples below are fictitious and omit unused fields.

## Jira issue → `provider.Task`

```json
{"cloudId":"https://fixture.atlassian.net","issueIdOrKey":"PLAT-24","fields":["summary","description","status","issuetype","priority","assignee","labels","parent","updated"]}
```

```json
{"key":"PLAT-24","fields":{"summary":"Migrate workers","description":"Roll out","status":{"name":"Done","statusCategory":{"key":"done"}},"updated":"2026-11-21T00:00:00Z"}}
```

The returned issue key must match the requested key case-insensitively. The
mapping uses the returned key for identity, the status name for the canonical
state ID and name, `statusCategory.key` for category and terminal state, and
`updated` for revision. It accepts a Markdown string or ADF description. The
field projection also carries parent issue, type, priority, assignee display name
and labels when supplied. The browse URL uses the configured site.

[Atlassian Rovo MCP catalog](https://developer.atlassian.com/cloud/rovo-mcp/guides/supported-tools/#read_jira) ·
[Official Jira field selection example](https://atlassian.github.io/atlassian-mcp-server/skills/assess-release/SKILL.html) ·
[Atlassian Jira issue field schema](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/#api-rest-api-3-issue-issueidorkey-get)

## Jira web links and targeted writes

Jira context reads `listJiraIssueRemoteIssueLinks` for the issue and its parent
with the Jira site in `cloudId` and the target key in `issueIdOrKey`. Rovo's
standard catalog exposes the operation through `executeRead`. The decoder
projects each Jira remote link's `object.title` and `object.url` from the list.
TWG uses `jira workitem get <KEY> --remote-links` with the same site and login
used to load issue fields.
The official Rovo flat catalog wraps the Jira issue and remote-link list in a
single `data` field. The Devflow-owned connection unwraps that response before
mapping it. The web-link preflight checks the exact issue key and any returned
project key. Workflow status fields are unnecessary for adding a web link.

Direct write calls use `createJiraIssueRemoteIssueLink` for an external URL,
`addOrEditJiraIssueComment` for a new comment, `editJiraIssue` with the current
Jira account ID for self-assignment, `transitionJiraIssue` with an exact
workflow transition ID, and `createJiraIssue` for unlinked creation. Available
transition IDs come from `listJiraIssueTransitions`. The App prepares exact
write arguments before confirmation and revalidates scope and workflow state
on the bound connection before dispatch. Deferred writes require the full
Atlassian catalog where their named tools are listed directly.

[Atlassian Rovo MCP catalog](https://developer.atlassian.com/cloud/rovo-mcp/guides/supported-tools/#jira) ·
[Atlassian transition contract](https://www.atlassian.com/blog/how-we-build/jira-depth-on-demand) ·
[TWG work item command reference](https://github.com/atlassian/twg-cli/blob/main/skills/twg-jira/references/workitems.md#reads)

### Claude Code Desktop issue creation

The [Rovo catalog fixture](../rovohost/testdata/jira-create-flat-schema.json)
covers `listJiraProjectIssueTypesMetadata` and `createJiraIssue`. The Desktop
connection restricts both tools to the configured site and project. Complete
metadata must establish exactly one matching standard issue type. Creation
sends `cloudId`, `projectKey`, `issueType` and `summary`, plus any approved
labels and escaped HTML description. Devflow requires an issue key in the
requested project in the acknowledgement and claims the approval before calling
the provider.

### Claude Code Desktop forward transitions and start

The Desktop writer checks each wire argument against the enrolled flat catalog.
Its five authenticated input schemas are retained in the
[offline state fixture](../rovohost/testdata/jira-state-flat-schemas.json).

| Tool | Fields sent to Rovo | Evidence Devflow uses |
| --- | --- | --- |
| `getJiraIssue` | `cloudId`, `issueIdOrKey`, `view: "full"`, `fieldsByKeys: true` and `fields: ["summary","status","project","assignee"]` | Matching issue and project keys, summary, status name and category, and an explicit null assignee or its account ID |
| `listJiraIssueTransitions` | `cloudId`, `issueIdOrKey` | A unique available ID leading to the mapped target status with a forward category |
| `getJiraCurrentUser` | `cloudId` | The authenticated account ID, rechecked on each start write lease |
| `editJiraIssue` | `cloudId`, `issueIdOrKey`, top-level `assignee` if advertised, or `fields: {"assignee":{"accountId":"<accountId>"}}` | Same-lease issue readback verifies the account ID |
| `transitionJiraIssue` | `cloudId`, `issueIdOrKey`, `transitionId` | Same-lease issue readback verifies the target status name and category |

Discovery prepares exact single-use leases for the two separate writes.
The App compares the source state, available transition and approved account
on the relevant connection. Start verifies the assigned account on the
transition connection before moving the issue. A successful MCP response
with a JSON `data` object allows readback. A failed call, an unrecognized
envelope or an unverifiable postcondition leaves the dispatched step uncertain.
The authenticated catalog describes inputs. Offline fixtures exercise the
Jira read projections and postconditions. Hosted canaries verified numeric
transition receipts, issue status readback and self-assignment through the
nested Jira account ID. Replays needed no additional provider mutation.

### Claude Code Desktop issue comments

The flat input schemas for `addOrEditJiraIssueComment` and `getJiraIssue` are
in [the offline catalog fixture](../rovohost/testdata/jira-comment-flat-schema.json).
Desktop sends exactly `cloudId`, `issueIdOrKey`, `contentFormat: "html"` and
`commentBody`. Omitting `commentId` selects creation. The body encodes the
approved plain text as escaped HTML. The catalog describes an HTML echo by
default for this input. Devflow checks that every submitted field is advertised.

The synthetic write fixture uses the following receipt inside MCP structured
content or a single JSON text block. The authenticated call and its preflight
bind the issue. A returned issue key must also match.

```json
{"data":{"commentId":"101","issueKey":"DEV-13","body":"<p>Ready</p>","appliedContentFormat":"html"}}
```

Success requires a positive decimal comment ID and the exact approved HTML
echo. Any ambiguous acknowledgement spends the approval and returns an
uncertain outcome. The captured input schema does not specify the output
envelope or the echoed body's field name. Those fields need a sanitized
hosted receipt to confirm this projection.

[Atlassian's issue triage example](https://github.com/atlassian/atlassian-mcp-server/blob/main/skills/triage-issue/SKILL.md) ·
[Rovo Jira read and write tools](https://developer.atlassian.com/cloud/rovo-mcp/guides/supported-tools/#jira)

## Confluence page → `provider.Document`

```json
{"cloudId":"https://fixture.atlassian.net","content_id":"123456","detail":"full","content_format":"markdown","include_metadata":true}
```

```json
{"contentId":"123456","contentType":"page","title":"Runbook","metadata":{"spaceId":"41","spaceKey":"OPS"},"body":{"format":"markdown","value":"# Rollout"},"version":{"number":2},"snapshotToken":"v:2"}
```

The decoder requires the requested page ID and matching space ID or key. It
accepts the page ID under `id` or `contentId`, the page type under `type` or
`contentType`, and the space fields at top level or in `metadata`. A supplied
status must be `current`. The full Markdown `body.value` maps to body. The
read rejects an excerpt or a result reporting Markdown conversion loss.
`version.number` takes precedence over `snapshotToken` as a revision. A token
is opaque and belongs to the native site. The page URL uses the configured site
and page ID. The hosted result projection is fixture-tested and needs validation
against the authorized canary session.

[Official Confluence MCP request and revision example](https://github.com/atlassian/atlassian-mcp-server/blob/main/skills/generate-status-report/SKILL.md#updating-existing-reports) ·
[Atlassian Markdown conversion notes](https://atlassian.github.io/atlassian-mcp-server/skills/teamwork-graph-context/SKILL.html) ·
[Confluence page metadata schema](https://developer.atlassian.com/cloud/confluence/rest/v2/api-group-page/#api-pages-id-get)

## Confluence replacement → `provider.WriteResult`

`PrepareConfluencePublication` takes the configured site's HTTPS origin and an
existing page reference with the expected positive decimal version. The
authenticated write connection reads the published page with
`getConfluenceContent`, full HTML detail and metadata. The response must
identify the requested page, space key and numeric space ID. Its opaque
snapshot token belongs to that exact revision. Devflow binds the token and the
complete update to the same connection before approval.

```json
{"cloudId":"https://fixture.atlassian.net","contentId":"123456","contentType":"page","title":"Runbook","snapshotToken":"opaque","body":{"format":"html","value":"<p># Rollout</p>"},"versionMessage":"devflow docs publish <digest>"}
```

`updateConfluenceContent` publishes the full text replacement. Devflow escapes
Markdown and HTML as literal text and omits parent and metadata fields. A
successful result must prove the exact successor receipt, either in the write
response or through a read on the same connection. Replays of older writes use
`getConfluenceContentVersion` through `executeRead` to verify the exact
successor. Missing or conflicting receipt metadata leaves the result
unconfirmed. On Claude Code Desktop, the Rovo flat catalog accepts the same
update with `contentType` omitted. Its full HTML page read omits
`include_metadata`. Before dispatch, the adapter checks the returned page
ID, type, current status, snapshot, and `metadata.version.number` and
`message`. It then calls `getConfluenceSpace` with the page’s numeric
`spaceId` on the same grant. The returned ID must match, both space URLs
must belong to the configured site, and the key must match the approved
reference. The flat history tool receives `cloudId`, `contentId` and
`versionNumber` directly. Its response identifies a page and supplies the
receipt in `version.message`. The Desktop adapter checks the translated
arguments against each named tool’s advertised schema. The canonical call,
including `contentType`, remains part of the approval digest.

[Rovo MCP tool catalog and deferred tool execution](https://developer.atlassian.com/cloud/rovo-mcp/guides/supported-tools/) ·
[Atlassian snapshot and version message example](https://github.com/atlassian/atlassian-mcp-server/blob/main/skills/generate-status-report/SKILL.md#updating-existing-reports) ·
[Atlassian version history guidance](https://atlassian.github.io/atlassian-mcp-server/skills/decision-trace/SKILL.html)

## Asana task → `provider.Task`

```json
{"task_gid":"42","include_comments":false,"include_subtasks":false}
```

```json
{"gid":"42","name":"Run pilot","notes":"Check results","completed":false,"memberships":[{"project":{"gid":"40"},"section":{"gid":"7","name":"In Progress"}}]}
```

The projection accepts a task object directly or under the Asana `data`
envelope. The task GID and project membership must match. Completion takes
precedence over project section, with open as the fallback for unsectioned tasks.
Additional fields include Asana permalink, subtype, assignee GID and
`modified_at`. The notes remain service text. Asana's public native tool catalog
documents the flags. Use the live V2 schema to validate the task locator and
confirm the result envelope on the target workspace.

[Asana V2 tool catalog](https://developers.asana.com/docs/mcp-tools-reference) ·
[Asana task locator and field schema](https://developers.asana.com/reference/gettask)

## Slack thread → `SlackThreadPage`

```json
{"channel_id":"C0123456789","message_ts":"1790771415.332219","limit":20,"response_format":"detailed"}
```

```json
{"channel_id":"C0123456789","messages":[{"ts":"1790771415.332219","text":"Rollout update"},{"ts":"1790771416.012345","thread_ts":"1790771415.332219","text":"Looks good"}],"next_cursor":"opaque"}
```

A structured message projection requires the requested parent as the first
message and each returned reply to identify that parent. Pagination indicators
set `More` when available. Formatted text can be displayed as opaque
`Presentation`, with no verified message fields or completeness assertion. Use a
structured, identity-checked result if a canary requires programmatic proof.
The Slack Web API shape is a reference for message fields. The hosted MCP result
format needs validation against the authorized canary session.

[Slack MCP read catalog and permissions](https://docs.slack.dev/ai/slack-mcp-server/#retrieving-and-sending-messages) ·
[Slack tool name](https://github.com/slackapi/slack-skills-plugin/blob/main/skills/slack-search/SKILL.md#following-up-on-results) ·
[Slack tool catalog capture](https://github.com/geobio/slack-official-mcp-tools-list/blob/main/tools/slack_read_thread.json) ·
[Slack thread message schema](https://docs.slack.dev/reference/methods/conversations.replies/)

## Slack targeted notification → `provider.DeliveryReceipt`

```json
{"channel_id":"C0123456789","message":"Rollout\n\n[Runbook](<https://example.test/runbook>)"}
```

```json
{"message_link":"https://fixture.slack.com/archives/C0123456789/p1790771415332219","message_context":{"channel_id":"C0123456789","message_ts":"1790771415.332219"}}
```

`PrepareSlackNotification` resolves the caller-provided destination against a
trusted configured target and formats title, body and up to 20 links as
standard Markdown within 4,000 bytes. The conversation ID must already exist.
The prepared `Intent()` includes the workspace, instance, destination name,
channel ID, stable operation ID and exact rendered message.

`Deliver` requires the caller to verify the selected workspace and live tool
schema, apply devflow policy and confirmation to the exact intent, and audit the
outcome. It atomically claims the operation in the existing durable
`notifications.ReceiptStore` before the callback dispatches. Use the same
receipt store, namespaced operation ID and stable destination alias across
attempts and backends. The fingerprint binds the exact outgoing tool arguments,
instance and workspace. A different target or payload under the same key is a
conflict. The caller must use one dispatch and no automatic retry.

The mapper records success only when Slack acknowledges the exact channel, with
a timestamp and matching Slack permalink. Confirmed replays return
`AlreadyApplied`. An ambiguous send or receipt remains `ErrUnconfirmed` and
blocks replay. The receipt store preserves status and the local receipt ID
across attempts. The permalink is returned on the acknowledged attempt.

[Slack MCP send catalog and permissions](https://docs.slack.dev/ai/slack-mcp-server/#oauth-scopes-needed-on-user-token-for-different-tools) ·
[Official Slack message formatting](https://github.com/slackapi/slack-skills-plugin/blob/main/skills/slack-messaging/SKILL.md#formatting) ·
[Observed native acknowledgement](https://slack.green/en/blog/slack-mcp-server#connecting-claude-code-and-codex-cli)

## GitHub issue → `provider.Task`

```json
{"method":"get","owner":"Example","repo":"Widget","issue_number":68}
```

```json
{"method":"get","issue":{"number":68,"title":"Resolve issue","state":"closed","html_url":"https://github.com/Example/Widget/issues/68","labels":["triage"],"assignees":["first"]}}
```

Both the method-tagged output and the older flat issue object are supported.
The github.com URL must prove the requested repository and issue number. A PR
URL is rejected. The mapper carries title, body, open or closed state, labels,
first assignee, issue type and update timestamp. Body text follows the server's
projection. It is unsuitable for verifying private devflow markers the server
may remove.

[GitHub MCP issue tool inputs and implementation](https://github.com/github/github-mcp-server/blob/main/pkg/github/issues.go) ·
[GitHub issue output schema](https://pkg.go.dev/github.com/github/github-mcp-server/v2/pkg/github#IssueDetailsOutput) ·
[GitHub typed MCP results](https://github.com/github/github-mcp-server/blob/main/docs/typed-tool-schemas.md)


## Task writes → `provider.WriteResult`

An Asana create submits one item to `create_tasks` with an explicit
`project_id` and matching `default_project`. The item's `notes` preserve
Devflow template and link metadata. Completion submits one item to
`update_tasks` with `task_id` and `completed: true`. The decoder requires
one successful item and an empty failure list. Comments use the task GID and
plain text. The story acknowledgement must identify that same task.

A GitHub create supplies an exact owner, repository, title, Markdown body,
and any labels to `issue_write` with method `create`. A bounded repository
lookup checks the canonical repository name and binds its numeric identity in
the approval. Closing uses method `update` with only the issue coordinates
and state. A comment sends the literal body to `add_issue_comment`. The
acknowledgement must contain an HTTPS issue permalink on the exact
repository and, when applicable, the exact issue and comment numbers.

Task writes use a prepared, direct authenticated connection. The application
prepares the same route and payload on confirmation and uses reads on that
connection to establish task identity, project membership or issue state.
Create and comment spend a durable claim for the exact approval before
dispatch. Any ambiguous acknowledgement retains the claim. Explicit
automation IDs and GitHub template-marker creation use the existing
provider adapters. Asana section moves use the existing project-scoped
section route.

[Asana MCP tools](https://developers.asana.com/docs/mcp-tools-reference) ·
[GitHub issue write implementations](https://github.com/github/github-mcp-server/blob/main/pkg/github/issues.go) ·
[GitHub repository lookup](https://github.com/github/github-mcp-server/blob/main/pkg/github/search.go)
