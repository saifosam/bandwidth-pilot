; Bandwidth Pilot installer (NSIS 3). Build with:
;   makensis -DVERSION=0.3.0 -DSRC=../dist/BandwidthPilot -DOUT=../dist/BandwidthPilot-0.3.0-setup.exe installer/bandwidthpilot.nsi
; build.sh and the release workflow do this for you.

Target amd64-unicode
SetCompressor /SOLID lzma
ManifestDPIAware true
RequestExecutionLevel admin

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRC
  !define SRC "..\dist\BandwidthPilot"
!endif
!ifndef OUT
  !define OUT "..\dist\BandwidthPilot-${VERSION}-setup.exe"
!endif

!define APP       "Bandwidth Pilot"
!define EXE       "BandwidthPilot.exe"
!define PUBLISHER "Saif Moataz Osam"
!define URL       "https://github.com/saifosam/bandwidth-pilot"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\BandwidthPilot"
; Microsoft Edge WebView2 Runtime client id (same for every architecture).
!define WV2GUID   "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

!include "MUI2.nsh"
!include "x64.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

Name "${APP}"
OutFile "${OUT}"
InstallDir "$PROGRAMFILES64\${APP}"
InstallDirRegKey HKLM "${UNINSTKEY}" "InstallLocation"
BrandingText "${APP} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APP}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "FileDescription" "${APP} Setup"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "LegalCopyright" "MIT License"

!define MUI_ICON "..\build\bandwidthpilot.ico"
!define MUI_UNICON "..\build\bandwidthpilot.ico"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TEXT "This will install ${APP} ${VERSION}, a per-app network monitor and speed limiter for Windows.$\r$\n$\r$\nIt installs WinDivert, an open-source packet driver, which some antivirus products flag. It is the signed, unmodified WinDivert 2.2.2 release.$\r$\n$\r$\nClick Next to continue."
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APP} now"
!define MUI_FINISHPAGE_LINK "Project page and source code"
!define MUI_FINISHPAGE_LINK_LOCATION "${URL}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "..\LICENSE"
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "${APP} needs 64-bit Windows 10 or 11."
    Abort
  ${EndIf}
  SetRegView 64
FunctionEnd

; Close a running copy so its files can be replaced. Closing it also removes
; all of its traffic shaping, so nothing is left half-applied.
!macro StopApp
  nsExec::Exec 'taskkill /IM "${EXE}" /F'
  Pop $0
  Sleep 800
!macroend

Section "${APP}" SecApp
  SectionIn RO
  !insertmacro StopApp
  SetOutPath "$INSTDIR"
  File "${SRC}\${EXE}"
  File "${SRC}\WinDivert.dll"
  File "${SRC}\WinDivert64.sys"
  File "${SRC}\WinDivert-LICENSE.txt"
  File "${SRC}\README.txt"
  File /oname=LICENSE.txt "..\LICENSE"

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  ; Settings > Apps entry
  WriteRegStr   HKLM "${UNINSTKEY}" "DisplayName"          "${APP}"
  WriteRegStr   HKLM "${UNINSTKEY}" "DisplayVersion"       "${VERSION}"
  WriteRegStr   HKLM "${UNINSTKEY}" "Publisher"            "${PUBLISHER}"
  WriteRegStr   HKLM "${UNINSTKEY}" "URLInfoAbout"         "${URL}"
  WriteRegStr   HKLM "${UNINSTKEY}" "HelpLink"             "${URL}/issues"
  WriteRegStr   HKLM "${UNINSTKEY}" "DisplayIcon"          "$INSTDIR\${EXE},0"
  WriteRegStr   HKLM "${UNINSTKEY}" "InstallLocation"      "$INSTDIR"
  WriteRegStr   HKLM "${UNINSTKEY}" "UninstallString"      '"$INSTDIR\Uninstall.exe"'
  WriteRegStr   HKLM "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNINSTKEY}" "EstimatedSize" "$0"

  ; The window needs the WebView2 runtime (part of Windows 11 and most
  ; up-to-date Windows 10 installs).
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WV2GUID}" "pv"
  ${If} $0 == ""
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WV2GUID}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    MessageBox MB_YESNO|MB_ICONINFORMATION "${APP} needs the Microsoft Edge WebView2 Runtime, which is not installed.$\r$\n$\r$\nOpen Microsoft's download page now?" /SD IDNO IDNO wv2done
      ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/#download"
    wv2done:
  ${EndIf}
SectionEnd

Section "Start menu shortcut" SecMenu
  SetShellVarContext all
  CreateShortcut "$SMPROGRAMS\${APP}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\${EXE}" 0
SectionEnd

Section /o "Desktop shortcut" SecDesktop
  SetShellVarContext all
  CreateShortcut "$DESKTOP\${APP}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\${EXE}" 0
SectionEnd

Section "Start with Windows" SecStartup
  nsExec::Exec '"$INSTDIR\${EXE}" --enable-startup'
  Pop $0
SectionEnd

!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecApp}     "The app and its packet driver."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecMenu}    "Add ${APP} to the Start menu."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecDesktop} "Add a shortcut to the desktop."
  !insertmacro MUI_DESCRIPTION_TEXT ${SecStartup} "Start in the notification area when you sign in, so your limits are always on. You can change this later in Settings."
!insertmacro MUI_FUNCTION_DESCRIPTION_END

Function un.onInit
  SetRegView 64
FunctionEnd

Section "Uninstall"
  !insertmacro StopApp
  ; Remove the sign-in task before the program it points to.
  nsExec::Exec '"$INSTDIR\${EXE}" --disable-startup'
  Pop $0

  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\WinDivert.dll"
  Delete "$INSTDIR\WinDivert64.sys"
  Delete "$INSTDIR\WinDivert-LICENSE.txt"
  Delete "$INSTDIR\README.txt"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  SetShellVarContext all
  Delete "$SMPROGRAMS\${APP}.lnk"
  Delete "$DESKTOP\${APP}.lnk"
  DeleteRegKey HKLM "${UNINSTKEY}"

  ; Rules, usage history and known apps live in the user's AppData.
  SetShellVarContext current
  MessageBox MB_YESNO|MB_ICONQUESTION "Also delete your rules and usage history?" /SD IDNO IDNO keepdata
    RMDir /r "$APPDATA\${APP}"
    RMDir /r "$LOCALAPPDATA\${APP}"
  keepdata:
SectionEnd
