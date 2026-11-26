# Upgrade a Devflow checkout to Agentflow

Agentflow uses the same GitHub repository and release history. Its new executable, generated files, environment variables, MCP tool names, and plugin IDs use `agentflow`. GitHub redirects clone and web URLs. In Actions, update both the repository name and release commit for every affected `uses:`. [Repository rename behavior](https://docs.github.com/en/repositories/creating-and-managing-repositories/renaming-a-repository)

## Prepare a migration branch

1. Start with a clean checkout or commit your local work on its own branch. Record the known-good workflow pins and the installed version for rollback. Keep a private backup of personal settings and project files outside Git. Include any untracked task, knowledge, or installation state.
2. Move the tracked `.devflow` directory to `.agentflow` and the caller workflow from `.github/workflows/devflow.yml` to `.github/workflows/agentflow.yml`. Move any related untracked project state into the corresponding new paths. Preserve project-owned configuration, task catalogs, release settings, provider definitions, and install receipts. Update paths inside those files, including `templates.catalog` and receipt paths for pre-existing files. Review names in the receipt against the original paths before changing them.
3. Review `.githooks`, client MCP settings, host plugin registrations, and your automation for uses of `.devflow/bin/devflow`, the old CLI and MCP names, or the old GitHub slug. Keep unrelated client registrations and hook logic. For Actions inputs and values you control, copy configured `DEVFLOW_*` variables and secrets to the corresponding `AGENTFLOW_*` names through GitHub settings or their normal secret manager. Keep their existing scopes.
4. If you use `~/.devflow`, copy its project-independent settings and records into `~/.agentflow` with the same access permissions. If you set `DEVFLOW_HOME`, point `AGENTFLOW_HOME` at the migrated directory. Keep the previous binary cache with its original paths and versions during rollout. Agentflow reads the previous personal directory while the new directory is absent. Existing Atlassian credentials also remain available. Re-enroll OAuth providers through the Agentflow login commands when prompted. Re-prepare pending approvals after the upgrade.

## Install and repin

Use the first verified Agentflow release supplied by the cutover owner. Existing Devflow launchers look for Devflow archives and require the old release workflow identity. Bootstrap the new launcher from the Agentflow installer to switch the artifact and signing identities together.

```bash
tag=vX.Y.Z # use the verified cutover tag
gh release download "$tag" --repo ryanhelms/agentflow --pattern install.sh
gh attestation verify install.sh --repo ryanhelms/agentflow \
  --signer-workflow ryanhelms/agentflow/.github/workflows/release.yml
bash install.sh /absolute/path/to/checkout
```

On Windows, download and verify `install.ps1` with the same repository and signer, then run it using PowerShell. Run the download and verification from a temporary directory to keep the installer out of the project commit. The installer writes the new cache and launchers. `agentflow init` preserves moved project configurations and updates the managed `AGENTS.md` block in place. It stops before writing project files when it finds the old config and the new config is absent.

- Use `agentflow install` for the relevant AI clients. Replace only the old product's registrations and reload the clients. Ensure each hook invokes the new CLI and that the MCP server exposes the `agentflow_*` tools.
- Run `agentflow pin --check` to inspect the proposed pin, then `agentflow pin` to write it. An existing generated caller using the old filename can be repinned before it is renamed. The command updates only references to the first-party workflow repository. Review manually maintained workflow and action references outside the generated caller.
- Run `agentflow doctor` and the consuming repository's required checks. Verify one real workflow run on the new commit, a successful hook on a local test commit, and the effective provider settings. Include those results in the migration PR.

## Roll back

If the new release fails verification, stop and keep the known-good installation. To roll back a migrated checkout, restore its previous config, workflow and hook changes together and restore the old client registrations. The old Actions pins need a working legacy Actions host. During the rename window, use the temporary workflow endpoint if one was arranged or coordinate a GitHub-side rollback before restoring the old pins. Keep the old credentials and user-state copies until the new installation and its rollback procedure have both been verified.
