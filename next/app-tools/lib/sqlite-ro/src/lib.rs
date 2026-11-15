//! Opens another program's SQLite database strictly read-only, creating no file beside it.
//!
//! A WAL database whose `-shm` is missing (no process has it open) cannot be opened in place
//! without SQLite making that file, so:
//!
//! - the database path must be a regular file, not a link, a FIFO or anything else, and a `-wal`,
//!   `-shm` or `-journal` beside it that exists must be one too (SQLite opens a `-journal` to check
//!   for a hot one, and a FIFO there would hang it). The database and a `-wal` that is copied are
//!   opened without following a link or waiting on a FIFO, and checked to be regular files on the
//!   open handle;
//! - a database whose `-shm` exists (a process has it open) is opened in place with `mode=ro`;
//! - one with no `-shm` and a `-wal` is never opened in place, whatever its header says: SQLite
//!   would delete a `-wal` that holds bytes beside a database of 0 bytes, or create a `-shm` beside
//!   one whose header is not WAL's. With the `-wal` empty it is opened with `immutable=1`; with the
//!   `-wal` holding bytes it is copied, with its `-wal`, into a private folder (0700) under
//!   `$PLUXY_TOOL_DATA_DIR/tmp` (the system's temporary folder when that is not set), read there,
//!   and the copy is removed when the [`Db`] is dropped;
//! - one with no `-shm` and no `-wal` is opened with `immutable=1` when its header says WAL, and
//!   else in place;
//! - the tool's own `tmp` must be a real folder, not a link; one that is not private is made
//!   private (0700; on Windows owned by this user with a protected access list for this user and
//!   SYSTEM only). Before a copy is made there, the copies left by processes that stopped
//!   (folders named exactly `wal-<pid>-<n>`) are removed. On Linux a copy is left over when `/proc`
//!   does not list its pid. On macOS and Windows a copy has a lock file beside it,
//!   `wal-<pid>-<n>.lock`, made already held before the folder and held until the folder is
//!   removed; a copy is left over when its lock is missing or no process holds it, whatever its
//!   pid (a pid can be reused), and is removed while the remover holds its lock;
//! - a copy's name that is taken already (a leftover with a reused pid) is passed over for the
//!   next.
//!
//! An [`Error`] names no path: a tool that shows one to its user adds it itself.

use rusqlite::{Connection, OpenFlags};
use std::fmt;
use std::fs;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::Duration;

const BUSY_TIMEOUT: Duration = Duration::from_secs(5);

/// Why a database was not opened. None of the messages names a path.
#[derive(Debug)]
pub enum Error {
    /// The database path is not a regular file (a link, a FIFO, a folder, or missing).
    NotRegular,
    /// The named sidecar (`-wal`, `-shm` or `-journal`) exists and is not a regular file.
    SidecarNotRegular(&'static str),
    /// The private copy of the database and its `-wal` could not be made.
    Copy(std::io::Error),
    /// SQLite could not open the database; holds SQLite's error code, described. (SQLite's own
    /// message for an open names the file, so it is not kept.)
    Open(String),
    /// SQLite's busy timeout could not be set; holds SQLite's error code, described.
    Timeout(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::NotRegular => f.write_str("the database is not a regular file"),
            Error::SidecarNotRegular(name) => {
                write!(f, "the database's {name} is not a regular file")
            }
            Error::Copy(e) => write!(f, "could not copy the database to read its -wal: {e}"),
            Error::Open(e) => write!(f, "could not open the database: {e}"),
            Error::Timeout(e) => write!(f, "could not set the database's timeout: {e}"),
        }
    }
}

impl std::error::Error for Error {}

/// An open database, and the private copy it reads (removed once it is closed).
pub struct Db {
    connection: Connection,
    _copy: Option<Copy>,
}

impl std::ops::Deref for Db {
    type Target = Connection;
    fn deref(&self) -> &Connection {
        &self.connection
    }
}

/// A private copy's folder, removed when dropped; and, on macOS and Windows, the lock beside it,
/// released after the folder is gone (see `prune`).
struct Copy {
    dir: PathBuf,
    #[cfg(any(windows, target_os = "macos"))]
    _lock: pluxy_files::Lock,
}

