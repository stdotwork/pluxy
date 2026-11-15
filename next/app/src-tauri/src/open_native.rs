//! The native test's check of opening a file in the person's own app (native-test builds with
//! `artifact-test-trust` only). It drives the real app, the real execution program and the real
//! native confirmations with the signed packages `native/open.ts` built: pluxy/execution (the
//! real execution program, built with its native-test feature so it takes the stand-in opener
//! from a search path) and pluxy/open-relay, a stand-in for a tool such as the Codex session
//! reader: a fixture tool whose `relay` calls execution's `prepare` and `launch`. The opener is
//! the fixture program as `xdg-open` in `bin/` (the one folder besides the system's the app takes
//! an opener from, in a native-test build), which appends what it was started with to
//! `bin/opened.jsonl` and exits. The tool may open files in `codex/`; `outside/` is not its.
//! Each refusal has a positive control on the same route:
//!
//! - enable: both tools installed and enabled;
//! - prepare: the tool asks the execution program to prepare opening a session file: the opener,
//!   the file as its only argument, its folder as the cwd, the file and the folder it may open
//!   files in named;
//! - cancel: the tool's launch of it asks a native confirmation whose headline names the file,
//!   its content type, the folder and the opener, with no Remember; cancelled, nothing runs;
//! - open: confirmed, the opener runs once with exactly that file, in its folder;
//! - outside: a file outside the folder (named directly, through `..`, or through a link inside
//!   the folder), an executable file inside it, a file that is not the launch's argument, a launch
//!   naming a file but no folder, and the whole file system as the folder are each refused
//!   before any confirmation shows; the execution program refuses to prepare the file outside;
//! - type: an HTML file named `.jsonl` and a `.desktop` file are refused;
//! - shape: an extra argument and an environment variable are refused; the opener ran once in all.
//!
//! One line on stderr: `native-test: open <JSON>`.

