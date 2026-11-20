//! The host's spawn service: starts the launches the router confirmed (`route.rs`), for the tool
//! that serves the confirmed call, which names nothing of the launch itself
//! (`../app-host/README.md`, "Launches and managed runtimes"), on Linux, macOS and Windows: the
//! processes it starts and holds, and what it may do to them, are `owned.rs`'s.
//!
//! A launch runs the executable the confirmation opened (the opened file, never its path again)
//! in the cwd it opened, with exactly the confirmed argv, the executable's name as named as its
//! `argv[0]`, /dev/null on stdin, stdout and stderr, and no other descriptor of the app's (no
//! tool pipe, vault file or socket) but, for a terminal launch on macOS, the confirmed program
//! the terminal runs, as 3 (`command`). Its environment is the launch environment (`environment`):
//! selected values of the app's own environment as it started (`Snapshot`), the variables the
//! confirmed launch set, inherited by name or removed, and the secrets the host resolved for it,
//! each resolved at the moment it is given (the spawn, a runtime's start or restart) by the
//! host's `Values`, which checks it as a delivery to the launch's package
//! (`client_keys::launch_value`), and checks them all once more right before the spawn.
//! A process given a secret's value has core file size limits of 1, so the kernel neither writes
//! its core nor runs a core dump handler for it, nor for its descendants.
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
//! same confirmed launch again, unless its executable changed since it was confirmed or a secret
//! it gets is refused now. The app's quit stops its runtimes; the package's disable or removal
//! retires them (stopped, their launches dropped, none started until it is enabled again); a
//! tool's idle stop, crash or swap does neither.
//!
//! If the app itself dies, the runtime's leader gets SIGTERM, but a descendant that ignores it or
//! started a session of its own lives on as an orphan: a process group and a parent-death signal
//! are not crash cleanup of every descendant. `runtimes.json` (owner-only) names the runtimes
//! running, so the next start reports each as `unknown`; it never signals them, because a
//! process ID alone does not prove it is still that runtime.

use crate::owned::{kill_group, leader, term_group, Child, Leader};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::ffi::OsString;
use std::fs::File;
#[cfg(unix)]
use std::io;
#[cfg(unix)]
use std::os::fd::AsRawFd;
#[cfg(target_os = "linux")]
use std::os::unix::fs::FileExt;
#[cfg(unix)]
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
#[cfg(unix)]
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

/// The launch environment policy's version: part of each launch's environment revision.
const POLICY: u32 = 1;
/// The session values a launch gets from the app's environment as it started, with `LC_*`.
#[cfg(target_os = "linux")]
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
/// macOS: the user's session values, its per-user temporary folder and text encoding, the proxy
/// variables.
#[cfg(target_os = "macos")]
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
    "TMPDIR",
    "__CF_USER_TEXT_ENCODING",
    "http_proxy",
    "https_proxy",
    "no_proxy",
    "all_proxy",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "NO_PROXY",
    "ALL_PROXY",
];
/// Windows (names in capitals: Windows compares them ignoring case): the user's profile and
/// folders, the system's folders (`SYSTEMROOT` is needed by the network stack), the program
/// search rules and the proxy variables.
#[cfg(windows)]
pub const BASE: &[&str] = &[
    "USERPROFILE",
    "USERNAME",
    "USERDOMAIN",
    "HOMEDRIVE",
    "HOMEPATH",
    "APPDATA",
    "LOCALAPPDATA",
    "PROGRAMDATA",
    "PROGRAMFILES",
    "PROGRAMFILES(X86)",
    "PROGRAMW6432",
    "COMMONPROGRAMFILES",
    "COMMONPROGRAMFILES(X86)",
    "COMMONPROGRAMW6432",
    "SYSTEMROOT",
    "SYSTEMDRIVE",
    "WINDIR",
    "COMSPEC",
    "PATHEXT",
    "PATH",
    "TEMP",
    "TMP",
    "OS",
    "PROCESSOR_ARCHITECTURE",
    "NUMBER_OF_PROCESSORS",
    "COMPUTERNAME",
    "LANG",
    "TZ",
    "HTTP_PROXY",
    "HTTPS_PROXY",
    "NO_PROXY",
    "ALL_PROXY",
];
/// Names a launch may never set, inherit or receive a secret in: the app's own and its tests'
/// (`PLUXY_`), the dynamic loader's (`LD_`) and the AppImage runtime's, and the .NET runtime's
/// profiler and the legacy `COMPlus_` settings. Prefixes are upper case, as names are compared.
const NEVER_PREFIX: &[&str] = &[
    "PLUXY_",
    "LD_",
    "MALLOC_",
    "CORECLR_",
    "COR_PROFILER_PATH",
    "DOTNET_PROFILER",
    "COMPLUS_",
];
/// And per platform: macOS's dynamic loader (`DYLD_`), the .NET runtime's other settings on
/// Windows (`COR_`).
#[cfg(target_os = "linux")]
const NEVER_PREFIX_PLATFORM: &[&str] = &[];
#[cfg(target_os = "macos")]
const NEVER_PREFIX_PLATFORM: &[&str] = &["DYLD_"];
#[cfg(windows)]
const NEVER_PREFIX_PLATFORM: &[&str] = &["COR_"];
/// And names that make the dynamic loader, the C library or an interpreter load or run code of
/// their own, like `LD_*`.
const NEVER: &[&str] = &[
    "APPIMAGE",
    "APPDIR",
    "ARGV0",
    "OWD",
    "GCONV_PATH",
    "GLIBC_TUNABLES",
    "LOCPATH",
    "NLSPATH",
    "HOSTALIASES",
    "RES_OPTIONS",
    "NODE_OPTIONS",
    "BASH_ENV",
    "ENV",
    "SHELLOPTS",
    "BASHOPTS",
    "PROMPT_COMMAND",
    "PS4",
    "PYTHONSTARTUP",
    "PYTHONINSPECT",
    "PERL5OPT",
    "PERL5DB",
    "PERL5LIB",
    "RUBYOPT",
    "JAVA_TOOL_OPTIONS",
    "_JAVA_OPTIONS",
    "JDK_JAVA_OPTIONS",
    "DOTNET_STARTUP_HOOKS",
    "COR_PROFILER",
    "DOTNET_ADDITIONAL_DEPS",
    "PYTHONPATH",
    "PYTHONHOME",
    "NODE_PATH",
    "RUBYLIB",
];
/// Windows adds a compatibility layer's code loading (`__COMPAT_LAYER`), and `WSLENV`, which
/// would pass the Windows variables it names on into a WSL distribution.
#[cfg(windows)]
const NEVER_PLATFORM: &[&str] = &["__COMPAT_LAYER", "WSLENV"];
#[cfg(not(windows))]
const NEVER_PLATFORM: &[&str] = &[];
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
    #[cfg(unix)]
    core: libc::rlimit,
}