impl Drop for Copy {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.dir);
    }
}

/// The lock file beside copy folder name, on macOS and Windows.
#[cfg(any(windows, target_os = "macos"))]
fn lock_path(root: &Path, name: &str) -> PathBuf {
    root.join(format!("{name}.lock"))
}

/// Where private copies go: the folder, and whether it is the tool's own (so stale copies in it
/// are pruned).
struct CopyRoot {
    dir: PathBuf,
    own: bool,
}

/// Opens a database read-only, creating nothing beside it (see the crate's notes).
pub fn open_read_only(path: &Path) -> Result<Db, Error> {
    open(path, &copy_root())
}

fn open(path: &Path, root: &CopyRoot) -> Result<Db, Error> {
    if !is_regular(path) {
        return Err(Error::NotRegular);
    }
    let wal = sidecar(path, "-wal");
    let shm = sidecar(path, "-shm");
    let journal = sidecar(path, "-journal");
    for (side, name) in [(&wal, "-wal"), (&shm, "-shm"), (&journal, "-journal")] {
        if fs::symlink_metadata(side).is_ok_and(|m| !m.is_file()) {
            return Err(Error::SidecarNotRegular(name));
        }
    }
    let in_place = |immutable| {
        Ok(Db {
            connection: connect(path, immutable)?,
            _copy: None,
        })
    };
    // A process has the database open: share its -shm as any reader does.
    if fs::symlink_metadata(&shm).is_ok() {
        return in_place(false);
    }
    match fs::symlink_metadata(&wal).map(|m| m.len()) {
        Ok(0) => in_place(true),
        Ok(_) => {
            let copy = private_copy(path, &wal, root).map_err(Error::Copy)?;
            Ok(Db {
                connection: connect(&copy.dir.join("db"), false)?,
                _copy: Some(copy),
            })
        }
        Err(_) if is_wal(path) => in_place(true),
        Err(_) => in_place(false),
    }
}

fn copy_root() -> CopyRoot {
    match std::env::var_os("PLUXY_TOOL_DATA_DIR").filter(|d| !d.is_empty()) {
        Some(d) => CopyRoot {
            dir: PathBuf::from(d).join("tmp"),
            own: true,
        },
        None => CopyRoot {
            dir: std::env::temp_dir(),
            own: false,
        },
    }
}

/// Copies db and its -wal into a new folder of this user's that no one else may use. Both are
/// read through handles opened without following a link or waiting on a FIFO.
fn private_copy(db: &Path, wal: &Path, root: &CopyRoot) -> std::io::Result<Copy> {
    let mut db_source = open_regular(db)?;
    let mut wal_source = open_regular(wal)?;
    prepare_root(root)?;
    let copy = new_copy(&root.dir)?;
    for (source, name) in [(&mut db_source, "db"), (&mut wal_source, "db-wal")] {
        let mut dest = pluxy_files::create_new_private(&copy.dir.join(name))?;
        std::io::copy(source, &mut dest)?;
    }
    Ok(copy)
}

