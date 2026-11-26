# Plan: event-driven, pluggable agentflow with ACP-driven AI, in 12 phases

## Context

agentflow pulls state on demand today:
- The web UI refreshes only when you press a button (`GET /api/state`).
- The only SSE stream carries tab navigation, and it closes after each event (`internal/webui/tabs.go:243`).
- Nothing watches git or config.
- The MCP server loads config once at startup (`internal/mcpserver/server.go:45`).
- Write operations share a gate helper but have no common abstraction (`internal/app/app.go:198-215`).

You want:
1. AI features driven over ACP v1, with installed harnesses detected automatically.
2. GoF and EIP patterns, so every part is pluggable and provider-driven.
3. Real-time UI over SSE, fed by detection of every relevant change.
4. Every config option behind a structured control, with one global Save bar.

This plan delivers that in **12 phases**. Each phase builds on the one before, ends with something you can see and use, and leaves `develop` releasable. A coordinator session runs the phases on its own. You can promote `develop` to `main` at any phase checkpoint, or only at the end.

## Decisions already made

| Decision | Choice |
| --- | --- |
| Merge point | New `develop` branch, `policy.coordinator_merge: true`. The coordinator merges green PRs into `develop`, never into `main`. You run `agentflow promote`. |
| Scope | Everything: platform, plugin host, ACP and every AI feature. |
| Goal record | "Active program" section in `AGENTS.md` pointing to `docs/plans/acp-platform/` (goal, plan, ledger), plus a memory note. |
| Settings | Global Save bar, live checks and effective-config preview, atomic save, undo last save. Loosening autonomy is confirmed in the Save review. |
| ACP | v1. Standard-library JSON-RPC client. Registry copy pinned per release. No runtime fetch, no automatic adapter install. |
| New dependency | `fsnotify` only, with polling fallback. |
| Plugin tiers | Declarative YAML, out-of-process JSON-RPC, built-in registry. No Go `plugin` package. |

## Findings that shape the order

- `internal/event` already exists (transition plans), so the bus goes in `internal/bus`.
- Every new tool edits `cli.go`, `mcpserver/server.go`, `parity_test.go` and README. The Command registry (phase 3) makes registration per-file, so the later phases can run lanes in parallel.
- `ci.yml` runs on `push` only for `main`. Phase 0 adds `develop`.
- Several processes write events (CLI, MCP servers, hooks, daemon). Rule: the daemon is the only writer when it runs, and others send to it over a local socket. With no daemon running, a process appends under `internal/filelock`.
- One daemon serves many repos, so `seq` is counted per repo and `Last-Event-ID` is a repo→seq vector.
- The message store sits in the git dir the watcher watches. Its directory is excluded from watching.

## How each phase runs

1. **Contract first.** The coordinator (Opus 5.5) writes or amends the ADR in `docs/knowledge/engineering/decisions/event-driven-platform.md` for that phase's interfaces. It merges that PR before any lane starts.
2. **Lanes in parallel.** One issue, worktree, session and PR per lane, at most 4 sessions at once. Lanes in one phase touch different files (the ownership map is in the ADR).
3. **Phase gate.** All lanes merged, `make check` green on `develop`, the phase demo works, and `make e2e` passes where the phase touches MCP, hooks or the adapter. Then the coordinator tags `develop-pN` and updates the ledger.

Models: **O** = Opus 5.5 (architecture and concurrency-heavy Go), **S** = Sonnet 5.5 (mechanical Go, wiring, docs), **F** = Fable 5.2 (all UI design and front-end). Fable runs as subagents inside UI lane sessions, since a session can start only at the coordinator's model or a cheaper one. Every UI lane also gets a Fable design review before merge.

---

## Phase 0 — Program setup

**You'll see:** a `develop` branch, the goal in `AGENTS.md`, and CI running on `develop`.

| Lane | Work | Model |
| --- | --- | --- |
| 0.1 | Create `develop`. PR into `main` (you merge it): `policy.coordinator_merge: true`, CI on `push` to `develop`, the `AGENTS.md` "Active program" section, `docs/plans/acp-platform/{goal,plan,ledger}.md`, and a README/`docs/worktrees.md` note | O |
| 0.2 | Golden tests that pin today's `Pending` JSON and CLI `--json` output for about 10 operations | S |

This is the only phase that needs you. Every later phase targets `develop`.

## Phase 1 — Live workspace overview

**You'll see:** `agentflow ui` with the overview updating within 1 second when you switch branch, commit, or edit `.agentflow/config.yml` in a terminal. No Refresh click. `agentflow events --follow` prints the same events.

