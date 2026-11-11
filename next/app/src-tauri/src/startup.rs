#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    fn store() -> Store {
        let root = std::env::temp_dir().join(format!("pluxy-startup-{}", crate::managed::random_key()));
        Store::new(root.join("dev.pluxy.desktop"), root, "dev.pluxy.desktop".into(), "/opt/pluxy app/bin/pluxy".into())
    }

    #[test]
    fn private_settings_and_independent_choices() {
        let s = store();
        assert_eq!(s.load(), Preferences::default());
        let prefs = Preferences { autostart_enabled: false, silent_start_enabled: true, start_core_on_launch: false };
        s.save(prefs).unwrap();
        assert_eq!(s.load(), prefs);
        assert_eq!(std::fs::metadata(s.file()).unwrap().permissions().mode() & 0o777, 0o600);
        assert!(!s.entry().exists());
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }

    #[test]
    fn xdg_entry_has_exact_content_and_disable_removes_it() {
        let s = store();
        s.save(Preferences { autostart_enabled: true, ..Preferences::default() }).unwrap();
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
        let backups: Vec<_> = std::fs::read_dir(&s.config).unwrap().map(|p| p.unwrap().path()).collect();
        assert_eq!(backups.len(), 1);
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        s.save(Preferences::default()).unwrap();
        assert_eq!(std::fs::read(&backups[0]).unwrap(), b"{broken");
        std::fs::remove_dir_all(&s.xdg).unwrap();
    }
}
