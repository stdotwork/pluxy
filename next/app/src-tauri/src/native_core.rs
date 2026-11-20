//! The native test's managed-core steps (native-test builds only). Before the window shows core's
//! pages, it drives the app's own core through the window's commands and the real native
//! confirmation dialog (worked through the UI driver, `ui_driver.rs`: X input from xdotool on
//! Linux, Windows' and macOS' own input elsewhere), against the signed test artifacts the runner
//! made (`native/run.ts` on Linux, `native/dialogs.ts` on Windows and macOS):
//! `PLUXY_APP_TEST_CORE_SOURCE` names their index.
//!
//! 1. with no core installed the app is not connected; an install with no trust roots is refused
//!    before any dialog, and nothing is installed; the runner then adds the roots;
//! 2. the same install, confirmed, puts core 1.0.0 in place and connects to it;
//! 3. a release whose signature does not verify is refused before any dialog;
//! 4. 2.0.0, confirmed, replaces 1.0.0, whose process is gone; the config is unchanged;
//! 5. 3.0.0 breaks its config and exits: the app restores the config and runs 2.0.0 again;
//! 6. an unknown rollback target is refused; a cancelled rollback changes nothing; a confirmed
//!    one runs the installed 1.0.0;
//! 7. a custom core program does not run until adopted; adopting copies it into the app's cores
//!    and runs the copy; a rollback then runs 1.0.0 again;
//! 8. with 2.0.0 installed again (the app keeps only the running version and the one before it,
//!    so step 7 removed it), a file of the earlier 1.0.0 is changed: rolling back to it is
//!    refused before any dialog, and installing it again, confirmed, repairs it and runs it;
//! 9. installing the running 1.0.0 again, confirmed, writes it anew and runs it in a new process
//!    (on Windows the core is stopped first: its folder cannot be moved aside while it runs).
//!
//! Each step prints `native-test: core_<step> <JSON>`; the runner checks the values.

use crate::confirmation::text::CONFIRM_TITLE as DIALOG;
use crate::native_test::ui::{self, Key};
use crate::native_test::{eval, log, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

/// The confirmation's buttons: Confirm and Cancel (`confirmation::text`).
pub(super) const CONFIRM: Key = Key::Access('o');
const CANCEL: Key = Key::Access('c');
static SEQ: AtomicU64 = AtomicU64::new(0);

/// One window command, as the Connection page calls it: Ok(value) or Err(the refusal).
fn invoke(main: &Webview, cmd: &str, args: Value) -> Result<Result<Value, String>, String> {
    let n = SEQ.fetch_add(1, Ordering::SeqCst);
    main.eval(format!(
        "window.__pluxyCore = window.__pluxyCore || {{}}; \
         window.__TAURI_INTERNALS__.invoke({}, {args}).then(\
           (v) => {{ window.__pluxyCore[{n}] = {{ok: true, value: v ?? null}}; }}, \
           (e) => {{ window.__pluxyCore[{n}] = {{ok: false, error: String(e)}}; }})",
        json!(cmd)
    ))
    .map_err(|e| e.to_string())?;
    let r = wait_for(cmd, || {
        eval(main, &format!("window.__pluxyCore?.[{n}] ?? null")).filter(|v| !v.is_null())
    })?;
    Ok(if r["ok"] == true {
        Ok(r["value"].clone())
    } else {
        Err(r["error"].as_str().unwrap_or_default().to_string())
    })
}

pub(super) fn call(main: &Webview, cmd: &str, args: Value) -> Result<Value, String> {
    invoke(main, cmd, args)?.map_err(|e| format!("{cmd}: {e}"))
}

fn require(ok: bool, what: impl Into<String>) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(what.into())
    }
}

/// Whether process pid runs (a zombie does not, on Linux; the app reaps its own elsewhere).
#[cfg(target_os = "linux")]
fn alive(pid: u64) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .ok()
        .and_then(|s| s.rsplit_once(") ").map(|(_, r)| !r.starts_with('Z')))
        .unwrap_or(false)
}

