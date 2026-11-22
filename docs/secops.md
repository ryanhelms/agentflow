# Triage security events

Configure the sources in `.devflow/config.yml` and select the task provider
under `task_management`. GitHub findings are read from the configured
GitHub.com repository, or from the code-hosting target when the repository
is omitted. Each enabled GitHub source reads the first 100 open alerts.
The default sources are Dependabot and code scanning. The default minimum
severity is `high`. Set `findings: []` for fixture-only triage or select
`secops.provider: none` to disable triage.

```yaml
secops:
  provider: github
  settings:
    repository: example/service
    findings: [dependabot, code-scanning]
    min_severity: high
    incidents:
      fixture: .devflow/incidents.json
      scope: example/service

task_management:
  provider: github-issues
  settings:
    repository: example/triage
```

The optional incident fixture is a checkout-relative JSON file with an array
of rows. Each row has `id`, `title`, `severity` and an HTTP(S) `url`, plus
optional `body` and RFC3339 `updated_at`. Keep its scope stable when the file
moves. Supply a read credential through `DEVFLOW_SECOPS_TOKEN` when the
findings need different permissions from the task destination. The task
destination uses the usual provider credentials.

```bash
devflow secops triage --since 2026-02-01T00:00:00Z
devflow secops triage --source incident-fixture/example%2Fservice/INC-7 --apply
```

The first command previews eligible tasks. Copy `source` from the preview
to select one task for creation. GitHub Issues, FileSystem and Repository
support this route. The chosen provider's default task template supplies
fields and required content, and devflow stores the template revision on
the task. GitHub Issues puts the source link in the body. File-backed tasks
store a structured source link. Repository tasks follow their configured
publication and protected-branch policy.

Devflow derives the operation ID from the source and task destination. Replay
the same reference after a failed or interrupted call. File-backed tasks
persist a provider receipt and reject a changed draft for an existing
operation. GitHub Issues stores an operation marker in the issue and keeps an
attempt claim under `DEVFLOW_HOME/secops-operations`. A retry can recover the
issue after GitHub makes the marker visible, including after a template revision
changes. An existing claim with no visible marker blocks a second POST. Keep the
same devflow home for all attempts and retain issue markers. The source must
still be eligible for triage when retrying from the source reference.

## Run from GitHub Actions

Call the reusable `secops-triage.yml` workflow from `schedule` or
`workflow_dispatch` on the default branch. Leave `apply-source` empty for a
preview. To approve one operation, pass the previewed source reference as
`apply-source` from a manual dispatch. Apply jobs use a self-hosted runner
labeled `devflow-secops` unless the caller supplies `runner-label`. Configure
that runner's `DEVFLOW_HOME` on persistent
storage shared by runners serving the same destination, and configure task
destination credentials through the usual provider setup. Pass
`SECOPS_READ_TOKEN` when the default token cannot read the selected sources.
For FileSystem task writes, use an absolute task directory on persistent storage.

Callers of `auto-issues.yml` can set `provider-secops: true` to use the same
provider workflow for security intake. Pass `secops-source` for manual apply.
The default security route in `auto-issues.yml` continues to serve legacy
Jira callers. Use a single intake route for each source and destination.
