//! The launcher: the window's Launcher page and the tray's Start… menu, from host data. A launch is
//! prepared by the optional execution program (`pluxy/execution`, its `prepare` operation turns a
//! typed terminal or direct launch into an executable, argv and cwd), confirmed by the router as
//! the window's direct call of its `launch` operation, and spawned by the host (`route.rs`,
//! `spawn.rs`); the execution program holds secret references only.
//!
//! `launcher.json` (owner-only, beside `connection.json`) keeps what the host shows: each launch
//! the person remembered from the launcher (its client label, the launch's input as confirmed and
//! the remembered approval it is bound to), the recent folders of each client (eight, newest
//! first, as the old app kept them) and whether one-click starts show a launch notice. The input
//! holds no secret: a secret field holds a vault secret's ID. A launch whose approval is gone
//! (revoked in the native settings) is dropped from it.
//!
//! A one-click Start, from the window's launcher (`launcher_start`) or a tray item, is the
//! window's call of `launch` with that input and a native gesture (`confirmation::Gesture`): the
//! remembered approval of exactly that launch starts it with no dialog; anything changed (the
//! executable, the program a terminal runs, the cwd, the execution program's release, the
//! environment) asks again, and remembering it again revokes the approval it replaces. One start
//! per launch per second; each shows a host-owned notice of
//! what started, unless that is turned off. Linux only, as the program host.

use crate::{confirmation, route, spawn, App};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::{BTreeMap, HashMap};
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};
use tauri::menu::{IsMenuItem, MenuItem, Submenu};
use tauri::{AppHandle, Manager, Wry};

/// The execution program the launcher uses.
pub const PACKAGE: &str = "pluxy/execution";
pub const FILE: &str = "launcher.json";
const VERSION: u32 = 1;
/// Recent folders kept per client, as the old app's launch history.
const HISTORY: usize = 8;
/// Remembered launches kept.
const MAX_ENTRIES: usize = 64;
/// Fields of a new launch, as the window sends it.
const FIELDS: &[&str] = &[
    "client",
    "kind",
    "program",
    "override",
    "args",
    "cwd",
    "terminal",
    "lifetime",
    "environment",
    "stdin",
    "key",
];

#[derive(Clone, Serialize, Deserialize)]
struct Entry {
    id: String,
    client: String,
    approval: String,
    /// The `launch` operation's input as confirmed.
    input: Value,
    #[serde(default)]
    used: u64,
}

#[derive(Serialize, Deserialize)]
struct Store {
    version: u32,
    #[serde(default = "yes")]
    notice: bool,
    #[serde(default)]
    history: BTreeMap<String, Vec<String>>,
    #[serde(default)]
    entries: Vec<Entry>,
}

fn yes() -> bool {
    true
}

impl Default for Store {
    fn default() -> Self {
        Store {
            version: VERSION,
            notice: true,
            history: BTreeMap::new(),
            entries: Vec::new(),
        }
    }
}

/// The launcher's state: its file, what it holds, and the last one-click start of each launch.
pub struct Launcher {
    file: PathBuf,
    store: Store,
    last: HashMap<String, Instant>,
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

impl Launcher {
    /// The launcher's file at file; a damaged one is moved aside (`launcher.json.corrupt-<time>`)
    /// and the launcher starts empty.
    pub fn open(file: PathBuf) -> Launcher {
        let store = match fs::read(&file) {
            Ok(b) => match serde_json::from_slice::<Store>(&b) {
                Ok(s) if s.version == VERSION => s,
                r => {
                    let why = r
                        .err()
                        .map_or("unsupported version".into(), |e| e.to_string());
                    let aside = file.with_file_name(format!("{FILE}.corrupt-{}", now_ms()));
                    let moved = fs::rename(&file, &aside);
                    eprintln!(
                        "pluxy-app: the launcher's file was not read ({why}); moved aside: {moved:?}"
                    );
                    Store::default()
                }
            },
            Err(_) => Store::default(),
        };
        Launcher {
            file,
            store,
            last: HashMap::new(),
        }
    }

