//! The host's spawn service: starts the launches the router confirmed (`route.rs`), for the tool
//! that serves the confirmed call, which names nothing of the launch itself
//! (`../app-host/README.md`, "Launches and managed runtimes"). Linux only, as the program host.
//!
//! A launch runs the executable the confirmation opened (the opened file, never its path again)
//! in the cwd it opened, with exactly the confirmed argv, the executable's name as named as its
//! `argv[0]`, /dev/null on stdin, stdout and stderr, and no other descriptor of the app's (no
//! tool pipe, vault file or socket). Its environment is the launch environment (`environment`):
//! selected values of the app's own environment as it started (`Snapshot`), the variables the
//! confirmed launch set, inherited by name or removed, and the secrets the host resolved for it.
//! A process given a secret's value cannot write core files, nor can its descendants.
//!
//! A **detached** launch (an agent, a terminal) leaves the app at once: a wrapper process starts
//! it in a session and process group of its own, with no parent-death signal, and exits, and the
//! app reaps the wrapper. Nothing of the app ever signals it: it outlives its tool's idle stop,
//! crash, swap and removal, and the app's quit.
//!
//! A **managed** runtime (`Runtimes`) is the app's: the host, not the tool, keeps its process
//! (the live child, so its number is never reused while the host may signal it), its process
//! group of its own (never the tool's), a start token and its exit, on a dedicated supervisor
//! thread that started it and stays until it is reaped, so its parent-death signal (SIGTERM) is
//! tied to the app and not to a short-lived thread. Only the package that started it controls it,
//! at any release or generation, through `runtime` frames: status, list, stop and restart. Stop
//! sends SIGTERM to its group and SIGKILL after five seconds, then reaps it; restart starts the
//! same confirmed launch again, unless its executable changed since it was confirmed. The app's
//! quit, and the package's disable or removal, stop its runtimes; a tool's idle stop, crash or
//! swap does not.
//!
//! If the app itself dies, the runtime's leader gets SIGTERM, but a descendant that ignores it or
//! started a session of its own lives on as an orphan: a process group and a parent-death signal
//! are not crash cleanup of every descendant. `runtimes.json` (owner-only) names the runtimes
//! running, so the next start reports each as `unknown`; it never signals them, because a
//! process ID alone does not prove it is still that runtime.

use crate::program::{leader, Leader};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::ffi::OsString;
use std::fs::File;
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{FileExt, OpenOptionsExt};
use std::os::unix::process::CommandExt;
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

/// The launch environment policy's version: part of each launch's environment revision.
const POLICY: u32 = 1;
/// The session values a launch gets from the app's environment as it started, with `LC_*`.
pub const BASE: &[&str] = &[
    "HOME",
    "USER",
    "LOGNAME",
    "SHELL",
    "PATH",
    "LANG",
    "LANGUAGE",
    "TZ",
    "TERM",
    "COLORTERM",
    "DISPLAY",
    "WAYLAND_DISPLAY",
    "XAUTHORITY",
    "DBUS_SESSION_BUS_ADDRESS",
    "XDG_RUNTIME_DIR",
    "XDG_CONFIG_HOME",
    "XDG_DATA_HOME",
    "XDG_CACHE_HOME",
    "XDG_STATE_HOME",
    "XDG_CONFIG_DIRS",
    "XDG_DATA_DIRS",
    "XDG_SESSION_TYPE",
    "XDG_CURRENT_DESKTOP",
    "XDG_SESSION_DESKTOP",
    "http_proxy",
    "https_proxy",
    "no_proxy",
    "all_proxy",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "NO_PROXY",
    "ALL_PROXY",
];
/// Names a launch may never set, inherit or receive a secret in: the app's own and its tests'
/// (`PLUXY_`), the dynamic loader's (`LD_`) and the AppImage runtime's.
const NEVER_PREFIX: &[&str] = &["PLUXY_", "LD_"];
const NEVER: &[&str] = &["APPIMAGE", "APPDIR", "ARGV0", "OWD"];
/// At most this many names in each of set, unset, inherit and secrets.
const MAX_NAMES: usize = 64;
const MAX_NAME: usize = 128;
const MAX_VALUE: usize = 32 << 10;
/// Runtimes a package keeps at once, running or ended; the oldest ended one makes room.
const MAX_RUNTIMES: usize = 8;
/// After SIGTERM to a runtime's group, before SIGKILL.
const STOP: Duration = Duration::from_secs(5);
const TICK: Duration = Duration::from_millis(100);

