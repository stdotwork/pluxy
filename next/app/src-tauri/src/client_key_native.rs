//! The native test's check of native one-time client-key capture (native-test builds with
//! `artifact-test-trust` only), in the vault check's session, after it (`vault_native.rs`), with
//! its signed fixture tools (`native/vault.ts`): pluxy/agent (a disposable agent: its `serve`
//! asks core with the client key it is given) and pluxy/consumer (another tool). It drives the
//! real app through the window's own commands, the real native confirmations and key entry, and
//! the app's own core. The keys core makes are read from that core's config file, the independent
//! record; the app never shows them. Each refusal has a positive control on the same route:
//!
//! - cancel: a capture the person cancels adds no key to core and none to the vault;
//! - capture: a confirmed capture adds one key to core's config and keeps it in the vault bound to
//!   the core, core's key ID and the agent; the window gets the secret's ID and binding only;
//! - serve: the agent, given the key for its `serve` after a native confirmation, is served by
//!   core with it (200); the key is in neither the agent's argv nor its environment nor its log;
//! - recipient: the key given to another tool is refused before any confirmation;
//! - deleted: a second captured key, deleted on core, is refused before any confirmation;
//! - changed core: with the app connected to another core (a listener of this check's), the key
//!   is refused before any confirmation and nothing reaches that core; connected to the app's own
//!   core again, it serves the agent again;
//! - enter: an existing client key typed into native key entry is kept bound the same way and
//!   serves the agent; a key core does not accept is refused and not kept;
//! - audit: no key reached the window's webview; the runner checks the app's stdout and stderr
//!   for them too (`client-keys`, written here as each key is learned).
//!
//! One line on stderr: `native-test: client_keys <JSON>`.

use super::admission::{ok, press, require, settle, start};
use super::key_entry_test::{clean_webview, watch};
use super::routing::{enable, install, showing};
use super::vault::{dialog_with, settle_unasked, type_key};
use super::{eval, log, wait_for};
use crate::App;
use serde_json::{json, Value};
use std::collections::BTreeSet;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

const AGENT: &str = "pluxy/agent";
const OTHER: &str = "pluxy/consumer";
const LABEL: &str = "fixture agent key";
const ENTRY: &str = "pluxy client key";

/// The client keys in the core config at path, its `client_keys` (the app writes that config as
/// JSON, and core keeps a JSON config JSON when it changes it).
fn core_keys(path: &Path) -> Result<BTreeSet<String>, String> {
    let text = std::fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let cfg: Value = serde_json::from_slice(&text)
        .map_err(|e| format!("{} is not JSON: {e}", path.display()))?;
    let keys = cfg["client_keys"]
        .as_array()
        .ok_or_else(|| format!("{} has no client_keys list", path.display()))?;
    Ok(keys
        .iter()
        .filter_map(Value::as_str)
        .map(String::from)
        .collect())
}

/// Adds key to the file the runner audits the app's output for.
fn note(file: &Path, key: &str) -> Result<(), String> {
    use std::io::Write;
    std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(file)
        .and_then(|mut f| writeln!(f, "{key}"))
        .map_err(|e| e.to_string())
}

/// Starts cmd with args and requires it refused saying says, before any native confirmation.
fn refused_unasked(main: &Webview, cmd: &str, args: Value, says: &str) -> Result<(), String> {
    let slot = start(main, cmd, args.clone())?;
    let r = settle_unasked(main, &slot)?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains(says)),
        format!("{cmd} {args} should be refused with {says:?}: {r}"),
    )
}

/// The agent's serve of secret, confirmed: its answer.
fn serve(
    app: &AppHandle,
    main: &Webview,
    secret: &str,
    base: &str,
    out: &Path,
) -> Result<Value, String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": AGENT, "op": "serve",
            "input": {"key": secret, "base": base, "out": out}}),
    )?;
    dialog_with(app, &["pluxy/agent serve", LABEL])?;
    press("alt+o")?;
    std::thread::sleep(Duration::from_millis(500));
    settle_unasked(main, &slot)
}