    fn save(&self) -> Result<(), String> {
        let dir = self
            .file
            .parent()
            .ok_or("the launcher's file has no folder")?;
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(dir)
            .map_err(|e| e.to_string())?;
        let tmp = dir.join(format!(".launcher-{}", crate::managed::random_key()));
        let r = (|| {
            let mut f = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&tmp)
                .map_err(|e| e.to_string())?;
            f.write_all(&serde_json::to_vec_pretty(&self.store).map_err(|e| e.to_string())?)
                .map_err(|e| e.to_string())?;
            f.sync_all().map_err(|e| e.to_string())?;
            fs::rename(&tmp, &self.file).map_err(|e| e.to_string())
        })();
        let _ = fs::remove_file(&tmp);
        r
    }

    /// Drops launches whose approval is gone.
    fn prune(&mut self, c: &confirmation::Confirmations) -> bool {
        let before = self.store.entries.len();
        self.store.entries.retain(|e| c.has_approval(&e.approval));
        before != self.store.entries.len()
    }

    fn remember_folder(&mut self, client: &str, cwd: &str) {
        let h = self.store.history.entry(client.to_string()).or_default();
        h.retain(|c| c != cwd);
        h.insert(0, cwd.to_string());
        h.truncate(HISTORY);
    }

    /// Keeps approval for a launch of client with input: the entry with that input gets it (a
    /// launch remembered again after a change), or a new one. Its ID, and the approval it
    /// replaced, which the caller revokes.
    fn keep(&mut self, client: &str, input: &Value, approval: &str) -> (String, Option<String>) {
        if let Some(e) = self.store.entries.iter().find(|e| e.input == *input) {
            let id = e.id.clone();
            let old = self.replace(&id, approval);
            if let Some(e) = self.store.entries.iter_mut().find(|e| e.id == id) {
                e.client = client.to_string();
            }
            return (id, old);
        }
        if self.store.entries.len() >= MAX_ENTRIES {
            // The least recently used goes.
            if let Some(i) =
                (0..self.store.entries.len()).min_by_key(|i| self.store.entries[*i].used)
            {
                self.store.entries.remove(i);
            }
        }
        let id = format!("l-{}", crate::managed::random_key());
        self.store.entries.push(Entry {
            id: id.clone(),
            client: client.to_string(),
            approval: approval.to_string(),
            input: input.clone(),
            used: now_ms(),
        });
        (id, None)
    }

    /// Binds entry id to approval, a fresh confirmation of it: the approval it replaced, if any
    /// other, which the caller revokes so that nothing still replays the old one.
    fn replace(&mut self, id: &str, approval: &str) -> Option<String> {
        let e = self.store.entries.iter_mut().find(|e| e.id == id)?;
        e.used = now_ms();
        if e.approval == approval {
            return None;
        }
        Some(std::mem::replace(&mut e.approval, approval.to_string()))
    }
}

/// Revokes approval, which a fresh confirmation of the same launch replaced.
fn revoke_superseded(app: &AppHandle, approval: Option<String>) {
    let Some(old) = approval else { return };
    let st = app.state::<App>();
    let mut c = st.confirmations.lock().unwrap();
    if c.has_approval(&old) {
        if let Err(e) = c.revoke(&old) {
            eprintln!("pluxy-app: revoking a replaced launch approval: {e}");
        }
    }
}

/// Text fit for a label: no control or format characters, at most max characters.
fn plain(what: &str, s: &str, max: usize) -> Result<String, String> {
    if s.is_empty() || s.chars().count() > max || s.chars().any(char::is_control) {
        return Err(format!(
            "a launch's {what} is 1 to {max} characters, with no control characters"
        ));
    }
    Ok(confirmation::escape_format_characters(s))
}

