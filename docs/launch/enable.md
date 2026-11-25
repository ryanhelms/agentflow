# Enable a repository

Start in an isolated sandbox checkout on a branch you can turn into a pull request. You need a verified Agentflow installation, access to the target repository, and permission to use the providers you choose.

1. Run `agentflow init --detect-providers` to inspect local evidence. Review the suggested task, knowledge, and delivery providers with the repository owner.
2. Run `agentflow init --guided`. Task and knowledge providers are opt-in. Select only the roles your repository uses.
3. Review the generated changes with `git status --short` and `git diff`. Check the managed `AGENTS.md` block, hooks, client setup, caller workflow, release settings, and recorded provider choices that apply to this checkout.
4. Run `agentflow setup providers --explain`, `agentflow setup plan`, and `agentflow setup verify`. Resolve missing access in the terminal or client that will run the operation. Keep credentials out of the repository.
5. Run `agentflow pin --check` and `agentflow pr --check`. Have the repository owner review the caller pin, enabled effects, and required checks. Run the workflow in the sandbox and record its outcome before enabling those effects elsewhere.

Open the [repository console](../repository-console.md) to inspect configuration and approvals. See the [consumer canary](../consumer-canary.md) for rehearsal and rollback. Start with the [Jira and Confluence walkthrough](jira-confluence.md) or the [GitHub Issues workflow](../knowledge/engineering/guides/task-workflow.md).
