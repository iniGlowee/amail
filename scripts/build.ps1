# Build AMail for every supported platform into dist\.
# Usage: powershell -ExecutionPolicy Bypass -File scripts\build.ps1 [-Version 1.0.0]
param([string]$Version = "")
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
if ($Version -eq "") {
    try { $Version = (git describe --tags --always --dirty 2>$null) } catch { }
    if (-not $Version) { $Version = "dev" }
}
New-Item -ItemType Directory -Force dist | Out-Null
function Build($os, $arch, $ext) {
    $out = "dist\amail-$Version-$os-$arch$ext"
    Write-Host "  $out"
    $env:CGO_ENABLED = "0"; $env:GOOS = $os; $env:GOARCH = $arch
    go build -trimpath -ldflags "-s -w -X main.version=$Version" -o $out ./cmd/amail
    if ($LASTEXITCODE -ne 0) { throw "build failed for $os/$arch" }
}
Write-Host "building amail $Version"
Build windows amd64 .exe
$out = "distmailw-$Version-windows-amd64.exe"; Write-Host "  $out (no console, for background tasks)"
$env:CGO_ENABLED = "0"; $env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version" -o $out ./cmd/amail
if ($LASTEXITCODE -ne 0) { throw "build failed for windowsgui" }
Build linux   amd64 ""
Build linux   arm64 ""
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
Get-ChildItem "dist\amail-$Version-*", "dist\amailw-$Version-*" | ForEach-Object { "{0}  {1}" -f (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower(), $_.Name } | Set-Content -Encoding ascii dist\SHA256SUMS
Write-Host "  dist\SHA256SUMS"
Write-Host "done"
