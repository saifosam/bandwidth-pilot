# Bandwidth Pilot

<img src="build/bandwidthpilot.png" width="96" alt="Bandwidth Pilot logo">

A per-app network monitor and traffic shaper for Windows: see what every app is
downloading and uploading, cap its speed, block it, give it a daily data quota,
or put it ahead of everything else.

Written in Go. One 7 MB executable, no installer, no runtime to install.

## Features

- **Live activity** for every app: download and upload speed, a one-minute
  trace, open connections with the website names they belong to.
- **Grouped like Task Manager**: Apps (have a window), Background processes
  and Windows processes, each collapsible with group totals.
- **Speed limits** per app, separately for download and upload.
- **Blocker**: cut an app off from the network, in either direction.
- **Priorities** (Low / Normal / High / Critical) inside a **total bandwidth
  cap**, so a call or a game stays smooth while a download runs.
- **Daily quotas**: an app is blocked until midnight once it has used its allowance.
- **Schedules**: any rule can apply only on chosen days and times, including
  overnight windows such as 22:00 to 06:00.
- **Per-process rules**: split an app such as Chrome into one row per running
  process and limit just one of them.
- **Website rules**: block or limit a site (and its subdomains), an IP address
  or a range, for every app at once. Presets for YouTube, Netflix, TikTok,
  Instagram and Facebook, Twitch, Steam downloads and Windows Update.
- **Connection log**: every new connection with time, app, destination name,
  port and whether it was allowed or blocked.
- **Ask before new apps connect**: firewall-style prompts. A new app's
  traffic is held until you allow or block it, and the answer is remembered.
- **Notification area icon**: closing the window keeps your limits running;
  live speeds in the tooltip; right-click to pause all rules or quit.
- **Start with Windows** through Task Scheduler, so it starts elevated at
  sign-in without a permission prompt.
- **Usage history**: 30 days of per-app and per-day totals.

## Installing

