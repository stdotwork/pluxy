//! The native test's check of the execution program and the launcher (native-test builds with
//! `artifact-test-trust` only). It drives the real app through the window's own commands, the
//! real native key entry and confirmations, a real page webview and the real tray menu (clicked
//! through the session's StatusNotifier watcher and dbusmenu by the session probe), with the
//! signed packages `native/launcher.ts` built: pluxy/execution (the real execution program),
//! pluxy/launch-relay (a fixture tool that calls execution's `launch`) and pluxy/launch-page (a
//! curated web page importing it). The terminal is the fixture program as `x-terminal-emulator`
//! and the agent the fixture program as `pluxy-agent`, both first on the app's PATH; each writes
//! what it sees of itself to its record. The key comes from `keys.json`, made there. Each refusal
//! has a positive control on the same route:
//!
//! - state: before the execution program is enabled the launcher says it is unavailable;
//! - terminals: Automatic first, then the stand-in terminal found on PATH;
//! - terminal: the window's launcher prepares a terminal launch (Automatic, the agent found by
//!   name) through the execution program; the native confirmation names the terminal, the agent,
//!   the cwd and the lifetime; remembered, the terminal runs with exactly `-e <agent> <args>` in
//!   the cwd, and the agent in a session of its own; the launcher lists it with its source;
//! - one-click: the launcher's Start of it runs it again with no confirmation and shows the
//!   host's launch notice; a second Start at once is refused;
//! - tray: the tray's Start… item runs it again with no confirmation;
//! - key consumers: an agent given its key in its environment (as Antigravity's), a final process
//!   given key, base URL and model (as Copilot CLI's, through an executable override) and a
//!   credential bridge given its key on its stdin (as the VS Code setup bridge, a managed runtime)
//!   each get the key; the frames the execution program received carried the key's reference and
//!   never the key;
//! - crash: the execution program killed leaves the agents; restarted, it lists the bridge's
//!   runtime, restarts it (a new process) and stops it;
//! - chain: the same launch through pluxy/launch-relay, started with a native gesture, asks;
//! - page: the page's Start of it asks every time with a launch notice; a flood gets one
//!   confirmation; with its consent revoked it is refused unasked;
//! - changed: the agent replaced (a self-update) asks again, and remembered again it runs in one
//!   click; the terminal's metadata changed asks again;
//! - idle: once the execution program stopped when idle, the agents run on;
//! - forget: a forgotten launch leaves the launcher and the tray's Start… menu;
//! - disable: disabling the execution program with a managed runtime running asks; cancelled,
//!   both stay; confirmed, the runtime is stopped and the package disabled.
//!
//! Then it writes `known.json` (the agents, for the runner, which checks after the app quit that
//! they run on, then ends them) and the app quits.
//!
//! One line on stderr: `native-test: launcher <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::routing::{answer, call, clicked, enable, install, showing, Page};
use super::spawn::{check_environment, ended, owned, record};
use super::vault::{dialog_with, type_key};
use super::{eval, log, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const EXECUTION: &str = "pluxy/execution";
const RELAY: &str = "pluxy/launch-relay";
const PAGE: &str = "pluxy/launch-page";
const LABEL: &str = "fixture launcher key";
const AGENT: &str = "pluxy-fixture-agent";
const CLIENT: &str = "Fixture agent";
const NOTICE: &str = "pluxy launch notice";

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

/// The text of the host's launch notice once it contains every one of needles.
fn notice(app: &AppHandle, needles: &[&str]) -> Result<String, String> {
    use gtk::prelude::*;
    let mut last = String::new();
    let found = wait_for(&format!("a launch notice naming {needles:?}"), || {
        let (tx, rx) = std::sync::mpsc::channel();
        app.run_on_main_thread(move || {
            let mut text = String::new();
            for w in gtk::Window::list_toplevels() {
                if let Some(w) = w.downcast_ref::<gtk::Window>() {
                    if w.is_visible() && w.title().as_deref() == Some(NOTICE) {
                        if let Some(l) = w.child().and_then(|c| c.downcast::<gtk::Label>().ok()) {
                            text.push_str(&l.text());
                        }
                    }
                }
            }
            let _ = tx.send(text);
        })
        .ok()?;
        let t = rx.recv_timeout(Duration::from_secs(5)).ok()?;
        last.clone_from(&t);
        needles.iter().all(|n| t.contains(n)).then_some(t)
    });
    found.map_err(|e| format!("{e}; the notice said: {last}"))
}

/// The session probe's click of the tray item labelled label (or "inspect"): its output.
fn tray(label: &str) -> Result<String, String> {
    let probe = std::env::var("PLUXY_APP_TEST_PROBE").map_err(|_| "no probe")?;
    let out = std::process::Command::new(probe)
        .args(["tray", &std::process::id().to_string(), label])
        .output()
        .map_err(|e| e.to_string())?;
    require(
        out.status.success(),
        format!(
            "tray {label:?}: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ),
    )?;
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
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

/// Starts cmd with args, requires a native confirmation naming every one of needles, cancels it
/// and requires the command refused.
fn asks(
    app: &AppHandle,
    main: &Webview,
    cmd: &str,
    args: Value,
    needles: &[&str],
) -> Result<(), String> {
    let slot = start(main, cmd, args.clone())?;
    dialog_with(app, needles)?;
    press("alt+c")?;
    let r = settle(main, &slot)?;
    require(
        r["ok"] == false,
        format!("{cmd} {args} went ahead though cancelled: {r}"),
    )
}

fn entries(main: &Webview) -> Result<Vec<Value>, String> {
    let s = ok(main, "launcher_state", json!({}))?;
    Ok(s["entries"].as_array().cloned().unwrap_or_default())
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let keys: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("keys.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let key = keys["key"].as_str().ok_or("no key")?.to_string();
    // The execution program's release, from its manifest (none before it exists).
    let execution_version =
        std::fs::read_to_string(fixtures.join("execution-version")).unwrap_or_default();
    let bin = std::fs::canonicalize(fixtures.join("bin")).map_err(|e| e.to_string())?;
    let terminal = bin.join("x-terminal-emulator");
    let agent = bin.join("pluxy-agent");
    let override_agent = std::fs::canonicalize(fixtures.join("override").join("agent"))
        .map_err(|e| e.to_string())?;
    let s = |p: &Path| p.to_string_lossy().into_owned();
    let work = fixtures.join("work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let work = std::fs::canonicalize(&work).map_err(|e| e.to_string())?;
    let at = |name: &str| work.join(name);
    let mut known = json!({"detached": [], "managed": []});
    let save = |known: &Value| {
        std::fs::write(fixtures.join("known.json"), known.to_string()).map_err(|e| e.to_string())
    };
    let note = |known: &mut Value, r: &Value, rec: &Path| {
        let marker = rec.to_string_lossy().into_owned();
        for pid in [&r["pid"], &r["child"]] {
            if let Some(list) = known["detached"].as_array_mut() {
                list.push(json!({"pid": pid, "marker": marker}));
            }
        }
    };
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

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

    // ---- secret: the key the consumers get
    let mut secret = String::new();
    step(
        "secret",
        (|| {
            let slot = start(main, "vault_secret_add", json!({"label": LABEL}))?;
            type_key(&key)?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("vault_secret_add: {r}"))?;
            secret = r["value"]["id"].as_str().unwrap_or_default().to_string();
            require(secret.starts_with("s-"), format!("the new secret: {r}"))
        })(),
    )?;

    step(
        "enable",
        (|| {
            for (id, version) in [(EXECUTION, execution_version.as_str()), (RELAY, "1.0.0")] {
                install(main, "curated", id, version)?;
                enable(app, main, id)?;
            }
            install(main, "curated", PAGE, "1.0.0")?;
            let slot = start(main, "device_imports", json!({"id": PAGE, "allowed": true}))?;
            dialog_with(app, &[PAGE, EXECUTION, "launch"])?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("the page's imports: {r}"))?;
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
            require(
                list.first()
                    .is_some_and(|a| a["id"] == "automatic" && a["label"] == "Automatic"),
                format!("Automatic is not the first terminal: {t}"),
            )?;
            require(
                list.iter()
                    .any(|a| a["id"] == "x-terminal-emulator" && a["path"] == json!(s(&terminal))),
                format!("the stand-in terminal is not listed: {t}"),
            )
        })(),
    )?;

    // ---- terminal: a remembered terminal launch, Automatic, the agent found by name
    let (rec1, trec) = (at("terminal-agent"), at("terminal"));
    let mut entry = String::new();
    let mut first = Value::Null;
    let plain = json!({
        "client": CLIENT, "kind": "terminal", "program": "pluxy-agent",
        "args": [AGENT, rec1], "cwd": work, "terminal": "automatic",
        "environment": {"set": {"FIXTURE_TERMINAL_RECORD": trec}},
    });
    let argv1 = json!(["-e", s(&agent), AGENT, rec1]);
    step(
        "terminal",
        (|| {
            let v = launch_new(
                app,
                main,
                plain.clone(),
                &[
                    EXECUTION,
                    "launch",
                    &s(&terminal),
                    &s(&agent),
                    &s(&work),
                    "detached",
                ],
                &[],
                true,
            )?;
            entry = v["entry"].as_str().unwrap_or_default().to_string();
            require(
                !entry.is_empty(),
                format!("no launcher entry for a remembered launch: {v}"),
            )?;
            let t = record(&trec, 0)?;
            let mut want = vec![json!(s(&terminal))];
            want.extend(argv1.as_array().cloned().unwrap_or_default());
            require(
                t["argv"] == json!(want),
                format!("the terminal's argv: {}", t["argv"]),
            )?;
            require(
                t["cwd"] == json!(work),
                format!("the terminal's cwd: {}", t["cwd"]),
            )?;
            first = record(&rec1, 0)?;
            note(&mut known, &first, &rec1);
            save(&known)?;
            require(
                first["argv"] == json!([s(&agent), AGENT, rec1]) && first["cwd"] == json!(work),
                format!(
                    "the agent's argv and cwd: {} {}",
                    first["argv"], first["cwd"]
                ),
            )?;
            let pid = first["pid"].as_u64().unwrap_or(0);
            require(
                first["sid"].as_u64() == Some(pid)
                    && first["ppid"].as_u64() != Some(u64::from(std::process::id())),
                format!("the agent is not detached in a session of its own: {first}"),
            )?;
            check_environment(
                &first["env"],
                &[("FIXTURE_TERMINAL_RECORD", &s(&trec))],
                &[],
                &[key.as_str()],
            )?;
            let list = entries(main)?;
            let e = list
                .iter()
                .find(|e| e["id"] == entry.as_str())
                .cloned()
                .unwrap_or_default();
            require(
                e["remembered"] == true
                    && e["client"] == CLIENT
                    && e["executable"] == json!(s(&terminal))
                    && e["argv"] == argv1
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
            )
        })(),
    )?;
    let pid1 = first["pid"].as_u64().unwrap_or(0);

    // ---- one-click from the window's launcher
    step(
        "one-click",
        (|| {
            let slot = start(main, "launcher_start", json!({"id": entry}))?;
            let r = unasked(main, &slot)?;
            require(r["ok"] == true, format!("the one-click start: {r}"))?;
            let again = record(&rec1, pid1)?;
            note(&mut known, &again, &rec1);
            save(&known)?;
            notice(app, &[CLIENT, "Started"])?;
            // At once again: one start per launch per second.
            let r = unasked(main, &start(main, "launcher_start", json!({"id": entry}))?)?;
            require(
                r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("second")),
                format!("a second start at once: {r}"),
            )
        })(),
    )?;

    // ---- tray: the Start… menu
    step(
        "tray",
        (|| {
            std::thread::sleep(Duration::from_millis(1100));
            let item = format!("{CLIENT} in {}", s(&work));
            let before = record(&rec1, 0)?["pid"].as_u64().unwrap_or(0);
            tray(&item)?;
            let again = record(&rec1, before)?;
            note(&mut known, &again, &rec1);
            save(&known)?;
            require(!showing(), "the tray's start asked a native confirmation")
        })(),
    )?;

    // ---- key consumers
    let (rec2, rec3, rec4) = (at("antigravity"), at("copilot"), at("bridge"));
    let mut bridge = String::new();
    step(
        "key consumers",
        (|| {
            // The agent's own environment, through a terminal.
            launch_new(
                app,
                main,
                json!({
                    "client": "Antigravity fixture", "kind": "terminal", "program": "pluxy-agent",
                    "args": [AGENT, rec2], "cwd": work, "terminal": "x-terminal-emulator",
                    "key": secret,
                    "environment": {"set": {"FIXTURE_TERMINAL_RECORD": at("antigravity-terminal")},
                                    "secrets": {"GEMINI_API_KEY": "key"}},
                }),
                &["GEMINI_API_KEY", LABEL, &s(&terminal)],
                &[&key],
                false,
            )?;
            let r = record(&rec2, 0)?;
            note(&mut known, &r, &rec2);
            save(&known)?;
            require(
                r["env"]["GEMINI_API_KEY"] == key.as_str(),
                "the agent did not get its key",
            )?;
            require(
                r["core"] == json!([1, 1]),
                format!("the agent holding a key: core {}", r["core"]),
            )?;
            // The final process's key, base URL and model, through an executable override.
            launch_new(
                app,
                main,
                json!({
                    "client": "Copilot CLI fixture", "kind": "direct", "program": "copilot",
                    "override": override_agent, "args": [AGENT, rec3], "cwd": work, "key": secret,
                    "environment": {
                        "set": {"COPILOT_PROVIDER_BASE_URL": "http://127.0.0.1:9/v1",
                                "COPILOT_PROVIDER_MODEL": "fixture-model"},
                        "secrets": {"COPILOT_PROVIDER_API_KEY": "key"},
                    },
                }),
                &[
                    "COPILOT_PROVIDER_API_KEY",
                    "COPILOT_PROVIDER_BASE_URL",
                    LABEL,
                    &s(&override_agent),
                ],
                &[&key],
                false,
            )?;
            let r = record(&rec3, 0)?;
            note(&mut known, &r, &rec3);
            save(&known)?;
            require(
                r["argv"][0] == json!(s(&override_agent))
                    && r["env"]["COPILOT_PROVIDER_API_KEY"] == key.as_str()
                    && r["env"]["COPILOT_PROVIDER_BASE_URL"] == "http://127.0.0.1:9/v1"
                    && r["env"]["COPILOT_PROVIDER_MODEL"] == "fixture-model",
                format!(
                    "the final process's override, key, base URL and model: argv {}",
                    r["argv"]
                ),
            )?;
            // The credential bridge's key on its stdin, a managed runtime.
            let v = launch_new(
                app,
                main,
                json!({
                    "client": "VS Code bridge fixture", "kind": "direct", "program": s(&agent),
                    "args": [AGENT, rec4], "cwd": work, "lifetime": "managed", "key": secret,
                    "stdin": "key", "environment": {"set": {"FIXTURE_READ_STDIN": "1"}},
                }),
                &["stdin", LABEL, "managed"],
                &[&key],
                false,
            )?;
            bridge = v["output"]["runtime"]
                .as_str()
                .unwrap_or_default()
                .to_string();
            require(
                bridge.starts_with("rt-"),
                format!("the bridge's runtime: {v}"),
            )?;
            let r = record(&rec4, 0)?;
            require(
                r["stdin"] == key.as_str(),
                "the bridge did not get its key on stdin",
            )?;
            require(
                r["env"].get("FIXTURE_KEY").is_none() && !r["env"].to_string().contains(&key),
                "the bridge got its key in its environment too",
            )?;
            // The execution program's frames carried the key's reference, never the key.
            // The host counts them as it writes them (native-test builds only).
            let st = ok(main, "program_status", json!({"package": EXECUTION}))?;
            let references = st["test_frames"]["references"].as_u64().unwrap_or(0);
            let values = st["test_frames"]["values"].as_u64().unwrap_or(u64::MAX);
            require(
                references >= 3 && values == 0,
                format!("the execution program's frames: {references} with a reference, {values} with a key"),
            )?;
            out["frames"] = json!({"references": references, "values": values});
            Ok(())
        })(),
    )?;
    let bridge_pid = record(&rec4, 0)?["pid"].as_u64().unwrap_or(0);

    // ---- crash: the execution program killed; restarted, it controls the bridge's runtime
    step(
        "crash",
        (|| {
            let st = ok(main, "program_status", json!({"package": EXECUTION}))?;
            let pid = st["pid"]
                .as_u64()
                .ok_or(format!("the execution program's status: {st}"))?;
            unsafe { libc::kill(pid as libc::pid_t, libc::SIGKILL) };
            let wait = wait_for("the execution program's crash", || {
                let st = ok(main, "program_status", json!({"package": EXECUTION})).ok()?;
                (st["state"] == "crashed").then(|| st["retry_in_ms"].as_u64().unwrap_or(0))
            })?;
            std::thread::sleep(Duration::from_millis(wait + 300));
            require(
                owned(pid1, &s(&rec1)),
                "the agent ended with the execution program's crash",
            )?;
            let r = call(main, EXECUTION, "runtime", json!({"op": "list"}))?;
            require(
                r["ok"] == true && r["value"].to_string().contains(&bridge),
                format!("the restarted execution program's runtimes: {r}"),
            )?;
            let r = call(
                main,
                EXECUTION,
                "runtime",
                json!({"op": "restart", "runtime": bridge}),
            )?;
            require(
                r["ok"] == true && r["value"]["state"] == "running",
                format!("restart: {r}"),
            )?;
            let fresh = record(&rec4, bridge_pid)?;
            require(
                fresh["stdin"] == key.as_str(),
                "the restarted bridge did not get its key",
            )?;
            require(
                ended(bridge_pid, &s(&rec4), Duration::from_secs(10)),
                "the bridge's old process runs on after its restart",
            )?;
            let r = call(
                main,
                EXECUTION,
                "runtime",
                json!({"op": "stop", "runtime": bridge}),
            )?;
            require(
                r["ok"] == true && r["value"]["state"] != "running",
                format!("stop: {r}"),
            )?;
            let fresh_pid = fresh["pid"].as_u64().unwrap_or(0);
            require(
                ended(fresh_pid, &s(&rec4), Duration::from_secs(10)),
                "the bridge runs on after its stop",
            )?;
            require(
                owned(pid1, &s(&rec1)),
                "the agent ended with a runtime's stop",
            )
        })(),
    )?;

    // The launch the launcher remembered, as its input names it.
    let same = json!({
        "executable": s(&terminal), "argv": argv1, "cwd": work, "program": s(&agent),
        "lifetime": "detached", "environment": {"set": {"FIXTURE_TERMINAL_RECORD": trec}},
    });

    // ---- chain: the same launch through another package asks, even after a native gesture
    step(
        "chain",
        (|| {
            let rx = clicked(
                app,
                RELAY,
                "relay",
                json!({"package": EXECUTION, "op": "launch", "input": same}),
            )?;
            let text = dialog_with(app, &[RELAY, EXECUTION, &s(&terminal)])?;
            require(
                !text.contains("Remember"),
                "another chain's confirmation offers Remember",
            )?;
            press("alt+c")?;
            match answer(&rx)? {
                Ok(v) => require(
                    v["answer"].get("error").is_some(),
                    format!("the relayed launch went ahead though cancelled: {v}"),
                ),
                Err(_) => Ok(()),
            }
        })(),
    )?;

    // ---- page: Start from a page always asks, rate-limited, with a launch notice
    step(
        "page",
        (|| {
            let mut page = Page::open(app, main, PAGE)?;
            let id = page.send(EXECUTION, "launch", same.clone())?;
            dialog_with(app, &[PAGE, EXECUTION, &s(&terminal)])?;
            notice(app, &[PAGE, "awaiting native confirmation"])?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+c")?;
            require(
                page.reply(&id)?["error"]["code"] == "forbidden",
                "the cancelled page Start",
            )?;
            // A flood: one confirmation, the rest refused.
            std::thread::sleep(Duration::from_millis(1100));
            let ids: Vec<String> = (0..4)
                .map(|_| page.send(EXECUTION, "launch", same.clone()))
                .collect::<Result<_, _>>()?;
            dialog_with(app, &[PAGE])?;
            let mut refused = 0;
            for id in &ids[1..] {
                if page.reply(id)?["error"]["code"] == "forbidden" {
                    refused += 1;
                }
            }
            require(
                refused == 3,
                format!("{refused} of a flood's 3 extra Starts refused"),
            )?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+c")?;
            require(
                page.reply(&ids[0])?["error"]["code"] == "forbidden",
                "the flood's Start",
            )?;
            // Revoked: refused before any confirmation.
            ok(
                main,
                "device_imports",
                json!({"id": PAGE, "allowed": false}),
            )?;
            std::thread::sleep(Duration::from_millis(1100));
            let id = page.send(EXECUTION, "launch", same.clone())?;
            let r = page.reply(&id)?;
            require(
                r["error"]["code"] == "forbidden" && !showing(),
                format!("the page's Start after its consent was revoked: {r}"),
            )?;
            page.close(main)
        })(),
    )?;

    // ---- changed: a replaced agent and a changed terminal ask again
    step(
        "changed",
        (|| {
            std::thread::sleep(Duration::from_millis(1100));
            let fresh = bin.join(".pluxy-agent.new");
            std::fs::copy(&agent, &fresh).map_err(|e| e.to_string())?;
            std::fs::rename(&fresh, &agent).map_err(|e| e.to_string())?;
            let before = record(&rec1, 0)?["pid"].as_u64().unwrap_or(0);
            let slot = start(main, "launcher_start", json!({"id": entry}))?;
            dialog_with(app, &[EXECUTION, &s(&agent)])?;
            press("alt+r")?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(
                r["ok"] == true,
                format!("the updated agent's confirmed start: {r}"),
            )?;
            let again = record(&rec1, before)?;
            note(&mut known, &again, &rec1);
            save(&known)?;
            // Remembered again: one click.
            std::thread::sleep(Duration::from_millis(1100));
            let before = again["pid"].as_u64().unwrap_or(0);
            let r = unasked(main, &start(main, "launcher_start", json!({"id": entry}))?)?;
            require(r["ok"] == true, format!("the start remembered again: {r}"))?;
            let again = record(&rec1, before)?;
            note(&mut known, &again, &rec1);
            save(&known)?;
            // The terminal's metadata changed.
            std::thread::sleep(Duration::from_millis(1100));
            let f = std::fs::File::options()
                .write(true)
                .open(&terminal)
                .map_err(|e| e.to_string())?;
            f.set_modified(std::time::SystemTime::now() - Duration::from_secs(3600))
                .map_err(|e| e.to_string())?;
            asks(
                app,
                main,
                "launcher_start",
                json!({"id": entry}),
                &[EXECUTION, &s(&terminal)],
            )
        })(),
    )?;

    // ---- idle: the execution program stops when idle; the agents run on
    step(
        "idle",
        (|| {
            let deadline = Instant::now() + Duration::from_secs(90);
            loop {
                let st = ok(main, "program_status", json!({"package": EXECUTION}))?;
                if st["state"] == "idle" {
                    break;
                }
                require(
                    Instant::now() < deadline,
                    format!("the execution program never stopped when idle: {st}"),
                )?;
                std::thread::sleep(Duration::from_millis(500));
            }
            require(
                owned(pid1, &s(&rec1)),
                "the agent ended with the execution program's idle stop",
            )
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
            let menu = wait_for("the tray's Start… menu without the launch", || {
                tray("inspect")
                    .ok()
                    .filter(|m| m.contains("No remembered launches") && !m.contains(CLIENT))
            })?;
            out["tray"] = json!(menu.lines().count());
            Ok(())
        })(),
    )?;

    // ---- disable: with a managed runtime running, disabling asks
    let rec5 = at("managed");
    step(
        "disable",
        (|| {
            launch_new(
                app,
                main,
                json!({
                    "client": "Managed fixture", "kind": "direct", "program": s(&override_agent),
                    "args": [AGENT, rec5], "cwd": work, "lifetime": "managed",
                }),
                &["managed"],
                &[],
                false,
            )?;
            let m = record(&rec5, 0)?;
            let pid = m["pid"].as_u64().unwrap_or(0);
            asks(
                app,
                main,
                "device_enable",
                json!({"id": EXECUTION, "enabled": false}),
                &[EXECUTION, "runtime"],
            )?;
            let enabled = |main: &Webview| -> Result<bool, String> {
                let list = ok(main, "device_list", json!({}))?;
                Ok(list["packages"]
                    .as_array()
                    .and_then(|a| a.iter().find(|p| p["id"] == EXECUTION).cloned())
                    .is_some_and(|p| p["enabled"] == true))
            };
            require(
                enabled(main)? && owned(pid, &s(&rec5)),
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
            require(
                ended(pid, &s(&rec5), Duration::from_secs(10)),
                "the runtime runs on after the disable",
            )?;
            require(owned(pid1, &s(&rec1)), "the agent ended with the disable")
        })(),
    )?;

    out["passed"] = json!(true);
    log("launcher", out);
    Ok(())
}
