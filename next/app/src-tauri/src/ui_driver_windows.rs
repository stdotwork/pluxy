//! The UI driver on Windows: the dialogs are read on the main thread (`dialog_windows::inspect`);
//! keys go in with `SendInput` when the dialog has the foreground, otherwise as the same key
//! messages posted to its focused control. Both reach the app's message loop, the dialog hook,
//! `IsDialogMessageW` and the controls, as typed keys do.
use super::{went, Key};
use crate::dialog::{After, Spec};
use crate::dialog_windows::{self as dw, inspect};
use crate::native_test::{on_main, wait_for};
use std::time::{Duration, Instant};
use tauri::AppHandle;
use windows::Win32::Foundation::{HWND, LPARAM, WPARAM};
use windows::Win32::UI::Input::KeyboardAndMouse::{
    GetFocus, SendInput, SetFocus, INPUT, INPUT_0, INPUT_KEYBOARD, KEYBDINPUT, KEYBD_EVENT_FLAGS,
    KEYEVENTF_KEYUP, KEYEVENTF_UNICODE, VIRTUAL_KEY, VK_ESCAPE, VK_MENU, VK_TAB,
};
use windows::Win32::UI::WindowsAndMessaging::{
    GetForegroundWindow, GetQueueStatus, GetWindowTextLengthW, IsChild, PostMessageW,
    SetForegroundWindow, QS_INPUT, WM_CHAR, WM_KEYDOWN, WM_KEYUP, WM_SYSCHAR,
};

/// A window handle as it crosses threads (`HWND` is not `Send`).
type Raw = isize;

fn raw(h: HWND) -> Raw {
    h.0 as isize
}

fn hwnd(r: Raw) -> HWND {
    HWND(r as *mut core::ffi::c_void)
}

pub(crate) fn count(app: &AppHandle, title: &str) -> usize {
    let title = title.to_string();
    on_main(app, move || inspect::all(&title).len()).unwrap_or(0)
}

/// The one open dialog titled title, once there is exactly one.
fn one(app: &AppHandle, title: &str) -> Result<Raw, String> {
    wait_for(title, || {
        let t = title.to_string();
        on_main(app, move || {
            let all = inspect::all(&t);
            (all.len() == 1).then(|| raw(all[0]))
        })
        .flatten()
    })
}

fn kb(vk: VIRTUAL_KEY, scan: u16, flags: KEYBD_EVENT_FLAGS) -> INPUT {
    INPUT {
        r#type: INPUT_KEYBOARD,
        Anonymous: INPUT_0 {
            ki: KEYBDINPUT {
                wVk: vk,
                wScan: scan,
                dwFlags: flags,
                time: 0,
                dwExtraInfo: 0,
            },
        },
    }
}

fn send(inputs: &[INPUT]) -> bool {
    let sent = unsafe { SendInput(inputs, std::mem::size_of::<INPUT>() as i32) };
    sent as usize == inputs.len()
}

/// Gives dialog d the foreground, as a person's click would; whether it has it.
fn foreground(d: HWND) -> bool {
    unsafe {
        let _ = SetForegroundWindow(d);
        GetForegroundWindow() == d
    }
}

/// Where a posted key goes: the focused control when it is in d, otherwise d.
fn target(d: HWND) -> HWND {
    let f = unsafe { GetFocus() };
    if !f.is_invalid() && (f == d || unsafe { IsChild(d, f) }.as_bool()) {
        f
    } else {
        d
    }
}

fn post(to: HWND, msg: u32, w: usize, l: isize) -> Result<(), String> {
    unsafe { PostMessageW(Some(to), msg, WPARAM(w), LPARAM(l)) }.map_err(|e| e.to_string())
}

