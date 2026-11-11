//! Native startup preferences. Only an explicit save changes OS registration.
use crate::managed::write_private;
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
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
    executable: PathBuf,
}
impl Store {
    pub fn new(config: PathBuf, xdg: PathBuf, identifier: String, executable: PathBuf) -> Self {
        Self {
            config,
            xdg,
            identifier,
            executable,
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
    pub fn save(&self, prefs: Preferences) -> Result<(), String> {
        fs::create_dir_all(&self.config).map_err(|e| e.to_string())?;
        let previous = match fs::read(self.entry()) {
            Ok(bytes) => Some(bytes),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => None,
            Err(e) => return Err(e.to_string()),
        };
        self.register(prefs.autostart_enabled)?;
        let bytes = serde_json::to_vec_pretty(&prefs).expect("startup preferences are JSON");
        if let Err(e) = write_private(&self.file(), &bytes) {
            let rollback = match previous {
                Some(b) => write_private(&self.entry(), &b).map_err(|e| e.to_string()),
                None => self.register(false),
            };
            return Err(match rollback {
                Ok(()) => format!("saving startup preferences: {e}"),
                Err(r) => {
                    format!("saving startup preferences: {e}; restoring autostart entry: {r}")
                }
            });
        }
        Ok(())
    }
    fn register(&self, enabled: bool) -> Result<(), String> {
        if !cfg!(target_os = "linux") {
            return if enabled {
                Err("Launch at startup is supported on Linux only".into())
            } else {
                Ok(())
            };
        }
        let entry = self.entry();
        if enabled {
            let text = desktop_entry(&self.executable)?;
            fs::create_dir_all(entry.parent().expect("autostart folder"))
                .map_err(|e| e.to_string())?;
            write_private(&entry, text.as_bytes()).map_err(|e| e.to_string())
        } else {
            match fs::remove_file(entry) {
                Ok(()) => Ok(()),
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
                Err(e) => Err(e.to_string()),
            }
        }
    }
}
fn desktop_entry(executable: &Path) -> Result<String, String> {
    let path = executable.to_str().ok_or("the app path is not UTF-8")?;
    if !executable.is_absolute() || path.chars().any(char::is_control) {
        return Err("autostart needs an absolute app path without control characters".into());
    }
    // Exec quoting, then Desktop Entry string escaping. Percent escapes field codes.
    let mut quoted = String::new();
    for c in path.chars() {
        if matches!(c, '\\' | '"' | '$') || c == char::from(96) {
            quoted.push('\\');
        }
        quoted.push(c);
        if c == '%' {
            quoted.push('%');
        }
    }
    let quoted = quoted.replace('\\', "\\\\");
    Ok(format!("[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"{quoted}\" --silent-start\nTerminal=false\n"))
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    fn store() -> Store {
        let root =
            std::env::temp_dir().join(format!("pluxy-startup-{}", crate::managed::random_key()));
        Store::new(
            root.join("dev.pluxy.desktop"),
            root,
            "dev.pluxy.desktop".into(),
            "/opt/pluxy app/bin/pluxy".into(),
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
        s.save(prefs).unwrap();
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
        s.save(Preferences {
            autostart_enabled: true,
            ..Preferences::default()
        })
        .unwrap();
        assert_eq!(s.entry(), s.xdg.join("autostart/dev.pluxy.desktop.desktop"));
        assert_eq!(std::fs::read_to_string(s.entry()).unwrap(), "[Desktop Entry]\nType=Application\nName=pluxy\nExec=\"/opt/pluxy app/bin/pluxy\" --silent-start\nTerminal=false\n");
        s.save(Preferences::default()).unwrap();
        assert!(!s.entry().exists());
        assert!(!s.load().autostart_enabled);
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn damaged_settings_are_kept_aside_and_defaults_allow_startup() {
        let s = store();
        s.save(Preferences::default()).unwrap();
        std::fs::write(s.file(), b"{broken").unwrap();
        assert_eq!(s.load(), Preferences::default());
        assert!(!s.file().exists());
        let backups: Vec<_> = std::fs::read_dir(&s.config)
            .unwrap()
            .map(|p| p.unwrap().path())
            .collect();
        assert_eq!(backups.len(), 1);
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        s.save(Preferences::default()).unwrap();
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn failed_save_restores_registration_and_does_not_change_preferences() {
        let s = store();
        s.save(Preferences::default()).unwrap();
        // An existing directory at the atomic writer's temporary path makes saving fail.
        std::fs::create_dir(s.file().with_extension("tmp")).unwrap();
        assert!(s
            .save(Preferences {
                autostart_enabled: true,
                ..Preferences::default()
            })
            .is_err());
        assert!(!s.entry().exists());
        assert_eq!(s.load(), Preferences::default());
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }
}
