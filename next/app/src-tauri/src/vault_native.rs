//! The native test's check of the vault, secret delivery, secret references and journal recovery
//! (native-test builds with `artifact-test-trust` only). It drives the real app through the
//! window's own commands, the real native key entry and confirmations and a real page webview,
//! with the signed fixture tools `native/vault.ts` built: pluxy/writer (a config writer),
//! pluxy/exec (an execution program, which may hold only references) and pluxy/consumer (the
//! final consumer of a key, as a credential bridge is). The key and the backup canary come from
//! `keys.json`, made there; the app never chose them. Each refusal has a positive control on the
//! same route:
//!
//! - secret: a key typed into native key entry is kept in the vault; the window's list names its
//!   ID and label, never its value;
//! - deliver: the window's write call of the writer with that key asks a native confirmation
//!   naming the key; the writer gets the key on its pipe and writes it, keeps the file's old bytes
//!   as a vault backup and forwards a reference to exec, which passes it on to the consumer; that
//!   call asks again (naming the whole chain and the key), and the consumer gets the key. Exec's
//!   own record of what it received holds a reference and neither the key nor the backup canary;
//!   the window's answer and the writer's log lines have the key redacted;
//! - recipient: exec passing on a reference given to the writer is refused, before any
//!   confirmation, and the consumer gets nothing;
//! - call: the writer passing on a reference from one of its earlier calls is refused;
//! - undeclared: the writer passing its reference to an operation its import does not allow
//!   secret references for, or passing the key's value itself, is refused;
//! - page: the writer's own page reads its report with the key redacted, and cannot pass a secret;
//! - generation: after the writer's update (a swap), its new release passing on a reference its
//!   old release was given is refused;
//! - recovery: a write that crashes before it commits its journal has an unknown outcome; the next
//!   start's describe names the open journal, the writer reconciles it as committed without
//!   writing again (its file was written once), and the window's status reports it;
//! - audit: neither canary reached the window's or the page's webview (DOM, storage, callbacks,
//!   messages); the runner checks the app's stdout and stderr for them too.
//!
//! One line on stderr: `native-test: vault <JSON>`.

use super::admission::{dialog_text, ok, press, require, settle, start, x};
use super::key_entry_test::{clean_webview, watch};
use super::routing::{call, enable, install, showing, Page};
use super::{log, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const WRITER: &str = "pluxy/writer";
const EXEC: &str = "pluxy/exec";
const CONSUMER: &str = "pluxy/consumer";
const LABEL: &str = "fixture client key";
const REDACTED: &str = "[redacted]";

/// Types key into the native key entry the vault opens, and accepts it. The bytes go to xdotool
/// on its stdin, never in argv.
fn type_key(key: &str) -> Result<(), String> {
    let id = wait_for("the native secret entry", || {
        x(&["search", "--onlyvisible", "--name", "^pluxy secret$"])
            .ok()
            .and_then(|s| s.lines().last().map(String::from))
    })?;
    x(&["windowfocus", "--sync", &id])?;
    use std::io::Write;
    let mut child = std::process::Command::new("xdotool")
        .args(["type", "--clearmodifiers", "--file", "-"])
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
        .map_err(|_| "could not start xdotool")?;
    child
        .stdin
        .take()
        .ok_or("no input pipe")?
        .write_all(key.as_bytes())
        .map_err(|_| "could not type the key")?;
    require(
        child.wait().map_err(|e| e.to_string())?.success(),
        "xdotool type failed",
    )?;
    x(&["key", "--clearmodifiers", "alt+o"]).map(|_| ())
}

/// The text of the native confirmation showing now that contains every one of needles.
fn dialog_with(app: &AppHandle, needles: &[&str]) -> Result<String, String> {
    let deadline = Instant::now() + Duration::from_secs(60);
    let mut last = String::new();
    while Instant::now() < deadline {
        if let Ok(t) = dialog_text(app) {
            if needles.iter().all(|n| t.contains(n)) {
                return Ok(t);
            }
            last = t;
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    Err(format!(
        "no native confirmation naming {needles:?}; the last one said: {last}"
    ))
}

/// Waits for the answer of the call in slot, failing if a native confirmation shows meanwhile.
fn settle_unasked(main: &Webview, slot: &str) -> Result<Value, String> {
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(r) = super::eval(
            main,
            &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
        )
        .filter(|v| !v.is_null())
        {
            return Ok(r);
        }
        if showing() {
            press("alt+c")?;
            return Err("a native confirmation showed; the call should be refused first".into());
        }
        require(Instant::now() < deadline, "no answer")?;
        std::thread::sleep(Duration::from_millis(200));
    }
}

fn read(p: &Path) -> String {
    std::fs::read_to_string(p).unwrap_or_default()
}

/// The writer's apply call from the window, with its confirmation accepted; then waits for its
/// answer, with no further confirmation.
fn apply(app: &AppHandle, main: &Webview, input: Value) -> Result<Value, String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": WRITER, "op": "apply", "input": input}),
    )?;
    dialog_with(app, &["pluxy/writer apply", LABEL])?;
    press("alt+o")?;
    std::thread::sleep(Duration::from_millis(500));
    settle_unasked(main, &slot)
}

