//! The native test's check of the program host (native-test builds with `artifact-test-trust`
//! only). It drives the real app through the window's own commands and the real GTK native
//! confirmation, with the signed summary tool `native/program.ts` built (its digest and the
//! summary's expected answer are in `expect.json`, worked out there, not by the app), and looks at
//! the real processes, threads and pipes in `/proc`:
//!
//! - installed, and enabled in the native confirmation, the tool runs no process; a call while it
//!   is disabled is refused and starts none;
//! - three calls from the window at once start it once: each answers the expected summary from
//!   the same process, which leads its own process group with the child it started, has only the
//!   contract's environment (not the app's canary; the app's HOME and its own 0700 data folder)
//!   and only stdin, stdout and stderr open, and
//!   runs in its package folder;
//! - the command thread that started it has ended while it runs on, with its supervisor thread;
//! - its stdin and stdout pipes are held by the app and the tool only, no web process;
//! - a call is cancelled from the window, then the next call is answered by the same process;
//! - from the tool's own page webview, every program command is refused by Tauri's command check,
//!   and none reached the tool (its call count);
//! - left alone, it stops after the 30-second idle delay: no process, child or host thread left;
//! - the next call starts it again; disabling it stops it at once;
//! - enabled again and called, its process and child are named for the runner, which checks they
//!   are gone once the app has quit.
//!
//! One line on stderr: `native-test: program <JSON>`.

use super::admission::{dialog_text, ok, press, refused, require, settle, start};
use super::{eval, log, wait_for};
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

pub(super) const ID: &str = "pluxy/summary";
/// The program commands a page webview must be refused.
const PROGRAM_COMMANDS: &[&str] = &["program_call", "program_cancel", "program_status"];
/// The environment app program contract v0 gives a tool, and nothing else: its three values,
/// the app's HOME and the tool's own data folder.
const CONTRACT_ENV: &[&str] = &[
    "HOME",
    "PLUXY_APP_ARTIFACT_SHA256",
    "PLUXY_APP_CONTRACT_VERSIONS",
    "PLUXY_EXTENSION_ID",
    "PLUXY_TOOL_DATA_DIR",
];

/// The processes running program, by /proc: the tool and the child it started.
pub(super) fn processes(program: &Path) -> BTreeSet<u32> {
    std::fs::read_dir("/proc")
        .map(|d| {
            d.filter_map(|e| e.ok())
                .filter_map(|e| e.file_name().to_str()?.parse::<u32>().ok())
                .filter(|pid| {
                    std::fs::read_link(format!("/proc/{pid}/exe")).is_ok_and(|exe| exe == program)
                })
                .collect()
        })
        .unwrap_or_default()
}

/// Whether pid runs and is not a zombie.
pub(super) fn alive(pid: u64) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .ok()
        .and_then(|s| {
            s.rsplit_once(") ")
                .and_then(|(_, rest)| rest.chars().next())
        })
        .is_some_and(|state| state != 'Z')
}

/// The names of this app's threads.
pub(super) fn threads() -> Vec<String> {
    std::fs::read_dir("/proc/self/task")
        .map(|d| {
            d.filter_map(|e| e.ok())
                .filter_map(|e| std::fs::read_to_string(e.path().join("comm")).ok())
                .map(|s| s.trim().to_string())
                .collect()
        })
        .unwrap_or_default()
}

/// The processes holding the pipe that pid has open as fd.
fn pipe_holders(pid: u64, fd: u32) -> Result<(String, BTreeSet<u64>), String> {
    let pipe = std::fs::read_link(format!("/proc/{pid}/fd/{fd}"))
        .map_err(|e| format!("fd {fd} of {pid}: {e}"))?
        .to_string_lossy()
        .into_owned();
    require(
        pipe.starts_with("pipe:"),
        format!("fd {fd} of the tool is {pipe}"),
    )?;
    let mut holders = BTreeSet::new();
    for e in std::fs::read_dir("/proc")
        .map_err(|e| e.to_string())?
        .flatten()
    {
        let Some(p) = e.file_name().to_str().and_then(|s| s.parse::<u64>().ok()) else {
            continue;
        };
        let Ok(fds) = std::fs::read_dir(e.path().join("fd")) else {
            continue;
        };
        if fds
            .flatten()
            .any(|f| std::fs::read_link(f.path()).is_ok_and(|l| l.to_string_lossy() == pipe))
        {
            holders.insert(p);
        }
    }
    Ok((pipe, holders))
}

fn summary(main: &Webview, items: &Value) -> Result<Value, String> {
    ok(
        main,
        "program_call",
        json!({"package": ID, "op": "summary", "input": {"items": items}}),
    )
}

pub(super) fn status(main: &Webview) -> Result<Value, String> {
    ok(main, "program_status", json!({"package": ID}))
}

