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
//! what started, unless that is turned off. Where the program host runs (`native_stack`).

use crate::environments::{Environment, Instance, Record};
use crate::{confirmation, route, spawn, App};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::{BTreeMap, HashMap};
use std::fs;
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
    /// A launch in WSL: the line its confirmation showed (part of what its approval names), and
    /// the folder in the distribution it starts in (its input's cwd is a Windows folder).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    shown: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    folder: Option<String>,
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

    /// Writes the launcher's file, readable and writable only by this user, in a folder only this
    /// user may use (`managed::write_private`).
    fn save(&self) -> Result<(), String> {
        let dir = self
            .file
            .parent()
            .ok_or("the launcher's file has no folder")?;
        #[cfg(unix)]
        let made = {
            use std::os::unix::fs::DirBuilderExt;
            fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)
        };
        #[cfg(windows)]
        let made = crate::private_windows::create_private_dir_all(dir);
        made.map_err(|e| e.to_string())?;
        let bytes = serde_json::to_vec_pretty(&self.store).map_err(|e| e.to_string())?;
        crate::managed::write_private(&self.file, &bytes).map_err(|e| e.to_string())
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
    fn keep(
        &mut self,
        client: &str,
        input: &Value,
        wsl: Option<(&str, &str)>,
        approval: &str,
    ) -> (String, Option<String>) {
        let (shown, folder) = (wsl.map(|w| w.0.to_string()), wsl.map(|w| w.1.to_string()));
        if let Some(e) = self
            .store
            .entries
            .iter()
            .find(|e| e.input == *input && e.shown == shown)
        {
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
            shown,
            folder,
        });
        (id, None)
    }

    /// Forgets environment's remembered launches and recent folders (its record was removed):
    /// the approvals of the launches forgotten, which the caller revokes.
    /// Its launches go by `client_in`'s name, which only a launch there has.
    fn forget_environment(&mut self, environment: &Environment) -> Vec<String> {
        if environment.is_local() {
            return Vec::new();
        }
        let there = client_in("", environment);
        let mut gone = Vec::new();
        self.store.entries.retain(|e| {
            let keep = e.shown.is_none() || !e.client.ends_with(&there);
            if !keep {
                gone.push(e.approval.clone());
            }
            keep
        });
        self.store.history.retain(|c, _| !c.ends_with(&there));
        gone
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

/// Forgets environment's remembered launches and recent folders, and revokes their approvals:
/// its record was removed, so none of them starts again (not even in a distribution or user made
/// again under the same names, which the person adds again). The approvals are revoked even when
/// saving the launcher's record fails, whose error is returned after.
pub fn forget_environment(app: &AppHandle, environment: &Environment) -> Result<(), String> {
    let (gone, saved) = {
        let st = app.state::<App>();
        let mut l = st.launcher.lock().unwrap();
        let gone = l.forget_environment(environment);
        (gone, l.save())
    };
    // shortcut: no test shows these revokes run when the save failed: revoking needs the running
    // app's `AppHandle`, which no test can make (the tests check `Launcher::forget_environment`'s
    // list alone); test it once the app has a test harness for its Tauri state, or once this code
    // takes that state instead of the handle.
    for approval in gone {
        revoke_superseded(app, Some(approval));
    }
    refresh_tray(app);
    saved
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
        .map(|(_, v)| path_entries(&v))
        .unwrap_or_default()
}

/// The absolute folders of a PATH value, split as this system splits it (`:` on Linux and macOS;
/// `;` on Windows, where an entry may be quoted); empty and relative entries are left out.
fn path_entries(path: &std::ffi::OsStr) -> Vec<String> {
    std::env::split_paths(path)
        .filter(|p| p.is_absolute())
        .map(|p| p.to_string_lossy().into_owned())
        .collect()
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
    call_of(app, PACKAGE, op, input, gesture, None)
}

/// `call`, of package (the execution program, or `pluxy/wsl` to prepare a launch in WSL), with
/// shown as the line its confirmation shows for where the launch runs.
fn call_of(
    app: &AppHandle,
    package: &str,
    op: &str,
    input: Value,
    gesture: Option<confirmation::Gesture>,
    shown: Option<String>,
) -> Result<(Value, Option<String>), String> {
    let (app, op, instance) = (app.clone(), op.to_string(), Instance::local(package));
    std::thread::Builder::new()
        .name("pluxy-launcher".into())
        .spawn(move || {
            let call = app
                .state::<App>()
                .programs
                .begin_call(None)
                .map_err(|e| e.to_string())?;
            route::call_main_in(&app, call, &instance, &op, input, gesture, shown)
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
pub fn launch(app: &AppHandle, req: &Value, environment: &Environment) -> Result<Value, String> {
    let m = req.as_object().ok_or("a launch is an object")?;
    if let Some(k) = m.keys().find(|k| !FIELDS.contains(&k.as_str())) {
        return Err(format!("a launch has no field {k}"));
    }
    let client = plain("client", req["client"].as_str().unwrap_or_default(), 128)?;
    if !environment.is_local() {
        return launch_in_wsl(app, req, &client, environment);
    }
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
        let kept = approval.map(|a| l.keep(&client, &input, None, &a));
        l.save()?;
        kept.map_or((None, None), |(id, old)| (Some(id), old))
    };
    revoke_superseded(app, old);
    refresh_tray(app);
    Ok(json!({"entry": entry, "output": output}))
}

/// The launcher's name of client's launches in WSL environment: its recent folders and its
/// remembered launches go by it. `Claude Code (WSL Ubuntu/bob)`.
pub fn client_in(client: &str, environment: &Environment) -> String {
    match environment {
        Environment::Local => client.to_string(),
        Environment::Wsl { distro, user } => format!("{client} (WSL {distro}/{user})"),
    }
}

/// What the launcher asks `pluxy/wsl` to prepare for a launch in record's environment: req's kind,
/// program or override, arguments and folder (in the distribution). A launch there opens in the
/// Windows console, so it names no terminal.
fn wsl_prepare_input(req: &Value, record: &Record) -> Result<Value, String> {
    if req.get("terminal").is_some() {
        return Err("a launch in WSL opens in the Windows console: it names no terminal".into());
    }
    let mut input = json!({"distro": record.distro, "user": record.user});
    for k in ["kind", "program", "override", "args", "cwd"] {
        if let Some(v) = req.get(k) {
            input[k] = v.clone();
        }
    }
    Ok(input)
}

/// The `launch` input of a launch `pluxy/wsl` prepared ({launch, kind}), with req's lifetime,
/// environment, stdin and key, and its kind applied first (execution sees `wsl.exe` as a direct
/// launch): a terminal launch takes no standard input (its input is the keyboard) and is detached
/// only; a detached launch with no standard input gets a console window of its own. The Linux
/// program is not passed as `program`: the host cannot open a file inside the distribution to
/// check it, so it is shown in the argv and named in the confirmation's line instead. Gives the
/// input and the Linux program.
///
/// The reply is refused unless it is exactly the launch asked for, since the confirmation's line
/// comes from record, not from the reply: a launch of only `executable`, `argv`, `cwd` and
/// `program` (nothing else of the launch comes from the reply; req's own fields are added after),
/// Windows' own `wsl.exe` (in system_root's System32) started in home (the folder the app gives
/// its tools), argv `-d <distro> -u <user> --cd <folder> --exec <program>` then req's arguments,
/// the folder req names (or, with none, an absolute one: the user's home), the program req's
/// override names (or, with none, an absolute path: the one found on the PATH), and req's kind.
fn wsl_launch_input(
    prepared: &Value,
    req: &Value,
    record: &Record,
    system_root: Option<&Path>,
    home: Option<&Path>,
) -> Result<(Value, String), String> {
    let kind = prepared["kind"].as_str().unwrap_or_default();
    if kind != "terminal" && kind != "direct" {
        return Err("pluxy/wsl prepared a launch of no known kind".into());
    }
    if req.get("kind").and_then(Value::as_str) != Some(kind) {
        return Err(format!(
            "pluxy/wsl prepared a launch of another kind ({kind}) than asked for"
        ));
    }
    let mut input = prepared["launch"]
        .as_object()
        .cloned()
        .ok_or("pluxy/wsl prepared no launch")?;
    if let Some(k) = input
        .keys()
        .find(|k| !["executable", "argv", "cwd", "program"].contains(&k.as_str()))
    {
        return Err(format!(
            "pluxy/wsl prepared a launch with {k}, which it never prepares"
        ));
    }
    let cwd = input.get("cwd").and_then(Value::as_str);
    if cwd.is_none() || cwd != home.and_then(Path::to_str) {
        return Err(format!(
            "pluxy/wsl prepared a launch that starts wsl.exe in {}, not the app's home folder",
            cwd.unwrap_or("no folder")
        ));
    }
    let program = input
        .remove("program")
        .and_then(|p| p.as_str().map(String::from))
        .ok_or("pluxy/wsl prepared a launch with no program")?;
    let executable = input
        .get("executable")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if !spawn::is_wsl_exe(Path::new(executable), system_root) {
        return Err(format!(
            "pluxy/wsl prepared a launch of {executable}, not Windows' own wsl.exe"
        ));
    }
    let argv: Option<Vec<&str>> = input
        .get("argv")
        .and_then(Value::as_array)
        .and_then(|a| a.iter().map(Value::as_str).collect());
    let args: Option<Vec<&str>> = match req.get("args") {
        None => Some(Vec::new()),
        Some(a) => a
            .as_array()
            .and_then(|a| a.iter().map(Value::as_str).collect()),
    };
    // As pluxy/wsl reads them: an empty folder or override is none.
    let asked = |k: &str| req.get(k).and_then(Value::as_str).filter(|v| !v.is_empty());
    let fits = match (argv.as_deref(), args) {
        (Some(["-d", d, "-u", u, "--cd", folder, "--exec", p, rest @ ..]), Some(args)) => {
            *d == record.distro
                && *u == record.user
                && folder.starts_with('/')
                && asked("cwd").is_none_or(|c| c == *folder)
                && *p == program
                && p.starts_with('/')
                && asked("override").is_none_or(|o| o == *p)
                && rest == args.as_slice()
        }
        _ => false,
    };
    if !fits {
        return Err(format!(
            "pluxy/wsl prepared a launch that is not the launch asked for: {} {}",
            executable,
            argv.unwrap_or_default().join(" ")
        ));
    }
    if kind == "terminal" {
        if req.get("stdin").is_some() {
            return Err(
                "a terminal launch takes no standard input: its input is the keyboard".into(),
            );
        }
        if req.get("lifetime").and_then(Value::as_str) == Some("managed") {
            return Err(
                "a terminal launch is detached only: closing its console window ends it".into(),
            );
        }
    }
    for k in ["lifetime", "environment", "stdin", "key"] {
        if let Some(v) = req.get(k) {
            input.insert(k.into(), v.clone());
        }
    }
    Ok((Value::Object(input), program))
}

/// The line the confirmation of a launch in record's environment shows above its review.
fn wsl_launch_line(record: &Record, program: &str) -> String {
    format!(
        "Starts in WSL: distribution \"{}\", user \"{}\" ({}). The program inside WSL ({program}) is found \
         when it starts; pluxy does not check it.",
        record.distro, record.user, record.home
    )
}

/// What a launch in WSL gave: execution's output and remembered approval, and what the launcher
/// remembers with it: the launch it ran, the line its confirmation showed and the folder in the
/// distribution it started in.
struct InWsl {
    output: Value,
    approval: Option<String>,
    input: Value,
    line: String,
    folder: String,
}

/// A launch of req in record's environment: `prepare` (`pluxy/wsl`'s, given its input), the
/// prepared launch checked against req and record (`wsl_launch_input`), then `launch`
/// (execution's, given that launch and the line its confirmation shows above the review).
fn run_in_wsl(
    req: &Value,
    record: &Record,
    system_root: Option<&Path>,
    home: Option<&Path>,
    prepare: impl FnOnce(Value) -> Result<Value, String>,
    launch: impl FnOnce(Value, String) -> Result<(Value, Option<String>), String>,
) -> Result<InWsl, String> {
    let prepared = prepare(wsl_prepare_input(req, record)?)?;
    let (input, program) = wsl_launch_input(&prepared, req, record, system_root, home)?;
    let line = wsl_launch_line(record, &program);
    // The folder in the distribution it starts in: `--cd`'s argument.
    let argv: Vec<&str> = input["argv"]
        .as_array()
        .map(|a| a.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    let folder = argv
        .iter()
        .position(|a| *a == "--cd")
        .and_then(|i| argv.get(i + 1))
        .map(|f| f.to_string())
        .ok_or("pluxy/wsl prepared a launch with no folder")?;
    let (output, approval) = launch(input.clone(), line.clone())?;
    Ok(InWsl {
        output,
        approval,
        input,
        line,
        folder,
    })
}

/// `launch`, in a WSL environment the host has a record of: prepared by `pluxy/wsl` (its
/// `prepare`, in the distribution as the user), then execution's `launch` of that `wsl.exe`
/// command, confirmed with the environment's line.
fn launch_in_wsl(
    app: &AppHandle,
    req: &Value,
    client: &str,
    environment: &Environment,
) -> Result<Value, String> {
    let record = app
        .state::<App>()
        .environments
        .lock()
        .unwrap()
        .get(environment)
        .cloned()
        .ok_or_else(|| {
            format!(
                "{} is not a WSL environment this app knows: add it again",
                environment.label().unwrap_or_default()
            )
        })?;
    let system_root = std::env::var_os("SystemRoot").map(PathBuf::from);
    // The folder pluxy/wsl starts wsl.exe in: the home the app gives its tools (`ToolEnv`).
    let home = std::env::home_dir();
    let InWsl {
        output,
        approval,
        input,
        line,
        folder,
    } = run_in_wsl(
        req,
        &record,
        system_root.as_deref(),
        home.as_deref(),
        |input| {
            call_of(
                app,
                crate::environments::WSL_PACKAGE,
                "prepare",
                input,
                None,
                None,
            )
            .map(|(prepared, _)| prepared)
        },
        // shortcut: the tests check that `run_in_wsl` hands this closure the environment's line
        // (`a_launch_in_wsl_is_confirmed_with_the_environments_line`), not that the closure passes
        // it on to `call_of`: that needs the running app's `AppHandle`, which no test can make;
        // test it once the app has a test harness for its Tauri state, or once this code takes
        // that state instead of the handle.
        |input, line| call_of(app, PACKAGE, "launch", input, None, Some(line)),
    )?;
    let client = client_in(client, environment);
    let st = app.state::<App>();
    let (entry, old) = {
        let mut l = st.launcher.lock().unwrap();
        l.remember_folder(&client, &folder);
        let kept = approval.map(|a| l.keep(&client, &input, Some((&line, &folder)), &a));
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
    let r = call_of(
        app,
        PACKAGE,
        "launch",
        entry.input.clone(),
        Some(gesture),
        entry.shown.clone(),
    );
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
            if let Some(cwd) = entry.folder.as_deref().or(i["cwd"].as_str()) {
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

/// The tray's Start… submenu itself, for the native test's click on Windows (through the menu's own
/// window message).
#[cfg(all(feature = "native-test", windows))]
pub fn start_menu() -> Option<Submenu<Wry>> {
    START.lock().unwrap().clone()
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

/// The tray's Start… items as they are now, each item's ID, label and whether it can be clicked;
/// none before the tray is built. For the native test, which reads what `refresh_tray` made.
#[cfg(feature = "native-test")]
pub fn tray_items() -> Option<Vec<(String, String, bool)>> {
    let menu = START.lock().unwrap().clone()?;
    menu.items()
        .ok()?
        .into_iter()
        .map(|i| {
            let m = i.as_menuitem()?;
            Some((m.id().0.clone(), m.text().ok()?, m.is_enabled().ok()?))
        })
        .collect()
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
