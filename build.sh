#!/usr/bin/env bash
# Builds the Windows app from Linux or macOS (Go cross-compiles; no Windows needed).
# Output: dist/BandwidthPilot/  (BandwidthPilot.exe + the WinDivert driver files)
set -euo pipefail
cd "$(dirname "$0")"

echo "== interface"
(cd ui && npm install --no-audit --no-fund --silent && npm run build)

# Icon + manifest + version info. The compiled .syso is checked in, so this
# step only matters if you changed something in build/.
if command -v x86_64-w64-mingw32-windres >/dev/null; then
  echo "== resources"
  x86_64-w64-mingw32-windres --preprocessor=gcc --preprocessor-arg=-E --preprocessor-arg=-xc \
    --preprocessor-arg=-DRC_INVOKED -I build build/bandwidthpilot.rc -O coff \
    -o cmd/bandwidthpilot/rsrc_windows_amd64.syso
fi

echo "== tests"
go test ./...

echo "== BandwidthPilot.exe"
mkdir -p dist/BandwidthPilot
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" \
  -o dist/BandwidthPilot/BandwidthPilot.exe ./cmd/bandwidthpilot
cp third_party/windivert/WinDivert.dll third_party/windivert/WinDivert64.sys dist/BandwidthPilot/
cp third_party/windivert/LICENSE.txt dist/BandwidthPilot/WinDivert-LICENSE.txt
echo "done: dist/BandwidthPilot"
