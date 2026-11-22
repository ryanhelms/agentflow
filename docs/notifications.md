# Delivery notifications

`.devflow/config.yml` selects notification destinations independently of the
code host and task, knowledge, DevOps, and SecOps providers. An omitted section
uses the `slack` provider and reads the incoming webhook from `SLACK_WEBHOOK_URL`.
An unset legacy variable produces a successful skip. Set `provider: none` to
disable delivery. Single-provider `slack` and `webhook` settings accept
`destination` and `credential_ref`. The standalone `webhook` provider defaults
to `WEBHOOK_URL` and reports a missing credential as a destination failure.

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
stable across replays. Put HTTPS webhook URLs only in the environment variables
named by `credential_ref`. The router and standalone webhook settings also
accept `header_refs` as a map from header names to environment variable names.

Slack receives an incoming-webhook JSON object with `text` and supports
mrkdwn. Generic webhooks receive a JSON object with `destination`, `title`,
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
operation ID was reused with different content. `receipt_error` signals local
receipt trouble. If `write_status` is `applied`, the receiver acknowledged the
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
