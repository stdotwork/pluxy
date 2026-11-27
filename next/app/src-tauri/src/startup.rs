//! Native startup preferences. Only an explicit save changes OS registration.
//!
//! The registration is the user's own login item, never machine-wide: on Linux an XDG autostart
//! `.desktop` file, on macOS a LaunchAgent property list in `~/Library/LaunchAgents`, on Windows
//! a value in the user's `Run` key (`startup/registry.rs`). Each runs the app with `--autostart`.
//! Each carries a mark that says pluxy made it: an entry without the mark is another program's,
//! and is left as it is.
use crate::managed::write_private;
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};

#[cfg(windows)]
mod registry;
/// The user's registry, for the app's update (`updater/uninstall_entry.rs`).
#[cfg(windows)]
pub(crate) use registry::{set_text, text_value};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Preferences {
    pub autostart_enabled: bool,
    pub silent_start_enabled: bool,
    pub start_core_on_launch: bool,
}
impl Default for Preferences {
    fn default() -> Self {
        Self {
            autostart_enabled: false,
            silent_start_enabled: false,
            start_core_on_launch: true,
        }
    }
}
pub struct Store {
    config: PathBuf,
    /// The folder the login item's own folder is in: the XDG config folder on Linux
    /// (`autostart/`), the user's `Library` folder on macOS (`LaunchAgents/`). Not used on Windows.
    #[cfg_attr(windows, allow(dead_code))]
    base: PathBuf,
    identifier: String,
    /// Whether this build offers Open at sign-in. A local build (package.ts local) does not: it
    /// opens only through its `start`, which sets its HOME and add-on trust (`local-start.sh`).
    sign_in: bool,
    /// The registry keys the Run entry is in (the user's own, outside tests).
    #[cfg(windows)]
    keys: registry::Keys,
    #[cfg(test)]
    fault: std::sync::atomic::AtomicBool,
    #[cfg(test)]
    read_fault: std::sync::atomic::AtomicBool,
}
impl Store {
    pub fn new(config: PathBuf, base: PathBuf, identifier: String) -> Self {
        Self {
            config,
            base,
            identifier,
            sign_in: true,
            #[cfg(windows)]
            keys: registry::Keys::user(),
            #[cfg(test)]
            fault: std::sync::atomic::AtomicBool::new(false),
            #[cfg(test)]
            read_fault: std::sync::atomic::AtomicBool::new(false),
        }
    }
    /// The same store for a build that offers no Open at sign-in: it reads and changes no login
    /// item, reports Open at sign-in and Start in the tray as off, and refuses to turn the first on.
    /// A login item an earlier build left is not shown as on and stays as it is (`lib.rs` `run`
    /// ends a launch it starts).
    pub fn without_sign_in(mut self) -> Self {
        self.sign_in = false;
        self
    }
    fn file(&self) -> PathBuf {
        self.config.join("startup.json")
    }
    #[cfg(not(windows))]
    fn entry(&self) -> PathBuf {
        #[cfg(target_os = "macos")]
        return self
            .base
            .join("LaunchAgents")
            .join(format!("{}.plist", self.identifier));
        #[cfg(not(target_os = "macos"))]
        self.base
            .join("autostart")
            .join(format!("{}.desktop", self.identifier))
    }
    /// A damaged file must not prevent startup or be overwritten unnoticed.
    pub fn load(&self) -> Preferences {
        match fs::read(self.file()) {
            Ok(bytes) => match serde_json::from_slice(&bytes) {
                Ok(p) => p,
                Err(e) => {
                    let backup = self.config.join(format!(
                        "startup.damaged-{}.json",
                        crate::managed::random_key()
                    ));
                    if let Err(keep) = fs::rename(self.file(), &backup) {
                        eprintln!("pluxy-app: damaged startup settings: {e}; keeping file in place: {keep}");
                    } else {
                        eprintln!(
                            "pluxy-app: damaged startup settings kept at {}: {e}",
                            backup.display()
                        );
                    }
                    Preferences::default()
                }
            },
            Err(e) => {
                if e.kind() != std::io::ErrorKind::NotFound {
                    eprintln!("pluxy-app: cannot read startup settings: {e}");
                }
                Preferences::default()
            }
        }
    }
    pub fn view(&self, mut preferences: Preferences) -> Result<View, String> {
        if !self.sign_in {
            preferences.autostart_enabled = false;
            preferences.silent_start_enabled = false;
            return Ok(View {
                preferences,
                registration_owned: false,
                registration_conflict: false,
                sign_in: false,
            });
        }
        let entry = self.registration()?;
        preferences.autostart_enabled = entry.enabled;
        Ok(View {
            preferences,
            registration_owned: entry.owned,
            registration_conflict: entry.exists && !entry.owned,
            sign_in: true,
        })
    }
    #[cfg(not(windows))]
    fn registration(&self) -> Result<Registration, String> {
        let path = self.entry();
        let metadata = match fs::symlink_metadata(&path) {
            Ok(m) => m,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                return Ok(Registration::default())
            }
            Err(e) => return Err(e.to_string()),
        };
        if !metadata.file_type().is_file() {
            return Ok(Registration {
                exists: true,
                ..Registration::default()
            });
        }
        let bytes = fs::read(path).map_err(|e| e.to_string())?;
        Ok(parse_entry(&bytes, &self.identifier))
    }
    #[cfg(windows)]
    fn registration(&self) -> Result<Registration, String> {
        registry::registration(&self.keys, &self.identifier)
    }
    /// What the system holds now, to put back if the save fails after it was changed.
    #[cfg(not(windows))]
    fn snapshot(&self, exists: bool) -> Result<Option<Vec<u8>>, String> {
        if exists {
            fs::read(self.entry()).map(Some).map_err(|e| e.to_string())
        } else {
            Ok(None)
        }
    }
    #[cfg(windows)]
    fn snapshot(&self, exists: bool) -> Result<registry::Snapshot, String> {
        let _ = exists;
        registry::snapshot(&self.keys, &self.identifier)
    }
    #[cfg(not(windows))]
    fn install(&self, executable: &Path) -> Result<(), String> {
        let bytes = render_entry(executable, &self.identifier)?;
        fs::create_dir_all(self.entry().parent().expect("autostart folder"))
            .map_err(|e| e.to_string())?;
        write_private(&self.entry(), &bytes).map_err(|e| e.to_string())
    }
    #[cfg(windows)]
    fn install(&self, executable: &Path) -> Result<(), String> {
        validate_executable(executable)?;
        registry::install(&self.keys, &self.identifier, executable)
    }
    #[cfg(not(windows))]
    fn uninstall(&self) -> Result<(), String> {
        fs::remove_file(self.entry()).map_err(|e| e.to_string())
    }
    #[cfg(windows)]
    fn uninstall(&self) -> Result<(), String> {
        registry::uninstall(&self.keys, &self.identifier)
    }
    #[cfg(not(windows))]
    fn restore(&self, previous: Option<Vec<u8>>) -> Result<(), String> {
        match previous {
            Some(b) => write_private(&self.entry(), &b),
            // Nothing was there, and a change that failed may have made nothing either.
            None => match fs::remove_file(self.entry()) {
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
                r => r,
            },
        }
        .map_err(|e| e.to_string())
    }
    /// For the tests: a change that is made and then reported as failed.
    #[cfg(test)]
    fn after_change(&self) -> Result<(), String> {
        if self.fault.load(std::sync::atomic::Ordering::SeqCst) {
            return Err("the change did not complete".into());
        }
        Ok(())
    }
    #[cfg(not(test))]
    fn after_change(&self) -> Result<(), String> {
        Ok(())
    }
    /// The entry as it stands after the change (for the tests, a read that fails).
    #[cfg(test)]
    fn read_back(&self) -> Result<Registration, String> {
        if self.read_fault.load(std::sync::atomic::Ordering::SeqCst) {
            return Err("the entry could not be read".into());
        }
        self.registration()
    }
    #[cfg(not(test))]
    fn read_back(&self) -> Result<Registration, String> {
        self.registration()
    }
    #[cfg(windows)]
    fn restore(&self, previous: registry::Snapshot) -> Result<(), String> {
        registry::restore(&self.keys, &self.identifier, previous)
    }
    pub fn save(
        &self,
        mut prefs: Preferences,
        executable: impl FnOnce() -> Result<PathBuf, String>,
    ) -> Result<View, String> {
        if !self.sign_in {
            if prefs.autostart_enabled {
                return Err(NO_SIGN_IN.into());
            }
            prefs.silent_start_enabled = false;
            fs::create_dir_all(&self.config).map_err(|e| e.to_string())?;
            let bytes = serde_json::to_vec_pretty(&prefs).expect("startup preferences are JSON");
            write_private(&self.file(), &bytes)
                .map_err(|e| format!("saving startup preferences: {e}"))?;
            return self.view(prefs);
        }
        fs::create_dir_all(&self.config).map_err(|e| e.to_string())?;
        let registration = self.registration()?;
        let changed = registration.enabled != prefs.autostart_enabled;
        if changed && registration.exists && !registration.owned {
            return Err("The autostart file belongs to you or another application; pluxy will not replace it".into());
        }
        // Resolved before anything is changed: an app path that cannot be had changes nothing.
        let exe = if changed && prefs.autostart_enabled {
            Some(executable()?)
        } else {
            None
        };
        let previous = if changed {
            Some(self.snapshot(registration.exists)?)
        } else {
            None
        };
        if changed {
            let applied = match &exe {
                Some(exe) => self.install(exe),
                None => self.uninstall(),
            }
            .and_then(|()| self.after_change());
            if let Err(e) = applied {
                // It may be done in part: what was there is put back.
                return Err(match previous.map(|p| self.restore(p)) {
                    Some(Err(r)) => format!("{e}; restoring autostart entry: {r}"),
                    _ => e,
                });
            }
        }
        // What is read back and saved after the entry changed: if either fails, the entry is put back.
        let saved = self.read_back().map_err(|e| e.to_string()).and_then(|r| {
            prefs.autostart_enabled = r.enabled;
            let bytes = serde_json::to_vec_pretty(&prefs).expect("startup preferences are JSON");
            write_private(&self.file(), &bytes)
                .map_err(|e| format!("saving startup preferences: {e}"))
        });
        if let Err(e) = saved {
            if let Some(previous) = previous {
                if let Err(r) = self.restore(previous) {
                    return Err(format!("{e}; restoring autostart entry: {r}"));
                }
            }
            return Err(e);
        }
        self.view(prefs)
    }
}