/// Enables the tool through its native confirmation, confirmed.
pub(super) fn enable(app: &AppHandle, main: &Webview) -> Result<(), String> {
    let slot = start(main, "device_enable", json!({"id": ID, "enabled": true}))?;
    dialog_text(app)?;
    press("alt+o")?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("confirmed native enable: {r}"))
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let sha = expect["sha256"].as_str().unwrap_or_default().to_string();
    let items = expect["items"].clone();
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store")
        .join(&sha);
    let program = dir.join("bin").join("summary");
    let mut out = json!({"program": program});

    // Installed: no process, and a call while disabled is refused and starts none.
    ok(
        main,
        "device_install",
        json!({"registry": "curated", "id": ID, "version": "1.0.0"}),
    )?;
    require(
        program.is_file(),
        format!("{} is not installed", program.display()),
    )?;
    require(
        processes(&program).is_empty(),
        "the tool runs after install",
    )?;
    out["disabled_call"] = json!(refused(
        main,
        "program_call",
        json!({"package": ID, "op": "summary", "input": {"items": items}}),
        "disabled",
    )?);
    require(
        processes(&program).is_empty(),
        "a refused call started the tool",
    )?;
    enable(app, main)?;
    std::thread::sleep(Duration::from_millis(500));
    require(processes(&program).is_empty(), "the tool runs once enabled")?;
    let s = status(main)?;
    require(s["state"] == "idle", format!("status once enabled: {s}"))?;

    // Three calls at once: one start, the expected summary, from the same process.
    let slots: Vec<String> = (0..3)
        .map(|_| {
            start(
                main,
                "program_call",
                json!({"package": ID, "op": "summary", "input": {"items": items}}),
            )
        })
        .collect::<Result<_, _>>()?;
    let mut answers = Vec::new();
    for slot in &slots {
        let r = settle(main, slot)?;
        require(r["ok"] == true, format!("a summary call: {r}"))?;
        answers.push(r["value"].clone());
    }
    let first = answers[0].clone();
    let pid = first["pid"].as_u64().ok_or(format!("no pid: {first}"))?;
    let child = first["child"]
        .as_u64()
        .ok_or(format!("no child: {first}"))?;
    for a in &answers {
        require(
            a["count"] == expect["count"] && a["longest"] == expect["longest"] && a["pid"] == pid,
            format!("a summary answer: {a}, expected {expect}"),
        )?;
    }
    let calls: BTreeSet<u64> = answers.iter().filter_map(|a| a["calls"].as_u64()).collect();
    require(
        calls == BTreeSet::from([1, 2, 3]),
        format!("the tool's call counts: {calls:?}"),
    )?;
    let env: Vec<&str> = first["env"]
        .as_array()
        .map(|a| a.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    require(
        env == CONTRACT_ENV,
        format!("the tool's environment: {env:?}"),
    )?;
    // HOME is the app's own; the data folder is the tool's, made 0700 beside the vault.
    let home = app.path().home_dir().map_err(|e| e.to_string())?;
    let data = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("tools")
        .join(ID);
    require(
        first["home"] == json!(home) && first["data_dir"] == json!(data),
        format!(
            "the tool's HOME and data folder: {} and {}, expected {} and {}",
            first["home"],
            first["data_dir"],
            home.display(),
            data.display()
        ),
    )?;
    {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        let m = std::fs::symlink_metadata(&data).map_err(|e| format!("{}: {e}", data.display()))?;
        require(
            m.is_dir()
                && m.permissions().mode() & 0o777 == 0o700
                && m.uid() == unsafe { libc::geteuid() },
            format!(
                "the tool's data folder {} is not this user's 0700 folder",
                data.display()
            ),
        )?;
    }
    require(
        first["fds"] == json!([0, 1, 2]),
        format!("the tool's open descriptors: {}", first["fds"]),
    )?;
    let real = std::fs::canonicalize(&dir).map_err(|e| e.to_string())?;
    require(
        first["pgid"] == pid && first["cwd"] == json!(real),
        format!("the tool's group and folder: {first}"),
    )?;
    require(
        processes(&program) == BTreeSet::from([pid as u32, child as u32]),
        format!(
            "processes of the tool: {:?}, expected {pid} and {child}",
            processes(&program)
        ),
    )?;
    out["started"] = json!({"pid": pid, "child": child, "env": env, "fds": first["fds"]});

    // The command thread that started it has ended; it runs on, supervised.
    std::thread::sleep(Duration::from_secs(1));
    let names = threads();
    require(
        alive(pid)
            && !names.iter().any(|t| t == "pluxy-prog-call")
            && names.iter().any(|t| t == "pluxy-prog-sup"),
        format!(
            "after the call returned: tool alive {}, threads {names:?}",
            alive(pid)
        ),
    )?;

    // Its pipes: the app's and the tool's only.
    let me = std::process::id() as u64;
    for fd in [0, 1] {
        let (pipe, holders) = pipe_holders(pid, fd)?;
        require(
            holders == BTreeSet::from([me, pid]),
            format!("{pipe} (the tool's fd {fd}) is held by {holders:?}; app {me}, tool {pid}"),
        )?;
    }

    // Cancel a call, then the same process answers the next one.
    let slot = start(
        main,
        "program_call",
        json!({"package": ID, "op": "wait", "input": {}, "ticket": "wait-1"}),
    )?;
    wait_for("the wait call to run", || {
        status(main).ok().filter(|s| s["state"] == "active")
    })?;
    ok(main, "program_cancel", json!({"ticket": "wait-1"}))?;
    let r = settle(main, &slot)?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("cancelled")),
        format!("the cancelled call: {r}"),
    )?;
    out["cancel"] = r["error"].clone();
    let after = summary(main, &items)?;
    require(
        after["pid"] == pid && after["calls"] == 5,
        format!("the call after the cancel: {after}"),
    )?;

    // The tool's own page has no program command; nothing reaches the tool from it.
    let label = ok(
        main,
        "device_page_open",
        json!({"id": ID, "page": "page", "rect": {"x": 0, "y": 0, "width": 400, "height": 300}}),
    )?;
    let label = label
        .as_str()
        .ok_or(format!("page label {label}"))?
        .to_string();
    let wv = wait_for("the tool's page webview", || app.get_webview(&label))?;
    wv.eval(format!(
        "(async () => {{ const out = {{}}; for (const c of {cmds}) {{ try {{ await window.__TAURI_INTERNALS__.invoke(c, {{package: {id}, op: 'summary', input: {{items: []}}, ticket: 'wait-1'}}); out[c] = 'allowed'; }} catch (e) {{ out[c] = String(e); }} }} window.__pluxyProgram = out; }})()",
        cmds = json!(PROGRAM_COMMANDS),
        id = json!(ID),
    ))
    .map_err(|e| e.to_string())?;
    let page = wait_for("the page's program probe", || {
        eval(&wv, "window.__pluxyProgram ?? null").filter(|v| !v.is_null())
    })?;
    for c in PROGRAM_COMMANDS {
        require(
            page[c].as_str().is_some_and(|e| e.contains("not allowed")),
            format!("{c} from the tool's page: {page}"),
        )?;
    }
    out["page"] = page;
    ok(main, "page_close", json!({"label": label}))?;
    let after = summary(main, &items)?;
    require(
        after["pid"] == pid && after["calls"] == 6,
        format!("the call after the page: {after}"),
    )?;

    // Idle: stopped after the idle delay, with its child and the host's threads.
    let idle = Instant::now();
    wait_for("the tool to stop when idle", || {
        processes(&program).is_empty().then_some(())
    })?;
    let stopped = idle.elapsed();
    require(
        stopped >= Duration::from_secs(29) && !alive(pid) && !alive(child),
        format!("the tool stopped {stopped:?} after its last call"),
    )?;
    std::thread::sleep(Duration::from_millis(500));
    let names = threads();
    require(
        !names.iter().any(|t| t.starts_with("pluxy-prog")),
        format!("threads after the idle stop: {names:?}"),
    )?;
    let s = status(main)?;
    require(
        s["state"] == "idle",
        format!("status after the idle stop: {s}"),
    )?;
    out["idle_stop_secs"] = json!(stopped.as_secs_f64());

    // The next call starts it again; disabling it stops it at once.
    let again = summary(main, &items)?;
    let pid2 = again["pid"].as_u64().unwrap_or_default();
    require(
        pid2 != 0 && pid2 != pid && again["calls"] == 1,
        format!("the call after the idle stop: {again}"),
    )?;
    let t = Instant::now();
    ok(main, "device_enable", json!({"id": ID, "enabled": false}))?;
    wait_for("the tool to stop when disabled", || {
        processes(&program).is_empty().then_some(())
    })?;
    require(
        t.elapsed() < Duration::from_secs(10),
        format!("disabling stopped the tool after {:?}", t.elapsed()),
    )?;
    out["disable_stop_secs"] = json!(t.elapsed().as_secs_f64());

    // Enabled again and running when the app quits: the runner checks these are gone.
    enable(app, main)?;
    let last = summary(main, &items)?;
    let (pid3, child3) = (
        last["pid"].as_u64().unwrap_or_default(),
        last["child"].as_u64().unwrap_or_default(),
    );
    require(
        alive(pid3) && alive(child3),
        format!("the tool before quitting: {last}"),
    )?;
    out["quit_pids"] = json!([pid3, child3]);
    out["passed"] = json!(true);
    log("program", out);
    Ok(())
}
