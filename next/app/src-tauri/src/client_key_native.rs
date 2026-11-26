//! The native test's check of native one-time client-key capture (native-test builds with
//! `artifact-test-trust` only), in the vault check's session, after it (`vault_native.rs`), with
//! its signed fixture tools (`native/vault.ts`): pluxy/agent (a disposable agent: its `serve`
//! asks core with the client key it is given) and pluxy/consumer (another tool). It drives the
//! real app through the window's own commands, the real native confirmations and key entry, and
//! the app's own core on Linux. On Windows and macOS, where the app's own core does not run yet, it
//! uses the session's test core in local mode instead (`native/platform-tools.ts` names its config
//! file in `core.json`). The keys core makes are read from that core's config file, the
//! independent record; the app never shows them. Each refusal has a positive control on the same
//! route:
//!
//! - cancel: a capture the person cancels adds no key to core and none to the vault;
//! - capture: a confirmed capture adds one key to core's config, which core lists under the
//!   capture's label, and keeps it in the vault bound to the core, core's key ID and the agent;
//!   the window gets the secret's ID and binding only;
//! - serve: the agent, given the key for its `serve` after a native confirmation, is served by
//!   core with it (200); the key is in neither the agent's argv nor its environment nor its log
//!   (argv and environment as the system has them, `proc_native.rs`; on Windows the command line
//!   as Windows has it, and the environment as the agent reports its own, since Windows gives no
//!   supported way to read another process's);
//! - recipient: the key given to another tool is refused before any confirmation;
//! - deleted: a second captured key, deleted on core, is refused before any confirmation;
//! - changed core: with the app connected to another core (a listener of this check's), the key
//!   is refused before any confirmation and nothing reaches that core; connected to the first core
//!   again (signed in again through native key entry, in local mode), it serves the agent again;
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
/// The mode of the core the check uses: the app's own on Linux, a test core elsewhere.
const MODE: &str = if cfg!(target_os = "linux") {
    "managed"
} else {
    "local"
};

