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
    SetForegroundWindow, QS_INPUT, SC_CLOSE, WM_CHAR, WM_KEYDOWN, WM_KEYUP, WM_SYSCHAR,
    WM_SYSCOMMAND,
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

/// The access key of a Win32 control's text: the character after its single `&`.
fn mnemonic(text: &str) -> Option<char> {
    let mut chars = text.chars();
    while let Some(ch) = chars.next() {
        if ch == '&' {
            match chars.next() {
                Some('&') => {}
                other => return other,
            }
        }
    }
    None
}

/// Whether the control of dialog d with access key c is enabled; None when no control has that
/// key.
fn access_enabled(app: &AppHandle, d: Raw, c: char) -> Result<Option<bool>, String> {
    use windows::Win32::UI::Input::KeyboardAndMouse::IsWindowEnabled;
    use windows::Win32::UI::WindowsAndMessaging::{
        GetWindow, GetWindowTextW, GW_CHILD, GW_HWNDNEXT,
    };
    on_main(app, move || {
        let mut child = unsafe { GetWindow(hwnd(d), GW_CHILD) }.ok();
        while let Some(w) = child {
            let mut b = [0u16; 256];
            let n = unsafe { GetWindowTextW(w, &mut b) }.max(0) as usize;
            if mnemonic(&String::from_utf16_lossy(&b[..n]))
                .is_some_and(|k| k.eq_ignore_ascii_case(&c))
            {
                return Some(unsafe { IsWindowEnabled(w) }.as_bool());
            }
            child = unsafe { GetWindow(w, GW_HWNDNEXT) }.ok();
        }
        None
    })
    .ok_or_else(|| "the main thread did not answer".into())
}