static SNAPSHOT: OnceLock<Snapshot> = OnceLock::new();

impl Snapshot {
    /// This process's environment and core limits now.
    pub fn now() -> Snapshot {
        let vars = std::env::vars_os()
            .filter_map(|(k, v)| Some((key(&k.into_string().ok()?), v)))
            .collect();
        Snapshot {
            vars,
            #[cfg(unix)]
            core: core_limit(),
        }
    }

    #[cfg(all(test, unix))]
    pub fn of(vars: &[(&str, &str)], core: libc::rlimit) -> Snapshot {
        Snapshot {
            vars: vars
                .iter()
                .map(|(k, v)| (key(k), OsString::from(v)))
                .collect(),
            core,
        }
    }

    #[cfg(all(test, not(unix)))]
    pub fn of(vars: &[(&str, &str)]) -> Snapshot {
        Snapshot {
            vars: vars
                .iter()
                .map(|(k, v)| (key(k), OsString::from(v)))
                .collect(),
        }
    }

    fn get(&self, name: &str) -> Option<&OsString> {
        self.vars.get(&key(name))
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
/// On Windows, where there are no core files, the app asks for no error report of its own crash
/// (`SEM_NOGPFAULTERRORBOX`), which a launch given a secret inherits; one given none starts with
/// the system's default (`owned::windows`), and tools and runtimes given secrets die without a
/// report in their jobs.
pub fn capture() {
    let s = snapshot();
    #[cfg(unix)]
    {
        let lowered = libc::rlimit {
            rlim_cur: 0,
            rlim_max: s.core.rlim_max,
        };
        unsafe { libc::setrlimit(libc::RLIMIT_CORE, &lowered) };
    }
    #[cfg(windows)]
    {
        let _ = s;
        use windows_sys::Win32::System::Diagnostics::Debug::{
            GetErrorMode, SetErrorMode, SEM_NOGPFAULTERRORBOX,
        };
        unsafe { SetErrorMode(GetErrorMode() | SEM_NOGPFAULTERRORBOX) };
    }
}

/// A variable's name as the app keeps it: on Windows in capitals, since Windows compares names
/// ignoring case (`Path` is `PATH`).
fn key(name: &str) -> String {
    if cfg!(windows) {
        name.to_ascii_uppercase()
    } else {
        name.to_string()
    }
}

#[cfg(unix)]
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
    /// The input's secret field whose secret the launch gets on its stdin (`stdin`), the private
    /// channel to a credential bridge: written, then closed.
    pub stdin: Option<String>,
    /// Only the absolute entries of `PATH` (a launch that opens a file): a relative entry would
    /// find programs in its working folder.
    pub absolute_path: bool,
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
    let upper = n.to_ascii_uppercase();
    if NEVER.contains(&upper.as_str())
        || NEVER_PLATFORM.contains(&upper.as_str())
        || NEVER_PREFIX
            .iter()
            .chain(NEVER_PREFIX_PLATFORM)
            .any(|p| upper.starts_with(p))
    {
        return Err(format!("a launch may not set or inherit {n}"));
    }
    Ok(())
}

/// Whether a variable named n likely holds a credential (case-insensitive: `*TOKEN*`, `*SECRET*`,
/// `*PASSWORD*`, `*PASSWD*`, `*CREDENTIAL*`, `*_KEY`, `*_KEY_*`, `*APIKEY*`, `TAURI_SIGNING_*`): a
/// launch gets such a value only as a vault secret (`secrets`), never inherited by name.
fn credential_like(n: &str) -> bool {
    let u = n.to_ascii_uppercase();
    [
        "TOKEN",
        "SECRET",
        "PASSWORD",
        "PASSWD",
        "CREDENTIAL",
        "APIKEY",
        "_KEY_",
    ]
    .iter()
    .any(|w| u.contains(w))
        || u.ends_with("_KEY")
        || u.starts_with("TAURI_SIGNING_")
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
    // A launch that names the program a terminal runs is a terminal launch. The process the host
    // holds is then the terminal's own launcher, which many terminals (gnome-terminal, and konsole
    // or kitty in single-instance mode) end at once after handing the work to a server process:
    // stop and status would track a process that is gone, not the agent. So only direct launches
    // are managed.
    if lifetime == Lifetime::Managed && input.get("program").is_some() {
        return Err("a terminal launch is detached only: a terminal may hand its session to a server process and exit, so the app could not stop it (only a direct launch can be managed)".into());
    }
    let mut r = Request::default();
    if let Some(v) = input.get("stdin") {
        let field = v.as_str().ok_or("a launch's stdin names a secret field")?;
        if !secret_field(field) {
            return Err(format!(
                "stdin names {field}, which is not a secret field this launch fills"
            ));
        }
        r.stdin = Some(field.to_string());
    }
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
    if let Some(n) = r.inherit.iter().find(|n| credential_like(n)) {
        return Err(format!(
            "a launch may not inherit {n}, which looks like a credential: pass it as a vault secret (environment secrets)"
        ));
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
    // Each name once, compared as the launch's environment compares them (`key`: ignoring case on
    // Windows), so the confirmation shows every variable the launch gets, and only once.
    let mut unset = std::collections::BTreeSet::new();
    if let Some(n) = r.unset.iter().find(|n| !unset.insert(key(n))) {
        return Err(format!("environment unset names {n} more than once"));
    }
    let mut seen = std::collections::BTreeSet::new();
    for n in r.inherit.iter().chain(r.set.keys()).chain(r.secrets.keys()) {
        if !seen.insert(key(n)) {
            return Err(format!(
                "environment names {n} more than once in set, inherit and secrets"
            ));
        }
    }
    Ok((lifetime, r))
}

impl Request {
    /// Whether the launch gives the secret of its secret field field to the process it starts: in
    /// its environment, or on its stdin.
    pub fn delivers(&self, field: &str) -> bool {
        self.secrets.values().any(|f| f == field) || self.stdin.as_deref() == Some(field)
    }

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
        if let Some(field) = &self.stdin {
            out.push((
                "(stdin)".into(),
                Some(format!(
                    "the secret {}, written to its standard input",
                    secret(field)
                )),
            ));
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
        // Only when asked, so a launch without it keeps the revision it had before stdin existed.
        let mut v = v;
        if let Some(field) = &self.stdin {
            v["stdin"] = json!(field);
        }
        Sha256::digest(v.to_string().as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    }
}

/// The entries of a `:`-separated list s that are not inside folder dir.
#[cfg(target_os = "linux")]
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
/// runs as one, and on macOS the client folders that exist added to the end of PATH
/// (`with_client_folders`); then the variables request inherits from the snapshot, those it sets,
/// and the resolved secrets (variable name, value).
pub fn environment(
    snap: &Snapshot,
    request: &Request,
    secrets: &[(String, String)],
) -> Vec<(String, OsString)> {
    let mut env: BTreeMap<String, OsString> = snap
        .vars
        .iter()
        .filter(|(k, _)| BASE.contains(&k.as_str()) || k.starts_with("LC_"))
        .filter(|(k, _)| !request.unset.iter().any(|u| key(u) == **k))
        .map(|(k, v)| (k.clone(), v.clone()))
        .collect();
    #[cfg(target_os = "linux")]
    if let Some(appdir) = snap.get("APPDIR") {
        for k in ["PATH", "XDG_DATA_DIRS", "XDG_CONFIG_DIRS"] {
            if let Some(v) = env.get_mut(k) {
                *v = outside(v, appdir);
            }
        }
    }
    #[cfg(target_os = "macos")]
    if let Some(v) = env.get_mut("PATH") {
        *v = with_client_folders(v, snap.get("HOME"));
    }
    for n in &request.inherit {
        if let Some(v) = snap.get(n) {
            env.insert(key(n), v.clone());
        }
    }
    for (n, v) in &request.set {
        env.insert(key(n), OsString::from(v));
    }
    for (n, v) in secrets {
        env.insert(key(n), OsString::from(v));
    }
    if request.absolute_path {
        if let Some(v) = env.get_mut("PATH") {
            // `;` and drive or UNC paths on Windows.
            let sep = if cfg!(windows) { ';' } else { ':' };
            let kept: Vec<String> = v
                .to_string_lossy()
                .split(sep)
                .filter(|e| !e.is_empty() && std::path::Path::new(e).is_absolute())
                .map(String::from)
                .collect();
            *v = OsString::from(kept.join(&sep.to_string()));
        }
    }
    env.into_iter().collect()
}

/// The folders people's command-line clients are installed in on macOS, under the home folder
/// and then system-wide: npm, Bun, Cargo, Volta, pnpm and Homebrew put them there.
#[cfg(target_os = "macos")]
const CLIENT_FOLDERS: &[&str] = &[
    "~/.local/bin",
    "~/.npm-global/bin",
    "~/.bun/bin",
    "~/.cargo/bin",
    "~/.volta/bin",
    "~/Library/pnpm",
    "~/bin",
    "/opt/homebrew/bin",
    "/usr/local/bin",
];

/// PATH value path followed by the folders of `CLIENT_FOLDERS` that exist and it lacks (`~` is
/// home, when it is absolute): an app started from the Finder or the Dock has launchd's PATH,
/// `/usr/bin:/bin:/usr/sbin:/sbin`, which finds no client a person installed.
#[cfg(target_os = "macos")]
fn with_client_folders(path: &OsString, home: Option<&OsString>) -> OsString {
    let home = home.map(PathBuf::from).filter(|h| h.is_absolute());
    let mut entries: Vec<PathBuf> = std::env::split_paths(path).collect();
    for f in CLIENT_FOLDERS {
        let d = match (f.strip_prefix("~/"), &home) {
            (Some(rest), Some(h)) => h.join(rest),
            (Some(_), None) => continue,
            (None, _) => PathBuf::from(f),
        };
        if d.is_dir() && !entries.contains(&d) {
            entries.push(d);
        }
    }
    std::env::join_paths(entries).unwrap_or_else(|_| path.clone())
}

/// What a launch runs from: the executable and cwd its confirmation opened, and the check that
/// they are still what was confirmed.
pub trait Opened: Send + Sync {
    fn executable(&self) -> &File;
    fn cwd(&self) -> &File;
    /// The program a terminal runs (a launch that names one), opened, and its path as the launch
    /// names it.
    fn program(&self) -> Option<(&File, &Path)> {
        None
    }
    /// Refused once the executable or cwd changed since it was confirmed (replaced, or written
    /// in place).
    fn recheck(&self) -> Result<(), String>;
    /// Whether a detached launch shows no window itself, so it starts with no console on Windows
    /// (`confirmation::Authorized::windowless`).
    #[cfg_attr(not(windows), allow(dead_code))]
    fn windowless(&self) -> bool {
        false
    }
}

#[cfg(native_stack)]
impl Opened for crate::confirmation::Authorized {
    fn executable(&self) -> &File {
        crate::confirmation::Authorized::executable(self).expect("a launch's executable")
    }
    fn cwd(&self) -> &File {
        crate::confirmation::Authorized::cwd(self).expect("a launch's cwd")
    }
    fn program(&self) -> Option<(&File, &Path)> {
        crate::confirmation::Authorized::program(self)
    }
    fn recheck(&self) -> Result<(), String> {
        crate::confirmation::Authorized::recheck(self)
    }
    fn windowless(&self) -> bool {
        crate::confirmation::Authorized::windowless(self)
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
    /// The vault secret ID whose value it gets on its stdin (`Request::stdin`).
    pub stdin: Option<String>,
}

/// Whether f is a script (`#!`): its interpreter opens it again by its descriptor's path, so the
/// descriptor must stay open across exec.
#[cfg(target_os = "linux")]
fn is_script(f: &File) -> bool {
    let mut b = [0u8; 2];
    f.read_at(&mut b, 0).ok() == Some(2) && &b == b"#!"
}

/// The command that runs launch l with environment env: managed (in a process group of its own,
/// on Linux SIGTERM when its parent dies) or detached (a wrapper that starts it in a session of its
/// own and exits). With secrets given, its core file size limits are 1 (soft and hard), inherited
/// by its descendants: no core file and no core dump handler; otherwise it gets the app's starting
/// limits back.
/// hold, for a detached launch on Linux: the pipes that hold its wrapper before it forks the
/// launch, until the app has moved it into a scope of its own (`Hold`).
/// On Linux it runs the opened file itself (`/proc/self/fd`); on macOS the path that file has now,
/// once no other user but a member of `admin` can change it (`owned::exec_target`,
/// `owned::Writers::Admins`). The program a terminal launch names is checked too, since the
/// terminal starts it after this: on Linux the terminal finds it by the path the launch names,
/// which no other user may be able to change or redirect (`owned::path_target`); on macOS the
/// execution program in Terminal's window runs it from the file checked here, which it gets as
/// descriptor 3, by the path that file has then, checked again there under the same rule.
#[cfg(unix)]
fn command(
    l: &Launch,
    env: Vec<(String, OsString)>,
    holds_secrets: bool,
    hold: Option<[i32; 4]>,
) -> Result<Command, String> {
    let exe = l.opened.executable();
    #[cfg(target_os = "linux")]
    let (exe_fd, script) = (exe.as_raw_fd(), is_script(exe));
    #[cfg(not(target_os = "linux"))]
    let _ = hold;
    let cwd_fd = l.opened.cwd().as_raw_fd();
    let detached = l.lifetime == Lifetime::Detached;
    // 1, not 0: with 0 the kernel still runs a `|` core dump handler (systemd-coredump), which
    // records the process's environment, the secret included, in the journal; with 1 it runs
    // none and writes no file.
    let core = if holds_secrets {
        libc::rlimit {
            rlim_cur: 1,
            rlim_max: 1,
        }
    } else {
        snapshot().core
    };
    let mut cmd = Command::new(crate::owned::exec_target(
        exe,
        crate::owned::Writers::Admins,
    )?);
    #[cfg(target_os = "macos")]
    let mut program_fd = None;
    if let Some((program, named)) = l.opened.program() {
        #[cfg(target_os = "linux")]
        crate::owned::path_target(program, named)?;
        #[cfg(target_os = "macos")]
        {
            let _ = named;
            crate::owned::exec_target(program, crate::owned::Writers::Admins)?;
            program_fd = Some(program.as_raw_fd());
        }
    }
    cmd.arg0(&l.named)
        .args(&l.argv)
        .env_clear()
        .envs(env)
        .stdin(if l.stdin.is_some() {
            Stdio::piped()
        } else {
            Stdio::null()
        })
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
                // Held until the app moved the wrapper into a scope of its own (or gave up), so
                // the launch and everything it starts is in that scope from its fork on.
                #[cfg(target_os = "linux")]
                if let Some([pid_r, pid_w, go_r, go_w]) = hold {
                    libc::close(pid_r);
                    libc::close(go_w);
                    let me = libc::getpid().to_ne_bytes();
                    libc::write(pid_w, me.as_ptr().cast(), me.len());
                    libc::close(pid_w);
                    let mut b = 0u8;
                    while libc::read(go_r, (&mut b as *mut u8).cast(), 1) == -1
                        && *libc::__errno_location() == libc::EINTR
                    {}
                    libc::close(go_r);
                }
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
                #[cfg(target_os = "linux")]
                if libc::prctl(libc::PR_SET_PDEATHSIG, 0) != 0 {
                    return Err(err());
                }
            } else {
                // If the app dies without stopping it, it gets SIGTERM (Linux only: macOS has no
                // parent-death signal).
                #[cfg(target_os = "linux")]
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
            crate::owned::close_on_exec_from_3()?;
            #[cfg(target_os = "linux")]
            if script && libc::fcntl(exe_fd, libc::F_SETFD, 0) != 0 {
                return Err(err());
            }
            // But the program a terminal runs, as 3 (`dup2` onto itself keeps close-on-exec).
            #[cfg(target_os = "macos")]
            if let Some(fd) = program_fd {
                if libc::dup2(fd, 3) == -1 || libc::fcntl(3, libc::F_SETFD, 0) != 0 {
                    return Err(err());
                }
            }
            Ok(())
        });
    }
    Ok(cmd)
}

/// The command that runs launch l with environment env on Windows (`owned::windows`): the opened
/// executable and cwd pinned by the paths they have now; managed in a job of its own, detached
/// outside every job of the app's.
#[cfg(windows)]
fn command(
    l: &Launch,
    env: Vec<(String, OsString)>,
    holds_secrets: bool,
) -> Result<crate::owned::windows::Command, String> {
    use crate::owned::windows::{self as w, Io, Kind};
    let kind = match l.lifetime {
        Lifetime::Detached => Kind::Detached,
        Lifetime::Managed => Kind::Managed,
    };
    let mut cmd = w::Command::new(w::pin_file(l.opened.executable())?, kind);
    cmd.cwd(w::pin_dir(l.opened.cwd())?)
        .arg0(&l.named)
        .args(&l.argv)
        .envs(env)
        .stdin(if l.stdin.is_some() {
            Io::Piped
        } else {
            Io::Null
        })
        .secrets(holds_secrets)
        .windowless(l.lifetime == Lifetime::Detached && l.opened.windowless());
    Ok(cmd)
}

/// A launch's secret as the host gave it: its vault secret ID, and the connection revision its
/// check held on (a client key's; none for a plain secret).
pub type Given = (String, Option<u64>);

/// How the host gives a launch's process its secrets (`client_keys::Launches`).
pub trait Values: Send + Sync {
    /// Vault secret id's value for package's launch, checked now, and the connection revision its
    /// check held on; or why it is refused.
    fn value(&self, package: &str, id: &str) -> Result<(String, Option<u64>), String>;
    /// Checked once more right before the spawn, after every secret of the launch resolved
    /// (given): refused once one left the vault, or the connection changed since a client key's
    /// check.
    fn still(&self, given: &[Given]) -> Result<(), String>;
}

/// A launch's secrets, resolved: each variable's value, its stdin's, and what `Values::still`
/// checks again.
struct Resolved {
    env: Vec<(String, String)>,
    stdin: Option<String>,
    given: Vec<Given>,
}

impl Resolved {
    /// Whether the process gets a secret's value.
    fn holds(&self) -> bool {
        !self.env.is_empty() || self.stdin.is_some()
    }
}

/// Resolves l's secrets, its environment's and its stdin's, for package with values now: the
/// launch is refused when one is.
fn resolve(l: &Launch, package: &str, values: &dyn Values) -> Result<Resolved, String> {
    let mut given = Vec::new();
    let mut one = |id: &str, what: &str| {
        let (v, revision) = values
            .value(package, id)
            .map_err(|e| format!("the secret for {what} is refused: {e}"))?;
        given.push((id.to_string(), revision));
        Ok::<_, String>(v)
    };
    let env = l
        .secrets
        .iter()
        .map(|(name, id)| one(id, name).map(|v| (name.clone(), v)))
        .collect::<Result<Vec<_>, _>>()?;
    let stdin = l
        .stdin
        .as_deref()
        .map(|id| one(id, "its stdin"))
        .transpose()?;
    Ok(Resolved { env, stdin, given })
}

/// Writes a launch's stdin secret to its stdin pipe and closes it. On Linux the value fits the
/// pipe's buffer (at most `MAX_VALUE`), so this never waits for the launch to read it; elsewhere,
/// where a pipe's buffer may be smaller, it is written on a thread of its own.
fn feed(stdin: Option<impl std::io::Write + Send + 'static>, value: Option<String>) {
    if let (Some(mut pipe), Some(v)) = (stdin, value) {
        let mut write = move || {
            let _ = pipe.write_all(v.as_bytes());
        };
        if cfg!(target_os = "linux") {
            write();
        } else {
            let _ = std::thread::Builder::new()
                .name("pluxy-launch-stdin".into())
                .spawn(write);
        }
    }
}

/// Starts package's detached launch l, with its secrets' values resolved by values now and
/// checked once more right before the spawn; returns once its wrapper has been reaped. With a
/// systemd user manager on the session bus (Linux), the launch runs in a transient scope of its
/// own (`scope`).
#[cfg(unix)]
pub fn detached(l: &Launch, package: &str, values: &dyn Values) -> Result<(), String> {
    l.opened.recheck()?;
    let r = resolve(l, package, values)?;
    let env = environment(snapshot(), &l.request, &r.env);
    #[cfg(target_os = "linux")]
    let hold = Hold::new().ok();
    #[cfg(target_os = "linux")]
    let fds = hold.as_ref().map(Hold::fds);
    #[cfg(not(target_os = "linux"))]
    let fds = None;
    let mut cmd = command(l, env, r.holds(), fds)?;
    #[cfg(target_os = "linux")]
    let mover = hold.as_ref().map(Hold::mover).transpose()?;
    let spawned = values.still(&r.given).and_then(|()| {
        cmd.spawn()
            .map_err(|e| format!("the launch did not start: {e}"))
    });
    drop(cmd);
    // The wrapper has its copies (or never started): without the app's, the mover sees the end.
    #[cfg(target_os = "linux")]
    {
        drop(hold);
        if let Some(m) = mover {
            let _ = m.join();
        }
    }
    let mut wrapper = spawned?;
    feed(wrapper.stdin.take(), r.stdin);
    wrapper.wait().map_err(|e| e.to_string())?;
    Ok(())
}

/// Starts package's detached launch l on Windows: outside every job of the app's, or refused
/// before it runs when the app's job does not allow that (`owned::windows::NO_BREAKAWAY`); the
/// host keeps nothing of it.
#[cfg(windows)]
pub fn detached(l: &Launch, package: &str, values: &dyn Values) -> Result<(), String> {
    l.opened.recheck()?;
    let r = resolve(l, package, values)?;
    let env = environment(snapshot(), &l.request, &r.env);
    let mut cmd = command(l, env, r.holds())?;
    // Checked again now its executable and cwd are pinned (`command`): nothing can change them
    // after this, and a change made since the check above is seen here.
    l.opened.recheck()?;
    values.still(&r.given)?;
    let stdin = cmd
        .spawn_detached()
        .map_err(|e| format!("the launch did not start: {e}"))?;
    feed(stdin, r.stdin);
    Ok(())
}

/// The pipes that hold a detached launch's wrapper (`command`): it writes its process ID to the
/// first, then waits on the second until the app has moved it into a scope of its own (`scope`)
/// or given up. Every end is close-on-exec; the wrapper closes those it does not use.
#[cfg(target_os = "linux")]
struct Hold {
    pid: (std::os::fd::OwnedFd, std::os::fd::OwnedFd),
    go: (std::os::fd::OwnedFd, Option<std::os::fd::OwnedFd>),
}

#[cfg(target_os = "linux")]
fn pipe() -> io::Result<(std::os::fd::OwnedFd, std::os::fd::OwnedFd)> {
    use std::os::fd::FromRawFd;
    let mut fds = [0i32; 2];
    if unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(unsafe {
        (
            std::os::fd::OwnedFd::from_raw_fd(fds[0]),
            std::os::fd::OwnedFd::from_raw_fd(fds[1]),
        )
    })
}

#[cfg(target_os = "linux")]
impl Hold {
    fn new() -> io::Result<Hold> {
        let pid = pipe()?;
        let (go_r, go_w) = pipe()?;
        Ok(Hold {
            pid,
            go: (go_r, Some(go_w)),
        })
    }