#[cfg(target_os = "macos")]
fn alive(pid: u64) -> bool {
    // SAFETY: signal 0 only asks whether the process exists.
    let found = unsafe { libc::kill(pid as libc::pid_t, 0) } == 0;
    found || std::io::Error::last_os_error().raw_os_error() == Some(libc::EPERM)
}

#[cfg(windows)]
fn alive(pid: u64) -> bool {
    use windows_sys::Win32::Foundation::{CloseHandle, STILL_ACTIVE};
    use windows_sys::Win32::System::Threading::{
        GetExitCodeProcess, OpenProcess, PROCESS_QUERY_LIMITED_INFORMATION,
    };
    // SAFETY: the handle is checked and closed; code is written on success.
    unsafe {
        let h = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid as u32);
        if h.is_null() {
            return false;
        }
        let mut code = 0u32;
        let ok = GetExitCodeProcess(h, &mut code) != 0;
        CloseHandle(h);
        ok && code == STILL_ACTIVE as u32
    }
}

/// The program process pid runs, with every link resolved; empty if it cannot be read.
#[cfg(target_os = "linux")]
fn exe(pid: u64) -> PathBuf {
    std::fs::read_link(format!("/proc/{pid}/exe")).unwrap_or_default()
}

#[cfg(target_os = "macos")]
fn exe(pid: u64) -> PathBuf {
    use std::os::unix::ffi::OsStrExt;
    let mut buf = vec![0u8; libc::PROC_PIDPATHINFO_MAXSIZE as usize];
    // SAFETY: buf holds the size given.
    let n = unsafe { libc::proc_pidpath(pid as i32, buf.as_mut_ptr().cast(), buf.len() as u32) };
    if n <= 0 {
        return PathBuf::new();
    }
    let path = PathBuf::from(std::ffi::OsStr::from_bytes(&buf[..n as usize]));
    std::fs::canonicalize(&path).unwrap_or(path)
}

#[cfg(windows)]
fn exe(pid: u64) -> PathBuf {
    use std::os::windows::ffi::OsStringExt;
    use windows_sys::Win32::Foundation::CloseHandle;
    use windows_sys::Win32::System::Threading::{
        OpenProcess, QueryFullProcessImageNameW, PROCESS_NAME_WIN32,
        PROCESS_QUERY_LIMITED_INFORMATION,
    };
    let mut buf = vec![0u16; 32_768];
    let mut len = buf.len() as u32;
    // SAFETY: the handle is checked and closed; buf holds len characters.
    let ok = unsafe {
        let h = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid as u32);
        if h.is_null() {
            return PathBuf::new();
        }
        let ok = QueryFullProcessImageNameW(h, PROCESS_NAME_WIN32, buf.as_mut_ptr(), &mut len) != 0;
        CloseHandle(h);
        ok
    };
    if !ok {
        return PathBuf::new();
    }
    let path = PathBuf::from(std::ffi::OsString::from_wide(&buf[..len as usize]));
    std::fs::canonicalize(&path).unwrap_or(path)
}

/// Every process ID on this computer.
#[cfg(target_os = "linux")]
fn pids() -> Vec<u64> {
    std::fs::read_dir("/proc")
        .into_iter()
        .flatten()
        .flatten()
        .filter_map(|d| d.file_name().to_str()?.parse::<u64>().ok())
        .collect()
}

#[cfg(target_os = "macos")]
fn pids() -> Vec<u64> {
    // SAFETY: a null buffer asks for the count; then buf holds the size given.
    let n = unsafe { libc::proc_listallpids(std::ptr::null_mut(), 0) };
    if n <= 0 {
        return Vec::new();
    }
    let mut buf = vec![0 as libc::pid_t; n as usize + 64];
    let size = (buf.len() * std::mem::size_of::<libc::pid_t>()) as libc::c_int;
    let n = unsafe { libc::proc_listallpids(buf.as_mut_ptr().cast(), size) };
    buf.truncate(n.max(0) as usize);
    buf.into_iter().map(|p| p as u64).collect()
}

