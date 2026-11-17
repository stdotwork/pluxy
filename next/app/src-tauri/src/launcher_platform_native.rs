//! The native test's check of the execution program and the launcher on Windows and macOS
//! (native-test builds with `artifact-test-trust` only; Linux runs `launcher_native.rs`). It
//! drives the real app through the window's own commands, the real native confirmations (worked
//! through `ui_driver.rs`) and the tray's Start… menu, with the signed pluxy/execution (the real
//! execution program, built for this system) that `native/launcher.ts` packed, and the fixture
//! program as the agent (`bin/pluxy-agent`, `.exe` on Windows; `bin` first on the app's PATH),
//! which writes what it sees of itself to its record.
//!
//! What Linux's check does with a detached agent, a terminal, native key entry or the
//! StatusNotifier tray waits for the slices that bring those here (the spawn and lifetime session,
//! the vault's key entry); this check uses managed launches only, which the app holds and stops:
//!
//! - state: before the execution program is enabled the launcher says it is unavailable; once
//!   it is enabled, available;
//! - terminals: Automatic first;
//! - launch: the window's launcher prepares a direct, managed launch of the agent, found by name
//!   on the search path, through the execution program, setting one variable; the native
//!   confirmation names the program, the agent as found and the lifetime; remembered, the agent
//!   runs with exactly that path and its arguments as its argv, in the cwd, with only the host's
//!   session values (`spawn::BASE`, `LC_*` on macOS) as the app has them and the variable set (not
//!   the canary variable the session gave the app); the launcher lists it with its executable,
//!   arguments, cwd and source, its client's recent folders start with the cwd, and the tray's
//!   Start… menu gets an item for it (`launcher::refresh_tray`);
//! - one-click: the launcher's Start of it runs it again with no confirmation and shows the
//!   host's launch notice; a second Start at once is refused;
//! - tray: the Start… item's click runs it again with no confirmation. On Windows the click is the
//!   menu's own: the `WM_COMMAND` with the item's command ID that Windows sends the tray icon's
//!   window once a person picks it, which the menu library turns into the app's menu event. On
//!   macOS the menu shows only while a person holds it open, so the check calls the menu event's
//!   handler (`tray_menu_event`) with the item's ID;
//! - forget: a forgotten launch leaves the launcher, and the Start… menu shows only "No
//!   remembered launches";
//! - disable: disabling the execution program with its managed runtimes running asks; cancelled,
//!   they run on; confirmed, the package is disabled and none of the agent's processes is left.
//!
//! One line on stderr: `native-test: launcher <JSON>`.