/// The client keys in the core config at path, its `client_keys` (the app writes that config as
/// JSON, and core keeps a JSON config when it changes it).
pub(super) fn core_keys(path: &Path) -> Result<BTreeSet<String>, String> {
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
pub(super) fn note(file: &Path, key: &str) -> Result<(), String> {
    use std::io::Write;
    std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(file)
        .and_then(|mut f| writeln!(f, "{key}"))
        .map_err(|e| e.to_string())
}

/// Starts cmd with args and requires it refused saying says, before any native confirmation.
pub(super) fn refused_unasked(
    main: &Webview,
    cmd: &str,
    args: Value,
    says: &str,
) -> Result<(), String> {
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
pub(super) fn new_key(
    before: &BTreeSet<String>,
    after: &BTreeSet<String>,
) -> Result<String, String> {
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
            && ck["core"] == json!({"mode": MODE, "origin": base})
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
pub(super) struct Listener {
    pub(super) base: String,
    got: Arc<Mutex<Vec<u8>>>,
    stop: Arc<AtomicBool>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Listener {
    pub(super) fn start() -> Result<Listener, String> {
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

    pub(super) fn received(&self) -> String {
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

/// The agent's program in its package, as its manifest names it (`native/admission.ts` `TOOL`).
/// The host starts it by its package folder joined with that, so on Windows its argv says
/// `<store>\<digest>\bin/tool.exe`, a mix of separators; `whole` compares with each `\` read as `/`.
const PROGRAM: &str = if cfg!(windows) {
    "bin/tool.exe"
} else {
    "bin/tool"
};

/// The agent's argv and environment, as the system has them for process pid; empty when they
/// cannot be read.
#[cfg(unix)]
fn argv_env(pid: u64, _out: &Path) -> (String, String) {
    super::procs::argv_env(pid).unwrap_or_default()
}

/// The agent's command line as Windows has it for process pid (`Win32_Process.CommandLine`, read
/// through PowerShell), and its environment as it reports it itself, beside out (the fixture
/// tool writes it there): Windows gives no supported way to read another process's environment.
/// Each empty when it cannot be read.
#[cfg(windows)]
fn argv_env(pid: u64, out: &Path) -> (String, String) {
    let shell = std::env::var_os("SystemRoot")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from(r"C:\Windows"))
        .join(r"System32\WindowsPowerShell\v1.0\powershell.exe");
    let mut c = std::process::Command::new(shell);
    c.args([
        "-NoProfile",
        "-NonInteractive",
        "-Command",
        &format!("(Get-CimInstance Win32_Process -Filter 'ProcessId={pid}').CommandLine"),
    ])
    .stdin(std::process::Stdio::null())
    .stdout(std::process::Stdio::piped())
    .stderr(std::process::Stdio::null());
    let argv = crate::owned::windows::std_spawn(&mut c)
        .and_then(|c| c.wait_with_output())
        .ok()
        .filter(|o| o.status.success())
        .map(|o| String::from_utf8_lossy(&o.stdout).trim().to_string())
        .unwrap_or_default();
    let mut seen = out.as_os_str().to_owned();
    seen.push(".process");
    (argv, std::fs::read_to_string(seen).unwrap_or_default())
}

/// Whether argv and env, the agent's as read, are whole: its argv names its program, and its
/// environment holds the extension ID the host gives every tool (it clears a tool's environment,
/// then sets that: program.rs). A read that failed, or a reply without the environment, is not.
fn whole(argv: &str, env: &str) -> bool {
    argv.replace('\\', "/").contains(PROGRAM) && env.contains("PLUXY_EXTENSION_ID=pluxy/agent")
}

/// The agent's argv and environment as read (`argv_env`), only when whole (`whole`): a read that
/// failed or came back partial fails, rather than leaving the key checks nothing to search.
fn agent_seen(pid: u64, out: &Path) -> Result<(String, String), String> {
    let (argv, env) = argv_env(pid, out);
    if whole(&argv, &env) {
        Ok((argv, env))
    } else {
        Err(format!(
            "the agent's argv and environment could not be read: {} and {} bytes",
            argv.len(),
            env.len()
        ))
    }
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let work = fixtures.join("client-keys-work");
    std::fs::create_dir_all(&work).map_err(|e| e.to_string())?;
    let at = |name: &str| work.join(name);
    let audit = fixtures.join("client-keys");
    let config = if MODE == "managed" {
        app.path()
            .app_data_dir()
            .map_err(|e| e.to_string())?
            .join("core")
            .join("config.json")
    } else {
        let named: Value = serde_json::from_slice(
            &std::fs::read(fixtures.join("core.json")).map_err(|e| format!("core.json: {e}"))?,
        )
        .map_err(|e| format!("core.json: {e}"))?;
        PathBuf::from(
            named["config"]
                .as_str()
                .ok_or("core.json names no config")?,
        )
    };
    let view = ok(main, "connection_get", json!({}))?;
    let base = view["target"].as_str().unwrap_or_default().to_string();
    require(
        view["mode"] == MODE && base.starts_with("http://127.0.0.1:"),
        format!("the connection: {view}"),
    )?;
    watch(main)?;
    let mut keys: Vec<String> = Vec::new();
    let mut mgmt_key: Option<String> = None;
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
            // Core names the key as the vault does: the capture sent the label.
            require(
                listed["body"]["keys"].as_array().is_some_and(|a| {
                    a.iter()
                        .any(|k| k["id"] == first.1.as_str() && k["label"] == LABEL)
                }),
                format!("core's list does not name the captured key {LABEL:?}: {listed}"),
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
            let (argv, env) = agent_seen(pid, &file)?;
            require(
                !keys
                    .iter()
                    .any(|k| argv.contains(k.as_str()) || env.contains(k.as_str())),
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

    // ---- changed core: another core, then the first again (the app's own on Linux, the session's
    // test core elsewhere)
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
            let v = if MODE == "managed" {
                ok(
                    main,
                    "connection_set",
                    json!({"change": {"mode": "managed", "core_source": saved["core_source"]}}),
                )?
            } else {
                // Another target forgets the key: the person signs in again with the kept one.
                ok(
                    main,
                    "connection_set",
                    json!({"change": {"mode": "local", "url": base}}),
                )?;
                let key = saved["key"].as_str().ok_or("no kept management key")?;
                mgmt_key = Some(key.to_string());
                let r = super::key_entry_test::enter(app, main, key, true, false)?;
                require(r["status"] == "signed_in", format!("signing in again: {r}"))?;
                ok(main, "connection_get", json!({}))?
            };
            require(
                v["target"] == base.as_str() && v["signed_in"] == true,
                format!("back to the first core: {v}"),
            )?;
            let file = at("served-2");
            let r = serve(app, main, &first.0, &base, &file)?;
            require(
                r["ok"] == true && r["value"]["status"] == 200,
                format!("the agent's serve on the first core again: {r}"),
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
    let mut canaries: Vec<&str> = keys.iter().map(String::as_str).collect();
    if let Some(k) = mgmt_key.as_deref() {
        canaries.push(k);
    }
    step("audit", clean_webview(main, &canaries))?;

    out["passed"] = json!(true);
    log("client_keys", out);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::{whole, PROGRAM};

    #[test]
    fn a_read_without_the_agents_environment_is_refused() {
        let argv = format!("/s/{PROGRAM}\0serve\0");
        // Argv only, as macOS gives a reply without the environment.
        assert!(!whole(&argv, ""));
        assert!(!whole("", "PLUXY_EXTENSION_ID=pluxy/agent\0"));
        assert!(whole(&argv, "HOME=/h\0PLUXY_EXTENSION_ID=pluxy/agent\0"));
    }

    #[test]
    fn a_windows_command_line_names_the_program_with_either_separator() {
        let env = "PLUXY_EXTENSION_ID=pluxy/agent\0";
        // As the host starts it on Windows (its folder joined with the manifest's `bin/...`), and
        // with every separator `\`.
        let mixed = format!(r#""C:\store\abc\{PROGRAM}" serve"#);
        let back = mixed.replace('/', r"\");
        assert!(whole(&mixed, env), "{mixed}");
        assert!(whole(&back, env), "{back}");
        assert!(!whole(r#""C:\store\abc\bin\other.exe" serve"#, env));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn a_process_linux_cannot_read_is_refused() {
        // No such process.
        let (argv, env) = super::super::procs::argv_env(u64::from(u32::MAX)).unwrap_or_default();
        assert!(!whole(&argv, &env));
        // This process: both read.
        let (argv, env) = super::super::procs::argv_env(u64::from(std::process::id()))
            .expect("this process's argv and environment");
        assert!(!argv.is_empty() && !env.is_empty());
    }

    /// The serve step's read (`agent_seen`) of real processes: one it cannot read, one read whole
    /// but not the agent, one with the agent's environment but another program, and the agent.
    #[cfg(target_os = "linux")]
    #[test]
    fn the_serve_step_takes_only_the_agents_whole_read() {
        use super::agent_seen;
        use std::process::{Command, Stdio};
        let dir = std::env::temp_dir().join(format!("ck-seen-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("bin")).unwrap();
        let out = dir.join("out");
        assert!(agent_seen(u64::from(u32::MAX), &out).is_err());
        // This process: argv and environment both read, neither the agent's.
        assert!(agent_seen(u64::from(std::process::id()), &out).is_err());
        let tool = dir.join(PROGRAM);
        std::os::unix::fs::symlink("/bin/sh", &tool).unwrap();
        // A shell that waits on its input; as `bin/tool` it keeps the argv it was started with.
        let start = |program: &std::path::Path, id: &str| {
            Command::new(program)
                .args(["-c", "read x; :"])
                .env_clear()
                .env("PLUXY_EXTENSION_ID", id)
                .stdin(Stdio::piped())
                .spawn()
                .unwrap()
        };
        let mut other = start(&tool, "pluxy/other");
        let mut shell = start(std::path::Path::new("/bin/sh"), "pluxy/agent");
        let mut agent = start(&tool, "pluxy/agent");
        // Each has started once its argv is its own, not the test's that forked it.
        let read = |c: &std::process::Child| {
            for _ in 0..100 {
                if let Some((argv, _)) = super::super::procs::argv_env(u64::from(c.id())) {
                    if argv.contains("read x") {
                        return;
                    }
                }
                std::thread::sleep(std::time::Duration::from_millis(20));
            }
            panic!("process {} did not start", c.id());
        };
        for c in [&other, &shell, &agent] {
            read(c);
        }
        let other_seen = agent_seen(u64::from(other.id()), &out);
        let shell_seen = agent_seen(u64::from(shell.id()), &out);
        let agent_read = agent_seen(u64::from(agent.id()), &out);
        for c in [&mut other, &mut shell, &mut agent] {
            drop(c.stdin.take());
            c.wait().unwrap();
        }
        std::fs::remove_dir_all(&dir).unwrap();
        assert!(other_seen.is_err(), "{other_seen:?}");
        assert!(shell_seen.is_err(), "{shell_seen:?}");
        let (argv, env) = agent_read.expect("the agent's read");
        assert!(argv.contains(PROGRAM) && env.contains("PLUXY_EXTENSION_ID=pluxy/agent"));
    }
}
