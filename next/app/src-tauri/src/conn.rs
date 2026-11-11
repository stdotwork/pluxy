//! How the app reaches core, in one of three modes:
//!
//! - **managed**: the app runs a core of its own (`managed.rs`), one of the versions it installed
//!   (`cores.rs`), and stops it when it quits;
//! - **local**: a core already running on this computer, such as a system service; the app only
//!   connects, never starts or stops it;
//! - **remote**: a core on another computer, over https only, shown with its address.
//!
//! The management key stays here: the window gets a view of the connection without it.
//! Nothing in this module needs Tauri, so the tests drive it against a real core.

use crate::artifact::PublisherKeys;
use crate::client::{certificates, Client, Reply};
use crate::cores::{self, ConfigSnapshot, Store};
use crate::managed::{core_program, write_private, ManagedCore, OnExit};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::fs;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use url::{Host, Url};

/// How long a started core has to answer its management API, once it says where it listens.
const READY_TIMEOUT: Duration = Duration::from_secs(10);

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
    /// A core program the person chose, in managed mode, which the app runs only once it is
    /// adopted (copied into the app's own versions); unset: `pluxy` beside the app.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub core_path: Option<String>,
    /// Where the app's own core versions come from, in managed mode: an index file.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub core_source: Option<String>,
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
pub struct Change {
    pub mode: Option<Mode>,
    pub url: Option<String>,
    /// Empty or unset: keep the key the app has for this same core.
    pub key: Option<String>,
    #[serde(default)]
    pub remember: bool,
    pub ca_file: Option<String>,
    pub core_path: Option<String>,
    pub core_source: Option<String>,
}

/// The connection as the window sees it: never the key.
#[derive(Clone, Debug, Default, PartialEq, Serialize)]
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
    pub core_source: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub core: Option<CoreView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct CoreView {
    /// `running`, `stopped`, or `missing` while no version is installed.
    pub state: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pid: Option<u32>,
    /// The installed version that runs (or would run): a release's version, or `custom-…`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    /// A core program waiting to be adopted.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub adopt: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// The app's own core versions, as the window sees them.
#[derive(Clone, Debug, Default, Serialize)]
pub struct Versions {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source: Option<String>,
    /// What the source offers for this computer.
    pub available: Vec<cores::Available>,
    /// Why the source could not be read.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_error: Option<String>,
    pub installed: Vec<cores::Installed>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub selected: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub previous: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub failed: Option<cores::Failed>,
    /// How the last install, rollback or adoption ended.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub outcome: Option<Outcome>,
}

/// How a core change the person asked for ended.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Outcome {
    /// Counts up with each change.
    pub seq: u64,
    /// `install`, `rollback` or `adopt`.
    pub action: &'static str,
    pub version: String,
    /// `done`, `cancelled` or `failed`.
    pub result: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
}

/// A core change the person confirmed, ready to apply.
pub enum CoreChange {
    /// A checked release, not yet unpacked.
    Install(cores::Staged),
    /// An installed version.
    Rollback(String),
    /// A core program to copy in, with the SHA-256 the person confirmed.
    Adopt { path: PathBuf, sha256: String },
}

const MISSING: &str =
    "No core is installed yet: install one from the core source, or adopt a core program.";

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
    /// The app's own core versions (`cores.rs`).
    pub cores: PathBuf,
}

impl Dirs {
    pub fn settings(&self) -> PathBuf {
        self.config.join("connection.json")
    }
}

/// The app's connection to core.
pub struct Connection {
    dirs: Dirs,
    settings: Settings,
    client: Option<Client>,
    key: Option<String>,
    managed: Option<ManagedCore>,
    /// The installed version the managed core runs.
    running: Option<String>,
    on_core_exit: Option<OnExit>,
    error: Option<String>,
    outcome: Option<Outcome>,
}

/// What a call needs, taken out of the connection so no lock is held while it runs.
#[derive(Clone)]
pub struct Caller {
    client: Option<Client>,
    key: Option<String>,
}

