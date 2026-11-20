//! The native test's check of the execution program and the launcher on Windows and macOS
//! (native-test builds with `artifact-test-trust` only; Linux runs `launcher_native.rs`). It
//! drives the real app through the window's own commands, the real native key entry and
//! confirmations (worked through `ui_driver.rs`) and the tray's Start… menu, with the signed
//! pluxy/execution (the real execution program, built for this system with its `native-test`
//! feature) that `native/launcher.ts` packed, and the fixture program as the agent
//! (`bin/pluxy-agent`, `.exe` on Windows; `bin` first on the app's PATH), which writes what it
//! sees of itself to its record and runs until a file named stop is in its record's folder. The
//! key comes from `keys.json`, made there; the app never chose it. Each launch has a folder of its
//! own in `work`. Each refusal has a positive control on the same route:
//!
//! - outside (Windows): the app runs outside any job that keeps a detached launch in it
//!   (`native/platform-tools.ts` starts it outside the runner's job);
//! - state: before the execution program is enabled the launcher says it is unavailable; once
//!   it is enabled, available;
//! - secret: a key typed into native key entry is kept in the vault;
//! - terminals: Automatic first, then this system's own: PowerShell, Command Prompt (each from
//!   System32) and Console window on Windows, Terminal on macOS;
//! - launch: the window's launcher prepares a direct, managed launch of the agent, found by name
//!   on the search path, through the execution program, setting one variable; the native
//!   confirmation names the program, the agent as found and the lifetime; remembered, the agent
//!   runs with exactly that path and its arguments as its argv, in the cwd, with only the host's
//!   session values (`spawn::BASE`, `LC_*`) as the app has them (PATH on macOS with the client
//!   folders that exist added) and the variable set (not the canary variable the session gave
//!   the app); the launcher lists it with its executable, arguments, cwd and source, and its
//!   client's recent folders start with the cwd;
//! - one-click: the launcher's Start of it runs it again with no confirmation and shows the
//!   host's launch notice; a second Start at once is refused;
//! - terminal kinds: a detached launch of the agent in each terminal kind (Windows `console`,
//!   `cmd`, `powershell`; macOS `terminal`, a real Terminal window) through the execution
//!   program's terminal mode; the confirmation names that mode and kind, the agent, the cwd and
//!   the detached lifetime; the agent runs with exactly its argv, in its cwd, with the launch's
//!   environment. On Windows it runs in a console window it shares with the execution program it
//!   was started through, in no job; once it ended, that window's after-exit step ran (the native
//!   test's build of the execution program: the console kind writes `held` instead of waiting for
//!   Enter, Command Prompt and PowerShell write their working folder) and every process of the
//!   window ended. On macOS it runs in Terminal's window (Terminal's `TERM_PROGRAM`, a terminal on
//!   its input, Terminal's own terminal names and PATH), in no session of the app's;
//! - tray: the first kind's launch, remembered, started from the tray's Start… item, runs again
//!   with no confirmation. On Windows the click is the menu's own: the `WM_COMMAND` with the
//!   item's command ID that Windows sends the tray icon's window once a person picks it, which the
//!   menu library turns into the app's menu event. On macOS the menu shows only while a person
//!   holds it open, so the check calls the menu event's handler (`tray_menu_event`) with the
//!   item's ID;
//! - npm shim (Windows): a terminal launch, Automatic, of `pluxy-node-agent` (an npm command shim
//!   found by name) runs the first `node.exe` on the app's PATH (Node, not Bun) with the shim's
//!   script and the launch's arguments, in a console; the confirmation names node.exe and the
//!   script; Automatic's PowerShell ran after it;
//! - direct: a detached direct launch of the agent by its path (the control of the refused
//!   `.exe` below), setting one variable and giving the vault secret by reference: the
//!   confirmation names the variable, the secret's label and the agent, never the key; the agent
//!   gets both, in no job (Windows) or in a session of its own that is not the app's child (macOS);
//! - stdin: a detached direct launch with the key on its stdin: the agent reads it there and has
//!   it nowhere in its environment; the same launch as a terminal launch is confirmed and then
//!   refused by the execution program (a terminal's input is its keyboard), and nothing runs;
//! - managed terminal: the first kind's launch with a managed lifetime is refused before any
//!   confirmation (the same launch, detached, ran above);
//! - programs (Windows), each refused before any confirmation: a batch file that is no shim (the
//!   shim beside it ran above), a `.exe` that is no program (the agent's `.exe` by its path ran
//!   above), `cmd.exe` with arguments (with none it asks; cancelled);
//! - forget: the forgotten launches leave the launcher, and the Start… menu shows only "No
//!   remembered launches";
//! - disable: disabling the execution program with its managed runtimes running asks; cancelled,
//!   they run on; confirmed, the package is disabled, the managed launches' processes end and the
//!   detached agents run on.
//!
//! The session (`native/platform-tools.ts`) ends the detached agents after the app quit, by stop
//! files, and on macOS quits Terminal when this session started it.
//!
//! One line on stderr: `native-test: launcher <JSON>`.