/// The app's environment as it started, and its core file size limits then: what launches take
/// their session values from. Only `spawn` reads it.
pub struct Snapshot {
    vars: BTreeMap<String, OsString>,
    core: libc::rlimit,
}

static SNAPSHOT: OnceLock<Snapshot> = OnceLock::new();

impl Snapshot {
    /// This process's environment and core limits now.
    pub fn now() -> Snapshot {
        let vars = std::env::vars_os()
            .filter_map(|(k, v)| Some((k.into_string().ok()?, v)))
            .collect();
        Snapshot {
            vars,
            core: core_limit(),
        }
    }

    #[cfg(test)]
    pub fn of(vars: &[(&str, &str)], core: libc::rlimit) -> Snapshot {
        Snapshot {
            vars: vars
                .iter()
                .map(|(k, v)| (k.to_string(), OsString::from(v)))
                .collect(),
            core,
        }
    }

    fn get(&self, name: &str) -> Option<&OsString> {
        self.vars.get(name)
    }
}

/// The app's snapshot, taken at its start (`capture`).
pub fn snapshot() -> &'static Snapshot {
    SNAPSHOT.get_or_init(Snapshot::now)
}

/// Takes the app's snapshot, before anything changes its environment, then lowers the app's own
/// soft core file size limit to zero: the app holds the vault's secrets, and its tools, which
/// inherit the limit, receive secrets on their pipes. The hard limit stays, so a developer can
/// raise the soft one again (`prlimit --core=unlimited --pid <app>`). Launches given no secret get
/// the limits the app started with back.
pub fn capture() {
    let s = snapshot();
    let lowered = libc::rlimit {
        rlim_cur: 0,
        rlim_max: s.core.rlim_max,
    };
    unsafe { libc::setrlimit(libc::RLIMIT_CORE, &lowered) };
}

fn core_limit() -> libc::rlimit {
    let mut l = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    unsafe { libc::getrlimit(libc::RLIMIT_CORE, &mut l) };
    l
}

/// How long a launch lives.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Lifetime {
    /// Outside the app: it outlives the tool and the app.
    Detached,
    /// The app's: stopped by its package's tool, the app's quit or the package's disable.
    Managed,
}

impl Lifetime {
    pub fn name(self) -> &'static str {
        match self {
            Lifetime::Detached => "detached",
            Lifetime::Managed => "managed",
        }
    }

    /// What the native confirmation says of it.
    pub fn review(self) -> &'static str {
        match self {
            Lifetime::Detached => {
                "Lifetime: detached: it runs on after the tool stops and after the app quits."
            }
            Lifetime::Managed => {
                "Lifetime: managed: the app keeps it running until the tool stops it, you disable or remove the package, or the app quits."
            }
        }
    }
}

/// A launch's environment request, as its confirmed input names it (`environment`): variables
/// set, removed from the session values, inherited from the app's environment by name, and given
/// the secret of one of the launch's secret fields.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Request {
    pub set: BTreeMap<String, String>,
    pub unset: Vec<String>,
    pub inherit: Vec<String>,
    /// Variable name → the input's secret field whose secret it gets.
    pub secrets: BTreeMap<String, String>,
}

fn valid_name(n: &str) -> Result<(), String> {
    let mut chars = n.chars();
    let first = chars
        .next()
        .is_some_and(|c| c.is_ascii_alphabetic() || c == '_');
    if !first || n.len() > MAX_NAME || !chars.all(|c| c.is_ascii_alphanumeric() || c == '_') {
        return Err(format!(
            "{n:?} is not an environment variable name (letters, digits and _, not starting with a digit, at most {MAX_NAME})"
        ));
    }
    Ok(())
}

fn allowed_name(n: &str) -> Result<(), String> {
    valid_name(n)?;
    if NEVER.contains(&n) || NEVER_PREFIX.iter().any(|p| n.starts_with(p)) {
        return Err(format!("a launch may not set or inherit {n}"));
    }
    Ok(())
}

