//! The execution program's launch modes on Windows, run by the app as a confirmed launch:
//!
//! - `terminal --with <kind> -- <program> <args>…`: run in the console window of the launch's own
//!   (the app starts it with a new console, which Windows shows in the person's default terminal
//!   app). It runs the program in that console with the launch's working folder and environment,
//!   passing Ctrl+C to it, then says how it ended; the window then waits for Enter (`console`) or
//!   stays at a Command Prompt or PowerShell prompt (`cmd`, `powershell`). Exits with the
//!   program's code. The program runs by its path once `checked::hold` holds it and every folder
//!   above it, so none can be changed until it runs, and no other account could have changed
//!   them since the app checked the program; a program it refuses never runs, and the window
//!   says why, naming the path.
//! - `open -- <file>`: opens file in the app Windows opens its type with (`ShellExecuteExW`,
//!   verb `open`, the file's own extension as its class, no dialog).
//! - `app -- <AUMID>`: starts a packaged app (`shell:AppsFolder\<AUMID>`).
//!
//! In a build with the `native-test` feature (the native test's only), nobody is at the window: the
//! `console` kind writes `held` to `pluxy-after-exit` in its working folder instead of waiting for
//! Enter, and the Command Prompt or PowerShell after the program runs one fixed command that writes
//! its working folder there, then exits. A normal build never does either (`after_exit_tests`).

use crate::recipe::os::{aumid_ok, kind_shell, opens, wide, OPEN_TYPES};
use std::io::{BufRead, Write};
use std::path::Path;
use std::process::Command;
use windows_sys::core::BOOL;
use windows_sys::Win32::System::Com::{
    CoInitializeEx, COINIT_APARTMENTTHREADED, COINIT_DISABLE_OLE1DDE,
};
use windows_sys::Win32::System::Console::{
    SetConsoleCtrlHandler, SetConsoleTitleW, CTRL_BREAK_EVENT, CTRL_C_EVENT,
};
use windows_sys::Win32::UI::Shell::{
    ShellExecuteExW, SEE_MASK_CLASSNAME, SEE_MASK_FLAG_NO_UI, SEE_MASK_NOASYNC,
    SEE_MASK_NOCLOSEPROCESS, SHELLEXECUTEINFOW,
};
use windows_sys::Win32::UI::WindowsAndMessaging::SW_SHOWNORMAL;

pub fn main(args: &[String]) -> i32 {
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    let r = match args.as_slice() {
        ["terminal", "--with", kind, "--", program, rest @ ..] => {
            return terminal(kind, program, rest)
        }
        ["open", "--", file] => open(file),
        ["app", "--", aumid] => app(aumid),
        _ => Err("execution: not a launch this program makes".into()),
    };
    match r {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("pluxy: {e}");
            1
        }
    }
}

/// Ctrl+C and Ctrl+Break are the program's: this one keeps running (a handler routine, unlike
/// ignoring them, is not passed on to the program).
unsafe extern "system" fn keep_running(event: u32) -> BOOL {
    BOOL::from(event == CTRL_C_EVENT || event == CTRL_BREAK_EVENT)
}

fn terminal(kind: &str, program: &str, args: &[&str]) -> i32 {
    let Some(shell) = kind_shell(kind) else {
        eprintln!("pluxy: no terminal {kind}");
        return 2;
    };
    let name = Path::new(program)
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| program.to_string());
    let title = wide(&name);
    // SAFETY: title is NUL-terminated; the handler is a plain function.
    unsafe {
        SetConsoleTitleW(title.as_ptr());
        SetConsoleCtrlHandler(Some(keep_running), 1);
    }
    let code = match run(program, args) {
        Ok(code) => code,
        Err(e) => {
            println!("pluxy: {e}");
            1
        }
    };
    println!();
    println!("[{name} exited with code {code}]");
    match shell {
        Some(sh) => {
            if let Err(e) = Command::new(&sh).args(shell_flags(kind)).status() {
                println!("pluxy: could not start {}: {e}", sh.display());
                hold();
            }
        }
        None => hold(),
    }
    code
}

/// Runs program with args once it is held (`checked::hold`), and lets it go once it runs: its
/// image is then mapped, which Windows keeps from being written, and the program may rename its
/// own file (to update itself) while it runs. Its exit code, or why it did not run.
fn run(program: &str, args: &[&str]) -> Result<i32, String> {
    let held =
        crate::checked::hold(program).map_err(|e| format!("will not run \"{program}\": {e}"))?;
    #[cfg(test)]
    BEFORE_RUN.with(|h| {
        if let Some(h) = h.borrow_mut().as_mut() {
            h(&held.path)
        }
    });
    let mut child = Command::new(&held.path)
        .args(args)
        .spawn()
        .map_err(|e| format!("could not start \"{program}\": {e}"))?;
    drop(held);
    let status = child
        .wait()
        .map_err(|e| format!("waiting for \"{program}\": {e}"))?;
    Ok(status.code().unwrap_or(1))
}

