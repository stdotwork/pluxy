//! The native test's check of the program host (native-test builds with `artifact-test-trust`
//! only). It drives the real app through the window's own commands and the real native
//! confirmation, with the signed summary tool `native/program.ts` built (its digest and the
//! summary's expected answer are in `expect.json`, worked out there, not by the app), and looks at
//! the real processes, threads and pipes (`proc_native.rs`: `/proc` on Linux, libproc on macOS,
//! a Tool Help snapshot on Windows):
//!
//! - installed, and enabled in the native confirmation, the tool runs no process; a call while it
//!   is disabled is refused and starts none;
//! - three calls from the window at once start it once: each answers the expected summary from
//!   the same process, which leads its own process group (on Windows: its immediate job holds
//!   only it, its child and the `conhost.exe` of its hidden console, and ends them when closed)
//!   with the child it started, has only the contract's environment (not the app's canary; the
//!   app's HOME and its own data folder, 0700 on Linux and macOS; on Windows also the app's
//!   `SystemRoot`, `APPDATA` and `LOCALAPPDATA`, `ToolEnv::system`) and only stdin, stdout and
//!   stderr open (on Windows: its three standard handles are pipes, and a file the app holds
//!   through an inheritable handle meanwhile is not open in it at that handle's value, though a
//!   process std starts has it there), and runs in its package folder;
//! - the command thread that started it has ended while it runs on, with its supervisor thread;
//! - its stdin and stdout pipes are held by the app and the tool only, no web process (Linux and
//!   macOS: Windows lists no process's handles to an unprivileged one);
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
/// The fixture's first argument that makes it print the file a handle value names
/// (`examples/program_fixture/fixture.rs`, `HANDLE`).
#[cfg(windows)]
const HANDLE: &str = "pluxy-fixture-handle";
/// The program commands a page webview must be refused.
const PROGRAM_COMMANDS: &[&str] = &["program_call", "program_cancel", "program_status"];
/// The environment app program contract v0 gives a tool, and nothing else: its three values,
/// the app's HOME and the tool's own data folder; on Windows also `SYSTEM_ENV`, those the app has.
const CONTRACT_ENV: &[&str] = &[
    "HOME",
    "PLUXY_APP_ARTIFACT_SHA256",
    "PLUXY_APP_CONTRACT_VERSIONS",
    "PLUXY_EXTENSION_ID",
    "PLUXY_TOOL_DATA_DIR",
];

/// The app's own system folders a tool gets on Windows (`program::ToolEnv::system`).
#[cfg(windows)]
const SYSTEM_ENV: &[&str] = &["APPDATA", "LOCALAPPDATA", "SystemRoot"];

pub(super) use super::procs::{alive, processes, threads};

/// The tool's program file in its package folder dir.
pub(super) fn program_file(dir: &Path) -> PathBuf {
    dir.join("bin").join(if cfg!(windows) {
        "summary.exe"
    } else {
        "summary"
    })
}

/// Windows: a file the app holds open through an inheritable handle while it starts the tool. The
/// host passes a tool its three standard handles only (`owned::windows`), so the tool must not
/// have this one.
#[cfg(windows)]
struct Canary {
    file: std::fs::File,
    path: PathBuf,
    /// Its final path (`std::fs::canonicalize`, which asks `GetFinalPathNameByHandleW`), in
    /// lower case.
    real: String,
}

#[cfg(windows)]
impl Canary {
    fn open(dir: &Path) -> Result<Self, String> {
        use std::os::windows::io::AsRawHandle;
        use windows_sys::Win32::Foundation::{SetHandleInformation, HANDLE_FLAG_INHERIT};
        let path = dir.join("pluxy-inherit-canary");
        let file = std::fs::File::create(&path).map_err(|e| format!("{}: {e}", path.display()))?;
        // SAFETY: the handle is the open file's own.
        if unsafe {
            SetHandleInformation(
                file.as_raw_handle() as _,
                HANDLE_FLAG_INHERIT,
                HANDLE_FLAG_INHERIT,
            )
        } == 0
        {
            return Err(format!(
                "making the canary's handle inheritable: {}",
                std::io::Error::last_os_error()
            ));
        }
        let real = std::fs::canonicalize(&path)
            .map_err(|e| format!("{}: {e}", path.display()))?
            .to_string_lossy()
            .to_lowercase();
        Ok(Self { file, path, real })
    }