/// A new private copy folder in root, `wal-<pid>-<n>` with the first n whose name is free; on macOS
/// and Windows with its lock made and held before the folder, so no prune can take it for a
/// leftover.
fn new_copy(root: &Path) -> std::io::Result<Copy> {
    use std::io::ErrorKind::AlreadyExists;
    use std::sync::atomic::{AtomicU64, Ordering};
    static N: AtomicU64 = AtomicU64::new(0);
    loop {
        let name = format!(
            "wal-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        );
        #[cfg(any(windows, target_os = "macos"))]
        let lock = match pluxy_files::create_locked(&lock_path(root, &name)) {
            Ok(lock) => lock,
            Err(e) if e.kind() == AlreadyExists => continue,
            Err(e) => return Err(e),
        };
        let dir = root.join(&name);
        match pluxy_files::create_private_dir(&dir) {
            Ok(()) => {
                return Ok(Copy {
                    dir,
                    #[cfg(any(windows, target_os = "macos"))]
                    _lock: lock,
                })
            }
            Err(e) if e.kind() == AlreadyExists => continue,
            Err(e) => return Err(e),
        }
    }
}

/// Makes root usable for copies. The tool's own `tmp` must be a real folder (not a link, so nothing
/// is made or removed elsewhere through it) that only this user can use; the system's temporary
/// folder is made if missing and left as it is.
fn prepare_root(root: &CopyRoot) -> std::io::Result<()> {
    use std::io::{Error as IoError, ErrorKind};
    if !root.own {
        return pluxy_files::create_private_dir_all(&root.dir);
    }
    let check = || -> std::io::Result<fs::Metadata> {
        let m = fs::symlink_metadata(&root.dir)?;
        if m.file_type().is_symlink() {
            return Err(IoError::new(
                ErrorKind::InvalidInput,
                "the copy folder is a link",
            ));
        }
        if !m.is_dir() {
            return Err(IoError::new(
                ErrorKind::InvalidInput,
                "the copy folder is not a folder",
            ));
        }
        Ok(m)
    };
    match check() {
        Err(e) if e.kind() == ErrorKind::NotFound => {
            pluxy_files::create_private_dir_all(&root.dir)?;
            check()?
        }
        other => other?,
    };
    if !pluxy_files::is_private(&root.dir)? {
        pluxy_files::make_private(&root.dir)?;
    }
    prune(&root.dir);
    Ok(())
}

/// Removes the copies in root that a process that stopped left behind (cut short). Only a folder
/// named exactly `wal-<pid>-<n>` is a copy.
#[cfg(not(any(windows, target_os = "macos")))]
fn prune(root: &Path) {
    let Ok(entries) = fs::read_dir(root) else {
        return;
    };
    for e in entries.flatten() {
        let pid = e.file_name().to_str().and_then(copy_pid);
        if let Some(pid) = pid {
            if e.file_type().is_ok_and(|t| t.is_dir()) && stale(pid) {
                let _ = fs::remove_dir_all(e.path());
            }
        }
    }
}

/// Removes the copies in root that a process that stopped left behind (cut short), and their
/// locks: a copy whose lock is missing or held by no process, whatever its pid. Each is removed
/// while this process holds its lock, so no copy can be made under its name meanwhile. Only a
/// folder named exactly `wal-<pid>-<n>` is a copy; a lock left with no folder goes too.
#[cfg(any(windows, target_os = "macos"))]
fn prune(root: &Path) {
    use std::io::ErrorKind::AlreadyExists;
    let Ok(entries) = fs::read_dir(root) else {
        return;
    };
    for e in entries.flatten() {
        let Ok(file_name) = e.file_name().into_string() else {
            continue;
        };
        let Ok(kind) = e.file_type() else {
            continue;
        };
        let name = match file_name.strip_suffix(".lock") {
            Some(name) if kind.is_file() => name,
            None if kind.is_dir() => &file_name,
            _ => continue,
        };
        if copy_pid(name).is_none() {
            continue;
        }
        let lock = lock_path(root, name);
        let held = match pluxy_files::create_locked(&lock) {
            Ok(held) => Some(held),
            Err(e) if e.kind() == AlreadyExists => pluxy_files::take_lock(&lock).ok().flatten(),
            Err(_) => None,
        };
        if let Some(held) = held {
            let dir = root.join(name);
            if fs::symlink_metadata(&dir).is_ok_and(|m| m.is_dir()) {
                let _ = fs::remove_dir_all(&dir);
            }
            drop(held);
        }
    }
}

/// The pid in a copy's folder name, `wal-<pid>-<n>` (digits only, the pid above 0).
fn copy_pid(name: &str) -> Option<u32> {
    let (pid, n) = name.strip_prefix("wal-")?.split_once('-')?;
    let digits = |s: &str| !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit());
    if !digits(pid) || !digits(n) {
        return None;
    }
    pid.parse::<u32>().ok().filter(|p| *p > 0)
}

/// Whether the copy made by process pid is left over: that process is no longer running (as
/// `/proc` tells; never when it cannot tell, as where there is no `/proc`).
#[cfg(not(any(windows, target_os = "macos")))]
fn stale(pid: u32) -> bool {
    let proc = Path::new("/proc");
    proc.join("self").exists() && !proc.join(pid.to_string()).exists()
}