/// The lifetime and environment request of a launch's input; secret_field says which of its
/// fields are secret fields the router filled.
pub fn parse(
    input: &Value,
    secret_field: &dyn Fn(&str) -> bool,
) -> Result<(Lifetime, Request), String> {
    let lifetime = match input.get("lifetime") {
        None => Lifetime::Detached,
        Some(v) if v == "detached" => Lifetime::Detached,
        Some(v) if v == "managed" => Lifetime::Managed,
        Some(v) => {
            return Err(format!(
                "a launch's lifetime is detached or managed, not {v}"
            ))
        }
    };
    let mut r = Request::default();
    let Some(env) = input.get("environment") else {
        return Ok((lifetime, r));
    };
    let env = env
        .as_object()
        .ok_or("a launch's environment is an object")?;
    if let Some(k) = env
        .keys()
        .find(|k| !["set", "unset", "inherit", "secrets"].contains(&k.as_str()))
    {
        return Err(format!(
            "a launch's environment has set, unset, inherit and secrets only, not {k}"
        ));
    }
    let names = |k: &str| -> Result<Vec<String>, String> {
        let Some(v) = env.get(k) else {
            return Ok(Vec::new());
        };
        let a = v
            .as_array()
            .filter(|a| a.len() <= MAX_NAMES)
            .ok_or(format!(
                "environment {k} is a list of at most {MAX_NAMES} names"
            ))?;
        let mut out: Vec<String> = a
            .iter()
            .map(|n| {
                n.as_str()
                    .map(String::from)
                    .ok_or(format!("environment {k} holds names"))
            })
            .collect::<Result<_, _>>()?;
        out.sort();
        out.dedup();
        Ok(out)
    };
    let pairs = |k: &str| -> Result<BTreeMap<String, String>, String> {
        let Some(v) = env.get(k) else {
            return Ok(BTreeMap::new());
        };
        let m = v
            .as_object()
            .filter(|m| m.len() <= MAX_NAMES)
            .ok_or(format!(
                "environment {k} maps at most {MAX_NAMES} names to strings"
            ))?;
        m.iter()
            .map(|(n, v)| {
                v.as_str()
                    .map(|s| (n.clone(), s.to_string()))
                    .ok_or(format!("environment {k}'s {n} is a string"))
            })
            .collect()
    };
    r.unset = names("unset")?;
    r.inherit = names("inherit")?;
    r.set = pairs("set")?;
    r.secrets = pairs("secrets")?;
    for n in &r.unset {
        valid_name(n)?;
    }
    for n in r.inherit.iter().chain(r.set.keys()).chain(r.secrets.keys()) {
        allowed_name(n)?;
    }
    for (n, v) in &r.set {
        if v.len() > MAX_VALUE || v.contains('\0') {
            return Err(format!(
                "environment set's {n} is at most {MAX_VALUE} bytes, with no NUL"
            ));
        }
    }
    for (n, field) in &r.secrets {
        if !secret_field(field) {
            return Err(format!(
                "environment secrets' {n} names {field}, which is not a secret field this launch fills"
            ));
        }
    }
    let mut seen = std::collections::BTreeSet::new();
    for n in r.inherit.iter().chain(r.set.keys()).chain(r.secrets.keys()) {
        if !seen.insert(n) {
            return Err(format!(
                "environment names {n} more than once in set, inherit and secrets"
            ));
        }
    }
    Ok((lifetime, r))
}

impl Request {
    /// What the native confirmation shows of it: each variable's name, and the value only of
    /// those set (the confirmed input's own text); never an inherited value or a secret's.
    /// secret names a secret field's secret (its label and ID).
    pub fn review(&self, secret: &dyn Fn(&str) -> String) -> Vec<(String, Option<String>)> {
        let mut out = Vec::new();
        for (n, v) in &self.set {
            out.push((n.clone(), Some(v.clone())));
        }
        for n in &self.inherit {
            out.push((
                n.clone(),
                Some("inherited from the app's environment".into()),
            ));
        }
        for n in &self.unset {
            out.push((n.clone(), Some("removed".into())));
        }
        for (n, field) in &self.secrets {
            out.push((n.clone(), Some(format!("the secret {}", secret(field)))));
        }
        out
    }

