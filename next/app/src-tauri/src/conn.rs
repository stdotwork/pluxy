//! How the app reaches core, in one of three modes:
//!
//! - **managed**: the app runs a core of its own (`managed.rs`) and stops it when it quits;
//! - **local**: a core already running on this computer, such as a system service; the app only
//!   connects, never starts or stops it;
//! - **remote**: a core on another computer, over https only, shown with its address.
//!
//! The management key stays here: the window gets a view of the connection without it.
//! Nothing in this module needs Tauri, so the tests drive it against a real core.

use crate::client::{certificates, Client, Reply, ROTATE};
use crate::managed::{core_program, write_private, ManagedCore, OnExit};
use crate::recovery::Recovery;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::fs;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::{Duration, Instant};
use url::{Host, Url};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Mode {
    Managed,
    Local,
    Remote,
}

/// What the app keeps in its config folder, `connection.json`, readable only by this user.
#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Settings {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mode: Option<Mode>,
    /// The core's origin, in local and remote mode.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    /// The management key, in local and remote mode, when the person chose to keep it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key: Option<String>,
    /// A PEM file of certificates to trust for a remote core, instead of the public roots.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ca_file: Option<String>,
    /// The core program, in managed mode; unset: `pluxy` beside the app.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub core_path: Option<String>,
}

impl Settings {
    /// The settings at path; none when the file is missing. A file the app cannot read is an
    /// error, so a broken file is never overwritten unseen.
    pub fn load(path: &Path) -> Result<Settings, String> {
        match fs::read(path) {
            Ok(b) => serde_json::from_slice(&b).map_err(|e| format!("{}: {e}", path.display())),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Settings::default()),
            Err(e) => Err(format!("{}: {e}", path.display())),
        }
    }

    /// Removes what a crash during a save of path left beside it: a regular file named as
    /// `write_private` names its temporary files, `<file>.<64 lowercase hex digits>.tmp`. Nothing
    /// else goes: not another name, a folder or a link (a link is never followed or removed).
    /// Best effort, and only for the app's start, before anything saves: a save in progress
    /// must not lose its file.
    pub fn remove_stale_temps(path: &Path) {
        let (Some(dir), Some(name)) = (path.parent(), path.file_name().and_then(|n| n.to_str()))
        else {
            return;
        };
        let Ok(entries) = fs::read_dir(dir) else {
            return;
        };
        for entry in entries.flatten() {
            let file = entry.file_name();
            let Some(nonce) = file
                .to_str()
                .and_then(|f| f.strip_prefix(name))
                .and_then(|f| f.strip_prefix('.'))
                .and_then(|f| f.strip_suffix(".tmp"))
            else {
                continue;
            };
            let is_nonce = nonce.len() == 64
                && nonce
                    .bytes()
                    .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b));
            let is_file = fs::symlink_metadata(entry.path()).is_ok_and(|m| m.file_type().is_file());
            if is_nonce && is_file {
                let _ = fs::remove_file(entry.path());
            }
        }
    }

    pub fn save(&self, path: &Path) -> Result<(), String> {
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        }
        let text = serde_json::to_vec_pretty(self).expect("settings are JSON");
        write_private(path, &text).map_err(|e| format!("saving {}: {e}", path.display()))
    }
}

/// What the person asks for on the Connection page.
#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Change {
    pub mode: Option<Mode>,
    pub url: Option<String>,
    pub ca_file: Option<String>,
    pub core_path: Option<String>,
}

/// The connection as the window sees it: never the key.
#[derive(Clone, Debug, Default, Serialize)]
pub struct View {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mode: Option<Mode>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target: Option<String>,
    pub signed_in: bool,
    pub remembered: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ca_file: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub core_path: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub core: Option<CoreView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    /// Taken under the connection lock: a later view has a higher version, so the tray can
    /// drop a view that reaches it after a newer one.
    #[serde(skip)]
    pub version: u64,
}

/// Two views are equal when they show the same connection: `version` only orders them.
impl PartialEq for View {
    fn eq(&self, other: &Self) -> bool {
        let View {
            mode,
            target,
            signed_in,
            remembered,
            url,
            ca_file,
            core_path,
            core,
            error,
            version: _,
        } = self;
        (
            mode, target, signed_in, remembered, url, ca_file, core_path, core, error,
        ) == (
            &other.mode,
            &other.target,
            &other.signed_in,
            &other.remembered,
            &other.url,
            &other.ca_file,
            &other.core_path,
            &other.core,
            &other.error,
        )
    }
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct CoreView {
    pub state: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pid: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

fn parse_origin(s: &str) -> Result<Url, String> {
    let u = Url::parse(s.trim()).map_err(|e| format!("{s:?} is not an address: {e}"))?;
    if u.scheme() != "http" && u.scheme() != "https" {
        return Err(format!("{s:?} is not an http or https address"));
    }
    if u.host().is_none() {
        return Err(format!("{s:?} names no host"));
    }
    if !u.username().is_empty() || u.password().is_some() {
        return Err("the address must not hold a user name or password".into());
    }
    if (u.path() != "/" && !u.path().is_empty()) || u.query().is_some() || u.fragment().is_some() {
        return Err(format!(
            "{s:?} must be only a scheme, host and port, such as http://127.0.0.1:8400"
        ));
    }
    Ok(u)
}

fn origin_of(u: &Url) -> String {
    u.origin().ascii_serialization()
}

/// Whether u's host is on this computer.
pub fn is_loopback(u: &Url) -> bool {
    match u.host() {
        Some(Host::Ipv4(ip)) => IpAddr::V4(ip).is_loopback(),
        Some(Host::Ipv6(ip)) => IpAddr::V6(ip).is_loopback(),
        Some(Host::Domain(d)) => d.eq_ignore_ascii_case("localhost"),
        None => false,
    }
}

/// The origin of a core on this computer: http or https on a loopback address.
pub fn local_origin(s: &str) -> Result<String, String> {
    let u = parse_origin(s)?;
    if !is_loopback(&u) {
        return Err(format!(
            "{} is not on this computer: use remote mode for another computer",
            origin_of(&u)
        ));
    }
    Ok(origin_of(&u))
}

/// The origin of a core on another computer: https only, so the key never travels in the clear.
pub fn remote_origin(s: &str) -> Result<String, String> {
    let u = parse_origin(s)?;
    if u.scheme() != "https" {
        return Err("a remote core must be reached over https".into());
    }
    Ok(origin_of(&u))
}

/// Where the app keeps its files.
#[derive(Clone, Debug)]
pub struct Dirs {
    /// `connection.json`.
    pub config: PathBuf,
    /// The managed core's folder: its config, credentials and packages.
    pub core: PathBuf,
}

impl Dirs {
    pub fn settings(&self) -> PathBuf {
        self.config.join("connection.json")
    }
}

/// The app's connection to core.
pub struct Connection {
    dirs: Dirs,
    revision: u64,
    settings: Settings,
    client: Option<Client>,
    key: Option<String>,
    managed: Option<ManagedCore>,
    on_core_exit: Option<OnExit>,
    recovery: Recovery,
    error: Option<String>,
    views: u64,
}

/// Validated native destination, frozen before asking for a secret.
pub(crate) struct KeyTarget {
    settings: Settings,
    client: Client,
    revision: u64,
    pub summary: String,
}

/// A rotation the person is asked to confirm, frozen to the connection it was prepared on.
pub(crate) struct RotationTarget {
    revision: u64,
    /// What the native confirmation says: the core, and how the new key will be kept.
    pub summary: String,
}

impl RotationTarget {
    pub(crate) fn revision(&self) -> u64 {
        self.revision
    }
}

/// How the app keeps a rotated key: as the old one was kept.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Kept {
    /// In `connection.json`, as the person chose for the old key.
    Device,
    /// Not kept: it lasts until the app quits.
    Session,
    /// In the config file of the app's own core, which core saved before replying.
    Core,
    /// It should have been saved in `connection.json` and was not: why, and what to do.
    NotSaved(String),
}

impl Kept {
    /// The name the window gets for it.
    pub fn name(&self) -> &'static str {
        match self {
            Kept::Device => "device",
            Kept::Session => "session",
            Kept::Core => "core",
            Kept::NotSaved(_) => "not_saved",
        }
    }

