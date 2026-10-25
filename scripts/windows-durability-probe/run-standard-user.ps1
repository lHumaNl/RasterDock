param(
    [Parameter(Mandatory = $true)][string] $ProbePath,
    [Parameter(Mandatory = $true)][string] $ArtifactDirectory
)

$ErrorActionPreference = 'Stop'
$root = $null
$rootCreated = $false
$userName = $null
$userSid = $null
$process = $null
$job = $null
$wrapperAssigned = $false
$work = $null
$failure = $null
$cleanupErrors = @()
$orphanFixtureId = $null
$fixtureInJob = $false
$jobCleanupVerified = $false
$jobActiveBeforeCleanup = $null
$jobActiveAfterCleanup = $null
$jobTerminated = $false
$utf8 = [Text.UTF8Encoding]::new($false)
[IO.Directory]::CreateDirectory($ArtifactDirectory) | Out-Null

function Set-OwnedAcl([string] $Path, [Security.Principal.SecurityIdentifier] $UserSid, [string] $UserRights) {
    $acl = Get-Acl -LiteralPath $Path
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($entry in @(
        [pscustomobject]@{ Sid = 'S-1-5-18'; Rights = 'FullControl' },     # SYSTEM
        [pscustomobject]@{ Sid = 'S-1-5-32-544'; Rights = 'FullControl' }, # built-in Administrators
        [pscustomobject]@{ Sid = $UserSid.Value; Rights = $UserRights }
    )) {
        $sid = [Security.Principal.SecurityIdentifier]::new($entry.Sid)
        $rule = [Security.AccessControl.FileSystemAccessRule]::new(
            $sid, [Security.AccessControl.FileSystemRights]$entry.Rights,
            [Security.AccessControl.InheritanceFlags] 'ContainerInherit, ObjectInherit',
            [Security.AccessControl.PropagationFlags]::None,
            [Security.AccessControl.AccessControlType]::Allow)
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

try {
    if (-not (Test-Path -LiteralPath $ProbePath -PathType Leaf)) { throw 'Diagnostic executable not found.' }
    $id = [guid]::NewGuid().ToString('N')
    $userName = 'rdp' + $id.Substring(0, 12)
    $root = Join-Path $env:ProgramData ('rasterdock-probe-' + $id)
    # New-Item fails if the unique path already exists. Only this exact directory is ever removed.
    if (Test-Path -LiteralPath $root) { throw 'Unique diagnostic directory already exists.' }
    New-Item -Path $root -ItemType Directory -ErrorAction Stop | Out-Null
    $rootCreated = $true
    $work = Join-Path $root 'work'
    New-Item -Path $work -ItemType Directory -ErrorAction Stop | Out-Null
    Copy-Item -LiteralPath $ProbePath -Destination (Join-Path $root 'probe.exe') -ErrorAction Stop
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'standard-user-child.ps1') -Destination (Join-Path $root 'standard-user-child.ps1') -ErrorAction Stop

    $random = New-Object byte[] 32
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($random)
    $password = ConvertTo-SecureString ('Aa1!' + [BitConverter]::ToString($random).Replace('-', '')) -AsPlainText -Force
    [Array]::Clear($random, 0, $random.Length)
    $account = New-LocalUser -Name $userName -Password $password -Description 'Temporary RasterDock diagnostic account' -ErrorAction Stop
    $userSid = $account.SID
    $usersSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-545')
    $adminsSid = [Security.Principal.SecurityIdentifier]::new('S-1-5-32-544')
    if (-not (@(Get-LocalGroupMember -SID $usersSid -ErrorAction Stop | Where-Object { $_.SID.Value -eq $userSid.Value }).Count)) {
        Add-LocalGroupMember -SID $usersSid -Member $account -ErrorAction Stop
    }
    if (@(Get-LocalGroupMember -SID $adminsSid -ErrorAction Stop | Where-Object { $_.SID.Value -eq $userSid.Value }).Count) {
        throw 'Temporary account is a member of Administrators.'
    }
    Set-OwnedAcl $root $userSid 'ReadAndExecute'
    Set-OwnedAcl $work $userSid 'Modify'

    . (Join-Path $PSScriptRoot 'job-object.ps1')
    $job = [RasterDockProbeJob]::Create()
    $credential = [Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$userName", $password)
    # Paths are generated from a GUID in ProgramData (no whitespace or quotes in generated suffix).
    # Start-Process joins ArgumentList with spaces: quote every path explicitly for powershell.exe.
    $script = Join-Path $root 'standard-user-child.ps1'
    $readyPath = Join-Path $root ('ready-' + [guid]::NewGuid().ToString('N'))
    $argsLine = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{0}" -ProbePath "{1}" -ParentPath "{2}" -WorkPath "{3}" -ReadyPath "{4}"' -f $script, (Join-Path $root 'probe.exe'), $root, $work, $readyPath
    $process = Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" `
        -Credential $credential -ArgumentList $argsLine -WorkingDirectory $work -PassThru `
        -RedirectStandardOutput (Join-Path $work 'child.stdout.txt') `
        -RedirectStandardError (Join-Path $work 'child.stderr.txt') -ErrorAction Stop
    # The wrapper only polls a marker under its read-only parent; it cannot
    # launch probe/fixture before assignment. No parent/controller is assigned.
    $job.Assign($process.Handle)
    $wrapperAssigned = $true
    $marker = [IO.FileStream]::new($readyPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    $marker.Dispose()
    # Read the report while the controlled fixture is still alive, using an
    # opened handle for the membership check rather than acting on a stale PID.
    $reportPath = Join-Path $work 'standard-user-report.json'
    $reportDeadline = [datetime]::UtcNow.AddSeconds(340)
    while (-not [IO.File]::Exists($reportPath)) {
        if ([datetime]::UtcNow -ge $reportDeadline -or $process.HasExited) { throw 'Wrapper did not publish a report in time.' }
        Start-Sleep -Milliseconds 100
    }
    $childReport = $null
    while (-not $childReport) {
        if ([datetime]::UtcNow -ge $reportDeadline) { throw 'Wrapper report could not be read in time.' }
        try { $childReport = Get-Content -LiteralPath $reportPath -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop }
        catch {
            Start-Sleep -Milliseconds 100
        }
    }
    $orphanFixtureId = $childReport.orphan_fixture_pid
    if ($orphanFixtureId -and $orphanFixtureId -match '^\d+$' -and [uint64]$orphanFixtureId -le [uint32]::MaxValue) {
        $fixtureInJob = $job.ContainsPid([uint32]$orphanFixtureId)
    }
    if (-not $fixtureInJob) { throw 'Controlled orphan fixture was not observed in the owned job.' }
    $jobActiveBeforeCleanup = $job.ActiveProcesses()
    if ($jobActiveBeforeCleanup -lt 1) { throw 'Owned job was empty before fixture cleanup.' }
    if (-not $process.WaitForExit(360000)) {
        throw 'Standard-user wrapper exceeded 360 seconds; job cleanup follows.'
    }
    $jobActiveBeforeCleanup = $job.ActiveProcesses()
    if ($jobActiveBeforeCleanup -lt 1) { throw 'No owned job process survived wrapper exit for cleanup.' }
    if ($process.ExitCode -ne 0) { throw "Standard-user wrapper exited with code $($process.ExitCode)." }
    if ($childReport.identity_sid -ne $userSid.Value -or $childReport.not_administrator -ne $true -or $childReport.parent_create_access_denied -ne $true -or
        $childReport.work_create_succeeded -ne $true -or $childReport.probe_token_not_elevated -ne $true) {
        throw 'Missing or failed standard-user, parent ACL, child ACL, or probe token assertion.'
    }
} catch {
    $failure = $_.Exception.Message
} finally {
    if ($job) {
        try {
            if ($null -eq $jobActiveBeforeCleanup) { $jobActiveBeforeCleanup = $job.ActiveProcesses() }
            $job.Terminate()
            $jobTerminated = $true
            $deadline = [datetime]::UtcNow.AddSeconds(15)
            do {
                $jobActiveAfterCleanup = $job.ActiveProcesses()
                if ($jobActiveAfterCleanup -eq 0) { $jobCleanupVerified = $true; break }
                Start-Sleep -Milliseconds 100
            } while ([datetime]::UtcNow -lt $deadline)
            if (-not $jobCleanupVerified) { throw 'Owned job retained active processes after termination.' }
        } catch { $cleanupErrors += "Job cleanup: $($_.Exception.Message)" }
        finally {
            try { $job.Dispose() } catch { $cleanupErrors += "Job handle closure: $($_.Exception.Message)" }
        }
    }
    if ($process) {
        try {
            # If assignment failed, the unreleased wrapper cannot spawn anything.
            # Terminate only the retained wrapper process handle, not a PID lookup.
            if (-not $wrapperAssigned -and -not $process.HasExited) { $process.Kill() }
            if (-not $process.WaitForExit(10000)) {
                throw 'Owned wrapper still running after job cleanup.'
            }
        } catch { $cleanupErrors += "Process cleanup: $($_.Exception.Message)" }
        finally { $process.Dispose() }
    }
    if ($work -and (Test-Path -LiteralPath $work)) {
        foreach ($name in @('probe.json', 'probe.stderr.txt', 'standard-user-report.json', 'child.stdout.txt', 'child.stderr.txt')) {
            try {
                $source = Join-Path $work $name
                if (Test-Path -LiteralPath $source -PathType Leaf) {
                    Copy-Item -LiteralPath $source -Destination (Join-Path $ArtifactDirectory $name) -ErrorAction Stop
                }
            } catch { $cleanupErrors += "Artifact copy ($name): $($_.Exception.Message)" }
        }
    }
    if ($userSid) {
        try {
            # Only a profile bound to the newly created account SID can be removed.
            $profile = Get-CimInstance Win32_UserProfile -Filter "SID='$($userSid.Value)'" -ErrorAction Stop
            if ($profile) { $profile | Remove-CimInstance -ErrorAction Stop }
        } catch { $cleanupErrors += "Temporary profile cleanup: $($_.Exception.Message)" }
    }
    if ($userName -and $userSid) {
        try { Remove-LocalUser -SID $userSid -ErrorAction Stop }
        catch { $cleanupErrors += "Temporary account cleanup: $($_.Exception.Message)" }
    }
    if ($rootCreated -and (Test-Path -LiteralPath $root)) {
        try { Remove-Item -LiteralPath $root -Recurse -Force -ErrorAction Stop }
        catch { $cleanupErrors += "Owned directory cleanup: $($_.Exception.Message)" }
    }
    $bootstrap = [ordered]@{
        success = (-not $failure -and $cleanupErrors.Count -eq 0)
        error = $failure
        cleanup_errors = $cleanupErrors
        job_cleanup_verified = $jobCleanupVerified
        job_terminated = $jobTerminated
        job_active_before_cleanup = $jobActiveBeforeCleanup
        job_active_after_cleanup = $jobActiveAfterCleanup
        orphan_fixture_in_job = $fixtureInJob
        orphan_fixture_pid = $orphanFixtureId
    }
    [IO.File]::WriteAllText((Join-Path $ArtifactDirectory 'bootstrap.json'), ($bootstrap | ConvertTo-Json -Depth 4), $utf8)
}
if ($failure -or $cleanupErrors.Count -gt 0) {
    throw "Windows diagnostic failed: $failure; cleanup: $($cleanupErrors -join '; '). See artifact bootstrap.json."
}
Write-Host 'Standard-user API observation completed; this is not evidence of power-loss durability.'
