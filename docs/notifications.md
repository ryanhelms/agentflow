# Delivery notifications

`.devflow/config.yml` selects notification destinations independently of the
code host and task, knowledge, DevOps, and SecOps providers. Set `provider: none`
to disable delivery.

## Send through an authenticated Slack connection

Configure a workspace ID and an existing conversation ID to send from a client
with a direct authenticated Slack MCP connection. `server` names the MCP
registration and defaults to `slack`. `actor_id` optionally requires a specific
authenticated Slack user. `destination` names the receipt's route and defaults
to `slack` for the primary provider.

```yaml
notifications:
  provider: slack
  settings:
    destination: release-team
    native:
      server: slack
      workspace_id: T0123456789
      channel_id: C0123456789
      actor_id: U0123456789
```

Devflow checks the channel against Slack's workspace-filtered conversation list
through the same authenticated connection. It verifies a configured actor ID
and rejects a different workspace ID when Slack reports one. Confirmation binds
the connection, the exact rendered Markdown, the scoped IDs and the verified
actor when available. A successful Slack acknowledgement includes the message
permalink. A repeat of the same delivery returns the existing receipt ID.

The client manages Slack OAuth. See [Slack's connector setup](https://docs.slack.dev/ai/slack-mcp-server/connect-to-harnesses/).
The Slack CLI's saved login is separate from the authenticated MCP connection.

Use `devflow_notify` in a supported MCP client, or select Codex's configured
connection from the CLI.

```sh
devflow --native-client codex notify delivery --operation-id deployment-42 --body 'Production deployment failed'
```

## Route notifications to multiple destinations

Use `router` to deliver one signal to several destinations.

```yaml
notifications:
  version: 1
  provider: router
  settings:
    destinations:
      team:
        provider: slack
        credential_ref: SLACK_WEBHOOK_URL
      audit:
        provider: webhook
        credential_ref: WEBHOOK_URL
        rejected_statuses: [429]
    routes:
      delivery: [team, audit]
      hygiene: [team]
```

Routes list destination names in delivery order. Repeated names are attempted
once. Omit `routes` to send each signal to every configured destination. With
explicit routes, an unlisted signal or empty list skips delivery. Keep names
stable across replays. A routed `slack` destination can use the same `native`
block as the primary provider. Native Slack and incoming webhook settings
belong on distinct destinations. Put HTTPS webhook URLs only in the environment
variables named by `credential_ref`. The router and standalone webhook settings
also accept `header_refs` as a map from header names to environment variable
names.

Slack incoming webhooks are available to automation. GitHub Actions keeps the
default `SLACK_WEBHOOK_URL` when the notification section is omitted. An unset
default Slack webhook produces a successful skip. An explicit `credential_ref`
selects a different environment variable. Slack incoming webhooks receive a
JSON object with `text` and support mrkdwn. A standalone generic `webhook`
defaults to `WEBHOOK_URL` and reports a missing credential as a destination
failure. Generic webhooks receive a JSON object with `destination`, `title`,
`body`, and optional `links`. HTTP redirects are treated as unconfirmed. Slack
provides known rejection statuses. For a generic webhook, add a status to
`rejected_statuses` only if the receiver guarantees the POST was rejected
before doing any work. HTTP 408, transport failures, and 5xx are always ambiguous.

## Send from the CLI or GitHub Actions

```sh
devflow notify delivery --operation-id deployment-42 --body 'Production deployment failed' --dry-run
devflow notify delivery --operation-id deployment-42 --body 'Production deployment failed'
```

Use the same operation ID, signal, message and destination names to replay an
event. Use a distinct operation ID for a new event. The CLI applies normal
remote autonomy and supports `--yes`, `--title`, and `--links` (a JSON array).

Continue to call `slack-notify.yml` for delivery signals. Existing `message`
and `SLACK_WEBHOOK_URL` callers keep the inline run link and Slack JSON format.
The `hygiene.yml` workflow emits the `hygiene` signal when it has a report.
Either workflow accepts `WEBHOOK_URL` as an additional optional secret. Pass it
explicitly from the caller's `secrets` mapping to enable the example audit
destination in CI. For repeated calls of `slack-notify.yml` in a single run
with the same `signal`, pass a distinct, stable `signal-id` to each call.
The reusable workflows load configuration from the caller's default branch.

## Read and reconcile delivery outcomes

The CLI prints JSON with one outcome per destination and exits nonzero when any
delivery needs attention. `delivered` records an acknowledgement. On replay,
`already_delivered` skips that destination, and `rejected` can be retried.
`unconfirmed` indicates the receiver may have accepted the request. Check the
receiver for that event before arranging another send. `conflict` means the
operation ID was reused with different content, scope, or delivery backend.
`receipt_error` signals local receipt trouble. If `write_status` is `applied`, the receiver acknowledged the
POST before the receipt error. `failed` includes a destination configuration or
credential error.

Local receipts live under `$DEVFLOW_HOME/notification-receipts` (default
`~/.devflow/notification-receipts`). Keep this directory with its original
repository identity and operation IDs while events can be replayed. GitHub
Actions uses an immutable reservation for each signal slot and attempt and
uploads a receipt snapshot with `delivery.json`. A rerun restores the
immediately preceding attempt's snapshot before posting. If that snapshot is
unavailable after cancellation, runner failure, a skipped attempt, or artifact
expiry, delivery stops for reconciliation. Receipt files contain payload
digests and delivery states. Credential values and message bodies stay out of
receipts and outcome reports.

Other headless runners can use `devflow notify SIGNAL --automation` with the configured
Slack webhook.
