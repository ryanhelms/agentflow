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

    # >>> agentflow cache root (the same block is in every PowerShell script that finds the binary)
    # A repository's environment (a client's per-project settings) can set LOCALAPPDATA, so the
    # install cache is never taken from inside a git working tree or git directory, unless that
    # tree's root is the account's own profile folder. A path through a link or junction cannot
    # be checked here and counts as inside, and so does a dangling one. Only the file system is
    # read, never git, so GIT_DIR and the other git variables cannot change the answer. Messages
    # go to stderr.
    function Test-AgentflowLink($Info) {
      # True when the file or folder $Info (a FileSystemInfo) is a link, junction or other reparse point.
      return ([int]($Info.Attributes -band [IO.FileAttributes]::ReparsePoint)) -ne 0
    }
    function Get-AgentflowInsideRepository([string]$Path) {
      # Returns $null when $Path is outside every repository, otherwise why it is not used.
      if ([IO.Path]::DirectorySeparatorChar -eq '\') {
        $p = $Path -replace '/', '\'
        $rooted = $p -match '^[A-Za-z]:\\' -or $p -match '^\\\\[^\\?.]'
      } else {
        $p = $Path
        $rooted = $p.StartsWith('/')
      }
      if (!$rooted) { return 'cannot be checked (its path is not absolute)' }
      $parts = $p -split '[\\/]'
      if ($parts -contains '.' -or $parts -contains '..') { return 'cannot be checked (its path has a . or .. folder)' }
      $profileDir = [Environment]::GetFolderPath('UserProfile').TrimEnd('\', '/')
      try {
        $d = New-Object IO.DirectoryInfo $p
        while ($d) {
          $dir = $d.FullName
          if ($d.Exists) {
            if (Test-AgentflowLink $d) { return "cannot be checked (it goes through the link $dir)" }
            $head = New-Object IO.FileInfo (Join-Path $dir 'HEAD')
            if (@([IO.Directory]::GetFileSystemEntries($dir, '.git')).Count -gt 0 -or
                ($head.Exists -and !(Test-AgentflowLink $head) -and
                 [IO.Directory]::Exists((Join-Path $dir 'objects')) -and [IO.Directory]::Exists((Join-Path $dir 'refs')))) {
              if ($profileDir -and $dir.TrimEnd('\', '/') -eq $profileDir) { return $null }
              return "is inside the repository $dir"
            }
          } elseif ([IO.File]::Exists($dir)) {
            return "cannot be checked (the file $dir is in its path)"
          } elseif ($d.Parent -and $d.Parent.Exists -and
                    @([IO.Directory]::GetFileSystemEntries($d.Parent.FullName, $d.Name)).Count -gt 0) {
            # Listed by its folder but neither a folder nor a file: a dangling link or junction.
            return "cannot be checked (it goes through the dangling link $dir)"
          }
          $d = $d.Parent
        }
      } catch {
        return "cannot be checked ($($_.Exception.Message))"
      }
      return $null
    }
    function Get-AgentflowCacheRoot {
      # The install cache root: %LOCALAPPDATA%\agentflow, or the account's own one when that
      # is inside a repository.
      $local = [Environment]::GetFolderPath('LocalApplicationData')
      $own = if ($local) { Join-Path $local 'agentflow' } else { $null }
      if (!$env:LOCALAPPDATA) {
        if ($own) { return $own }
        throw 'agentflow: LOCALAPPDATA is not set and the account has no local application data folder'
      }
      $cache = Join-Path $env:LOCALAPPDATA 'agentflow'
      if ($own -and $cache.TrimEnd('\', '/') -eq $own.TrimEnd('\', '/')) { return $cache }
      $why = Get-AgentflowInsideRepository $cache
      if (!$why) { return $cache }
      if (!$own) { throw "agentflow: not using the cache $cache (from LOCALAPPDATA): it $why, and the account has no local application data folder to use instead" }
      [Console]::Error.WriteLine("agentflow: ignoring the cache $cache (from LOCALAPPDATA): it $why, so a repository could supply the agentflow binary. Using $own instead.")
      return $own
    }
    function Get-AgentflowActive([string]$CacheRoot) {
      # The version folder current.txt names, when it is a version folder directly in $CacheRoot
      # and holds agentflow.exe, neither of them a link, and $CacheRoot is the account's own
      # cache or outside every repository; otherwise $null. Links above the account's own cache
      # (a profile folder behind a junction) and a profile inside a repository do not matter.
      $pointer = Join-Path $CacheRoot 'current.txt'
      if (!(Test-Path -LiteralPath $pointer -PathType Leaf)) { return $null }
      $active = ([string](Get-Content -LiteralPath $pointer -Raw)).Trim()
      if (!$active -or (Split-Path -Parent $active) -ne $CacheRoot -or
          (Split-Path -Leaf $active) -notmatch '^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$') {
        [Console]::Error.WriteLine("agentflow: ignoring the cache pointer ${pointer}: it does not name a version folder of $CacheRoot")
        return $null
      }
      $local = [Environment]::GetFolderPath('LocalApplicationData')
      $why = $null
      if (!$local -or $CacheRoot.TrimEnd('\', '/') -ne (Join-Path $local 'agentflow').TrimEnd('\', '/')) {
        $why = Get-AgentflowInsideRepository $CacheRoot
      }
      if (!$why) {
        foreach ($item in @($active, (Join-Path $active 'agentflow.exe'))) {
          $info = Get-Item -LiteralPath $item -Force -ErrorAction SilentlyContinue
          if ($info -and (Test-AgentflowLink $info)) {
            $why = "cannot be checked (it goes through the link $item)"
            break
          }
        }
      }
      if ($why) {
        [Console]::Error.WriteLine("agentflow: not running $active\agentflow.exe: it $why")
        return $null
      }
      if (!(Test-Path -LiteralPath (Join-Path $active 'agentflow.exe') -PathType Leaf)) { return $null }
      return $active
    }
    # <<< agentflow cache root
    function Get-InstalledBinary {
        $active = Get-AgentflowActive (Get-AgentflowCacheRoot)
        if ($active) { return Join-Path $active 'agentflow.exe' }
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
                if (!$gh) { throw 'Resolving the latest release requires GitHub CLI (gh) signed in to github.com with access to ryanhelms/agentflow.' }
                $tag = & $gh.Source api --hostname github.com repos/ryanhelms/agentflow/releases/latest --jq .tag_name
                if ($LASTEXITCODE -ne 0 -or @($tag).Count -ne 1 -or $tag -cnotmatch '^v\d+\.\d+\.\d+$') {
                    throw 'Could not resolve the latest stable ryanhelms/agentflow release. Check gh auth status and repository access.'
                }
                $commandArgs = @('init')
                if (!$command -or (Split-Path -Leaf (Split-Path -Parent $command)) -ne $tag.Substring(1)) {
                    $source = "repos/ryanhelms/agentflow/contents/templates/repo/.agentflow/bin/agentflow.ps1?ref=$tag"
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