    /// The environment revision a confirmation and a remembered approval are bound to: the policy
    /// (its version and selected names), the lifetime and this request. Session values (the
    /// display, the bus address, PATH's entries) are not part of it.
    pub fn revision(&self, lifetime: Lifetime) -> String {
        let v = json!({
            "policy": POLICY, "base": BASE, "lifetime": lifetime.name(),
            "set": self.set, "unset": self.unset, "inherit": self.inherit, "secrets": self.secrets,
        });
        Sha256::digest(v.to_string().as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    }
}

/// The entries of a `:`-separated list s that are not inside folder dir.
fn outside(s: &OsString, dir: &OsString) -> OsString {
    let (s, dir) = (s.to_string_lossy(), dir.to_string_lossy());
    let dir = dir.trim_end_matches('/');
    let kept: Vec<&str> = s
        .split(':')
        .filter(|e| !(e == &dir || e.starts_with(&format!("{dir}/"))))
        .collect();
    OsString::from(kept.join(":"))
}

/// A launch's environment: the snapshot's session values (`BASE`, `LC_*`) but those request
/// removes, with the AppImage's own folder taken out of PATH and the XDG folder lists when the app
/// runs as one; then the variables request inherits from the snapshot, those it sets, and the
/// resolved secrets (variable name, value).
pub fn environment(
    snap: &Snapshot,
    request: &Request,
    secrets: &[(String, String)],
) -> Vec<(String, OsString)> {
    let mut env: BTreeMap<String, OsString> = snap
        .vars
        .iter()
        .filter(|(k, _)| BASE.contains(&k.as_str()) || k.starts_with("LC_"))
        .filter(|(k, _)| !request.unset.contains(k))
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect();
    if let Some(appdir) = snap.get("APPDIR") {
        for k in ["PATH", "XDG_DATA_DIRS", "XDG_CONFIG_DIRS"] {
            if let Some(v) = env.get_mut(k) {
                *v = outside(v, appdir);
            }
        }
    }
    for n in &request.inherit {
        if let Some(v) = snap.get(n) {
            env.insert(n.clone(), v.clone());
        }
    }
    for (n, v) in &request.set {
        env.insert(n.clone(), OsString::from(v));
    }
    for (n, v) in secrets {
        env.insert(n.clone(), OsString::from(v));
    }
    env.into_iter().collect()
}

/// What a launch runs from: the executable and cwd its confirmation opened, and the check that
/// they are still what was confirmed.
pub trait Opened: Send + Sync {
    fn executable(&self) -> &File;
    fn cwd(&self) -> &File;
    /// Refused once the executable or cwd changed since it was confirmed (replaced, or written
    /// in place).
    fn recheck(&self) -> Result<(), String>;
}

impl Opened for crate::confirmation::Authorized {
    fn executable(&self) -> &File {
        crate::confirmation::Authorized::executable(self).expect("a launch's executable")
    }
    fn cwd(&self) -> &File {
        crate::confirmation::Authorized::cwd(self).expect("a launch's cwd")
    }
    fn recheck(&self) -> Result<(), String> {
        crate::confirmation::Authorized::recheck(self)
    }
}

/// A confirmed launch, ready to spawn: what it runs from, as named, with its argv, its lifetime,
/// its environment request, and its secrets as variable name and vault secret ID.
pub struct Launch {
    pub opened: Box<dyn Opened>,
    pub named: PathBuf,
    pub argv: Vec<String>,
    pub lifetime: Lifetime,
    pub request: Request,
    pub secrets: Vec<(String, String)>,
}

/// Whether f is a script (`#!`): its interpreter opens it again by its descriptor's path, so the
/// descriptor must stay open across exec.
fn is_script(f: &File) -> bool {
    let mut b = [0u8; 2];
    f.read_at(&mut b, 0).ok() == Some(2) && &b == b"#!"
}

/// The command that runs launch l with environment env: managed (in a process group of its own,
/// SIGTERM when its parent dies) or detached (a wrapper that starts it in a session of its own
/// and exits). With secrets given, it and its descendants cannot write core files; otherwise it
/// gets the app's starting limits back.
fn command(l: &Launch, env: Vec<(String, OsString)>, holds_secrets: bool) -> Command {
    let exe = l.opened.executable();
    let exe_fd = exe.as_raw_fd();
    let cwd_fd = l.opened.cwd().as_raw_fd();
    let script = is_script(exe);
    let detached = l.lifetime == Lifetime::Detached;
    let core = if holds_secrets {
        libc::rlimit {
            rlim_cur: 0,
            rlim_max: 0,
        }
    } else {
        snapshot().core
    };
    let mut cmd = Command::new(format!("/proc/self/fd/{exe_fd}"));
    cmd.arg0(&l.named)
        .args(&l.argv)
        .env_clear()
        .envs(env)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    if !detached {
        cmd.process_group(0);
    }
    let parent = unsafe { libc::getpid() };
    unsafe {
        cmd.pre_exec(move || {
            let err = io::Error::last_os_error;
            if libc::fchdir(cwd_fd) != 0 {
                return Err(err());
            }
            if detached {
                // The wrapper exits at once, for the app to reap; the launch goes on without it,
                // never the app's child, in a session of its own.
                match libc::fork() {
                    -1 => return Err(err()),
                    0 => {}
                    _ => libc::_exit(0),
                }
                if libc::setsid() == -1 {
                    return Err(err());
                }
                if libc::prctl(libc::PR_SET_PDEATHSIG, 0) != 0 {
                    return Err(err());
                }
            } else {
                // If the app dies without stopping it, it gets SIGTERM.
                if libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGTERM) != 0 {
                    return Err(err());
                }
                if libc::getppid() != parent {
                    return Err(io::Error::from_raw_os_error(libc::ESRCH));
                }
            }
            if libc::setrlimit(libc::RLIMIT_CORE, &core) != 0 {
                return Err(err());
            }
            // No descriptor of the app's reaches it: every one but 0-2 closes as it executes.
            if libc::syscall(
                libc::SYS_close_range,
                3u32,
                u32::MAX,
                libc::CLOSE_RANGE_CLOEXEC,
            ) != 0
            {
                return Err(err());
            }
            if script && libc::fcntl(exe_fd, libc::F_SETFD, 0) != 0 {
                return Err(err());
            }
            Ok(())
        });
    }
    cmd
}

