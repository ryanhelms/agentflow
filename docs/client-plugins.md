# Install the Devflow client plugin

Open the **target Git project** in your client. Install the plugin from the internal [Devflow repository](https://github.com/nscaledev/devflow) using a Git identity with read access. For a first binary download, the host also needs a signed-in GitHub CLI with `gh attestation verify` available. The Codex, Cursor, VS Code and Gemini plugin servers run with Node.js on the host’s PATH. The existing Claude Code plugin uses its shell launcher.

| Client | Add from the repository | Verify |
| --- | --- | --- |
| Claude Code | Run `claude plugin marketplace add nscaledev/devflow`, then `claude plugin install devflow@nscale`. In an interactive session, the equivalent commands start with `/plugin`. | Open a new project session. Use `/devflow:manager` and inspect the Devflow server in `/mcp`. |
| Claude Desktop, local Code or Cowork | Open **Customize → Plugins → Add → Add marketplace → Add from a repository**, enter `nscaledev/devflow`, then add Devflow and open the project. Organization policy and GitHub access must permit the source. | Check the available skills and MCP tools in the selected local surface. For Claude Code Desktop’s dedicated client adapter and provider setup, see the [provider guide](providers.md). |
| Codex CLI or desktop | Run `codex plugin marketplace add nscaledev/devflow`, then `codex plugin add devflow@nscale`. Restart the project session. The Codex IDE extension uses the project MCP adapter. | Check `codex plugin list --marketplace nscale`, find the manager skill, and check `/mcp` for Devflow in the project session. |
| Gemini CLI | Run `gemini extensions install https://github.com/nscaledev/devflow` and review its trust and installation prompts. An authenticated local checkout path is also supported. Restart the session. | `gemini extensions list` shows the manager skill and the Devflow server. Inspect `/mcp` in the project. |
| Cursor Teams or Enterprise | An authorized administrator can import the repository under **Dashboard → Plugins & MCPs → Team Marketplaces → Add Marketplace**. Users can then install Devflow in **Customize**. Where local imports are permitted, use an authenticated checkout and **Import from Disk**. | Open the target workspace and inspect Devflow under **Customize** and MCP tools. |
| VS Code with the Copilot harness | In **Chat: Open Customizations**, select the Copilot harness and install from the Devflow Git repository. Choose the repo root for the Copilot plugin. Approve the host prompt. | Open the project, select that same harness, and inspect **MCP Servers** and **Skills**. |

Ask **“Use the Devflow manager to install Devflow in this project.”** The same skill handles updates and removal. Installing it puts the manager and Devflow’s MCP tools in the client. The manager operates on the project you select and reports the result. Use the host's plugin manager to remove the client plugin itself.

The Devflow server reads its repository from its working directory. Open a trusted local Git checkout before asking it for task context; this can be a project that has never used Devflow. Check which provider and client configuration is active with `devflow_setup_providers` and `devflow_setup_plan`. Existing per-project adapters can already register a server called `devflow`. If a host shows two copies, choose the registration for the active project. Gemini gives its project settings precedence over extension registrations of the same name.

These manifests provide local stdio services. Claude chat does not run a plugin’s local stdio server. Grok and other configured MCP-only clients can use their existing project adapters as described in the [README](../README.md). The binary release supports macOS and Linux on amd64/arm64, and Windows amd64. Private repository access, host policies, runtimes and permissions also apply on remote development machines.

### Locate the installed CLI before project setup

The manager is available before the project has a `.devflow` directory. In a Codex, Cursor, Gemini, or VS Code local session, the skill details identify the installed `skills/manager/SKILL.md` file. The directory containing `skills/` is the plugin root. Set the working directory to the target Git checkout. With that actual root substituted, check the binary on macOS or Linux with `sh "<plugin-root>/templates/repo/.devflow/bin/devflow" version`, or on Windows amd64 with PowerShell `& "<plugin-root>\templates\repo\.devflow\bin\devflow.ps1" version`. Append the manager’s requested CLI arguments to this same launcher. The Claude Code plugin exposes its own `bin/devflow` on the session shell’s PATH; its POSIX launcher also works at `<claude-plugin-root>/bin/devflow`. When the host cannot run local commands, use the authenticated [release installers](https://github.com/nscaledev/devflow/releases/latest) from a terminal with access to the project.

### How the plugin finds Devflow

The packaged launchers use the shared verified binary cache and download a release only when required. A new download checks the archive checksum and GitHub build attestation before use. Native adapters preserve the project working directory and pass the host’s client identifier. The Codex compatibility descriptor includes the launchers as command arguments so it can run in the project even when Codex stores the plugin elsewhere. After editing either launcher or the Node wrapper, regenerate it with `node scripts/build-codex-plugin.mjs`. `node scripts/build-codex-plugin.mjs --check` checks for drift.

Each Devflow release stamps the host plugin manifests with its version. A client
can still have an older plugin loaded after the manager updates the binary.
To pin the plugin, choose a Git ref containing its manifest. Codex supports
`codex plugin marketplace add nscaledev/devflow --ref <ref>`. Gemini supports
`gemini extensions install https://github.com/nscaledev/devflow --ref <ref>`.

### Host references

- [Claude Code plugin installation](https://code.claude.com/docs/en/discover-plugins) and [Claude Desktop plugin availability](https://support.claude.com/en/articles/13837440-use-plugins-in-claude).
- [Codex plugin availability](https://learn.chatgpt.com/docs/plugins) and [OpenAI plugin packaging and marketplaces](https://developers.openai.com/plugins/build/plugins).
- [Gemini extension installation, discovery and precedence](https://geminicli.com/docs/extensions/reference/).
- [Cursor team marketplaces and local imports](https://cursor.com/docs/plugins) and [Cursor plugin manifests](https://cursor.com/docs/reference/plugins).
- [VS Code agent plugin installation](https://code.visualstudio.com/docs/agent-customization/agent-plugins) and [Copilot plugin configuration](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-plugin-reference).
