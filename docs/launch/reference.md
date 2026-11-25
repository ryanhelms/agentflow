# CLI and MCP reference

The CLI and MCP server call the same application operations. Some operations require a provider session, or a client capable of performing a delegated read or write. Start in the repository you intend to inspect.

| Task | CLI entry point | MCP entry point |
| --- | --- | --- |
| Read selected providers and configuration layers | `agentflow setup providers --explain` | `agentflow_setup_providers` |
| Find work and load task context | `agentflow work`, `agentflow context` | `agentflow_my_work`, `agentflow_context` |
| Bind a task | `agentflow start KEY` | `agentflow_start` |
| Inspect criteria and scope | `agentflow ac list`, `agentflow scope` | `agentflow_ac_checklist`, `agentflow_scope_check` |
| Inspect the repository's approval rule | `agentflow policy explain ACTION` | `agentflow_policy_explain` |
| Prepare a commit or pull request | `agentflow commit --prepare`, `agentflow pr --prepare` | `agentflow_commit_prepare`, `agentflow_pr_prepare` |
| Run pull-request readiness checks | `agentflow pr --check` | `agentflow_pr_ready` |
| Search, read, or publish knowledge | `agentflow docs search`, `get`, `publish` | `agentflow_docs_search`, `agentflow_docs_get`, `agentflow_docs_publish` |
| Inspect and change repository settings | `agentflow config` | `agentflow_config`, `agentflow_config_apply` |
| Open the local console | `agentflow ui` | `agentflow_ui_open` |

For the exact CLI arguments, run `agentflow help` or read the [usage source](../../internal/cli/cli.go). The [parity test](../../internal/cli/parity_test.go) lists the complete tool-to-command map and verifies coverage. The [MCP server source](../../internal/mcpserver/server.go) and companion registrations define tool inputs. Use `--json` on supported commands when automating reads.

For a write, keep the proposed target, revision, and effect in view. Apply the candidate through its normal approval path. If an approval expires or a material input changes, prepare it again. See [Operations](../operations.md) and the [repository console guide](../repository-console.md).