#[cfg(windows)]
fn pids() -> Vec<u64> {
    use windows_sys::Win32::System::ProcessStatus::K32EnumProcesses;
    let mut buf = vec![0u32; 4096];
    loop {
        let mut used = 0u32;
        let size = (buf.len() * 4) as u32;
        // SAFETY: buf holds size bytes; used is written on success.
        if unsafe { K32EnumProcesses(buf.as_mut_ptr(), size, &mut used) } == 0 {
            return Vec::new();
        }
        if used < size {
            buf.truncate(used as usize / 4);
            return buf.into_iter().map(u64::from).collect();
        }
        buf.resize(buf.len() * 2, 0);
    }
}

/// The processes running program, by their executable.
fn running(program: &Path) -> Vec<u64> {
    let want = std::fs::canonicalize(program).unwrap_or_else(|_| program.into());
    pids().into_iter().filter(|pid| exe(*pid) == want).collect()
}

/// Whether process pid runs a program in the app's core store dir.
pub(super) fn in_store(pid: u64, dir: &Path) -> bool {
    let dir = std::fs::canonicalize(dir).unwrap_or_else(|_| dir.into());
    exe(pid).starts_with(dir)
}

/// Changes the installed file at path as a program of this user could: made writable again (its
/// owner may), and a byte added.
fn tamper(path: &Path) -> Result<(), String> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))
            .map_err(|e| e.to_string())?;
    }
    #[cfg(windows)]
    {
        let f =
            crate::private_windows::open_for_access_list(path, false).map_err(|e| e.to_string())?;
        crate::private_windows::make_private(&f, false).map_err(|e| e.to_string())?;
    }
    use std::io::Write;
    std::fs::OpenOptions::new()
        .append(true)
        .open(path)
        .and_then(|mut f| f.write_all(b" "))
        .map_err(|e| format!("{}: {e}", path.display()))
}

fn installed(dir: &Path) -> Vec<String> {
    let mut v: Vec<String> = std::fs::read_dir(dir.join("versions"))
        .into_iter()
        .flatten()
        .flatten()
        .filter(|e| e.path().is_dir())
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .collect();
    v.sort();
    v
}

/// The core the app runs now, from the window's view: (version, pid).
fn current(main: &Webview) -> Result<(Option<String>, Option<u64>, Value), String> {
    let v = call(main, "connection_get", json!({}))?;
    Ok((
        v["core"]["version"].as_str().map(String::from),
        v["core"]["pid"].as_u64(),
        v,
    ))
}

/// Waits for the outcome of the core action after seq, as `core_versions` reports it.
fn outcome(main: &Webview, after: u64) -> Result<Value, String> {
    wait_for("the core action's outcome", || {
        let v = call(main, "core_versions", json!({})).ok()?;
        let o = v.get("outcome")?.clone();
        (o["seq"].as_u64()? > after).then_some(o)
    })
}

fn seq(main: &Webview) -> Result<u64, String> {
    let v = call(main, "core_versions", json!({}))?;
    Ok(v["outcome"]["seq"].as_u64().unwrap_or(0))
}

/// Asks for a core action, checks that its native dialog shows each of shows (what will run),
/// answers it with key, and returns its outcome.
pub(super) fn act(
    main: &Webview,
    cmd: &str,
    args: Value,
    key: Key,
    shows: &[String],
) -> Result<Value, String> {
    let app = main.app_handle();
    let before = seq(main)?;
    call(main, cmd, args)?;
    ui::wait_shown(app, DIALOG)?;
    let texts = ui::texts(app, DIALOG)?;
    let what = shown_what(&texts)
        .ok_or_else(|| format!("the {cmd} dialog shows no action: {}", texts.join("\n")))?;
    for want in shows {
        require(
            what.contains(want.as_str()),
            format!("the {cmd} dialog's action does not say {want}: {what}"),
        )?;
    }
    ui::press(app, DIALOG, key)?;
    outcome(main, before)
}

