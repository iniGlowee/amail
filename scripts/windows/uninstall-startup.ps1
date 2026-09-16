# Remove the "AMail" scheduled task created by install-startup.ps1.
$ErrorActionPreference = "Stop"
$t = Get-ScheduledTask -TaskName "AMail" -ErrorAction SilentlyContinue
if (-not $t) { Write-Host "No 'AMail' scheduled task found."; exit 0 }
Stop-ScheduledTask -TaskName "AMail" -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName "AMail" -Confirm:$false
Get-Process amail -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Write-Host "Removed the 'AMail' scheduled task. Config and mailbox were left in place."
