# ACP platform program: ledger

One row per lane in [plan.md](plan.md). The coordinator updates this table when a lane's issue, PR or status changes.

Models: O = Opus 5.5, S = Sonnet 5.5, F = Fable 5.2.

Phase gates:

| Phase | Integration PR | Tag | Demo | Status |
| --- | --- | --- | --- | --- |
| 1 | #31 | `develop-p1` at `5cf3e6d` | the live event pipeline end to end | done |
| 2 | #51 | `develop-p2` at `a5648e3` | approvals, the commit hook, config, client settings and tabs live; MCP reload | done |
| 3 | #59 (contract, issue #52) | `develop-p3` at `c555ac4` | a write such as `commit --prepare` or `notify` shows `op.requested` and its outcome in `agentflow events` | done; promoted to `main` by auto-promote, released as v0.57.0 on 2026-11-26 |
| 4 | #72 (contract, issue #68) | `develop-p4` at `33f5ffd` | `config set --dry-run` flags a loosening edit; `console.autonomy: suggest` blocks an autonomy raise; the Settings page review with loosening ticks | done; promoted to `main`, released as v0.58.0 on 2026-11-26 |
| 5 | contract issue #77 | | `plugin add` both samples; a CI failure becomes a `notify` operation from `plugin:ci-notify` on the timeline; a failed operation gets a `timeline-hints` annotation; the Plugins page shows live state and a restart after a crash; plugin settings on the Settings page | contract in progress |

Lanes:

