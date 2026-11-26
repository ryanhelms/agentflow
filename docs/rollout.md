# agentflow rollout checklist

Phases are gated by outcomes, not dates. Start a phase only when the previous one has
passed its exit check. The full plan is in [blueprint.html](blueprint.html#rollout).

## Architecture approval

Kyle Redelinghuys and Vishal Srini approved [the architecture and rollout plan](blueprint.html). Each rollout phase still has its own exit check.

Foundations and verification:
- [x] `ryanhelms/agentflow` repo with CLI, MCP server, hooks, workflows, adapters and templates
- [x] CI green (tests with race detector, golangci-lint, shellcheck, actionlint, GoReleaser check)
- [x] Claude Code plugin tested end to end with `scripts/e2e-claude.sh` (12/12) and installed
      from the `ryanhelms/agentflow` marketplace
- [ ] Test with a real Jira ticket once a pilot developer runs `agentflow login` (read-only tools first)

## Provider migration rehearsal

Record the baseline before enabling a provider-backed workflow. Capture the
consumer commit, caller workflow pins, effective provider settings, and the
workflow that owns releases for that repository. Use the same event payload
and revision when comparing legacy and provider previews.

1. Review the [provider configuration](providers.md) and run
   `agentflow setup plan --json`. Complete sign-in in the runtime that will make
   provider calls. Run `agentflow setup verify` there with that runtime's
   credentials.
2. Run the consumer fixture in a separate checkout with its own agentflow home.
   Preview the candidate writes with read-only credentials. Assign one writer
   to each effect before applying any change. Keep the operation identifiers
   and delivery receipts available for replay.
3. Interrupt a run after one effect, repeat it, and compare the final receipts.
   Record which operations applied, were already present, or need human
   reconciliation. Compare the release plan against the expected units and
   versions.
4. Stop the candidate writers before restoring the previous provider settings
   and caller pins. Restore the saved configuration through its normal review
   path. Re-run the legacy preview for the same event and verify the effect
   counts and release plan. Reconcile already-applied effects with the service
   owners before considering any compensating write.

The existing [agentflow repository pilot](https://github.com/nscaledev/agentflow/issues/25)
provides issue and ADR replay evidence. The [consumer rehearsal report](consumer-canary.md#recorded-consumer-rehearsal)
links the hosted canary, rollback, and release verification.

## P0 · Foundations
- [ ] Engineering leadership nominates the pilot team and two pilot repos
- [ ] Install the GitHub for Jira app on the `nscaledev` org (all repos)
- [ ] Create the `svc-agentflow` Atlassian account and scoped API token ([operations](operations.md#atlassian-service-account))
- [ ] Org secrets/variables, or repo-level ones in pilot repos ([operations](operations.md)); the agentflow GitHub App is optional ([operations](operations.md#github-app-optional))
- [ ] Pilot developers run `agentflow login` with their own Atlassian API token ([operations](operations.md#developer-atlassian-login))
- [ ] Pilot lead fills in the pilot project's block in `flow-map.yml`
- [ ] Eng managers publish the shared Confluence templates in the Engineering space
- [ ] Capture baseline metrics (key coverage, manual transitions, release-notes lead time)
- [x] First agentflow releases published (`v0.1.0`, `v0.2.0`); floating `v0` tag points at the latest
- **Exit:** pilot named; dev panel shows commits on a pilot ticket; pilot flow-map row reviewed

## P1 · Commit convention
- [x] Run `agentflow init` in the first pilot repo (`nscaledev/mcp-gateway`)
- [ ] Run `agentflow init` in pilot repos; commit `.agentflow/`, `.githooks/`, client configs
- [ ] Pilot engineers run `git config core.hooksPath .githooks` and `agentflow login`
- [ ] `git config user.email` set to @nscale.com addresses (Smart Commits)
- [ ] agentflow session context, commit generate/validate/split, breaking-change detection in use
- **Exit:** every pilot engineer has the hook; Smart Commits resolve to their Jira user

## P2 · PR key enforcement
- [ ] Caller workflow `key-check` with `warn-only: true` (it applies the repo's `.agentflow/config.yml` rules, so repos without a Jira project can set `commit.require_key: false`)
- [ ] Fix common misses; switch to `warn-only: false` and require `key-check / validate` in an org ruleset
- [ ] agentflow PR readiness check and PR opening
  - Verify a keyless feature branch fails the Jira branch check with the default rules
    and passes it with `commit.require_key: false`. For a keyless PR, supply a
    conventional title explicitly. Verify that branches with a Jira key still show
    Jira effect previews and acceptance criteria when available.
- **Exit:** 95% of pilot commits and PRs carry a valid key; check required

## P3 · Ticket transitions
- [ ] Set repo variable `AGENTFLOW_ENABLED=true`
- [ ] Branch, PR and merge transitions plus commit verbs live
- [ ] agentflow dry-run preview, pick + start, AC tracking, scope drift, review follow-up, collision radar
- **Exit:** no manual ticket moves by the pilot team; no wrong-direction transitions across a full sprint

## P4 · Releases & deployment tracking
- [ ] `agentflow release init` on pilot repos; review `.agentflow/releases.yml` units and strategy ([releases](releases.md))
- [ ] First fully automatic SemVer GitHub Release published per pilot deployable (artifacts + attestations attached)
- [ ] `ryanhelms/agentflow/actions/release-guard@v0` first in every production deploy job
- [ ] `production-hotfix` environment with required reviewers
- [ ] Staging builds tagged with `ryanhelms/agentflow/actions/version@v0`
- [ ] `.jira/config.yml` committed; deploy jobs use GitHub Environments
- [ ] `ryanhelms/agentflow/actions/ship@v0` added after production deploys (with `fix-version`)
- [ ] agentflow ship status
- **Exit:** every pilot deployable has published a release; production deploys only
  releases or approved hotfixes; every production deploy shows on its tickets and moves them

## P5 · Release notes & docs-as-code
- [ ] `release-notes` job with the pilot's Confluence space and parent page id
- [ ] `docs/confluence.yml` mapping; docs-sync live
- [ ] agentflow Confluence context, ADR drafts, release preview
- **Exit:** release notes published within 10 minutes of a release with no manual edits

## P6 · Epic pages & sprint reports
- [ ] Jira Automation: Epic → design doc ([spec](../jira-automation/epic-design-doc.md))
- [ ] Monday sprint report job
- [ ] agentflow standup and end-of-day drafts
- **Exit:** pilot uses generated design docs and reports in planning and retros

## P7 · Signals
- [ ] Slack webhooks per team; hygiene job; blocked → Slack rule
- [ ] Incident flow rules; auto-issues job (add `workflow_run` for CI names)
- **Exit:** alert noise reviewed with the pilot; nothing muted for being too chatty

## P8 · Org-wide rollout
- [ ] Team by team: add the project block to `flow-map.yml` (team lead approves), run `agentflow init`, confirm against real tickets, enable
- **Exit:** all in-scope teams on the flow map and meeting the success measures

## P9 · AI assist
- [ ] Covered by Nscale's existing Anthropic approval; set `ANTHROPIC_API_KEY` org secret
- [ ] Pilot repos add the `agentflow-ai` topic
- [ ] Measure how much engineers edit the drafts
- **Exit:** pilot engineers keep it on; approval to widen