/// The launch environment's PATH entries: where the execution program looks for programs and
/// terminals, as the launch itself would find them.
fn search_path() -> Vec<String> {
    spawn::environment(spawn::snapshot(), &spawn::Request::default(), &[])
        .into_iter()
        .find(|(k, _)| k == "PATH")
        .map(|(_, v)| {
            v.to_string_lossy()
                .split(':')
                .filter(|e| e.starts_with('/'))
                .map(String::from)
                .collect()
        })
        .unwrap_or_default()
}

/// Calls op of the execution program from the window, on a thread of its own that ends with the
/// call (a tool it starts is held by the host's supervisor thread). With a gesture, a launch may
/// use its remembered approval.
fn call(
    app: &AppHandle,
    op: &str,
    input: Value,
    gesture: Option<confirmation::Gesture>,
) -> Result<(Value, Option<String>), String> {
    let (app, op) = (app.clone(), op.to_string());
    std::thread::Builder::new()
        .name("pluxy-launcher".into())
        .spawn(move || {
            let call = app
                .state::<App>()
                .programs
                .begin_call(None)
                .map_err(|e| e.to_string())?;
            route::call_main_launch(&app, call, PACKAGE, &op, input, gesture)
                .map_err(|e| e.to_string())
        })
        .map_err(|e| e.to_string())?
        .join()
        .map_err(|_| "the launcher's call failed".to_string())?
}

/// The window's view: whether the execution program can be used now, its release, the remembered
/// launches, the recent folders and the notice setting.
pub fn state(app: &AppHandle) -> Value {
    let st = app.state::<App>();
    let grant = st.device.lock().unwrap().main_grant(
        PACKAGE,
        "launch",
        &crate::trust::TrustStore::current(),
    );
    let source = grant.as_ref().ok().map(|g| {
        let p = g.target.package();
        json!({"package": p.id, "version": g.target.version(), "sha256": p.digest,
               "source": p.source, "signer": p.signer})
    });
    let c = st.confirmations.lock().unwrap();
    let mut l = st.launcher.lock().unwrap();
    if l.prune(&c) {
        let _ = l.save();
    }
    let entries: Vec<Value> = l
        .store
        .entries
        .iter()
        .map(|e| {
            let i = &e.input;
            json!({
                "id": e.id, "client": e.client, "executable": i["executable"], "argv": i["argv"],
                "cwd": i["cwd"], "program": i.get("program"),
                "lifetime": i.get("lifetime").cloned().unwrap_or(json!("detached")),
                "used": e.used,
                "source": source.clone().unwrap_or(json!({"package": PACKAGE})),
            })
        })
        .collect();
    json!({
        "available": grant.is_ok(),
        "reason": grant.err().map(|r| r.message),
        "package": PACKAGE,
        "source": source,
        "notice": l.store.notice,
        "entries": entries,
        "history": l.store.history,
    })
}

/// The terminals the execution program finds on the launch environment's PATH: Automatic first.
pub fn terminals(app: &AppHandle) -> Result<Value, String> {
    call(
        app,
        "terminals",
        json!({"search_path": search_path()}),
        None,
    )
    .map(|(v, _)| v)
}