    /// What the native window showing the new key says about it.
    pub fn notice(&self) -> String {
        let shown = "It is shown only here, once.";
        match self {
            Kept::Device => format!("The app now uses this management key and keeps it on this device. Programs and browser panels that used the old key need this one. {shown}"),
            Kept::Session => format!("The app now uses this management key until it quits. It is not kept on this device: copy it now, because you need it to sign in again. {shown}"),
            Kept::Core => format!("The app's core saved this management key in its config file, and the app uses it. Programs and browser panels that used the old key need this one. {shown}"),
            Kept::NotSaved(why) => format!("{why} {shown}"),
        }
    }
}

/// A rotated management key, for the native window that shows it once, and how it is kept.
pub struct Rotation {
    pub key: String,
    pub kept: Kept,
}

/// How long the app waits for core's reply to a rotation. The connection's lock is held while it
/// waits, which keeps tray Quit and every other call waiting too, so it is far below the 300
/// seconds other calls get; core only writes its config file before it replies, which 30 seconds
/// covers also behind another config change or over a slow link. A rotation that times out is
/// reported as a lost reply, with how to find the current key.
const ROTATION_TIMEOUT: Duration = Duration::from_secs(30);

/// Whether a key core sent can be used: what a bearer token can carry, of a sane length.
fn usable_key(k: &str) -> bool {
    !k.is_empty() && k.len() <= 1024 && k.bytes().all(|b| b.is_ascii_graphic())
}

/// What a call needs, taken out of the connection so no lock is held while it runs.
#[derive(Clone)]
pub struct Caller {
    client: Option<Client>,
    key: Option<String>,
}

impl Caller {
    pub fn call(&self, method: &str, path: &str, body: Option<&Value>) -> Reply {
        self.send(method, path, body, None)
    }

    fn send(
        &self,
        method: &str,
        path: &str,
        body: Option<&Value>,
        limit: Option<Duration>,
    ) -> Reply {
        match (&self.client, &self.key, limit) {
            (Some(c), Some(k), None) => c.call(k, method, path, body),
            (Some(c), Some(k), Some(l)) => c.call_within(k, method, path, body, l),
            (None, _, _) => Reply::failed(0, "unavailable", "the app is not connected to a core"),
            (_, None, _) => Reply::failed(401, "unauthorized", "not signed in"),
        }
    }

    /// Calls as call does. A call core refused because a rotation replaced the key while it was
    /// on its way goes once more with the current key, to the same core: core refuses a key
    /// before acting, so nothing runs twice.
    pub fn call_or_retry(
        &self,
        method: &str,
        path: &str,
        body: Option<&Value>,
        current: impl FnOnce() -> Caller,
    ) -> Reply {
        let r = self.call(method, path, body);
        if r.status != 401 {
            return r;
        }
        let now = current();
        let same_core =
            now.client.as_ref().map(Client::base) == self.client.as_ref().map(Client::base);
        if !same_core || now.key.is_none() || now.key == self.key {
            return r;
        }
        now.call(method, path, body)
    }
}

impl Connection {
    /// The connection the saved settings describe; in managed mode this starts the app's core.
    /// on_core_exit runs whenever a core the app started exits.
    pub fn open(dirs: Dirs, on_core_exit: Option<OnExit>) -> Connection {
        Self::open_at_startup(dirs, on_core_exit, true)
    }

    /// Startup alone may leave a saved managed core stopped. Explicit connection changes and
    /// Start/Restart retain their existing behaviour; foreign cores are always only connected.
    pub fn open_at_startup(
        dirs: Dirs,
        on_core_exit: Option<OnExit>,
        start_core: bool,
    ) -> Connection {
        Settings::remove_stale_temps(&dirs.settings());
        let (settings, error) = match Settings::load(&dirs.settings()) {
            Ok(s) => (s, None),
            Err(e) => (Settings::default(), Some(e)),
        };
        let mut c = Connection {
            dirs,
            revision: 0,
            settings,
            client: None,
            key: None,
            managed: None,
            on_core_exit,
            recovery: Recovery::default(),
            error,
            views: 0,
        };
        if c.error.is_none() && (start_core || c.settings.mode != Some(Mode::Managed)) {
            c.connect();
        }
        c
    }

    /// Connects as the settings say, recording why not when it cannot.
    fn connect(&mut self) {
        self.stop_core();
        self.client = None;
        self.key = None;
        self.error = None;
        let s = self.settings.clone();
        let result = match s.mode {
            None => Ok(()),
            Some(Mode::Managed) => self.start_core(),
            Some(Mode::Local) => s
                .url
                .as_deref()
                .ok_or_else(|| "no address set".to_string())
                .and_then(local_origin)
                .map(|base| {
                    self.client = Some(Client::new(&base, None));
                    self.key = s.key.clone();
                }),
            Some(Mode::Remote) => s
                .url
                .as_deref()
                .ok_or_else(|| "no address set".to_string())
                .and_then(remote_origin)
                .and_then(|base| {
                    let certs = match &s.ca_file {
                        Some(f) => Some(
                            fs::read(f)
                                .map_err(|e| format!("{f}: {e}"))
                                .and_then(|pem| certificates(&pem))?,
                        ),
                        None => None,
                    };
                    self.client = Some(Client::new(&base, certs));
                    self.key = s.key.clone();
                    Ok(())
                }),
        };
        if let Err(e) = result {
            self.error = Some(e);
        }
    }

    fn start_core(&mut self) -> Result<(), String> {
        let program = core_program(self.settings.core_path.as_deref())?;
        let core = ManagedCore::start(&program, &self.dirs.core, self.on_core_exit.clone())?;
        self.client = Some(Client::new(core.base(), None));
        self.key = Some(core.key().to_string());
        self.managed = Some(core);
        self.recovery.running();
        self.error = None;
        Ok(())
    }

    /// Stops the app's own core, if it runs one. A local or remote core is never stopped.
    pub fn stop_core(&mut self) {
        self.recovery.cancel();
        if self.settings.mode == Some(Mode::Managed) {
            self.client = None;
            self.key = None;
            self.error = None;
        }
        if let Some(mut core) = self.managed.take() {
            core.stop();
        }
    }

    /// Starts only an owned core; a live process is never replaced by Start.
    pub fn resume_core(&mut self) -> Result<(), String> {
        if self.settings.mode != Some(Mode::Managed) {
            return Err("the app runs no core of its own in this mode".into());
        }
        if self.managed.as_ref().is_some_and(|m| m.exited().is_none()) {
            return Ok(());
        }
        self.stop_core();
        let result = self.start_core();
        if let Err(e) = &result {
            self.error = Some(e.clone());
        }
        result
    }