#[derive(Default)]
struct Registration {
    exists: bool,
    owned: bool,
    enabled: bool,
}

#[derive(Serialize)]
pub struct View {
    #[serde(flatten)]
    pub preferences: Preferences,
    pub registration_owned: bool,
    pub registration_conflict: bool,
    /// Whether this build offers Open at sign-in (`Store::without_sign_in`).
    pub sign_in: bool,
}

/// What a build without Open at sign-in answers when asked to turn it on.
pub const NO_SIGN_IN: &str = "A local build opens with its start command, not at sign-in";

/// Whether a launch with these arguments ends at once: a login launch (`--autostart`) of a build
/// that offers no Open at sign-in, which only an earlier build's login item can start. Such a
/// launch would bypass that build's `start` and run without its HOME and add-on trust.
pub fn refused_launch(sign_in: bool, mut args: impl Iterator<Item = std::ffi::OsString>) -> bool {
    !sign_in && args.any(|a| a == "--autostart")
}

/// Commands require all choices; stored preferences allow older/newer schemas.
#[derive(Deserialize)]
pub struct Input {
    pub autostart_enabled: bool,
    pub silent_start_enabled: bool,
    pub start_core_on_launch: bool,
}
impl From<Input> for Preferences {
    fn from(p: Input) -> Self {
        Self {
            autostart_enabled: p.autostart_enabled,
            silent_start_enabled: p.silent_start_enabled,
            start_core_on_launch: p.start_core_on_launch,
        }
    }
}