use super::admission::{dialog_text, ok, press, require, settle, start};
use super::procs::processes;
use super::routing::{enable, install, showing};
use super::{eval, log, ui, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const EXECUTION: &str = "pluxy/execution";
const AGENT: &str = "pluxy-fixture-agent";
const CLIENT: &str = "Fixture agent";
const NOTICE: &str = "pluxy launch notice";
/// The variable the launch sets, and its value.
const ASKED: (&str, &str) = ("FIXTURE_LAUNCH_VALUE", "set-by-the-launch");
/// The variable the session gives the app (`native/platform-tools.ts`), which no launch may get.
const CANARY: &str = "PLUXY_TEST_LAUNCHER_CANARY";

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

/// The record the agent writes at path, once it is there and names a process other than not.
fn record(path: &Path, not: u64) -> Result<Value, String> {
    wait_for(&format!("the agent's record {}", path.display()), || {
        std::fs::read(path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .filter(|r| r["pid"].as_u64().is_some_and(|p| p != not))
    })
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

/// The agent's environment (env, its record's): the host's session values the app has, each as
/// the app has it, and the variable the launch asked for, and nothing else; never the session's
/// canary. On macOS CoreFoundation, which the agent links, sets `__CF_USER_TEXT_ENCODING` in its
/// own environment as it starts, so that value is the agent's own.
fn check_environment(env: &Value) -> Result<(), String> {
    let own = |name: &str| cfg!(target_os = "macos") && name == "__CF_USER_TEXT_ENCODING";
    let map = env
        .as_object()
        .ok_or(format!("no environment in the record: {env}"))?;
    // Windows compares names ignoring case.
    let same = |a: &str, b: &str| {
        if cfg!(windows) {
            a.eq_ignore_ascii_case(b)
        } else {
            a == b
        }
    };
    for name in map.keys() {
        let base = crate::spawn::BASE.iter().any(|b| same(b, name))
            || (cfg!(unix) && name.starts_with("LC_"));
        require(
            base || same(name, ASKED.0),
            format!("the agent got {name}, which nothing selected or asked for"),
        )?;
    }
    // Each session value the app has, as the app has it.
    for (k, v) in std::env::vars_os() {
        let k = k.to_string_lossy();
        let base =
            crate::spawn::BASE.iter().any(|b| same(b, &k)) || (cfg!(unix) && k.starts_with("LC_"));
        if !base || own(&k) {
            continue;
        }
        let got = map.iter().find(|(n, _)| same(n, &k)).map(|(_, v)| v);
        require(
            got.and_then(Value::as_str) == Some(v.to_string_lossy().as_ref()),
            format!("the agent's {k} is not the app's"),
        )?;
    }
    require(
        map.iter()
            .any(|(k, v)| same(k, ASKED.0) && v.as_str() == Some(ASKED.1)),
        format!("the agent did not get the {} the launch set", ASKED.0),
    )?;
    require(map.keys().any(|k| same(k, "PATH")), "the agent has no PATH")?;
    let canary = std::env::var(CANARY).map_err(|_| format!("the app has no {CANARY}"))?;
    require(
        !map.keys().any(|k| same(k, CANARY)) && !env.to_string().contains(&canary),
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

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let execution_version =
        std::fs::read_to_string(fixtures.join("execution-version")).unwrap_or_default();
    // The agent as the execution program finds it by name: in bin, as the app's PATH has it (the
    // session puts the fixtures' bin there as this path), with Windows' `.exe`.
    let named = fixtures.join("bin").join(if cfg!(windows) {
        "pluxy-agent.exe"
    } else {
        "pluxy-agent"
    });
    let agent = canonical(&named)?;
    let work = fixtures.join("work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let work = canonical(&work)?;
    let s = |p: &Path| p.to_string_lossy().into_owned();
    let rec = work.join("managed-agent");
    let mut out = json!({"platform": std::env::consts::OS});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    // The agent's processes: what it records as its own and its group member.
    let running = |r: &Value| {
        let now = processes(&agent);
        [&r["pid"], &r["child"]]
            .iter()
            .all(|p| p.as_u64().is_some_and(|p| now.contains(&(p as u32))))
    };

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
            require(
                list.first()
                    .is_some_and(|a| a["id"] == "automatic" && a["label"] == "Automatic"),
                format!("Automatic is not the first terminal: {t}"),
            )
        })(),
    )?;

    // ---- launch: a remembered, managed, direct launch
    let mut entry = String::new();
    let mut first = Value::Null;
    let label = format!("{CLIENT} in {}", s(&work));
    step(
        "launch",
        (|| {
            let slot = start(
                main,
                "launcher_launch",
                json!({"launch": {
                    "client": CLIENT, "kind": "direct", "program": "pluxy-agent",
                    "args": [AGENT, rec], "cwd": work, "lifetime": "managed",
                    "environment": {"set": {ASKED.0: ASKED.1}},
                }}),
            )?;
            // The review text is JSON, so the executable shows JSON-encoded (doubled
            // backslashes on Windows) as `launch.executable.selected`.
            let shown = json!(s(&named)).to_string();
            dialog_with(app, &[EXECUTION, "launch", &shown, "managed"])?;
            press("alt+r")?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("the confirmed launch: {r}"))?;
            entry = r["value"]["entry"].as_str().unwrap_or_default().to_string();
            require(
                !entry.is_empty() && r["value"]["output"]["runtime"].as_str().is_some(),
                format!("no launcher entry or runtime for a remembered managed launch: {r}"),
            )?;
            first = record(&rec, 0)?;
            let cwd = first["cwd"]
                .as_str()
                .and_then(|c| canonical(Path::new(c)).ok());
            require(
                first["argv"] == json!([s(&named), AGENT, rec]) && cwd.as_ref() == Some(&work),
                format!(
                    "the agent's argv and cwd: {} {}, expected [{}, {AGENT}, {}] and {}",
                    first["argv"],
                    first["cwd"],
                    named.display(),
                    rec.display(),
                    work.display()
                ),
            )?;
            check_environment(&first["env"])?;
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
            let e = entries(main)?
                .into_iter()
                .find(|e| e["id"] == entry.as_str())
                .unwrap_or_default();
            require(
                e["client"] == CLIENT
                    && e["executable"] == json!(s(&named))
                    && e["argv"] == json!([AGENT, rec])
                    && e["cwd"] == json!(work)
                    && e["source"]["package"] == EXECUTION,
                format!("the launcher's entry: {e}"),
            )?;
            let st = ok(main, "launcher_state", json!({}))?;
            require(
                st["history"][CLIENT]
                    .as_array()
                    .is_some_and(|h| h.first() == Some(&json!(work))),
                format!("the launcher's recent folders: {}", st["history"]),
            )?;
            // The tray's Start… menu, refreshed: an item for it, which can be clicked.
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

    // ---- tray: the Start… item's click
    step(
        "tray",
        (|| {
            std::thread::sleep(Duration::from_millis(1100));
            let items = tray_items("the Start… menu's item for the launch", |i| {
                i.iter().any(|(_, text, on)| *text == label && *on)
            })?;
            let before = again["pid"].as_u64().unwrap_or(0);
            #[cfg(windows)]
            {
                let _ = items;
                tray_click(app, &label)?;
            }
            #[cfg(not(windows))]
            {
                let id = items
                    .iter()
                    .find(|(_, text, _)| *text == label)
                    .map(|(id, _, _)| id.clone())
                    .unwrap_or_default();
                let handle = app.clone();
                app.run_on_main_thread(move || crate::tray_menu_event(&handle, &id))
                    .map_err(|e| e.to_string())?;
            }
            let r = record(&rec, before)?;
            require(running(&r), format!("the tray start's agent: {r}"))?;
            require(!showing(), "the tray's start asked a native confirmation")
        })(),
    )?;

    // ---- forget
    step(
        "forget",
        (|| {
            ok(main, "launcher_forget", json!({"id": entry}))?;
            require(
                !entries(main)?.iter().any(|e| e["id"] == entry.as_str()),
                "the forgotten launch is still listed",
            )?;
            let items = tray_items("the Start… menu without the launch", |i| {
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
            let before = processes(&agent);
            require(
                before.contains(&(pid1 as u32)),
                format!("the first launch's agent {pid1} is not running: {before:?}"),
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
                enabled(main)? && processes(&agent) == before,
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
            wait_for("the agent's processes to end", || {
                processes(&agent).is_empty().then_some(())
            })?;
            out["stopped"] = json!({"processes": before.len(), "secs": t.elapsed().as_secs_f64()});
            Ok(())
        })(),
    )?;

    out["passed"] = json!(true);
    log("launcher", out);
    Ok(())
}
