//! How the app reaches core, in one of three modes:
//!
//! - **managed**: the app runs a core of its own (`managed.rs`) and stops it when it quits;
//! - **local**: a core already running on this computer, such as a system service; the app only
//!   connects, never starts or stops it;
//! - **remote**: a core on another computer, over https only, shown with its address.
//!
//! The management key stays here: the window gets a view of the connection without it.
//! Nothing in this module needs Tauri, so the tests drive it against a real core.

use crate::client::{certificates, Client, Reply};
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
pub struct Change {
    pub mode: Option<Mode>,
    pub url: Option<String>,
    /// Empty or unset: keep the key the app has for this same core.
    pub key: Option<String>,
    #[serde(default)]
    pub remember: bool,
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
    settings: Settings,
    client: Option<Client>,
    key: Option<String>,
    managed: Option<ManagedCore>,
    on_core_exit: Option<OnExit>,
    error: Option<String>,
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
        if self.settings.mode != Some(Mode::Managed) {
            return Err("the app runs no core of its own in this mode".into());
        }
        self.connect();
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

    /// With no new key, "keep the key" still decides whether the key the app has is kept.
    #[test]
    fn keeping_the_key_follows_remember_without_a_new_key() {
        let dir =
            std::env::temp_dir().join(format!("pluxy-app-keep-{}", crate::managed::random_key()));
        let dirs = Dirs {
            config: dir.join("config"),
            core: dir.join("core"),
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