pub fn executable(
    appimage: Option<std::ffi::OsString>,
    current: impl FnOnce() -> std::io::Result<PathBuf>,
) -> Result<PathBuf, String> {
    let path = match appimage {
        Some(p) => PathBuf::from(p),
        None => current().map_err(|e| e.to_string())?,
    };
    validate_executable(&path)?;
    Ok(path)
}
fn validate_executable(path: &Path) -> Result<(), String> {
    let text = path.to_str().ok_or("the app path is not UTF-8")?;
    if !path.is_absolute() || text.chars().any(char::is_control) || text.ends_with(" (deleted)") {
        return Err(
            "autostart needs an existing absolute app path without control characters or (deleted)"
                .into(),
        );
    }
    // Backslash is the separator on Windows; `%` would be expanded by the shell there too.
    if text.contains('%') || (cfg!(not(windows)) && text.contains(char::from(92))) {
        return Err(if cfg!(windows) {
            "autostart executable paths cannot contain %".into()
        } else {
            "autostart executable paths cannot contain % or backslash".into()
        });
    }
    if !fs::metadata(path)
        .map_err(|e| format!("autostart executable: {e}"))?
        .is_file()
    {
        return Err("autostart executable must be an existing regular file".into());
    }
    Ok(())
}

/// Silent Launch keeps the app's own core in the tray. Local and remote modes host no core, so
/// they open normally.
pub fn wants_silent(preferences: Preferences, autostart: bool, managed: bool) -> bool {
    preferences.silent_start_enabled && autostart && managed
}

/// GTK tray creation can succeed with no desktop host. Never hide a startup window then. The
/// check is `window::tray_host`'s: the same one decides whether closing the window can leave the
/// app in the tray (the Windows and macOS desktops always show tray icons).
pub fn tray_host() -> Result<(), String> {
    crate::window::tray_host()
}

/// The bytes of the login item for `executable`.
#[cfg(not(windows))]
fn render_entry(executable: &Path, identifier: &str) -> Result<Vec<u8>, String> {
    #[cfg(target_os = "linux")]
    {
        let _ = identifier;
        desktop_entry(executable, crate::DISPLAY_NAME).map(String::into_bytes)
    }
    #[cfg(target_os = "macos")]
    {
        launch_agent(executable, identifier)
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        let _ = (executable, identifier);
        Err("Open at sign-in is supported on Linux, macOS and Windows only".into())
    }
}

