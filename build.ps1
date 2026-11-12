# Builds Bandwidth Pilot on Windows. Needs Go (https://go.dev/dl). Node.js is only
# needed if you changed the interface in ui/ (pass -UI to rebuild it).
# Output: dist\BandwidthPilot\  (BandwidthPilot.exe + the WinDivert driver files)
param([switch]$UI)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

if ($UI) {
    Write-Host "== interface"
    Push-Location ui
    npm install --no-audit --no-fund
    npm run build
    Pop-Location
}

Write-Host "== tests"
go test ./...

Write-Host "== BandwidthPilot.exe"
New-Item -ItemType Directory -Force -Path dist\BandwidthPilot | Out-Null
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist\BandwidthPilot\BandwidthPilot.exe .\cmd\bandwidthpilot
Copy-Item third_party\windivert\WinDivert.dll, third_party\windivert\WinDivert64.sys dist\BandwidthPilot\ -Force
Copy-Item third_party\windivert\LICENSE.txt dist\BandwidthPilot\WinDivert-LICENSE.txt -Force
Write-Host "done: dist\BandwidthPilot"
