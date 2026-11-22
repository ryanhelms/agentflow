# Keep the CLI and MCP at parity

The same rule is in AGENTS.md ("Keep the CLI and MCP at parity"); AGENTS.md is the source
of truth.

The devflow CLI is the engine. Anything devflow can do through an MCP tool must also be
doable from the CLI, and both must behave the same.

- Every `devflow_*` MCP tool has a CLI command. Both call the same `internal/app` method.
- `internal/cli` and `internal/mcpserver` are thin layers: they parse input and format
  output. Business and provider-specific logic belongs in `internal/app` or a focused
  package.
- CLI commands that write locally or to a provider use the same confirmation flow as the
  tool (`confirmLoop`: needs_confirmation summary, prompt, `--yes`), and commands that
  print data offer `--json`.

When you add or change an MCP tool, in the same PR:

1. Add or change its CLI command in `internal/cli`.
2. Add or update its row in `mcpToCLI` in `internal/cli/parity_test.go`.
3. Update the usage text in `internal/cli/cli.go`.
4. Add or update its plugin skill in `adapters/claude-code/skills/<command>/SKILL.md`.
5. Update README.md (see `readme-up-to-date.md`).

`TestEveryMCPToolHasCLICommand` fails when a registered MCP tool has no CLI command, so
`make check` catches a missing one.
