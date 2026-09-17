# Install amail.exe for this user and register a scheduled task that runs
# "amail run" whenever this user logs on.
#
#   powershell -ExecutionPolicy Bypass -File scripts\windows\install-startup.ps1 [-Binary C:\path\amail.exe]
#
# What it does:
#   - copies the exe to %LOCALAPPDATA%\Programs\AMail\amail.exe (a local disk,
#     so the task does not depend on a network or removable drive);
#   - registers task "AMail": starts at logon of this user only, runs in your
#     session, hidden, ends when you log off, never wakes the computer, is
#     allowed on battery, restarts the node a minute after a crash;
#   - passes the node home explicitly (--home %APPDATA%\AMail) so the task
#     never depends on environment variables;
#   - re-running with a new -Binary replaces the exe and the task (upgrade).
param([string]$Binary = "", [string]$NodeHome = "")
$ErrorActionPreference = "Stop"
if ($Binary -eq "") {
    $cmd = Get-Command amail.exe -ErrorAction SilentlyContinue
    if ($cmd) { $Binary = $cmd.Source }
    else {
        $local = Join-Path $PSScriptRoot "..\..\dist"
        $cand = Get-ChildItem -Path $local -Filter "amail-*-windows-amd64.exe" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1
        if ($cand) { $Binary = $cand.FullName }
    }
}
if (-not $Binary -or -not (Test-Path $Binary)) { throw "amail.exe not found; pass -Binary <path>" }
$Binary = (Resolve-Path $Binary).Path

$installDir = Join-Path $env:LOCALAPPDATA "Programs\AMail"
$exe = Join-Path $installDir "amail.exe"
# Node home: -Home, else $AMAIL_HOME, else %APPDATA%\AMail (the default `amail init` uses).
$nodeHome = $NodeHome
if ($nodeHome -eq "" -and $env:AMAIL_HOME) { $nodeHome = $env:AMAIL_HOME }
if ($nodeHome -eq "") { $nodeHome = Join-Path $env:APPDATA "AMail" }
if (-not (Test-Path (Join-Path $nodeHome "config.json"))) { throw "no config.json in $nodeHome; run 'amail init' first or pass -NodeHome <folder>" }
New-Item -ItemType Directory -Force $installDir | Out-Null

# Stop any running node (task or hand-started) so the exe can be replaced
# and two copies do not fight over the mailbox.
Get-ScheduledTask -TaskName "AMail" -ErrorAction SilentlyContinue | Stop-ScheduledTask -ErrorAction SilentlyContinue
Get-Process | Where-Object { $_.ProcessName -like "amail*" } | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 800
Copy-Item -Path $Binary -Destination $exe -Force

# The task runs the GUI-subsystem build (amailw.exe: same program, no console
# window) when one sits next to the given binary, e.g. dist\amailw-<ver>-windows-amd64.exe.
$taskExe = $exe
$guiCandidate = Join-Path (Split-Path $Binary) ((Split-Path $Binary -Leaf) -replace '^amail-', 'amailw-')
if (Test-Path $guiCandidate) {
    Copy-Item -Path $guiCandidate -Destination (Join-Path $installDir "amailw.exe") -Force
    $taskExe = Join-Path $installDir "amailw.exe"
} elseif (Test-Path (Join-Path $installDir "amailw.exe")) {
    $taskExe = Join-Path $installDir "amailw.exe"
}

$action   = New-ScheduledTaskAction -Execute $taskExe -Argument "--home `"$nodeHome`" run" -WorkingDirectory $installDir
$trigger  = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -WakeToRun:$false -StartWhenAvailable:$false `
    -MultipleInstances IgnoreNew `
    -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -Hidden
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName "AMail" -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Description "AMail (Armas Mail) node: runs while $env:USERNAME is logged in" -Force | Out-Null
Start-ScheduledTask -TaskName "AMail"
Start-Sleep -Seconds 4
$info = Get-ScheduledTaskInfo -TaskName "AMail"
$state = (Get-ScheduledTask -TaskName "AMail").State
Write-Host "Installed $exe"
Write-Host "Task 'AMail': state $state, last result $($info.LastTaskResult) (0 or 267009 = running fine); runs $taskExe at logon of $env:USERNAME, hidden, no wake."
Write-Host "Log: $nodeHome\amail.log    Check: `"$exe`" status    Remove: scripts\windows\uninstall-startup.ps1"
if ($state -ne "Running") { Write-Host "The node is not running. Last lines of the log:"; Get-Content "$nodeHome\amail.log" -Tail 5 }