    /// Observe only the child we own. Exit callbacks from an older child cannot affect it.
    pub fn core_exited(&mut self) -> Option<(u64, Instant)> {
        if self.settings.mode != Some(Mode::Managed) || !self.recovery.armed() {
            return None;
        }
        let why = self.managed.as_ref().and_then(ManagedCore::exited)?;
        if self.recovery.plan().is_none() {
            self.client = None;
            self.key = None;
            self.recovery.schedule(Instant::now(), why);
        }
        self.recovery.plan()
    }

    pub fn recover_core(&mut self, generation: u64) -> Option<(u64, Instant)> {
        if self.settings.mode != Some(Mode::Managed)
            || !self.recovery.begin(generation, Instant::now())
        {
            return None;
        }
        #[cfg(feature = "native-test")]
        eprintln!("native-test: recovery-attempt {{\"generation\":{generation}}}");
        // The supervisor has already reaped this child. Never signal a PID found by address.
        self.managed.take();
        if let Err(e) = self.start_core() {
            self.error = Some(e.clone());
            self.recovery.schedule(Instant::now(), e);
            return self.recovery.plan();
        }
        // A child can die during startup, before the connection lock is released.
        self.core_exited()
    }

    /// Starts the app's own core again, in managed mode.
    pub fn restart_core(&mut self) -> Result<(), String> {
        self.revision += 1;
        if self.settings.mode != Some(Mode::Managed) {
            return Err("the app runs no core of its own in this mode".into());
        }
        self.connect();
        self.error.clone().map_or(Ok(()), Err)
    }

    pub(crate) fn prepare_key(&self, change: Option<Change>) -> Result<KeyTarget, String> {
        let ch = change.unwrap_or_else(|| Change {
            mode: self.settings.mode,
            url: self.settings.url.clone(),
            ca_file: self.settings.ca_file.clone(),
            core_path: None,
        });
        let mode = ch.mode.ok_or("choose a local or remote core")?;
        let url = ch.url.as_deref().unwrap_or("");
        let base = match mode {
            Mode::Local => local_origin(url)?,
            Mode::Remote => remote_origin(url)?,
            Mode::Managed => return Err("the app signs in to its own core by itself".into()),
        };
        let ca_file = ch.ca_file.filter(|f| !f.is_empty() && mode == Mode::Remote);
        let certs = match &ca_file {
            Some(f) => Some(certificates(
                &fs::read(f).map_err(|e| format!("certificate authority {f}: {e}"))?,
            )?),
            None => None,
        };
        let mut summary = format!("Key for {base}");
        if let Some(ca) = &ca_file {
            summary.push_str(&format!("\nCertificate authority: {}", shown_path(ca)));
        }
        // The target last confirmed and saved, not the client in memory: a connection that failed,
        // or a managed core that stopped, has no client, and the saved target is still the source.
        let previous = match self.settings.mode {
            Some(Mode::Local | Mode::Remote) => self.settings.url.as_deref(),
            Some(Mode::Managed) => Some("the app's own core"),
            None => None,
        };
        if previous != Some(base.as_str()) {
            summary.push_str(&format!(
                "\nSwitches from {} to {base}",
                previous.unwrap_or("not connected")
            ));
        }
        Ok(KeyTarget {
            client: Client::new(&base, certs),
            revision: self.revision,
            summary,
            settings: Settings {
                mode: Some(mode),
                url: Some(base),
                ca_file,
                ..Settings::default()
            },
        })
    }

    /// Whether the connection is still the one target was prepared on.
    fn ensure_current(&self, target: &KeyTarget) -> Result<(), String> {
        if self.revision != target.revision {
            return Err("The connection changed. Open key entry again.".into());
        }
        Ok(())
    }

    /// Checks key and keeps it, all under one borrow: the tests' way. The native dialog uses
    /// `finish_key_unlocked`, which does not hold the connection while core answers.
    pub(crate) fn finish_key(
        &mut self,
        target: KeyTarget,
        key: &str,
        remember: bool,
    ) -> Result<(), String> {
        self.ensure_current(&target)?;
        check_key(&target.client, key)?;
        self.commit_key(target, key, remember)
    }