/// Lets the main thread take every key it was sent, then one more round trip: keys sent with
/// `SendInput` come after posted messages (and the main-thread tasks that are posted too), so
/// the next input, whichever way it goes, comes after them.
fn settle(app: &AppHandle, sent: bool) {
    if sent {
        let deadline = Instant::now() + Duration::from_secs(3);
        std::thread::sleep(Duration::from_millis(50));
        while Instant::now() < deadline {
            let pending = on_main(app, || unsafe { GetQueueStatus(QS_INPUT) } >> 16).unwrap_or(0);
            if pending == 0 {
                break;
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    }
    let _ = on_main(app, || ());
}

pub(crate) fn press(app: &AppHandle, title: &str, key: Key) -> Result<(), String> {
    let d = one(app, title)?;
    let way = on_main(app, move || -> Result<&'static str, String> {
        let d = hwnd(d);
        if foreground(d) {
            let up = KEYEVENTF_KEYUP;
            let none = KEYBD_EVENT_FLAGS(0);
            let inputs = match key {
                Key::Access(c) => {
                    let vk = VIRTUAL_KEY(c.to_ascii_uppercase() as u16);
                    vec![
                        kb(VK_MENU, 0, none),
                        kb(vk, 0, none),
                        kb(vk, 0, up),
                        kb(VK_MENU, 0, up),
                    ]
                }
                Key::Escape => vec![kb(VK_ESCAPE, 0, none), kb(VK_ESCAPE, 0, up)],
                Key::Tab => vec![kb(VK_TAB, 0, none), kb(VK_TAB, 0, up)],
            };
            if send(&inputs) {
                return Ok("sendinput");
            }
        }
        let t = target(d);
        match key {
            // Alt held (context code, bit 29), as WM_SYSCHAR from a typed Alt+letter has it.
            Key::Access(c) => post(t, WM_SYSCHAR, c.to_ascii_lowercase() as usize, 1 | 1 << 29)?,
            Key::Escape | Key::Tab => {
                let vk = if key == Key::Escape {
                    VK_ESCAPE
                } else {
                    VK_TAB
                };
                post(t, WM_KEYDOWN, vk.0 as usize, 1)?;
                post(t, WM_KEYUP, vk.0 as usize, 1 | 3 << 30)?;
            }
        }
        Ok("posted")
    })
    .ok_or("the main thread did not answer")??;
    went(way);
    settle(app, way == "sendinput");
    Ok(())
}

/// The hidden entry of dialog d and how many UTF-16 units it holds, on the main thread.
fn entry(d: HWND) -> Option<(HWND, i32)> {
    let e = inspect::secret(d)?;
    Some((e, unsafe { GetWindowTextLengthW(e) }))
}

pub(crate) fn type_text(app: &AppHandle, title: &str, text: &str) -> Result<(), String> {
    let d = one(app, title)?;
    let units: Vec<u16> = text.encode_utf16().collect();
    let n = units.len() as i32;
    let (way, before) = on_main(app, move || -> Result<(&'static str, i32), String> {
        let d = hwnd(d);
        let (e, before) = entry(d).ok_or("the dialog has no hidden entry")?;
        if foreground(d) {
            if unsafe { GetFocus() } != e {
                // Where a person would click first.
                unsafe {
                    let _ = SetFocus(Some(e));
                }
            }
            let mut inputs = Vec::with_capacity(units.len() * 2);
            for &u in &units {
                inputs.push(kb(VIRTUAL_KEY(0), u, KEYEVENTF_UNICODE));
                inputs.push(kb(VIRTUAL_KEY(0), u, KEYEVENTF_UNICODE | KEYEVENTF_KEYUP));
            }
            let sent = send(&inputs);
            for i in &mut inputs {
                i.Anonymous.ki.wScan = 0;
            }
            if sent {
                return Ok(("sendinput", before));
            }
        }
        for &u in &units {
            post(e, WM_CHAR, u as usize, 1)?;
        }
        Ok(("posted", before))
    })
    .ok_or("the main thread did not answer")??;
    went(way);
    settle(app, way == "sendinput");
    // Every unit reached the entry before anything else is pressed.
    wait_for("the typed key in the native entry", || {
        on_main(app, move || entry(hwnd(d)).map(|(_, len)| len))
            .flatten()
            .filter(|len| *len == before + n)
    })
    .map(|_| ())
}

pub(crate) fn texts(app: &AppHandle, title: &str) -> Result<Vec<String>, String> {
    let title = title.to_string();
    on_main(app, move || {
        inspect::all(&title)
            .into_iter()
            .map(|d| inspect::texts(d).join("\n"))
            .collect()
    })
    .ok_or_else(|| "no native window audit".into())
}

pub(crate) fn secret_hidden(app: &AppHandle, title: &str) -> Result<bool, String> {
    let title = title.to_string();
    on_main(app, move || {
        inspect::all(&title).into_iter().any(inspect::secret_hidden)
    })
    .ok_or_else(|| "could not inspect the native entry".into())
}

/// The clipboard's text, and whether it is marked to stay out of clipboard history.
pub(crate) fn clipboard(app: &AppHandle) -> (Option<String>, Option<bool>) {
    on_main(app, inspect::clipboard)
        .map(|(text, concealed)| (text, Some(concealed)))
        .unwrap_or((None, None))
}

pub(crate) fn cancel_focused(app: &AppHandle, title: &str) -> Result<bool, String> {
    let title = title.to_string();
    on_main(app, move || {
        inspect::all(&title)
            .into_iter()
            .any(inspect::cancel_focused)
    })
    .ok_or_else(|| "could not inspect the native dialog".into())
}

/// Shows a native dialog titled title with one button, `_Start`, and presses it with Alt+S: its
/// press handler, on the main thread, calls f, as a trusted native launcher's would.
pub(crate) fn fixture_button(
    app: &AppHandle,
    title: &str,
    f: impl FnOnce() + Send + 'static,
) -> Result<(), String> {
    let name = title.to_string();
    on_main(app, move || {
        let mut f = Some(f);
        let mut spec = Spec::new(
            &name,
            Box::new(move |_, _| {
                if let Some(f) = f.take() {
                    f();
                }
                After::Close
            }),
        );
        spec.buttons = vec![(1, "_Start".into())];
        spec.default = Some(1);
        spec.focus = Some(1);
        dw::show(spec, None).map(|_| ())
    })
    .ok_or("the main thread did not answer")??;
    press(app, title, Key::Access('s'))
}

/// Reads the key entry titled title as another process can, through UI Automation (Windows
/// PowerShell): its entry must say it is a password field and must not give out canary, which
/// was typed into it. The canary goes to PowerShell on stdin; only booleans come back.
pub(crate) fn uia_canary(title: &str, canary: &str) -> Result<serde_json::Value, String> {
    use base64::Engine;
    use std::io::Write;
    use std::process::{Command, Stdio};
    let script = format!(
        r#"$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
$canary = [Console]::In.ReadLine()
$A = [System.Windows.Automation.AutomationElement]
$scope = [System.Windows.Automation.TreeScope]
$named = New-Object System.Windows.Automation.PropertyCondition($A::NameProperty, '{title}')
$win = $null
for ($i = 0; $i -lt 100 -and -not $win; $i++) {{
  $win = $A::RootElement.FindFirst($scope::Descendants, $named)
  if (-not $win) {{ Start-Sleep -Milliseconds 100 }}
}}
$out = @{{ found = [bool]$win; is_password = $false; readable = $false; leaked = $false }}
if ($win) {{
  $edit = New-Object System.Windows.Automation.PropertyCondition($A::ControlTypeProperty, [System.Windows.Automation.ControlType]::Edit)
  foreach ($e in $win.FindAll($scope::Descendants, $edit)) {{
    if ($e.Current.IsPassword) {{
      $out.is_password = $true
      try {{
        $v = $e.GetCurrentPattern([System.Windows.Automation.ValuePattern]::Pattern).Current.Value
        $out.readable = $true
        if ($v -and $v.Contains($canary)) {{ $out.leaked = $true }}
      }} catch {{ }}
    }}
  }}
}}
$out | ConvertTo-Json -Compress"#
    );
    let encoded: Vec<u8> = script
        .encode_utf16()
        .flat_map(|u| u.to_le_bytes())
        .collect();
    let encoded = base64::engine::general_purpose::STANDARD.encode(encoded);
    let mut child = Command::new("powershell.exe")
        .args(["-NoProfile", "-NonInteractive", "-EncodedCommand", &encoded])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("could not start PowerShell: {e}"))?;
    child
        .stdin
        .take()
        .ok_or("no input pipe")?
        .write_all(format!("{canary}\n").as_bytes())
        .map_err(|_| "could not give PowerShell the canary")?;
    let out = child
        .wait_with_output()
        .map_err(|e| format!("PowerShell failed: {e}"))?;
    let text = String::from_utf8_lossy(&out.stdout);
    serde_json::from_str(text.trim()).map_err(|_| {
        format!(
            "PowerShell gave no UI Automation result (status {}): {}",
            out.status,
            String::from_utf8_lossy(&out.stderr)
                .chars()
                .take(500)
                .collect::<String>()
        )
    })
}
