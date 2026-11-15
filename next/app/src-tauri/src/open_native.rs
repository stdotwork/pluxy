//! The native test's check of opening a file in the person's own app (native-test builds with
//! `artifact-test-trust` only). It drives the real app, the real execution program and the real
//! native confirmations with the signed packages `native/open.ts` built: pluxy/execution (the
//! real execution program) and pluxy/open-relay, a stand-in for a tool such as the Codex session
//! reader: a fixture tool whose `relay` calls execution's `prepare` and `launch`. The opener is
//! the fixture program as `xdg-open` in `bin/`, which writes what it was started with to its
//! record and exits. The tool may open files in `codex/`; `outside/` is not its. Each refusal
//! has a positive control on the same route:
//!
//! - enable: both tools installed and enabled;
//! - prepare: the tool asks the execution program to prepare opening a session file: the opener
//!   found on the search path, the file as its only argument, its folder as the cwd, the file
//!   and the folder it may open files in named;
//! - cancel: the tool's launch of it asks a native confirmation naming the tool, the opener, the
//!   file and the folder, with no Remember; cancelled, nothing runs;
//! - open: confirmed, the opener runs once with exactly that file, in its folder;
//! - outside: a file outside the folder (named directly, through `..`, or through a link inside
//!   the folder), an executable file inside it, a file that is not the launch's argument, a launch
//!   naming a file but no folder, and the whole file system as the folder are each refused
//!   before any confirmation shows; the execution program refuses to prepare the file outside.
//!
//! One line on stderr: `native-test: open <JSON>`.

use super::admission::{press, require};
use super::log;
use super::routing::{answer, clicked, enable, install, showing};
use super::spawn::record;
use super::vault::dialog_with;
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
    let records = canonical(fixtures.join("records"))?;
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
    // The launch of what was prepared, the opener writing its record to rec.
    let launch = |rec: &Path| {
        let mut l = prepared.clone();
        l["environment"] = json!({"set": {"FIXTURE_OPENER_RECORD": s(rec)}});
        l
    };

    // ---- cancel: the confirmation names the tool, the opener, the file and the folder
    step(
        "cancel",
        (|| {
            let rec = records.join("cancel");
            let rx = clicked(
                app,
                RELAY,
                "relay",
                json!({"package": EXECUTION, "op": "launch", "input": launch(&rec)}),
            )?;
            let text = dialog_with(
                app,
                &[
                    RELAY,
                    EXECUTION,
                    &s(&opener),
                    &s(&file),
                    &s(&codex),
                    "Opens",
                ],
            )?;
            require(
                !text.contains("Remember"),
                "a tool's open confirmation offers Remember",
            )?;
            press("alt+c")?;
            let v = answer(&rx)?.map_err(|e| format!("the relay call failed: {e:?}"))?;
            require(
                v["answer"].get("error").is_some(),
                format!("the open went ahead though cancelled: {v}"),
            )?;
            std::thread::sleep(Duration::from_millis(1500));
            require(
                !rec.exists(),
                "the opener ran though the open was cancelled",
            )
        })(),
    )?;

    // ---- open: confirmed, the opener runs once with exactly that file, in its folder
    step(
        "open",
        (|| {
            std::thread::sleep(Duration::from_millis(1100));
            let rec = records.join("open");
            let rx = clicked(
                app,
                RELAY,
                "relay",
                json!({"package": EXECUTION, "op": "launch", "input": launch(&rec)}),
            )?;
            dialog_with(app, &[RELAY, &s(&opener), &s(&file), "Opens"])?;
            // A tool chain's dialog takes its Confirm only a second after it shows.
            std::thread::sleep(Duration::from_millis(1300));
            press("alt+o")?;
            let v = answer(&rx)?.map_err(|e| format!("the relay call failed: {e:?}"))?;
            require(
                v["answer"]["output"]["lifetime"] == "detached",
                format!("the confirmed open: {v}"),
            )?;
            let r = record(&rec, 0)?;
            require(
                r["argv"] == json!([s(&opener), s(&file)]) && r["cwd"] == json!(s(&folder)),
                format!("the opener ran with {}", r),
            )?;
            out["opened"] = r["argv"].clone();
            Ok(())
        })(),
    )?;

    // ---- outside: files the tool may not name, refused before any confirmation
    step(
        "outside",
        (|| {
            let opens = |f: &Path, within: &Path| {
                json!({"executable": s(&opener), "argv": [s(f)], "cwd": s(&folder),
                       "file": s(f), "within": s(within)})
            };
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
                "cannot be / or the home folder",
            )?;
            refused(
                app,
                "prepare",
                json!({"kind": "open", "file": s(&private), "within": s(&codex), "search_path": [s(&bin)]}),
                INSIDE,
            )?;
            require(
                std::fs::read_dir(&records)
                    .map(|d| d.count() == 1)
                    .unwrap_or(false),
                "the opener ran for a refused file",
            )
        })(),
    )?;

    out["passed"] = json!(true);
    log("open", out);
    Ok(())
}
