# Rehearse Jira to release and Confluence

Use an approved sandbox GitHub repository, Jira project, and Confluence space. The identifiers `LAUNCH-42` and `confluence/LAUNCH/42` below are examples. Use synthetic issue titles and page text. Keep the terminal, agent client, and CI runner identities clear during the rehearsal.

1. Set `task_management.provider: jira` and `knowledge_base.provider: confluence` in the sandbox configuration. Run `agentflow setup providers --explain` and sign in through the configured provider path. Use the [provider setup guide](../providers.md) to verify readiness for the client and runner.
2. Read the task with `agentflow context LAUNCH-42`. Start it with `agentflow start LAUNCH-42` and verify the branch and any configured Jira effect. Review the acceptance criteria before writing a commit.
3. Check the pull request with `agentflow pr --check`. Prepare a draft with `agentflow pr --draft --prepare`. Review the proposed title, target, and publication effects. Apply the reviewed operation through its original client and approval flow.
4. Have an authorized engineer merge the sandbox PR. After the configured release workflow publishes, inspect the release with `agentflow release get TAG`. Compare its revision and artifacts with the expected merged work. Confirm provider transitions separately in Jira when they are enabled.
5. Read the Confluence page and record its current revision with `agentflow docs get confluence/LAUNCH/42 --json`. Prepare the new text using `agentflow docs publish` with the page reference, title, body, expected revision, and a unique operation ID. Review the destination and apply the approved update. Reopen the page to verify the result. If the page changed before approval, reread its revision and prepare a fresh preview.

Repeat the read, preview, and approval portions in Claude Code Desktop and Codex. Record the provider transport that handled each step and use the CLI for any handoff that the agent client delegates. Capture only sandbox data. Show Nscale-branded sample URLs in launch assets, and keep the actual service endpoints in the internal rehearsal records. Omit sign-in URLs, credentials, approval tokens, local account paths, and private browser tabs.

For a different task or knowledge stack, use the [compatibility guide](compatibility.md) and the [GitHub Issues workflow](../knowledge/engineering/guides/task-workflow.md). Keep release verification separate from the Confluence update so the demonstration works with an explicit approval.
