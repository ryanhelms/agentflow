# devflow rollout checklist

Phases are gated by outcomes, not dates. Start a phase only when the previous one has
passed its exit check. The full plan is in [blueprint.html](blueprint.html#rollout).

## P0 · Foundations
- [ ] Engineering leadership nominates the pilot team and two pilot repos
- [ ] Install the GitHub for Jira app on the `nscaledev` org (all repos)
- [ ] Create the `svc-devflow` Atlassian account and scoped API token ([operations](operations.md#atlassian-service-account))
- [ ] Create the devflow GitHub App and org secrets/variables ([operations](operations.md#github-app))
- [ ] Pilot developers run `devflow login` with their own Atlassian API token ([operations](operations.md#developer-atlassian-login))
- [ ] Pilot lead fills in the pilot project's block in `flow-map.yml`
- [ ] Eng managers publish the shared Confluence templates in the Engineering space
- [ ] Capture baseline metrics (key coverage, manual transitions, release-notes lead time)
- [ ] First devflow release published and promoted; `v1` tag points at it
- **Exit:** pilot named; dev panel shows commits on a pilot ticket; pilot flow-map row reviewed

## P1 · Commit convention
- [ ] Run `devflow init` in pilot repos; commit `.devflow/`, `.githooks/`, client configs
- [ ] Pilot engineers run `git config core.hooksPath .githooks` and `devflow login`
- [ ] `git config user.email` set to @nscale.com addresses (Smart Commits)
- [ ] devflow session context, commit generate/validate/split, breaking-change detection in use
- **Exit:** every pilot engineer has the hook; Smart Commits resolve to their Jira user

## P2 · PR key enforcement
- [ ] Caller workflow `key-check` with `warn-only: true`
- [ ] Fix common misses; switch to `warn-only: false` and require `key-check / validate` in an org ruleset
- [ ] devflow PR readiness check and PR opening
- **Exit:** 95% of pilot commits and PRs carry a valid key; check required

## P3 · Ticket transitions
- [ ] Set repo variable `DEVFLOW_ENABLED=true`
- [ ] Branch, PR and merge transitions plus commit verbs live
- [ ] devflow dry-run preview, pick + start, AC tracking, scope drift, review follow-up, collision radar
- **Exit:** no manual ticket moves by the pilot team; no wrong-direction transitions across a full sprint

## P4 · Deployment tracking
- [ ] `.jira/config.yml` committed; deploy jobs use GitHub Environments
- [ ] `nscaledev/devflow/actions/ship@v1` added after production deploys (with `fix-version`)
- [ ] devflow ship status
- **Exit:** every production deploy shows on its tickets and moves them

## P5 · Release notes & docs-as-code
- [ ] `release-notes` job with the pilot's Confluence space and parent page id
- [ ] `docs/confluence.yml` mapping; docs-sync live
- [ ] devflow Confluence context, ADR drafts, release preview
- **Exit:** release notes published within 10 minutes of a release with no manual edits

## P6 · Epic pages & sprint reports
- [ ] Jira Automation: Epic → design doc ([spec](../jira-automation/epic-design-doc.md))
- [ ] Monday sprint report job
- [ ] devflow standup and end-of-day drafts
- **Exit:** pilot uses generated design docs and reports in planning and retros

## P7 · Signals
- [ ] Slack webhooks per team; hygiene job; blocked → Slack rule
- [ ] Incident flow rules; auto-issues job (add `workflow_run` for CI names)
- **Exit:** alert noise reviewed with the pilot; nothing muted for being too chatty

## P8 · Org-wide rollout
- [ ] Team by team: add the project block to `flow-map.yml` (team lead approves), run `devflow init`, confirm against real tickets, enable
- **Exit:** all in-scope teams on the flow map and meeting the success measures

## P9 · AI assist
- [ ] Covered by Nscale's existing Anthropic approval; set `ANTHROPIC_API_KEY` org secret
- [ ] Pilot repos add the `devflow-ai` topic
- [ ] Measure how much engineers edit the drafts
- **Exit:** pilot engineers keep it on; approval to widen
