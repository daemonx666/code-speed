; code-speed's Windows installer: a per-user install (no admin prompt)
; into %LOCALAPPDATA%\Programs\code-speed, a Start menu shortcut, an
; optional start at login, and an uninstaller in Apps & features.
;
;   makensis -DVERSION=0.1.0 -DARCH=amd64 -DSRC=<folder with code-speed.exe> -DOUT=<setup.exe> installer.nsi

Unicode true
!include "MUI2.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif

Name "code-speed"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\code-speed"
RequestExecutionLevel user
SetCompressor /SOLID lzma
VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "code-speed"
VIAddVersionKey "ProductVersion" "${VERSION}.0"
VIAddVersionKey "CompanyName" "code-speed"
VIAddVersionKey "FileDescription" "code-speed installer"
VIAddVersionKey "FileVersion" "${VERSION}.0"
VIAddVersionKey "LegalCopyright" "MIT License"

!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\code-speed"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${SRC}\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\code-speed.exe"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

Section "code-speed" SecMain
  SectionIn RO
  ; a running copy holds the exe
  nsExec::Exec 'taskkill /IM code-speed.exe /F'
  SetOutPath "$INSTDIR"
  File "${SRC}\code-speed.exe"
  File "${SRC}\LICENSE"
  File "${SRC}\THIRD_PARTY_NOTICES.md"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\code-speed.lnk" "$INSTDIR\code-speed.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "code-speed"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "code-speed"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\code-speed.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
SectionEnd

Section "Start at login" SecAutostart
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "code-speed" '"$INSTDIR\code-speed.exe"'
SectionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /IM code-speed.exe /F'
  Delete "$INSTDIR\code-speed.exe"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\code-speed.lnk"
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "code-speed"
  DeleteRegKey HKCU "${UNINSTKEY}"
  ; WebView2's user data, which Wails keeps under the exe's name
  RMDir /r "$APPDATA\code-speed.exe"
SectionEnd