use super::admission::{dialog_text, ok, press, require, settle, start};
use super::procs::{alive, processes};
use super::routing::{enable, install, showing};
use super::vault::type_key;
use super::{eval, log, ui, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const EXECUTION: &str = "pluxy/execution";
const AGENT: &str = "pluxy-fixture-agent";
const CLIENT: &str = "Fixture agent";
const TERMINAL_CLIENT: &str = "Fixture terminal";
const NOTICE: &str = "pluxy launch notice";
/// The vault secret's label.
const LABEL: &str = "fixture launcher key";
/// The variable the direct launch gives the secret in.
const SECRET_NAME: &str = "FIXTURE_LAUNCH_KEY";
/// The variable the launches set, and its value.
const ASKED: (&str, &str) = ("FIXTURE_LAUNCH_VALUE", "set-by-the-launch");
/// The variable the session gives the app (`native/platform-tools.ts`), which no launch may get.
const CANARY: &str = "PLUXY_TEST_LAUNCHER_CANARY";
/// The terminal kinds this system offers besides Automatic (`app-tools` README, `execution`); the
/// first one's launch is remembered for the tray.
#[cfg(windows)]
const KINDS: &[&str] = &["console", "cmd", "powershell"];
#[cfg(target_os = "macos")]
const KINDS: &[&str] = &["terminal"];
/// The names a launch in Terminal's window takes from that window, not the launch: what Terminal
/// tells programs about itself, and its shell's PATH (`app-tools` README, `execution`).
#[cfg(target_os = "macos")]
const WINDOW_NAMES: &[&str] = &[
    "TERM",
    "COLORTERM",
    "TERM_PROGRAM",
    "TERM_PROGRAM_VERSION",
    "TERM_SESSION_ID",
    "TERMINFO",
    "LC_TERMINAL",
    "LC_TERMINAL_VERSION",
    "PATH",
];
#[cfg(windows)]
const WINDOW_NAMES: &[&str] = &[];
/// The folders a launch's PATH gets on macOS after the app's own, when they exist and it lacks
/// them (app-host README, "What a launch gets"; `spawn::with_client_folders`; `~` is the app's
/// HOME): kept here apart, so the session's expectation does not come from the code it checks.
#[cfg(target_os = "macos")]
const CLIENT_FOLDERS: &[&str] = &[
    "~/.local/bin",
    "~/.npm-global/bin",
    "~/.bun/bin",
    "~/.cargo/bin",
    "~/.volta/bin",
    "~/Library/pnpm",
    "~/bin",
    "/opt/homebrew/bin",
    "/usr/local/bin",
];

/// path without Windows' `\\?\` prefix on a drive path, as a person would type it (what
/// `canonicalize` gives there); unchanged elsewhere.
fn plain(path: PathBuf) -> PathBuf {
    #[cfg(windows)]
    if let Some(rest) = path.to_str().and_then(|s| s.strip_prefix(r"\\?\")) {
        if rest.as_bytes().get(1) == Some(&b':') {
            return PathBuf::from(rest);
        }
    }
    path
}

fn canonical(path: &Path) -> Result<PathBuf, String> {
    std::fs::canonicalize(path)
        .map(plain)
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// Whether a (a path some process reported) names the same file or folder as b.
fn same_file(a: &str, b: &Path) -> bool {
    match (canonical(Path::new(a.trim())), canonical(b)) {
        (Ok(x), Ok(y)) => x == y,
        _ => false,
    }
}

/// s as the native confirmation shows it: inside its JSON, where a `\` is `\\`.
fn shown(s: &str) -> String {
    let j = json!(s).to_string();
    j[1..j.len() - 1].to_string()
}

/// The native confirmation's text once it names every one of needles.
fn dialog_with(app: &AppHandle, needles: &[&str]) -> Result<String, String> {
    let mut last = String::new();
    wait_for(&format!("a native confirmation naming {needles:?}"), || {
        let t = dialog_text(app).ok()?;
        last.clone_from(&t);
        needles.iter().all(|n| t.contains(n)).then_some(t)
    })
    .map_err(|e| format!("{e}; it said: {last}"))
}

/// Waits for the answer of the call in slot, failing if a native confirmation shows meanwhile.
fn unasked(main: &Webview, slot: &str) -> Result<Value, String> {
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(r) = eval(
            main,
            &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
        )
        .filter(|v| !v.is_null())
        {
            return Ok(r);
        }
        if showing() {
            press("alt+c")?;
            return Err("a native confirmation showed for a remembered launch".into());
        }
        require(Instant::now() < deadline, "no answer")?;
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// The launcher's new launch with input refused, its error saying says, before any native
/// confirmation shows. One that shows is cancelled and fails the check.
fn refused_unasked(main: &Webview, input: Value, says: &str) -> Result<String, String> {
    let r = unasked(
        main,
        &start(main, "launcher_launch", json!({ "launch": input.clone() }))?,
    )?;
    let error = r["error"].as_str().unwrap_or_default().to_string();
    require(
        r["ok"] == false && error.contains(says),
        format!("the launch {input} should be refused with {says:?}: {r}"),
    )?;
    Ok(error)
}

/// The launcher's new launch with input, its native confirmation (naming every one of needles and
/// none of hidden) confirmed, remembered if remember; the command's answer.
fn launch_new(
    app: &AppHandle,
    main: &Webview,
    input: Value,
    needles: &[&str],
    hidden: &[&str],
    remember: bool,
) -> Result<Value, String> {
    let slot = start(main, "launcher_launch", json!({ "launch": input }))?;
    let text = dialog_with(app, needles)?;
    if let Some(h) = hidden.iter().find(|h| text.contains(**h)) {
        press("alt+c")?;
        return Err(format!(
            "the launch's confirmation shows a value it must not ({} characters)",
            h.len()
        ));
    }
    if remember {
        press("alt+r")?;
    }
    press("alt+o")?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
    Ok(r["value"].clone())
}

/// The record the agent writes at path, once it is there and names a process other than not.
fn record(path: &Path, not: u64) -> Result<Value, String> {
    wait_for(&format!("the agent's record {}", path.display()), || {
        std::fs::read(path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .filter(|r| r["pid"].as_u64().is_some_and(|p| p != not))
    })
}

/// Ends the agents whose records are in dir: the file they wait for.
fn stop(dir: &Path) -> Result<(), String> {
    std::fs::write(dir.join("stop"), "").map_err(|e| format!("{}: {e}", dir.display()))
}

/// The launch notice's text once it names every one of needles.
fn notice(app: &AppHandle, needles: &[&str]) -> Result<String, String> {
    let mut last = String::new();
    wait_for(&format!("a launch notice naming {needles:?}"), || {
        let t = ui::texts(app, NOTICE).ok()?.join("\n");
        last.clone_from(&t);
        needles.iter().all(|n| t.contains(n)).then_some(t)
    })
    .map_err(|e| format!("{e}; the notice said: {last}"))
}

/// Whether two environment names are the same: Windows compares them ignoring case.
fn same_name(a: &str, b: &str) -> bool {
    if cfg!(windows) {
        a.eq_ignore_ascii_case(b)
    } else {
        a == b
    }
}

/// The PATH a launch gets: the app's own; on macOS followed by the client folders that exist and
/// it lacks.
fn launch_path() -> Option<String> {
    let path = std::env::var_os("PATH")?;
    #[cfg(target_os = "macos")]
    let path = {
        let home = std::env::var_os("HOME")
            .map(PathBuf::from)
            .filter(|h| h.is_absolute());
        let mut entries: Vec<PathBuf> = std::env::split_paths(&path).collect();
        for f in CLIENT_FOLDERS {
            let d = match (f.strip_prefix("~/"), &home) {
                (Some(rest), Some(h)) => h.join(rest),
                (Some(_), None) => continue,
                (None, _) => PathBuf::from(f),
            };
            if d.is_dir() && !entries.contains(&d) {
                entries.push(d);
            }
        }
        std::env::join_paths(entries).ok()?
    };
    Some(path.to_string_lossy().into_owned())
}

/// The environment an agent got (env, its record's): the host's session values the app has, each
/// as the app has it (PATH as `launch_path`), and the values set, and nothing else; with window,
/// the names of `WINDOW_NAMES` too, with the window's values. Never the session's canary. On
/// macOS CoreFoundation, which the agent links, sets `__CF_USER_TEXT_ENCODING` in its own
/// environment as it starts, so that value is the agent's own.
fn check_environment(env: &Value, set: &[(&str, &str)], window: bool) -> Result<(), String> {
    let own = |name: &str| cfg!(target_os = "macos") && name == "__CF_USER_TEXT_ENCODING";
    let from_window = |name: &str| window && WINDOW_NAMES.iter().any(|w| same_name(w, name));
    let base = |name: &str| {
        crate::spawn::BASE.iter().any(|b| same_name(b, name)) || name.starts_with("LC_")
    };
    let map = env
        .as_object()
        .ok_or(format!("no environment in the record: {env}"))?;
    for name in map.keys() {
        require(
            base(name) || from_window(name) || set.iter().any(|(k, _)| same_name(k, name)),
            format!("the agent got {name}, which nothing selected or asked for"),
        )?;
    }
    // Each session value the app has, as the app has it.
    for (k, v) in std::env::vars_os() {
        let k = k.to_string_lossy();
        if !base(&k) || own(&k) || from_window(&k) {
            continue;
        }
        let want = if same_name(&k, "PATH") {
            launch_path()
        } else {
            Some(v.to_string_lossy().into_owned())
        };
        let got = map.iter().find(|(n, _)| same_name(n, &k)).map(|(_, v)| v);
        require(
            got.and_then(Value::as_str) == want.as_deref(),
            format!("the agent's {k} is not the launch's: {got:?}, expected {want:?}"),
        )?;
    }
    for (k, v) in set {
        require(
            map.iter()
                .any(|(n, got)| same_name(n, k) && got.as_str() == Some(v)),
            format!("the agent did not get the {k} the launch set"),
        )?;
    }
    require(
        map.keys().any(|k| same_name(k, "PATH")),
        "the agent has no PATH",
    )?;
    let canary = std::env::var(CANARY).map_err(|_| format!("the app has no {CANARY}"))?;
    require(
        !map.keys().any(|k| same_name(k, CANARY)) && !env.to_string().contains(&canary),
        format!("the agent got the app's {CANARY}"),
    )
}

/// Windows: clicks the tray's Start… item labelled label as Windows reports a person's pick of
/// it: the `WM_COMMAND` with the item's command ID (read from the submenu's own menu handle)
/// posted to the tray icon's window, whose menu library turns it into the app's menu event.
#[cfg(windows)]
fn tray_click(app: &AppHandle, label: &str) -> Result<(), String> {
    use tauri::menu::ContextMenu;
    use windows::Win32::Foundation::{HWND, LPARAM, WPARAM};
    use windows::Win32::UI::WindowsAndMessaging::{
        GetMenuItemCount, GetMenuItemID, GetMenuStringW, PostMessageW, HMENU, MF_BYPOSITION,
        WM_COMMAND,
    };
    let menu = crate::launcher::start_menu().ok_or("no Start… menu")?;
    let hmenu = HMENU(menu.hpopupmenu().map_err(|e| e.to_string())? as _);
    let tray = app.tray_by_id("pluxy").ok_or("no tray icon")?;
    let hwnd = tray
        .with_inner_tray_icon(|t| t.window_handle() as isize)
        .map_err(|e| e.to_string())?;
    // SAFETY: hmenu is the live submenu's handle; buf is as long as said.
    let n = unsafe { GetMenuItemCount(Some(hmenu)) };
    let mut found = Vec::new();
    for i in 0..n.max(0) {
        let mut buf = [0u16; 512];
        let len = unsafe { GetMenuStringW(hmenu, i as u32, Some(&mut buf), MF_BYPOSITION) };
        let text = String::from_utf16_lossy(&buf[..len.max(0) as usize]);
        if text == label {
            let id = unsafe { GetMenuItemID(hmenu, i) };
            // SAFETY: hwnd is the tray icon's window, alive as long as the tray icon.
            return unsafe {
                PostMessageW(
                    Some(HWND(hwnd as _)),
                    WM_COMMAND,
                    WPARAM(id as usize),
                    LPARAM(0),
                )
            }
            .map_err(|e| format!("posting the item's command: {e}"));
        }
        found.push(text);
    }
    Err(format!("no Start… item {label:?}: {found:?}"))
}

/// Clicks the tray's Start… item labelled label (`tray_click` on Windows; on macOS the menu
/// event's handler with the item's ID, which items gives).
fn tray_start(
    app: &AppHandle,
    items: &[(String, String, bool)],
    label: &str,
) -> Result<(), String> {
    #[cfg(windows)]
    {
        let _ = items;
        tray_click(app, label)
    }
    #[cfg(not(windows))]
    {
        let id = items
            .iter()
            .find(|(_, text, _)| text == label)
            .map(|(id, _, _)| id.clone())
            .ok_or(format!("no Start… item {label:?}: {items:?}"))?;
        let handle = app.clone();
        app.run_on_main_thread(move || crate::tray_menu_event(&handle, &id))
            .map_err(|e| e.to_string())
    }
}

/// The tray's Start… items once they satisfy want.
fn tray_items(
    what: &str,
    want: impl Fn(&[(String, String, bool)]) -> bool,
) -> Result<Vec<(String, String, bool)>, String> {
    let mut last = None;
    wait_for(what, || {
        let items = crate::launcher::tray_items();
        last.clone_from(&items);
        items.filter(|i| want(i))
    })
    .map_err(|e| format!("{e}; the Start… menu had {last:?}"))
}

fn entries(main: &Webview) -> Result<Vec<Value>, String> {
    let s = ok(main, "launcher_state", json!({}))?;
    Ok(s["entries"].as_array().cloned().unwrap_or_default())
}

fn enabled(main: &Webview) -> Result<bool, String> {
    let list = ok(main, "device_list", json!({}))?;
    Ok(list["packages"]
        .as_array()
        .and_then(|a| a.iter().find(|p| p["id"] == EXECUTION).cloned())
        .is_some_and(|p| p["enabled"] == true))
}

/// The process IDs a record names: the agent's and its group member's.
fn pids(r: &Value) -> Vec<u64> {
    [&r["pid"], &r["child"]]
        .iter()
        .filter_map(|p| p.as_u64())
        .collect()
}

/// Windows: the agent's console (its record r's): it has a window, and the agent shares it with
/// a process running helper (the execution program it was started through); the console's
/// processes.
#[cfg(windows)]
fn in_helpers_console(r: &Value, helper: &Path) -> Result<Vec<u64>, String> {
    let c = &r["console"];
    let attached: Vec<u64> = c["processes"]
        .as_array()
        .map(|a| a.iter().filter_map(Value::as_u64).collect())
        .unwrap_or_default();
    require(
        c["window"] == true && attached.contains(&r["pid"].as_u64().unwrap_or(0)),
        format!("the agent has no console window of its own: {c}"),
    )?;
    let helpers = processes(helper);
    require(
        attached.iter().any(|p| helpers.contains(&(*p as u32))),
        format!(
            "no process on the agent's console runs {}: {c}, those running it are {helpers:?}",
            helper.display()
        ),
    )?;
    require(
        r["in_job"] == false,
        format!("the agent runs in a job: {}", r["in_job"]),
    )?;
    Ok(attached)
}

/// Windows: the after-exit step of the console window of a terminal launch of kind in dir, once
/// the agents there are told to stop: `held` (console) or the window's working folder (Command
/// Prompt, PowerShell), written by the execution program's native-test build; then every process
/// on the window's console (attached) ends. Its text.
#[cfg(windows)]
fn after_exit(kind: &str, dir: &Path, attached: &[u64]) -> Result<String, String> {
    let file = dir.join("pluxy-after-exit");
    require(
        !file.exists(),
        format!("the {kind} window ran its after-exit step while its agent ran"),
    )?;
    stop(dir)?;
    let text = wait_for(&format!("the {kind} window's after-exit step"), || {
        let t = std::fs::read_to_string(&file).ok()?;
        // The shells write the folder and a line break, the console kind `held` alone.
        (t == "held" || t.ends_with('\n')).then_some(t)
    })?;
    if kind == "console" {
        require(
            text == "held",
            format!("the console window's after-exit step wrote {text:?}, not held"),
        )?;
    } else {
        require(
            same_file(&text, dir),
            format!(
                "the {kind} window's after-exit step wrote {text:?}, not its folder {}",
                dir.display()
            ),
        )?;
    }
    wait_for(&format!("the {kind} window's processes to end"), || {
        attached.iter().all(|p| !alive(*p)).then_some(())
    })?;
    Ok(text)
}

/// macOS: the agent (its record r) runs in Terminal's window: Terminal's `TERM_PROGRAM`, a
/// terminal on its input, and in no session of the app's.
#[cfg(target_os = "macos")]
fn in_terminal(r: &Value) -> Result<(), String> {
    require(
        r["env"]["TERM_PROGRAM"] == "Apple_Terminal",
        format!(
            "the agent's TERM_PROGRAM is {}, not Terminal's",
            r["env"]["TERM_PROGRAM"]
        ),
    )?;
    require(
        r["tty"].as_str().is_some_and(|t| t.starts_with("/dev/tty")),
        format!("the agent has no terminal on its input: {}", r["tty"]),
    )?;
    // SAFETY: getsid(0) and getpid have no preconditions.
    let (sid, me) = unsafe { (libc::getsid(0), libc::getpid()) };
    require(
        r["sid"].as_i64() != Some(i64::from(sid)) && r["ppid"].as_i64() != Some(i64::from(me)),
        format!(
            "the agent runs in the app's session or as its child: {} {}",
            r["sid"], r["ppid"]
        ),
    )
}

/// The first node.exe on the app's PATH, as the execution program finds Node for an npm shim
/// with no node.exe beside it.
#[cfg(windows)]
fn first_node() -> Result<PathBuf, String> {
    let path = std::env::var_os("PATH").ok_or("the app has no PATH")?;
    let found = std::env::split_paths(&path)
        .filter(|d| d.is_absolute())
        .map(|d| d.join("node.exe"))
        .find(|p| p.is_file())
        .ok_or("no node.exe on the app's PATH")?;
    canonical(&found)
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let keys: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("keys.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let key = keys["key"].as_str().ok_or("no key")?.to_string();
    let execution_version =
        std::fs::read_to_string(fixtures.join("execution-version")).unwrap_or_default();
    // The agent as the execution program finds it by name: in bin, as the app's PATH has it (the
    // session puts the fixtures' bin there as this path), with Windows' `.exe`.
    let bin = fixtures.join("bin");
    let named = bin.join(if cfg!(windows) {
        "pluxy-agent.exe"
    } else {
        "pluxy-agent"
    });
    let agent = canonical(&named)?;
    let work = fixtures.join("work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let work = canonical(&work)?;
    let s = |p: &Path| p.to_string_lossy().into_owned();
    // A launch's own folder in work.
    let folder = |name: &str| -> Result<PathBuf, String> {
        let d = work.join(name);
        std::fs::create_dir_all(&d).map_err(|e| format!("{}: {e}", d.display()))?;
        canonical(&d)
    };
    let mut out = json!({"platform": std::env::consts::OS});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    // The agent's processes: what it records as its own and its group member.
    let running = |r: &Value| {
        let now = processes(&agent);
        pids(r).iter().all(|p| now.contains(&(*p as u32))) && pids(r).len() == 2
    };

    // ---- outside: a detached launch can leave the app's job
    #[cfg(windows)]
    step("outside", {
        let b = crate::owned::windows::breakaway();
        out["breakaway"] = json!(format!("{b:?}"));
        require(
            b.is_ok(),
            format!(
                "the app runs in a job no detached launch may leave ({b:?}); the session must start it outside the runner's job"
            ),
        )
    })?;

    // ---- state: no execution program yet
    step(
        "state",
        (|| {
            let st = ok(main, "launcher_state", json!({}))?;
            require(
                st["available"] == false && st["entries"] == json!([]),
                format!("the launcher before the execution program is enabled: {st}"),
            )
        })(),
    )?;

    // ---- secret: the key the direct and stdin launches get
    let mut secret = String::new();
    step(
        "secret",
        (|| {
            let slot = start(main, "vault_secret_add", json!({"label": LABEL}))?;
            type_key("pluxy secret", &key)?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("vault_secret_add: {r}"))?;
            secret = r["value"]["id"].as_str().unwrap_or_default().to_string();
            require(secret.starts_with("s-"), format!("the new secret: {r}"))
        })(),
    )?;

    step(
        "enable",
        (|| {
            install(main, "curated", EXECUTION, execution_version.trim())?;
            enable(app, main, EXECUTION)?;
            let st = ok(main, "launcher_state", json!({}))?;
            require(
                st["available"] == true,
                format!("the launcher once enabled: {st}"),
            )
        })(),
    )?;

    step(
        "terminals",
        (|| {
            let t = ok(main, "launcher_terminals", json!({}))?;
            let list = t["terminals"].as_array().cloned().unwrap_or_default();
            out["terminals"] = json!(list.len());
            let ids: Vec<&str> = list.iter().filter_map(|a| a["id"].as_str()).collect();
            let labels: Vec<&str> = list.iter().filter_map(|a| a["label"].as_str()).collect();
            #[cfg(windows)]
            let (want_ids, want_labels, paths) = {
                let system = PathBuf::from(
                    std::env::var_os("SystemRoot").ok_or("the app has no SystemRoot")?,
                )
                .join("System32");
                (
                    ["automatic", "powershell", "cmd", "console"],
                    [
                        "Automatic",
                        "PowerShell",
                        "Command Prompt",
                        "Console window",
                    ],
                    vec![
                        (
                            "powershell",
                            system.join(r"WindowsPowerShell\v1.0\powershell.exe"),
                        ),
                        ("cmd", system.join("cmd.exe")),
                    ],
                )
            };
            #[cfg(target_os = "macos")]
            let (want_ids, want_labels, paths) = {
                let terminal = [
                    "/System/Applications/Utilities/Terminal.app",
                    "/Applications/Utilities/Terminal.app",
                ]
                .into_iter()
                .find(|p| Path::new(p).is_dir())
                .ok_or("this Mac has no Terminal")?;
                (
                    ["automatic", "terminal"],
                    ["Automatic", "Terminal"],
                    vec![("terminal", PathBuf::from(terminal))],
                )
            };
            require(
                ids == want_ids && labels == want_labels,
                format!("the terminals: {t}, expected {want_ids:?} {want_labels:?}"),
            )?;
            for (id, path) in paths {
                let got = list
                    .iter()
                    .find(|a| a["id"] == id)
                    .and_then(|a| a["path"].as_str())
                    .unwrap_or_default();
                require(
                    same_file(got, &path),
                    format!("the terminal {id} is at {got:?}, not {}", path.display()),
                )?;
            }
            Ok(())
        })(),
    )?;

    // ---- launch: a remembered, managed, direct launch
    let mut entry = String::new();
    let mut first = Value::Null;
    let managed = folder("managed")?;
    let rec = managed.join("agent");
    step(
        "launch",
        (|| {
            let v = launch_new(
                app,
                main,
                json!({
                    "client": CLIENT, "kind": "direct", "program": "pluxy-agent",
                    "args": [AGENT, rec], "cwd": managed, "lifetime": "managed",
                    "environment": {"set": {ASKED.0: ASKED.1}},
                }),
                &[EXECUTION, "launch", &shown(&s(&named)), "Lifetime: managed"],
                &[],
                true,
            )?;
            entry = v["entry"].as_str().unwrap_or_default().to_string();
            require(
                !entry.is_empty() && v["output"]["runtime"].as_str().is_some(),
                format!("no launcher entry or runtime for a remembered managed launch: {v}"),
            )?;
            first = record(&rec, 0)?;
            let cwd = first["cwd"]
                .as_str()
                .and_then(|c| canonical(Path::new(c)).ok());
            require(
                first["argv"] == json!([s(&named), AGENT, rec]) && cwd.as_ref() == Some(&managed),
                format!(
                    "the agent's argv and cwd: {} {}, expected [{}, {AGENT}, {}] and {}",
                    first["argv"],
                    first["cwd"],
                    named.display(),
                    rec.display(),
                    managed.display()
                ),
            )?;
            check_environment(&first["env"], &[ASKED], false)?;
            require(
                running(&first),
                format!(
                    "the agent's processes {}, {} are not running {}: {:?}",
                    first["pid"],
                    first["child"],
                    agent.display(),
                    processes(&agent)
                ),
            )?;
            #[cfg(windows)]
            require(
                first["in_job"] == true,
                format!("the managed agent is in no job: {}", first["in_job"]),
            )?;
            let e = entries(main)?
                .into_iter()
                .find(|e| e["id"] == entry.as_str())
                .unwrap_or_default();
            require(
                e["client"] == CLIENT
                    && e["executable"] == json!(s(&named))
                    && e["argv"] == json!([AGENT, rec])
                    && e["cwd"] == json!(managed)
                    && e["source"]["package"] == EXECUTION,
                format!("the launcher's entry: {e}"),
            )?;
            let st = ok(main, "launcher_state", json!({}))?;
            require(
                st["history"][CLIENT]
                    .as_array()
                    .is_some_and(|h| h.first() == Some(&json!(managed))),
                format!("the launcher's recent folders: {}", st["history"]),
            )?;
            let label = format!("{CLIENT} in {}", s(&managed));
            let items = tray_items("the Start… menu to list the launch", |i| {
                i.iter()
                    .any(|(id, text, on)| *id == format!("launch:{entry}") && *text == label && *on)
            })?;
            out["tray_after_launch"] = json!(items.len());
            Ok(())
        })(),
    )?;
    let pid1 = first["pid"].as_u64().unwrap_or(0);

    // ---- one-click from the window's launcher
    let mut again = Value::Null;
    step(
        "one-click",
        (|| {
            let slot = start(main, "launcher_start", json!({"id": entry}))?;
            let r = unasked(main, &slot)?;
            require(r["ok"] == true, format!("the one-click start: {r}"))?;
            again = record(&rec, pid1)?;
            require(
                running(&again),
                format!("the one-click start's agent: {again}"),
            )?;
            notice(app, &[CLIENT, "Started"])?;
            // At once again: one start per launch per second.
            let r = unasked(main, &start(main, "launcher_start", json!({"id": entry}))?)?;
            require(
                r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("second")),
                format!("a second start at once: {r}"),
            )
        })(),
    )?;
    let managed_pids: Vec<u64> = pids(&first).into_iter().chain(pids(&again)).collect();

    // ---- terminal kinds, and the tray's start of the first one
    let mut tentry = String::new();
    // The execution program the terminal launches run (their confirmed executable).
    let mut helper = PathBuf::new();
    let mut kinds = json!({});
    // The launch of the agent in terminal kind, recording in dir.
    let terminal_input = |kind: &str, dir: &Path| {
        json!({
            "client": TERMINAL_CLIENT, "kind": "terminal", "program": "pluxy-agent",
            "args": [AGENT, dir.join("agent")], "cwd": dir, "terminal": kind,
            "environment": {"set": {ASKED.0: ASKED.1}},
        })
    };
    // What a terminal launch's confirmation names of kind, besides the agent and folder.
    let kind_needles = |kind: &str| -> Vec<String> {
        let mut n = vec![
            EXECUTION.to_string(),
            "\"terminal\"".into(),
            "\"--with\"".into(),
            format!("\"{kind}\""),
            "Lifetime: detached".into(),
        ];
        if cfg!(target_os = "macos") {
            n.push("\"--app\"".into());
        }
        n
    };
    for (i, kind) in KINDS.iter().enumerate() {
        let dir = folder(kind)?;
        let rec = dir.join("agent");
        step(
            &format!("terminal {kind}"),
            (|| {
                let mut needles = kind_needles(kind);
                needles.extend([shown(&s(&named)), shown(&s(&dir))]);
                let needles: Vec<&str> = needles.iter().map(String::as_str).collect();
                let v = launch_new(app, main, terminal_input(kind, &dir), &needles, &[], i == 0)?;
                if i == 0 {
                    tentry = v["entry"].as_str().unwrap_or_default().to_string();
                    let e = entries(main)?
                        .into_iter()
                        .find(|e| e["id"] == tentry.as_str())
                        .unwrap_or_default();
                    require(
                        e["client"] == TERMINAL_CLIENT
                            && e["program"] == json!(s(&named))
                            && e["argv"][1] == "--with"
                            && e["argv"][2] == *kind
                            && e["cwd"] == json!(dir)
                            && e["lifetime"] == "detached",
                        format!("the launcher's entry for the {kind} launch: {e}"),
                    )?;
                    helper = PathBuf::from(e["executable"].as_str().unwrap_or_default());
                    // The confirmed executable is the execution program: the tool runs it too.
                    let st = ok(main, "program_status", json!({"package": EXECUTION}))?;
                    let tool = st["pid"].as_u64().unwrap_or(0);
                    require(
                        processes(&helper).contains(&(tool as u32)),
                        format!(
                            "the terminal launch's executable {} is not the execution program ({st})",
                            helper.display()
                        ),
                    )?;
                }
                let r = record(&rec, 0)?;
                require(
                    r["argv"][0].as_str().is_some_and(|a| same_file(a, &named))
                        && r["argv"].as_array().and_then(|a| a.get(1..))
                            == Some(&[json!(AGENT), json!(rec)][..])
                        && r["cwd"].as_str().is_some_and(|c| same_file(c, &dir)),
                    format!(
                        "the {kind} agent's argv and cwd: {} {}",
                        r["argv"], r["cwd"]
                    ),
                )?;
                check_environment(&r["env"], &[ASKED], true)?;
                let mut seen = json!({"pid": r["pid"]});
                #[cfg(windows)]
                {
                    let mut attached = in_helpers_console(&r, &helper)?;
                    seen["console"] = r["console"].clone();
                    if i == 0 {
                        // ---- tray: the remembered launch from the Start… menu
                        std::thread::sleep(Duration::from_millis(1100));
                        let label = format!("{TERMINAL_CLIENT} in {}", s(&dir));
                        let items = tray_items("the Start… menu's item for the launch", |i| {
                            i.iter().any(|(_, text, on)| *text == label && *on)
                        })?;
                        tray_start(app, &items, &label)?;
                        let t = record(&rec, r["pid"].as_u64().unwrap_or(0))?;
                        require(!showing(), "the tray's start asked a native confirmation")?;
                        attached.extend(in_helpers_console(&t, &helper)?);
                        seen["tray"] = t["pid"].clone();
                    }
                    seen["after_exit"] = json!(after_exit(kind, &dir, &attached)?);
                }
                #[cfg(target_os = "macos")]
                {
                    in_terminal(&r)?;
                    let mut ended = pids(&r);
                    if i == 0 {
                        std::thread::sleep(Duration::from_millis(1100));
                        let label = format!("{TERMINAL_CLIENT} in {}", s(&dir));
                        let items = tray_items("the Start… menu's item for the launch", |i| {
                            i.iter().any(|(_, text, on)| *text == label && *on)
                        })?;
                        tray_start(app, &items, &label)?;
                        let t = record(&rec, r["pid"].as_u64().unwrap_or(0))?;
                        require(!showing(), "the tray's start asked a native confirmation")?;
                        in_terminal(&t)?;
                        ended.extend(pids(&t));
                        seen["tray"] = t["pid"].clone();
                    }
                    seen["tty"] = r["tty"].clone();
                    stop(&dir)?;
                    wait_for(&format!("the {kind} agents to end"), || {
                        ended.iter().all(|p| !alive(*p)).then_some(())
                    })?;
                }
                kinds[*kind] = seen;
                Ok(())
            })(),
        )?;
    }
    out["kinds"] = kinds;

    // ---- npm shim: Node and its script, in Automatic's terminal
    #[cfg(windows)]
    step(
        "npm shim",
        (|| {
            let dir = folder("node")?;
            let rec = dir.join("record");
            let node = first_node()?;
            let script = canonical(&bin.join("pluxy-node-agent.js"))?;
            launch_new(
                app,
                main,
                json!({
                    "client": "Fixture Node agent", "kind": "terminal",
                    "program": "pluxy-node-agent", "args": [rec], "cwd": dir,
                    "terminal": "automatic",
                }),
                &[
                    EXECUTION,
                    "\"powershell\"",
                    &shown(&s(&node)),
                    &shown(&s(&script)),
                    &shown(&s(&dir)),
                ],
                &[],
                false,
            )?;
            let r = record(&rec, 0)?;
            let argv = r["argv"].as_array().cloned().unwrap_or_default();
            let at = |i: usize, p: &Path| {
                argv.get(i)
                    .and_then(Value::as_str)
                    .is_some_and(|a| same_file(a, p))
            };
            require(
                r["bun"].is_null(),
                format!("Bun ran the script as node.exe: {}", r["bun"]),
            )?;
            require(
                r["exec_path"].as_str().is_some_and(|e| same_file(e, &node))
                    && at(0, &node)
                    && at(1, &script)
                    && argv.get(2..) == Some(&[json!(rec)][..]),
                format!(
                    "Node's program and argv: {} {}, expected {} with {} and [{}]",
                    r["exec_path"],
                    r["argv"],
                    node.display(),
                    script.display(),
                    rec.display()
                ),
            )?;
            require(
                r["cwd"].as_str().is_some_and(|c| same_file(c, &dir)) && r["tty"] == true,
                format!("Node's folder and console: {} {}", r["cwd"], r["tty"]),
            )?;
            // Automatic is PowerShell: it ran after Node, in the launch's folder.
            let file = dir.join("pluxy-after-exit");
            let text = wait_for("PowerShell after Node", || {
                std::fs::read_to_string(&file)
                    .ok()
                    .filter(|t| t.ends_with('\n'))
            })?;
            require(
                same_file(&text, &dir),
                format!("PowerShell after Node wrote {text:?}"),
            )?;
            out["node"] = json!({"exec_path": r["exec_path"], "after_exit": text});
            Ok(())
        })(),
    )?;

    // ---- direct: detached, a variable set and the secret by reference
    let direct = folder("direct")?;
    let rec = direct.join("agent");
    let mut detached_pids: Vec<u64> = Vec::new();
    step(
        "direct",
        (|| {
            launch_new(
                app,
                main,
                json!({
                    "client": "Fixture direct", "kind": "direct", "program": agent,
                    "args": [AGENT, rec], "cwd": direct, "key": secret,
                    "environment": {"set": {ASKED.0: ASKED.1}, "secrets": {SECRET_NAME: "key"}},
                }),
                &[
                    EXECUTION,
                    &shown(&s(&agent)),
                    ASKED.0,
                    SECRET_NAME,
                    LABEL,
                    "Lifetime: detached",
                ],
                &[&key],
                false,
            )?;
            let r = record(&rec, 0)?;
            detached_pids.extend(pids(&r));
            require(
                r["argv"] == json!([s(&agent), AGENT, rec])
                    && r["cwd"].as_str().is_some_and(|c| same_file(c, &direct)),
                format!(
                    "the direct agent's argv and cwd: {} {}",
                    r["argv"], r["cwd"]
                ),
            )?;
            check_environment(&r["env"], &[ASKED, (SECRET_NAME, &key)], false)?;
            #[cfg(windows)]
            require(
                r["in_job"] == false,
                format!("the detached agent runs in a job: {}", r["in_job"]),
            )?;
            #[cfg(unix)]
            {
                let pid = r["pid"].as_u64().unwrap_or(0);
                require(
                    r["sid"].as_u64() == Some(pid)
                        && r["ppid"].as_u64() != Some(u64::from(std::process::id())),
                    format!("the agent is not detached in a session of its own: {r}"),
                )?;
            }
            Ok(())
        })(),
    )?;

    // ---- stdin: the key on a direct launch's stdin; a terminal launch takes none
    let piped = folder("stdin")?;
    let rec = piped.join("agent");
    step(
        "stdin",
        (|| {
            launch_new(
                app,
                main,
                json!({
                    "client": "Fixture stdin", "kind": "direct", "program": agent,
                    "args": [AGENT, rec], "cwd": piped, "key": secret, "stdin": "key",
                    "environment": {"set": {"FIXTURE_READ_STDIN": "1"}},
                }),
                &[EXECUTION, &shown(&s(&agent)), "stdin", LABEL],
                &[&key],
                false,
            )?;
            let r = record(&rec, 0)?;
            detached_pids.extend(pids(&r));
            require(
                r["stdin"] == key.as_str(),
                "the agent did not get the key on its stdin",
            )?;
            require(
                !r["env"].to_string().contains(&key),
                "the agent got the key in its environment too",
            )?;
            check_environment(&r["env"], &[("FIXTURE_READ_STDIN", "1")], false)?;
            // The same as a terminal launch: confirmed, then refused by the execution program.
            let dir = folder("stdin-terminal")?;
            let mut input = terminal_input(KINDS[0], &dir);
            input["key"] = json!(secret);
            input["stdin"] = json!("key");
            let slot = start(main, "launcher_launch", json!({ "launch": input }))?;
            let text = dialog_with(app, &[EXECUTION, "stdin", LABEL, &shown(&s(&dir))])?;
            if text.contains(&key) {
                press("alt+c")?;
                return Err("the terminal launch's confirmation shows the key".into());
            }
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(
                r["ok"] == false
                    && r["error"]
                        .as_str()
                        .is_some_and(|e| e.contains("takes no standard input")),
                format!("a terminal launch with stdin: {r}"),
            )?;
            require(
                !dir.join("agent").exists(),
                "the refused terminal launch ran the agent",
            )
        })(),
    )?;

    // ---- managed terminal: refused before any confirmation
    step(
        "managed terminal",
        (|| {
            let dir = folder("managed-terminal")?;
            let mut input = terminal_input(KINDS[0], &dir);
            input["lifetime"] = json!("managed");
            let e = refused_unasked(main, input, "detached only")?;
            out["managed_terminal"] = json!(e);
            Ok(())
        })(),
    )?;

    // ---- programs Windows never runs as given
    #[cfg(windows)]
    step(
        "programs",
        (|| {
            let dir = folder("programs")?;
            let input = |program: &str, args: &[&str]| {
                json!({
                    "client": "Fixture program", "kind": "direct", "program": program,
                    "args": args, "cwd": dir,
                })
            };
            refused_unasked(main, input("pluxy-batch", &[]), "is a batch file")?;
            refused_unasked(
                main,
                input(&s(&bin.join("pluxy-not-a-program.exe")), &[]),
                "is not an executable file",
            )?;
            refused_unasked(
                main,
                input("cmd.exe", &["/c", "echo"]),
                "reads its command line its own way",
            )?;
            // With no arguments, cmd.exe is a program like any other: it asks.
            let slot = start(
                main,
                "launcher_launch",
                json!({ "launch": input("cmd.exe", &[]) }),
            )?;
            dialog_with(app, &[EXECUTION, "cmd.exe", &shown(&s(&dir))])?;
            press("alt+c")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == false, format!("cmd.exe, cancelled: {r}"))
        })(),
    )?;

    // ---- forget
    step(
        "forget",
        (|| {
            for id in [&entry, &tentry] {
                ok(main, "launcher_forget", json!({"id": id}))?;
            }
            require(
                !entries(main)?
                    .iter()
                    .any(|e| e["id"] == entry.as_str() || e["id"] == tentry.as_str()),
                "a forgotten launch is still listed",
            )?;
            let items = tray_items("the Start… menu without the launches", |i| {
                i.len() == 1
                    && i[0].0 == "launch-none"
                    && i[0].1 == "No remembered launches"
                    && !i[0].2
            })?;
            out["tray_after_forget"] = json!(items.len());
            Ok(())
        })(),
    )?;

    // ---- disable: with managed runtimes running, disabling asks
    step(
        "disable",
        (|| {
            let all = |p: &[u64]| p.iter().all(|p| alive(*p));
            require(
                all(&managed_pids) && all(&detached_pids),
                format!(
                    "the managed {managed_pids:?} and detached {detached_pids:?} agents do not all run"
                ),
            )?;
            let slot = start(
                main,
                "device_enable",
                json!({"id": EXECUTION, "enabled": false}),
            )?;
            dialog_with(app, &[EXECUTION, "runtime"])?;
            press("alt+c")?;
            let r = settle(main, &slot)?;
            require(
                r["ok"] == false,
                format!("a cancelled disable went ahead: {r}"),
            )?;
            require(
                enabled(main)? && all(&managed_pids),
                "a cancelled disable stopped something",
            )?;
            let slot = start(
                main,
                "device_enable",
                json!({"id": EXECUTION, "enabled": false}),
            )?;
            dialog_with(app, &[EXECUTION, "runtime"])?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("the confirmed disable: {r}"))?;
            require(!enabled(main)?, "the execution program is still enabled")?;
            let t = Instant::now();
            wait_for("the managed agents' processes to end", || {
                managed_pids.iter().all(|p| !alive(*p)).then_some(())
            })?;
            require(
                all(&detached_pids),
                format!("a detached agent ended with the disable: {detached_pids:?}"),
            )?;
            out["stopped"] =
                json!({"processes": managed_pids.len(), "secs": t.elapsed().as_secs_f64()});
            Ok(())
        })(),
    )?;

    out["passed"] = json!(true);
    log("launcher", out);
    Ok(())
}
