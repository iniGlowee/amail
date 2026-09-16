# Register a scheduled task that runs "amail run" at logon for the current user.
# Usage: powershell -ExecutionPolicy Bypass -File scripts\windows\install-startup.ps1 [-Binary C:\path\amail.exe]
param([string]$Binary = "")
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
$action  = New-ScheduledTaskAction -Execute $Binary -Argument "run" -WorkingDirectory (Split-Path $Binary)
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -Hidden -StartWhenAvailable
Register-ScheduledTask -TaskName "AMail" -Action $action -Trigger $trigger -Settings $settings -Description "AMail (Armas Mail) node" -Force | Out-Null
Start-ScheduledTask -TaskName "AMail"
Write-Host "Scheduled task 'AMail' registered and started ($Binary run)."
Write-Host "Log: $env:APPDATA\AMail\amail.log   Status: amail status   Remove: scripts\windows\uninstall-startup.ps1"
