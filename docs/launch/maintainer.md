# Architecture and maintenance

Agentflow links repositories to configured providers through a local CLI, a Model Context Protocol (MCP) server, and GitHub workflows. The CLI and MCP server parse inputs and call the shared application layer. Provider adapters translate task and knowledge operations to their backing systems. The approval layer applies repository and user policy before operations with effects.

| Area | Where to start |
| --- | --- |
| Command flow | [CLI](../../internal/cli/cli.go), [MCP server](../../internal/mcpserver/server.go), and [application code](../../internal/app) |
| Provider contracts | [Provider interfaces](../../internal/provider/provider.go), [definition design](../design/provider-definitions.md), and [provider authoring](../provider-authoring.md) |
| Repository setup | [Setup](../../internal/setup), [embedded templates](../../templates), and [client adapters](../../adapters) |
| Policy and configuration | [Policy](../../internal/policy), [configuration](../../internal/config), and [console behavior](../repository-console.md) |
| Local web UI | [Repository console](../../internal/webui) and the separate [launch site](../../website) |

Run `make check` before opening a PR. Use the [contributor guide](../../CONTRIBUTING.md) for local development, tests, and website maintenance. Changes to the CLI or MCP surface also need parity, client skill, and README updates.

Agentflow's own release workflow versions the binary after changes land on `main`, then publishes verified installers and release artifacts. A consuming repository configures its deployable units and release workflow independently. The [release guide](../releases.md) describes the consumer workflow. The [upgrade guide](../agentflow-migration.md) covers existing installations and rollback.
