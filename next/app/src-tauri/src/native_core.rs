//! The native test's managed-core steps (native-test builds only). Before the window shows core's
//! pages, it drives the app's own core through the window's commands and the real native
//! confirmation dialog (X input from xdotool), against the signed test artifacts the runner made
//! (`native/run.ts`): `PLUXY_APP_TEST_CORE_SOURCE` names their index.
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
//!    and runs the copy; a rollback then runs 1.0.0 again, for the steps after this one.
//!
//! Each step prints `native-test: core_<step> <JSON>`; the runner checks the values.

use crate::confirmation::native::{key, window};
use crate::native_test::{eval, log, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

const DIALOG: &str = "pluxy native confirmation";
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

fn call(main: &Webview, cmd: &str, args: Value) -> Result<Value, String> {
    invoke(main, cmd, args)?.map_err(|e| format!("{cmd}: {e}"))
}

fn require(ok: bool, what: impl Into<String>) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(what.into())
    }
}

fn alive(pid: u64) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .ok()
        .and_then(|s| s.rsplit_once(") ").map(|(_, r)| !r.starts_with('Z')))
        .unwrap_or(false)
}

fn exe(pid: u64) -> PathBuf {
    std::fs::read_link(format!("/proc/{pid}/exe")).unwrap_or_default()
}

/// The processes running program, by their executable.
fn running(program: &Path) -> Vec<u64> {
    let want = std::fs::canonicalize(program).unwrap_or_else(|_| program.into());
    std::fs::read_dir("/proc")
        .into_iter()
        .flatten()
        .flatten()
        .filter_map(|d| d.file_name().to_str()?.parse::<u64>().ok())
        .filter(|pid| std::fs::read_link(format!("/proc/{pid}/exe")).ok() == Some(want.clone()))
        .collect()
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

/// Asks for a core action, answers its native dialog with keys, and returns its outcome.
fn act(main: &Webview, cmd: &str, args: Value, keys: &str) -> Result<Value, String> {
    let before = seq(main)?;
    call(main, cmd, args)?;
    wait_for("the native dialog", || window(DIALOG))?;
    key(DIALOG, keys)?;
    outcome(main, before)
}

/// A refusal before any dialog: the command fails and no dialog opens.
fn refused(main: &Webview, cmd: &str, args: Value) -> Result<String, String> {
    let r = invoke(main, cmd, args)?;
    // No dialog at any point for two seconds after the refusal.
    for _ in 0..20 {
        require(window(DIALOG).is_none(), format!("{cmd} opened a dialog"))?;
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
    let data = app.path().app_data_dir().map_err(|e| e.to_string())?;
    let cores = data.join("cores");
    let config = data.join("core").join("config.json");

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
    let o = act(main, "core_install", json!({"version": "1.0.0"}), "alt+o")?;
    let (version, pid, view) = current(main)?;
    let (status, reported) = status_version(app);
    let pid1 = pid.unwrap_or_default();
    log(
        "core_install",
        json!({"outcome": o["result"], "version": version, "connected": !view["target"].is_null(),
               "signed_in": view["signed_in"], "status": status, "reported": reported,
               "exe_in_app": exe(pid1).starts_with(&cores),
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
    let o = act(main, "core_install", json!({"version": "2.0.0"}), "alt+o")?;
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
    let o = act(main, "core_install", json!({"version": "3.0.0"}), "alt+o")?;
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
    let cancel = act(main, "core_rollback", json!({"version": "1.0.0"}), "alt+c")?;
    let (cancel_version, cancel_pid, _) = current(main)?;
    let o = act(main, "core_rollback", json!({"version": "1.0.0"}), "alt+o")?;
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
    let o = act(main, "core_adopt", json!({}), "alt+o")?;
    let (version, pid, _) = current(main)?;
    let pid_custom = pid.unwrap_or_default();
    let (status, _) = status_version(app);
    let adopted_exe = exe(pid_custom);
    // While the adopted copy runs, before anything else changes.
    let custom_running = !running(&custom).is_empty();
    let o2 = act(main, "core_rollback", json!({"version": "1.0.0"}), "alt+o")?;
    let (back, _, _) = current(main)?;
    log(
        "core_adopt",
        json!({"pending": set["core"]["adopt"], "running_before": set["core"]["version"],
               "custom_not_run": not_run, "outcome": o["result"], "version": version,
               "status": status, "exe_in_app": adopted_exe.starts_with(&cores),
               "custom_running": custom_running,
               "back": back, "back_outcome": o2["result"]}),
    );
    Ok(())
}