/// The `what` of the action the dialog shows. The dialog shows the action only as JSON, where
/// the same values come again outside `what` (the executor's digest and contract, the selected
/// files), so the checks read `what` alone, parsed: a Windows path's doubled `\` is undone.
/// The dialog's labels come as one text, the action's JSON among them: the first JSON object in
/// it that has a `what`.
fn shown_what(texts: &[String]) -> Option<String> {
    texts.iter().find_map(|t| {
        t.match_indices('{').find_map(|(i, _)| {
            let v = serde_json::Deserializer::from_str(&t[i..])
                .into_iter::<Value>()
                .next()?
                .ok()?;
            v["what"].as_str().map(String::from)
        })
    })
}

/// The SHA-256 of release version's archive, as the core source's index (the runner's) gives it
/// in the signed descriptor.
fn archive_sha(source: &str, version: &str) -> Result<String, String> {
    let index: Value = serde_json::from_slice(&std::fs::read(source).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())?;
    let release = index["releases"]
        .as_array()
        .and_then(|r| r.iter().find(|r| r["version"] == version))
        .ok_or_else(|| format!("no release {version} in {source}"))?;
    let descriptor: Value = serde_json::from_str(
        release["artifacts"][0]["descriptor"]
            .as_str()
            .unwrap_or_default(),
    )
    .map_err(|e| e.to_string())?;
    descriptor["sha256"]
        .as_str()
        .map(String::from)
        .ok_or_else(|| format!("release {version} has no SHA-256"))
}

