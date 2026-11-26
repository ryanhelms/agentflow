# Consumer task workflow canary and rollback

Run `scripts/consumer-canary.sh` from a checkout with Git, Bash, Go, the cached
Go modules, jq and Mike Farah yq v4. The script builds a binary with synthetic
release metadata and creates a temporary consumer repository. Its release
inputs and provider responses are fixtures. A successful run ends with
`PASS: legacy caller and config restored, manifest preserved`.

## What the fixture checks

| Path | Evidence |
| --- | --- |
| Caller contract | A stale synthetic SHA fails `agentflow pin --check`. The check leaves the caller intact. `agentflow pin` updates each agentflow reference and the harness compares the entire caller with its expected content. |
| Jira event parity | The legacy shell handler and the Go planner consume the same JSON payloads. Tests compare the task and lifecycle for branch creation, PR events, default-branch merges and commit verbs, including skips. The provider planner drops fork PRs before reading tasks. |
| GitHub Issues | The real adapter reads a fixture issue using the runner token. A merge replay with mock approval produces one state change. A consumer repository with the same approval variable cannot apply. |
| File-backed consumer | The validator checks PR and commit trailers. The PR plan matches the ordinary CLI preview. Replaying the run returns the same operation ID and effect. The ship action returns the qualified key, honors commit opt-outs and previews the deploy transition. |
| Partial completion | Issue creation reuses its operation marker. A lost document PR creation response is reconciled by head and base. A release creation error is reconciled by its tag and target. Notification fan-out retries a rejected destination and retains the receipt for the successful one. These checks use in-process mock services. |
| Releases | The fixture preserves existing release-please manifest versions, checks both the no-release baseline and per-unit bumps, generates package config and runs the production matrix step with recorded action outputs for a root and a nested package. It checks tag, version, URL and build metadata. Separately, the version step used by agentflow's GoReleaser workflow derives the expected bump and release notes from local Git history. |
| Rollback | Restore the original caller and provider config from the baseline commit, check both byte-for-byte, verify Jira selection and confirm the manifest and package config are unchanged. A required Jira token missing from the fixture produces a named error in strict mode and a warning with a successful exit in warn-only mode. |

## Consumer rehearsal

1. Record the current caller commit, provider config, previous release manifest,
   released tags and the last processed event or workflow run ID. Keep the
   existing operation receipts for each enrolled destination.
2. Review a change to the provider config and the SHA-pinned caller together.
   The updated `jira-key.yml` retains `warn-only` and validates the exact squash
   subject and each non-merge commit against the base configuration. Jira checks
   keys in the PR title. GitHub Issues, FileSystem and Repository check qualified
   `Task-Ref` trailers in PR bodies and commit messages.
3. Run `transitions.yml` with `dry-run: true` for the legacy baseline.
   The generic workflow previews effects for new task provider selections. Jira also recognizes keys in branch names. A provider that
   uses qualified trailers needs a PR or commit body to identify a task.
   The shared planner honors draft PRs, fork boundaries, default-branch
   restrictions and the `[agentflow skip]` and `Agentflow: skip` opt-outs.
4. Scope credentials to the selected reader. Jira uses the `JIRA_BASE`
   repository variable and the `JIRA_USER` and `JIRA_TOKEN` secrets. For a
   private GitHub Issues repository, pass the optional `TASK_READ_TOKEN` with
   Issues read access to that repository if the built-in token cannot read it.
   Repository tasks use Git read access to their configured origin. FileSystem
   reads records available in the trusted checkout. Asana reads use
   `ASANA_ACCESS_TOKEN`. Hosted event previews use its configured default
   project. A missing credential or
   unreadable referenced task identifies the failing check.
5. Compare task, destination, event, target state, release unit and tag against
   the legacy plan. Record the preview and run ID with the baseline. Keep one
   writer per real event. The agentflow repository pilot has a separate approval
   for its enrolled issue and verifies its ADR before closing that issue. A
   consumer can apply an individual reviewed change through its normal
   `agentflow transition` approval flow.

## Rollback after an interrupted run

1. Disable the caller with `AGENTFLOW_ENABLED=false`. Pause any separately
   authorized writer, including standalone deploy jobs calling `actions/ship`.
   Account for jobs already in flight.
2. Reconcile completed effects against their original identifiers. Check the
   issue state, document branch and PR, tag and release target, and notification
   receipts per destination. Resolve ambiguous delivery acknowledgments with
   the destination owner before any retry.
3. Restore the recorded caller and provider config together. Keep the release
   manifest, existing tags, documents, issues and receipts. Compare the restored
   caller against the recorded file including SHA pins, job permissions,
   declared secrets and opt-outs.
4. Replay the recorded inputs in preview, compare the resulting plans and mark
   completed effects. Re-enable the selected writer for subsequent events.
   Retain the before/after diff, run IDs and reconciliation results as evidence.

## Recorded consumer rehearsal

The isolated `nscaledev/mcp-gateway` branch rehearsed agentflow v0.20.0 against
consumer baseline `9a12968`. The runner selected the FileSystem task provider
and verified every updated caller pin, then restored the exact legacy config
and caller. Both runs checked the existing image workflow against the same
baseline. [Consumer canary](https://github.com/nscaledev/mcp-gateway/actions/runs/44981506133),
[rollback rehearsal](https://github.com/nscaledev/mcp-gateway/actions/runs/44981636085).

The fixture interrupted the delivery after Slack accepted it and the webhook
rejected it. Replay retried the rejected destination and finished with one
task, one document, one release and one accepted notification per destination.
The Git-backed and Jira event fixtures checked the legacy comparisons and
restored the original settings and manifest.

Both hosted runs passed the native Windows companion install and client
registration fixtures. Release Please 17.3.0 used a read-only token and
proposed one `0.2.0` release PR in dry-run mode. `mcp-gateway`'s image workflow
continues to own its production versions. Agentflow's separate GoReleaser
workflow published v0.20.0. [GoReleaser run](https://github.com/ryanhelms/agentflow/actions/runs/44981124315),
[verification work item](https://github.com/nscaledev/agentflow/issues/29).
