//! Privacy mode, kept in `privacy-mode.json` beside `connection.json` (a JSON `true` or `false`,
//! readable only by this user). It is kept outside the window's webview so that losing the
//! webview's storage cannot turn it off while the person shares their screen; for the same
//! reason a file that cannot be read or parsed counts as on.

use crate::managed::write_private;
use std::fs;
use std::path::Path;

pub const FILE: &str = "privacy-mode.json";

/// The saved choice: off when nothing is saved, on when the file is unreadable or damaged.
pub fn load(file: &Path) -> bool {
    match fs::read(file) {
        Ok(bytes) => serde_json::from_slice::<bool>(&bytes).unwrap_or_else(|e| {
            eprintln!(
                "pluxy-app: damaged privacy mode preference {}: {e}; privacy mode is on",
                file.display()
            );
            true
        }),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => false,
        Err(e) => {
            eprintln!(
                "pluxy-app: cannot read {}: {e}; privacy mode is on",
                file.display()
            );
            true
        }
    }
}

pub fn save(file: &Path, on: bool) -> Result<(), String> {
    if let Some(dir) = file.parent() {
        fs::create_dir_all(dir).map_err(|e| format!("saving privacy mode: {e}"))?;
    }
    let bytes: &[u8] = if on { b"true" } else { b"false" };
    write_private(file, bytes).map_err(|e| format!("saving privacy mode: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp() -> std::path::PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "pluxy-privacy-{}-{}",
            std::process::id(),
            crate::managed::random_key()
        ));
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn off_until_saved_then_read_back_and_private() {
        let dir = temp();
        let file = dir.join(FILE);
        assert!(!load(&file));
        for on in [true, false, true] {
            save(&file, on).unwrap();
            assert_eq!(load(&file), on);
        }
        assert_eq!(fs::read(&file).unwrap(), b"true");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&file).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_damaged_file_counts_as_on() {
        let dir = temp();
        let file = dir.join(FILE);
        for damaged in [&b"not json"[..], b"\"on\"", b"1", b""] {
            fs::write(&file, damaged).unwrap();
            assert!(load(&file), "{damaged:?}");
        }
        fs::remove_dir_all(dir).unwrap();
    }
}