/// The SHA-256 of the file at path, in hex.
fn file_sha(path: &Path) -> Result<String, String> {
    use sha2::{Digest, Sha256};
    let bytes = std::fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
    Ok(Sha256::digest(&bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect())
}

/// A refusal before any dialog: the command fails and no dialog opens.
fn refused(main: &Webview, cmd: &str, args: Value) -> Result<String, String> {
    let r = invoke(main, cmd, args)?;
    // No dialog at any point for two seconds after the refusal.
    for _ in 0..20 {
        require(
            !ui::shown(main.app_handle(), DIALOG),
            format!("{cmd} opened a dialog"),
        )?;
        std::thread::sleep(Duration::from_millis(100));
    }
    r.err().ok_or_else(|| format!("{cmd} was not refused"))
}

fn status_version(app: &AppHandle) -> (u16, Option<String>) {
    let caller = app.state::<crate::App>().conn.lock().unwrap().caller();
    let r = caller.call("GET", "/status", None);
    let version = r
        .body
        .as_ref()
        .and_then(|b| b["version"].as_str())
        .map(String::from);
    (r.status, version)
}

pub fn run(app: &AppHandle, main: &Webview) -> Result<(), String> {
    let Ok(source) = std::env::var("PLUXY_APP_TEST_CORE_SOURCE") else {
        return Ok(());
    };
    let custom =
        PathBuf::from(std::env::var("PLUXY_APP_TEST_CORE_CUSTOM").map_err(|e| e.to_string())?);
    let ready =
        PathBuf::from(std::env::var("PLUXY_APP_TEST_TRUST_READY").map_err(|e| e.to_string())?);
    // Where the app keeps them (lib.rs): on Windows in the local application data.
    #[cfg(windows)]
    let data = app.path().app_local_data_dir().map_err(|e| e.to_string())?;
    #[cfg(not(windows))]
    let data = app.path().app_data_dir().map_err(|e| e.to_string())?;
    let cores = data.join("cores");
    let config = data.join("core").join("config.json");
    // What an install's dialog must show: the version and its archive's SHA-256.
    let install = |v: &str| -> Result<Vec<String>, String> {
        Ok(vec![v.to_string(), archive_sha(&source, v)?])
    };

    // 1. Missing core: not connected; no roots: refused before any dialog, nothing installed.
    let (_, _, view) = current(main)?;
    require(
        view["target"].is_null(),
        format!("connected before any core was installed: {view}"),
    )?;
    log(
        "core_missing",
        json!({"state": view["core"]["state"], "connected": !view["target"].is_null(), "installed": installed(&cores)}),
    );
    let detail = refused(main, "core_install", json!({"version": "1.0.0"}))?;
    log(
        "core_no_roots",
        json!({"refused": true, "detail": detail, "installed": installed(&cores), "dialog": false}),
    );
    wait_for("the runner's trust roots", || ready.exists().then_some(()))?;

    // 2. The same install, with roots and confirmed: core 1.0.0 runs and the app connects.
    let o = act(
        main,
        "core_install",
        json!({"version": "1.0.0"}),
        CONFIRM,
        &install("1.0.0")?,
    )?;
    let (version, pid, view) = current(main)?;
    let (status, reported) = status_version(app);
    let pid1 = pid.unwrap_or_default();
    log(
        "core_install",
        json!({"outcome": o["result"], "version": version, "connected": !view["target"].is_null(),
               "signed_in": view["signed_in"], "status": status, "reported": reported,
               "exe_in_app": in_store(pid1, &cores),
               "pid_runs_program": running(&cores.join("versions").join("1.0.0").join(crate::cores::PROGRAM)).contains(&pid1),
               "installed": installed(&cores), "source": source}),
    );

    // 3. A bad signature, refused before any dialog; the same route installed 1.0.0 above.
    let detail = refused(main, "core_install", json!({"version": "2.1.0"}))?;
    log(
        "core_bad_signature",
        json!({"refused": true, "detail": detail, "installed": installed(&cores), "running": current(main)?.0}),
    );

    // 4. v1 to v2: the old process is gone, the config the same.
    let before = std::fs::read(&config).map_err(|e| e.to_string())?;
    let o = act(
        main,
        "core_install",
        json!({"version": "2.0.0"}),
        CONFIRM,
        &install("2.0.0")?,
    )?;
    let (version, pid, _) = current(main)?;
    let pid2 = pid.unwrap_or_default();
    let (status, reported) = status_version(app);
    log(
        "core_update",
        json!({"outcome": o["result"], "version": version, "status": status, "reported": reported,
               "old_alive": alive(pid1), "new_pid": pid2 != pid1,
               "config_kept": std::fs::read(&config).ok() == Some(before.clone()),
               "installed": installed(&cores)}),
    );

    // 5. A candidate that breaks its config and exits: config restored, 2.0.0 again.
    let o = act(
        main,
        "core_install",
        json!({"version": "3.0.0"}),
        CONFIRM,
        &install("3.0.0")?,
    )?;
    let (version, pid, _) = current(main)?;
    let (status, reported) = status_version(app);
    let versions = call(main, "core_versions", json!({}))?;
    log(
        "core_bad_start",
        json!({"outcome": o["result"], "detail": o["detail"], "version": version,
               "status": status, "reported": reported, "old_alive": alive(pid2),
               "running": pid.is_some_and(alive),
               "config_restored": std::fs::read(&config).ok() == Some(before.clone()),
               "failed": versions["failed"]["version"], "installed": installed(&cores)}),
    );

    // 6. Rollback: unknown refused; cancel changes nothing; confirmed runs 1.0.0.
    let detail = refused(main, "core_rollback", json!({"version": "9.9.9"}))?;
    let (_, pid_before, _) = current(main)?;
    let cancel = act(
        main,
        "core_rollback",
        json!({"version": "1.0.0"}),
        CANCEL,
        &["1.0.0".into()],
    )?;
    let (cancel_version, cancel_pid, _) = current(main)?;
    let o = act(
        main,
        "core_rollback",
        json!({"version": "1.0.0"}),
        CONFIRM,
        &["1.0.0".into()],
    )?;
    let (version, _, _) = current(main)?;
    let (status, reported) = status_version(app);
    log(
        "core_rollback",
        json!({"unknown_refused": true, "unknown_detail": detail,
               "cancel": cancel["result"], "cancel_kept": cancel_version.as_deref() == Some("2.0.0") && cancel_pid == pid_before,
               "outcome": o["result"], "version": version, "status": status, "reported": reported,
               "config_kept": std::fs::read(&config).ok() == Some(before.clone())}),
    );

    // 7. A custom program: not run until adopted; the adopted copy runs, not the program.
    let set = call(
        main,
        "connection_set",
        json!({"change": {"mode": "managed", "core_path": custom, "core_source": source}}),
    )?;
    let not_run = running(&custom).is_empty();
    let o = act(
        main,
        "core_adopt",
        json!({}),
        CONFIRM,
        &[custom.display().to_string(), file_sha(&custom)?],
    )?;
    let (version, pid, _) = current(main)?;
    let pid_custom = pid.unwrap_or_default();
    let (status, _) = status_version(app);
    let adopted_in_app = in_store(pid_custom, &cores);
    // While the adopted copy runs, before anything else changes.
    let custom_running = !running(&custom).is_empty();
    let o2 = act(
        main,
        "core_rollback",
        json!({"version": "1.0.0"}),
        CONFIRM,
        &["1.0.0".into()],
    )?;
    let (back, _, _) = current(main)?;
    log(
        "core_adopt",
        json!({"pending": set["core"]["adopt"], "running_before": set["core"]["version"],
               "custom_not_run": not_run, "outcome": o["result"], "version": version,
               "status": status, "exe_in_app": adopted_in_app,
               "custom_running": custom_running,
               "back": back, "back_outcome": o2["result"]}),
    );

    // 8. A changed installed version: refused before any dialog; installed again, it runs.
    // 2.0.0 runs first, so 1.0.0 is the installed version before it, not the running one.
    let o1 = act(
        main,
        "core_install",
        json!({"version": "2.0.0"}),
        CONFIRM,
        &install("2.0.0")?,
    )?;
    let changed = cores
        .join("versions")
        .join("1.0.0")
        .join("bundled")
        .join("index.json");
    tamper(&changed)?;
    let detail = refused(main, "core_rollback", json!({"version": "1.0.0"}))?;
    let o = act(
        main,
        "core_install",
        json!({"version": "1.0.0"}),
        CONFIRM,
        &install("1.0.0")?,
    )?;
    let (version, _, _) = current(main)?;
    let (status, reported) = status_version(app);
    log(
        "core_changed",
        json!({"reinstalled": o1["result"], "refused": true, "detail": detail,
               "repaired": o["result"], "version": version, "status": status,
               "reported": reported, "installed": installed(&cores)}),
    );

    // 9. The running version installed again: written anew and run in a new process.
    let (_, pid_before, _) = current(main)?;
    let o = act(
        main,
        "core_install",
        json!({"version": "1.0.0"}),
        CONFIRM,
        &install("1.0.0")?,
    )?;
    let (version, pid, _) = current(main)?;
    let (status, reported) = status_version(app);
    log(
        "core_repair_running",
        json!({"outcome": o["result"], "detail": o["detail"], "version": version,
               "new_process": pid.is_some() && pid != pid_before,
               "old_gone": pid_before.is_some_and(|p| !alive(p)),
               "status": status, "reported": reported, "installed": installed(&cores)}),
    );
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::shown_what;

    /// The dialog's text as the UI driver reads it: its labels, the action's JSON among them,
    /// with values repeated outside `what`. Only `what` counts, with a Windows path as it is.
    #[test]
    fn only_the_actions_what_counts() {
        let review = serde_json::to_string_pretty(&serde_json::json!({
            "what": "Install core 1.2.0 from C:\\Users\\x\\core.tar.gz (SHA-256 aaa)",
            "chain": [{"id": "pluxy app", "digest": "ccc"}],
            "executor": {"digest": "bbb", "contract": "core 9.9.9"},
            "files": [{"selected": "C:\\other\\core.tar.gz"}],
        }))
        .unwrap();
        let texts = vec![format!("Confirm this action?\n{review}\nConfirm\nCancel")];
        let what = shown_what(&texts).expect("the action's what");
        for shown in ["1.2.0", "C:\\Users\\x\\core.tar.gz", "aaa"] {
            assert!(what.contains(shown), "{shown} not in {what}");
        }
        for elsewhere in ["bbb", "ccc", "9.9.9", "C:\\other\\core.tar.gz"] {
            assert!(
                !what.contains(elsewhere),
                "{elsewhere} taken from outside what"
            );
        }
        assert_eq!(shown_what(&["Confirm\nCancel".to_string()]), None);
    }
}