    /// Saves a key core accepted and switches to its target; refuses when the connection has
    /// changed since the target was prepared. The key is saved only after its check passed.
    fn commit_key(
        &mut self,
        mut target: KeyTarget,
        key: &str,
        remember: bool,
    ) -> Result<(), String> {
        self.ensure_current(&target)?;
        target.settings.key = remember.then(|| key.to_owned());
        target.settings.save(&self.dirs.settings())?;
        self.stop_core();
        self.revision += 1;
        self.settings = target.settings;
        self.client = Some(target.client);
        self.key = Some(key.to_owned());
        self.error = None;
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn enter_key(
        &mut self,
        ch: Option<Change>,
        key: &str,
        remember: bool,
    ) -> Result<(), String> {
        let target = self.prepare_key(ch)?;
        self.finish_key(target, key, remember)
    }

    /// Applies what the person set, after checking it; saves it once it is accepted. On a refused
    /// change, the connection stays as it was.
    pub fn change(&mut self, ch: Change) -> Result<(), String> {
        let mode = ch.mode.ok_or("choose a mode")?;
        let mut next = Settings {
            mode: Some(mode),
            ..Settings::default()
        };
        match mode {
            Mode::Managed => {
                next.core_path = ch.core_path.filter(|p| !p.is_empty());
                core_program(next.core_path.as_deref())?;
            }
            Mode::Local | Mode::Remote => {
                let url = ch.url.as_deref().unwrap_or("");
                let base = if mode == Mode::Local {
                    local_origin(url)?
                } else {
                    remote_origin(url)?
                };
                next.ca_file = ch.ca_file.filter(|f| !f.is_empty() && mode == Mode::Remote);
                let certs = match &next.ca_file {
                    Some(f) => Some(certificates(
                        &fs::read(f).map_err(|e| format!("{f}: {e}"))?,
                    )?),
                    None => None,
                };
                next.url = Some(base.clone());
                let same = self.settings.mode == Some(mode)
                    && self.settings.url == next.url
                    && self.settings.ca_file == next.ca_file;
                let client = Client::new(&base, certs);
                // A metadata-only change preserves an existing key only for this same target. A
                // kept key is replaced by the one in use, which differs after a rotated key could
                // not be saved; signed out, the kept key stays as it is.
                let session = if same { self.key.clone() } else { None };
                next.key = if same {
                    self.settings
                        .key
                        .as_ref()
                        .map(|kept| self.key.clone().unwrap_or_else(|| kept.clone()))
                } else {
                    None
                };
                next.save(&self.dirs.settings())?;
                self.stop_core();
                self.revision += 1;
                self.settings = next;
                self.client = Some(client);
                self.key = session;
                self.error = None;
                return Ok(());
            }
        }
        next.save(&self.dirs.settings())?;
        self.revision += 1;
        self.settings = next;
        self.connect();
        self.error.clone().map_or(Ok(()), Err)
    }

    /// Checks key against the connected core and keeps it when core accepts it; remember keeps
    /// it in the settings too. Managed mode needs no key from the person.
    pub fn sign_in(&mut self, key: &str, remember: bool) -> Result<(), String> {
        let target = self.prepare_key(None)?;
        self.finish_key(target, key, remember)
    }

    /// What a rotation would do, for the native confirmation; changes nothing.
    pub(crate) fn prepare_rotation(&self) -> Result<RotationTarget, Reply> {
        let base = self
            .client
            .as_ref()
            .map(|c| c.base().to_string())
            .ok_or_else(|| Reply::failed(0, "unavailable", "the app is not connected to a core"))?;
        if self.key.is_none() {
            return Err(Reply::failed(401, "unauthorized", "not signed in"));
        }
        let kept = match self.settings.mode {
            Some(Mode::Managed) => "The app's core saves the new key in its config file.",
            _ if self.settings.key.is_some() => {
                "The app keeps the new key on this device, as it kept the old one."
            }
            _ => "The app uses the new key until it quits; like the old one, it is not kept on this device.",
        };
        Ok(RotationTarget {
            revision: self.revision,
            summary: format!(
                "Rotate the management key of {base}. Core replaces it with a new random key at once, and the old key stops working for this app and for every other program that uses it. {kept} The app shows the new key once in a window of its own."
            ),
        })
    }

    /// Rotates the management key on the core target was prepared for and adopts the new one,
    /// kept as the old one was. Its caller holds the connection's lock throughout (waiting at
    /// most ROTATION_TIMEOUT for core), so nothing else changes the connection between core's
    /// reply and the new key taking effect. On an error the connection is unchanged; the reply
    /// never holds a key.
    pub(crate) fn finish_rotation(&mut self, target: RotationTarget) -> Result<Rotation, Reply> {
        if self.revision != target.revision {
            return Err(Reply::failed(
                409,
                "conflict",
                "The connection changed. Rotate again.",
            ));
        }
        let reply = self
            .caller()
            .send("POST", ROTATE, None, Some(ROTATION_TIMEOUT));
        let lost = || {
            let recover = if self.settings.mode == Some(Mode::Managed) {
                "Restart the core on the Connection page: the app then reads the current key from its config file."
            } else {
                "Find the current key in core's config file and sign in with it before rotating again."
            };
            Reply::failed(
                0,
                "unknown",
                format!("Core's reply to the rotation was lost, so the management key may have changed. {recover}"),
            )
        };
        match reply.status {
            200 => {}
            0 => return Err(lost()),
            status => {
                return Err(Reply {
                    status,
                    body: None,
                    error: reply.error,
                })
            }
        }
        let key = reply
            .body
            .as_ref()
            .and_then(|b| b.get("value"))
            .and_then(Value::as_str)
            .filter(|k| usable_key(k))
            .map(str::to_owned)
            .ok_or_else(lost)?;
        self.revision += 1;
        self.key = Some(key.clone());
        let kept = if self.settings.mode == Some(Mode::Managed) {
            Kept::Core
        } else if self.settings.key.is_some() {
            let mut next = self.settings.clone();
            next.key = Some(key.clone());
            match next.save(&self.dirs.settings()) {
                Ok(()) => {
                    self.settings = next;
                    Kept::Device
                }
                // The old key stays on disk, so a later sign-out still removes it.
                Err(e) => Kept::NotSaved(format!(
                    "The management key was rotated, but the new key could not be kept on this device ({e}). The app uses it until it quits; the kept key no longer works. Copy the new key now, fix the problem, then enter the new key again with \"Keep the key on this device\"."
                )),
            }
        } else {
            Kept::Session
        };
        self.error = match &kept {
            Kept::NotSaved(why) => Some(why.clone()),
            _ => None,
        };
        Ok(Rotation { key, kept })
    }

    /// Rotates the management key without asking: native confirmation is the caller's.
    pub fn rotate_key(&mut self) -> Result<Rotation, Reply> {
        let target = self.prepare_rotation()?;
        self.finish_rotation(target)
    }

    /// Forgets the key, also the kept one. Managed mode keeps its own.
    pub fn sign_out(&mut self) -> Result<(), String> {
        self.revision += 1;
        if self.settings.mode == Some(Mode::Managed) {
            return Ok(());
        }
        self.key = None;
        if let Some(kept) = self.settings.key.take() {
            if let Err(e) = self.settings.save(&self.dirs.settings()) {
                // Still on disk: say so, and try again on the next sign-out.
                self.settings.key = Some(kept);
                return Err(format!("the kept key could not be removed: {e}"));
            }
        }
        Ok(())
    }

    pub fn caller(&self) -> Caller {
        Caller {
            client: self.client.clone(),
            key: self.key.clone(),
        }
    }

    #[cfg(feature = "native-test")]
    pub(crate) fn test_settings_path(&self) -> PathBuf {
        self.dirs.settings()
    }

    pub fn mode(&self) -> Option<Mode> {
        self.settings.mode
    }

    /// The app's own core, if it runs one: its process ID.
    pub fn core_pid(&self) -> Option<u32> {
        self.managed
            .as_ref()
            .filter(|m| m.exited().is_none())
            .map(ManagedCore::pid)
    }

    pub fn view(&mut self) -> View {
        let core = if self.settings.mode == Some(Mode::Managed) {
            Some(if let Some((state, detail)) = self.recovery.status() {
                CoreView {
                    state,
                    pid: None,
                    detail: Some(detail),
                }
            } else {
                match self.managed.as_ref() {
                    Some(m) => match m.exited() {
                        None => CoreView {
                            state: "running",
                            pid: Some(m.pid()),
                            detail: None,
                        },
                        Some(why) => CoreView {
                            state: "stopped",
                            pid: None,
                            detail: Some(why),
                        },
                    },
                    None => CoreView {
                        state: "stopped",
                        pid: None,
                        detail: self.error.clone(),
                    },
                }
            })
        } else {
            None
        };
        View {
            mode: self.settings.mode,
            target: self.client.as_ref().map(|c| c.base().to_string()),
            signed_in: self.key.is_some(),
            remembered: self.settings.key.is_some(),
            url: self.settings.url.clone(),
            ca_file: self.settings.ca_file.clone(),
            core_path: self.settings.core_path.clone(),
            core,
            error: self.error.clone(),
            version: {
                self.views += 1;
                self.views
            },
        }
    }
}

/// Checks key against the target core without holding the connection's lock, which other
/// commands (and tray Quit) wait for; then, under the lock, refuses if the connection changed
/// meanwhile, and otherwise keeps the key. Returns the outcome with the view taken under the
/// same lock.
pub(crate) fn finish_key_unlocked(
    conn: &Mutex<Connection>,
    target: KeyTarget,
    key: &str,
    remember: bool,
) -> (Result<(), String>, View) {
    let checked = check_key(&target.client, key);
    let mut c = conn.lock().unwrap();
    let result = c
        .ensure_current(&target)
        .and(checked)
        .and_then(|()| c.commit_key(target, key, remember));
    (result, c.view())
}

/// Whether core accepts key, as the panel checks it: `GET /status`.
fn check_key(client: &Client, key: &str) -> Result<(), String> {
    let r = client.call(key, "GET", "/status", None);
    match r.status {
        200 => Ok(()),
        401 => Err("core refused this key".into()),
        404 => Err("core's management API is off: set management_key in its config".into()),
        0 => Err(match r.error.as_ref().map(|e| e.code.as_str()) {
            Some("tls") => "TLS verification or handshake failed; check the certificate authority",
            Some("timeout") => "the connection to core timed out",
            Some("dns") => "core's host name could not be resolved",
            Some("io") => "the connection to core failed (I/O)",
            Some("protocol") => "core returned an invalid HTTP response",
            _ => "core could not be reached or its reply could not be read",
        }
        .into()),
        s => Err(format!("core answered {s}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_failed_sign_in_save_preserves_the_old_connection() {
        use std::io::{Read, Write};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut head = Vec::new();
            let mut byte = [0];
            while !head.ends_with(b"\r\n\r\n") {
                stream.read_exact(&mut byte).unwrap();
                head.push(byte[0]);
            }
            assert!(head.starts_with(b"GET /v0/management/status HTTP/1.1\r\n"));
            stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
                .unwrap();
        });
        let dir =
            std::env::temp_dir().join(format!("pluxy-entry-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        let old = crate::managed::random_key();
        Settings {
            mode: Some(Mode::Local),
            url: Some(base),
            key: Some(old.clone()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        let before = c.view();
        std::fs::remove_file(dirs.settings()).unwrap();
        std::fs::create_dir(dirs.settings()).unwrap();
        assert!(c.sign_in(&crate::managed::random_key(), false).is_err());
        assert_eq!(
            c.view(),
            before,
            "failed persistence changed the connection"
        );
        assert_eq!(c.key.as_deref(), Some(old.as_str()));
        server.join().unwrap();
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn native_entry_authenticates_before_switching_and_only_keeps_on_request() {
        use std::io::{Read, Write};
        let key = crate::managed::random_key();
        let refused = crate::managed::random_key();
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let accepted = key.clone();
        let server = std::thread::spawn(move || {
            for status in [401, 200, 200] {
                let (mut stream, _) = listener.accept().unwrap();
                stream
                    .set_read_timeout(Some(std::time::Duration::from_secs(5)))
                    .unwrap();
                let mut head = Vec::new();
                let mut byte = [0];
                while !head.ends_with(b"\r\n\r\n") {
                    stream.read_exact(&mut byte).unwrap();
                    head.push(byte[0]);
                }
                let head = String::from_utf8(head).unwrap();
                assert!(head.starts_with("GET /v0/management/status HTTP/1.1\r\n"));
                assert_eq!(head.contains(&format!("Bearer {accepted}")), status == 200);
                let response = format!("HTTP/1.1 {status} response\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{{}}");
                stream.write_all(response.as_bytes()).unwrap();
            }
        });
        let dir =
            std::env::temp_dir().join(format!("pluxy-entry-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(Mode::Local),
            url: Some("http://127.0.0.1:1".into()),
            key: Some(crate::managed::random_key()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        let before = c.view();
        let saved = std::fs::read(dirs.settings()).unwrap();
        let change = || Change {
            mode: Some(Mode::Local),
            url: Some(base.clone()),
            ..Change::default()
        };
        assert_eq!(
            c.enter_key(Some(change()), &refused, true).unwrap_err(),
            "core refused this key"
        );
        assert_eq!(c.view(), before);
        assert_eq!(std::fs::read(dirs.settings()).unwrap(), saved);
        c.enter_key(Some(change()), &key, false).unwrap();
        assert!(c.view().signed_in && !c.view().remembered);
        assert_eq!(Settings::load(&dirs.settings()).unwrap().key, None);
        c.enter_key(None, &key, true).unwrap();
        assert!(c.view().signed_in && c.view().remembered);
        assert_eq!(
            Settings::load(&dirs.settings()).unwrap().key.as_deref(),
            Some(key.as_str())
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                std::fs::metadata(dirs.settings())
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777,
                0o600
            );
        }
        assert!(!serde_json::to_string(&c.view()).unwrap().contains(&key));
        server.join().unwrap();
        std::fs::remove_dir_all(dir).unwrap();
    }

    /// A reply without a key the app can use, or a connection changed since the confirmation,
    /// changes nothing and says the key may have changed (core may have rotated it).
    #[test]
    fn a_rotation_reply_without_a_usable_key_changes_nothing() {
        use std::io::{Read, Write};
        let bodies = [
            r#"{}"#,
            r#"{"value":""}"#,
            r#"{"value":"two words"}"#,
            r#"{"value":"line\nbreak"}"#,
            r#"{"value":7}"#,
        ];
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let server = std::thread::spawn(move || {
            for body in bodies {
                let (mut stream, _) = listener.accept().unwrap();
                let mut head = Vec::new();
                let mut byte = [0];
                while !head.ends_with(b"\r\n\r\n") {
                    stream.read_exact(&mut byte).unwrap();
                    head.push(byte[0]);
                }
                assert!(head.starts_with(b"POST /v0/management/management-key/rotate HTTP/1.1\r\n"));
                let response = format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
                stream.write_all(response.as_bytes()).unwrap();
            }
        });
        let dir =
            std::env::temp_dir().join(format!("pluxy-rotate-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        let old = crate::managed::random_key();
        Settings {
            mode: Some(Mode::Local),
            url: Some(base),
            key: Some(old.clone()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        let before = c.view();
        let saved = fs::read(dirs.settings()).unwrap();
        for _ in bodies {
            let r = c.rotate_key().err().expect("an unusable key was adopted");
            assert_eq!(r.status, 0);
            assert!(r.error.unwrap().message.contains("may have changed"));
            assert_eq!(c.view(), before);
            assert_eq!(c.key.as_deref(), Some(old.as_str()));
            assert_eq!(fs::read(dirs.settings()).unwrap(), saved);
        }
        server.join().unwrap();
        let target = c.prepare_rotation().ok().unwrap();
        c.sign_out().unwrap();
        assert_eq!(
            c.finish_rotation(target).err().unwrap().status,
            409,
            "a rotation confirmed for an older connection ran"
        );
        fs::remove_dir_all(dir).unwrap();
    }

    /// A fake core on a free port that reads one rotation request, then answers with body, or,
    /// with None, holds the connection without answering until the sender is dropped (two
    /// minutes at most). Returns its address.
    fn one_rotation(
        body: Option<String>,
    ) -> (
        String,
        std::sync::mpsc::Sender<()>,
        std::thread::JoinHandle<()>,
    ) {
        use std::io::{Read, Write};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let (done, held) = std::sync::mpsc::channel::<()>();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut head = Vec::new();
            let mut byte = [0];
            while !head.ends_with(b"\r\n\r\n") {
                stream.read_exact(&mut byte).unwrap();
                head.push(byte[0]);
            }
            assert!(head.starts_with(b"POST /v0/management/management-key/rotate HTTP/1.1\r\n"));
            match body {
                Some(body) => {
                    let response = format!("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
                    stream.write_all(response.as_bytes()).unwrap();
                }
                None => {
                    let _ = held.recv_timeout(std::time::Duration::from_secs(120));
                }
            }
        });
        (base, done, server)
    }

    /// A connection in local mode on base whose key old is kept in connection.json.
    fn kept_local(base: &str, old: &str) -> (PathBuf, Dirs, Connection) {
        let dir =
            std::env::temp_dir().join(format!("pluxy-rotate-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(Mode::Local),
            url: Some(base.to_string()),
            key: Some(old.to_string()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let c = Connection::open(dirs.clone(), None);
        (dir, dirs, c)
    }

    /// Core took the rotation and never answered: the app gives up long before the 300 seconds
    /// other calls may take, since the connection's lock is held meanwhile, and changes nothing.
    #[test]
    fn a_rotation_core_never_answers_gives_up_early_and_changes_nothing() {
        let (base, done, server) = one_rotation(None);
        let old = crate::managed::random_key();
        let (dir, dirs, mut c) = kept_local(&base, &old);
        let saved = fs::read(dirs.settings()).unwrap();
        let started = std::time::Instant::now();
        let r = c
            .rotate_key()
            .err()
            .expect("a rotation without a reply succeeded");
        let waited = started.elapsed();
        assert!(
            waited < std::time::Duration::from_secs(60),
            "waited {waited:?} for a core that never answered"
        );
        assert_eq!(r.status, 0);
        assert!(r.error.unwrap().message.contains("may have changed"));
        assert_eq!(c.key.as_deref(), Some(old.as_str()));
        assert_eq!(fs::read(dirs.settings()).unwrap(), saved);
        drop(done);
        server.join().unwrap();
        fs::remove_dir_all(dir).unwrap();
    }

    /// After a rotated key could not be kept, saving the same connection again keeps the key
    /// the app uses, not the old one core no longer accepts.
    #[test]
    fn saving_the_connection_after_a_failed_save_keeps_the_rotated_key() {
        use std::os::unix::fs::PermissionsExt;
        let new = crate::managed::random_key();
        let (base, _done, server) = one_rotation(Some(format!(r#"{{"value":"{new}"}}"#)));
        let old = crate::managed::random_key();
        let (dir, dirs, mut c) = kept_local(&base, &old);
        // Saving fails while the settings folder is read-only.
        let folder = dirs.settings().parent().unwrap().to_path_buf();
        fs::set_permissions(&folder, fs::Permissions::from_mode(0o500)).unwrap();
        let kept = c.rotate_key().map(|r| r.kept);
        fs::set_permissions(&folder, fs::Permissions::from_mode(0o700)).unwrap();
        server.join().unwrap();
        assert!(
            matches!(kept, Ok(Kept::NotSaved(_))),
            "the rotated key was saved in a read-only folder"
        );
        assert_eq!(
            Settings::load(&dirs.settings()).unwrap().key.as_deref(),
            Some(old.as_str())
        );
        c.change(Change {
            mode: Some(Mode::Local),
            url: Some(base),
            ca_file: None,
            core_path: None,
        })
        .unwrap();
        assert_eq!(
            Settings::load(&dirs.settings()).unwrap().key.as_deref(),
            Some(new.as_str()),
            "saving the connection again kept the key core no longer accepts"
        );
        assert_eq!(c.key.as_deref(), Some(new.as_str()));
        assert!(c.view().remembered && c.view().error.is_none());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn key_preparation_validates_and_describes_the_destination_without_changing_state() {
        let dir =
            std::env::temp_dir().join(format!("pluxy-prepare-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(Mode::Local),
            url: Some("http://127.0.0.1:1234".into()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        let before = c.view();
        let saved = fs::read(dirs.settings()).unwrap();
        let prepared = c
            .prepare_key(Some(Change {
                mode: Some(Mode::Remote),
                url: Some("https://example.com:9443".into()),
                ..Change::default()
            }))
            .unwrap();
        assert_eq!(prepared.summary, "Key for https://example.com:9443\nSwitches from http://127.0.0.1:1234 to https://example.com:9443");
        assert_eq!(c.view(), before);
        assert_eq!(fs::read(dirs.settings()).unwrap(), saved);
        for (mode, url, ca) in [
            (Mode::Managed, "", None),
            (Mode::Local, "http://example.com", None),
            (Mode::Remote, "http://example.com", None),
            (Mode::Remote, "https://example.com/path", None),
            (
                Mode::Remote,
                "https://example.com",
                Some(dir.join("missing.pem").to_string_lossy().into_owned()),
            ),
        ] {
            assert!(c
                .prepare_key(Some(Change {
                    mode: Some(mode),
                    url: Some(url.into()),
                    ca_file: ca,
                    ..Change::default()
                }))
                .is_err());
        }
        let prepared = c.prepare_key(None).unwrap();
        c.sign_out().unwrap();
        assert_eq!(
            c.finish_key(prepared, &crate::managed::random_key(), false)
                .unwrap_err(),
            "The connection changed. Open key entry again."
        );
        fs::remove_dir_all(dir).unwrap();
    }

    /// A connection in local mode on an address nothing listens on, the one the person last
    /// confirmed, and its folders.
    fn confirmed_local(url: &str, mode: Mode) -> (PathBuf, Dirs, Connection) {
        let dir =
            std::env::temp_dir().join(format!("pluxy-confirmed-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(mode),
            url: Some(url.into()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let c = Connection::open(dirs.clone(), None);
        (dir, dirs, c)
    }

    /// The confirmation names the target last confirmed and saved, not the client in memory: a
    /// connection that failed to come up has no client, but the saved target is still the source.
    #[test]
    fn the_confirmation_names_the_last_confirmed_target_not_the_client() {
        let (dir, _dirs, c) = confirmed_local("http://example.com", Mode::Local);
        assert!(c.client.is_none(), "the saved address is not a local one");
        let to_local = |c: &Connection, url: &str| {
            c.prepare_key(Some(Change {
                mode: Some(Mode::Local),
                url: Some(url.into()),
                ..Change::default()
            }))
            .unwrap()
            .summary
        };
        assert_eq!(
            to_local(&c, "http://127.0.0.1:1234"),
            "Key for http://127.0.0.1:1234\nSwitches from http://example.com to http://127.0.0.1:1234"
        );
        let (dir2, _dirs2, c2) = confirmed_local("https://example.com:9443", Mode::Remote);
        let kept = Change {
            mode: Some(Mode::Remote),
            url: Some("https://example.com:9443".into()),
            ..Change::default()
        };
        assert_eq!(
            c2.prepare_key(Some(kept)).unwrap().summary,
            "Key for https://example.com:9443",
            "the saved target itself is no switch"
        );
        fs::remove_dir_all(dir).unwrap();
        fs::remove_dir_all(dir2).unwrap();
    }

    /// With no saved connection, or the app's own core saved, the confirmation says so.
    #[test]
    fn the_confirmation_names_an_unset_or_managed_source() {
        let dir =
            std::env::temp_dir().join(format!("pluxy-confirmed-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        let mut c = Connection::open_at_startup(dirs.clone(), None, false);
        let change = || Change {
            mode: Some(Mode::Local),
            url: Some("http://127.0.0.1:1234".into()),
            ..Change::default()
        };
        assert!(c
            .prepare_key(Some(change()))
            .unwrap()
            .summary
            .ends_with("Switches from not connected to http://127.0.0.1:1234"));
        c.settings.mode = Some(Mode::Managed);
        assert!(c
            .prepare_key(Some(change()))
            .unwrap()
            .summary
            .ends_with("Switches from the app's own core to http://127.0.0.1:1234"));
    }

    /// A crash during a settings write leaves a temporary file of the writer's own name; the next
    /// start removes it, and only it: other names, folders and links stay.
    #[cfg(unix)]
    #[test]
    fn a_stale_settings_temporary_file_is_removed_at_startup() {
        use std::os::unix::fs::symlink;
        let (dir, dirs, c) = confirmed_local("http://127.0.0.1:1", Mode::Local);
        drop(c);
        let stale = dirs.config.join(format!(
            "connection.json.{}.tmp",
            crate::managed::random_key()
        ));
        fs::write(&stale, b"{\"key\":\"half written").unwrap();
        let other_names = [
            "connection.json.planted.tmp".to_string(),
            "window.json.tmp".to_string(),
            format!("window.json.{}.tmp", crate::managed::random_key()),
            format!("connection.json.{}.bak", crate::managed::random_key()),
            format!(
                "connection.json.{}.tmp",
                crate::managed::random_key().to_uppercase()
            ),
        ];
        for name in &other_names {
            fs::write(dirs.config.join(name), b"x").unwrap();
        }
        let a_folder = dirs.config.join(format!(
            "connection.json.{}.tmp",
            crate::managed::random_key()
        ));
        fs::create_dir(&a_folder).unwrap();
        let victim = dirs.config.join("victim");
        fs::write(&victim, b"untouched").unwrap();
        let a_link = dirs.config.join(format!(
            "connection.json.{}.tmp",
            crate::managed::random_key()
        ));
        symlink(&victim, &a_link).unwrap();
        let saved = fs::read(dirs.settings()).unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        assert!(!stale.exists(), "the stale temporary file is still there");
        for name in &other_names {
            assert!(dirs.config.join(name).exists(), "{name} was removed");
        }
        assert!(a_folder.is_dir());
        assert!(fs::symlink_metadata(&a_link).unwrap().is_symlink());
        assert_eq!(fs::read(&victim).unwrap(), b"untouched");
        assert_eq!(fs::read(dirs.settings()).unwrap(), saved);
        assert!(c.view().error.is_none());
        fs::remove_dir_all(dir).unwrap();
    }

    /// A fake core that holds a status check until released: signals when the check arrived.
    fn held_status_core(
        key: String,
    ) -> (
        String,
        std::sync::mpsc::Receiver<()>,
        std::sync::mpsc::Sender<()>,
        std::thread::JoinHandle<()>,
    ) {
        use std::io::{Read, Write};
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let (arrived, reached) = std::sync::mpsc::channel::<()>();
        let (release, held) = std::sync::mpsc::channel::<()>();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            stream
                .set_read_timeout(Some(Duration::from_secs(5)))
                .unwrap();
            let mut head = Vec::new();
            let mut byte = [0];
            while !head.ends_with(b"\r\n\r\n") {
                stream.read_exact(&mut byte).unwrap();
                head.push(byte[0]);
            }
            let head = String::from_utf8(head).unwrap();
            assert!(head.starts_with("GET /v0/management/status HTTP/1.1\r\n"));
            assert!(head.contains(&format!("Bearer {key}")));
            arrived.send(()).unwrap();
            let _ = held.recv_timeout(Duration::from_secs(30));
            stream
                .write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
                .unwrap();
        });
        (base, reached, release, server)
    }

    /// Waits for the connection's lock for up to two seconds: false when something keeps it.
    fn lock_is_free(conn: &std::sync::Mutex<Connection>) -> bool {
        let until = Instant::now() + Duration::from_secs(2);
        while Instant::now() < until {
            if let Ok(g) = conn.try_lock() {
                drop(g);
                return true;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        false
    }

    /// The new key's check is a network call: other commands go on meanwhile, and the key is
    /// kept only once the check has passed.
    #[test]
    fn the_key_check_does_not_hold_the_connection_lock() {
        let key = crate::managed::random_key();
        let (base, reached, release, server) = held_status_core(key.clone());
        let (dir, dirs, c) = confirmed_local("http://127.0.0.1:1", Mode::Local);
        let conn = std::sync::Mutex::new(c);
        let target = conn
            .lock()
            .unwrap()
            .prepare_key(Some(Change {
                mode: Some(Mode::Local),
                url: Some(base),
                ..Change::default()
            }))
            .unwrap();
        let before = fs::read(dirs.settings()).unwrap();
        std::thread::scope(|s| {
            let entry = s.spawn(|| finish_key_unlocked(&conn, target, &key, true));
            reached.recv_timeout(Duration::from_secs(10)).unwrap();
            let free = lock_is_free(&conn);
            assert_eq!(
                fs::read(dirs.settings()).unwrap(),
                before,
                "the key was saved before the check passed"
            );
            release.send(()).unwrap();
            let (result, view) = entry.join().unwrap();
            assert!(free, "the connection lock was held during the key check");
            result.unwrap();
            assert!(view.signed_in && view.remembered);
        });
        server.join().unwrap();
        assert_eq!(
            Settings::load(&dirs.settings()).unwrap().key.as_deref(),
            Some(key.as_str())
        );
        fs::remove_dir_all(dir).unwrap();
    }

    /// A connection that changes while the check runs is not overwritten by the key's target:
    /// nothing is saved and the person is asked to open key entry again.
    #[test]
    fn a_connection_changed_during_the_key_check_refuses_the_key() {
        let key = crate::managed::random_key();
        let (base, reached, release, server) = held_status_core(key.clone());
        let (dir, dirs, c) = confirmed_local("http://127.0.0.1:1", Mode::Local);
        let conn = std::sync::Mutex::new(c);
        let target = conn
            .lock()
            .unwrap()
            .prepare_key(Some(Change {
                mode: Some(Mode::Local),
                url: Some(base),
                ..Change::default()
            }))
            .unwrap();
        let before = fs::read(dirs.settings()).unwrap();
        std::thread::scope(|s| {
            let entry = s.spawn(|| finish_key_unlocked(&conn, target, &key, true));
            reached.recv_timeout(Duration::from_secs(10)).unwrap();
            assert!(lock_is_free(&conn), "the connection lock was held");
            conn.lock().unwrap().sign_out().unwrap();
            release.send(()).unwrap();
            let (result, view) = entry.join().unwrap();
            assert_eq!(
                result.unwrap_err(),
                "The connection changed. Open key entry again."
            );
            assert!(!view.signed_in);
        });
        server.join().unwrap();
        assert_eq!(fs::read(dirs.settings()).unwrap(), before);
        assert_eq!(
            conn.lock().unwrap().settings.url.as_deref(),
            Some("http://127.0.0.1:1")
        );
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn window_changes_cannot_supply_a_key() {
        let change = serde_json::json!({"mode": "local", "key": crate::managed::random_key()});
        assert!(
            serde_json::from_value::<Change>(change).is_err(),
            "connection_set still accepts a key from a webview"
        );
    }

    #[test]
    fn local_means_this_computer() {
        assert_eq!(
            local_origin("http://127.0.0.1:8400").unwrap(),
            "http://127.0.0.1:8400"
        );
        assert_eq!(
            local_origin("http://localhost:8400/").unwrap(),
            "http://localhost:8400"
        );
        assert_eq!(
            local_origin("http://[::1]:8400").unwrap(),
            "http://[::1]:8400"
        );
        assert_eq!(
            local_origin("http://127.1.2.3:1").unwrap(),
            "http://127.1.2.3:1"
        );
        for bad in [
            "http://10.0.0.2:8400",
            "http://example.com",
            "ftp://127.0.0.1",
            "http://127.0.0.1:8400/panel/",
            "http://u:p@127.0.0.1:8400",
            "http://127.0.0.1:8400?x",
            "127.0.0.1:8400",
            "",
        ] {
            assert!(local_origin(bad).is_err(), "{bad:?} was allowed");
        }
    }

    #[test]
    fn startup_core_off_keeps_saved_managed_mode_without_starting_a_program() {
        let dir =
            std::env::temp_dir().join(format!("pluxy-startup-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(Mode::Managed),
            core_path: Some("/missing/startup-test-core".into()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open_at_startup(dirs, None, false);
        assert_eq!(c.mode(), Some(Mode::Managed));
        assert!(c.core_pid().is_none());
        assert!(
            c.view().error.is_none(),
            "a disabled startup must not try the missing program"
        );
        assert!(
            c.restart_core().is_err(),
            "explicit Start still tries the chosen program"
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn remote_means_https() {
        assert_eq!(
            remote_origin("https://pluxy.example").unwrap(),
            "https://pluxy.example"
        );
        assert_eq!(
            remote_origin("https://127.0.0.1:9443/").unwrap(),
            "https://127.0.0.1:9443"
        );
        assert!(remote_origin("http://pluxy.example")
            .unwrap_err()
            .contains("https"));
        assert!(remote_origin("https://pluxy.example/sub").is_err());
    }

    #[test]
    fn settings_round_trip_and_a_broken_file_is_an_error() {
        let dir = std::env::temp_dir().join(format!(
            "pluxy-app-settings-{}",
            crate::managed::random_key()
        ));
        let path = dir.join("connection.json");
        assert_eq!(Settings::load(&path).unwrap(), Settings::default());
        let s = Settings {
            mode: Some(Mode::Remote),
            url: Some("https://pluxy.example".into()),
            ..Settings::default()
        };
        s.save(&path).unwrap();
        assert_eq!(Settings::load(&path).unwrap(), s);
        fs::write(&path, b"{not json").unwrap();
        assert!(Settings::load(&path).is_err());
        fs::remove_dir_all(dir).unwrap();
    }

    /// A sign-out whose save fails says so, and keeps the kept key in view, so it can be retried.
    #[cfg(unix)]
    #[test]
    fn a_failed_sign_out_keeps_the_kept_key() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!(
            "pluxy-app-signout-{}",
            crate::managed::random_key()
        ));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(Mode::Local),
            url: Some("http://127.0.0.1:1".into()),
            key: Some("kept-key".into()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        fs::set_permissions(&dirs.config, fs::Permissions::from_mode(0o500)).unwrap();
        let r = c.sign_out();
        fs::set_permissions(&dirs.config, fs::Permissions::from_mode(0o700)).unwrap();
        assert!(r.is_err());
        assert!(
            c.view().remembered,
            "the key is still on disk, and the view must say so"
        );
        c.sign_out().unwrap();
        assert!(!c.view().remembered);
        assert_eq!(Settings::load(&dirs.settings()).unwrap().key, None);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn the_view_never_holds_the_key() {
        let dir =
            std::env::temp_dir().join(format!("pluxy-app-view-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        Settings {
            mode: Some(Mode::Local),
            url: Some("http://127.0.0.1:1".into()),
            key: Some("secret-key-123".into()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs, None);
        let v = c.view();
        assert!(v.signed_in && v.remembered);
        // Each view is newer than the last, so the tray can drop one that arrives late.
        assert!(c.view().version > v.version);
        assert!(!serde_json::to_string(&v)
            .unwrap()
            .contains("secret-key-123"));
        fs::remove_dir_all(dir).unwrap();
    }

    /// Start leaves a running core alone and Restart replaces it. The window's Start and the
    /// tray's use Start, so a click that races a recovery keeps the recovered core.
    #[cfg(unix)]
    #[test]
    fn start_leaves_a_live_core_alone_and_restart_replaces_it() {
        use std::os::unix::fs::PermissionsExt;
        let dir =
            std::env::temp_dir().join(format!("pluxy-app-start-{}", crate::managed::random_key()));
        fs::create_dir_all(&dir).unwrap();
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        let program = dir.join("core.sh");
        fs::write(
            &program,
            "#!/bin/sh\necho 'pluxy: listening on http://127.0.0.1:1' >&2\nexec sleep 30\n",
        )
        .unwrap();
        fs::set_permissions(&program, fs::Permissions::from_mode(0o755)).unwrap();
        Settings {
            mode: Some(Mode::Managed),
            core_path: Some(program.display().to_string()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs, None);
        let first = c.core_pid().expect("the fixture core runs");
        c.resume_core().unwrap();
        assert_eq!(c.core_pid(), Some(first), "Start replaced a running core");
        // The same measure sees a replaced core.
        c.restart_core().unwrap();
        let second = c.core_pid().expect("Restart runs a core");
        assert_ne!(second, first, "Restart kept the old core");
        c.stop_core();
        assert_eq!(c.core_pid(), None);
        fs::remove_dir_all(dir).unwrap();
    }

    /// A core can exit while the app is still starting, before anything can act on its exit
    /// callback. Asking the connection afterwards, as the app's setup does, schedules recovery.
    #[cfg(unix)]
    #[test]
    fn a_core_that_exits_before_anyone_watches_is_recovered() {
        use std::os::unix::fs::PermissionsExt;
        use std::time::Duration;
        let dir =
            std::env::temp_dir().join(format!("pluxy-app-early-{}", crate::managed::random_key()));
        fs::create_dir_all(&dir).unwrap();
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
        };
        // Says where it listens, then exits at once.
        let program = dir.join("core.sh");
        fs::write(
            &program,
            "#!/bin/sh\necho 'pluxy: listening on http://127.0.0.1:1' >&2\nexit 7\n",
        )
        .unwrap();
        fs::set_permissions(&program, fs::Permissions::from_mode(0o755)).unwrap();
        Settings {
            mode: Some(Mode::Managed),
            core_path: Some(program.display().to_string()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        // Like the app's callback before its state exists: it reaches no connection.
        let (exited, exit) = std::sync::mpsc::channel();
        let on_exit: OnExit = std::sync::Arc::new(move || {
            let _ = exited.send(());
        });
        let mut c = Connection::open(dirs, Some(on_exit));
        assert_eq!(c.view().error, None, "the fixture core started");
        exit.recv_timeout(Duration::from_secs(10))
            .expect("the fixture core exited");

        let asked = Instant::now();
        let (_, due) = c.core_exited().expect("the early exit is recovered");
        let core = c.view().core.expect("a managed core view");
        assert_eq!(core.state, "recovering");
        assert!(
            core.detail
                .as_deref()
                .is_some_and(|d| d.contains("exit status: 7")),
            "{:?}",
            core.detail
        );
        // The first of the 2, 5 and 15 second delays.
        let delay = due.duration_since(asked);
        assert!(
            delay >= Duration::from_millis(1900) && delay <= Duration::from_secs(3),
            "{delay:?}"
        );
        fs::remove_dir_all(dir).unwrap();
    }
}

/// A path as the key dialog shows it: control characters escaped, so a file name cannot add
/// lines to the dialog.
fn shown_path(path: &str) -> String {
    path.chars()
        .map(|c| {
            if c.is_control() {
                c.escape_unicode().to_string()
            } else {
                c.to_string()
            }
        })
        .collect()
}

#[cfg(test)]
mod shown_path_tests {
    #[test]
    fn control_characters_cannot_add_dialog_lines() {
        assert_eq!(super::shown_path("/etc/ca.pem"), "/etc/ca.pem");
        assert_eq!(
            super::shown_path("/tmp/a\nKey for https://x"),
            "/tmp/a\\u{a}Key for https://x"
        );
    }
}
