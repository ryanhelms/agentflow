---
type: Guide
title: Install and manage Agentflow in a project
description: Choose a Git checkout, review provider detection, and manage Agentflow through a terminal or agent.
tags: [installation, providers, agents, lifecycle]
x-agentflow-ref: {scope: engineering, id: project-installation}
---
# Install and manage Agentflow in a project

Sign in to the GitHub CLI with access to the internal `ryanhelms/agentflow` repository.
The installer reads from GitHub Releases and verifies the downloaded binary.
Run the command in a checkout or pass a path. A subdirectory selects its worktree
root. Outside a worktree, the installer asks for a path and retries until it
finds one or you cancel.

## macOS or Linux

```bash
s=$(gh release download -R ryanhelms/agentflow -p install.sh -O -) && bash -c "$s" agentflow
```

Append `--repo '/path/to/my project'` to select a different checkout.

## Windows PowerShell

```powershell
$s = gh release download -R ryanhelms/agentflow -p install.ps1 -O -; if ($LASTEXITCODE -eq 0) { & ([scriptblock]::Create($s -join "`n")) }
```

To target another checkout, add `-Repo 'C:\src\my project'` immediately before
the final `}`. In each shell the installer finishes downloading before execution,
and standard input is available for setup.

## Ask an agent

> Install Agentflow from https://github.com/ryanhelms/agentflow and initialize it in my payments checkout.

The agent resolves the target to a single local Git worktree. If the project name
is ambiguous, it asks for the intended path. The manager reads local provider
evidence before scaffolding, shows the selections for review and asks about
unknowns. It persists the confirmed choices and uses the same settings next time.
Ask for an update to refresh the verified binary and the project's workflow pin.
Ask to remove Agentflow to preview and remove its repo-owned files and registrations.

See the [client installation guide](../../../client-plugins.md) for each
host's installation route and the [manager instructions](../../../../adapters/claude-code/skills/manager/SKILL.md)
for the commands an agent executes.
