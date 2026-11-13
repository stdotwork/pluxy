//! The native test's check of native tool generations (native-test builds with
//! `artifact-test-trust` only): swaps, crash backoff and quarantine, and calls whose outcome is
//! unknown. It runs after `program_native.rs` in the same session, with the summary tool 1.0.0
//! enabled and running, and drives the real app through the window's own commands and the real
//! GTK native confirmation, with the releases `native/program.ts` built (their digests are in
//! `expect.json`, worked out there, not by the app). It looks at the real processes and threads
//! in `/proc`:
//!
//! - a slow call runs on 1.0.0 while 1.1.0 is installed. The native confirmation names 1.1.0 and
//!   full access; once it is confirmed, 1.1.0 runs (started and described, no call yet) and 1.0.0
//!   drains with its slow call. The call made next is 1.1.0's first and answers only after the slow
//!   call, which 1.0.0 answers. Then 1.0.0 is reaped and 1.1.0 runs on. The install command's
//!   thread has ended; the tool's supervisor thread stays;
//! - a cancelled native confirmation for 1.2.0 starts nothing and changes nothing;
//! - confirmed, 1.2.0 is incompatible (it describes an operation its manifest does not declare):
//!   the install is refused, its process reaped and its files deleted, and 1.1.0 stays active,
//!   enabled and running in the same process;
//! - five crashes in a row: each crash call fails with its outcome unknown, and the host records
//!   it. A call during the backoff (1, 2, 4 and 8 seconds) is refused and starts nothing. The first
//!   call after it starts the tool again, from a call thread that has ended while the tool runs on;
//!   it is the new process's first call (the crash call was not sent again), and the new process's
//!   describe reported the crash call. The fifth crash quarantines the tool: calls are refused and
//!   start nothing until it is disabled and enabled again;
//! - a call is cancelled; then, left alone, 1.1.0 stops after the idle delay, with no process or
//!   host thread left;
//! - rolled back to 1.0.0 through a native confirmation, 1.0.0 runs and answers; its process and
//!   child are named for the runner, which checks they are gone once the app has quit.
//!
//! One line on stderr: `native-test: swap <JSON>`.

use super::admission::{dialog_text, ok, press, refused, require, settle, start};
use super::program::{alive, enable, processes, status, threads, ID};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::path::PathBuf;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

const FULL_ACCESS: &str = "full access to your user account";
/// How long the slow call on 1.0.0 takes: past the swap's confirmation and start.
const SLOW_MS: u64 = 20_000;

fn call(main: &Webview, op: &str, input: Value) -> Result<Value, String> {
    ok(
        main,
        "program_call",
        json!({"package": ID, "op": op, "input": input}),
    )
}

fn summary(main: &Webview, items: &Value) -> Result<Value, String> {
    call(main, "summary", json!({"items": items}))
}

fn pid_of(v: &Value) -> Result<u64, String> {
    v["pid"].as_u64().ok_or(format!("no pid: {v}"))
}

/// The package as the window lists it.
fn package(main: &Webview) -> Result<Value, String> {
    super::admission::listed(main, ID)?.ok_or(format!("{ID} is not listed"))
}