    /// pid read, pid write, go read, go write.
    fn fds(&self) -> [i32; 4] {
        [
            self.pid.0.as_raw_fd(),
            self.pid.1.as_raw_fd(),
            self.go.0.as_raw_fd(),
            self.go.1.as_ref().map_or(-1, |f| f.as_raw_fd()),
        ]
    }

    /// The thread that reads the wrapper's process ID, moves it into a scope and lets it go on.
    fn mover(&self) -> Result<std::thread::JoinHandle<()>, String> {
        let dup = |f: &std::os::fd::OwnedFd| f.try_clone().map_err(|e| e.to_string());
        let pid_r = File::from(dup(&self.pid.0)?);
        let go_w = File::from(dup(self.go.1.as_ref().ok_or("no go pipe")?)?);
        std::thread::Builder::new()
            .name("pluxy-launch-scope".into())
            .spawn(move || {
                use std::io::{Read, Write};
                let (mut pid_r, mut go_w) = (pid_r, go_w);
                let mut b = [0u8; 4];
                if pid_r.read_exact(&mut b).is_ok() {
                    let pid = i32::from_ne_bytes(b);
                    if let Err(e) = scope::enter(pid as u32) {
                        scope::report(&e);
                    }
                    let _ = go_w.write_all(&[1]);
                }
            })
            .map_err(|e| e.to_string())
    }
}

/// Transient systemd scopes for detached launches. `setsid` leaves a launch in the app's cgroup,
/// where stopping a unit that runs the app (its default `KillMode`, `ExitType=cgroup`) or
/// `systemd-oomd` acting on it would end it too. With a systemd user manager on the session bus,
/// the host asks it (`StartTransientUnit`) for a scope `app-pluxy-launch-<hex>.scope` holding
/// the launch's wrapper before it forks the launch, and waits until the wrapper's cgroup is that
/// scope. With no user manager, or a refusal (the app runs in a cgroup the user manager does not
/// own), the launch runs in the app's cgroup as before.
#[cfg(target_os = "linux")]
mod scope {
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::time::{Duration, Instant};