/// A new launch from the window's launcher (`FIELDS`): prepared by the execution program, then
/// its `launch` call, which always asks a native confirmation (offering Remember). A launch the
/// person remembered joins the launcher's list (and the tray's Start… menu).
pub fn launch(app: &AppHandle, req: &Value) -> Result<Value, String> {
    let m = req.as_object().ok_or("a launch is an object")?;
    if let Some(k) = m.keys().find(|k| !FIELDS.contains(&k.as_str())) {
        return Err(format!("a launch has no field {k}"));
    }
    let client = plain("client", req["client"].as_str().unwrap_or_default(), 128)?;
    let cwd = req["cwd"]
        .as_str()
        .ok_or("a launch names its cwd")?
        .to_string();
    let mut prepare = json!({"search_path": search_path()});
    for k in ["kind", "program", "override", "args", "cwd", "terminal"] {
        if let Some(v) = req.get(k) {
            prepare[k] = v.clone();
        }
    }
    let (prepared, _) = call(app, "prepare", prepare, None)?;
    let mut input = prepared
        .as_object()
        .cloned()
        .ok_or("the execution program prepared no launch")?;
    // What the execution program does not decide: they go to the confirmation as asked.
    for k in ["lifetime", "environment", "stdin", "key"] {
        if let Some(v) = req.get(k) {
            input.insert(k.into(), v.clone());
        }
    }
    let input = Value::Object(input);
    let (output, approval) = call(app, "launch", input.clone(), None)?;
    let st = app.state::<App>();
    let (entry, old) = {
        let mut l = st.launcher.lock().unwrap();
        l.remember_folder(&client, &cwd);
        let kept = approval.map(|a| l.keep(&client, &input, &a));
        l.save()?;
        kept.map_or((None, None), |(id, old)| (Some(id), old))
    };
    revoke_superseded(app, old);
    refresh_tray(app);
    Ok(json!({"entry": entry, "output": output}))
}

/// One-click start of remembered launch id with gesture (the window's launcher or a tray item):
/// with no dialog while it is exactly the launch remembered.
pub fn start(app: &AppHandle, id: &str, gesture: confirmation::Gesture) -> Result<Value, String> {
    let st = app.state::<App>();
    let entry = {
        let mut l = st.launcher.lock().unwrap();
        l.last.retain(|_, t| t.elapsed() < Duration::from_secs(1));
        if l.last.contains_key(id) {
            return Err("one start of a launch per second".into());
        }
        let e = l
            .store
            .entries
            .iter()
            .find(|e| e.id == id)
            .cloned()
            .ok_or("no such remembered launch")?;
        l.last.insert(id.to_string(), Instant::now());
        e
    };
    let r = call(app, "launch", entry.input.clone(), Some(gesture));
    let i = &entry.input;
    let what = format!(
        "{}: {} {} in {}",
        entry.client,
        i["executable"].as_str().unwrap_or_default(),
        i["argv"],
        i["cwd"].as_str().unwrap_or_default()
    );
    let (notice, old) = {
        let mut l = st.launcher.lock().unwrap();
        let old = match &r {
            Ok((_, Some(a))) => l.replace(id, a),
            _ => None,
        };
        if r.is_ok() {
            if let Some(cwd) = i["cwd"].as_str() {
                l.remember_folder(&entry.client, cwd);
            }
            let _ = l.save();
        }
        (l.store.notice, old)
    };
    revoke_superseded(app, old);
    if notice {
        let text = match &r {
            Ok(_) => format!("Started {what}"),
            Err(e) => format!("Could not start {what}: {e}"),
        };
        confirmation::ui_notice(app, confirmation::escape_format_characters(&text));
    }
    refresh_tray(app);
    r.map(|(output, _)| json!({ "output": output }))
}

/// Forgets remembered launch id: its approval is revoked and it leaves the launcher.
pub fn forget(app: &AppHandle, id: &str) -> Result<Value, String> {
    let st = app.state::<App>();
    {
        let mut c = st.confirmations.lock().unwrap();
        let mut l = st.launcher.lock().unwrap();
        let e = l
            .store
            .entries
            .iter()
            .find(|e| e.id == id)
            .cloned()
            .ok_or("no such remembered launch")?;
        if c.has_approval(&e.approval) {
            c.revoke(&e.approval)?;
        }
        l.store.entries.retain(|x| x.id != id);
        l.save()?;
    }
    refresh_tray(app);
    Ok(state(app))
}

/// Whether one-click starts show a launch notice.
pub fn settings(app: &AppHandle, notice: bool) -> Result<Value, String> {
    {
        let st = app.state::<App>();
        let mut l = st.launcher.lock().unwrap();
        l.store.notice = notice;
        l.save()?;
    }
    Ok(state(app))
}

/// The tray's Start… submenu, once the tray is built.
static START: Mutex<Option<Submenu<Wry>>> = Mutex::new(None);