/// Resolves l's secrets with value (a vault secret ID's value), refused when one is gone.
fn resolve(
    l: &Launch,
    value: &dyn Fn(&str) -> Option<String>,
) -> Result<Vec<(String, String)>, String> {
    l.secrets
        .iter()
        .map(|(name, id)| {
            value(id)
                .map(|v| (name.clone(), v))
                .ok_or(format!("the secret for {name} was removed from the vault"))
        })
        .collect()
}

/// Starts detached launch l, with its secrets' values resolved by value; returns once its
/// wrapper has been reaped.
pub fn detached(l: &Launch, value: &dyn Fn(&str) -> Option<String>) -> Result<(), String> {
    l.opened.recheck()?;
    let secrets = resolve(l, value)?;
    let env = environment(snapshot(), &l.request, &secrets);
    let mut cmd = command(l, env, !secrets.is_empty());
    let mut wrapper = cmd
        .spawn()
        .map_err(|e| format!("the launch did not start: {e}"))?;
    drop(cmd);
    wrapper.wait().map_err(|e| e.to_string())?;
    Ok(())
}

fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn random_id() -> String {
    let mut b = [0u8; 16];
    getrandom::fill(&mut b).expect("random bytes");
    format!(
        "rt-{}",
        b.iter().map(|x| format!("{x:02x}")).collect::<String>()
    )
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Running,
    Stopping,
    Exited,
    /// Named by `runtimes.json` at start: the app stopped without stopping it. Never signalled.
    Unknown,
}

impl State {
    fn name(self) -> &'static str {
        match self {
            State::Running => "running",
            State::Stopping => "stopping",
            State::Exited => "exited",
            State::Unknown => "unknown",
        }
    }
}

struct Entry {
    id: String,
    package: String,
    named: PathBuf,
    argv: Vec<String>,
    state: State,
    pid: u32,
    started: u64,
    restarts: u32,
    /// A different value each time it is started: the supervisor of an earlier start leaves it.
    token: u64,
    stop: bool,
    busy: bool,
    exit: Option<String>,
    launch: Option<Arc<Launch>>,
}

impl Entry {
    fn view(&self) -> Value {
        let mut v = json!({
            "runtime": self.id, "package": self.package, "state": self.state.name(),
            "pid": self.pid, "started": self.started, "restarts": self.restarts,
            "executable": self.named, "argv": self.argv,
        });
        if let Some(e) = &self.exit {
            v["exit"] = json!(e);
        }
        if self.state == State::Unknown {
            v["note"] = json!("the app stopped without stopping it: it may still run, and the app no longer controls it");
        }
        v
    }
}