| Lane | Work | Model | Issue | PR | Status |
| --- | --- | --- | --- | --- | --- |
| 0.1 | Create `develop`. PR into `main` (you merge it): `policy.coordinator_merge: true`, CI on `push` to `develop`, the `AGENTS.md` "Active program" section, `docs/plans/acp-platform/{goal,plan,ledger}.md`, and a README/`docs/worktrees.md` note | O | #12 | | in progress |
| 0.2 | Golden tests that pin today's `Pending` JSON and CLI `--json` output for about 10 operations | S | #13 | | in progress |
| 1.0 | Phase 1 contract ADR | O | #14 | | in progress |
| 1.1 | Bus: Pub-Sub, Point-to-Point, Wire Tap to `policy.Gate.Audit`, Dead Letter, bounded buffers | O | | #31 | done |
| 1.2 | Message store: JSONL, rotation, 7 days / 50 MB retention, replay from cursor, single-writer rule | O | | #31 | done |
| 1.3 | Watcher, git and repo config only: HEAD, index, refs, worktrees, rebase state, `.agentflow/*`; fsnotify with polling fallback; debounce (Aggregator) | O | | #31 | done |
| 1.4 | SSE endpoint `/api/events`: snapshot + delta, `Last-Event-ID` replay, resync; overview projection; `agentflow events` CLI and `agentflow_events` MCP | O | | #31 | done |
| 1.5 | Browser event client: one SharedWorker stream, snapshot store, connection and resync states; overview panel driven by it | F | | #31 | done |
| 2.1 | Watcher, more sources: approvals journal, `~/.agentflow/**`, client settings files | S | | #51 | done |
| 2.2 | Hook → daemon local socket (Unix socket; loopback with token on Windows); hooks publish `hook.*` and `commit.created` | S | | #51 | done |
| 2.3 | GitHub poller with ETags and rate-limit backoff, publishing `gh.*` | S | | #51 | done |
| 2.4 | Activity, approvals and tabs projections; `tabHub` moved onto the stream; old tab stream retired | O | | #51 | done |
| 2.5 | UI: activity, approvals and handoff panels driven by events; resync control; live "changed" highlights | F | | #51 | done |
| 2.6 | MCP server reloads config on `config.changed` | S | | #51 | done |
| 3.1 | Command registry and Invoker; CLI dispatch, MCP tools, usage and parity derived from it; 3 operations moved as examples | O | #53 | #61 (in #59) | done |
| 3.2 | Move commit, PR, push and review (after 3.1) | S | #54 | #63 (in #59) | done |
| 3.3 | Move task, transition, comment, ticket, AC and scope (after 3.1) | S | #55 | #62 (in #59) | done |
| 3.4 | Move docs, release, notify, setup, worktree and secops (after 3.1) | S | #56 | #64 (in #59) | done |
| 3.5 | Timeline UI: live operation feed, filters, links to issue, PR and audit entry | F | #57 | #60 (in #59) | done |
| 4.0 | Phase 4 contract ADR: change set, diagnostics, preview, save, undo journal, field metadata, settings projection, commands, ownership | O | #68 | #72 | done |
| 4.1 | `Field` metadata (label, description, group, widget, applies, sensitive, source) and `TestEveryConfigFieldHasControl` | S | #69 | #73 | done |
| 4.2 | Settings backend as Commands: change set, live checks, preview, atomic save, personal layer applied at once, repo layer written to the worktree and published as a PR, undo last save; `agentflow config set A=1 B=2` | O | #70 | #74, #75 | done |
| 4.3 | Settings UI: controls for every field type, Save bar, preview, conflict warnings, undo, search, "modified" filter | F | #71 | #76 | done |
| 5.0 | Phase 5 contract ADR: manifest, tiers, extension points, JSON-RPC methods, lifecycle, events, plugins projection, settings, commands, security, ownership | O | #77 | | in progress |
| 5.1 | Shared stdio JSON-RPC framing (from `codexhost/stdio.go`, which stays as it is) and scrubbed-environment launcher | O | #78 | | pending |
| 5.2 | Plugin host: three tiers, registry (Abstract Factory), lifecycle, crash isolation into Dead Letter | O | #79 | | pending |
| 5.3 | Plugin commands, the two sample plugins, docs (`docs/plugins.md`) | S | #80 | | pending |
| 5.4 | Plugins page: installed plugins, status, what each adds, live health | F | #81 | | pending |
| 6.1 | Harness registry from the pinned ACP registry copy, plus YAML overrides; PATH and version probes; no installs | S | | | pending |
| 6.2 | `initialize` probe with timeout and cache; `harness.*` events from slow polling and focus checks | O | | | pending |
| 6.3 | `ai harnesses` and `ai doctor` commands, skills, README | S | | | pending |
| 6.4 | Harnesses page: states, fixes, sign-in guidance | F | | | pending |
| 7.1 | ACP v1 client and fake ACP agent for tests | O | | | pending |
| 7.2 | Background sessions (Process Manager), session store, permission chain, worktree-only file and terminal access | O | | | pending |
| 7.3 | `ai run/status/respond/cancel` commands; agentflow's own MCP server passed to the harness, with a guard against starting sessions without limit | S | | | pending |
| 7.4 | AI panel: streaming output, tool calls, plan view, permission prompts | F | | | pending |
| 8.1 | Feature framework, routing settings (`ai.features.<name>.harness`), prompt context as data | O | | | pending |
| 8.2 | Commit and PR drafting; acceptance-criteria check (after 8.1) | S | | | pending |
| 8.3 | Ticket from text; review-thread replies (after 8.1) | S | | | pending |
| 8.4 | "Draft with AI" controls and draft review UI | F | | | pending |
| 9.1 | ACP transport for `delegate`; agents come from the harness registry | O | | | pending |
| 9.2 | Second opinion | O | | | pending |
| 9.3 | Fan-out comparison | O | | | pending |
| 9.4 | Compare view and delegated session view | F | | | pending |
| 10.1 | Standup and EOD summaries | S | | | pending |
| 10.2 | CI failure triage | O | | | pending |
| 10.3 | Session history and resume (`session/load`, Memento) | S | | | pending |
| 10.4 | Harness definition sharing; usage recorded in audit | S | | | pending |
| 10.5 | History, reports and usage views | F | | | pending |
| 11.1 | Soak tests: retention, overflow, many worktrees, fsnotify limits and polling fallback, concurrent writers | O | | | pending |
| 11.2 | `make e2e` coverage for the new MCP tools, hooks socket and AI commands against the fake agent | S | | | pending |
| 11.3 | README, `docs/`, blueprint capability table and skills audit; `TestEveryUIPanelIsEventDriven` | S | | | pending |
| 11.4 | Full UI review across every page: consistency, accessibility, light and dark | F | | | pending |
