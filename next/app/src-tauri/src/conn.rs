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
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::fs;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
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
    pub core: Option<CoreView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
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
    error: Option<String>,
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
        match (&self.client, &self.key) {
            (Some(c), Some(k)) => c.call(k, method, path, body),
            (None, _) => Reply::failed(0, "unavailable", "the app is not connected to a core"),
            (_, None) => Reply::failed(401, "unauthorized", "not signed in"),
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
            error,
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
        Ok(())
    }

    /// Stops the app's own core, if it runs one. A local or remote core is never stopped.
    pub fn stop_core(&mut self) {
        if let Some(mut core) = self.managed.take() {
            core.stop();
        }
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
        let previous = self.client.as_ref().map(|c| c.base());
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

    pub(crate) fn finish_key(
        &mut self,
        mut target: KeyTarget,
        key: &str,
        remember: bool,
    ) -> Result<(), String> {
        if self.revision != target.revision {
            return Err("The connection changed. Open key entry again.".into());
        }
        check_key(&target.client, key)?;
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
            Some(Mode::Managed) => "The app's core saves the new key in its config file",
            _ if self.settings.key.is_some() => {
                "The app keeps the new key on this device, as it kept the old one"
            }
            _ => "The app uses the new key until it quits; like the old one, it is not kept on this device",
        };
        Ok(RotationTarget {
            revision: self.revision,
            summary: format!(
                "Rotate the management key of {base}. Core replaces it with a new random key at once, and the old key stops working for this app and for every other program that uses it. {kept}, and shows it once in a window of its own."
            ),
        })
    }

    /// Rotates the management key on the core target was prepared for and adopts the new one,
    /// kept as the old one was. Its caller holds the connection's lock throughout, so nothing
    /// else changes the connection between core's reply and the new key taking effect. On an
    /// error the connection is unchanged; the reply never holds a key.
    pub(crate) fn finish_rotation(&mut self, target: RotationTarget) -> Result<Rotation, Reply> {
        if self.revision != target.revision {
            return Err(Reply::failed(
                409,
                "conflict",
                "The connection changed. Rotate again.",
            ));
        }
        let reply = self.caller().call("POST", ROTATE, None);
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
        self.managed.as_ref().map(ManagedCore::pid)
    }

    pub fn view(&mut self) -> View {
        let core = if self.settings.mode == Some(Mode::Managed) {
            Some(match self.managed.as_ref() {
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
        assert!(!serde_json::to_string(&v)
            .unwrap()
            .contains("secret-key-123"));
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