/// A capture of a client key for the agent, confirmed (or cancelled): the window's answer.
fn capture(app: &AppHandle, main: &Webview, base: &str, confirm: bool) -> Result<Value, String> {
    let slot = start(
        main,
        "client_key_capture",
        json!({"label": LABEL, "client": AGENT}),
    )?;
    // Answered without a confirmation: say so now, rather than wait for one.
    wait_for("the capture's native confirmation", || {
        if showing() {
            return Some(Ok(()));
        }
        eval(
            main,
            &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
        )
        .filter(|v| !v.is_null())
        .map(|r| Err(format!("the capture answered without asking: {r}")))
    })??;
    dialog_with(app, &[base, AGENT, LABEL])?;
    press(if confirm { "alt+o" } else { "alt+c" })?;
    settle(main, &slot)
}

/// The one key in after that is not in before.
fn new_key(before: &BTreeSet<String>, after: &BTreeSet<String>) -> Result<String, String> {
    let added: Vec<&String> = after.difference(before).collect();
    match added.as_slice() {
        [k] => Ok((*k).clone()),
        _ => Err(format!(
            "core's config gained {} keys, not one",
            added.len()
        )),
    }
}

/// Requires the capture answer r to name a client key of core origin base for the agent, masked
/// as key is, and not to hold key; gives the secret's ID and core's key ID.
fn captured(r: &Value, base: &str, key: &str) -> Result<(String, String), String> {
    let v = &r["value"];
    let ck = &v["client_key"];
    let masked = format!("{}******{}", &key[..2], &key[key.len() - 2..]);
    require(
        r["ok"] == true
            && v["id"].as_str().is_some_and(|i| i.starts_with("s-"))
            && v["label"] == LABEL
            && ck["core"] == json!({"mode": "managed", "origin": base})
            && ck["client"] == AGENT
            && ck["masked"] == masked.as_str()
            && ck["key_id"].as_str().is_some_and(|i| !i.is_empty())
            && !r.to_string().contains(key),
        format!("the capture's answer: {r}"),
    )?;
    Ok((
        v["id"].as_str().unwrap_or_default().into(),
        ck["key_id"].as_str().unwrap_or_default().into(),
    ))
}