type SecretValue = dyn Fn(&str) -> Option<String> + Send + Sync;

struct Shared {
    list: Mutex<Vec<Entry>>,
    changed: Condvar,
    file: Option<PathBuf>,
    value: Box<SecretValue>,
    quitting: AtomicBool,
    tokens: Mutex<u64>,
}

/// The managed runtime registry (see the module's description).
pub struct Runtimes(Arc<Shared>);

fn missing(package: &str, id: &str) -> String {
    format!("{package} has no runtime {id}")
}

impl Runtimes {
    /// The registry, its record of running runtimes in file; those a previous run left are
    /// reported as unknown. value resolves a vault secret ID.
    pub fn open(file: Option<PathBuf>, value: Box<SecretValue>) -> Runtimes {
        let mut list = Vec::new();
        if let Some(f) = &file {
            if let Some(left) = std::fs::read(f)
                .ok()
                .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            {
                for r in left.as_array().into_iter().flatten() {
                    let s = |k: &str| r[k].as_str().unwrap_or_default().to_string();
                    list.push(Entry {
                        id: s("runtime"),
                        package: s("package"),
                        named: PathBuf::from(s("executable")),
                        argv: serde_json::from_value(r["argv"].clone()).unwrap_or_default(),
                        state: State::Unknown,
                        pid: r["pid"].as_u64().unwrap_or(0) as u32,
                        started: r["started"].as_u64().unwrap_or(0),
                        restarts: 0,
                        token: 0,
                        stop: false,
                        busy: false,
                        exit: None,
                        launch: None,
                    });
                }
            }
        }
        let r = Runtimes(Arc::new(Shared {
            list: Mutex::new(list),
            changed: Condvar::new(),
            file,
            value,
            quitting: AtomicBool::new(false),
            tokens: Mutex::new(0),
        }));
        let list = r.0.list.lock().unwrap();
        r.0.save(&list);
        drop(list);
        r
    }

    /// Starts managed launch l for package; its runtime ID and process ID.
    pub fn start(&self, package: &str, l: Launch) -> Result<Value, String> {
        if self.0.quitting.load(Ordering::SeqCst) {
            return Err("the app is quitting".into());
        }
        {
            let mut list = self.0.list.lock().unwrap();
            let mine = list.iter().filter(|e| e.package == package).count();
            if mine >= MAX_RUNTIMES {
                let oldest = list
                    .iter()
                    .position(|e| e.package == package && e.state == State::Exited)
                    .ok_or(format!(
                        "{package} has {MAX_RUNTIMES} runtimes running already"
                    ))?;
                list.remove(oldest);
            }
        }
        let id = random_id();
        let launch = Arc::new(l);
        {
            let mut list = self.0.list.lock().unwrap();
            list.push(Entry {
                id: id.clone(),
                package: package.into(),
                named: launch.named.clone(),
                argv: launch.argv.clone(),
                state: State::Exited,
                pid: 0,
                started: 0,
                restarts: 0,
                token: 0,
                stop: false,
                busy: true,
                exit: None,
                launch: Some(Arc::clone(&launch)),
            });
        }
        let r = self.run(&id, &launch);
        let mut list = self.0.list.lock().unwrap();
        match r {
            Ok(()) => {
                let e = list
                    .iter_mut()
                    .find(|e| e.id == id)
                    .expect("the new runtime");
                e.busy = false;
                let v = e.view();
                self.0.changed.notify_all();
                Ok(json!({"lifetime": "managed", "runtime": id, "pid": v["pid"]}))
            }
            Err(e) => {
                list.retain(|x| x.id != id);
                Err(e)
            }
        }
    }