    fn value(&self) -> u64 {
        use std::os::windows::io::AsRawHandle;
        self.file.as_raw_handle() as usize as u64
    }

    /// Whether path, as `GetFinalPathNameByHandleW` gives it, is this file.
    fn named(&self, path: &Value) -> bool {
        path.as_str().is_some_and(|p| p.to_lowercase() == self.real)
    }
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
    let program = program_file(&dir);
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

    // Three calls at once: one start, the expected summary, from the same process. On Windows the
    // app holds a file open through an inheritable handle meanwhile, which the tool must not get.
    #[cfg(windows)]
    let canary = Canary::open(&fixtures)?;
    #[allow(unused_mut)]
    let mut input = json!({"items": items});
    #[cfg(windows)]
    {
        input["handle"] = json!(canary.value());
    }
    let slots: Vec<String> = (0..3)
        .map(|_| {
            start(
                main,
                "program_call",
                json!({"package": ID, "op": "summary", "input": input}),
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
    #[cfg(not(windows))]
    let want: Vec<&str> = CONTRACT_ENV.to_vec();
    // The app's own system folders join them, those it has as full paths, as the tool sorts.
    #[cfg(windows)]
    let want: Vec<&str> = {
        let mut w = CONTRACT_ENV.to_vec();
        w.extend(SYSTEM_ENV.iter().copied().filter(|k| {
            std::env::var_os(k).is_some_and(|v| std::path::Path::new(&v).is_absolute())
        }));
        w.sort();
        w
    };
    require(env == want, format!("the tool's environment: {env:?}"))?;
    // On Windows each system folder the tool has is the app's own, and it has each the app has as
    // a full path (none the app has as a relative one).
    // shortcut: a runner's system folders are all full paths, so no run checks the relative case
    // (left out of the tool's environment, `ToolEnv::system`), nor does a unit test; upgrade when
    // a session can start the app with a relative APPDATA, LOCALAPPDATA or SystemRoot.
    #[cfg(windows)]
    for k in SYSTEM_ENV {
        let app_value = std::env::var_os(k)
            .filter(|v| Path::new(v).is_absolute())
            .map(|v| v.to_string_lossy().into_owned());
        require(
            env.contains(k) == app_value.is_some() && first["system"][k] == json!(app_value),
            format!(
                "the tool's {k}: {}, the app's {app_value:?}",
                first["system"][k]
            ),
        )?;
    }
    // HOME is the app's own (on Windows its profile folder, `USERPROFILE`); the data folder is the
    // tool's, made private beside the vault (on Windows in the local data folder, which does not
    // roam).
    // Windows: read from the app's environment here, not as the host finds it (`home_dir`).
    #[cfg(windows)]
    let (home, data) = (
        std::env::var_os("USERPROFILE")
            .map(PathBuf::from)
            .ok_or("the app has no USERPROFILE")?,
        app.path()
            .app_local_data_dir()
            .map_err(|e| e.to_string())?
            .join("tools")
            .join(ID),
    );
    #[cfg(not(windows))]
    let (home, data) = (
        app.path().home_dir().map_err(|e| e.to_string())?,
        app.path()
            .app_data_dir()
            .map_err(|e| e.to_string())?
            .join("tools")
            .join(ID),
    );
    // As paths: on Windows the package ID's `/` and the folder's `\` are both separators.
    let same = |v: &Value, p: &Path| v.as_str().is_some_and(|v| Path::new(v) == p);
    require(
        same(&first["home"], &home) && same(&first["data_dir"], &data),
        format!(
            "the tool's HOME and data folder: {} and {}, expected {} and {}",
            first["home"],
            first["data_dir"],
            home.display(),
            data.display()
        ),
    )?;
    #[cfg(unix)]
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
        require(
            first["fds"] == json!([0, 1, 2]),
            format!("the tool's open descriptors: {}", first["fds"]),
        )?;
    }
    #[cfg(windows)]
    {
        let m = std::fs::symlink_metadata(&data).map_err(|e| format!("{}: {e}", data.display()))?;
        let f = crate::private_windows::open_for_access_list(&data, true)
            .map_err(|e| format!("{}: {e}", data.display()))?;
        require(
            m.is_dir()
                && !m.is_symlink()
                && crate::private_windows::owned(&f).unwrap_or(false)
                && crate::private_windows::is_private(&f).unwrap_or(false),
            format!(
                "the tool's data folder {} is not this user's private folder",
                data.display()
            ),
        )?;
        require(
            first["std_pipes"] == json!([true, true, true]),
            format!("the tool's standard handles: {}", first["std_pipes"]),
        )?;
        require(
            !canary.named(&first["handle_path"]),
            format!(
                "the tool got the app's inheritable handle {} to {}",
                canary.value(),
                canary.path.display()
            ),
        )?;
        // The control: a process started by std's spawn, which passes on every inheritable
        // handle, has it at that value.
        let mut control = std::process::Command::new(&program);
        control
            .args([HANDLE, &canary.value().to_string()])
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::null());
        let o = crate::owned::windows::std_spawn(&mut control)
            .and_then(|c| c.wait_with_output())
            .map_err(|e| format!("the handle control: {e}"))?;
        let seen = String::from_utf8_lossy(&o.stdout).trim().to_string();
        require(
            canary.named(&json!(seen)),
            format!(
                "the handle control did not find {} at {}: {seen:?}",
                canary.path.display(),
                canary.value()
            ),
        )?;
        out["inherit_control"] = json!(seen);
        drop(canary);
    }
    let real = std::fs::canonicalize(&dir).map_err(|e| e.to_string())?;
    // On Linux and macOS it leads its own process group. On Windows its immediate job is the
    // host's, and ends what it holds when it closes: the tool, its child, and one process more,
    // `%SystemRoot%\System32\conhost.exe`, which Windows starts for the hidden console the host
    // gives the tool (`CREATE_NO_WINDOW`) and puts in the tool's job. Every member the job counts
    // is listed with the program it runs; nothing else may be in it.
    #[cfg(unix)]
    let grouped = first["pgid"] == pid;
    #[cfg(windows)]
    let grouped = {
        let job = &first["job"];
        let members: Vec<(u64, &str)> = job["members"]
            .as_array()
            .map(|a| {
                a.iter()
                    .filter_map(|m| Some((m["pid"].as_u64()?, m["image"].as_str()?)))
                    .collect()
            })
            .unwrap_or_default();
        let listed = job["members"].as_array().map_or(0, Vec::len);
        let conhost = std::env::var("SystemRoot")
            .map(|root| format!(r"{root}\System32\conhost.exe"))
            .map_err(|e| format!("SystemRoot: {e}"))?;
        let (ours, others): (Vec<_>, Vec<_>) =
            members.iter().partition(|(p, _)| *p == pid || *p == child);
        job["kill_on_close"] == true
            && job["assigned"].as_u64() == Some(listed as u64)
            && members.len() == listed
            && ours.iter().map(|(p, _)| *p).collect::<BTreeSet<_>>() == BTreeSet::from([pid, child])
            && others.len() == 1
            && others
                .iter()
                .all(|(_, image)| image.eq_ignore_ascii_case(&conhost))
    };
    // Its folder as it reports it, resolved as the package folder is.
    let cwd = first["cwd"]
        .as_str()
        .and_then(|c| std::fs::canonicalize(c).ok());
    require(
        grouped && cwd.as_deref() == Some(real.as_path()),
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
    #[cfg(unix)]
    for fd in [0, 1] {
        let me = std::process::id() as u64;
        let (pipe, holders) = super::procs::pipe_holders(pid, fd)?;
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