/// path as what follows `file:` in a URI, before percent-encoding. On Windows the path must be a
/// full one (SQLite would read any other against its own idea of the current folder); it is
/// given in its long form (`\\?\C:\…`, `\\?\UNC\server\share\…`), which SQLite passes to Windows
/// as it is, so a path past Windows' old 260-character limit opens.
#[cfg(windows)]
fn uri_path(path: &Path) -> Result<Vec<u8>, Error> {
    use std::path::{Component, Prefix};
    let not_full = || Error::Open("the database path is not a full path".into());
    if !path.is_absolute() {
        return Err(not_full());
    }
    let Some(Component::Prefix(prefix)) = path.components().next() else {
        return Err(not_full());
    };
    let long = match prefix.kind() {
        Prefix::Verbatim(_) | Prefix::VerbatimUNC(..) | Prefix::VerbatimDisk(_) => path.to_owned(),
        // Made full first (`.` and `..` resolved, `/` turned to `\\`), which the long form leaves
        // as they are.
        Prefix::Disk(_) | Prefix::UNC(..) => {
            let full = std::path::absolute(path).map_err(|_| not_full())?;
            let s = full.as_os_str().to_str().ok_or_else(not_unicode)?;
            match s.strip_prefix(r"\\") {
                Some(share) => PathBuf::from(format!(r"\\?\UNC\{share}")),
                None => PathBuf::from(format!(r"\\?\{s}")),
            }
        }
        Prefix::DeviceNS(_) => return Err(not_full()),
    };
    Ok(long.to_str().ok_or_else(not_unicode)?.as_bytes().to_vec())
}

#[cfg(windows)]
fn not_unicode() -> Error {
    Error::Open("the database path is not valid Unicode".into())
}

#[cfg(not(windows))]
fn uri_path(path: &Path) -> Result<Vec<u8>, Error> {
    Ok(path.as_os_str().as_encoded_bytes().to_vec())
}

/// A read-only connection to path (immutable when asked).
fn connect(path: &Path, immutable: bool) -> Result<Connection, Error> {
    let mut uri = String::from("file:");
    for b in &uri_path(path)? {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' | b'/' => {
                uri.push(*b as char)
            }
            _ => uri.push_str(&format!("%{b:02X}")),
        }
    }
    uri.push_str("?mode=ro");
    if immutable {
        uri.push_str("&immutable=1");
    }
    // Windows' SQLite VFS that takes paths up to Windows' own limit, not 260 characters.
    if cfg!(windows) {
        uri.push_str("&vfs=win32-longpath");
    }
    let connection = Connection::open_with_flags(
        &uri,
        OpenFlags::SQLITE_OPEN_READ_ONLY
            | OpenFlags::SQLITE_OPEN_NO_MUTEX
            | OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| Error::Open(describe(&e)))?;
    connection
        .busy_timeout(BUSY_TIMEOUT)
        .map_err(|e| Error::Timeout(describe(&e)))?;
    Ok(connection)
}

/// SQLite's error code, described. Its messages can name the file, so they are dropped.
fn describe(e: &rusqlite::Error) -> String {
    match e {
        rusqlite::Error::SqliteFailure(failure, _) => failure.to_string(),
        _ => "unexpected error".to_string(),
    }
}

/// Whether path is a regular file, not through a link.
fn is_regular(path: &Path) -> bool {
    fs::symlink_metadata(path).is_ok_and(|m| m.is_file())
}

fn sidecar(path: &Path, suffix: &str) -> PathBuf {
    let mut s = path.as_os_str().to_os_string();
    s.push(suffix);
    PathBuf::from(s)
}

use pluxy_files::open_regular;

/// Whether path is a SQLite database in WAL mode, by its header.
fn is_wal(path: &Path) -> bool {
    let mut header = [0u8; 20];
    open_regular(path)
        .and_then(|mut f| f.read_exact(&mut header))
        .is_ok_and(|_| &header[..16] == b"SQLite format 3\0" && header[18] == 2)
}

#[cfg(test)]
mod tests;
