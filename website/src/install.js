const macLinuxInstaller = 's=$(gh release download -R nscaledev/devflow -p install.sh -O -) && bash -c "$s" devflow';
const windowsInstaller = '$s = gh release download -R nscaledev/devflow -p install.ps1 -O -; if ($LASTEXITCODE -eq 0) { & ([scriptblock]::Create($s -join "`n"))';

export function installerCommand(platform, path) {
  if (platform === "windows") {
    // PowerShell also treats typographic apostrophes as string delimiters.
    const quoted = "'" + path.replace(/['\u2018\u2019\u201A\u201B]/g, (character) => "'" + character) + "'";
    return windowsInstaller + (path ? " -Repo " + quoted : "") + " }";
  }
  const quoted = "'" + path.split("'").join("'\u0022'\u0022'") + "'";
  return macLinuxInstaller + (path ? " --repo " + quoted : "");
}

export function agentPrompt(project) {
  const name = project.trim();
  return "Install Devflow from https://github.com/nscaledev/devflow in " +
    (name ? "the project " + JSON.stringify(name) : "this project") +
    ". Reuse saved provider choices. For any unconfigured role, suggest a provider when project evidence is clear and ask me when it is ambiguous. Preview changes before applying them, then verify setup and show me what changed.";
}

export const pluginCommands = {
  claude: [
    "claude plugin marketplace add nscaledev/devflow",
    "claude plugin install devflow@nscale",
  ].join("\n"),
  codex: [
    "codex plugin marketplace add nscaledev/devflow",
    "codex plugin add devflow@nscale",
  ].join("\n"),
  gemini: "gemini extensions install https://github.com/nscaledev/devflow",
};
