//! The native test's check of the execution program and the launcher on Windows and macOS
//! (native-test builds with `artifact-test-trust` only; Linux runs `launcher_native.rs`). It
//! drives the real app through the window's own commands, the real native confirmations (worked
//! through `ui_driver.rs`) and the tray's Start… menu, with the signed pluxy/execution (the real
//! execution program, built for this system) that `native/launcher.ts` packed, and the fixture
//! program as the agent (`bin/pluxy-agent`), which writes what it sees of itself to its record.
//!
//! What Linux's check does with a detached agent, a terminal, native key entry or the
//! StatusNotifier tray waits for the slices that bring those here (the spawn and lifetime session,
//! the vault's key entry); this check uses managed launches only, which the app holds and stops:
//!
//! - state: before the execution program is enabled the launcher says it is unavailable; once
//!   it is enabled, available;
//! - terminals: Automatic first;
//! - launch: the window's launcher prepares a direct, managed launch of the agent through the
//!   execution program; the native confirmation names the program, the agent and the lifetime;
//!   remembered, the agent runs with its arguments in the cwd, the launcher lists it with its
//!   source, and the tray's Start… menu gets an item for it (`launcher::refresh_tray`);
//! - one-click: the launcher's Start of it runs it again with no confirmation and shows the
//!   host's launch notice; a second Start at once is refused;
//! - tray: the Start… item's click (the menu event handler's `launcher::tray_start`, called with
//!   the item's ID as the menu has it) runs it again with no confirmation;
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
    let agent = canonical(&fixtures.join("bin").join(if cfg!(windows) {
        "pluxy-agent.exe"
    } else {
        "pluxy-agent"
    }))?;
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
                    "client": CLIENT, "kind": "direct", "program": s(&agent),
                    "args": [AGENT, rec], "cwd": work, "lifetime": "managed",
                }}),
            )?;
            dialog_with(app, &[EXECUTION, "launch", &s(&agent), "managed"])?;
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
            let argv = first["argv"].as_array().cloned().unwrap_or_default();
            let cwd = first["cwd"]
                .as_str()
                .and_then(|c| canonical(Path::new(c)).ok());
            require(
                argv.get(1..) == Some(&[json!(AGENT), json!(rec)][..])
                    && cwd.as_ref() == Some(&work),
                format!(
                    "the agent's argv and cwd: {} {}",
                    first["argv"], first["cwd"]
                ),
            )?;
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
                e["client"] == CLIENT && e["source"]["package"] == EXECUTION,
                format!("the launcher's entry: {e}"),
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
            let id = items
                .iter()
                .find(|(_, text, _)| *text == label)
                .map(|(id, _, _)| id.clone())
                .unwrap_or_default();
            let before = again["pid"].as_u64().unwrap_or(0);
            crate::launcher::tray_start(app, &id);
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
