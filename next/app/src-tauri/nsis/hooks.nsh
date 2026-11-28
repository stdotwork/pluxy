; The per-user setup program's hooks (`bundle.windows.nsis.installerHooks`), which Tauri's NSIS
; template (tauri-bundler 2.10.4, installer.nsi) includes and inserts into its Install and
; Uninstall sections.
;
; - Before installing and before uninstalling: refused while the app runs from the install folder.
;   The template's own check that follows asks Restart Manager whether the app's program is in
;   use, then closes the app by force (RmShutdown) without asking in silent and passive modes:
;   that skips the app's Quit, which stops its core and tools in order, and ends processes the app
;   did not start. These hooks ask Restart Manager about every program in the folder the app may
;   run from: the app's program, `pluxy.exe` and a program an interrupted update left aside
;   (`.pluxy-old-*.exe`). When Restart Manager cannot start a session or answer, they refuse too:
;   they cannot tell whether the app runs.
;   shortcut: nothing holds the app from starting between this check and the template's own one
;   right after it, which closes an app started in that moment by force in silent and passive
;   modes (the uninstall then goes on as usual). Upgrade with a lock the app takes as it starts,
;   if that moment matters.
; - Before uninstalling, once that check passed and before the template's own deletes: what the
;   app itself leaves in the install folder is removed, which the uninstaller does not know (it is
;   written once, by the first version installed, and an update replaces the app's files, never
;   the uninstaller): every `.pluxy-*` entry (an update's staging folder and the parts it set
;   aside), `bundled\` and `pluxy.exe`. Removed first, so the template's removal of the folders it
;   knows finds them gone and never asks Windows to delete one at the next restart (its
;   `RMDir /REBOOTOK` of a folder that still holds a file would). Nothing is removed through a
;   link: each entry's attributes are read before it is removed, and a link or junction is
;   removed itself and never entered (NSIS's own `RMDir /r` enters a junction and empties what it
;   points to). The walk enters real folders at most 32 deep (PLUXY_DEEPEST) and leaves anything
;   deeper where it is. Unless the uninstaller runs for an update (/UPDATE), the app's own
;   login item goes too: the `Run` value named with the app's identifier and Task Manager's value
;   of that name under `StartupApproved\Run`, only when the `Run` value is exactly this install's
;   command (`"<folder>\<program>.exe" --autostart`).

!define /ifndef ERROR_MORE_DATA 234
!define /ifndef PLUXY_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define /ifndef PLUXY_APPROVED_KEY "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run"
; How deep the removal goes into real folders (it never enters a link): `bundled\` is far
; shallower.
!define /ifndef PLUXY_DEEPEST 32

; Registers path with the Restart Manager session handle when the file exists. Sets $R9 to why
; the check cannot go on when Restart Manager refuses it.
!macro PLUXY_REGISTER handle path
  ${If} ${FileExists} "${path}"
  ${AndIf} $R9 == ""
    !insertmacro RestartManager_RegisterFile ${handle} "${path}"
    ${If} $0 <> 0
      StrCpy $R9 "Could not check whether ${PRODUCTNAME} is running from $INSTDIR: Restart Manager refused ${path} ($0)."
    ${EndIf}
  ${EndIf}
!macroend

; Aborts, saying why and what to do, when a program of the app's in $INSTDIR is running or
; Restart Manager cannot tell. again: what to run again once the app has quit.
!macro PLUXY_REFUSE_WHILE_RUNNING again
  StrCpy $R9 ""
  ; No program there: nothing of this install can be running.
  ${If} ${FileExists} "$INSTDIR\${MAINBINARYNAME}.exe"
  ${OrIf} ${FileExists} "$INSTDIR\pluxy.exe"
  ${OrIf} ${FileExists} "$INSTDIR\.pluxy-old-*.exe"
    !insertmacro RestartManager_StartSession $R8
    ${If} $R8 == ""
      StrCpy $R9 "Could not check whether ${PRODUCTNAME} is running from $INSTDIR: Restart Manager did not start a session."
    ${Else}
      !insertmacro PLUXY_REGISTER $R8 "$INSTDIR\${MAINBINARYNAME}.exe"
      !insertmacro PLUXY_REGISTER $R8 "$INSTDIR\pluxy.exe"
      ; Each program an interrupted update set aside, at most 64.
      StrCpy $R5 0
      FindFirst $R7 $R6 "$INSTDIR\.pluxy-old-*.exe"
      ${DoWhile} $R6 != ""
        ${If} $R5 >= 64
          ${Break}
        ${EndIf}
        !insertmacro PLUXY_REGISTER $R8 "$INSTDIR\$R6"
        IntOp $R5 $R5 + 1
        FindNext $R7 $R6
      ${Loop}
      FindClose $R7
      ${If} $R9 == ""
        ; ERROR_SUCCESS: no process uses them; ERROR_MORE_DATA: some do.
        System::Call 'RSTRTMGR::RmGetList(p R8, *i .r1, *i .r2, p 0, *i .r3) i .r0'
        ${If} $0 = ${ERROR_MORE_DATA}
          StrCpy $R9 "${PRODUCTNAME} is running from $INSTDIR."
        ${ElseIf} $0 <> 0
          StrCpy $R9 "Could not check whether ${PRODUCTNAME} is running from $INSTDIR: Restart Manager could not list its processes ($0)."
        ${EndIf}
      ${EndIf}
      !insertmacro RestartManager_EndSession $R8
    ${EndIf}
  ${EndIf}
  ${If} $R9 != ""
    StrCpy $R9 "$R9 Quit ${PRODUCTNAME} (tray > Quit), and run ${again} again."
    ${If} ${Silent}
    ${OrIf} $PassiveMode = 1
      ; No dialog: the reason goes to the standard output the caller gave (redirected), or else
      ; to the console of the process that started this one. Passive mode also shows it in its
      ; progress window (Abort's message).
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
    ${Else}
      MessageBox MB_OK|MB_ICONSTOP "$R9"
    ${EndIf}
    SetErrorLevel 2
    Abort "$R9"
  ${EndIf}
!macroend

Var PluxyDepth

; Removes the path on the stack: a link or junction itself, a folder with what is in it (each
; entry the same way, so no link inside is entered either), or a file. Nothing when the path does
; not exist. Below PLUXY_DEEPEST real folders it stops descending; what is left there stays.
Function un.PluxyRemove
  Exch $R0
  Push $R1
  Push $R2
  Push $R3
  Push $0
  Push $1
  ; shortcut: the attributes are read here and the folder is listed below by name, so a process
  ; of the same user could swap a folder for a junction in between and have the walk enter it. It
  ; gains nothing it could not delete itself. Upgrade by opening the folder once
  ; (FILE_FLAG_OPEN_REPARSE_POINT), checking that handle and listing through it.
  System::Call 'kernel32::GetFileAttributesW(ws) i .r0' "$R0"
  ${If} $0 <> -1
    IntOp $1 $0 & 0x400 ; FILE_ATTRIBUTE_REPARSE_POINT
    IntOp $0 $0 & 0x10 ; FILE_ATTRIBUTE_DIRECTORY
    ${If} $1 <> 0
      ; The link itself: RemoveDirectory and DeleteFile remove a link, not what it points to.
      ${If} $0 <> 0
        RMDir "$R0"
      ${Else}
        Delete "$R0"
      ${EndIf}
    ${ElseIf} $0 <> 0
      ${If} $PluxyDepth < ${PLUXY_DEEPEST}
        IntOp $PluxyDepth $PluxyDepth + 1
        FindFirst $R1 $R2 "$R0\*"
        ${DoWhile} $R2 != ""
          ${If} $R2 != "."
          ${AndIf} $R2 != ".."
            Push "$R0\$R2"
            Call un.PluxyRemove
          ${EndIf}
          FindNext $R1 $R2
        ${Loop}
        FindClose $R1
        IntOp $PluxyDepth $PluxyDepth - 1
      ${EndIf}
      RMDir "$R0"
    ${Else}
      Delete "$R0"
    ${EndIf}
  ${EndIf}
  Pop $1
  Pop $0
  Pop $R3
  Pop $R2
  Pop $R1
  Pop $R0
FunctionEnd

; What the app leaves in $INSTDIR, and its login item (see the top of this file). The template
; removes the folder once it has removed its own files.
!macro PLUXY_REMOVE_OWN
  StrCpy $PluxyDepth 0
  ; Each .pluxy-* entry, removed as the listing goes, as NSIS's own RMDir /r does; one that cannot
  ; be removed is passed over.
  FindFirst $R7 $R6 "$INSTDIR\.pluxy-*"
  ${DoWhile} $R6 != ""
    Push "$INSTDIR\$R6"
    Call un.PluxyRemove
    FindNext $R7 $R6
  ${Loop}
  FindClose $R7
  Push "$INSTDIR\bundled"
  Call un.PluxyRemove
  Push "$INSTDIR\pluxy.exe"
  Call un.PluxyRemove
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
