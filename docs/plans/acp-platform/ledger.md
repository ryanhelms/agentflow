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
| 5 | #82 (contract, issue #77) | `develop-p5` at `013e2f4` | on an isolated build both sample plugins installed; a failed commit produced a `timeline-hints` annotation; `ci-notify` approve-then-run-once is covered by `TestApprovedPluginWriteRunsOnceThroughTheDaemon` | done; promoted to `main` by auto-promote (`f0928d4`), released as v0.59.0 (`e282e6d`) on 2026-11-26 |
| 6 | #118 (contract #104, issue #97) | `develop-p6` at `b4389ac` | `ai harnesses --probe` and `ai doctor` show every state with its fix against the fake ACP agent and fake CLI, including a sign-in check (`signed_out`, and `unknown` with a credential variable set); an agent's probe is refused while `auto_probe` is off; the Harnesses page follows a harness being removed and added again | done: lanes merged through #118 (with #108; contract #104; home fix #106) and the security re-review fixes #126 (issue #120); tagged `develop-p6` at `b4389accd8d85dae92a06079a265d223c20ee3b5`, released as v0.60.0. Gate: `make check` green, `make e2e` 26/26, promotion dry run passed, demo steps 1, 2 and 4 and the env-credential case run live with the fake agent; step 3 (live remove and restore) not run live because the console daemon registry ignores `AGENTFLOW_HOME`, covered by `TestHarnessProductionWiring`. Open: #111, #117, #119 |
| 7 | #138 (contract #127, issue #121) | `develop-p7` at `951c8c5` | with the fake ACP agent only: `ai run` streams a session in a task worktree; reads outside the worktree are denied; control-file writes and terminals ask; an agent cannot allow; the running cap and cancel hold; the AI panel follows a session (Go tests, since a live console would reach the owner's daemon) | PR #138: lanes 7.1 to 7.4 (#135, #137, #136, #134) with the integration work and #117. Gate on 2026-11-27: security reviews and re-check done; `make e2e` 38/38 at `d8356d7` (after fixing two wrong checks in the script), not rerun for the later commits, which change only the terminal side of `ai run` and `ai status`; the demo ran from an agent session with the fake agent only: steps 0 to 6 and 8 live, plus `ai status --follow`; step 7 passed as Go tests only. Not run: the person-only steps (a foreground `ai run` in a real terminal, Allow once in a live console), any real harness (so also the isolated-home sign-in check), and Windows (the session tests skip there). `make check` passed on the tagged tree. Done: promoted to `main`, released as v0.61.0 on 2026-11-27; a release asset's build attestation verified and a clean first install succeeded (`agentflow update` from an older build was not tried) |

Lanes:

| Lane | Work | Model | Issue | PR | Status |
| --- | --- | --- | --- | --- | --- |
| 0.1 | Create `develop`. PR into `main` (you merge it): `policy.coordinator_merge: true`, CI on `push` to `develop`, the `AGENTS.md` "Active program" section, `docs/plans/acp-platform/{goal,plan,ledger}.md`, and a README/`docs/worktrees.md` note | O | #12 | #15 | done (merged into `main`; issue #12 still open) |
| 0.2 | Golden tests that pin today's `Pending` JSON and CLI `--json` output for about 10 operations | S | #13 | #22 | done (merged into `main`; issue #13 still open) |
| 1.0 | Phase 1 contract ADR | O | #14 | #16 | done |
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
| 5.0 | Phase 5 contract ADR: manifest, tiers, extension points, JSON-RPC methods, lifecycle, events, plugins projection, settings, commands, security, ownership | O | #77 | #82 | done |
| 5.1 | Shared stdio JSON-RPC framing (from `codexhost/stdio.go`, which stays as it is) and scrubbed-environment launcher | O | #78 | #83 | done |
| 5.2 | Plugin host: three tiers, registry (Abstract Factory), lifecycle, crash isolation into Dead Letter | O | #79 | #90 (with #87, #88, #89) | done |
| 5.3 | Plugin commands, the two sample plugins, docs (`docs/plugins.md`) | S | #80 | #85, #86 | done |
| 5.4 | Plugins page: installed plugins, status, what each adds, live health | F | #81 | #84 | done |
| 5.5 | `plugin approve` and the host's resume of an approved plugin write; `requested_by` on approvals | O | #91 | #92, #94, #96 | done |
| 6.0 | Phase 6 contract ADR: `Harness`, definitions and layers, pinned registry copy, `initialize` slice, launch profile, sign-in check, states and fixes, config keys, events, harnesses projection, commands, security, ownership | O | #97 | #104 | done |
| 6.1 | Harness registry from the pinned ACP registry copy, plus YAML overrides; PATH and version probes; no installs | S | #100 | #108, #118 | done |
| 6.2 | `initialize` probe with timeout and cache; `harness.*` events from slow polling and focus checks | O | #101 | #118 | done |
| 6.3 | `ai harnesses` and `ai doctor` commands, skills, README | S | #102 | #118 | done |
| 6.4 | Harnesses page: states, fixes, sign-in guidance | F | #103 | #118 | done |
| 7.0 | Phase 7 contract ADR: ACP session types, client and fake agent, session lifecycle and host, session environment, permission chain, file and terminal access, who may start and answer, repository influence, limits, store and retention, events, AI projection, config keys, session MCP server, commands, AI panel, test guard, demo and gate, ownership, security, owner questions | O | #121 | #127; amended in #138 | done |
| 7.1 | ACP v1 client and fake ACP agent for tests | O | #122 | #128 (contract), #135 (in #138) | done |
| 7.2 | Background sessions (Process Manager), session store, permission chain, worktree-only file and terminal access | O | #123 | #132 (contract), #137 (in #138) | done |
| 7.3 | `ai run/status/respond/cancel` commands; agentflow's own MCP server passed to the harness, with a guard against starting sessions without limit | S | #124 | #136 (in #138) | done |
| 7.4 | AI panel: streaming output, tool calls, plan view, permission prompts | F | #125 | #134 (in #138) | done |
| 8.0 | Phase 8 contract ADR: feature framework (Template Method), prompt context and filtering, draft session profile, answer parsing, candidates, who may request, `ai.draft` command and tool, `--ai`, events, AI projection and console routes, draft review UI data and states, config keys, fake agent modes, test guard, demo and gate, ownership, security, owner questions, plan corrections | O | #151 | | in progress |
| 8.1 | Feature framework (`internal/ai`), routing settings (`ai.features.<id>.harness`), prompt context as data; draft profile in `internal/aisession`; `ai draft` and `agentflow_ai_draft`; `ai.draft.finished`; drafts in the `ai` projection and console routes; fake agent draft modes | O | | | pending |
| 8.2 | Commit and PR drafting; acceptance-criteria check; `--ai` on `commit`, `pr` and `ac` (after 8.1) | S | | | pending |
| 8.3 | Ticket from text; review-thread replies (feature `reply`); `--ai` on `task create` and `review reply` (after 8.1) | S | | | pending |
| 8.4 | "Draft with AI" request and draft review view on the AI page (no apply in phase 8) | F | | | pending |
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

Phase 11 hardening backlog (found in phase 5, left for 11.1):