    /// How long the host waits for the user manager, and then for the move.
    const WAIT: Duration = Duration::from_secs(2);

    pub enum Failed {
        /// No user manager on the session bus (or no session bus).
        Absent(String),
        Refused(String),
    }

    /// f with a cancellable that is cancelled once wait passes: a session bus that accepts but
    /// never answers holds a launch no longer than that.
    pub fn within<T>(
        wait: Duration,
        f: impl FnOnce(&gtk::gio::Cancellable) -> Result<T, gtk::glib::Error>,
    ) -> Result<T, gtk::glib::Error> {
        use gtk::gio::prelude::CancellableExt;
        let cancel = gtk::gio::Cancellable::new();
        let (done, rx) = std::sync::mpsc::channel::<()>();
        let c = cancel.clone();
        std::thread::spawn(move || {
            if rx.recv_timeout(wait) == Err(std::sync::mpsc::RecvTimeoutError::Timeout) {
                c.cancel();
            }
        });
        let r = f(&cancel);
        let _ = done.send(());
        r
    }

    /// Moves process pid into a new transient scope of its own; the scope's name.
    pub fn enter(pid: u32) -> Result<String, Failed> {
        use gtk::gio;
        use gtk::glib::{ToVariant, Variant, VariantTy};
        let conn = within(WAIT, |c| gio::bus_get_sync(gio::BusType::Session, Some(c)))
            .map_err(|e| Failed::Absent(e.to_string()))?;
        let mut b = [0u8; 8];
        getrandom::fill(&mut b).map_err(|e| Failed::Refused(e.to_string()))?;
        let name = format!(
            "app-pluxy-launch-{}.scope",
            b.iter().map(|x| format!("{x:02x}")).collect::<String>()
        );
        let props: Vec<(String, Variant)> = vec![
            ("PIDs".into(), vec![pid].to_variant()),
            ("CollectMode".into(), "inactive-or-failed".to_variant()),
            ("Description".into(), "pluxy launch".to_variant()),
        ];
        let aux: Vec<(String, Vec<(String, Variant)>)> = Vec::new();
        let params = (name.as_str(), "fail", props, aux).to_variant();
        let reply = VariantTy::new("(o)").map_err(|e| Failed::Refused(e.to_string()))?;
        conn.call_sync(
            Some("org.freedesktop.systemd1"),
            "/org/freedesktop/systemd1",
            "org.freedesktop.systemd1.Manager",
            "StartTransientUnit",
            Some(&params),
            Some(reply),
            gio::DBusCallFlags::NO_AUTO_START,
            WAIT.as_millis() as i32,
            gio::Cancellable::NONE,
        )
        .map_err(|e| {
            let text = e.to_string();
            if text.contains("ServiceUnknown") || text.contains("NameHasNoOwner") {
                Failed::Absent(text)
            } else {
                Failed::Refused(text)
            }
        })?;
        let deadline = Instant::now() + WAIT;
        loop {
            if std::fs::read_to_string(format!("/proc/{pid}/cgroup"))
                .is_ok_and(|c| c.lines().any(|l| l.ends_with(&format!("/{name}"))))
            {
                return Ok(name);
            }
            if Instant::now() > deadline {
                return Err(Failed::Refused(format!(
                    "{name} was started but the launch is not in it"
                )));
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    /// Whether the session bus has a systemd user manager (for the tests).
    #[cfg(test)]
    pub fn available() -> bool {
        use gtk::gio;
        use gtk::glib::ToVariant;
        within(WAIT, |c| gio::bus_get_sync(gio::BusType::Session, Some(c)))
            .and_then(|c| {
                c.call_sync(
                    Some("org.freedesktop.DBus"),
                    "/org/freedesktop/DBus",
                    "org.freedesktop.DBus",
                    "NameHasOwner",
                    Some(&("org.freedesktop.systemd1",).to_variant()),
                    None,
                    gio::DBusCallFlags::NONE,
                    2000,
                    gio::Cancellable::NONE,
                )
            })
            .ok()
            .and_then(|v| v.child_value(0).get::<bool>())
            .unwrap_or(false)
    }

    /// Logs why a launch runs in the app's cgroup: a missing user manager once, a refusal each
    /// time.
    pub fn report(e: &Failed) {
        static ABSENT: AtomicBool = AtomicBool::new(false);
        match e {
            Failed::Absent(why) => {
                if !ABSENT.swap(true, Ordering::SeqCst) {
                    eprintln!("pluxy-app: no systemd user manager ({why}): detached launches run in the app's cgroup");
                }
            }
            Failed::Refused(why) => {
                eprintln!("pluxy-app: a detached launch runs in the app's cgroup: {why}")
            }
        }
    }
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
    /// Its package was removed: the entry goes once it has been reaped and is not being started.
    removed: bool,
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

/// The runtimes, and the packages closed to them: disabled (false) or removed (true). A closed
/// package's runtimes are stopped and their launches dropped, and none starts for it until it is
/// enabled again (`Runtimes::reopen`).
#[derive(Default)]
struct Registry {
    entries: Vec<Entry>,
    closed: BTreeMap<String, bool>,
}

impl std::ops::Deref for Registry {
    type Target = Vec<Entry>;
    fn deref(&self) -> &Vec<Entry> {
        &self.entries
    }
}

impl std::ops::DerefMut for Registry {
    fn deref_mut(&mut self) -> &mut Vec<Entry> {
        &mut self.entries
    }
}

struct Shared {
    list: Mutex<Registry>,
    changed: Condvar,
    file: Option<PathBuf>,
    values: Box<dyn Values>,
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
    /// reported as unknown. values resolves a package's launch's secrets at each start and
    /// restart, refusing one the package may not be given now, and checks them again right
    /// before the spawn.
    pub fn open(file: Option<PathBuf>, values: Box<dyn Values>) -> Runtimes {
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
                        removed: false,
                        exit: None,
                        launch: None,
                    });
                }
            }
        }
        let r = Runtimes(Arc::new(Shared {
            list: Mutex::new(Registry {
                entries: list,
                closed: BTreeMap::new(),
            }),
            changed: Condvar::new(),
            file,
            values,
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
        let id = random_id();
        let launch = Arc::new(l);
        {
            let mut list = self.0.list.lock().unwrap();
            self.0.open(&list, package)?;
            // Runtimes left by an earlier run of the app (unknown) take no room.
            let mine = list
                .iter()
                .filter(|e| e.package == package && e.state != State::Unknown)
                .count();
            if mine >= MAX_RUNTIMES {
                // Only an ended one nothing is restarting makes room.
                let oldest = list
                    .iter()
                    .position(|e| e.package == package && e.state == State::Exited && !e.busy)
                    .ok_or(format!(
                        "{package} has {MAX_RUNTIMES} runtimes running already"
                    ))?;
                list.remove(oldest);
            }
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
                removed: false,
                exit: None,
                launch: Some(Arc::clone(&launch)),
            });
        }
        let r = self.run(package, &id, &launch);
        let mut list = self.0.list.lock().unwrap();
        let started = r.and_then(|()| {
            list.iter_mut()
                .find(|e| e.id == id)
                .ok_or_else(|| format!("{package}'s runtime was retired as it started"))
        });
        match started {
            Ok(e) => {
                e.busy = false;
                let v = e.view();
                // Its package was removed as it started, and it was reaped meanwhile.
                if e.removed && e.state == State::Exited {
                    list.retain(|x| x.id != id);
                }
                self.0.changed.notify_all();
                Ok(json!({"lifetime": "managed", "runtime": id, "pid": v["pid"]}))
            }
            Err(e) => {
                // Not started, or stopped again as it started: no entry is left for it once
                // its supervisor has reaped it.
                if let Some(x) = list.iter_mut().find(|x| x.id == id) {
                    x.busy = false;
                    x.launch = None;
                    if x.state == State::Exited {
                        list.retain(|x| x.id != id);
                    }
                }
                Err(e)
            }
        }
    }