/// What an existing login item is: pluxy's own (marked), whether it is turned off.
#[cfg(not(windows))]
fn parse_entry(bytes: &[u8], identifier: &str) -> Registration {
    #[cfg(target_os = "linux")]
    {
        let _ = identifier;
        parse_desktop_entry(bytes)
    }
    #[cfg(target_os = "macos")]
    {
        parse_launch_agent(bytes, identifier)
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        let _ = (bytes, identifier);
        Registration {
            exists: true,
            ..Registration::default()
        }
    }
}

#[cfg(target_os = "linux")]
fn parse_desktop_entry(bytes: &[u8]) -> Registration {
    let text = String::from_utf8_lossy(bytes);
    let mut desktop = false;
    let mut owned = false;
    let mut disabled = false;
    for line in text.lines().map(str::trim) {
        if line.starts_with('[') {
            desktop = line == "[Desktop Entry]";
        }
        if desktop {
            owned |= line == "X-Pluxy-Autostart=true";
            disabled |= line == "Hidden=true" || line == "X-GNOME-Autostart-enabled=false";
        }
    }
    Registration {
        exists: true,
        owned,
        enabled: owned && !disabled,
    }
}

/// The LaunchAgent that starts the app at login: a property list launchd reads from
/// `~/Library/LaunchAgents`, for this user's graphical session only (`Aqua`, so not in a
/// terminal-only login). The mark `X-Pluxy-Autostart` says pluxy made it.
#[cfg(any(target_os = "macos", all(test, not(windows))))]
fn launch_agent(executable: &Path, identifier: &str) -> Result<Vec<u8>, String> {
    use plist::{Dictionary, Value};
    validate_executable(executable)?;
    let path = executable.to_str().ok_or("the app path is not UTF-8")?;
    let mut d = Dictionary::new();
    d.insert("Label".into(), Value::String(identifier.into()));
    d.insert(
        "ProgramArguments".into(),
        Value::Array(vec![
            Value::String(path.into()),
            Value::String("--autostart".into()),
        ]),
    );
    d.insert("RunAtLoad".into(), Value::Boolean(true));
    d.insert(
        "LimitLoadToSessionType".into(),
        Value::String("Aqua".into()),
    );
    d.insert("X-Pluxy-Autostart".into(), Value::Boolean(true));
    let mut out = Vec::new();
    Value::Dictionary(d)
        .to_writer_xml(&mut out)
        .map_err(|e| e.to_string())?;
    Ok(out)
}

/// Whether a LaunchAgent is pluxy's own (marked, labelled with the app's identifier, and starting
/// something with `--autostart`) and whether it is turned off (`Disabled`). A file that is not a
/// property list is someone else's.
#[cfg(any(target_os = "macos", all(test, not(windows))))]
fn parse_launch_agent(bytes: &[u8], identifier: &str) -> Registration {
    use plist::Value;
    let Ok(Value::Dictionary(d)) = Value::from_reader(std::io::Cursor::new(bytes)) else {
        return Registration {
            exists: true,
            ..Registration::default()
        };
    };
    let flag = |key: &str| d.get(key).and_then(Value::as_boolean) == Some(true);
    let labelled = d.get("Label").and_then(Value::as_string) == Some(identifier);
    let starts = d
        .get("ProgramArguments")
        .and_then(Value::as_array)
        .is_some_and(|a| a.len() == 2 && a[1].as_string() == Some("--autostart"));
    let owned = flag("X-Pluxy-Autostart") && labelled && starts;
    Registration {
        exists: true,
        owned,
        enabled: owned && !flag("Disabled"),
    }
}

