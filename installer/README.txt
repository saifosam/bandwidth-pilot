Bandwidth Pilot {{VERSION}}
======================

Per-app network monitor, speed limiter, blocker and prioritiser for Windows 10/11 (64-bit).

Start
-----
1. Keep all files in this folder together.
2. Double-click BandwidthPilot.exe and accept the administrator prompt.

Use
---
- Click an app to see its traffic and set rules on the right.
- Right-click an app for quick presets (block, limits, priority).
- Click your PC's name at the top of the list to set a total bandwidth cap.
  Priorities work inside that cap.
- Closing Bandwidth Pilot removes all limits immediately.

If something goes wrong
-----------------------
- "Windows protected your PC": the app is not code-signed. Choose More info > Run anyway.
- Antivirus may flag WinDivert64.sys / WinDivert.dll. That is the open-source packet
  driver Bandwidth Pilot uses (https://reqrypt.org/windivert.html). Allow it.
- To try the interface without touching your network: BandwidthPilot.exe --demo
- For a diagnostic report: BandwidthPilot.exe --selftest
  It writes %AppData%\Bandwidth Pilot\selftest.txt. The log is %AppData%\Bandwidth Pilot\bandwidthpilot.log.