**Contract:** envelope, event types, bus interfaces, store and cursor, SSE wire format (`snapshot|delta|resync`).

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 1.1 | Bus: Pub-Sub, Point-to-Point, Wire Tap to `policy.Gate.Audit`, Dead Letter, bounded buffers | `internal/bus` | O |
| 1.2 | Message store: JSONL, rotation, 7 days / 50 MB retention, replay from cursor, single-writer rule | `internal/bus/store` | O |
| 1.3 | Watcher, git and repo config only: HEAD, index, refs, worktrees, rebase state, `.agentflow/*`; fsnotify with polling fallback; debounce (Aggregator) | `internal/watch` | O |
| 1.4 | SSE endpoint `/api/events`: snapshot + delta, `Last-Event-ID` replay, resync; overview projection; `agentflow events` CLI and `agentflow_events` MCP | `internal/webui/stream.go`, `projection*.go`, CLI/MCP wiring | O |
| 1.5 | Browser event client: one SharedWorker stream, snapshot store, connection and resync states; overview panel driven by it | `static/event-worker.js`, `app.js` (overview only) | F |

## Phase 2 — Everything live

**You'll see:**
- Approvals appear and clear in the UI the moment an agent asks or you answer.
- PR and CI status update live.
- Tab navigation runs over the same stream.
- Changing config makes a running MCP server pick it up without a restart.
- The Refresh button becomes a small "resync" control.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 2.1 | Watcher, more sources: approvals journal, `~/.agentflow/**`, client settings files | `internal/watch` | S |
| 2.2 | Hook → daemon local socket (Unix socket; loopback with token on Windows); hooks publish `hook.*` and `commit.created` | `internal/hooks`, `internal/notifysock`, `internal/webui/instance*.go` | S |
| 2.3 | GitHub poller with ETags and rate-limit backoff, publishing `gh.*` | `internal/gh`, `internal/watch/github.go` | S |
| 2.4 | Activity, approvals and tabs projections; `tabHub` moved onto the stream; old tab stream retired | `internal/webui/projection*.go`, `tabs.go` | O |
| 2.5 | UI: activity, approvals and handoff panels driven by events; resync control; live "changed" highlights | `static/app.js`, `tab-navigation.js`, `style.css` | F |
| 2.6 | MCP server reloads config on `config.changed` | `internal/mcpserver` (subscribe only) | S |

## Phase 3 — Operations as Commands, with a live timeline

**You'll see:** a Timeline panel. Run `agentflow commit`, `agentflow pr` or an MCP tool from any agent, and each step (requested, waiting for approval, applied or failed) appears live, linked to its issue and branch.

**Contract:** `command.Spec`, the Invoker, `op.*` events, and per-file registration.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 3.1 | Command registry and Invoker; CLI dispatch, MCP tools, usage and parity derived from it; 3 operations moved as examples | `internal/command`, `cli.go`, `parity*.go`, `mcpserver/server.go` | O |
| 3.2 | Move commit, PR, push and review (after 3.1) | their `internal/app` files | S |
| 3.3 | Move task, transition, comment, ticket, AC and scope (after 3.1) | their `internal/app` files | S |
| 3.4 | Move docs, release, notify, setup, worktree and secops (after 3.1) | their `internal/app` files | S |
| 3.5 | Timeline UI: live operation feed, filters, links to issue, PR and audit entry | `static/timeline.*` | F |

`TestEveryWriteOperationEmitsEvents` is added at the end of this phase.

## Phase 4 — Settings with a global Save bar

**You'll see:** a Settings page where every option has a structured control: toggles, selects, steppers, duration pickers, chip lists, key/value tables, live pickers for providers, branches and files, and credential-reference pickers. Changes show a live preview and diff. One Save bar saves everything together, then Undo. Outside edits show up live, with a warning for conflicting fields.

**Contract:** `ChangeSet`, `Diagnostic`, the preview response, the undo journal, and `Field` widget metadata.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 4.1 | `Field` metadata (label, description, group, widget, applies, sensitive, source) and `TestEveryConfigFieldHasControl` | `internal/config/schema.go`, `schema_coverage_test.go` | S |
| 4.2 | Settings backend as Commands: change set, live checks, preview, atomic save, personal layer applied at once, repo layer written to the worktree and published as a PR, undo last save; `agentflow config set A=1 B=2` | `internal/configconsole`, `internal/consolecfg`, `internal/webui/settings.go` | O |
| 4.3 | Settings UI: controls for every field type, Save bar, preview, conflict warnings, undo, search, "modified" filter | `static/typed-editor.*`, `static/settings-*.js` | F |

## Phase 5 — Plugin host

**You'll see:**
- `agentflow plugin list/add/remove`, and a Plugins page.
- Two sample plugins:
  - A YAML plugin that turns `ci.updated` failures into a notification.
  - A small out-of-process plugin that adds a timeline annotation.
- Each plugin's own settings appear on the Settings page automatically.

