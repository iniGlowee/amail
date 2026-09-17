# Register a scheduled task that runs "amail process" whenever this user logs
# on, next to the node's own "AMail" task.
#
#   powershell -ExecutionPolicy Bypass -File scripts\windows\install-processor-task.ps1 -Config C:\path\processor.json [-Binary C:\path\amail.exe] [-NodeHome C:\path\.amail]
#
# Same behaviour as the node task: this user's logon only, hidden, ends at
# logoff, never wakes the PC, allowed on battery, restarted after a crash.
param([string]$Config = "", [string]$Binary = "", [string]$NodeHome = "")
$ErrorActionPreference = "Stop"
if ($Binary -eq "") { $Binary = Join-Path $env:LOCALAPPDATA "Programs\AMail\amail.exe" }
if (-not (Test-Path $Binary)) { throw "amail.exe not found at $Binary; run install-startup.ps1 first or pass -Binary" }
if ($NodeHome -eq "" -and $env:AMAIL_HOME) { $NodeHome = $env:AMAIL_HOME }
if ($NodeHome -eq "") { $NodeHome = Join-Path $env:APPDATA "AMail" }
if ($Config -eq "") { $Config = Join-Path $NodeHome "processor.json" }
if (-not (Test-Path $Config)) { throw "processor config not found: $Config (create one with: amail process --init --config $Config)" }

Get-ScheduledTask -TaskName "AMail processor" -ErrorAction SilentlyContinue | Stop-ScheduledTask -ErrorAction SilentlyContinue
$action   = New-ScheduledTaskAction -Execute $Binary -Argument "--home `"$NodeHome`" process --config `"$Config`"" -WorkingDirectory (Split-Path $Binary)
$trigger  = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -WakeToRun:$false -StartWhenAvailable:$false -MultipleInstances IgnoreNew -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -Hidden
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName "AMail processor" -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Description "AMail processor: runs handler commands on tagged notes while $env:USERNAME is logged in" -Force | Out-Null
Start-ScheduledTask -TaskName "AMail processor"
Start-Sleep -Seconds 4
$state = (Get-ScheduledTask -TaskName "AMail processor").State
Write-Host "Task 'AMail processor': state $state; config $Config; log $NodeHome\processor.log"
if ($state -ne "Running") { Write-Host "Not running. Last lines of the log:"; Get-Content "$NodeHome\processor.log" -Tail 5 -ErrorAction SilentlyContinue }