/// For the tests: called with the program's path once it is held, right before it runs, to
/// change it as another program could then.
#[cfg(test)]
type BeforeRun = Box<dyn FnMut(&Path)>;

#[cfg(test)]
thread_local! {
    static BEFORE_RUN: std::cell::RefCell<Option<BeforeRun>> = const { std::cell::RefCell::new(None) };
}

/// The arguments of the shell started after the program: Command Prompt with AutoRun off,
/// PowerShell with no logo and no profile, each interactive with no command.
#[cfg(not(feature = "native-test"))]
pub(crate) fn shell_flags(kind: &str) -> &'static [&'static str] {
    if kind == "cmd" {
        &["/D"]
    } else {
        &["-NoLogo", "-NoProfile"]
    }
}

/// The native test's shell after the program: one fixed command writing the shell's working
/// folder to `pluxy-after-exit`, then it exits.
#[cfg(feature = "native-test")]
pub(crate) fn shell_flags(kind: &str) -> &'static [&'static str] {
    if kind == "cmd" {
        &["/D", "/C", "cd > pluxy-after-exit"]
    } else {
        &[
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-Command",
            "(Get-Location).Path | Out-File -Encoding ascii pluxy-after-exit",
        ]
    }
}

/// Whether the `console` window waits for Enter before it closes: always, but in the native
/// test's build.
pub(crate) fn waits() -> bool {
    cfg!(not(feature = "native-test"))
}

/// Waits for Enter, so the window stays until the person has read it. In the native test's build
/// it writes `held` to `pluxy-after-exit` instead.
fn hold() {
    if !waits() {
        let _ = std::fs::write("pluxy-after-exit", "held");
        return;
    }
    print!("Press Enter to close this window.");
    let _ = std::io::stdout().flush();
    let _ = std::io::stdin().lock().read_line(&mut String::new());
}

/// Runs ShellExecuteExW on file (or a shell name) with class, verb `open`, no dialog.
fn shell_execute(file: &str, class: Option<&str>, dir: Option<&str>) -> Result<(), String> {
    // SAFETY: COM is set up once for this thread, as the shell needs.
    unsafe {
        CoInitializeEx(
            std::ptr::null(),
            (COINIT_APARTMENTTHREADED | COINIT_DISABLE_OLE1DDE) as u32,
        )
    };
    let (verb, file_w) = (wide("open"), wide(file));
    let class_w = class.map(wide);
    let dir_w = dir.map(wide);
    // SAFETY: an all-zero SHELLEXECUTEINFOW is its documented empty value.
    let mut info: SHELLEXECUTEINFOW = unsafe { std::mem::zeroed() };
    info.cbSize = std::mem::size_of::<SHELLEXECUTEINFOW>() as u32;
    info.fMask = SEE_MASK_NOASYNC | SEE_MASK_FLAG_NO_UI | SEE_MASK_NOCLOSEPROCESS;
    info.lpVerb = verb.as_ptr();
    info.lpFile = file_w.as_ptr();
    if let Some(c) = &class_w {
        info.fMask |= SEE_MASK_CLASSNAME;
        info.lpClass = c.as_ptr();
    }
    if let Some(d) = &dir_w {
        info.lpDirectory = d.as_ptr();
    }
    info.nShow = SW_SHOWNORMAL;
    // SAFETY: every pointer in info outlives the call.
    if unsafe { ShellExecuteExW(&mut info) } == 0 {
        return Err(format!(
            "Windows could not open {file}: {}",
            std::io::Error::last_os_error()
        ));
    }
    if !info.hProcess.is_null() {
        // SAFETY: the handle is ours to close (SEE_MASK_NOCLOSEPROCESS).
        unsafe { windows_sys::Win32::Foundation::CloseHandle(info.hProcess) };
    }
    Ok(())
}

fn open(file: &str) -> Result<(), String> {
    let p = Path::new(file);
    let ext = p
        .extension()
        .and_then(|e| e.to_str())
        .map(str::to_ascii_lowercase)
        .unwrap_or_default();
    if !OPEN_TYPES.iter().any(|(x, _)| *x == ext) || !p.is_absolute() || !p.is_file() {
        return Err(format!("{file} is not a file pluxy opens"));
    }
    // ShellExecuteExW reports success and opens nothing for a type no app opens.
    if !opens(&ext) {
        return Err(format!("no app opens .{ext} files on this computer"));
    }
    let dir = p.parent().and_then(|d| d.to_str());
    shell_execute(file, Some(&format!(".{ext}")), dir)
}

fn app(aumid: &str) -> Result<(), String> {
    if !aumid_ok(aumid) {
        return Err(format!("{aumid} is not an app's ID"));
    }
    shell_execute(&format!(r"shell:AppsFolder\{aumid}"), None, None)
}

#[cfg(test)]
#[path = "after_exit_tests.rs"]
mod after_exit_tests;
#[cfg(test)]
#[path = "launch_windows_tests.rs"]
mod tests;