    /// Starts runtime id's launch on a supervisor thread of its own, which stays until it has
    /// been reaped; returns once it runs (the entry is Running with its process ID).
    fn run(&self, id: &str, l: &Arc<Launch>) -> Result<(), String> {
        l.opened.recheck().map_err(|e| {
            format!("{e}; its executable or cwd changed since it was confirmed: launch it again")
        })?;
        let secrets = resolve(l, &*self.0.value)?;
        let env = environment(snapshot(), &l.request, &secrets);
        let token = {
            let mut t = self.0.tokens.lock().unwrap();
            *t += 1;
            *t
        };
        let (tx, rx) = std::sync::mpsc::channel();
        let (shared, launch, rid) = (Arc::clone(&self.0), Arc::clone(l), id.to_string());
        let holds = !secrets.is_empty();
        drop(secrets);
        std::thread::Builder::new()
            .name("pluxy-runtime".into())
            .spawn(move || {
                let mut cmd = command(&launch, env, holds);
                let child = cmd.spawn();
                drop(cmd);
                match child {
                    Ok(c) => {
                        let pid = c.id();
                        {
                            let mut list = shared.list.lock().unwrap();
                            if let Some(e) = list.iter_mut().find(|e| e.id == rid) {
                                e.state = State::Running;
                                e.pid = pid;
                                e.started = now_ms();
                                e.token = token;
                                e.stop = false;
                                e.exit = None;
                            }
                            shared.save(&list);
                        }
                        let _ = tx.send(Ok(()));
                        supervise(&shared, &rid, token, c);
                    }
                    Err(e) => {
                        let _ = tx.send(Err(format!("the runtime did not start: {e}")));
                    }
                }
            })
            .map_err(|e| e.to_string())?;
        rx.recv()
            .unwrap_or_else(|_| Err("the runtime's supervisor stopped".into()))
    }

    fn find<'a>(
        list: &'a mut MutexGuard<'_, Vec<Entry>>,
        package: &str,
        id: &str,
    ) -> Result<&'a mut Entry, String> {
        list.iter_mut()
            .find(|e| e.id == id && e.package == package)
            .ok_or_else(|| missing(package, id))
    }

    /// Runtime id of package, as status shows it.
    pub fn status(&self, package: &str, id: &str) -> Result<Value, String> {
        let mut list = self.0.list.lock().unwrap();
        Self::find(&mut list, package, id).map(|e| e.view())
    }

    /// Package's runtimes.
    pub fn list(&self, package: &str) -> Vec<Value> {
        let list = self.0.list.lock().unwrap();
        list.iter()
            .filter(|e| e.package == package)
            .map(Entry::view)
            .collect()
    }

    /// Stops runtime id of package and waits until it has been reaped; its status.
    pub fn stop(&self, package: &str, id: &str) -> Result<Value, String> {
        let mut list = self.0.list.lock().unwrap();
        let e = Self::find(&mut list, package, id)?;
        if e.state == State::Unknown {
            return Err(format!(
                "{id} was started before the app last stopped without stopping it; the app no longer controls it"
            ));
        }
        if e.busy {
            return Err(format!("{id} is being started or restarted"));
        }
        drop(list);
        self.end(package, id)
    }

    /// Asks runtime id's supervisor to stop it and waits, at most the stop grace and two seconds
    /// more, until it has been reaped.
    fn end(&self, package: &str, id: &str) -> Result<Value, String> {
        let deadline = Instant::now() + STOP + Duration::from_secs(2);
        let mut list = self.0.list.lock().unwrap();
        loop {
            let e = Self::find(&mut list, package, id)?;
            match e.state {
                State::Running => {
                    e.stop = true;
                    e.state = State::Stopping;
                }
                State::Stopping => {}
                _ => return Ok(e.view()),
            }
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return Err(format!("{id} did not stop in time"));
            }
            list = self.0.changed.wait_timeout(list, left).unwrap().0;
        }
    }

    /// Stops runtime id of package, then starts its confirmed launch again, unless its executable
    /// or cwd changed since it was confirmed or a secret it gets was removed; its status.
    pub fn restart(&self, package: &str, id: &str) -> Result<Value, String> {
        let launch = {
            let mut list = self.0.list.lock().unwrap();
            let e = Self::find(&mut list, package, id)?;
            let l = e.launch.clone().ok_or_else(|| {
                format!("{id} was started before the app last stopped without stopping it; the app no longer controls it")
            })?;
            if e.busy {
                return Err(format!("{id} is being started or restarted"));
            }
            e.busy = true;
            l
        };
        let r = self.end(package, id).and_then(|_| {
            if self.0.quitting.load(Ordering::SeqCst) {
                return Err("the app is quitting".into());
            }
            self.run(id, &launch)
        });
        let mut list = self.0.list.lock().unwrap();
        let e = Self::find(&mut list, package, id)?;
        e.busy = false;
        if r.is_ok() {
            e.restarts += 1;
        }
        let v = e.view();
        drop(list);
        r.map(|_| v)
    }

    /// Stops every runtime the predicate picks and waits, at most the stop grace and two seconds
    /// more, until each has been reaped.
    fn stop_where(&self, pick: impl Fn(&Entry) -> bool) {
        let ids: Vec<(String, String)> = {
            let mut list = self.0.list.lock().unwrap();
            let mut ids = Vec::new();
            for e in list.iter_mut().filter(|e| pick(e)) {
                if e.state == State::Running {
                    e.stop = true;
                    e.state = State::Stopping;
                }
                if e.state == State::Stopping {
                    ids.push((e.package.clone(), e.id.clone()));
                }
            }
            ids
        };
        for (p, id) in ids {
            let _ = self.end(&p, &id);
        }
    }

    /// Stops package's runtimes: it was disabled or removed, so nothing could control them.
    pub fn stop_package(&self, package: &str) {
        self.stop_where(|e| e.package == package);
    }

    /// Stops every runtime the app owns, at its quit; none starts after. Detached launches are
    /// not the app's and go on.
    pub fn stop_all(&self) {
        self.0.quitting.store(true, Ordering::SeqCst);
        self.stop_where(|_| true);
    }
}