**Contract:** the plugin manifest, the JSON-RPC methods (`initialize`, `subscribe`, `command.invoke`, `shutdown`), and the extension point list (`harness`, `provider`, `source`, `sink`, `handler`, `permission_rule`, `widget`).

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 5.1 | Shared stdio JSON-RPC framing (from `codexhost/stdio.go`, which stays as it is) and scrubbed-environment launcher | `internal/jsonrpc` | O |
| 5.2 | Plugin host: three tiers, registry (Abstract Factory), lifecycle, crash isolation into Dead Letter | `internal/plugin` | O |
| 5.3 | Plugin commands, the two sample plugins, docs (`docs/plugins.md`) | command files, `examples/plugins/` | S |
| 5.4 | Plugins page: installed plugins, status, what each adds, live health | `static/plugins.*` | F |

## Phase 6 — Harness detection

**You'll see:** `agentflow ai harnesses --probe` and `agentflow ai doctor` list every ACP harness installed on this machine with its state (`native`, `adapter_installed`, `adapter_available`, `needs_auth`, `incompatible`) and the exact fix for each. A Harnesses page updates live when you install or remove one.

**Contract:** `Harness`, harness as a plugin extension point, and the detection states.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 6.1 | Harness registry from the pinned ACP registry copy, plus YAML overrides; PATH and version probes; no installs | `internal/harness` | S |
| 6.2 | `initialize` probe with timeout and cache; `harness.*` events from slow polling and focus checks | `internal/harness/probe.go`, `internal/watch` | O |
| 6.3 | `ai harnesses` and `ai doctor` commands, skills, README | command files, `adapters/claude-code/skills/ai*/` | S |
| 6.4 | Harnesses page: states, fixes, sign-in guidance | `static/harnesses.*` | F |

## Phase 7 — Talk to any harness

**You'll see:**
- `agentflow ai run --harness X "<prompt>"` streams a live session in a task worktree.
- An AI panel in the UI streams the same session.
- When the harness asks for permission, the panel and CLI show the request. Policy decides it, or you approve or deny it.
- `ai status`, `ai respond` and `ai cancel` work from the CLI and MCP.

**Contract:** ACP v1 structs, `PermissionHandler` chain (never-list → boundary → policy rule → `policy.Gate`, deny by default), session state machine, `ai.session.*` events.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 7.1 | ACP v1 client and fake ACP agent for tests | `internal/acp`, `internal/acp/acptest` | O |
| 7.2 | Background sessions (Process Manager), session store, permission chain, worktree-only file and terminal access | `internal/aisession` | O |
| 7.3 | `ai run/status/respond/cancel` commands; agentflow's own MCP server passed to the harness, with a guard against starting sessions without limit | command files | S |
| 7.4 | AI panel: streaming output, tool calls, plan view, permission prompts | `static/ai-panel.*` | F |

## Phase 8 — AI in the delivery loop

**You'll see:** "Draft with AI" in the commit, PR and ticket flows, in the UI and as `--ai` on the CLI. Every draft goes through the normal prepare and confirm flow.
- Commit messages and PR titles that already pass the convention.
- A plain-text description turned into a ticket using the template.
- Acceptance-criteria checks against the diff, proposing `ac done`.
- Drafted replies to open review threads.

**Contract:** `ai.Feature` (Template Method: gather context → prompt → run → parse → prepared candidate), per-feature routing keys.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 8.1 | Feature framework, routing settings (`ai.features.<name>.harness`), prompt context as data | `internal/ai` | O |
| 8.2 | Commit and PR drafting; acceptance-criteria check (after 8.1) | `internal/ai/features/` | S |
| 8.3 | Ticket from text; review-thread replies (after 8.1) | `internal/ai/features/` | S |
| 8.4 | "Draft with AI" controls and draft review UI | `static/ai-draft.*` | F |

## Phase 9 — AI across agents and repos

**You'll see:**
- `agentflow delegate` starts and follows an ACP session in another repo, without tmux.
- `agentflow ai review --with <harness>` asks a different harness for a second opinion on the diff.
- `agentflow ai compare` runs one prompt on several harnesses in separate worktrees and shows the diffs and test results side by side.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 9.1 | ACP transport for `delegate`; agents come from the harness registry | `internal/delegate` | O |
| 9.2 | Second opinion | `internal/ai/features/review.go` | O |
| 9.3 | Fan-out comparison | `internal/ai/features/compare.go` | O |
| 9.4 | Compare view and delegated session view | `static/ai-compare.*` | F |

## Phase 10 — AI reporting and history

**You'll see:**
- Standup and EOD summaries generated from commits, PRs and transitions.
- CI failure triage with a proposed fix in the task worktree.
- A session history you can search and resume.
- Harness definitions you can share with `agentflow harness add <file>`.
- Usage per feature and harness in the timeline.

