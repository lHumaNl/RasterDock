param(
    [Parameter(Mandatory = $true)][string] $ProbePath,
    [Parameter(Mandatory = $true)][string] $ParentPath,
    [Parameter(Mandatory = $true)][string] $WorkPath,
    [Parameter(Mandatory = $true)][string] $ReadyPath,
    [string] $FilesystemTestPath,
    [string] $StorageTestPath,
    [string] $ApplicationTestPath
)

$ErrorActionPreference = 'Stop'
$runTests = $false
$result = [ordered]@{
    identity = $null
    identity_sid = $null
    not_administrator = $false
    parent_create_access_denied = $false
    work_create_succeeded = $false
    probe_exit_code = $null
    probe_token_not_elevated = $false
    test_count = 0
    test_results = @()
    orphan_fixture_pid = $null
    errors = @()
}
$reportPath = Join-Path $WorkPath 'standard-user-report.json'
$utf8 = [Text.UTF8Encoding]::new($false)

function Invoke-ProductionTest([string] $Name, [string] $Executable) {
    $stdoutPath = Join-Path $WorkPath ($Name + '-test.stdout.txt')
    $stderrPath = Join-Path $WorkPath ($Name + '-test.stderr.txt')
    # Precreate fixed log names so startup and timeout failures still leave artifacts.
    [IO.File]::WriteAllText($stdoutPath, '', $utf8)
    [IO.File]::WriteAllText($stderrPath, '', $utf8)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $Executable
    $start.Arguments = '-test.v -test.timeout=90s'
    $start.WorkingDirectory = $WorkPath
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $testProcess = [Diagnostics.Process]::Start($start)
    $stdout = $null
    $stderr = $null
    $timedOut = $false
    try {
        $stdout = $testProcess.StandardOutput.ReadToEndAsync()
        $stderr = $testProcess.StandardError.ReadToEndAsync()
        if (-not $testProcess.WaitForExit(110000)) {
            $timedOut = $true
            $testProcess.Kill()
            if (-not $testProcess.WaitForExit(10000)) { throw "$Name test did not stop after timeout; owned job cleanup required." }
        }
        if (-not [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdout, $stderr), 10000)) {
            throw "$Name test output drain exceeded 10 seconds; owned job cleanup required."
        }
        return [pscustomobject]@{
            name = $Name
            exit_code = $testProcess.ExitCode
            timed_out = $timedOut
            stdout_log = [IO.Path]::GetFileName($stdoutPath)
            stderr_log = [IO.Path]::GetFileName($stderrPath)
        }
    } finally {
        # Windows PowerShell 5.1 uses .NET Framework: Task has Status but not
        # the newer IsCompletedSuccessfully property.
        if ($stdout -and $stdout.Status -eq [Threading.Tasks.TaskStatus]::RanToCompletion) { [IO.File]::WriteAllText($stdoutPath, $stdout.Result, $utf8) }
        if ($stderr -and $stderr.Status -eq [Threading.Tasks.TaskStatus]::RanToCompletion) { [IO.File]::WriteAllText($stderrPath, $stderr.Result, $utf8) }
        if (-not $testProcess.HasExited) {
            $testProcess.Kill()
            if (-not $testProcess.WaitForExit(10000)) { throw "$Name test still running after termination attempt." }
        }
        $testProcess.Dispose()
    }
}
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
    if ([bool]$StorageTestPath -ne [bool]$ApplicationTestPath -or [bool]$StorageTestPath -ne [bool]$FilesystemTestPath) { throw 'All three production test paths must be supplied together.' }
    $runTests = [bool]$StorageTestPath
    if ($runTests) {
        $testsPath = Join-Path $ParentPath 'tests'
        if (-not [string]::Equals([IO.Path]::GetFullPath($FilesystemTestPath), (Join-Path $testsPath 'filesystem.test.exe'), [StringComparison]::OrdinalIgnoreCase) -or
            -not [string]::Equals([IO.Path]::GetFullPath($StorageTestPath), (Join-Path $testsPath 'storage.test.exe'), [StringComparison]::OrdinalIgnoreCase) -or
            -not [string]::Equals([IO.Path]::GetFullPath($ApplicationTestPath), (Join-Path $testsPath 'application.test.exe'), [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Unexpected production test path; only controller-owned test executables are permitted.'
        }
    }
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
    if ($runTests) {
        $result.test_results += (Invoke-ProductionTest -Name 'filesystem' -Executable $FilesystemTestPath)
        $result.test_count = $result.test_results.Count
        $result.test_results += (Invoke-ProductionTest -Name 'storage' -Executable $StorageTestPath)
        $result.test_count = $result.test_results.Count
        $result.test_results += (Invoke-ProductionTest -Name 'application' -Executable $ApplicationTestPath)
        $result.test_count = $result.test_results.Count
        foreach ($test in $result.test_results) {
            if ($test.timed_out -or $test.exit_code -ne 0) {
                throw "Production tests failed: $($test.name) exit $($test.exit_code) timeout $($test.timed_out)."
            }
        }
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
if ($result.errors.Count -gt 0 -or -not $result.parent_create_access_denied -or -not $result.work_create_succeeded -or -not $result.probe_token_not_elevated -or
    ($runTests -and ($result.test_count -ne 3))) { exit 1 }
exit 0