/// What the writer's forwarded call answered, inside the window's answer r.
fn forwarded(r: &Value) -> &Value {
    &r["value"]["answer"]
}

fn refused(r: &Value, says: &str, what: &str) -> Result<(), String> {
    let a = forwarded(r);
    require(
        r["ok"] == true
            && a["error"]["code"] == "permission"
            && a["error"]["message"]
                .as_str()
                .is_some_and(|m| m.contains(says))
            && a.get("output").is_none(),
        format!("{what} should be refused with {says:?}: {r}"),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let keys: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("keys.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let key = keys["key"].as_str().ok_or("no key")?.to_string();
    let backup = keys["backup"]
        .as_str()
        .ok_or("no backup canary")?
        .to_string();
    let canaries = [key.as_str(), backup.as_str()];
    let work = fixtures.join("work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let at = |name: &str| work.join(name);
    let consumer = json!({"package": CONSUMER, "op": "deliver"});
    watch(main)?;
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

    // ---- secret
    let mut secret = String::new();
    step(
        "secret",
        (|| {
            let before = ok(main, "vault_secrets", json!({}))?;
            require(
                before["secrets"] == json!([]),
                format!("the vault before: {before}"),
            )?;
            let slot = start(main, "vault_secret_add", json!({"label": LABEL}))?;
            type_key(&key)?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("vault_secret_add: {r}"))?;
            secret = r["value"]["id"].as_str().unwrap_or_default().to_string();
            require(secret.starts_with("s-"), format!("the new secret: {r}"))?;
            let list = ok(main, "vault_secrets", json!({}))?;
            require(
                list["secrets"].as_array().is_some_and(|a| a.len() == 1)
                    && list["secrets"][0]["id"] == secret
                    && list["secrets"][0]["label"] == LABEL
                    && !list.to_string().contains(&key),
                format!("the vault's list: {list}"),
            )
        })(),
    )?;

    // ---- enable the three tools (each import's target first)
    step(
        "enable",
        (|| {
            for id in [CONSUMER, EXEC, WRITER] {
                install(main, "curated", id, "1.0.0")?;
            }
            for id in [CONSUMER, EXEC, WRITER] {
                enable(app, main, id)?;
            }
            Ok(())
        })(),
    )?;

    // ---- deliver: the positive control of every route below
    step(
        "deliver",
        (|| {
            let file = at("config");
            std::fs::write(&file, format!("original {backup}\n")).map_err(|e| e.to_string())?;
            let slot = start(
                main,
                "program_call",
                json!({"package": WRITER, "op": "apply", "input": {
                    "key": secret, "file": file,
                    "forward": {"package": EXEC, "op": "run", "input": {
                        "record": at("exec-record"), "out": at("consumer-out"),
                        "consumer": consumer}}}}),
            )?;
            let first = dialog_with(app, &["pluxy/writer apply", LABEL])?;
            press("alt+o")?;
            let second = dialog_with(app, &["pluxy/consumer deliver", EXEC, LABEL])?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            let v = &r["value"];
            require(
                r["ok"] == true
                    && v["written"] == true
                    && v["echo"] == REDACTED
                    && v["answer"]["output"]["answer"]["output"]["received"] == true,
                format!("the writer's call: {r}"),
            )?;
            require(
                read(&file) == format!("key={key}\n"),
                "the writer did not write the key it was given",
            )?;
            require(
                read(&at("consumer-out")) == key,
                "the consumer did not get the key",
            )?;
            let record = read(&at("exec-record"));
            let frame: Value = serde_json::from_str(record.lines().next().unwrap_or_default())
                .map_err(|e| format!("exec's record: {e}"))?;
            require(
                frame["input"]["key"]
                    .as_str()
                    .is_some_and(|r| r.starts_with("r-"))
                    && frame.get("secrets").is_none()
                    && !canaries.iter().any(|c| record.contains(c)),
                format!("exec received more than a reference: {frame}"),
            )?;
            let status = ok(main, "program_status", json!({"package": WRITER}))?;
            let log = status["log"].to_string();
            require(
                log.contains(REDACTED) && !canaries.iter().any(|c| log.contains(c)),
                format!("the writer's log: {log}"),
            )?;
            out["confirmations"] = json!([first, second]);
            Ok(())
        })(),
    )?;

    // ---- recipient: exec passes on the writer's own reference
    let leaked = at("leaked-reference");
    step(
        "recipient",
        (|| {
            let r = apply(
                app,
                main,
                json!({"key": secret, "file": at("config-2"), "leak": leaked,
                    "forward": {"package": EXEC, "op": "run", "input": {
                        "steal": leaked, "record": at("exec-record-2"),
                        "out": at("consumer-out-2"), "consumer": consumer}}}),
            )?;
            let inner = &forwarded(&r)["output"]["answer"];
            require(
                r["ok"] == true
                    && inner["error"]["code"] == "permission"
                    && inner["error"]["message"]
                        .as_str()
                        .is_some_and(|m| m.contains("given to pluxy/writer")),
                format!("exec passing on the writer's reference: {r}"),
            )?;
            require(
                !at("consumer-out-2").exists(),
                "the consumer got a key through the wrong recipient",
            )
        })(),
    )?;

    // ---- call: a reference from one of the writer's earlier calls
    step(
        "call",
        (|| {
            let r = apply(
                app,
                main,
                json!({"key": secret, "file": at("config-3"),
                    "forward": {"package": EXEC, "op": "run", "key_from": leaked, "input": {
                        "record": at("exec-record-3"), "out": at("consumer-out-3"),
                        "consumer": consumer}}}),
            )?;
            refused(&r, "another call", "a reference from an earlier call")?;
            require(
                !at("exec-record-3").exists(),
                "exec was called with a reference from an earlier call",
            )
        })(),
    )?;

    // ---- undeclared: an import with no secret use, and the value itself
    step(
        "undeclared",
        (|| {
            let r = apply(
                app,
                main,
                json!({"key": secret, "file": at("config-4"),
                    "forward": {"package": EXEC, "op": "peek", "input": {}}}),
            )?;
            refused(
                &r,
                "secret",
                "a reference to peek, which the import does not allow",
            )?;
            let r = apply(
                app,
                main,
                json!({"key": secret, "file": at("config-5"),
                    "forward": {"package": EXEC, "op": "run", "raw": true, "input": {
                        "record": at("exec-record-5"), "out": at("consumer-out-5"),
                        "consumer": consumer}}}),
            )?;
            refused(&r, "secret", "passing the key's value")?;
            require(
                !at("exec-record-5").exists(),
                "exec was called with the key's value",
            )
        })(),
    )?;

    // ---- page
    step(
        "page",
        (|| {
            let mut page = Page::open(app, main, WRITER)?;
            watch(page.webview())?;
            let r = page.call(WRITER, "report", json!({}))?;
            require(
                r["result"]["echo"] == REDACTED && r["result"]["calls"].is_u64(),
                format!("the writer's page reading its report: {r}"),
            )?;
            let r = page.call(
                WRITER,
                "apply",
                json!({"key": secret, "file": at("config-page")}),
            )?;
            require(
                r["error"]["code"] == "forbidden" && !at("config-page").exists(),
                format!("the page passing a secret: {r}"),
            )?;
            clean_webview(page.webview(), &canaries)?;
            page.close(main)
        })(),
    )?;

    // ---- generation: the writer's update, then its old release's reference
    step(
        "generation",
        (|| {
            let slot = start(
                main,
                "device_install",
                json!({"registry": "curated", "id": WRITER, "version": "1.1.0"}),
            )?;
            dialog_with(app, &[WRITER, "1.1.0"])?;
            press("alt+o")?;
            require(
                settle(main, &slot)?["ok"] == true,
                "the writer's confirmed update",
            )?;
            let r = apply(
                app,
                main,
                json!({"key": secret, "file": at("config-6"),
                    "forward": {"package": EXEC, "op": "run", "key_from": leaked, "input": {
                        "record": at("exec-record-6"), "out": at("consumer-out-6"),
                        "consumer": consumer}}}),
            )?;
            refused(&r, "release", "the old release's reference")?;
            require(
                !at("exec-record-6").exists(),
                "exec was called with the old release's reference",
            )
        })(),
    )?;

    // ---- recovery
    step(
        "recovery",
        (|| {
            let file = at("config-crash");
            std::fs::write(&file, "before\n").map_err(|e| e.to_string())?;
            let r = apply(
                app,
                main,
                json!({"key": secret, "file": file, "crash": true}),
            )?;
            require(
                r["ok"] == false
                    && r["error"]
                        .as_str()
                        .is_some_and(|e| e.contains("outcome is unknown")),
                format!("the crashed write: {r}"),
            )?;
            let status = ok(main, "program_status", json!({"package": WRITER}))?;
            let open: Vec<Value> = status["journals"]
                .as_array()
                .map(|a| {
                    a.iter()
                        .filter(|j| j["outcome"].is_null())
                        .cloned()
                        .collect()
                })
                .unwrap_or_default();
            require(
                open.len() == 1 && open[0]["op"] == "apply",
                format!("the open journal after the crash: {status}"),
            )?;
            let journal = open[0]["id"].clone();
            // After the crash's backoff.
            std::thread::sleep(Duration::from_millis(1500));
            let r = call(main, WRITER, "report", json!({}))?;
            require(
                r["ok"] == true
                    && r["value"]["reconciled"]
                        == json!([{"journal": journal, "outcome": "committed",
                            "reply": {"output": {"journal": journal, "outcome": "committed"}}}]),
                format!("the writer's reconciliation: {r}"),
            )?;
            require(
                read(&file) == format!("key={key}\n")
                    && read(&file.with_extension("writes")) == "1",
                "the crashed write was redone or lost",
            )?;
            let status = ok(main, "program_status", json!({"package": WRITER}))?;
            let j = status["journals"]
                .as_array()
                .and_then(|a| a.iter().find(|j| j["id"] == journal).cloned())
                .unwrap_or_default();
            require(
                j["outcome"] == "committed" && j["reconciled"] == true,
                format!("the reconciled journal in the window's status: {status}"),
            )?;
            out["journal"] = j;
            Ok(())
        })(),
    )?;

    // ---- audit
    step("audit", clean_webview(main, &canaries))?;

    out["passed"] = json!(true);
    log("vault", out);
    Ok(())
}