| Lane | Work | Main files | Model |
| --- | --- | --- | --- |
| 10.1 | Standup and EOD summaries | `internal/ai/features/report.go` | S |
| 10.2 | CI failure triage | `internal/ai/features/ci.go` | O |
| 10.3 | Session history and resume (`session/load`, Memento) | `internal/aisession/history.go` | S |
| 10.4 | Harness definition sharing; usage recorded in audit | `internal/harness`, command files | S |
| 10.5 | History, reports and usage views | `static/ai-history.*` | F |

## Phase 11 — Hardening and handover

**You'll see:** a promote checklist with the version preview from `bin/next-version.sh`, a full README and docs audit, and green soak tests. The run stops here for you to run `agentflow promote`.

| Lane | Work | Model |
| --- | --- | --- |
| 11.1 | Soak tests: retention, overflow, many worktrees, fsnotify limits and polling fallback, concurrent writers | O |
| 11.2 | `make e2e` coverage for the new MCP tools, hooks socket and AI commands against the fake agent | S |
| 11.3 | README, `docs/`, blueprint capability table and skills audit; `TestEveryUIPanelIsEventDriven` | S |
| 11.4 | Full UI review across every page: consistency, accessibility, light and dark | F |

---

## Guardrails

| Risk | Guardrail |
| --- | --- |
| Lanes colliding | Each PR is checked against the ADR ownership map with `agentflow collisions` and `agentflow scope`. Before phase 3, only the lane that owns CLI/MCP wiring may touch `cli.go`, `mcpserver/server.go` or `parity_test.go`. |
| Contracts drifting | A lane that needs a contract change stops and reports. The coordinator merges an ADR amendment first. |
| Regressions while moving operations | The golden tests from 0.2 must stay green. |
| Tests reaching real services | Fake ACP agent, fake PATH, temp `HOME`, `httptest` only. A guard fails any test that resolves a real harness binary. |
| Running untrusted code | No `npx`, `uvx` or downloads during detection. Harnesses run with a scrubbed environment, worktree-only access and a deny-by-default permission chain. |
| AI output treated as instructions | Issue, review and CI text goes into prompts as data. AI produces prepared candidates only. |
| Forbidden edits | PRs touching `CLAUDE.md`, `CHANGELOG.md`, `.claude/settings.json`, `go.mod` after phase 1, or policy-weakening code are sent back. Titles stay within 64 characters. No AI attribution trailers. |
| A lane going in circles | Stop after 2 failed CI fix attempts, 2 rebase conflicts, or a diff past about 1,500 lines (split it). Flaky tests get their own issue. |
| `develop` breaking | Phase gates and `develop-pN` tags for rollback. If 2 or more lanes block on the same contract, the phase stops. |
| A breaking version bump | No `feat!` or `BREAKING CHANGE` without asking you. |

**The run stops for you when:** a compatibility contract in `AGENTS.md` would break; a dependency other than `fsnotify` is needed; a security finding can't be fixed in its lane; a plan decision turns out wrong; a step needs secrets, credentials or another repository; or phase 11 is done.

## Goal statement

Saved to `docs/plans/acp-platform/goal.md` and linked from the "Active program" section of `AGENTS.md`:

> **Goal:** Deliver the ACP platform program in `docs/plans/acp-platform/plan.md` to `develop`, phase by phase. Each phase starts with its ADR contract, runs its lanes in parallel (one issue, worktree and PR each), and ends at its gate: all lanes merged, `make check` green, the phase demo working, tag `develop-pN`, ledger updated. Continue until phase 11 is done, then stop and ask the owner to run `agentflow promote`.
>
> **Rules for every agent:** Follow `AGENTS.md`. Use the GoF and EIP patterns named in the ADR. Every MCP tool has a CLI command. Every UI update arrives over the SSE event stream. Every config option has a structured control. Never merge into or push to `main`, and never run `agentflow promote`. Tests never call real external services or harnesses.
>
> **Stop and wait for the owner when:** a compatibility contract would break; a dependency other than `fsnotify` is needed; a security finding can't be fixed in its lane; a plan decision turns out wrong; or a step needs secrets, credentials or another repository.

## Verification

- **Every PR:** `make check`, plus `make e2e` where the lane touches MCP, hooks, the adapter or `init`.
- **Every phase gate:** that phase's "You'll see" demo, run by the coordinator against `make build` output on `develop`.
- **Coverage tests by the end:**
  - `TestEveryMCPToolHasCLICommand` (exists)
  - `TestEveryWriteOperationEmitsEvents` (phase 3)
  - `TestEveryConfigFieldHasControl` (phase 4)
  - `TestEveryUIPanelIsEventDriven` (phase 11)
