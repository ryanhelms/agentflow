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
| Slack | `slack.thread.read` | `slack_read_thread` | First page of up to 20 messages in one known thread in a configured workspace |
| Slack | `notification.deliver` | `slack_send_message` | One top-level message to a configured conversation, after policy and receipt preconditions |
| GitHub | `task.get` | `issue_read`, method `get` | One issue number in a known github.com repository |

`CapabilityFor` returns `provider.ErrUnsupported` for other provider and
operation pairs. In particular, native task search, creation, assignment,
comments and transitions, document search and publication, and GitHub PR,
release, deployment and security operations are outside this table. The
Confluence replacement mapper uses its own prepared publication.

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
unconfirmed. The hosted version projection is fixture-tested.

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
