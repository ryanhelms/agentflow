# Feature inventory and history

This review starts at the Agentflow rename squash merge at `ba469a2`. Devflow `v0.52.2` was the final pre-cutover release. Agentflow `v0.53.0` is the first signed release under the canonical repository name. A source entry below identifies implementation or repository evidence. A live launch rehearsal is still required where an operation depends on a client, provider, or runner.

## Milestones

| Milestone | Source evidence |
| --- | --- |
| CLI, MCP server, hooks, adapters, GitHub Actions, and Atlassian delivery loop | Initial implementation `9537532` and [repository README](../../README.md) |
| Semantic releases, release guards, immutable caller pins, and signed artifacts | [Release history](../../CHANGELOG.md) and [release guide](../releases.md) |
| Typed provider architecture, multiple task and knowledge backends, receipts, and setup | [Provider decision](../knowledge/engineering/decisions/provider-architecture.md) and [provider configuration](../providers.md) |
| Desktop provider connections and CLI/MCP parity | PRs #63–#82 and [client setup](../client-plugins.md) |
| Open Knowledge Format adoption and verified consumer rehearsal | PRs #90–#93, [rollout evidence](../okf-rollout.md), and [consumer canary](../consumer-canary.md) |
| Local repository console, shared approvals, configuration editors, and opt-in providers | PRs #98, #100, #105, #115, #116, #118, and #120 |
| Agentflow naming and migration | PR #121, [migration guide](../agentflow-migration.md), and [signed Agentflow v0.53.0 release](https://github.com/nscaledev/agentflow/releases/tag/v0.53.0) |

## Capability inventory

| Area | Shipped entry points and requirements | Evidence or verification |
| --- | --- | --- |
| Project setup and undo | CLI and manager plugin. Init, provider detection and guidance, install, remove, and pin. | [Workstation setup](workstation.md), [init guide](../knowledge/engineering/guides/project-installation.md), and PR #102 |
| Versioned binary and installer | GitHub Releases, Bash and PowerShell installers, update and rollback. Requires release and artifact read access. | [Migration](../agentflow-migration.md) and [release history](../../CHANGELOG.md) |
| Git commit discipline | Commit parser, hooks, PR convention checks, Jira key policy, commit planning and breaking-change guidance. | [Repository rules and usage](../../README.md#commit-convention) |
| Task lifecycle | Work listing, context, task creation, bind/start, comment, transitions, task links, templates, and automation routing. | [Provider configuration](../providers.md), [GitHub workflow](../knowledge/engineering/guides/task-workflow.md), and [consumer rehearsal](../consumer-canary.md) |
| Acceptance and scope | Acceptance checklist, local done state, scope-drift checks, collision radar, and optional progress posting. | [Command reference](reference.md) and [CLI usage](../../internal/cli/cli.go) |
| Branch and PR publication | Readiness, draft, review threads, mark-ready, push, review replies, and approved publication effects. | [Command reference](reference.md), PR #105, and PR #120 |
| Runtime and client registration | CLI, MCP, project launchers, assisted provider setup, and host-specific connector support. | [Client plugins](../client-plugins.md), [compatibility](compatibility.md), and parity test |
| Provider configuration | Layered definitions, explicit task and knowledge roles, setup explanation, verification, companion lifecycle. | [Provider definitions](../design/provider-definitions.md), [provider configuration](../providers.md), and PR #118 |
| Jira and Confluence | Jira tasks and configured transitions, Confluence search, revision-checked publishing and receipt verification. | [Working example](jira-confluence.md), PRs #70, #72, #74–#76, #79, #82 |
| GitHub, Asana, and file-backed tasks | Provider-specific task support governed by configuration and authorization. | [Provider configuration](../providers.md), PRs #50, #58, #80, #81 |
| Repository knowledge and OKF | Search, read, publish and link knowledge, including repository and wiki selections. | [Knowledge runbook](../knowledge/engineering/runbooks/knowledge-publication.md), PRs #51, #90–#93 |
| Local console configuration | Loopback UI, worktree-specific drafts, typed editors, readiness, provider and autonomy controls, and MCP open. | [Console guide](../repository-console.md), PRs #98, #100, #104, #115, #116 |
| Approval scopes and auditing | Effective policy, prepare/apply confirmation, scoped approvals and worker handoff, automatic PR publication rules. | [Console guide](../repository-console.md), PR #105, PR #120 |
| Delivery automation | Consumer release-please and version units, release lookup and publication, deployment checks, prereleases, and release guard. | [Consumer releases](../releases.md) and [canary](../consumer-canary.md) |
| Agentflow release operation | Commit-derived version, generated changelog, signature and checksum checks, verified promotion. | [Release process](../../CONTRIBUTING.md#how-releases-happen) and PR #121 |
| Signals and reporting | Slack/webhook notifications, action receipts and replay, standups, EOD notes, hygiene and sprint-report workflows. | [Notifications](../notifications.md), [operations](../operations.md), and [CLI](../../internal/cli/cli.go) |
| Security intake | Preview scoped provider tasks from supported security sources, with replay identity. | [Security triage](../secops.md) and [reporting policy](../../SECURITY.md) |
| Upgrade and backwards compatibility | Legacy settings preservation, renamed workflow pins, client re-registration, and consumer rollback. | [Migration](../agentflow-migration.md) and PR #121 |

## Launch findings

| Finding | Launch disposition |
| --- | --- |
| The existing site version list stopped at Devflow v0.46.0 and had outdated rollout status. | Replace hard-coded versions with the changelog and an explicitly verified release. |
| The cutover and first signed Agentflow release are complete. | Verify the pinned installer and signer when publishing each site image. |
| The consumer canary exercised GitHub Issues and repository knowledge. Jira and Confluence need launch-specific cross-client evidence. | Run the approved sandbox in the terminal, Claude Code Desktop, and Codex. Publish only the observed support claims and reviewed captures. |
| Preview and engineering access require deployment configuration and pilot approvals. | Engineering Enablement captures the approved host, access groups, image digest, and promotion evidence before widening access. |