use super::admission::{press, require};
use super::routing::{answer, clicked, enable, install, showing};
use super::vault::dialog_with;
use super::{log, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const EXECUTION: &str = "pluxy/execution";
const RELAY: &str = "pluxy/open-relay";
/// What the host and the execution program say of a file outside the folder a tool may open.
const INSIDE: &str = "must be inside its within folder";

/// The tool's relayed call of op of the execution program with input, from a native click in the
/// window: the answer the tool got, `{"output": ...}` or `{"error": ...}`. A native confirmation
/// that shows meanwhile fails it (and is cancelled).
fn unasked(app: &AppHandle, op: &str, input: Value) -> Result<Value, String> {
    let rx = clicked(
        app,
        RELAY,
        "relay",
        json!({"package": EXECUTION, "op": op, "input": input}),
    )?;
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        match rx.try_recv() {
            Ok(Ok(v)) => return Ok(v["answer"].clone()),
            Ok(Err(e)) => return Err(format!("the relay call failed: {e:?}")),
            Err(std::sync::mpsc::TryRecvError::Disconnected) => {
                return Err("the relay call gave no answer".into())
            }
            Err(std::sync::mpsc::TryRecvError::Empty) => {}
        }
        if showing() {
            press("alt+c")?;
            return Err(format!(
                "{op} {input} asked for a native confirmation; it should not"
            ));
        }
        require(Instant::now() < deadline, format!("{op}: no answer"))?;
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// Requires the tool's relayed call of op with input refused before any confirmation, with an
/// error whose message contains says.
fn refused(app: &AppHandle, op: &str, input: Value, says: &str) -> Result<(), String> {
    // Confirmations from a tool's chain: one attempt per second.
    std::thread::sleep(Duration::from_millis(1100));
    let a = unasked(app, op, input.clone())?;
    let message = a["error"]["message"].as_str().unwrap_or_default();
    require(
        a.get("output").is_none() && message.contains(says),
        format!("{op} {input} should be refused saying {says:?}: {a}"),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let execution_version =
        std::fs::read_to_string(fixtures.join("execution-version")).unwrap_or_default();
    let canonical =
        |p: PathBuf| std::fs::canonicalize(&p).map_err(|e| format!("{}: {e}", p.display()));
    let s = |p: &Path| p.to_string_lossy().into_owned();
    let bin = canonical(fixtures.join("bin"))?;
    let opener = bin.join("xdg-open");
    let codex = canonical(fixtures.join("codex"))?;
    let folder = codex.join("sessions/2026/11/15");
    let file = folder.join("rollout-2026-11-15T03-00-00-session.jsonl");
    let outside = canonical(fixtures.join("outside"))?;
    let private = outside.join("private.txt");
    // What the stand-in opener ran with, a line each time it ran.
    let opened = || -> Vec<Value> {
        std::fs::read_to_string(bin.join("opened.jsonl"))
            .unwrap_or_default()
            .lines()
            .filter_map(|l| serde_json::from_str(l).ok())
            .collect()
    };
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

    step(
        "enable",
        (|| {
            for (id, version) in [(EXECUTION, execution_version.as_str()), (RELAY, "1.0.0")] {
                install(main, "curated", id, version)?;
                enable(app, main, id)?;
            }
            Ok(())
        })(),
    )?;

    // ---- prepare: the opener, the file, its folder and the folder it may open files in
    let mut prepared = Value::Null;
    step(
        "prepare",
        (|| {
            let a = unasked(
                app,
                "prepare",
                json!({"kind": "open", "file": s(&file), "within": s(&codex), "search_path": [s(&bin)]}),
            )?;
            let want = json!({
                "executable": s(&opener), "argv": [s(&file)], "cwd": s(&folder),
                "file": s(&file), "within": s(&codex),
            });
            require(
                a["output"] == want,
                format!("prepared opening the session file: {a}, not {want}"),
            )?;
            prepared = a["output"].clone();
            Ok(())
        })(),
    )?;

    // ---- cancel: the confirmation's headline names the file, its type, the folder and the opener
    step(
        "cancel",
        (|| {
            let rx = clicked(
                app,
                RELAY,
                "relay",
                json!({"package": EXECUTION, "op": "launch", "input": prepared}),
            )?;
            let head = format!("Opens the file {} (", s(&file));
            let tail = format!(", inside {}) with {}.", s(&codex), s(&opener));
            let text = dialog_with(app, &[RELAY, EXECUTION, &head, &tail])?;
            require(
                !text.contains("Remember"),
                "a tool's open confirmation offers Remember",
            )?;
            // The content type the app found for a JSON Lines session file, as the dialog says.
            out["type"] = json!(text
                .split(&head)
                .nth(1)
                .and_then(|t| t.split(", inside").next())
                .unwrap_or_default());
            press("alt+c")?;
            let v = answer(&rx)?.map_err(|e| format!("the relay call failed: {e:?}"))?;
            require(
                v["answer"].get("error").is_some(),
                format!("the open went ahead though cancelled: {v}"),
            )?;
            std::thread::sleep(Duration::from_millis(1500));
            require(
                opened().is_empty(),
                "the opener ran though the open was cancelled",
            )
        })(),
    )?;

    // ---- open: confirmed, the opener runs once with exactly that file, in its folder
    step(
        "open",
        (|| {
            std::thread::sleep(Duration::from_millis(1100));
            let rx = clicked(
                app,
                RELAY,
                "relay",
                json!({"package": EXECUTION, "op": "launch", "input": prepared}),
            )?;
            dialog_with(app, &[RELAY, &s(&opener), &s(&file), "Opens the file"])?;
            // A tool chain's dialog takes its Confirm only a second after it shows.
            std::thread::sleep(Duration::from_millis(1300));
            press("alt+o")?;
            let v = answer(&rx)?.map_err(|e| format!("the relay call failed: {e:?}"))?;
            require(
                v["answer"]["output"]["lifetime"] == "detached",
                format!("the confirmed open: {v}"),
            )?;
            let r = wait_for("the opener's record", || opened().into_iter().next())?;
            require(
                r["argv"] == json!([s(&opener), s(&file)]) && r["cwd"] == json!(s(&folder)),
                format!("the opener ran with {r}"),
            )?;
            out["opened"] = r["argv"].clone();
            Ok(())
        })(),
    )?;

    // The launch of file inside within, as the opener would run it.
    let opens = |f: &Path, within: &Path| {
        json!({"executable": s(&opener), "argv": [s(f)], "cwd": s(&folder),
               "file": s(f), "within": s(within)})
    };

    // ---- outside: files the tool may not name, refused before any confirmation
    step(
        "outside",
        (|| {
            refused(app, "launch", opens(&private, &codex), INSIDE)?;
            let dotted = codex.join("../outside/private.txt");
            refused(app, "launch", opens(&dotted, &codex), INSIDE)?;
            refused(
                app,
                "launch",
                opens(&folder.join("rollout-link.jsonl"), &codex),
                INSIDE,
            )?;
            refused(
                app,
                "launch",
                opens(&folder.join("run.sh"), &codex),
                "must not be executable",
            )?;
            let mut other = opens(&file, &codex);
            other["argv"] = json!([s(&folder.join("run.sh"))]);
            refused(app, "launch", other, "file must be one of its argv")?;
            let mut bare = opens(&file, &codex);
            bare.as_object_mut().map(|m| m.remove("within"));
            refused(app, "launch", bare, "names its within folder")?;
            refused(
                app,
                "launch",
                opens(&file, Path::new("/")),
                "a folder above it",
            )?;
            refused(
                app,
                "prepare",
                json!({"kind": "open", "file": s(&private), "within": s(&codex), "search_path": [s(&bin)]}),
                INSIDE,
            )
        })(),
    )?;

    // ---- type: files a desktop would open in an app that runs them
    step(
        "type",
        (|| {
            refused(
                app,
                "launch",
                opens(&folder.join("rollout-page.jsonl"), &codex),
                "plain text, not text/html",
            )?;
            refused(
                app,
                "launch",
                opens(&folder.join("app.desktop"), &codex),
                ".desktop",
            )
        })(),
    )?;

    // ---- shape: an open launch runs the opener with the file alone, in the launch environment
    step(
        "shape",
        (|| {
            let mut extra = prepared.clone();
            extra["argv"] = json!([s(&file), "--new-window"]);
            refused(app, "launch", extra, "runs exactly")?;
            let mut env = prepared.clone();
            env["environment"] = json!({"set": {"BROWSER": s(&opener)}});
            refused(app, "launch", env, "takes no environment")?;
            require(
                opened().len() == 1,
                format!("the opener ran {} times, not once", opened().len()),
            )
        })(),
    )?;

    out["passed"] = json!(true);
    log("open", out);
    Ok(())
}
