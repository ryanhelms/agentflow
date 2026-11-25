# Set up a workstation

Use the installer for your operating system from the verified [Agentflow release page](https://github.com/nscaledev/agentflow/releases). Sign in to GitHub CLI with an Nscale identity that can read the repository. Open a terminal in the intended checkout. The launch site provides a pinned download and verification command when the signed release is available.

After installation:

1. Run `agentflow doctor` and review what the checkout already configures.
2. Run `agentflow setup plan --client codex` or select the client you use. Apply the reviewed project registration with `agentflow setup apply --client codex` for Codex. See [client plugins](../client-plugins.md) for host-specific names and setup.
3. Reopen the coding client in the checkout. Confirm that it can read Agentflow context and run `agentflow doctor --providers` to see which provider connections need sign-in.
4. Use the setup flow for the missing provider connection. The terminal, desktop agent, and CI runner may each have their own identity and permissions.

If Agentflow detects the old project configuration, follow [Upgrade from Devflow](../agentflow-migration.md). For a new repository, coordinate with the owner and follow [Enable a repository](enable.md).