/// Installs version through its native confirmation, answered with keys; the command's outcome.
fn install(app: &AppHandle, main: &Webview, version: &str, keys: &str) -> Result<Value, String> {
    let slot = start(
        main,
        "device_install",
        json!({"registry": "curated", "id": ID, "version": version}),
    )?;
    let text = dialog_text(app)?;
    require(
        text.contains(FULL_ACCESS) && text.contains(ID) && text.contains(version),
        format!("the native confirmation for {version} shows: {text}"),
    )?;
    press(keys)?;
    settle(main, &slot)
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let items = expect["items"].clone();
    let store = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store");
    let sha = |v: &str| {
        expect["releases"][v]
            .as_str()
            .unwrap_or_default()
            .to_string()
    };
    let dir = |v: &str| store.join(sha(v));
    let program = |v: &str| dir(v).join("bin").join("summary");
    let (p1, p2, p3) = (program("1.0.0"), program("1.1.0"), program("1.2.0"));
    let mut out = json!({});

    // 1.0.0 runs, as program_native.rs left it.
    let first = summary(main, &items)?;
    let v1 = pid_of(&first)?;
    require(
        first["version"] == "1.0.0" && alive(v1),
        format!("1.0.0 before the swap: {first}"),
    )?;

    // A slow call on 1.0.0, then 1.1.0 installed and confirmed while it runs.
    let slow = start(
        main,
        "program_call",
        json!({"package": ID, "op": "slow", "input": {"ms": SLOW_MS}}),
    )?;
    wait_for("the slow call to run", || {
        status(main).ok().filter(|s| s["state"] == "active")
    })?;
    let swapped_at = Instant::now();
    let r = install(app, main, "1.1.0", "alt+o")?;
    require(r["ok"] == true, format!("the swap to 1.1.0: {r}"))?;
    let s = status(main)?;
    let v2 = s["pid"].as_u64().unwrap_or_default();
    let draining: Vec<u64> = s["draining"]
        .as_array()
        .map(|a| a.iter().filter_map(|d| d["pid"].as_u64()).collect())
        .unwrap_or_default();
    require(
        v2 != 0 && v2 != v1 && alive(v2) && alive(v1) && draining == vec![v1],
        format!("just after the swap (1.0.0 is {v1}): {s}"),
    )?;
    require(
        processes(&p2).contains(&(v2 as u32)) && processes(&p1).contains(&(v1 as u32)),
        format!(
            "1.1.0's processes {:?}, 1.0.0's {:?}",
            processes(&p2),
            processes(&p1)
        ),
    )?;
    let p = package(main)?;
    require(
        p["active"]["version"] == "1.1.0"
            && p["last_good"]["version"] == "1.0.0"
            && p["enabled"] == true
            && p["authorized"] == true,
        format!("{ID} after the swap: {p}"),
    )?;
    // The call made now goes to 1.1.0, and waits for the slow call on 1.0.0.
    let next = start(
        main,
        "program_call",
        json!({"package": ID, "op": "summary", "input": {"items": items}}),
    )?;
    let slow = settle(main, &slow)?;
    require(
        slow["ok"] == true && slow["value"]["version"] == "1.0.0" && slow["value"]["pid"] == v1,
        format!("the slow call: {slow}"),
    )?;
    let next = settle(main, &next)?;
    let n = &next["value"];
    require(
        next["ok"] == true
            && n["version"] == "1.1.0"
            && n["pid"] == v2
            && n["calls"] == 1
            && n["at"]
                .as_u64()
                .zip(slow["value"]["at"].as_u64())
                .is_some_and(|(next, slow)| next >= slow),
        format!("the call after the swap: {next}; the slow call: {slow}"),
    )?;
    require(
        swapped_at.elapsed() < Duration::from_millis(SLOW_MS) + Duration::from_secs(10),
        "the slow call took too long",
    )?;
    wait_for("1.0.0 to be reaped after its drain", || {
        processes(&p1).is_empty().then_some(())
    })?;
    let s = status(main)?;
    let names = threads();
    require(
        s["pid"] == v2
            && alive(v2)
            && s["draining"].as_array().is_some_and(|a| a.is_empty())
            && !names.iter().any(|t| t == "pluxy-prog-swap")
            && names.iter().filter(|t| *t == "pluxy-prog-sup").count() == 1,
        format!("after the drain: {s}; threads {names:?}"),
    )?;
    out["swap"] = json!({"from": v1, "to": v2, "slow": slow["value"], "next": n});

    // A cancelled confirmation for 1.2.0 starts nothing and changes nothing.
    let r = install(app, main, "1.2.0", "alt+c")?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("cancelled")),
        format!("the cancelled swap to 1.2.0: {r}"),
    )?;
    require(
        processes(&p3).is_empty() && !dir("1.2.0").exists(),
        "a cancelled swap left 1.2.0 running or installed",
    )?;
    out["cancelled"] = r["error"].clone();

    // Confirmed, 1.2.0 is incompatible: 1.1.0 stays, in the same process.
    let r = install(app, main, "1.2.0", "alt+o")?;
    let e = r["error"].as_str().unwrap_or_default();
    require(
        r["ok"] == false && e.contains("incompatible") && e.contains("1.1.0"),
        format!("the swap to the incompatible 1.2.0: {r}"),
    )?;
    wait_for("1.2.0 to be reaped", || {
        processes(&p3).is_empty().then_some(())
    })?;
    require(
        !dir("1.2.0").exists(),
        "the refused 1.2.0 is still in the store",
    )?;
    let p = package(main)?;
    require(
        p["active"]["version"] == "1.1.0" && p["enabled"] == true && p["authorized"] == true,
        format!("{ID} after the refused swap: {p}"),
    )?;
    let again = summary(main, &items)?;
    require(
        again["pid"] == v2 && again["version"] == "1.1.0" && again["calls"] == 2,
        format!("1.1.0 after the refused swap: {again}"),
    )?;
    out["refused"] = json!(e);

    // Five crashes in a row: backoff between them, then quarantine.
    let mut backoffs = Vec::new();
    for n in 1..=5u64 {
        let r = super::admission::call(
            main,
            "program_call",
            json!({"package": ID, "op": "crash", "input": {}}),
        )?;
        let e = r["error"].as_str().unwrap_or_default().to_string();
        require(
            r["ok"] == false && e.contains("crashed") && e.contains("unknown"),
            format!("crash call {n}: {r}"),
        )?;
        let s = wait_for("the crash to be recorded", || {
            status(main)
                .ok()
                .filter(|s| s["state"] == "crashed" || s["state"] == "quarantined")
        })?;
        let unknown: Vec<&str> = s["unknown"]
            .as_array()
            .map(|a| a.iter().filter_map(|u| u["op"].as_str()).collect())
            .unwrap_or_default();
        require(
            s["crashes"] == n && unknown == ["crash"] && s["pid"].is_null(),
            format!("status after crash {n}: {s}"),
        )?;
        if n == 5 {
            require(
                s["state"] == "quarantined",
                format!("after five crashes: {s}"),
            )?;
            break;
        }
        let wait = s["retry_in_ms"].as_u64().unwrap_or_default();
        require(
            s["state"] == "crashed" && (1..=1000 << (n - 1)).contains(&wait),
            format!("the backoff after crash {n}: {s}"),
        )?;
        refused(
            main,
            "program_call",
            json!({"package": ID, "op": "summary", "input": {"items": items}}),
            "restart",
        )?;
        require(
            processes(&p2).is_empty(),
            "a call during the backoff started the tool",
        )?;
        std::thread::sleep(Duration::from_millis(wait + 200));
        let r = summary(main, &items)?;
        let pid = pid_of(&r)?;
        let reported: Vec<&str> = r["reported"]
            .as_array()
            .map(|a| a.iter().filter_map(|u| u["op"].as_str()).collect())
            .unwrap_or_default();
        require(
            r["calls"] == 1 && reported == ["crash"],
            format!("the first call after crash {n}: {r}"),
        )?;
        std::thread::sleep(Duration::from_secs(1));
        let names = threads();
        let s = status(main)?;
        require(
            alive(pid)
                && !names.iter().any(|t| t == "pluxy-prog-call")
                && names.iter().any(|t| t == "pluxy-prog-sup")
                && s["unknown"].as_array().is_some_and(|a| a.is_empty()),
            format!("after the restart: {s}; threads {names:?}"),
        )?;
        backoffs.push(wait);
    }
    let e = refused(
        main,
        "program_call",
        json!({"package": ID, "op": "summary", "input": {"items": items}}),
        "quarantined",
    )?;
    require(
        processes(&p2).is_empty(),
        "a call to a quarantined tool started it",
    )?;
    out["quarantine"] = json!({"backoff_ms": backoffs, "refused": e});
    ok(main, "device_enable", json!({"id": ID, "enabled": false}))?;
    enable(app, main)?;
    let r = summary(main, &items)?;
    let v2 = pid_of(&r)?;
    require(
        r["calls"] == 1 && r["version"] == "1.1.0",
        format!("the call after quarantine ended: {r}"),
    )?;
    let s = status(main)?;
    require(s["crashes"] == 0, format!("status after enable: {s}"))?;

    // A cancelled call, then the idle stop: nothing of it left.
    let slot = start(
        main,
        "program_call",
        json!({"package": ID, "op": "wait", "input": {}, "ticket": "swap-wait"}),
    )?;
    wait_for("the wait call to run", || {
        status(main).ok().filter(|s| s["state"] == "active")
    })?;
    ok(main, "program_cancel", json!({"ticket": "swap-wait"}))?;
    let r = settle(main, &slot)?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("cancelled")),
        format!("the cancelled call: {r}"),
    )?;
    let after = summary(main, &items)?;
    require(
        after["pid"] == v2 && after["calls"] == 3,
        format!("the call after the cancel: {after}"),
    )?;
    let idle = Instant::now();
    wait_for("1.1.0 to stop when idle", || {
        processes(&p2).is_empty().then_some(())
    })?;
    std::thread::sleep(Duration::from_millis(500));
    let names = threads();
    let s = status(main)?;
    require(
        idle.elapsed() >= Duration::from_secs(29)
            && !names.iter().any(|t| t.starts_with("pluxy-prog"))
            && s["state"] == "idle",
        format!(
            "the idle stop after {:?}: {s}; threads {names:?}",
            idle.elapsed()
        ),
    )?;

    // Rolled back to 1.0.0 through its native confirmation.
    let slot = start(main, "device_rollback", json!({"id": ID}))?;
    let text = dialog_text(app)?;
    require(
        text.contains(FULL_ACCESS) && text.contains("1.0.0"),
        format!("the rollback's native confirmation shows: {text}"),
    )?;
    press("alt+o")?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("the rollback: {r}"))?;
    let p = package(main)?;
    require(
        p["active"]["version"] == "1.0.0"
            && p["last_good"]["version"] == "1.1.0"
            && p["authorized"] == true,
        format!("{ID} after the rollback: {p}"),
    )?;
    let last = summary(main, &items)?;
    let (pid, child) = (
        last["pid"].as_u64().unwrap_or_default(),
        last["child"].as_u64().unwrap_or_default(),
    );
    require(
        last["version"] == "1.0.0"
            && alive(pid)
            && alive(child)
            && processes(&p1) == BTreeSet::from([pid as u32, child as u32]),
        format!("1.0.0 after the rollback: {last}"),
    )?;
    out["program"] = json!(p1);
    out["quit_pids"] = json!([pid, child]);
    out["passed"] = json!(true);
    log("swap", out);
    Ok(())
}