/// The tray's Start… submenu, filled with the remembered launches.
pub fn tray_menu(app: &AppHandle) -> tauri::Result<Submenu<Wry>> {
    let menu = Submenu::with_id(app, "launch-start", "Start…", true)?;
    *START.lock().unwrap() = Some(menu.clone());
    refresh_tray(app);
    Ok(menu)
}

/// The label of a launch in the tray's Start… menu.
fn tray_label(e: &Entry) -> String {
    format!(
        "{} in {}",
        e.client,
        e.input["cwd"].as_str().unwrap_or_default()
    )
}

/// Fills the tray's Start… menu again: an item per remembered launch, or a disabled "No
/// remembered launches". One closure on the main thread reads the launches and rebuilds the menu,
/// so rebuilds asked from several threads never interleave and the last one shows the latest list.
pub fn refresh_tray(app: &AppHandle) {
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        let app = &handle;
        let Some(menu) = START.lock().unwrap().clone() else {
            return;
        };
        let st = app.state::<App>();
        let items: Vec<(String, String)> = {
            let c = st.confirmations.lock().unwrap();
            let mut l = st.launcher.lock().unwrap();
            if l.prune(&c) {
                let _ = l.save();
            }
            l.store
                .entries
                .iter()
                .map(|e| (format!("launch:{}", e.id), tray_label(e)))
                .collect()
        };
        while let Ok(Some(_)) = menu.remove_at(0) {}
        let made: Vec<MenuItem<Wry>> = if items.is_empty() {
            MenuItem::with_id(
                app,
                "launch-none",
                "No remembered launches",
                false,
                None::<&str>,
            )
            .into_iter()
            .collect()
        } else {
            items
                .iter()
                .filter_map(|(id, label)| {
                    MenuItem::with_id(app, id, label, true, None::<&str>).ok()
                })
                .collect()
        };
        for i in &made {
            let _ = menu.append(i as &dyn IsMenuItem<Wry>);
        }
    });
}

/// A click on one of the tray's Start… items (`launch:<id>`): a native gesture, made here.
pub fn tray_start(app: &AppHandle, item: &str) {
    let Some(id) = item.strip_prefix("launch:") else {
        return;
    };
    let gesture = confirmation::Gesture::tray();
    let (app, id) = (app.clone(), id.to_string());
    std::thread::spawn(move || {
        if let Err(e) = start(&app, &id, gesture) {
            eprintln!("pluxy-app: the tray's start of a remembered launch: {e}");
        }
    });
}

/// The file in the app's config folder.
pub fn file(config: &Path) -> PathBuf {
    config.join(FILE)
}

/// Asks whether to stop package's running managed runtimes before it is disabled or removed
/// (verb); refused when the person cancels. Nothing is asked when none runs.
pub fn confirm_runtimes(app: &AppHandle, package: &str, verb: &str) -> Result<(), String> {
    let running: Vec<String> = app
        .state::<App>()
        .runtimes
        .list(package)
        .into_iter()
        .filter(|r| r["state"] == "running" || r["state"] == "stopping")
        .map(|r| {
            format!(
                "{} {} (process {})",
                r["executable"].as_str().unwrap_or_default(),
                r["argv"],
                r["pid"]
            )
        })
        .collect();
    if running.is_empty() {
        return Ok(());
    }
    let what = format!(
        "{verb} {package}?\n\nIts {} managed runtime{} will be stopped:\n{}",
        running.len(),
        if running.len() == 1 { "" } else { "s" },
        running.join("\n")
    );
    match confirmation::confirm_app(app, what, now_ms()) {
        Ok(true) => Ok(()),
        Ok(false) => Err(format!(
            "cancelled; {package} and its runtimes stay as they are"
        )),
        Err(e) => Err(format!("{e}; {package} and its runtimes stay as they are")),
    }
}

#[cfg(test)]
#[path = "launcher_tests.rs"]
mod tests;
