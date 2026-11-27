# ACP platform program: goal

Adopted by the owner on 2026-11-27. It replaces the first goal, which ended at
`agentflow promote`. "I" and "me" mean the owner. The coordinator session runs
this goal. Other agents follow its rules but never merge, tag, release, publish
or change repository settings.

```text
Goal: complete all remaining agentflow work, then make the repository public.

First: record this goal in docs/plans/acp-platform/goal.md and the "Active
program" section of AGENTS.md, note in plan.md that phase 11 no longer stops
for promote, and update memory.

Phases 7 to 11 (docs/plans/acp-platform/plan.md), one at a time:
- Contract first, then lanes in parallel (at most 4), then one integration PR.
- Design review for UI. Security review where code runs programs, writes
  files or handles untrusted text.
- Gate: make check; make e2e if MCP, hooks or the adapter changed; the demo.
  Say which demo steps ran live and which only in tests.
- Push tag develop-pN, confirm the release, update the ledger and memory.

Other work:
- #129 launcher gap: may run beside a phase. Release it before publishing.
- #111, #117, #119 and review leftovers: fix in phase 11.
- #130 clean-up and MIT license: in phase 11, once no lane owns the static
  files or docs.

Publish, last:
- Finish and release #129 and #130.
- Scan git history, comments, review comments, Actions logs, release notes
  and assets for secrets and private data.
- Require approval for fork pull request workflows, turn on private
  vulnerability reporting, update the README status row.
- List the open security issues that will become visible.
- When all of this passes, make ryanhelms/agentflow public.

Rules:
- Follow AGENTS.md. Merge green, reviewed PRs into develop. Never merge or
  push to main and never run agentflow promote; CI promotes develop-pN tags.
- Tests and demos use only the fake agent, never a real harness or external
  service.
- Opus for architecture, concurrency and security code; Sonnet for mechanical
  work; Fable for all UI design.
- Take your recommended option for product and design choices and record it
  in the ledger. Ask me about any choice that changes what agents may do,
  what leaves my machine, or what gets deleted.
- Report each phase gate in a few lines.

Ask me in one round, before the clean-up: renaming the agentflow@nscale
plugin marketplace, changing the default init.orgs, deleting old remote
branches, downloading a secret scanner.

Stop and wait for me when: a compatibility contract would break or a change
needs "!"; a dependency other than fsnotify is needed; a security finding
can't be fixed; a plan decision turns out wrong; a step needs secrets,
credentials or another repository; or the scan finds a secret or private data
I have not already accepted (history cannot be rewritten).

Done when: phases 7 to 11 are released, #129 and #130 are closed, the other
open issues are closed or deferred in the ledger, and the repository is
public.
```

## Rules from the first goal that still apply

Follow `AGENTS.md`. Use the GoF and EIP patterns named in the ADR. Every MCP tool has a CLI command. Every UI update arrives over the SSE event stream. Every config option has a structured control. Never merge into or push to `main`, and never run `agentflow promote`. Tests never call real external services or harnesses.
