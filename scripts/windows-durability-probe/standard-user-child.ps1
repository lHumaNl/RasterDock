param(
    [Parameter(Mandatory = $true)][string] $ProbePath,
    [Parameter(Mandatory = $true)][string] $ParentPath,
    [Parameter(Mandatory = $true)][string] $WorkPath,
    [Parameter(Mandatory = $true)][string] $ReadyPath
)

$ErrorActionPreference = 'Stop'
$result = [ordered]@{
    identity = $null
    identity_sid = $null
    not_administrator = $false
    parent_create_access_denied = $false
    work_create_succeeded = $false
    probe_exit_code = $null
    probe_token_not_elevated = $false
    orphan_fixture_pid = $null
    errors = @()
}
$reportPath = Join-Path $WorkPath 'standard-user-report.json'
$utf8 = [Text.UTF8Encoding]::new($false)
try {
    # Parent directory is read-only for this account. The controller creates the
    # per-run marker only after assigning this wrapper to its owned job. Never
    # spawn the probe or fixture before release; absence/timeout fails closed.
    if ([IO.Path]::GetDirectoryName($ReadyPath) -ne $ParentPath) { throw 'Invalid controller ready marker path.' }
    $readyDeadline = [datetime]::UtcNow.AddSeconds(60)
    while (-not [IO.File]::Exists($ReadyPath)) {
        if ([datetime]::UtcNow -ge $readyDeadline) { throw 'Controller job assignment was not released within 60 seconds.' }
        Start-Sleep -Milliseconds 100
    }
    $env:TEMP = $WorkPath
    $env:TMP = $WorkPath
    [Environment]::CurrentDirectory = $WorkPath
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    $result.identity = $identity.Name
    $result.identity_sid = $identity.User.Value
    $administrators = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
    $result.not_administrator = -not $principal.IsInRole($administrators)
    if (-not $result.not_administrator) { throw 'Child token belongs to Administrators.' }

    $parentTest = Join-Path $ParentPath 'parent-create-denial-test'
    $stream = $null
    try {
        $stream = [IO.FileStream]::new($parentTest, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    } catch {
        $reason = $_.Exception
        while ($reason.InnerException) { $reason = $reason.InnerException }
        if (($reason -is [UnauthorizedAccessException]) -or
            (($reason -is [IO.IOException]) -and ($reason.HResult -eq -2147024891))) {
            $result.parent_create_access_denied = $true
        } else {
            throw
        }
    }
    # Only failure of CreateNew itself proves the ACL boundary. A later delete
    # denial must never turn successful creation into a passing observation.
    if ($stream) {
        $stream.Dispose()
        try { [IO.File]::Delete($parentTest) }
        finally { throw 'Exclusive create in read-only parent unexpectedly succeeded.' }
    }

    $workTest = Join-Path $WorkPath 'child-create-test'
    $stream = [IO.FileStream]::new($workTest, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    $stream.Dispose()
    [IO.File]::Delete($workTest)
    $result.work_create_succeeded = $true

    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $ProbePath
    $start.WorkingDirectory = $WorkPath
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [System.Diagnostics.Process]::Start($start)
    try {
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(180000)) {
            $process.Kill()
            if ($process.WaitForExit(10000)) {
                if ([Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdout, $stderr), 10000)) {
                    [IO.File]::WriteAllText((Join-Path $WorkPath 'probe.json'), $stdout.Result, $utf8)
                    [IO.File]::WriteAllText((Join-Path $WorkPath 'probe.stderr.txt'), $stderr.Result, $utf8)
                }
            }
            throw 'Probe exceeded 180 seconds.'
        }
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdout, $stderr), 10000)) {
            throw 'Probe output drain exceeded 10 seconds; a descendant may retain pipe handles.'
        }
        [IO.File]::WriteAllText((Join-Path $WorkPath 'probe.json'), $stdout.Result, $utf8)
        [IO.File]::WriteAllText((Join-Path $WorkPath 'probe.stderr.txt'), $stderr.Result, $utf8)
        $result.probe_exit_code = $process.ExitCode
        $probe = $stdout.Result | ConvertFrom-Json -ErrorAction Stop
        $result.probe_token_not_elevated = ($probe.token_elevated -is [bool]) -and ($probe.token_elevated -eq $false)
        if (-not $result.probe_token_not_elevated) { throw 'Probe token_elevated is missing, unknown or true.' }
        if ($process.ExitCode -ne 0) { throw "Probe exited with code $($process.ExitCode)." }
    } finally {
        if (-not $process.HasExited) {
            $process.Kill()
            if (-not $process.WaitForExit(10000)) { throw 'Owned probe still running after termination attempt.' }
        }
        $process.Dispose()
    }
    # Controlled cleanup regression: this process deliberately outlives the
    # wrapper, with inherited wrapper standard handles (NOT probe stream-drain
    # handles). The controller verifies membership using an opened process handle
    # and terminates the owned job after the wrapper exits.
    $fixture = [Diagnostics.ProcessStartInfo]::new()
    $fixture.FileName = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
    # Longer than the controller's bounded wrapper/report waits, so successful
    # cleanup must terminate the fixture rather than merely wait for natural exit.
    $fixture.Arguments = '-NoProfile -NonInteractive -Command "Start-Sleep -Seconds 900"'
    $fixture.WorkingDirectory = $WorkPath
    $fixture.UseShellExecute = $false
    $fixture.CreateNoWindow = $true
    $orphan = [Diagnostics.Process]::Start($fixture)
    try {
        if ($orphan.HasExited) { throw 'Controlled orphan fixture exited before its parent.' }
        $result.orphan_fixture_pid = $orphan.Id
    } finally { $orphan.Dispose() }
} catch {
    $result.errors += $_.Exception.Message
} finally {
    [IO.File]::WriteAllText($reportPath, ($result | ConvertTo-Json -Depth 5), $utf8)
}
if ($result.errors.Count -gt 0 -or -not $result.parent_create_access_denied -or -not $result.work_create_succeeded -or -not $result.probe_token_not_elevated) { exit 1 }
exit 0