impl Shared {
    /// Writes the runtimes running (or stopping) to the registry's file, owner-only.
    fn save(&self, list: &[Entry]) {
        let Some(f) = &self.file else {
            return;
        };
        let running: Vec<Value> = list
            .iter()
            .filter(|e| matches!(e.state, State::Running | State::Stopping))
            .map(Entry::view)
            .collect();
        let tmp = f.with_extension("tmp");
        let written = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(&tmp)
            .and_then(|mut file| {
                use std::io::Write;
                file.write_all(json!(running).to_string().as_bytes())?;
                file.sync_all()
            })
            .and_then(|_| std::fs::rename(&tmp, f));
        if let Err(e) = written {
            eprintln!("pluxy-app: could not record the managed runtimes: {e}");
        }
    }
}

/// The supervisor: waits for runtime id's leader (started as token) to exit or be asked to stop;
/// on a stop, SIGTERM to its group and SIGKILL after the grace; then SIGKILL to what is left of
/// its group, while the leader is not reaped (its group's number cannot be reused until then),
/// and the reap.
fn supervise(shared: &Shared, id: &str, token: u64, mut child: Child) {
    let pid = child.id();
    let mut term: Option<Instant> = None;
    loop {
        if leader(pid) != Leader::Running {
            break;
        }
        let stop = shared
            .list
            .lock()
            .unwrap()
            .iter()
            .find(|e| e.id == id && e.token == token)
            .is_none_or(|e| e.stop);
        if stop && term.is_none() {
            unsafe { libc::killpg(pid as libc::pid_t, libc::SIGTERM) };
            term = Some(Instant::now());
        }
        if term.is_some_and(|t| t.elapsed() >= STOP) {
            break;
        }
        std::thread::sleep(TICK);
    }
    if leader(pid) != Leader::Gone {
        unsafe { libc::killpg(pid as libc::pid_t, libc::SIGKILL) };
        let _ = child.kill();
    }
    let status = child
        .wait()
        .map(|s| s.to_string())
        .unwrap_or_else(|e| e.to_string());
    let mut list = shared.list.lock().unwrap();
    if let Some(e) = list.iter_mut().find(|e| e.id == id && e.token == token) {
        e.state = State::Exited;
        e.stop = false;
        e.exit = Some(status);
    }
    shared.save(&list);
    shared.changed.notify_all();
}

/// The opened executable and cwd of a launch started without a confirmation: the unit tests'.
#[cfg(test)]
pub struct Plain {
    pub executable: File,
    pub cwd: File,
}

#[cfg(test)]
impl Opened for Plain {
    fn executable(&self) -> &File {
        &self.executable
    }
    fn cwd(&self) -> &File {
        &self.cwd
    }
    fn recheck(&self) -> Result<(), String> {
        Ok(())
    }
}

#[cfg(test)]
#[path = "spawn_tests.rs"]
mod tests;
