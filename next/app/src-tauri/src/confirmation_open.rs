//! A launch that opens a file, on Windows and macOS (`confirmation.rs` has Linux's rules). These
//! systems choose the app by the file's extension (the shell's association; LaunchServices' type
//! from the extension), not by its content, so the type is the extension's, from a fixed list, and
//! the opener is fixed: on Windows the execution program's (`pluxy/execution`, a reserved
//! publisher's, so from a curated source only) own program in its `open` mode (which calls
//! `ShellExecuteExW`), on macOS `/usr/bin/open`.
//!
//! The file must be a regular file of one of `TYPES`, inside its within folder, which is neither
//! a root, the home folder nor a folder above it. The file, the within folder and every folder
//! between them must be this user's and writable by no one else: on Windows by their owner and
//! access list (`win_acl.rs`), on macOS by their owner, mode and no access list entry at all. On
//! Windows the file is on a local drive, names no alternate data stream, and its path is shorter
//! than 260 characters (the shell's limit). On macOS it has no execute bit and none of the extended
//! attributes that can choose another app for it.

#[cfg(windows)]
use super::identity;
use super::{OpenTarget, Target};
use std::fs;
use std::path::{Path, PathBuf};

/// The extensions a launch may open, with the type each is shown as: the plain-text types Linux
/// allows by content.
const TYPES: &[(&str, &str)] = &[
    ("txt", "text/plain"),
    ("json", "application/json"),
    ("jsonl", "application/jsonl"),
    ("ndjson", "application/x-ndjson"),
    ("md", "text/markdown"),
    ("log", "text/x-log"),
];

/// The opener on macOS: protected by System Integrity Protection.
#[cfg(target_os = "macos")]
const OPEN: &str = "/usr/bin/open";

/// The extended attributes that can choose another app for a file on macOS: Finder's type and
/// creator codes, the old resource fork and "Open with".
#[cfg(target_os = "macos")]
const CHOOSERS: &[&str] = &[
    "com.apple.FinderInfo",
    "com.apple.ResourceFork",
    "com.apple.LaunchServices.OpenWith",
];

/// The type path is opened as, from its extension.
fn type_of(path: &Path) -> Option<&'static str> {
    let e = path.extension()?.to_str()?.to_ascii_lowercase();
    TYPES.iter().find(|(x, _)| *x == e).map(|(_, t)| *t)
}

