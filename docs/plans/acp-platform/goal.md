# ACP platform program: goal

Saved to `docs/plans/acp-platform/goal.md` and linked from the "Active program" section of `AGENTS.md`:

> **Goal:** Deliver the ACP platform program in `docs/plans/acp-platform/plan.md` to `develop`, phase by phase. Each phase starts with its ADR contract, runs its lanes in parallel (one issue, worktree and PR each), and ends at its gate: all lanes merged, `make check` green, the phase demo working, tag `develop-pN`, ledger updated. Continue until phase 11 is done, then stop and ask the owner to run `agentflow promote`.
>
> **Rules for every agent:** Follow `AGENTS.md`. Use the GoF and EIP patterns named in the ADR. Every MCP tool has a CLI command. Every UI update arrives over the SSE event stream. Every config option has a structured control. Never merge into or push to `main`, and never run `agentflow promote`. Tests never call real external services or harnesses.
>
> **Stop and wait for the owner when:** a compatibility contract would break; a dependency other than `fsnotify` is needed; a security finding can't be fixed in its lane; a plan decision turns out wrong; or a step needs secrets, credentials or another repository.
