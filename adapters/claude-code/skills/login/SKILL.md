---
name: login
description: Connect devflow to Atlassian (Jira and Confluence)
disable-model-invocation: true
---
The `devflow` command is on your Bash PATH (the plugin's `bin/`); the first run installs the latest verified release.

Never ask for, read, echo or handle the Atlassian API token yourself.

1. Run `devflow version` so the binary is installed.
2. Tell me to create a token at https://id.atlassian.com/manage-profile/security/api-tokens
   if I don't have one.
3. Tell me to run this in my own terminal, from this repo (it prompts for the token
   without echoing it and stores it in the OS keychain; it needs a real terminal, so the
   `!` prompt prefix won't work):

   `.devflow/bin/devflow login` (or `~/.cache/devflow/current/devflow login` if the repo
   has no `.devflow/` yet)

4. When I say it's done, run `devflow doctor` and report the Atlassian line.