- The repository boundary's `stripWrappers` misses wrappers whose options take a value.
- A plugin within its publish rate limit can still push other events out of the timeline window.
- Declarative template values that are whole objects are not capped.
- `Reload` keeps the plugins' previous state when the personal `config.yml` cannot be read.
- A panic mid-operation leaves `op.requested` without an outcome event.
- A plugin write held for approval lives in the daemon's memory: an approval granted after a daemon restart runs nothing.
- The commit-message check may reject a valid heredoc `-m` message (unverified).
- Phase 7 (deferred from the #138 security review): a process a session terminal starts that leaves its process group (`setsid`, a daemon) survives the session's end; agentflow kills only the terminal's group (ADR phase 7 integration amendment 35).
- Phase 7 (deferred on 2026-11-27 from the independent re-check of #138):
  - On macOS and Windows every file name with a non-ASCII character asks a person (`unicode_name`), however ordinary (`docs/café.md` under an allow rule). Find a narrower rule.
  - The account layer leaves these settings to the session home: `keep_transcripts`, `keep_days`, `sessions.modes`, `harnesses.overrides` and `approval_window`.
  - A terminal's process group whose leader has exited is not killed when a lost session is ended, so a background child of that terminal survives.
  - The repository boundary does not treat `agentflow ai cancel` or `ai respond` run from a changed directory (`cd other-repo && ...`) as a write to another repository.
  - `DISPLAY` and the session bus (`DBUS_SESSION_BUS_ADDRESS`) can still steer which helper opens the console on Linux (ADR amendment 38).
  - A file name that contains the literal text `<U+202E>` looks the same as the hidden character shown that way.
  - Whole-line terminal rules (matching arguments, not just the program) may be considered (owner decision on amendment 31).
- Phase 7 (found at the gate on 2026-11-27): agent text can imitate transcript lines in a terminal, and a foreground prompt cannot discard a line the person had only partly typed.

Outside the backlog:

- A personal home that a repository's environment points inside the repository (`AGENTFLOW_HOME` set by direnv), issue #105, is fixed by #106.
- Phase 6 security re-review findings (versioned interpreters in plugin harnesses; the version command next to Probe), issue #120, are fixed by #126, merged before `develop-p6`.
- The owner answered the phase 7 open questions on #127; the ADR's "Phase 7 owner decisions" records them (6, 8, 11 and 12 taken as recommended, not owner-confirmed).
- Phase 7 decisions taken by the coordinator: `agentflow_commit_context` left out of the session MCP allowlist (it stages paths; #136); lane 7.4 may change one line of `internal/webui/testdata/live-overview-test.js` (the `ai.session.**` topic; #134); the ADR is amended to what the lanes built (dated amendments in "Phase 7 integration amendments", #138).
- Phase 7 owner decision on 2026-11-27: only a person at a terminal gets the console's one-time sign-in link; `agentflow ui` never prints it for an agent or an AI session (#117, fixed in #138). `ui --serve-child` reports readiness only and never a link; the starter gets the link over the encrypted control request (the earlier parent-process check could be faked and is removed). The never-list blocks agents from running `agentflow ui --no-open`, `ui --serve-child` and `ai host` directly: a cheap layer that indirection gets around.
- Phase 7 owner decision on 2026-11-27 ("Accept and document"): "only a person may allow" and "only a person gets the sign-in link" stop ordinary agent behaviour, not a program running as the person that deliberately fakes a terminal and removes the variables that mark it as an agent. Such a program can get the link, allow its own asks and start sessions as the person, and the record says a person did it. The person rule is unchanged (ADR phase 7 integration amendment 41). Accepted with the other same-user limits: a same-user process can read agentflow's private files (the console registry and control socket, `answer.key`, the session store) and so forge a person's allow or obtain a ticket; the opener's URL is briefly visible in the process list while the browser opens; the opener's desktop variables (`DISPLAY`, `WAYLAND_DISPLAY`, `DBUS_SESSION_BUS_ADDRESS`, `XDG_CURRENT_DESKTOP`) pass; `AGENTFLOW_ACP_SESSION` can be unset by the harness's own shell, so the store caps are what bound a session that starts sessions.
- Phase 7 coordinator decisions in the #138 security review (2026-11-27): a person at the console may always cancel a session, even with `autonomy.actions.ai.cancel: suggest` (stopping is a safety action); `ai status`, `ai respond` and `ai cancel` (CLI, MCP and console routes) see only the caller's repository's sessions; `ai.respond` is not an autonomy action (nothing gates it); a terminal request carrying agent `env` always asks and shows each value; the ADR's "rules match the full line" contradicts its own configuration-key text and demo, so rules still match the program (open question for the owner, ADR amendment 31).
- Phase 7 owner decisions on 2026-11-27: terminal rules match the program name only, and the docs say plainly that allowing a program allows everything it can do; whole-line rules may be considered in phase 11 (ADR amendment 31, resolved). The pseudo-terminal limit stays "Accept and document" (amendment 41). The plugin marketplace rename is not marked breaking.
- The repository was made public on 2026-11-27, after a secret scan and before the clean-up in #130, with approval required for workflow runs from outside contributors, private vulnerability reporting, secret scanning and push protection turned on. The README status row says so; the rest of the clean-up is another session's.
- Phase 7 fixes from the independent re-check of #138 (2026-11-27): the caps count every agentflow home (stubs in the account store); no allow rule applies under an account layer; the account's agentflow home is on the session never-list; reads and writes are pinned to the file and folder the chain classified; the console's serve-child no longer marks a tab as awaited, so auto-open gets its link again (ADR amendments 28, 36, 37 and 39).
- The test cleanup race in `internal/plugin` that failed the first v0.59.0 release run is fixed by #99.
- Issue #129 (consumers keep the old launchers): `agentflow pin` also replaces the four launcher and hook files, but only when a file matches the SHA-256 of a stock file shipped from v0.53.0 on or the current template. Changed files, links and linked folders warn; devflow-era hooks are left out so an unmigrated repository is not switched to agentflow. The Claude Code plugin launcher no longer runs a repository's committed `.agentflow/bin/agentflow`.
- Phase 7 gate findings, fixed in #138 before the merge: `ai run` and `ai status` printed only the kind of most transcript items (such as `[ask]`), and the session list did not name a waiting ask's ID; the foreground prompt now takes an answer only from input typed after it (ADR amendment 44).
- Phase 7 demo differences from the ADR are recorded as amendment 45. The demo and `make e2e` also read the account's real agentflow home through the account layer, and each demo session left a stub record there, by design; every stub was ended and stubs are pruned after an hour.
- The first `develop-p7` attempt would have failed: a README Status row edit sits next to the version line each release rewrites on `main`. `main` was synced into `develop` with a merge commit (#147), and is synced again after v0.61.0.
- Releases up to v0.60.0 were built while the repository was private and have no build attestation, so a first install of them needs `AGENTFLOW_INSECURE_SKIP_ATTESTATION=1`; v0.61.0 on are attested.
- Phase 8 owner decisions on 2026-11-27 (all as recommended): drafting sends context only for a feature the person gave a harness (off by default), with a list of what is sent and secret files left out; agents may request drafts only while agent-started sessions are on, and always need confirmation; the console may start drafts and applies nothing; a draft's harness starts in an empty folder and drafts are allowed in the primary checkout.