impl Caller {
    pub fn call(&self, method: &str, path: &str, body: Option<&Value>) -> Reply {
        match (&self.client, &self.key) {
            (Some(c), Some(k)) => c.call(k, method, path, body),
            (None, _) => Reply::failed(0, "unavailable", "the app is not connected to a core"),
            (_, None) => Reply::failed(401, "unauthorized", "not signed in"),
        }
    }
}

impl Connection {
    /// The connection the saved settings describe; in managed mode this starts the app's core.
    /// on_core_exit runs whenever a core the app started exits.
    pub fn open(dirs: Dirs, on_core_exit: Option<OnExit>) -> Connection {
        let (settings, error) = match Settings::load(&dirs.settings()) {
            Ok(s) => (s, None),
            Err(e) => (Settings::default(), Some(e)),
        };
        let mut c = Connection {
            dirs,
            settings,
            client: None,
            key: None,
            managed: None,
            running: None,
            on_core_exit,
            error,
            outcome: None,
        };
        if let Ok(store) = c.store() {
            store.clear_staging();
        }
        if c.error.is_none() {
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

    fn config_path(&self) -> PathBuf {
        self.dirs.core.join("config.json")
    }

    /// The app's own core versions.
    pub fn store(&self) -> Result<Store, String> {
        Store::open(&self.dirs.cores)
    }

    /// Starts the selected version. A switch the app did not finish (it stopped while a
    /// candidate was starting) is undone first: the config is put back and the candidate is
    /// recorded as failed. With no version installed, the app runs no core.
    fn start_core(&mut self) -> Result<(), String> {
        let store = self.store()?;
        let mut st = store.state()?;
        if let Some(c) = st.candidate.take() {
            ConfigSnapshot::kept(&store, &self.config_path()).restore()?;
            st.failed = Some(cores::Failed {
                version: c.clone(),
                reason: format!("the app stopped while core {c} was starting"),
            });
            store.save_state(&st)?;
        }
        let Some(id) = st.selected else {
            return Ok(());
        };
        self.run_version(&store, &id)
    }

    /// Starts installed version id and waits until it answers with the management key (and, for
    /// a release, its own version), then connects to it.
    fn run_version(&mut self, store: &Store, id: &str) -> Result<(), String> {
        let program = store.program(id)?;
        let expected = store.version(id)?;
        let name = store.folder(id).join(cores::PROGRAM);
        let core = ManagedCore::start(program, &name, &self.dirs.core, self.on_core_exit.clone())?;
        ready(&core, expected.as_deref())?;
        self.client = Some(Client::new(core.base(), None));
        self.key = Some(core.key().to_string());
        self.managed = Some(core);
        self.running = Some(id.to_string());
        Ok(())
    }

    /// Stops the app's own core, if it runs one. A local or remote core is never stopped.
    pub fn stop_core(&mut self) {
        if let Some(mut core) = self.managed.take() {
            core.stop();
        }
        self.running = None;
    }

    fn require_managed(&self) -> Result<(), String> {
        if self.settings.mode != Some(Mode::Managed) {
            return Err("the app runs no core of its own in this mode".into());
        }
        Ok(())
    }

    /// The core source the person set.
    pub fn core_source(&self) -> Result<String, String> {
        self.require_managed()?;
        self.settings
            .core_source
            .clone()
            .ok_or_else(|| "set a core source first".into())
    }

    /// Finds version in the core source and checks its signed archive with keys, before the
    /// person confirms installing it.
    pub fn prepare_install(
        &self,
        keys: &impl PublisherKeys,
        version: &str,
    ) -> Result<cores::Staged, String> {
        let source = self.core_source()?;
        self.store()?.stage(keys, &source, version)
    }

    /// Checks that version is installed, not running, and unchanged, before the person confirms
    /// switching to it.
    pub fn prepare_rollback(&self, id: &str) -> Result<(), String> {
        self.require_managed()?;
        let store = self.store()?;
        if !store.installed(id) {
            return Err(format!(
                "core {id} is not installed: there is nothing to roll back to"
            ));
        }
        if self.running.as_deref() == Some(id)
            && self.managed.as_ref().is_some_and(|m| m.exited().is_none())
        {
            return Err(format!("core {id} already runs"));
        }
        store.program(id).map(|_| ())
    }

    /// The core program to adopt (the one the person chose, or `pluxy` beside the app), with its
    /// SHA-256 and size, before the person confirms copying it in.
    pub fn prepare_adopt(&self) -> Result<(PathBuf, String, u64), String> {
        self.require_managed()?;
        let path = core_program(self.settings.core_path.as_deref())?;
        let (sha, size) = Store::inspect(&path)?;
        Ok((path, sha, size))
    }

    /// Applies a core change the person confirmed: installs or copies in the version and runs
    /// it in place of the current one. If it does not start and answer, the config is put back
    /// as it was and the last version that worked runs again.
    pub fn apply(&mut self, change: CoreChange) -> Result<(), String> {
        self.require_managed()?;
        let store = self.store()?;
        match change {
            CoreChange::Install(staged) => {
                let id = store.install(staged)?;
                self.switch(&store, &id, true)
            }
            CoreChange::Rollback(id) => {
                if !store.installed(&id) {
                    return Err(format!(
                        "core {id} is not installed: there is nothing to roll back to"
                    ));
                }
                self.switch(&store, &id, false)
            }
            CoreChange::Adopt { path, sha256 } => {
                let fresh =
                    !store.installed(&format!("custom-{}", &sha256[..12.min(sha256.len())]));
                let id = store.adopt(&path, &sha256)?;
                self.switch(&store, &id, fresh)?;
                self.settings.core_path = None;
                self.settings.save(&self.dirs.settings())
            }
        }
    }

    /// Runs version id in place of the current core. fresh: id was installed for this switch, so
    /// a failure removes it again.
    fn switch(&mut self, store: &Store, id: &str, fresh: bool) -> Result<(), String> {
        let mut st = store.state()?;
        let snapshot = ConfigSnapshot::take(store, &self.config_path())?;
        st.candidate = Some(id.to_string());
        store.save_state(&st)?;
        self.stop_core();
        self.client = None;
        self.key = None;
        self.error = None;
        match self.run_version(store, id) {
            Ok(()) => {
                if st.selected.as_deref() != Some(id) {
                    st.previous = st.selected.take();
                }
                st.selected = Some(id.to_string());
                st.candidate = None;
                st.failed = None;
                store.save_state(&st)?;
                let keep: Vec<&str> = [st.selected.as_deref(), st.previous.as_deref()]
                    .into_iter()
                    .flatten()
                    .collect();
                store.prune(&keep);
                Ok(())
            }
            Err(why) => {
                self.stop_core();
                let restored = snapshot.restore();
                if fresh {
                    store.remove(id);
                }
                st.candidate = None;
                st.failed = Some(cores::Failed {
                    version: id.to_string(),
                    reason: why.clone(),
                });
                store.save_state(&st)?;
                let mut msg = format!("core {id} did not start: {why}");
                match restored {
                    Ok(()) => msg.push_str("; its config was put back as it was"),
                    Err(e) => msg.push_str(&format!("; {e}")),
                }
                if let Some(last) = st.selected.clone() {
                    match self.run_version(store, &last) {
                        Ok(()) => msg.push_str(&format!(", and core {last} runs again")),
                        Err(e) => {
                            msg.push_str(&format!(", and core {last} did not start either: {e}"))
                        }
                    }
                }
                self.error = self.managed.is_none().then(|| msg.clone());
                Err(msg)
            }
        }
    }

    /// Records how a core change ended, for the window.
    pub fn record_outcome(
        &mut self,
        action: &'static str,
        version: &str,
        result: &'static str,
        detail: Option<String>,
    ) {
        let seq = self.outcome.as_ref().map_or(0, |o| o.seq) + 1;
        self.outcome = Some(Outcome {
            seq,
            action,
            version: version.to_string(),
            result,
            detail,
        });
    }

    /// The app's own core versions, and what the core source offers.
    pub fn versions(&self) -> Versions {
        let mut v = Versions {
            source: self.settings.core_source.clone(),
            outcome: self.outcome.clone(),
            ..Versions::default()
        };
        match self.store() {
            Ok(store) => {
                v.installed = store.list();
                if let Ok(st) = store.state() {
                    v.selected = st.selected;
                    v.previous = st.previous;
                    v.failed = st.failed;
                }
            }
            Err(e) => v.source_error = Some(e),
        }
        if let Some(src) = &self.settings.core_source {
            match cores::available(src) {
                Ok(a) => v.available = a,
                Err(e) => v.source_error = Some(e),
            }
        }
        v
    }

    /// Starts the app's own core again, in managed mode.
    pub fn restart_core(&mut self) -> Result<(), String> {
        self.require_managed()?;
        self.connect();
        if self.error.is_none() && self.managed.is_none() {
            return Err(MISSING.into());
        }
        self.error.clone().map_or(Ok(()), Err)
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
                if next
                    .core_path
                    .as_deref()
                    .is_some_and(|p| !Path::new(p).is_absolute())
                {
                    return Err("the core program must be given as a full path".into());
                }
                next.core_source = ch.core_source.filter(|p| !p.is_empty());
                if let Some(src) = &next.core_source {
                    cores::source_path(src)?;
                }
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
                let key = ch.key.filter(|k| !k.is_empty());
                // A new key is checked against that core before anything changes.
                if let Some(k) = &key {
                    check_key(&Client::new(&base, certs), k)?;
                }
                let session = match &key {
                    Some(k) => Some(k.clone()),
                    None if same => self.key.clone(),
                    None => None,
                };
                // The key the app will have, kept only when the person asks.
                next.key = session.clone().filter(|_| ch.remember);
                next.save(&self.dirs.settings())?;
                self.settings = next;
                self.connect();
                // Without a key, the window asks for one.
                self.key = session;
                return self.error.clone().map_or(Ok(()), Err);
            }
        }
        next.save(&self.dirs.settings())?;
        self.settings = next;
        self.connect();
        self.error.clone().map_or(Ok(()), Err)
    }

