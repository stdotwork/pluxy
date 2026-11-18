; The per-user setup program's hooks (`bundle.windows.nsis.installerHooks`), which Tauri's NSIS
; template (tauri-bundler 2.10.4, installer.nsi) includes and inserts into its Install and
; Uninstall sections. next/app/README.md ("Packages") says what the setup does.
;
; - Before installing and before uninstalling: refused while the app runs from the install folder.
;   The template's own check that follows asks Restart Manager the same question, then closes the
;   app by force (RmShutdown) without asking in silent and passive modes: that skips the app's
;   Quit, which stops its core and tools in order, and ends processes the app did not start. When
;   Restart Manager cannot start a session or answer, these hooks refuse too: they cannot tell
;   whether the app runs.
; - Before uninstalling, once that check passed: what the app itself leaves in the install folder
;   is removed, which the uninstaller does not know (it is written once, by the first version
;   installed, and an update replaces the app's files, never the uninstaller): every `.pluxy-*`
;   entry (an update's staging folder and the parts it set aside), `bundled\` and `pluxy.exe`. A
;   link or junction among them is removed itself, never followed. Unless the uninstaller runs for
;   an update (/UPDATE), the app's own login item goes too: the `Run` value named with the app's
;   identifier and Task Manager's value of that name under `StartupApproved\Run`, only when the
;   `Run` value is exactly this install's command (`"<folder>\<program>.exe" --autostart`).

!define /ifndef ERROR_MORE_DATA 234
!define /ifndef PLUXY_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define /ifndef PLUXY_APPROVED_KEY "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"

; Aborts, saying why and what to do, when the app's program in $INSTDIR is running or Restart
; Manager cannot tell. again: what to run again once the app has quit.
!macro PLUXY_REFUSE_WHILE_RUNNING again
  ; No program there: nothing of this install can be running.
  ${If} ${FileExists} "$INSTDIR\${MAINBINARYNAME}.exe"
    StrCpy $R9 ""
    !insertmacro RestartManager_StartSession $R8
    ${If} $R8 == ""
      StrCpy $R9 "Could not check whether ${PRODUCTNAME} is running from $INSTDIR: Restart Manager did not start a session. Quit ${PRODUCTNAME} if it runs (tray > Quit), and run ${again} again."
    ${Else}
      !insertmacro RestartManager_RegisterFile $R8 "$INSTDIR\${MAINBINARYNAME}.exe"
      ${If} $0 <> 0
        StrCpy $R9 "Could not check whether ${PRODUCTNAME} is running from $INSTDIR: Restart Manager refused the program ($0). Quit ${PRODUCTNAME} if it runs (tray > Quit), and run ${again} again."
      ${Else}
        ; ERROR_SUCCESS: no process uses the program; ERROR_MORE_DATA: some do.
        System::Call 'RSTRTMGR::RmGetList(p R8, *i .r1, *i .r2, p 0, *i .r3) i .r0'
        ${If} $0 = ${ERROR_MORE_DATA}
          StrCpy $R9 "${PRODUCTNAME} is running from $INSTDIR. Quit it (tray > Quit), and run ${again} again."
        ${ElseIf} $0 <> 0
          StrCpy $R9 "Could not check whether ${PRODUCTNAME} is running from $INSTDIR: Restart Manager could not list its processes ($0). Quit ${PRODUCTNAME} if it runs (tray > Quit), and run ${again} again."
        ${EndIf}
      ${EndIf}
      !insertmacro RestartManager_EndSession $R8
    ${EndIf}
    ${If} $R9 != ""
      ${If} ${Silent}
        ; Silent mode shows nothing: the reason goes to the standard output the caller gave
        ; (redirected), or else to the console of the process that started this one.
        System::Call 'kernel32::GetStdHandle(i -11) i .r0'
        ${If} $0 = 0
        ${OrIf} $0 = -1
          System::Call 'kernel32::AttachConsole(i -1) i .r1'
          System::Call 'kernel32::GetStdHandle(i -11) i .r0'
        ${EndIf}
        ${If} $0 <> 0
        ${AndIf} $0 <> -1
          FileWrite $0 "$R9$\r$\n"
        ${EndIf}
      ${EndIf}
      MessageBox MB_OK|MB_ICONSTOP "$R9" /SD IDOK
      SetErrorLevel 2
      Abort "$R9"
    ${EndIf}
  ${EndIf}
!macroend

; Removes path: a link or junction itself, a folder with what is in it, or a file. Nothing when
; path does not exist.
!macro PLUXY_REMOVE path
  System::Call 'kernel32::GetFileAttributesW(ws) i .r0' "${path}"
  ${If} $0 <> -1
    IntOp $1 $0 & 0x400 ; FILE_ATTRIBUTE_REPARSE_POINT
    IntOp $2 $0 & 0x10 ; FILE_ATTRIBUTE_DIRECTORY
    ${If} $1 <> 0
      ${If} $2 <> 0
        RMDir "${path}"
      ${Else}
        Delete "${path}"
      ${EndIf}
    ${ElseIf} $2 <> 0
      RMDir /r "${path}"
    ${Else}
      Delete "${path}"
    ${EndIf}
  ${EndIf}
!macroend

; What the app leaves in $INSTDIR and its login item (see the top of this file).
!macro PLUXY_REMOVE_OWN
  ; Each .pluxy-* entry, the first one found each time (nothing is removed during a listing); at
  ; most 64, so one that cannot be removed does not hold the uninstaller.
  StrCpy $R5 0
  ${Do}
    FindFirst $R7 $R6 "$INSTDIR\.pluxy-*"
    FindClose $R7
    ${If} $R6 == ""
    ${OrIf} $R5 >= 64
      ${Break}
    ${EndIf}
    !insertmacro PLUXY_REMOVE "$INSTDIR\$R6"
    IntOp $R5 $R5 + 1
  ${Loop}
  !insertmacro PLUXY_REMOVE "$INSTDIR\bundled"
  !insertmacro PLUXY_REMOVE "$INSTDIR\pluxy.exe"
  ${If} $UpdateMode <> 1
    ReadRegStr $R6 HKCU "${PLUXY_RUN_KEY}" "${BUNDLEID}"
    ${If} $R6 == '"$INSTDIR\${MAINBINARYNAME}.exe" --autostart'
      DeleteRegValue HKCU "${PLUXY_RUN_KEY}" "${BUNDLEID}"
      DeleteRegValue HKCU "${PLUXY_APPROVED_KEY}" "${BUNDLEID}"
    ${EndIf}
  ${EndIf}
!macroend

!macro NSIS_HOOK_PREINSTALL
  !insertmacro PLUXY_REFUSE_WHILE_RUNNING "the setup"
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  !insertmacro PLUXY_REFUSE_WHILE_RUNNING "the uninstaller"
  !insertmacro PLUXY_REMOVE_OWN
!macroend