    /// Starts runtime id's launch on a supervisor thread of its own, which stays until it has
    /// been reaped; returns once it runs (the entry is Running with its process ID).
    /// It is refused when package is closed, the entry's launch was dropped (by a close, even if
    /// the package was opened again since) or the app is quitting: checked before its secrets are
    /// resolved, and again under the registry's lock as it is spawned, so a close or the app's quit
    /// either comes first and nothing starts, or finds it running and stops it. It is refused too
    /// when a secret it gets is refused, as it resolves or right before the spawn
    /// (`Values::still`, just before the registry's lock is taken).
    fn run(&self, package: &str, id: &str, l: &Arc<Launch>) -> Result<(), String> {
        l.opened.recheck().map_err(|e| {
            format!("{e}; its executable or cwd changed since it was confirmed: launch it again")
        })?;
        self.0.live(&self.0.list.lock().unwrap(), package, id)?;
        let r = resolve(l, package, &*self.0.values)?;
        let env = environment(snapshot(), &l.request, &r.env);
        let token = {
            let mut t = self.0.tokens.lock().unwrap();
            *t += 1;
            *t
        };
        let (tx, rx) = std::sync::mpsc::channel();
        let (shared, launch, rid) = (Arc::clone(&self.0), Arc::clone(l), id.to_string());
        let package = package.to_string();
        let holds = r.holds();
        let (given, stdin) = (r.given, r.stdin);
        drop(r.env);
        std::thread::Builder::new()
            .name("pluxy-runtime".into())
            .spawn(move || {
                // Its secrets checked once more just before the registry's lock is taken for the
                // spawn, not under it: the connection's lock, which the check takes, may be held
                // for seconds while a core starts or stops, and the registry's lock must not wait
                // for it.
                if let Err(e) = shared.values.still(&given) {
                    let _ = tx.send(Err(e));
                    return;
                }
                // Spawned under the registry's lock, which a close and the app's quit take too.
                let mut list = shared.list.lock().unwrap();
                if let Err(e) = shared.live(&list, &package, &rid) {
                    drop(list);
                    let _ = tx.send(Err(e));
                    return;
                }
                #[cfg(unix)]
                let cmd = command(&launch, env, holds, None);
                // On Windows checked again once its executable and cwd are pinned (`command`):
                // nothing can change them after this.
                #[cfg(windows)]
                let cmd = command(&launch, env, holds).and_then(|c| {
                    launch.opened.recheck().map_err(|e| {
                        format!("{e}; its executable or cwd changed since it was confirmed: launch it again")
                    })?;
                    Ok(c)
                });
                let child = cmd.and_then(|mut cmd| cmd.spawn().map_err(|e| e.to_string()));
                match child {
                    Ok(mut c) => {
                        if let Some(e) = list.iter_mut().find(|e| e.id == rid) {
                            e.state = State::Running;
                            e.pid = c.id();
                            e.started = now_ms();
                            e.token = token;
                            e.stop = false;
                            e.exit = None;
                        }
                        shared.save(&list);
                        // Its stdin secret, still under the lock: only a launch that may run gets
                        // it, and it never waits (`feed`).
                        feed(c.stdin.take(), stdin);
                        drop(list);
                        let _ = tx.send(Ok(()));
                        supervise(&shared, &rid, token, c);
                    }
                    Err(e) => {
                        drop(list);
                        let _ = tx.send(Err(format!("the runtime did not start: {e}")));
                    }
                }
            })
            .map_err(|e| e.to_string())?;
        rx.recv()
            .unwrap_or_else(|_| Err("the runtime's supervisor stopped".into()))
    }

