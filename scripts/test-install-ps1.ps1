# Run with pwsh. All repositories, credentials, and release responses are test fixtures.
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$installer = (Get-Item -LiteralPath (Join-Path $PSScriptRoot '../install.ps1')).FullName
$pwsh = (Get-Process -Id $PID).Path
$windows = $env:OS -eq 'Windows_NT'
$temp = (New-Item -ItemType Directory -Path (Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName()))).FullName
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
if ($args -join ' ' -ceq 'api --hostname github.com repos/nscaledev/devflow/releases/latest --jq .tag_name') {
    if ($mode -eq 'no-release') { exit 19 }
    'v1.2.3'
} elseif ($args -join ' ' -ceq 'api --hostname github.com repos/nscaledev/devflow/contents/templates/repo/.devflow/bin/devflow.ps1?ref=v1.2.3') {
    $source = if ($mode -like 'stale-*') { 'release-launcher.ps1' } else { 'mock-launcher.ps1' }
    $bytes = [IO.File]::ReadAllBytes((Join-Path $stage $source))
    $size = $bytes.Length
    if ($mode -eq 'incomplete') { $size++ }
    @{ type='file'; encoding='base64'; size=$size; content=[Convert]::ToBase64String($bytes) } | ConvertTo-Json -Compress
    if ($mode -eq 'download-failure') { exit 21 }
} elseif ($args[0] -eq 'release' -and $args[1] -eq 'download') {
    $zip = 'devflow_1.2.3_windows_amd64.zip'
    if ($args.Count -ne 11 -or ($args[2..5] -join ' ') -cne 'v1.2.3 --repo nscaledev/devflow --dir' -or
        ($args[7..10] -join ' ') -cne "--pattern $zip --pattern checksums.txt") { throw "Unexpected archive arguments: $args" }
    $pkg = Join-Path $args[6] $zip
    [IO.File]::Copy((Join-Path $stage 'release.zip'), $pkg)
    $sum = (Get-FileHash -Algorithm SHA256 -LiteralPath $pkg).Hash.ToLowerInvariant()
    if ($mode -eq 'stale-checksum') { $sum = '0' * 64 }
    [IO.File]::WriteAllText((Join-Path $args[6] 'checksums.txt'), "$sum  $zip")
    if ($mode -eq 'stale-download') { exit 23 }
} elseif ($args[0] -eq 'attestation' -and $args[1] -eq 'verify') {
    if ($args.Count -ne 7 -or (Split-Path -Leaf $args[2]) -cne 'devflow_1.2.3_windows_amd64.zip' -or
        ($args[3..6] -join ' ') -cne '--repo nscaledev/devflow --signer-workflow nscaledev/devflow/.github/workflows/release.yml') {
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
    foreach ($name in '.devflow/config.yml','.devflow/bin/devflow','.devflow/bin/devflow.cmd','.devflow/bin/devflow.ps1','.github/workflows/devflow.yml') {
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
    Assert (!(Test-Path -LiteralPath (Join-Path $repo '.devflow'))) 'A different checkout was modified.'

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
    $missing = Join-Path $linked '.devflow/config.yml'
    Remove-Item -LiteralPath $missing
    [IO.File]::WriteAllText((Join-Path $temp 'mode'), 'ok')
    $res = Run-Installer @($linked)
    Assert ($res.Code -eq 0 -and (Test-Path -LiteralPath $missing)) "Partial setup was not repaired: $($res.Text)"
    [IO.File]::WriteAllText($missing,'user settings')

    # A native executable that reads a file named "init" stands in for a matching cache.
    $cache = Join-Path $env:LOCALAPPDATA 'devflow'
    $versionDir = Join-Path $cache '1.2.3'
    [IO.Directory]::CreateDirectory($versionDir) | Out-Null
    $cached = Join-Path $versionDir 'devflow.exe'
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
    $sourceLauncher = Join-Path (Split-Path -Parent $installer) 'templates/repo/.devflow/bin/devflow.ps1'
    Copy-Item -LiteralPath $sourceLauncher -Destination (Join-Path $temp 'release-launcher.ps1')
    $archiveDir = [IO.Directory]::CreateDirectory((Join-Path $temp 'archive')).FullName
    if ($windows) {
        Copy-Item -LiteralPath $cached -Destination (Join-Path $archiveDir 'devflow.exe')
        Remove-Item -LiteralPath $versionDir -Recurse -Force
    } else {
        [IO.File]::WriteAllText((Join-Path $archiveDir 'devflow.exe'), 'fixture')
    }
    [IO.Compression.ZipFile]::CreateFromDirectory($archiveDir,(Join-Path $temp 'release.zip'))
    $staleDir = [IO.Directory]::CreateDirectory((Join-Path $cache '0.9.0')).FullName
    $staleExe = Join-Path $staleDir 'devflow.exe'
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
    'PowerShell project installer tests passed.'
} finally {
    Set-Location -LiteralPath $original
    foreach ($key in $vars.Keys) {
        if ($null -eq $vars[$key]) { Remove-Item "Env:$key" -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($key, $vars[$key]) }
    }
    Remove-Item -LiteralPath $temp -Force -Recurse
}
