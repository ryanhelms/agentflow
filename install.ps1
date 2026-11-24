# Install Agentflow into a Git working tree. Supply a path or start in the intended checkout.
[CmdletBinding()]
param([Parameter(Position = 0)][string]$Repo = (Get-Location).Path)

& {
    $ErrorActionPreference = 'Stop'
    $PSNativeCommandUseErrorActionPreference = $false

    function Find-WorktreeRoot([string]$Candidate) {
        if ([string]::IsNullOrWhiteSpace($Candidate)) { return }
        $directory = Get-Item -LiteralPath $Candidate -ErrorAction SilentlyContinue
        if ($directory -isnot [IO.DirectoryInfo]) { return }
        # A failed native Git probe is expected for an invalid candidate.
        $ErrorActionPreference = 'Continue'
        $lines = @(& $git -C $directory.FullName rev-parse --is-inside-work-tree --show-toplevel 2>$null)
        if ($LASTEXITCODE -eq 0 -and $lines.Count -eq 2 -and $lines[0] -eq 'true') {
            return $lines[1]
        }
    }

    function Get-InstalledBinary {
        $cache = if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'agentflow' } else { Join-Path $HOME '.cache/agentflow' }
        $pointer = Join-Path $cache 'current.txt'
        if (!(Test-Path -LiteralPath $pointer -PathType Leaf)) { return }
        $directory = ([string](Get-Content -LiteralPath $pointer -Raw)).Trim()
        if (!$directory -or ![IO.Path]::IsPathRooted($directory) -or
            (Split-Path -Parent $directory) -ne $cache -or (Split-Path -Leaf $directory) -notmatch '^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$') {
            throw "Invalid Agentflow cache pointer at $pointer. Repair the installation and retry."
        }
        $file = Join-Path $directory 'agentflow.exe'
        if (Test-Path -LiteralPath $file -PathType Leaf) { return $file }
    }

    # Git settings inherited from a hook or another shell must not redirect this operation.
    $gitEnvironment = @{}
    foreach ($name in 'GIT_DIR', 'GIT_WORK_TREE', 'GIT_COMMON_DIR', 'GIT_INDEX_FILE') {
        $gitEnvironment[$name] = [Environment]::GetEnvironmentVariable($name)
        Remove-Item "Env:$name" -ErrorAction SilentlyContinue
    }
    try {
        $git = (Get-Command git -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
        $candidate = $Repo
        while (!($root = Find-WorktreeRoot $candidate)) {
            [Console]::Error.WriteLine("No Git working tree at '$candidate'. Supply the target repository with -Repo PATH.")
            if ([Environment]::GetCommandLineArgs() -contains '-NonInteractive') { throw 'A Git working tree is required. Rerun with -Repo PATH.' }
            if ([Console]::IsInputRedirected) {
                $candidate = [Console]::ReadLine()
            } else {
                $candidate = Read-Host 'Enter a Git working tree path (leave blank to cancel)'
            }
            if ([string]::IsNullOrWhiteSpace($candidate)) { throw 'Installation cancelled. Rerun with -Repo PATH.' }
        }

        $markers = @('.agentflow/config.yml', '.agentflow/bin/agentflow', '.agentflow/bin/agentflow.ps1',
                     '.agentflow/bin/agentflow.cmd', '.github/workflows/agentflow.yml')
        Push-Location -LiteralPath $root
        try {
            foreach ($path in $markers) {
                $item = Get-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
                if ($item -and ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint))) {
                    throw "Expected a regular project file at $path."
                }
            }
            Write-Host "Initializing Agentflow in $root."

            $command = Get-InstalledBinary
            $stage = $null
            try {
                $gh = Get-Command gh -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
                if (!$gh) { throw 'Resolving the latest release requires GitHub CLI (gh) signed in to github.com with access to nscaledev/agentflow.' }
                $tag = & $gh.Source api --hostname github.com repos/nscaledev/agentflow/releases/latest --jq .tag_name
                if ($LASTEXITCODE -ne 0 -or @($tag).Count -ne 1 -or $tag -cnotmatch '^v\d+\.\d+\.\d+$') {
                    throw 'Could not resolve the latest stable nscaledev/agentflow release. Check gh auth status and repository access.'
                }
                $commandArgs = @('init')
                if (!$command -or (Split-Path -Leaf (Split-Path -Parent $command)) -ne $tag.Substring(1)) {
                    $source = "repos/nscaledev/agentflow/contents/templates/repo/.agentflow/bin/agentflow.ps1?ref=$tag"
                    $response = & $gh.Source api --hostname github.com $source
                    if ($LASTEXITCODE -ne 0) { throw "Could not download the Agentflow launcher at $tag." }
                    $file = ($response -join [Environment]::NewLine) | ConvertFrom-Json
                    if ($file.type -ne 'file' -or $file.encoding -ne 'base64' -or $file.size -le 0) {
                        throw "Unexpected launcher response for $tag."
                    }
                    $bytes = [Convert]::FromBase64String($file.content)
                    if ($bytes.Length -ne $file.size) { throw "Incomplete Agentflow launcher at $tag." }
                    $stageName = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
                    $stage = (New-Item -ItemType Directory -Path $stageName).FullName
                    $command = Join-Path $stage 'agentflow.ps1'
                    [IO.File]::WriteAllBytes($command, $bytes)
                    $commandArgs = @('--bootstrap-release', $tag, 'init')
                }
                & $command @commandArgs
                if ($LASTEXITCODE -ne 0) { throw "Agentflow init failed with exit code $LASTEXITCODE." }
            } finally {
                if ($stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
            }
        } finally {
            Pop-Location
        }
    } finally {
        foreach ($name in $gitEnvironment.Keys) {
            if ($null -ne $gitEnvironment[$name]) {
                [Environment]::SetEnvironmentVariable($name, $gitEnvironment[$name])
            }
        }
    }
}