    fn find<'a>(
        list: &'a mut MutexGuard<'_, Registry>,
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
    /// or cwd changed since it was confirmed or a secret it gets is refused now (removed, or a
    /// client key that no longer passes its checks); its status.
    pub fn restart(&self, package: &str, id: &str) -> Result<Value, String> {
        let launch = {
            let mut list = self.0.list.lock().unwrap();
            let e = Self::find(&mut list, package, id)?;
            let unknown = e.state == State::Unknown;
            let l = e.launch.clone().ok_or_else(|| if unknown {
                format!("{id} was started before the app last stopped without stopping it; the app no longer controls it")
            } else {
                format!("{id}'s launch was retired when {package} was disabled or removed, or it failed to start: launch it again")
            })?;
            if e.busy {
                return Err(format!("{id} is being started or restarted"));
            }
            e.busy = true;
            l
        };
        let r = self
            .end(package, id)
            .and_then(|_| self.run(package, id, &launch));
        let mut list = self.0.list.lock().unwrap();
        let e = Self::find(&mut list, package, id)?;
        e.busy = false;
        if r.is_ok() {
            e.restarts += 1;
        }
        let v = e.view();
        // Its package was removed as it restarted, and it is not running: nothing of it stays, as
        // after a reap.
        if e.removed && e.state == State::Exited {
            list.retain(|x| x.id != id);
        }
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

    /// Closes package: it was disabled, or removed. Its runtimes are told to stop (their
    /// supervisors end them within the stop grace; this does not wait) and their launches are
    /// dropped, so none can be restarted, even once it is enabled again; a removed package's
    /// entries go once they have been reaped. None starts for it until `reopen`.
    pub fn close(&self, package: &str, removed: bool) {
        let mut list = self.0.list.lock().unwrap();
        list.closed.insert(package.to_string(), removed);
        for e in list.iter_mut().filter(|e| e.package == package) {
            e.launch = None;
            e.removed |= removed;
            if e.state == State::Running {
                e.stop = true;
                e.state = State::Stopping;
            }
        }
        if removed {
            list.retain(|e| {
                e.package != package
                    || matches!(e.state, State::Running | State::Stopping)
                    || e.busy
            });
        }
        self.0.save(&list);
        self.0.changed.notify_all();
    }

    /// Opens package again: it is enabled. Its earlier launches stay dropped.
    pub fn reopen(&self, package: &str) {
        self.0.list.lock().unwrap().closed.remove(package);
    }

    /// Stops every runtime the app owns, at its quit; none starts after. Detached launches are
    /// not the app's and go on.
    pub fn stop_all(&self) {
        // Under the registry's lock, which every spawn holds: a start or restart under way either
        // spawns before this, and runs to be stopped below, or is refused after it.
        let list = self.0.list.lock().unwrap();
        self.0.quitting.store(true, Ordering::SeqCst);
        drop(list);
        self.stop_where(|_| true);
    }
}

impl Shared {
    /// Refused when package is closed or the app is quitting.
    fn open(&self, list: &Registry, package: &str) -> Result<(), String> {
        if self.quitting.load(Ordering::SeqCst) {
            return Err("the app is quitting".into());
        }
        if list.closed.contains_key(package) {
            return Err(format!(
                "{package} was disabled or removed: none of its runtimes starts"
            ));
        }
        Ok(())
    }

