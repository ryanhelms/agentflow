# Client and provider compatibility

Check capability per provider, operation, and execution environment. A working MCP connection can expose Agentflow's operations while the underlying provider still needs sign-in or team authorization.

| Environment | What to verify |
| --- | --- |
| CLI on macOS, Linux, or Windows | Install the signed binary, run `agentflow doctor`, and verify the provider transport in the target checkout. |
| Claude Code Desktop | Verify the selected native Atlassian or Asana operation and the account behind it. Review a separate capability for reads, writes, and receipt verification. |
| Codex | Verify the project MCP registration and each configured provider path. Use the CLI or normal GitHub review flow for steps the selected connector delegates. |
| Claude Code, Cursor, Gemini, VS Code, or Grok | Use the host's registration guidance. Verify MCP initialization, context reads, and the provider path you need. |
| CI runner | Verify caller pin, secrets, environment, repository permissions, and the task or knowledge role required by the workflow. |

| Role | Repository choices to inspect |
| --- | --- |
| Tasks | Jira, GitHub Issues, Asana, filesystem, repository, or disabled. |
| Knowledge | Confluence, GitHub Wiki, filesystem, repository, or disabled. |
| Code hosting and delivery | GitHub selection and target remote, reusable workflows, configured release units, and deployment receipts. |
| Notifications and triage | Explicit provider and destination settings, preview, permissions, and delivery or replay receipts. |

Use `agentflow setup providers --explain` for the effective role, selected provider, and setting source. Use `agentflow setup verify` and [Provider configuration](../providers.md) for operation-specific limits and authentication. The [client plugin guide](../client-plugins.md) covers enrollment for each host.
