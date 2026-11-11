//! Native startup preferences. Only an explicit save changes OS registration.
use crate::managed::write_private;
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};

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
    xdg: PathBuf,
    identifier: String,
}
impl Store {
    pub fn new(config: PathBuf, xdg: PathBuf, identifier: String) -> Self {
        Self {
            config,
            xdg,
            identifier,
        }
    }
    fn file(&self) -> PathBuf {
        self.config.join("startup.json")
    }
    fn entry(&self) -> PathBuf {
        self.xdg
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
        let entry = self.registration()?;
        preferences.autostart_enabled = entry.enabled;
        Ok(View {
            preferences,
            registration_owned: entry.owned,
            registration_conflict: entry.exists && !entry.owned,
        })
    }
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
        let text = fs::read_to_string(path).map_err(|e| e.to_string())?;
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
        Ok(Registration {
            exists: true,
            owned,
            enabled: owned && !disabled,
        })
    }
    pub fn save(
        &self,
        mut prefs: Preferences,
        executable: impl FnOnce() -> Result<PathBuf, String>,
    ) -> Result<View, String> {
        fs::create_dir_all(&self.config).map_err(|e| e.to_string())?;
        let registration = self.registration()?;
        let changed = registration.enabled != prefs.autostart_enabled;
        if changed && registration.exists && !registration.owned {
            return Err("The autostart file belongs to you or another application; pluxy will not replace it".into());
        }
        let previous = if changed && registration.exists {
            Some(fs::read(self.entry()).map_err(|e| e.to_string())?)
        } else {
            None
        };
        if changed {
            if prefs.autostart_enabled {
                if !cfg!(target_os = "linux") {
                    return Err("Launch at startup is supported on Linux only".into());
                }
                let text = desktop_entry(&executable()?)?;
                fs::create_dir_all(self.entry().parent().expect("autostart folder"))
                    .map_err(|e| e.to_string())?;
                write_private(&self.entry(), text.as_bytes()).map_err(|e| e.to_string())?;
            } else {
                fs::remove_file(self.entry()).map_err(|e| e.to_string())?;
            }
        }
        prefs.autostart_enabled = self.registration()?.enabled;
        let bytes = serde_json::to_vec_pretty(&prefs).expect("startup preferences are JSON");
        if let Err(e) = write_private(&self.file(), &bytes) {
            if changed {
                let rollback = match previous {
                    Some(b) => write_private(&self.entry(), &b),
                    None => fs::remove_file(self.entry()),
                };
                if let Err(r) = rollback {
                    return Err(format!(
                        "saving startup preferences: {e}; restoring autostart entry: {r}"
                    ));
                }
            }
            return Err(format!("saving startup preferences: {e}"));
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
    if text.contains(['%', char::from(92)]) {
        return Err("autostart executable paths cannot contain % or backslash".into());
    }
    if !fs::metadata(path)
        .map_err(|e| format!("autostart executable: {e}"))?
        .is_file()
    {
        return Err("autostart executable must be an existing regular file".into());
    }
    Ok(())
}

pub fn wants_silent(preferences: Preferences, autostart: bool) -> bool {
    preferences.silent_start_enabled && autostart
}

/// GTK tray creation can succeed with no desktop host. Never hide a startup window then.
#[cfg(target_os = "linux")]
pub fn tray_host() -> Result<(), String> {
    use gtk::gio::{self, DBusCallFlags};
    use gtk::glib::{ToVariant, Variant};
    let bus = gio::bus_get_sync(gio::BusType::Session, gio::Cancellable::NONE)
        .map_err(|e| e.to_string())?;
    let owner = bus
        .call_sync(
            Some("org.freedesktop.DBus"),
            "/org/freedesktop/DBus",
            "org.freedesktop.DBus",
            "NameHasOwner",
            Some(&("org.kde.StatusNotifierWatcher",).to_variant()),
            None,
            DBusCallFlags::NO_AUTO_START,
            500,
            gio::Cancellable::NONE,
        )
        .map_err(|e| e.to_string())?;
    if owner.get::<(bool,)>() != Some((true,)) {
        return Err("no StatusNotifierWatcher owns the session bus name".into());
    }
    let host = bus
        .call_sync(
            Some("org.kde.StatusNotifierWatcher"),
            "/StatusNotifierWatcher",
            "org.freedesktop.DBus.Properties",
            "Get",
            Some(
                &(
                    "org.kde.StatusNotifierWatcher",
                    "IsStatusNotifierHostRegistered",
                )
                    .to_variant(),
            ),
            None,
            DBusCallFlags::NO_AUTO_START,
            500,
            gio::Cancellable::NONE,
        )
        .map_err(|e| e.to_string())?;
    if host.get::<(Variant,)>().and_then(|(v,)| v.get::<bool>()) != Some(true) {
        return Err("StatusNotifierWatcher has no registered host".into());
    }
    Ok(())
}
#[cfg(not(target_os = "linux"))]
pub fn tray_host() -> Result<(), String> {
    Err("silent startup host detection is supported on Linux only".into())
}

fn desktop_entry(executable: &Path) -> Result<String, String> {
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
    Ok(format!("[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"{quoted}\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n"))
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
        s.save(prefs, || Ok(s.xdg.join("pluxy app"))).unwrap();
        assert_eq!(s.load(), prefs);
        assert_eq!(
            std::fs::metadata(s.file()).unwrap().permissions().mode() & 0o777,
            0o600
        );
        assert!(!s.entry().exists());
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn xdg_entry_has_exact_content_and_disable_removes_it() {
        let s = store();
        s.save(
            Preferences {
                autostart_enabled: true,
                ..Preferences::default()
            },
            || Ok(s.xdg.join("pluxy app")),
        )
        .unwrap();
        assert_eq!(s.entry(), s.xdg.join("autostart/dev.pluxy.desktop.desktop"));
        assert_eq!(std::fs::read_to_string(s.entry()).unwrap(), format!("[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"{}/pluxy app\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n", s.xdg.display()));
        s.save(Preferences::default(), || Ok(s.xdg.join("pluxy app")))
            .unwrap();
        assert!(!s.entry().exists());
        assert!(!s.load().autostart_enabled);
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn damaged_settings_are_kept_aside_and_defaults_allow_startup() {
        let s = store();
        s.save(Preferences::default(), || Ok(s.xdg.join("pluxy app")))
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
        s.save(Preferences::default(), || Ok(s.xdg.join("pluxy app")))
            .unwrap();
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn failed_save_restores_registration_and_does_not_change_preferences() {
        let s = store();
        s.save(Preferences::default(), || Ok(s.xdg.join("pluxy app")))
            .unwrap();
        // An existing directory at the atomic writer's temporary path makes saving fail.
        std::fs::create_dir(s.file().with_extension("tmp")).unwrap();
        assert!(s
            .save(
                Preferences {
                    autostart_enabled: true,
                    ..Preferences::default()
                },
                || Ok(s.xdg.join("pluxy app"))
            )
            .is_err());
        assert!(!s.entry().exists());
        assert_eq!(s.load(), Preferences::default());
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }
    #[test]
    fn input_is_complete_but_stored_settings_allow_future_fields() {
        assert!(serde_json::from_str::<Input>(r#"{"autostart_enabled":true}"#).is_err());
        let p: Preferences =
            serde_json::from_str(r#"{"silent_start_enabled":true,"future":42}"#).unwrap();
        assert!(p.silent_start_enabled);
        assert!(p.start_core_on_launch);
        assert!(!wants_silent(p, false));
        assert!(wants_silent(p, true));
    }

    #[test]
    fn registration_is_read_from_owned_entry_and_foreign_files_are_preserved() {
        let s = store();
        let on = Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        };
        s.save(on, || Ok(s.xdg.join("pluxy app"))).unwrap();
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
        assert!(s.save(on, || Ok(s.xdg.join("pluxy app"))).is_err());
        s.save(Preferences::default(), || {
            panic!("disabling foreign entry must not resolve executable")
        })
        .unwrap();
        assert_eq!(fs::read(s.entry()).unwrap(), foreign);
        fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn executable_uses_appimage_and_rejects_unusable_paths() {
        let s = store();
        let appimage = s.xdg.join("pluxy.AppImage");
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
            let path = s.xdg.join(name);
            if name != "missing" {
                fs::write(&path, b"fixture").unwrap();
            }
            assert!(desktop_entry(&path).is_err(), "{name}");
        }
        assert!(desktop_entry(&s.xdg).is_err());
        fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn desktop_entry_quotes_reserved_characters() {
        let s = store();
        let path = s.xdg.join("space quote\" dollar$ tick` ;&=");
        fs::write(&path, b"fixture").unwrap();
        let text = desktop_entry(&path).unwrap();
        let expected = format!("[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"{}/space quote\\\\\" dollar\\\\$ tick\\\\` ;&=\" --autostart\nTerminal=false\nX-Pluxy-Autostart=true\n", s.xdg.display());
        assert_eq!(text, expected);
        fs::remove_dir_all(&s.xdg).unwrap();
    }
}