    /// Refused unless runtime id of package may start now: package open, the app not quitting, and
    /// the entry still holding its launch (a close drops it, even if the package is opened again).
    fn live(&self, list: &Registry, package: &str, id: &str) -> Result<(), String> {
        self.open(list, package)?;
        if !list.iter().any(|e| e.id == id && e.launch.is_some()) {
            return Err(format!(
                "{package}'s runtime {id} was retired when {package} was disabled or removed: launch it again"
            ));
        }
        Ok(())
    }

    /// Writes the runtimes running (or stopping) to the registry's file, readable only by this
    /// user and replaced in one step (`managed::write_private`: 0600 on Linux and macOS, a
    /// protected access list of this user and SYSTEM on Windows).
    fn save(&self, list: &[Entry]) {
        let Some(f) = &self.file else {
            return;
        };
        let running: Vec<Value> = list
            .iter()
            .filter(|e| matches!(e.state, State::Running | State::Stopping))
            .map(Entry::view)
            .collect();
        let written = crate::managed::write_private(f, json!(running).to_string().as_bytes());
        if let Err(e) = written {
            eprintln!("pluxy-app: could not record the managed runtimes: {e}");
        }
    }
}

/// The supervisor: waits for runtime id's leader (started as token) to exit or be asked to stop;
/// on a stop, SIGTERM to its group (on Windows CTRL_BREAK) and SIGKILL after the grace (on Windows
/// at once when it cannot get CTRL_BREAK); then SIGKILL to what is left of its group, while the
/// leader is not reaped (its group's number cannot be reused until then; on Windows its job), and
/// the reap (`owned`).
fn supervise(shared: &Shared, id: &str, token: u64, mut child: Child) {
    let mut term: Option<Instant> = None;
    loop {
        if leader(&child) != Leader::Running {
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
            if !term_group(&child) {
                break;
            }
            term = Some(Instant::now());
        }
        if term.is_some_and(|t| t.elapsed() >= STOP) {
            break;
        }
        std::thread::sleep(TICK);
    }
    kill_group(&mut child);
    let status = child
        .wait()
        .map(|s| s.to_string())
        .unwrap_or_else(|e| e.to_string());
    let mut list = shared.list.lock().unwrap();
    let mut gone = false;
    if let Some(e) = list.iter_mut().find(|e| e.id == id && e.token == token) {
        e.state = State::Exited;
        e.stop = false;
        e.exit = Some(status);
        gone = !e.busy && e.removed;
    }
    // Its package was removed: nothing of it stays.
    if gone {
        list.retain(|e| e.id != id);
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
