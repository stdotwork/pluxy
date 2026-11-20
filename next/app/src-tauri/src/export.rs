//! Files the window's own pages save for the person: a theme file, a log, a usage table. The
//! window cannot download them as a browser would (it opens no address but the app's own, and a
//! download link's address is a `blob:` one), so its pages hand the text to the `file_save` command.
//!
//! A file goes into the person's Downloads folder, as a browser's download does, under the name
//! the page gives, and the page shows the path. It never replaces a file: a name already there
//! gets ` (1)`, ` (2)` and so on before its extension. The name is a file name, never a path:
//! separators and what a file system or a shell reads specially become `_`.

use std::fs::OpenOptions;
use std::io::{self, Write};
use std::path::{Path, PathBuf};

/// The most bytes one file holds.
pub const MAX_BYTES: usize = 64 << 20;
/// How many names are tried for one file before giving up.
const MAX_TRIES: u32 = 1000;

/// The file name to save under for the name a page gives: path separators, the characters Windows
/// refuses in a name, and control and format characters become `_`; leading dots and white space
/// and trailing dots and white space go (no hidden file, no `..`); a Windows device name such as
/// `nul.json` gets `_` in front; it is cut to 200 bytes; an empty name is `file`.
pub fn file_name(name: &str) -> String {
    let replaced: String = name
        .chars()
        .map(|c| {
            let special = matches!(c, '/' | '\\' | ':' | '*' | '?' | '"' | '<' | '>' | '|')
                || c.is_control()
                || matches!(c, '\u{2028}' | '\u{2029}')
                || crate::format_chars::is_format(c);
            if special {
                '_'
            } else {
                c
            }
        })
        .collect();
    let mut out = replaced
        .trim_start_matches(|c: char| c == '.' || c.is_whitespace())
        .trim_end_matches(|c: char| c == '.' || c.is_whitespace())
        .to_string();
    while out.len() > 200 {
        out.pop();
    }
    if out.is_empty() {
        return "file".into();
    }
    let stem = out.split('.').next().unwrap_or("").to_ascii_uppercase();
    let device = matches!(stem.as_str(), "CON" | "PRN" | "AUX" | "NUL")
        || (stem.len() == 4
            && (stem.starts_with("COM") || stem.starts_with("LPT"))
            && stem.ends_with(|c: char| c.is_ascii_digit()));
    if device {
        out.insert(0, '_');
    }
    out
}

/// The nth name to try for name: name itself, then ` (n)` before its last extension.
fn numbered(name: &str, n: u32) -> String {
    if n == 0 {
        return name.into();
    }
    match name.rfind('.') {
        Some(dot) if dot > 0 => format!("{} ({n}){}", &name[..dot], &name[dot..]),
        _ => format!("{name} ({n})"),
    }
}

/// Saves bytes in folder (made if it is not there) as a new file named after name, and returns its
/// path. Nothing is left behind when it fails.
pub fn save(folder: &Path, name: &str, bytes: &[u8]) -> Result<PathBuf, String> {
    if bytes.len() > MAX_BYTES {
        return Err(format!(
            "the file is over {} MiB",
            MAX_BYTES / (1024 * 1024)
        ));
    }
    std::fs::create_dir_all(folder).map_err(|e| format!("{}: {e}", folder.display()))?;
    let name = file_name(name);
    for n in 0..MAX_TRIES {
        let path = folder.join(numbered(&name, n));
        let mut file = match OpenOptions::new().write(true).create_new(true).open(&path) {
            Ok(file) => file,
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(format!("{}: {e}", path.display())),
        };
        if let Err(e) = file.write_all(bytes).and_then(|()| file.sync_all()) {
            drop(file);
            let _ = std::fs::remove_file(&path);
            return Err(format!("{}: {e}", path.display()));
        }
        return Ok(path);
    }
    Err(format!(
        "{} already holds {MAX_TRIES} files named like {name}",
        folder.display()
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp() -> PathBuf {
        let mut rnd = [0u8; 8];
        getrandom::fill(&mut rnd).unwrap();
        let name: String = rnd.iter().map(|b| format!("{b:02x}")).collect();
        std::env::temp_dir().join(format!("pluxy-export-{name}"))
    }

    #[test]
    fn a_name_is_never_a_path_or_a_special_file() {
        assert_eq!(file_name("nightfall.json"), "nightfall.json");
        assert_eq!(file_name("../../etc/passwd"), "_.._etc_passwd");
        assert_eq!(file_name("..\\a:b*?.txt"), "_a_b__.txt");
        assert_eq!(file_name(" .hidden "), "hidden");
        assert_eq!(file_name("a\u{202e}b\n.json"), "a_b_.json");
        assert_eq!(file_name("log. . "), "log");
        assert_eq!(file_name(".."), "file");
        assert_eq!(file_name(""), "file");
        assert_eq!(file_name("nul.json"), "_nul.json");
        assert_eq!(file_name("COM1"), "_COM1");
        assert_eq!(file_name("common.json"), "common.json");
        assert!(file_name(&"x".repeat(500)).len() <= 200);
    }

    #[test]
    fn a_save_never_replaces_a_file_and_makes_its_folder() {
        let folder = temp().join("Downloads");
        let first = save(&folder, "nightfall.json", b"{\"a\":1}\n").unwrap();
        assert_eq!(first, folder.join("nightfall.json"));
        let second = save(&folder, "nightfall.json", b"second").unwrap();
        assert_eq!(second, folder.join("nightfall (1).json"));
        let third = save(&folder, "nightfall.json", b"third").unwrap();
        assert_eq!(third, folder.join("nightfall (2).json"));
        assert_eq!(std::fs::read(&first).unwrap(), b"{\"a\":1}\n");
        assert_eq!(std::fs::read(&second).unwrap(), b"second");
        // No extension, and a name that is only an extension.
        assert_eq!(save(&folder, "notes", b"").unwrap(), folder.join("notes"));
        assert_eq!(
            save(&folder, "notes", b"").unwrap(),
            folder.join("notes (1)")
        );
        // A path stays inside the folder.
        let inside = save(&folder, "../outside.json", b"x").unwrap();
        assert_eq!(inside.parent(), Some(folder.as_path()));
        assert!(!folder.parent().unwrap().join("outside.json").exists());
        std::fs::remove_dir_all(folder.parent().unwrap()).unwrap();
    }

    #[test]
    fn a_save_that_cannot_be_made_says_where() {
        let root = temp();
        std::fs::create_dir_all(&root).unwrap();
        // A file where the folder should be.
        let blocked = root.join("Downloads");
        std::fs::write(&blocked, b"").unwrap();
        let e = save(&blocked, "a.json", b"x").unwrap_err();
        assert!(e.contains("Downloads"), "{e}");
        std::fs::remove_dir_all(&root).unwrap();
    }
}