/// A loopback listener standing in for another core: it keeps every byte sent to it.
struct Listener {
    base: String,
    got: Arc<Mutex<Vec<u8>>>,
    stop: Arc<AtomicBool>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Listener {
    fn start() -> Result<Listener, String> {
        let l = std::net::TcpListener::bind("127.0.0.1:0").map_err(|e| e.to_string())?;
        let base = format!("http://{}", l.local_addr().map_err(|e| e.to_string())?);
        l.set_nonblocking(true).map_err(|e| e.to_string())?;
        let (got, stop) = (
            Arc::new(Mutex::new(Vec::new())),
            Arc::new(AtomicBool::new(false)),
        );
        let thread = {
            let (got, stop) = (Arc::clone(&got), Arc::clone(&stop));
            std::thread::spawn(move || {
                while !stop.load(Ordering::SeqCst) {
                    match l.accept() {
                        Ok((mut s, _)) => {
                            let _ = s.set_nonblocking(false);
                            let _ = s.set_read_timeout(Some(Duration::from_secs(1)));
                            let mut b = Vec::new();
                            let _ = s.read_to_end(&mut b);
                            got.lock().unwrap().extend(b"\n--connection--\n");
                            got.lock().unwrap().extend(b);
                        }
                        Err(_) => std::thread::sleep(Duration::from_millis(50)),
                    }
                }
            })
        };
        Ok(Listener {
            base,
            got,
            stop,
            thread: Some(thread),
        })
    }

    fn received(&self) -> String {
        String::from_utf8_lossy(&self.got.lock().unwrap()).into_owned()
    }
}

impl Drop for Listener {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

/// The process pid's argv and environment, as /proc has them.
fn argv_env(pid: u64) -> String {
    let read = |f: &str| std::fs::read(format!("/proc/{pid}/{f}")).unwrap_or_default();
    format!(
        "{}\n{}",
        String::from_utf8_lossy(&read("cmdline")),
        String::from_utf8_lossy(&read("environ"))
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let work = fixtures.join("client-keys-work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let at = |name: &str| work.join(name);
    let audit = fixtures.join("client-keys");
    let config = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("core")
        .join("config.json");
    let view = ok(main, "connection_get", json!({}))?;
    let base = view["target"].as_str().unwrap_or_default().to_string();
    require(
        view["mode"] == "managed" && base.starts_with("http://127.0.0.1:"),
        format!("the connection: {view}"),
    )?;
    watch(main)?;
    let mut keys: Vec<String> = Vec::new();
    let mut out = json!({});
    let step =
        |name: &str, r: Result<(), String>| r.map_err(|e| format!("client keys {name}: {e}"));

    step(
        "enable",
        (|| {
            install(main, "curated", AGENT, "1.0.0")?;
            enable(app, main, AGENT).map(|_| ())
        })(),
    )?;

    // ---- cancel
    step(
        "cancel",
        (|| {
            let before = core_keys(&config)?;
            require(!before.is_empty(), "core's config has no client keys")?;
            let r = capture(app, main, &base, false)?;
            require(
                r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("cancelled")),
                format!("the cancelled capture: {r}"),
            )?;
            require(
                core_keys(&config)? == before,
                "a cancelled capture added a key to core",
            )?;
            let list = ok(main, "vault_secrets", json!({}))?;
            require(
                !list.to_string().contains(LABEL),
                format!("a cancelled capture kept a secret: {list}"),
            )
        })(),
    )?;

    // ---- capture
    let mut first = (String::new(), String::new());
    step(
        "capture",
        (|| {
            let before = core_keys(&config)?;
            let r = capture(app, main, &base, true)?;
            let key = new_key(&before, &core_keys(&config)?)?;
            note(&audit, &key)?;
            keys.push(key.clone());
            first = captured(&r, &base, &key)?;
            let listed = ok(
                main,
                "core_call",
                json!({"method": "GET", "path": "/client-keys"}),
            )?;
            require(
                listed["body"]["keys"]
                    .as_array()
                    .is_some_and(|a| a.iter().any(|k| k["id"] == first.1.as_str())),
                format!("core's list lacks the captured key's ID: {listed}"),
            )?;
            let list = ok(main, "vault_secrets", json!({}))?;
            let entry = list["secrets"]
                .as_array()
                .and_then(|a| a.iter().find(|s| s["id"] == first.0.as_str()).cloned())
                .unwrap_or_default();
            require(
                entry["client_key"] == r["value"]["client_key"] && !list.to_string().contains(&key),
                format!("the vault's list: {list}"),
            )?;
            out["captured"] = r["value"].clone();
            Ok(())
        })(),
    )?;

    // ---- serve: the positive control of every refusal below
    step(
        "serve",
        (|| {
            let file = at("served-1");
            let r = serve(app, main, &first.0, &base, &file)?;
            require(
                r["ok"] == true && r["value"]["status"] == 200,
                format!("the agent's serve: {r}"),
            )?;
            require(
                std::fs::read_to_string(&file).unwrap_or_default() == "200",
                "core did not serve the agent with the key",
            )?;
            let pid = r["value"]["pid"].as_u64().ok_or("no agent pid")?;
            let seen = argv_env(pid);
            require(
                !seen.is_empty(),
                "the agent's argv and environment could not be read",
            )?;
            require(
                !keys.iter().any(|k| seen.contains(k.as_str())),
                "the key is in the agent's argv or environment",
            )?;
            let status = ok(main, "program_status", json!({"package": AGENT}))?;
            require(
                !keys.iter().any(|k| status.to_string().contains(k.as_str())),
                "the key is in the agent's status or log",
            )
        })(),
    )?;

    // ---- recipient: another tool
    step(
        "recipient",
        (|| {
            refused_unasked(
                main,
                "program_call",
                json!({"package": OTHER, "op": "deliver",
                    "input": {"key": first.0, "out": at("other-out")}}),
                "client key for pluxy/agent",
            )?;
            require(!at("other-out").exists(), "the other tool got the key")
        })(),
    )?;

    // ---- deleted: a second key, deleted on core
    step(
        "deleted",
        (|| {
            let before = core_keys(&config)?;
            let r = capture(app, main, &base, true)?;
            let key = new_key(&before, &core_keys(&config)?)?;
            note(&audit, &key)?;
            keys.push(key.clone());
            let (secret, key_id) = captured(&r, &base, &key)?;
            let d = ok(
                main,
                "core_call",
                json!({"method": "DELETE", "path": format!("/client-keys/{key_id}")}),
            )?;
            require(d["status"] == 204, format!("deleting the key on core: {d}"))?;
            require(
                !core_keys(&config)?.contains(&key),
                "core still has the deleted key",
            )?;
            refused_unasked(
                main,
                "program_call",
                json!({"package": AGENT, "op": "serve",
                    "input": {"key": secret, "base": base, "out": at("served-deleted")}}),
                "no longer accepts",
            )?;
            require(
                !at("served-deleted").exists(),
                "the agent got the deleted key",
            )
        })(),
    )?;

    // ---- changed core: another core, then the app's own again
    step(
        "changed core",
        (|| {
            let settings = app.state::<App>().conn.lock().unwrap().test_settings_path();
            let saved: Value =
                serde_json::from_slice(&std::fs::read(&settings).map_err(|e| e.to_string())?)
                    .map_err(|e| e.to_string())?;
            let other = Listener::start()?;
            let v = ok(
                main,
                "connection_set",
                json!({"change": {"mode": "local", "url": other.base}}),
            )?;
            require(
                v["target"] == other.base.as_str(),
                format!("the switch: {v}"),
            )?;
            refused_unasked(
                main,
                "program_call",
                json!({"package": AGENT, "op": "serve",
                    "input": {"key": first.0, "base": other.base, "out": at("served-other")}}),
                "another core",
            )?;
            std::thread::sleep(Duration::from_millis(1500));
            let got = other.received();
            require(
                !at("served-other").exists()
                    && !got.contains("/v1/models")
                    && !keys.iter().any(|k| got.contains(k.as_str())),
                "the key was sent to the other core",
            )?;
            drop(other);
            let v = ok(
                main,
                "connection_set",
                json!({"change": {"mode": "managed", "core_source": saved["core_source"]}}),
            )?;
            require(
                v["target"] == base.as_str() && v["signed_in"] == true,
                format!("back to the app's own core: {v}"),
            )?;
            let file = at("served-2");
            let r = serve(app, main, &first.0, &base, &file)?;
            require(
                r["ok"] == true && r["value"]["status"] == 200,
                format!("the agent's serve on the app's own core again: {r}"),
            )
        })(),
    )?;

    // ---- enter: an existing key typed in natively, and one core does not accept
    step(
        "enter",
        (|| {
            let slot = start(
                main,
                "client_key_enter",
                json!({"label": LABEL, "client": AGENT}),
            )?;
            type_key(ENTRY, &keys[0])?;
            let r = settle(main, &slot)?;
            let (secret, _) = captured(&r, &base, &keys[0])?;
            let file = at("served-entered");
            let s = serve(app, main, &secret, &base, &file)?;
            require(
                s["ok"] == true && s["value"]["status"] == 200,
                format!("the agent's serve with the entered key: {s}"),
            )?;
            let wrong = format!("sk-{}", "A".repeat(26));
            let before = ok(main, "vault_secrets", json!({}))?;
            let slot = start(
                main,
                "client_key_enter",
                json!({"label": "wrong key", "client": AGENT}),
            )?;
            type_key(ENTRY, &wrong)?;
            let r = settle(main, &slot)?;
            require(
                r["ok"] == false
                    && r["error"]
                        .as_str()
                        .is_some_and(|e| e.contains("does not accept")),
                format!("a key core does not accept: {r}"),
            )?;
            require(
                ok(main, "vault_secrets", json!({}))? == before,
                "a key core does not accept was kept",
            )
        })(),
    )?;

    // ---- audit
    let canaries: Vec<&str> = keys.iter().map(String::as_str).collect();
    step("audit", clean_webview(main, &canaries))?;

    out["passed"] = json!(true);
    log("client_keys", out);
    Ok(())
}