/// The resolved path p as a person reads it and the opener gets it. On Windows `C:\…`, without
/// `\\?\` when the path without it names the same file; None for a network path or one that names
/// another file without the prefix (a name ending in a dot or a space). On macOS p itself.
pub(super) fn plain(p: &Path) -> Option<String> {
    let s = p.to_str()?;
    #[cfg(windows)]
    {
        let rest = s.strip_prefix(r"\\?\").unwrap_or(s);
        let b = rest.as_bytes();
        if !(b.len() >= 3 && b[0].is_ascii_alphabetic() && b[1] == b':' && b[2] == b'\\') {
            return None;
        }
        if rest != s && fs::canonicalize(rest).ok()?.as_path() != p {
            return None;
        }
        Some(rest.to_string())
    }
    #[cfg(not(windows))]
    Some(s.to_string())
}

/// p as the confirmation shows it: its plain form when it has one.
fn shown(p: &Path) -> String {
    plain(p).unwrap_or_else(|| p.display().to_string())
}

/// Refused unless path (resolved) is a file, or with folder a folder, of this user's that no one
/// else may write.
#[cfg(windows)]
fn own(path: &Path, folder: bool, what: &str) -> Result<(), String> {
    crate::win_acl::check(path, folder, crate::win_acl::Others::Read)
        .map_err(|e| format!("{what} must be your own, writable by no one else: {e}"))
}

/// Refused unless path (resolved) is a file, or with folder a folder, of this user's that no one
/// else may write: its owner, its mode and no access list entry at all.
#[cfg(target_os = "macos")]
fn own(path: &Path, folder: bool, what: &str) -> Result<(), String> {
    use std::os::unix::fs::MetadataExt;
    let m = fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    if m.file_type().is_symlink() || m.is_dir() != folder || (!folder && !m.is_file()) {
        return Err(format!(
            "{what} is no longer a {}",
            if folder { "folder" } else { "regular file" }
        ));
    }
    if m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o022 != 0 {
        return Err(format!("{what} must be your own, writable by no one else"));
    }
    if has_acl(path)? {
        return Err(format!(
            "{what} has an access list, which may let others write it: remove it (chmod -N)"
        ));
    }
    Ok(())
}

/// Whether path (not followed) has an access list with any entry.
#[cfg(target_os = "macos")]
fn has_acl(path: &Path) -> Result<bool, String> {
    use std::ffi::{c_int, c_void, CString};
    use std::os::unix::ffi::OsStrExt;
    /// `<sys/acl.h>`.
    const ACL_TYPE_EXTENDED: c_int = 0x0000_0100;
    const ACL_FIRST_ENTRY: c_int = 0;
    extern "C" {
        fn acl_get_link_np(path: *const libc::c_char, kind: c_int) -> *mut c_void;
        fn acl_get_entry(acl: *mut c_void, id: c_int, entry: *mut *mut c_void) -> c_int;
        fn acl_free(object: *mut c_void) -> c_int;
    }
    let c = CString::new(path.as_os_str().as_bytes()).map_err(|e| e.to_string())?;
    // SAFETY: c is a NUL-terminated path; the list it returns is freed below.
    let acl = unsafe { acl_get_link_np(c.as_ptr(), ACL_TYPE_EXTENDED) };
    if acl.is_null() {
        let e = std::io::Error::last_os_error();
        // No access list.
        if e.raw_os_error() == Some(libc::ENOENT) {
            return Ok(false);
        }
        return Err(format!("{}: {e}", path.display()));
    }
    let mut entry = std::ptr::null_mut();
    // SAFETY: acl is a list acl_get_link_np returned; entry points into it, freed with it.
    let any = unsafe { acl_get_entry(acl, ACL_FIRST_ENTRY, &mut entry) } == 0;
    // SAFETY: acl came from acl_get_link_np and is not used after this.
    unsafe { acl_free(acl) };
    Ok(any)
}

/// Refused when the file held in f carries an extended attribute of `CHOOSERS`.
#[cfg(target_os = "macos")]
fn no_choosers(f: &fs::File) -> Result<(), String> {
    use std::os::fd::AsRawFd;
    let fd = f.as_raw_fd();
    // SAFETY: with no buffer, flistxattr returns the size its names need.
    let n = unsafe { libc::flistxattr(fd, std::ptr::null_mut(), 0, 0) };
    if n < 0 {
        return Err(std::io::Error::last_os_error().to_string());
    }
    let mut names = vec![0u8; n as usize];
    // SAFETY: names holds names.len() bytes.
    let n = unsafe { libc::flistxattr(fd, names.as_mut_ptr().cast(), names.len(), 0) };
    if n < 0 {
        return Err(std::io::Error::last_os_error().to_string());
    }
    for name in names[..n as usize].split(|b| *b == 0) {
        if let Some(c) = CHOOSERS.iter().find(|c| c.as_bytes() == name) {
            return Err(format!(
                "the file a launch opens carries {c}, which can choose another app for it"
            ));
        }
    }
    Ok(())
}

/// The rules for a file a launch opens, held in f, inside folder w, both held open (see the
/// module). Its type.
pub(super) fn file_rules(f: &OpenTarget, w: &OpenTarget) -> Result<String, String> {
    let m = f.file.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() {
        return Err("the file a launch opens must be a regular file".into());
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if m.permissions().mode() & 0o111 != 0 {
            return Err("the file a launch opens must not be executable".into());
        }
    }
    let (file, within) = (&f.identity.canonical, &w.identity.canonical);
    if !file.starts_with(within) || file == within {
        return Err("the file a launch opens must be inside its within folder".into());
    }
    let t = type_of(file).ok_or_else(|| {
        let names: Vec<String> = TYPES.iter().map(|(x, _)| format!(".{x}")).collect();
        let (last, rest) = names.split_last().expect("TYPES names at least one type");
        format!(
            "the file a launch opens must be {} or {last}",
            rest.join(", ")
        )
    })?;
    #[cfg(windows)]
    {
        let p = plain(file).ok_or(
            "the file a launch opens must be on a local drive, named by a path Windows reads the same without \\\\?\\",
        )?;
        if p[2..].contains(':') {
            return Err("the file a launch opens must not name an alternate data stream".into());
        }
        // MAX_PATH counts UTF-16 units.
        if p.encode_utf16().count() >= 260 {
            return Err(
                "the file a launch opens must have a path shorter than 260 characters".into(),
            );
        }
    }
    #[cfg(target_os = "macos")]
    no_choosers(&f.file)?;
    own(file, false, "the file a launch opens")?;
    for d in file.ancestors().skip(1) {
        if d == within {
            break;
        }
        own(
            d,
            true,
            &format!("the folder {} of the file a launch opens", shown(d)),
        )?;
    }
    own(within, true, "a launch's within folder")?;
    Ok(t.to_string())
}

/// A launch's file to open and the folder it must be inside, both opened and resolved, each named
/// by its resolved path as a person reads it: the folder neither a root, the home folder nor a
/// folder above it, the file as `file_rules` says.
pub(super) fn within(
    file: &Path,
    within: &Path,
) -> Result<(OpenTarget, OpenTarget, String), String> {
    let w = OpenTarget::open(within)?;
    if !w.file.metadata().map_err(|e| e.to_string())?.is_dir() {
        return Err("a launch's within folder is not a folder".into());
    }
    let home = std::env::home_dir().and_then(|h| fs::canonicalize(h).ok());
    if w.identity.canonical.parent().is_none()
        || home
            .as_ref()
            .is_some_and(|h| h.starts_with(&w.identity.canonical))
    {
        return Err(
            "a launch's within folder cannot be a root, the home folder or a folder above it"
                .into(),
        );
    }
    let f = OpenTarget::open(file)?;
    let t = file_rules(&f, &w)?;
    let named = |t: &Target| plain(&t.canonical).is_some_and(|p| Path::new(&p) == t.selected);
    if !named(&f.identity) || !named(&w.identity) {
        return Err(
            "a launch names the file it opens and its within folder by their resolved paths".into(),
        );
    }
    Ok((f, w, t))
}

/// Whether package, launching executable with adapter its own program, runs that program: on
/// Windows only the execution program's (`launcher::PACKAGE`, a reserved publisher's, which only a
/// curated source may install) counts, compared by identity.
#[cfg(windows)]
fn executions_own(executable: &Target, adapter: Option<&Path>, package: &str) -> bool {
    package == crate::launcher::PACKAGE
        && adapter
            .and_then(|a| identity(a).ok())
            .is_some_and(|i| i == (executable.dev, executable.ino))
}

/// The shape a launch that opens file (resolved) must have: on Windows the execution program's
/// own program (`executions_own`: package launching with adapter its program) with argv exactly
/// `open -- <file>`; on macOS `/usr/bin/open <file>`; the file's folder as its cwd, no terminal
/// program.
pub(super) fn shape(
    executable: &Target,
    adapter: Option<&Path>,
    package: &str,
    argv: &[String],
    cwd: &Path,
    program: Option<&PathBuf>,
    file: &Path,
) -> Result<(), String> {
    let path = plain(file).ok_or("the file a launch opens has no plain path")?;
    #[cfg(windows)]
    let want = {
        if !executions_own(executable, adapter, package) {
            return Err(format!(
                "a launch that opens a file runs {}'s own program in its open mode, not {} of {package}",
                crate::launcher::PACKAGE,
                shown(&executable.canonical)
            ));
        }
        vec!["open".to_string(), "--".into(), path]
    };
    #[cfg(target_os = "macos")]
    let want = {
        let _ = (adapter, package);
        if executable.canonical != Path::new(OPEN) {
            return Err(format!(
                "a launch that opens a file runs {OPEN}, not {}",
                executable.canonical.display()
            ));
        }
        vec![path]
    };
    if argv != want.as_slice() {
        return Err(format!(
            "an open launch runs exactly {} {}",
            shown(&executable.canonical),
            serde_json::to_string(&want).unwrap_or_default()
        ));
    }
    if Some(cwd) != file.parent() {
        return Err("an open launch runs in the folder of the file it opens".into());
    }
    if program.is_some() {
        return Err("an open launch runs no terminal program".into());
    }
    Ok(())
}

/// The confirmation's line for what a launch opens: file, its type and within folder, and how.
pub(super) fn review_line(file: &Path, kind: &str, within: &Path, executable: &Path) -> String {
    let ext = file
        .extension()
        .map(|e| e.to_string_lossy().to_ascii_lowercase())
        .unwrap_or_default();
    let system = if cfg!(windows) { "Windows" } else { "macOS" };
    format!(
        "Opens the file {} ({kind}, inside {}) with the app {system} opens .{ext} files with, through {}.",
        shown(file),
        shown(within),
        shown(executable)
    )
}

/// Whether a confirmed launch runs the execution program's own program (`executions_own`) in its
/// `open` or `app` mode, which shows no window itself: on Windows it starts with no console, so
/// none flashes.
#[cfg(windows)]
pub(super) fn windowless(
    executable: &Target,
    adapter: Option<&Path>,
    package: &str,
    argv: &[String],
) -> bool {
    matches!(argv, [mode, dash, _] if (mode == "open" || mode == "app") && dash == "--")
        && executions_own(executable, adapter, package)
}
