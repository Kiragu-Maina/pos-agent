; AlkenaCode POS — Windows installer.
;
; Per-user install (no admin rights needed, which suits shop PCs). Installs the
; agent, adds Start Menu + Desktop shortcuts and an auto-start-on-login entry,
; registers an uninstaller in Add/Remove Programs, and offers to launch the app.
;
; Build (from the repo root, on Linux or Windows):
;   makensis -DVERSION=1.0.0 build/windows/pos.nsi
; Output: dist/AlkenaCode-POS-Setup.exe
;
; Paths to source files are relative to the repo root (where makensis is run).

Unicode true
!include "MUI2.nsh"

!ifndef VERSION
  !define VERSION "1.0.0"
!endif
!define APPNAME  "AlkenaCode POS"
!define COMPANY  "AlkenaCode Creations"
!define EXE      "pos.exe"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\AlkenaCodePOS"

Name "${APPNAME}"
OutFile "dist/AlkenaCode-POS-Setup.exe"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\${APPNAME}"
InstallDirRegKey HKCU "Software\${APPNAME}" "InstallDir"
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName"     "${APPNAME}"
VIAddVersionKey "CompanyName"     "${COMPANY}"
VIAddVersionKey "FileDescription" "${APPNAME} Setup"
VIAddVersionKey "FileVersion"     "${VERSION}.0"
VIAddVersionKey "LegalCopyright"  "Copyright (C) 2026 ${COMPANY}"

!define MUI_ICON   "build/windows/pos.ico"
!define MUI_UNICON "build/windows/pos.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Open ${APPNAME} now"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "Install"
  SetOutPath "$INSTDIR"          ; also sets the shortcuts' working directory
  ; Stop a running copy so its file can be replaced on upgrade.
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  File "dist/${EXE}"
  File "build/windows/pos.ico"

  ; Shortcuts: Start Menu, Desktop, and Startup (launch on login — a till should
  ; come back by itself after a reboot or power cut).
  CreateShortCut "$SMPROGRAMS\${APPNAME}.lnk" "$INSTDIR\${EXE}" "" "$INSTDIR\pos.ico"
  CreateShortCut "$DESKTOP\${APPNAME}.lnk"    "$INSTDIR\${EXE}" "" "$INSTDIR\pos.ico"
  CreateShortCut "$SMSTARTUP\${APPNAME}.lnk"  "$INSTDIR\${EXE}" "" "$INSTDIR\pos.ico"

  ; Uninstaller + Add/Remove Programs entry (per-user, so HKCU).
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr   HKCU "Software\${APPNAME}" "InstallDir" "$INSTDIR"
  WriteRegStr   HKCU "${UNINSTKEY}" "DisplayName"     "${APPNAME}"
  WriteRegStr   HKCU "${UNINSTKEY}" "DisplayVersion"  "${VERSION}"
  WriteRegStr   HKCU "${UNINSTKEY}" "Publisher"       "${COMPANY}"
  WriteRegStr   HKCU "${UNINSTKEY}" "DisplayIcon"     "$INSTDIR\pos.ico"
  WriteRegStr   HKCU "${UNINSTKEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /IM ${EXE} /F'
  Delete "$SMPROGRAMS\${APPNAME}.lnk"
  Delete "$DESKTOP\${APPNAME}.lnk"
  Delete "$SMSTARTUP\${APPNAME}.lnk"
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\pos.ico"
  Delete "$INSTDIR\uninstall.exe"
  ; Leave $INSTDIR\data (the shop's sales) in place; RMDir only removes it if empty.
  RMDir "$INSTDIR"
  DeleteRegKey HKCU "${UNINSTKEY}"
  DeleteRegKey HKCU "Software\${APPNAME}"
SectionEnd