    /// Checks key against the connected core and keeps it when core accepts it; remember keeps
    /// it in the settings too. Managed mode needs no key from the person.
    pub fn sign_in(&mut self, key: &str, remember: bool) -> Result<(), String> {
        let client = self
            .client
            .clone()
            .ok_or("the app is not connected to a core")?;
        if self.settings.mode == Some(Mode::Managed) {
            return Err("the app signs in to its own core by itself".into());
        }
        check_key(&client, key)?;
        self.key = Some(key.to_string());
        if remember {
            self.settings.key = Some(key.to_string());
        } else {
            self.settings.key = None;
        }
        self.settings.save(&self.dirs.settings())
    }

    /// Forgets the key, also the kept one. Managed mode keeps its own.
    pub fn sign_out(&mut self) -> Result<(), String> {
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

    pub fn mode(&self) -> Option<Mode> {
        self.settings.mode
    }

    /// The app's own core, if it runs one: its process ID.
    pub fn core_pid(&self) -> Option<u32> {
        self.managed.as_ref().map(ManagedCore::pid)
    }

    pub fn view(&mut self) -> View {
        let core = if self.settings.mode == Some(Mode::Managed) {
            let selected = self
                .store()
                .and_then(|s| s.state())
                .ok()
                .and_then(|s| s.selected);
            let adopt = self.settings.core_path.clone();
            Some(match self.managed.as_ref() {
                Some(m) => match m.exited() {
                    None => CoreView {
                        state: "running",
                        pid: Some(m.pid()),
                        version: self.running.clone(),
                        adopt,
                        detail: None,
                    },
                    Some(why) => CoreView {
                        state: "stopped",
                        pid: None,
                        version: self.running.clone(),
                        adopt,
                        detail: Some(why),
                    },
                },
                None if selected.is_none() => CoreView {
                    state: "missing",
                    pid: None,
                    version: None,
                    adopt,
                    detail: Some(self.error.clone().unwrap_or_else(|| MISSING.into())),
                },
                None => CoreView {
                    state: "stopped",
                    pid: None,
                    version: selected,
                    adopt,
                    detail: self.error.clone(),
                },
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
            core_source: self.settings.core_source.clone(),
            core,
            error: self.error.clone(),
        }
    }
}

/// Whether a core the app just started is the one it asked for: it says it listens on this
/// computer, answers `GET /status` with the app's management key, and (for a release) reports the
/// version the app installed.
fn ready(core: &ManagedCore, expected: Option<&str>) -> Result<(), String> {
    let base = local_origin(core.base())
        .map_err(|e| format!("core said it listens on {}: {e}", core.base()))?;
    let client = Client::new(&base, None);
    let deadline = Instant::now() + READY_TIMEOUT;
    loop {
        if let Some(why) = core.exited() {
            return Err(why);
        }
        let r = client.call(core.key(), "GET", "/status", None);
        match r.status {
            200 => {
                let got = r.body.as_ref().and_then(|b| b["version"].as_str());
                return match expected {
                    Some(want) if got != Some(want) => Err(format!(
                        "it reports version {}, not {want}",
                        got.unwrap_or("none")
                    )),
                    _ => Ok(()),
                };
            }
            0 if Instant::now() < deadline => std::thread::sleep(Duration::from_millis(100)),
            0 => return Err("it did not answer its management API".into()),
            401 => return Err("it refused the app's management key".into()),
            s => return Err(format!("its management API answered {s}")),
        }
    }
}

/// Whether core accepts key, as the panel checks it: `GET /status`.
fn check_key(client: &Client, key: &str) -> Result<(), String> {
    let r = client.call(key, "GET", "/status", None);
    match r.status {
        200 => Ok(()),
        401 => Err("core refused this key".into()),
        404 => Err("core's management API is off: set management_key in its config".into()),
        0 => Err(r
            .error
            .map(|e| e.message)
            .unwrap_or_else(|| "core could not be reached".into())),
        s => Err(format!("core answered {s}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

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

    /// With no new key, "keep the key" still decides whether the key the app has is kept.
    #[test]
    fn keeping_the_key_follows_remember_without_a_new_key() {
        let dir =
            std::env::temp_dir().join(format!("pluxy-app-keep-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
            cores: dir.join("cores"),
        };
        let url = "http://127.0.0.1:1";
        Settings {
            mode: Some(Mode::Local),
            url: Some(url.into()),
            key: Some("kept-key".into()),
            ..Settings::default()
        }
        .save(&dirs.settings())
        .unwrap();
        let mut c = Connection::open(dirs.clone(), None);
        let same = |remember| Change {
            mode: Some(Mode::Local),
            url: Some(url.into()),
            remember,
            ..Change::default()
        };
        c.change(same(false)).unwrap();
        assert!(c.view().signed_in, "the session keeps the key");
        assert!(!c.view().remembered);
        assert_eq!(Settings::load(&dirs.settings()).unwrap().key, None);
        c.change(same(true)).unwrap();
        assert!(c.view().remembered);
        assert_eq!(
            Settings::load(&dirs.settings()).unwrap().key.as_deref(),
            Some("kept-key")
        );
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
            cores: dir.join("cores"),
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
            cores: dir.join("cores"),
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
        assert!(!serde_json::to_string(&v)
            .unwrap()
            .contains("secret-key-123"));
        fs::remove_dir_all(dir).unwrap();
    }
}
