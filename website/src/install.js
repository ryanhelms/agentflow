export function installerCommand(platform, tag) {
  if (!/^v\d+\.\d+\.\d+$/.test(tag)) throw new Error("a pinned release is required");
  const repo = "nscaledev/agentflow";
  const signer = repo + "/.github/workflows/release.yml";
  if (platform === "windows") {
    return [
      "$installer = Join-Path ([System.IO.Path]::GetTempPath()) ('agentflow-' + [guid]::NewGuid() + '.ps1')",
      `gh release download ${tag} --repo ${repo} --pattern install.ps1 --output $installer`,
      "if ($LASTEXITCODE -ne 0) { throw 'Download failed' }",
      `gh attestation verify $installer --repo ${repo} --signer-workflow ${signer}`,
      "if ($LASTEXITCODE -ne 0) { throw 'Verification failed' }",
      "& $installer -Repo (Get-Location).Path",
    ].join("\n");
  }
  return [
    'installer=$(mktemp) &&',
    `gh release download ${tag} --repo ${repo} --pattern install.sh --output "$installer" --clobber &&`,
    `gh attestation verify "$installer" --repo ${repo} --signer-workflow ${signer} &&`,
    'bash "$installer" --repo "$PWD"',
  ].join("\n");
}
