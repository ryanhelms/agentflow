---
name: login
description: Connect agentflow to Atlassian (Jira and Confluence)
disable-model-invocation: true
---
The `agentflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

Never ask for, read, echo or handle the Atlassian API token yourself.

1. Run `agentflow version` so the binary is installed.
2. Tell me to create a token at https://id.atlassian.com/manage-profile/security/api-tokens
   if I don't have one.
3. Tell me to run this in my own terminal, from this repo (it prompts for the token
   without echoing it and stores it in the OS keychain; it needs a real terminal, so the
   `!` prompt prefix won't work):

   `.agentflow/bin/agentflow login` (or `~/.cache/agentflow/current/agentflow login` if the repo
   has no `.agentflow/` yet)

4. When I say it's done, run `agentflow doctor` and report the Atlassian line.
