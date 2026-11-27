# Run with pwsh. All repositories, credentials, and release responses are test fixtures.
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$installer = (Get-Item -LiteralPath (Join-Path $PSScriptRoot '../install.ps1')).FullName
$pwsh = (Get-Process -Id $PID).Path
$windows = $env:OS -eq 'Windows_NT'
$temp = (New-Item -ItemType Directory -Path (Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName()))).FullName
# The cache check refuses paths through links (macOS /var is one), so use the physical path.
if (!$windows) { $temp = (& /bin/sh -c 'cd -P -- "$1" && pwd -P' sh $temp) }
$original = (Get-Location).Path
$vars = @{}
foreach ($key in 'PATH','LOCALAPPDATA','DF_TEST_SHELL','DF_TEST_TEMP','GIT_DIR','GIT_WORK_TREE','GIT_COMMON_DIR','GIT_INDEX_FILE') {
    $vars[$key] = [Environment]::GetEnvironmentVariable($key)
}
function Assert([bool]$Pass, [string]$Reason) { if (!$Pass) { throw $Reason } }
function Run-Installer([string[]]$Options, [string]$InputText = '', [switch]$NonInteractive) {
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $pwsh
    $info.WorkingDirectory = (Get-Location).Path
    $info.UseShellExecute = $false
    $info.RedirectStandardInput = $info.RedirectStandardError = $info.RedirectStandardOutput = $true
    $info.ArgumentList.Add('-NoProfile')
    if ($NonInteractive) { $info.ArgumentList.Add('-NonInteractive') }
    $info.ArgumentList.Add('-File')
    $info.ArgumentList.Add($installer)
    foreach ($arg in $Options) { $info.ArgumentList.Add($arg) }
    $proc = [Diagnostics.Process]::Start($info)
    try {
        $out = $proc.StandardOutput.ReadToEndAsync()
        $err = $proc.StandardError.ReadToEndAsync()
        $proc.StandardInput.Write($InputText)
        $proc.StandardInput.Close()
        if (!$proc.WaitForExit(30000)) { $proc.Kill(); throw 'Installer timed out waiting for input.' }
        return @{ Code = $proc.ExitCode; Text = $out.Result + $err.Result }
    } finally { $proc.Dispose() }
}
try {
    foreach ($key in 'GIT_DIR','GIT_WORK_TREE','GIT_COMMON_DIR','GIT_INDEX_FILE') { Remove-Item "Env:$key" -ErrorAction SilentlyContinue }
    $env:LOCALAPPDATA = Join-Path $temp 'profile [test]'
    $env:DF_TEST_TEMP = $temp
    $env:DF_TEST_SHELL = $pwsh
    $env:PATH = $temp + [IO.Path]::PathSeparator + $env:PATH
    Set-Location -LiteralPath $temp
    # Mock gh as a process so stdout, nonzero exits, and partially written output cross a real process boundary.
    $mock = @'
$ErrorActionPreference = 'Stop'
$stage = $env:DF_TEST_TEMP
$mode = [IO.File]::ReadAllText((Join-Path $stage 'mode'))
[IO.File]::AppendAllText((Join-Path $stage 'gh.log'), ($args -join ' ') + [Environment]::NewLine)
if ($args -join ' ' -ceq 'api --hostname github.com repos/ryanhelms/agentflow/releases/latest --jq .tag_name') {
    if ($mode -eq 'no-release') { exit 19 }
    'v1.2.3'
} elseif ($args -join ' ' -ceq 'api --hostname github.com repos/ryanhelms/agentflow/contents/templates/repo/.agentflow/bin/agentflow.ps1?ref=v1.2.3') {
    $source = if ($mode -like 'stale-*') { 'release-launcher.ps1' } else { 'mock-launcher.ps1' }
    $bytes = [IO.File]::ReadAllBytes((Join-Path $stage $source))
    $size = $bytes.Length
    if ($mode -eq 'incomplete') { $size++ }
    @{ type='file'; encoding='base64'; size=$size; content=[Convert]::ToBase64String($bytes) } | ConvertTo-Json -Compress
    if ($mode -eq 'download-failure') { exit 21 }
} elseif ($args[0] -eq 'release' -and $args[1] -eq 'download') {
    $zip = 'agentflow_1.2.3_windows_amd64.zip'
    if ($args.Count -ne 11 -or ($args[2..5] -join ' ') -cne 'v1.2.3 --repo ryanhelms/agentflow --dir' -or
        ($args[7..10] -join ' ') -cne "--pattern $zip --pattern checksums.txt") { throw "Unexpected archive arguments: $args" }
    $pkg = Join-Path $args[6] $zip
    [IO.File]::Copy((Join-Path $stage 'release.zip'), $pkg)
    $sum = (Get-FileHash -Algorithm SHA256 -LiteralPath $pkg).Hash.ToLowerInvariant()
    if ($mode -eq 'stale-checksum') { $sum = '0' * 64 }
    [IO.File]::WriteAllText((Join-Path $args[6] 'checksums.txt'), "$sum  $zip")
    if ($mode -eq 'stale-download') { exit 23 }
} elseif ($args[0] -eq 'attestation' -and $args[1] -eq 'verify') {
    if ($args.Count -ne 7 -or (Split-Path -Leaf $args[2]) -cne 'agentflow_1.2.3_windows_amd64.zip' -or
        ($args[3..6] -join ' ') -cne '--repo ryanhelms/agentflow --signer-workflow ryanhelms/agentflow/.github/workflows/release.yml') {
        throw "Unexpected verification arguments: $args"
    }
    if ($mode -eq 'stale-signer') { exit 24 }
} else { throw "Unexpected gh arguments: $args" }
exit 0
'@
    [IO.File]::WriteAllText((Join-Path $temp 'mock-gh.ps1'), $mock)
    if ($windows) {
        [IO.File]::WriteAllText((Join-Path $temp 'gh.cmd'), "@echo off`r`n`"%DF_TEST_SHELL%`" -NoProfile -File `"%DF_TEST_TEMP%\mock-gh.ps1`" %*`r`nexit /b %errorlevel%`r`n")
    } else {
        $shim = "#!/bin/sh`n" + 'exec "$DF_TEST_SHELL" -NoProfile -File "$DF_TEST_TEMP/mock-gh.ps1" "$@"'
        [IO.File]::WriteAllText((Join-Path $temp 'gh'), $shim)
        & chmod +x (Join-Path $temp 'gh')
    }
    $launcher = @'
$ErrorActionPreference = 'Stop'
$mode = [IO.File]::ReadAllText((Join-Path $env:DF_TEST_TEMP 'mode'))
if ($args.Count -ne 3 -or $args[0] -ne '--bootstrap-release' -or $args[1] -ne 'v1.2.3' -or $args[2] -ne 'init') { throw "Unexpected launcher arguments: $args" }
[IO.File]::AppendAllText((Join-Path $env:DF_TEST_TEMP 'calls.log'), "init|$((Get-Location).Path)" + [Environment]::NewLine)
if ($mode -eq 'init-failure') { exit 17 }
if ($args[2] -eq 'init') {
    foreach ($name in '.agentflow/config.yml','.agentflow/bin/agentflow','.agentflow/bin/agentflow.cmd','.agentflow/bin/agentflow.ps1','.github/workflows/agentflow.yml') {
        $path = Join-Path (Get-Location).Path $name
        [IO.Directory]::CreateDirectory((Split-Path -Parent $path)) | Out-Null
        if (![IO.File]::Exists($path)) { [IO.File]::WriteAllText($path,'throw "Checkout launcher executed"') }
    }
}
exit 0
'@
    [IO.File]::WriteAllText((Join-Path $temp 'mock-launcher.ps1'), $launcher)
    [IO.File]::WriteAllText((Join-Path $temp 'mode'), 'ok')

    $repo = Join-Path $temp 'primary project'
    $bare = Join-Path $temp 'bare.git'
    $linked = Join-Path $temp 'linked [project]'
    & git init --quiet $repo
    & git -C $repo -c user.name=Installer -c user.email=installer@example.test -c commit.gpgsign=false commit --quiet --allow-empty -m fixture
    & git -C $repo worktree add -q --detach $linked HEAD
    & git init --quiet --bare $bare
    Assert ($LASTEXITCODE -eq 0) 'Could not create disposable Git fixtures.'
    $linked = (Get-Item -LiteralPath (& git -C $linked rev-parse --show-toplevel)).FullName
    $nested = Join-Path $linked 'nested'
    [IO.Directory]::CreateDirectory($nested) | Out-Null

    $gitDir = Join-Path $repo '.git'
    $env:GIT_DIR = $gitDir
    $env:GIT_WORK_TREE = $repo
    foreach ($path in $temp, $bare, $gitDir, (Join-Path $gitDir 'objects')) {
        $res = Run-Installer @('-Repo',$path)
        Assert ($res.Code -ne 0 -and $res.Text -match 'Repo PATH') "Accepted invalid target or failed to explain how to retry: $path"
    }
    $res = Run-Installer @($temp) ($repo + [Environment]::NewLine) -NonInteractive
    Assert ($res.Code -ne 0 -and !(Test-Path -LiteralPath (Join-Path $temp 'gh.log'))) 'Invalid/noninteractive selection reached bootstrap.'
    $res = Run-Installer @($bare) ("$gitDir" + [Environment]::NewLine + $nested + [Environment]::NewLine)
    Assert ($res.Code -eq 0) "Retry with nested linked checkout failed: $($res.Text)"
    $calls = Join-Path $temp 'calls.log'
    Assert ((Get-Content -LiteralPath $calls -Tail 1) -eq "init|$linked") "Init ran outside the selected worktree root: expected $linked; actual $(Get-Content -LiteralPath $calls -Tail 1)."
    Assert (!(Test-Path -LiteralPath (Join-Path $repo '.agentflow'))) 'A different checkout was modified.'

    $script = [scriptblock]::Create([IO.File]::ReadAllText($installer))
    $before = (Get-Location).Path
    & $script -Repo $nested
    Assert ((Get-Location).Path -eq $before -and $env:GIT_DIR -eq $gitDir -and $env:GIT_WORK_TREE -eq $repo) 'Installer changed caller state.'
    Assert ((Get-Content -LiteralPath $calls -Tail 1) -eq "init|$linked") 'Second run failed to initialize the selected root.'
    Remove-Item Env:GIT_DIR, Env:GIT_WORK_TREE

    Push-Location -LiteralPath $nested
    try { $res = Run-Installer @(); Assert ($res.Code -eq 0) "No-argument nested checkout failed: $($res.Text)" }
    finally { Pop-Location }
    $beforeCalls = @(Get-Content -LiteralPath $calls).Count
    foreach ($mode in 'no-release','incomplete','download-failure','init-failure') {
        [IO.File]::WriteAllText((Join-Path $temp 'mode'), $mode)
        $res = Run-Installer @($repo)
        Assert ($res.Code -ne 0) "Installer ignored $mode."
        if ($mode -ne 'init-failure') { Assert (@(Get-Content -LiteralPath $calls).Count -eq $beforeCalls) "Executed a failed download: $mode" }
        else { Assert ($res.Text -match '17') 'Installer hid the launcher exit status.' }
    }
    $missing = Join-Path $linked '.agentflow/config.yml'
    Remove-Item -LiteralPath $missing
    [IO.File]::WriteAllText((Join-Path $temp 'mode'), 'ok')
    $res = Run-Installer @($linked)
    Assert ($res.Code -eq 0 -and (Test-Path -LiteralPath $missing)) "Partial setup was not repaired: $($res.Text)"
    [IO.File]::WriteAllText($missing,'user settings')

    # A native executable that reads a file named "init" stands in for a matching cache.
    $cache = Join-Path $env:LOCALAPPDATA 'agentflow'
    $versionDir = Join-Path $cache '1.2.3'
    [IO.Directory]::CreateDirectory($versionDir) | Out-Null
    $cached = Join-Path $versionDir 'agentflow.exe'
    if ($windows) { Copy-Item -LiteralPath (Join-Path $env:SystemRoot 'System32/sort.exe') -Destination $cached }
    else { New-Item -ItemType SymbolicLink -Path $cached -Target '/bin/cat' | Out-Null }
    [IO.File]::WriteAllText((Join-Path $cache 'current.txt'), $versionDir)
    [IO.File]::WriteAllText((Join-Path $linked 'init'), 'verified-from-cache')
    $ghCount = @(Get-Content -LiteralPath (Join-Path $temp 'gh.log')).Count
    $res = Run-Installer @($linked)
    Assert ($res.Code -eq 0 -and $res.Text -match 'verified-from-cache') "Trusted cache failed: $($res.Text)"
    Assert (@(Get-Content -LiteralPath (Join-Path $temp 'gh.log')).Count -eq $ghCount + 1) 'Current cache should only resolve the latest tag.'
    # Exercise real release verification while migrating a stale pointer. On Windows the ZIP
    # contains a native fixture. On other hosts the matching version is already cached.
    $sourceLauncher = Join-Path (Split-Path -Parent $installer) 'templates/repo/.agentflow/bin/agentflow.ps1'
    Copy-Item -LiteralPath $sourceLauncher -Destination (Join-Path $temp 'release-launcher.ps1')
    $archiveDir = [IO.Directory]::CreateDirectory((Join-Path $temp 'archive')).FullName
    if ($windows) {
        Copy-Item -LiteralPath $cached -Destination (Join-Path $archiveDir 'agentflow.exe')
        Remove-Item -LiteralPath $versionDir -Recurse -Force
    } else {
        [IO.File]::WriteAllText((Join-Path $archiveDir 'agentflow.exe'), 'fixture')
    }
    [IO.Compression.ZipFile]::CreateFromDirectory($archiveDir,(Join-Path $temp 'release.zip'))
    $staleDir = [IO.Directory]::CreateDirectory((Join-Path $cache '0.9.0')).FullName
    $staleExe = Join-Path $staleDir 'agentflow.exe'
    if ($windows) { Copy-Item -LiteralPath (Join-Path $env:SystemRoot 'System32/sort.exe') -Destination $staleExe }
    else { New-Item -ItemType SymbolicLink -Path $staleExe -Target '/bin/cat' | Out-Null }
    $pointer = Join-Path $cache 'current.txt'
    $ghLog = Join-Path $temp 'gh.log'
    [IO.File]::WriteAllText($pointer, $staleDir)
    foreach ($mode in 'stale-download','stale-checksum','stale-signer','stale-success') {
        [IO.File]::WriteAllText($ghLog, '')
        [IO.File]::WriteAllText((Join-Path $temp 'mode'), $mode)
        $res = Run-Installer @($linked)
        $operations = [IO.File]::ReadAllText($ghLog)
        Assert ($operations -match 'release download v1.2.3') "Skipped the selected stable release in ${mode}: $($res.Text)"
        if ($mode -in 'stale-signer','stale-success') { Assert ($operations -match 'attestation verify') "Skipped verification in $mode." }
        else { Assert ($operations -notmatch 'attestation verify') "Continued after a failed download or checksum in $mode." }
        if ($mode -eq 'stale-success') {
            Assert ($res.Code -eq 0 -and $res.Text -match 'verified-from-cache') "Validated release failed to run: $($res.Text)"
            Assert ([IO.File]::ReadAllText($pointer) -eq $versionDir) 'Latest release was not activated.'
        } else {
            Assert ($res.Code -ne 0 -and $res.Text -notmatch 'verified-from-cache') "Executed an unverified release in $mode."
            Assert ([IO.File]::ReadAllText($pointer) -eq $staleDir) "Changed the prior installation after $mode."
        }
    }
    # A repository-set LOCALAPPDATA is never used to find or install the binary (#129).
    # Only the checks run here: the account's own folder is computed, never read.
    $launcherText = [IO.File]::ReadAllText($sourceLauncher)
    $begin = $launcherText.IndexOf('# >>> agentflow cache root')
    $end = $launcherText.IndexOf('# <<< agentflow cache root')
    Assert ($begin -ge 0 -and $end -gt $begin) 'The launcher has no cache-root block.'
    . ([scriptblock]::Create($launcherText.Substring($begin, $end - $begin)))
    $evil = Join-Path $temp 'evil repo'
    [IO.Directory]::CreateDirectory($evil) | Out-Null
    & git -C $evil init -q
    Assert ($LASTEXITCODE -eq 0) 'git init failed.'
    $outside = [IO.Directory]::CreateDirectory((Join-Path $temp 'outside')).FullName
    Assert ($null -eq (Get-AgentflowInsideRepository (Join-Path $outside 'agentflow'))) 'Refused a cache outside every repository.'
    foreach ($inside in (Join-Path $evil '.cache/agentflow'), (Join-Path $evil '.git/agentflow')) {
        Assert ((Get-AgentflowInsideRepository $inside) -match 'is inside the repository') "Accepted a cache inside a repository: $inside"
    }
    Assert ((Get-AgentflowInsideRepository 'relative/agentflow') -match 'cannot be checked') 'Accepted a relative cache.'
    Assert ((Get-AgentflowInsideRepository (Join-Path $evil '../outside/agentflow')) -match 'cannot be checked') 'Accepted a .. cache.'
    $env:LOCALAPPDATA = Join-Path $evil '.cache'
    $root = Get-AgentflowCacheRoot 2>$null
    $own = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'agentflow'
    Assert ($root -eq $own) "Used the repository-set LOCALAPPDATA: $root"
    $env:LOCALAPPDATA = $outside
    Assert ((Get-AgentflowCacheRoot) -eq (Join-Path $outside 'agentflow')) 'Changed a cache outside every repository.'
    $planted = Join-Path $evil 'agentflow'
    $plantedVersion = [IO.Directory]::CreateDirectory((Join-Path $planted '1.2.3')).FullName
    [IO.File]::WriteAllText((Join-Path $plantedVersion 'agentflow.exe'), 'planted')
    [IO.File]::WriteAllText((Join-Path $planted 'current.txt'), $plantedVersion)
    function Get-Warning([scriptblock]$Block) {
        # Runs $Block and returns what it wrote to [Console]::Error.
        $old = [Console]::Error
        $writer = [IO.StringWriter]::new()
        [Console]::SetError($writer)
        try { $null = & $Block } finally { [Console]::SetError($old) }
        return $writer.ToString()
    }
    function New-TestLink([string]$Path, [string]$Target) {
        $type = if ($windows) { 'Junction' } else { 'SymbolicLink' }
        New-Item -ItemType $type -Path $Path -Target $Target | Out-Null
    }
    $warning = Get-Warning { Assert ($null -eq (Get-AgentflowActive $planted)) 'Would run a binary from inside a repository.' }
    Assert ($warning -match 'is inside the repository') "No repository reason for a cache inside a repository: $warning"
    $pointedAway = [IO.Directory]::CreateDirectory((Join-Path $outside 'agentflow')).FullName
    [IO.File]::WriteAllText((Join-Path $pointedAway 'current.txt'), $plantedVersion)
    $warning = Get-Warning { Assert ($null -eq (Get-AgentflowActive $pointedAway)) 'Followed current.txt out of the cache.' }
    Assert ($warning -match 'does not name a version folder') "No reason for a pointer out of the cache: $warning"
    # A version folder in the cache that is a link into a repository: the link check refuses it.
    $linkedRoot = [IO.Directory]::CreateDirectory((Join-Path $outside 'linked-cache')).FullName
    New-TestLink (Join-Path $linkedRoot '1.2.3') $plantedVersion
    [IO.File]::WriteAllText((Join-Path $linkedRoot 'current.txt'), (Join-Path $linkedRoot '1.2.3'))
    $warning = Get-Warning { Assert ($null -eq (Get-AgentflowActive $linkedRoot)) 'Followed current.txt into a repository.' }
    Assert ($warning -match 'goes through the link') "No link reason for a version folder that links into a repository: $warning"
    # A version folder directly in a cache outside every repository runs.
    $goodRoot = Join-Path $outside 'good-cache'
    $goodVersion = [IO.Directory]::CreateDirectory((Join-Path $goodRoot '1.2.3')).FullName
    [IO.File]::WriteAllText((Join-Path $goodVersion 'agentflow.exe'), 'good')
    [IO.File]::WriteAllText((Join-Path $goodRoot 'current.txt'), $goodVersion)
    Assert ((Get-AgentflowActive $goodRoot) -eq $goodVersion) 'Refused a version folder in a cache outside every repository.'
    # A dangling link cannot be checked: its target could be created inside a repository.
    $gone = [IO.Directory]::CreateDirectory((Join-Path $evil 'gone')).FullName
    $dangling = Join-Path $outside 'dangling'
    New-TestLink $dangling $gone
    Remove-Item -LiteralPath $gone -Recurse -Force
    Assert ((Get-AgentflowInsideRepository (Join-Path $dangling 'agentflow')) -match 'link') 'Accepted a cache through a dangling link.'
    Assert ((Get-AgentflowInsideRepository $dangling) -match 'link') 'Accepted a dangling link as the cache.'
    $env:LOCALAPPDATA = $dangling
    Assert ((Get-AgentflowCacheRoot 2>$null) -eq $own) 'Used a LOCALAPPDATA that is a dangling link.'
    # The account's own cache behind a link (a profile behind a junction) still runs. Off
    # Windows, .NET takes the local application data folder from XDG_DATA_HOME.
    if (!$windows) {
        $realLocal = [IO.Directory]::CreateDirectory((Join-Path $temp 'real-local')).FullName
        $linkedLocal = Join-Path $temp 'linked-local'
        New-TestLink $linkedLocal $realLocal
        $oldData = $env:XDG_DATA_HOME
        $env:XDG_DATA_HOME = $linkedLocal
        try {
            if ([Environment]::GetFolderPath('LocalApplicationData') -eq $linkedLocal) {
                $ownRoot = Join-Path $linkedLocal 'agentflow'
                $ownVersion = [IO.Directory]::CreateDirectory((Join-Path $ownRoot '1.2.3')).FullName
                [IO.File]::WriteAllText((Join-Path $ownVersion 'agentflow.exe'), 'own')
                [IO.File]::WriteAllText((Join-Path $ownRoot 'current.txt'), $ownVersion)
                Assert ((Get-AgentflowActive $ownRoot) -eq $ownVersion) 'Refused the account''s own cache behind a link.'
            }
        } finally { $env:XDG_DATA_HOME = $oldData }
    }
    'PowerShell project installer tests passed.'
} finally {
    Set-Location -LiteralPath $original
    foreach ($key in $vars.Keys) {
        if ($null -eq $vars[$key]) { Remove-Item "Env:$key" -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($key, $vars[$key]) }
    }
    Remove-Item -LiteralPath $temp -Force -Recurse
}
