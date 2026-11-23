---
name: manager
description: Install Devflow in a project, inspect its setup, update an installation, or remove it.
---

# Manage Devflow

Find the absolute path of this installed `SKILL.md` in the host's skill details. Its directory is `<plugin-root>/skills/manager`. Use that installed root even if the selected project has no Devflow files. Keep the command's working directory at the target Git checkout.

Read [the shared manager instructions](../../adapters/claude-code/skills/manager/SKILL.md) from this bundle. For every `devflow` command in those instructions, invoke the packaged launcher with the same arguments. For example, to check the binary before enrollment:

- On macOS or Linux, run `sh "<plugin-root>/templates/repo/.devflow/bin/devflow" version`.
- On Windows amd64, from PowerShell run `& "<plugin-root>\templates\repo\.devflow\bin\devflow.ps1" version`.

Replace `<plugin-root>` with the actual installation path. These commands work without a global Devflow binary. If the local host offers no installed file access, use the release installer from the authenticated Devflow GitHub repository as described in the shared instructions.