/// The desktop entry that starts `executable` at login, named `name` (the app's display name).
#[cfg(target_os = "linux")]
fn desktop_entry(executable: &Path, name: &str) -> Result<String, String> {
    validate_executable(executable)?;
    let path = executable.to_str().ok_or("the app path is not UTF-8")?;
    // Exec quoting, then Desktop Entry string escaping.
    let mut quoted = String::new();
    for c in path.chars() {
        if matches!(c, '\\' | '"' | '$') || c == char::from(96) {
            quoted.push('\\');
        }
        quoted.push(c);
    }
    let quoted = quoted.replace('\\', "\\\\");
    Ok(format!("[Desktop Entry]\nType=Application\nName={name}\nExec=\"{quoted}\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n"))
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    fn store() -> Store {
        let root =
            std::env::temp_dir().join(format!("pluxy-startup-{}", crate::managed::random_key()));
        fs::create_dir_all(&root).unwrap();
        fs::write(root.join("pluxy app"), b"fixture").unwrap();
        Store::new(
            root.join("dev.pluxy.desktop"),
            root,
            "dev.pluxy.desktop".into(),
        )
    }

    #[test]
    fn private_settings_and_independent_choices() {
        let s = store();
        assert_eq!(s.load(), Preferences::default());
        let prefs = Preferences {
            autostart_enabled: false,
            silent_start_enabled: true,
            start_core_on_launch: false,
        };
        s.save(prefs, || Ok(s.base.join("pluxy app"))).unwrap();
        assert_eq!(s.load(), prefs);
        assert_eq!(
            std::fs::metadata(s.file()).unwrap().permissions().mode() & 0o777,
            0o600
        );
        assert!(!s.entry().exists());
        std::fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn xdg_entry_has_exact_content_and_disable_removes_it() {
        let s = store();
        s.save(
            Preferences {
                autostart_enabled: true,
                ..Preferences::default()
            },
            || Ok(s.base.join("pluxy app")),
        )
        .unwrap();
        assert_eq!(
            s.entry(),
            s.base.join("autostart/dev.pluxy.desktop.desktop")
        );
        assert_eq!(std::fs::read_to_string(s.entry()).unwrap(), format!("[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"{}/pluxy app\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n", s.base.display()));
        s.save(Preferences::default(), || Ok(s.base.join("pluxy app")))
            .unwrap();
        assert!(!s.entry().exists());
        assert!(!s.load().autostart_enabled);
        std::fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn damaged_settings_are_kept_aside_and_defaults_allow_startup() {
        let s = store();
        s.save(Preferences::default(), || Ok(s.base.join("pluxy app")))
            .unwrap();
        std::fs::write(s.file(), b"{broken").unwrap();
        assert_eq!(s.load(), Preferences::default());
        assert!(!s.file().exists());
        let backups: Vec<_> = std::fs::read_dir(&s.config)
            .unwrap()
            .map(|p| p.unwrap().path())
            .collect();
        assert_eq!(backups.len(), 1);
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        s.save(Preferences::default(), || Ok(s.base.join("pluxy app")))
            .unwrap();
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        std::fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn failed_save_restores_registration_and_does_not_change_preferences() {
        let s = store();
        s.save(Preferences::default(), || Ok(s.base.join("pluxy app")))
            .unwrap();
        // A directory where the settings file goes makes the atomic writer's rename fail, for
        // root as well (the writer's temporary names are random, so they cannot be blocked).
        std::fs::remove_file(s.file()).unwrap();
        std::fs::create_dir(s.file()).unwrap();
        assert!(s
            .save(
                Preferences {
                    autostart_enabled: true,
                    ..Preferences::default()
                },
                || Ok(s.base.join("pluxy app"))
            )
            .is_err());
        assert!(!s.entry().exists());
        assert!(s.file().is_dir(), "the failed save replaced the settings");
        assert_eq!(s.load(), Preferences::default());
        std::fs::remove_dir_all(&s.base).unwrap();
    }
    #[test]
    fn input_is_complete_but_stored_settings_allow_future_fields() {
        assert!(serde_json::from_str::<Input>(r#"{"autostart_enabled":true}"#).is_err());
        let p: Preferences =
            serde_json::from_str(r#"{"silent_start_enabled":true,"future":42}"#).unwrap();
        assert!(p.silent_start_enabled);
        assert!(p.start_core_on_launch);
        assert!(!wants_silent(p, false, true));
        assert!(wants_silent(p, true, true));
        assert!(!wants_silent(p, true, false));
    }

    #[test]
    fn registration_is_read_from_owned_entry_and_foreign_files_are_preserved() {
        let s = store();
        let on = Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        };
        s.save(on, || Ok(s.base.join("pluxy app"))).unwrap();
        let entry = fs::read_to_string(s.entry()).unwrap();
        assert!(
            s.view(Preferences::default())
                .unwrap()
                .preferences
                .autostart_enabled
        );
        for disabled in ["Hidden=true", "X-GNOME-Autostart-enabled=false"] {
            fs::write(s.entry(), format!("{entry}{disabled}\n")).unwrap();
            assert!(!s.view(on).unwrap().preferences.autostart_enabled);
        }
        fs::write(s.entry(), &entry).unwrap();
        let before = fs::metadata(s.entry()).unwrap().modified().unwrap();
        s.save(on, || {
            panic!("unchanged registration must not resolve the executable")
        })
        .unwrap();
        assert_eq!(fs::metadata(s.entry()).unwrap().modified().unwrap(), before);
        fs::write(
            s.entry(),
            "[Desktop Entry]\nType=Application\nExec=/my/program\n",
        )
        .unwrap();
        let foreign = fs::read(s.entry()).unwrap();
        assert!(s.view(on).unwrap().registration_conflict);
        assert!(!s.view(on).unwrap().preferences.autostart_enabled);
        assert!(s.save(on, || Ok(s.base.join("pluxy app"))).is_err());
        s.save(Preferences::default(), || {
            panic!("disabling foreign entry must not resolve executable")
        })
        .unwrap();
        assert_eq!(fs::read(s.entry()).unwrap(), foreign);
        fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn executable_uses_appimage_and_rejects_unusable_paths() {
        let s = store();
        let appimage = s.base.join("pluxy.AppImage");
        fs::write(&appimage, b"fixture").unwrap();
        assert_eq!(
            executable(Some(appimage.clone().into_os_string()), || panic!(
                "must prefer AppImage"
            ))
            .unwrap(),
            appimage
        );
        assert!(executable(None, || Err(std::io::Error::other("unreadable exe"))).is_err());
        for name in ["missing", "app (deleted)", "app%name", "app\\name"] {
            let path = s.base.join(name);
            if name != "missing" {
                fs::write(&path, b"fixture").unwrap();
            }
            assert!(desktop_entry(&path, "pluxy").is_err(), "{name}");
        }
        assert!(desktop_entry(&s.base, "pluxy").is_err());
        fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn desktop_entry_quotes_reserved_characters() {
        let s = store();
        let path = s.base.join("space quote\" dollar$ tick` ;&=");
        fs::write(&path, b"fixture").unwrap();
        let text = desktop_entry(&path, "pluxy").unwrap();
        let expected = format!("[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"{}/space quote\\\\\" dollar\\\\$ tick\\\\` ;&=\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n", s.base.display());
        assert_eq!(text, expected);
        fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn a_local_builds_login_item_is_named_as_its_window() {
        let s = store();
        let path = s.base.join("pluxy-app-local");
        fs::write(&path, b"fixture").unwrap();
        let text = desktop_entry(&path, crate::display_name(true)).unwrap();
        assert_eq!(text, format!("[Desktop Entry]\nType=Application\nName=pluxy (local build)\nExec=\"{}/pluxy-app-local\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n", s.base.display()));
        fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn a_build_without_sign_in_never_registers_and_ignores_an_earlier_builds_entry() {
        let earlier = store();
        let s = Store::new(
            earlier.config.clone(),
            earlier.base.clone(),
            earlier.identifier.clone(),
        )
        .without_sign_in();
        let on = Preferences {
            autostart_enabled: true,
            silent_start_enabled: true,
            start_core_on_launch: true,
        };
        let refused = s.save(on, || {
            panic!("a refused save must not resolve the executable")
        });
        assert_eq!(refused.err().as_deref(), Some(NO_SIGN_IN));
        assert!(!s.entry().exists(), "it registered a login item");
        assert!(!s.file().exists(), "a refused save wrote the settings");
        // An entry an earlier build wrote, pluxy's own and turned on, is neither shown nor changed.
        earlier.save(on, || Ok(s.base.join("pluxy app"))).unwrap();
        assert!(earlier.view(on).unwrap().preferences.autostart_enabled);
        let entry = fs::read(s.entry()).unwrap();
        let view = s.view(on).unwrap();
        assert!(!view.sign_in);
        assert!(!view.preferences.autostart_enabled);
        assert!(!view.preferences.silent_start_enabled);
        assert!(!view.registration_owned && !view.registration_conflict);
        let off = Preferences {
            autostart_enabled: false,
            silent_start_enabled: true,
            start_core_on_launch: false,
        };
        let saved = s
            .save(off, || panic!("no executable is needed"))
            .unwrap()
            .preferences;
        assert_eq!(
            saved,
            Preferences {
                silent_start_enabled: false,
                ..off
            }
        );
        assert_eq!(s.load(), saved);
        assert_eq!(
            fs::read(s.entry()).unwrap(),
            entry,
            "it changed the earlier entry"
        );
        assert!(earlier.view(on).unwrap().sign_in);
        fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn only_a_login_launch_of_a_build_without_sign_in_is_refused() {
        let args = |a: &[&str]| {
            a.iter()
                .map(std::ffi::OsString::from)
                .collect::<Vec<_>>()
                .into_iter()
        };
        assert!(refused_launch(
            false,
            args(&["pluxy-app-local", "--autostart"])
        ));
        assert!(!refused_launch(
            false,
            args(&["pluxy-app-local", "--safe-mode"])
        ));
        assert!(!refused_launch(true, args(&["pluxy-app", "--autostart"])));
    }
}

/// The macOS login item's format, checked wherever the tests run (it is plain data).
#[cfg(all(test, not(windows)))]
mod launch_agent_tests {
    use super::*;

    const ID: &str = "dev.pluxy.desktop";

    fn folder() -> PathBuf {
        let d = std::env::temp_dir().join(format!("pluxy-agent-{}", crate::managed::random_key()));
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn value(bytes: &[u8]) -> plist::Dictionary {
        plist::Value::from_reader(std::io::Cursor::new(bytes))
            .unwrap()
            .into_dictionary()
            .unwrap()
    }

    #[test]
    fn the_agent_runs_the_app_with_the_argument_for_this_user_and_carries_the_mark() {
        let dir = folder();
        // Characters a property list escapes survive.
        let exe = dir.join("pluxy <app> & \"co\" 'x'");
        fs::write(&exe, b"fixture").unwrap();
        let bytes = launch_agent(&exe, ID).unwrap();
        assert!(std::str::from_utf8(&bytes).unwrap().starts_with("<?xml"));
        let d = value(&bytes);
        assert_eq!(d.get("Label").unwrap().as_string(), Some(ID));
        let args: Vec<_> = d
            .get("ProgramArguments")
            .unwrap()
            .as_array()
            .unwrap()
            .iter()
            .map(|a| a.as_string().unwrap().to_owned())
            .collect();
        assert_eq!(args, [exe.to_str().unwrap(), "--autostart"]);
        assert_eq!(d.get("RunAtLoad").unwrap().as_boolean(), Some(true));
        assert_eq!(
            d.get("LimitLoadToSessionType").unwrap().as_string(),
            Some("Aqua")
        );
        assert_eq!(d.get("X-Pluxy-Autostart").unwrap().as_boolean(), Some(true));
        // Nothing machine-wide, nothing that keeps it alive.
        for key in ["KeepAlive", "UserName", "GroupName", "Disabled"] {
            assert!(d.get(key).is_none(), "{key}");
        }
        let r = parse_launch_agent(&bytes, ID);
        assert!(r.exists && r.owned && r.enabled);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn an_agent_turned_off_is_pluxys_and_off() {
        let dir = folder();
        let exe = dir.join("pluxy");
        fs::write(&exe, b"fixture").unwrap();
        let mut d = value(&launch_agent(&exe, ID).unwrap());
        d.insert("Disabled".into(), plist::Value::Boolean(true));
        let mut out = Vec::new();
        plist::Value::Dictionary(d).to_writer_xml(&mut out).unwrap();
        let r = parse_launch_agent(&out, ID);
        assert!(r.exists && r.owned && !r.enabled);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn another_programs_agent_is_not_pluxys() {
        let dir = folder();
        let exe = dir.join("pluxy");
        fs::write(&exe, b"fixture").unwrap();
        let good = launch_agent(&exe, ID).unwrap();
        let edit = |f: &dyn Fn(&mut plist::Dictionary)| {
            let mut d = value(&good);
            f(&mut d);
            let mut out = Vec::new();
            plist::Value::Dictionary(d).to_writer_xml(&mut out).unwrap();
            out
        };
        let others = [
            edit(&|d| {
                d.remove("X-Pluxy-Autostart");
            }),
            edit(&|d| {
                d.insert("Label".into(), plist::Value::String("com.other.app".into()));
            }),
            edit(&|d| {
                d.insert(
                    "ProgramArguments".into(),
                    plist::Value::Array(vec![plist::Value::String("/bin/other".into())]),
                );
            }),
            b"not a property list".to_vec(),
            Vec::new(),
        ];
        for other in others {
            let r = parse_launch_agent(&other, ID);
            assert!(r.exists && !r.owned && !r.enabled);
        }
        // Another app's identifier is not this one's.
        assert!(!parse_launch_agent(&good, "dev.other.desktop").owned);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn paths_the_agent_cannot_name_are_refused() {
        let dir = folder();
        let missing = dir.join("missing");
        assert!(launch_agent(&missing, ID).is_err());
        assert!(launch_agent(Path::new("relative/app"), ID).is_err());
        assert!(launch_agent(&dir, ID).is_err());
        let percent = dir.join("100%");
        fs::write(&percent, b"fixture").unwrap();
        assert!(launch_agent(&percent, ID).is_err());
        fs::remove_dir_all(dir).unwrap();
    }
}

/// The macOS registration end to end through the store, in a folder standing for `~/Library`.
#[cfg(all(test, target_os = "macos"))]
mod macos_tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    const ID: &str = "dev.pluxy.desktop";

    fn store() -> Store {
        let root =
            std::env::temp_dir().join(format!("pluxy-startup-{}", crate::managed::random_key()));
        fs::create_dir_all(&root).unwrap();
        fs::write(root.join("pluxy app"), b"fixture").unwrap();
        Store::new(root.join(ID), root, ID.into())
    }

    fn on() -> Preferences {
        Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        }
    }

    #[test]
    fn the_agent_is_written_in_the_users_launch_agents_and_removed() {
        let s = store();
        let exe = s.base.join("pluxy app");
        assert_eq!(
            s.entry(),
            s.base.join("LaunchAgents/dev.pluxy.desktop.plist")
        );
        s.save(on(), || Ok(exe.clone())).unwrap();
        let bytes = fs::read(s.entry()).unwrap();
        assert_eq!(bytes, launch_agent(&exe, ID).unwrap());
        // launchd refuses a plist others can write.
        assert_eq!(
            fs::metadata(s.entry()).unwrap().permissions().mode() & 0o022,
            0
        );
        assert!(
            s.view(Preferences::default())
                .unwrap()
                .preferences
                .autostart_enabled
        );
        s.save(Preferences::default(), || {
            panic!("removing does not resolve the executable")
        })
        .unwrap();
        assert!(!s.entry().exists());
        assert!(!s.load().autostart_enabled);
        fs::remove_dir_all(&s.base).unwrap();
    }

    #[test]
    fn a_foreign_agent_is_kept_and_a_failed_save_puts_the_agent_back() {
        let s = store();
        let exe = s.base.join("pluxy app");
        fs::create_dir_all(s.entry().parent().unwrap()).unwrap();
        fs::write(s.entry(), b"<plist version=\"1.0\"><dict/></plist>").unwrap();
        let foreign = fs::read(s.entry()).unwrap();
        assert!(s.view(on()).unwrap().registration_conflict);
        assert!(s.save(on(), || Ok(exe.clone())).is_err());
        s.save(Preferences::default(), || panic!("must not resolve"))
            .unwrap();
        assert_eq!(fs::read(s.entry()).unwrap(), foreign);
        fs::remove_file(s.entry()).unwrap();
        // A directory where the settings file goes makes the writer's rename fail.
        s.save(Preferences::default(), || Ok(exe.clone())).unwrap();
        fs::remove_file(s.file()).unwrap();
        fs::create_dir(s.file()).unwrap();
        assert!(s.save(on(), || Ok(exe.clone())).is_err());
        assert!(!s.entry().exists(), "the failed save left the agent");
        fs::remove_dir_all(&s.base).unwrap();
    }
}

/// A change that fails once it is made is undone, on the systems whose login item is a file.
#[cfg(all(test, not(windows)))]
mod rollback_tests {
    use super::*;
    use std::sync::atomic::Ordering;

    fn store() -> (Store, PathBuf) {
        let root =
            std::env::temp_dir().join(format!("pluxy-rollback-{}", crate::managed::random_key()));
        fs::create_dir_all(&root).unwrap();
        let exe = root.join("pluxy app");
        fs::write(&exe, b"fixture").unwrap();
        (
            Store::new(
                root.join("config"),
                root.clone(),
                "dev.pluxy.desktop".into(),
            ),
            exe,
        )
    }

    fn on() -> Preferences {
        Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        }
    }

    #[test]
    fn an_item_made_and_then_failed_is_not_left_and_one_removed_and_then_failed_is_put_back() {
        let (s, exe) = store();
        let e = exe.clone();
        // Making it fails after it was made: nothing is left, and the settings did not change.
        s.fault.store(true, Ordering::SeqCst);
        assert!(s.save(on(), || Ok(e.clone())).is_err());
        assert!(!s.entry().exists(), "the failed save left the item");
        assert!(!s.file().exists(), "the failed save wrote the settings");
        // The positive control on the same route: without the failure it is made.
        s.fault.store(false, Ordering::SeqCst);
        s.save(on(), || Ok(e.clone())).unwrap();
        let made = fs::read(s.entry()).unwrap();
        assert!(s.registration().unwrap().enabled);
        // Removing it fails after it was removed: it is back, as it was.
        s.fault.store(true, Ordering::SeqCst);
        let err = s
            .save(Preferences::default(), || {
                panic!("removing does not resolve the app")
            })
            .err()
            .expect("the removal was to fail");
        assert!(err.contains("did not complete"), "{err}");
        assert_eq!(
            fs::read(s.entry()).unwrap(),
            made,
            "the failed removal did not put it back"
        );
        assert!(s.registration().unwrap().enabled);
        // Reading the entry back fails after it was changed: it is put back, and both are reported.
        s.fault.store(false, Ordering::SeqCst);
        s.read_fault.store(true, Ordering::SeqCst);
        let err = s
            .save(Preferences::default(), || {
                panic!("removing does not resolve the app")
            })
            .err()
            .expect("the read was to fail");
        assert!(err.contains("could not be read"), "{err}");
        assert_eq!(
            fs::read(s.entry()).unwrap(),
            made,
            "the failed read did not put it back"
        );
        s.read_fault.store(false, Ordering::SeqCst);
        s.save(Preferences::default(), || Ok(e.clone())).unwrap();
        assert!(!s.entry().exists());
        s.read_fault.store(true, Ordering::SeqCst);
        assert!(s.save(on(), || Ok(e.clone())).is_err());
        assert!(!s.entry().exists(), "the failed read left the new item");
        s.read_fault.store(false, Ordering::SeqCst);
        // An app path that cannot be had changes nothing at all.
        s.fault.store(false, Ordering::SeqCst);
        s.save(Preferences::default(), || Ok(e.clone())).unwrap();
        assert!(!s.entry().exists());
        assert!(s.save(on(), || Err("no path".into())).is_err());
        assert!(!s.entry().exists());
        fs::remove_dir_all(exe.parent().unwrap()).unwrap();
    }
}