pub(crate) fn press(app: &AppHandle, title: &str, key: Key) -> Result<(), String> {
    let d = one(app, title)?;
    if let Key::Access(c) = key {
        super::until_enabled(title, c, || access_enabled(app, d, c))?;
    }
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

/// Closes the dialog titled title with its close box, as a click on it does: `SC_CLOSE`.
pub(crate) fn close_box(app: &AppHandle, title: &str) -> Result<(), String> {
    let d = one(app, title)?;
    on_main(app, move || {
        post(hwnd(d), WM_SYSCOMMAND, SC_CLOSE as usize, 0)
    })
    .ok_or("the main thread did not answer")??;
    settle(app, false);
    Ok(())
}

/// Posts to the dialog titled title the click message of its button labelled label (GTK mnemonic
/// form), as a click on it sends, without asking the button: whether the button was enabled
/// when the message went.
pub(crate) fn post_click(app: &AppHandle, title: &str, label: &str) -> Result<bool, String> {
    use windows::Win32::UI::Input::KeyboardAndMouse::IsWindowEnabled;
    use windows::Win32::UI::WindowsAndMessaging::{
        GetDlgCtrlID, GetWindow, GetWindowTextW, BN_CLICKED, GW_CHILD, GW_HWNDNEXT, WM_COMMAND,
    };
    let d = one(app, title)?;
    let want = crate::confirmation::text::win32(label);
    let enabled = on_main(app, move || -> Result<bool, String> {
        let d = hwnd(d);
        let mut child = unsafe { GetWindow(d, GW_CHILD) }.ok();
        while let Some(c) = child {
            let mut b = [0u16; 256];
            let n = unsafe { GetWindowTextW(c, &mut b) }.max(0) as usize;
            if String::from_utf16_lossy(&b[..n]) == want {
                let enabled = unsafe { IsWindowEnabled(c) }.as_bool();
                let id = unsafe { GetDlgCtrlID(c) } as usize & 0xFFFF;
                post(
                    d,
                    WM_COMMAND,
                    id | (BN_CLICKED as usize) << 16,
                    c.0 as isize,
                )?;
                return Ok(enabled);
            }
            child = unsafe { GetWindow(c, GW_HWNDNEXT) }.ok();
        }
        Err(format!("no button {want:?}"))
    })
    .ok_or("the main thread did not answer")??;
    settle(app, false);
    Ok(enabled)
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

/// Reads the key entry titled title as another process can, through UI Automation's native
/// client (Windows PowerShell): its entry must be an edit control that says it is a password field
/// and must not give out canary, which was typed into it. The client opens the entry by its window
/// handle and reports the path from the desktop down to it (each element's control type, name and
/// window class, never its value) and the description of the provider that answered. The canary
/// goes to PowerShell on stdin; only booleans, that path, the provider and what Active
/// Accessibility says of the entry (role and state) come back.
pub(crate) fn uia_canary(
    app: &AppHandle,
    title: &str,
    canary: &str,
) -> Result<serde_json::Value, String> {
    use base64::Engine;
    use std::io::Write;
    use std::process::{Command, Stdio};
    let t = title.to_string();
    let (edit, style_password) = on_main(app, move || {
        let d = *inspect::all(&t).first()?;
        Some((raw(inspect::secret(d)?), inspect::secret_hidden(d)))
    })
    .flatten()
    .ok_or("no key window with an entry to read through UI Automation")?;
    let script = format!(
        r#"$ErrorActionPreference = 'Stop'
$canary = [Console]::In.ReadLine()
# The native UI Automation client (UIAutomationCore's CUIAutomation), as assistive tools use it,
# with the Win32 control proxies it has built in; the .NET client's System.Windows.Automation
# loads its proxies from an assembly of its own, which fails on the hosted runners. Declared here
# only as far as used: each placeholder keeps its method's place in the interface.
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
[ComImport, Guid("d22108aa-8ac5-49a5-837b-37bbb3d7591e"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface PluxyUiaElement {{
  void SetFocus(); void GetRuntimeId(); void FindFirst(); void FindAll(); void FindFirstBuildCache();
  void FindAllBuildCache(); void BuildUpdatedCache();
  [return: MarshalAs(UnmanagedType.Struct)] object GetCurrentPropertyValue(int property);
}}
[ComImport, Guid("4042c624-389c-4afc-a630-9df854a541fc"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface PluxyUiaWalker {{
  PluxyUiaElement GetParentElement(PluxyUiaElement element);
}}
[ComImport, Guid("30cbe57d-d9d0-452a-ab13-7ac5ac4825ee"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface PluxyUia {{
  void CompareElements(); void CompareRuntimeIds(); void GetRootElement();
  PluxyUiaElement ElementFromHandle(IntPtr hwnd);
  void ElementFromPoint(); void GetFocusedElement(); void GetRootElementBuildCache();
  void ElementFromHandleBuildCache(); void ElementFromPointBuildCache(); void GetFocusedElementBuildCache();
  void CreateTreeWalker(); void ControlViewWalker(); void ContentViewWalker();
  PluxyUiaWalker RawViewWalker();
}}
public class PluxyUiaResult {{
  public bool found; public bool is_edit; public bool is_password; public bool readable; public bool leaked;
  public string provider = ""; public string[] path = new string[0];
}}
public static class PluxyUiaClient {{
  static object Prop(PluxyUiaElement e, int id) {{ return e.GetCurrentPropertyValue(id); }}
  static string Describe(PluxyUiaElement e) {{
    int t = Convert.ToInt32(Prop(e, 30003));
    string type = t == 50004 ? "Edit" : t == 50032 ? "Window" : t == 50033 ? "Pane" : t.ToString();
    return type + " '" + Prop(e, 30005) + "' [" + Prop(e, 30012) + "]";
  }}
  // Only booleans, the path and the provider's description come back, never the entry's value.
  public static PluxyUiaResult Inspect(IntPtr edit, string canary) {{
    PluxyUiaResult r = new PluxyUiaResult();
    PluxyUia uia = (PluxyUia)Activator.CreateInstance(Type.GetTypeFromCLSID(new Guid("ff48dba4-60ef-4201-aa87-54103eef594e")));
    PluxyUiaElement e = uia.ElementFromHandle(edit);
    if (e == null) return r;
    r.found = true;
    PluxyUiaWalker walker = uia.RawViewWalker();
    System.Collections.Generic.List<string> path = new System.Collections.Generic.List<string>();
    for (PluxyUiaElement x = e; x != null && path.Count < 16; x = walker.GetParentElement(x)) path.Insert(0, Describe(x));
    r.path = path.ToArray();
    r.is_edit = Convert.ToInt32(Prop(e, 30003)) == 50004;
    r.is_password = Convert.ToBoolean(Prop(e, 30019));
    r.provider = Convert.ToString(Prop(e, 30107));
    try {{
      if (Convert.ToBoolean(Prop(e, 30043))) {{
        object v = Prop(e, 30045);
        r.readable = true;
        r.leaked = v != null && Convert.ToString(v).Contains(canary);
      }}
    }} catch (Exception) {{ }}
    return r;
  }}
}}
"@
try {{
  $r = [PluxyUiaClient]::Inspect([IntPtr]{edit}, $canary)
  $out = @{{ found = $r.found; is_edit = $r.is_edit; is_password = $r.is_password; readable = $r.readable; leaked = $r.leaked; path = @($r.path); provider = $r.provider }}
}} catch {{ $out = @{{ found = $false; error = "$($_.Exception.Message)" }} }}
# What Active Accessibility (oleacc) says of the entry, beside UI Automation.
try {{
  Add-Type -AssemblyName Accessibility
  Add-Type -ReferencedAssemblies Accessibility -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class PluxyMsaa {{
  [DllImport("oleacc.dll")]
  static extern int AccessibleObjectFromWindow(IntPtr h, uint id, ref Guid iid, [MarshalAs(UnmanagedType.IUnknown)] out object o);
  public static string Describe(IntPtr h) {{
    Guid iid = new Guid("618736E0-3C3D-11CF-810C-00AA00389B71");
    object o;
    int hr = AccessibleObjectFromWindow(h, 0xFFFFFFFC, ref iid, out o);
    if (hr != 0) return "hr 0x" + hr.ToString("x");
    Accessibility.IAccessible a = (Accessibility.IAccessible)o;
    return "role " + a.get_accRole(0) + ", state 0x" + Convert.ToInt64(a.get_accState(0)).ToString("x");
  }}
}}
"@
  $out.msaa = [PluxyMsaa]::Describe([IntPtr]{edit})
}} catch {{ $out.msaa = "unavailable: $($_.Exception.Message)" }}
$out | ConvertTo-Json -Compress -Depth 3"#
    );
    let encoded: Vec<u8> = script
        .encode_utf16()
        .flat_map(|u| u.to_le_bytes())
        .collect();
    let encoded = base64::engine::general_purpose::STANDARD.encode(encoded);
    let started = std::time::Instant::now();
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
    let mut v: serde_json::Value = serde_json::from_str(text.trim()).map_err(|_| {
        format!(
            "PowerShell gave no UI Automation result (status {}): {}",
            out.status,
            String::from_utf8_lossy(&out.stderr)
                .chars()
                .take(500)
                .collect::<String>()
        )
    })?;
    // What the control itself says, beside what UI Automation reports of it.
    v["style_password"] = style_password.into();
    v["ms"] = (started.elapsed().as_millis() as u64).into();
    Ok(v)
}