Download from [Releases](https://github.com/saifosam/bandwidth-pilot/releases/latest):

- **`BandwidthPilot-x.y.z-setup.exe`** (recommended): installs to Program
  Files, adds a Start menu entry and an uninstaller, and can start Bandwidth
  Pilot with Windows.
- **`BandwidthPilot-x.y.z-portable.zip`**: unzip anywhere and run
  `BandwidthPilot.exe`. Keep `WinDivert.dll` and `WinDivert64.sys` next to it.

Accept the administrator prompt when it starts; capturing packets needs it.

Requirements: 64-bit Windows 10 or 11. The window uses the Edge WebView2
runtime, which is part of Windows 11 and of most Windows 10 installs.

Things to know:

- Windows SmartScreen warns about the exe because it is not code-signed.
- Some antivirus products flag WinDivert, the open-source packet driver, as a
  "hack tool" because any program can use it to intercept traffic. It is the
  signed, unmodified 2.2.2 release from <https://reqrypt.org/windivert.html>.
- Closing Bandwidth Pilot removes all shaping at once. Nothing stays installed in the
  network stack.
- `BandwidthPilot.exe --demo` runs on simulated traffic and touches nothing.
- `BandwidthPilot.exe --selftest` writes a diagnostic report to
  `%AppData%\Bandwidth Pilot\selftest.txt`. The log is `%AppData%\Bandwidth Pilot\bandwidthpilot.log`.

## How it works

```
 WinDivert driver ──packets──▶ engine ──"what do I do with this?"──▶ shaper
        ▲                        │                                     │
        └──────re-inject─────────┴◀────────pass / hold / drop──────────┘
                                 │
                             core service ◀──JSON calls──▶ interface (WebView2)
```

**Engine** (`internal/engine`). Two WinDivert handles are open:

- a *flow* handle reports each new connection with the id of the process that
  owns it. That is how a packet is matched to an app. Connections older than
  Bandwidth Pilot are matched through the Windows TCP/UDP owner tables.
- a *network* handle sees every TCP and UDP packet. While no rule is active it
  only **sniffs**: Windows delivers packets as usual and Bandwidth Pilot counts copies,
  so it cannot slow anything down. Once a limit, block, quota or cap is active
  it switches to **intercepting**: each packet is diverted, the shaper decides,
  and survivors are re-injected.

**Shaper** (`internal/shaper`). Pure Go with no OS dependencies, covered by unit
tests. Every app has a token bucket per direction; there is one more pair of
buckets for the total cap. A packet that cannot be paid for is queued. A
scheduler runs every 2 ms and releases queued packets in priority order,
round-robin inside a priority class. Queues are short (0.6 s of traffic), so a
sender that outruns its limit loses packets and TCP slows itself down. A low
priority app still gets one packet every 250 ms, so it is never starved to zero.

**Website rules** (`internal/sites`). The engine reads every DNS answer that
passes by and remembers which name each IP address was looked up as. A packet
to an address covered by a website rule is shaped by that rule's own token
bucket, shared by every app; the app's own blocks and quota still apply.

**Core** (`internal/core`). Owns the shaper and the engine, samples counters
once a second, switches scheduled rules on and off, keeps the connection log,
holds new apps that need approval, and stores rules, known apps and usage as
JSON in `%AppData%\Bandwidth Pilot`.

**Interface** (`ui/`). Preact, bundled by esbuild into a single HTML file that
is embedded in the executable. The Windows shell is a Win32 window hosting
WebView2 with the backend bound in as a JavaScript function, so no port is
opened.

### Priorities need a cap

Bandwidth Pilot can only reorder traffic at a point where traffic is waiting, and
packets only wait where there is a bottleneck it controls. So priorities take
effect inside the total bandwidth cap (select your PC at the top of the list).
Set the cap slightly below your real line speed; "Set to 90% of the fastest
speed seen" does that for you.

## Building

The compiled interface, the Windows resources and all Go dependencies are
checked in, so Go is the only requirement.

```powershell
# Windows
.\build.ps1          # add -UI if you changed ui/ (needs Node.js)
```

```bash
# Linux or macOS, cross-compiling for Windows.
# With NSIS installed (apt install nsis) this also builds the installer,
# the portable zip and SHA256SUMS.txt in dist/.
./build.sh
```

### Releasing

1. Update `version` in `cmd/bandwidthpilot/main.go` and add a section to `CHANGELOG.md`.
2. Commit and push to `main`. The Release workflow sees a version with no
   release yet, builds everything, creates the `vX.Y.Z` tag and publishes
   the release. (Pushing a `vX.Y.Z` tag yourself works too.)

Development without Windows:

```bash
go run ./cmd/bandwidthpilot --demo --serve    # interface in your browser, simulated traffic
go test ./...
```

## macOS

Everything except the packet engine is portable: the shaper, core, interface
and demo engine build and run on macOS today (`go run ./cmd/bandwidthpilot` opens the
interface in a browser on simulated traffic). A real macOS version needs two
pieces:

1. an engine implementing `engine.Engine` (`internal/engine/engine.go`), built
   on `pf` + `dummynet` or a Network Extension;
2. a `shell_darwin.go` that hosts the same page in a `WKWebView`.

## Limits of this version

- The packet engine has unit tests and has been exercised against the Windows
  API surface under Wine, but version 0.1 has not yet had long soak time on
  real Windows machines. Treat it as a first release.
- All traffic passes through one user-mode thread while rules are active. That
  is comfortable for home connections; it is not built for multi-gigabit links.
- A VPN shows traffic twice: once for the app, once for the VPN's tunnel process.
- Windows services share `svchost.exe`, so they appear as one app.
- IPv4 fragments and protocols other than TCP and UDP are passed through
  untouched and uncounted.
- Website rules match by name only when DNS is visible. Browsers using
  "secure DNS" (DNS over HTTPS) hide it; use the site's IP range instead.
- Per-process rules last until that process exits (process ids are not reused
  safely across runs).

## Layout

```
cmd/bandwidthpilot/        entry point, window shell, localhost server for --serve
internal/shaper/     token buckets, priority scheduler, quotas (+ tests)
internal/engine/     WinDivert engine, process and icon lookup, demo engine
internal/sites/      website rules, DNS answer parser (+ tests)
internal/core/       service, rule and usage stores, prompts, log, RPC (+ tests)
internal/ui/         embedded build of the interface
ui/                  interface source (Preact)
build/               icon, manifest, version resource
installer/           NSIS installer script
.github/workflows/   CI and the release workflow
third_party/         WinDivert 2.2.2 binaries and licence
```

## Licences

Bandwidth Pilot is MIT licensed (see `LICENSE`). WinDivert is LGPLv3 / GPLv2
(`third_party/windivert/LICENSE.txt`); go-webview2 and go-winloader are MIT;
golang.org/x/sys is BSD-3-Clause.
