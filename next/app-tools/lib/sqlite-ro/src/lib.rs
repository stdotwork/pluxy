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
//! - the tool's own `tmp` must be a real folder, not a link; one open to group or others is
//!   tightened to 0700 (on Windows, given an access list for this user and SYSTEM only). Before a
//!   copy is made there, the copies of processes no longer running (folders named exactly
//!   `wal-<pid>-<n>`) are removed; those of this process and of every process still running stay.
//!   On Linux a process is running when `/proc` lists its pid; elsewhere a copy holds a `lock`
//!   file that its process keeps locked while the copy is open, and a copy is stale once that lock
//!   can be taken (or, with no `lock` file yet, once its folder is a minute old).
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

/// A private copy's folder, removed when dropped; and, outside Linux, its lock (see `prune`).
struct Copy(PathBuf, Option<fs::File>);

impl Drop for Copy {
    fn drop(&mut self) {
        drop(self.1.take());
        let _ = fs::remove_dir_all(&self.0);
    }
}

/// Outside Linux: the file in a copy's folder that its process holds locked while it is open.
#[cfg(not(target_os = "linux"))]
const LOCK: &str = "lock";

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
                connection: connect(&copy.0.join("db"), false)?,
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
    use std::sync::atomic::{AtomicU64, Ordering};
    static N: AtomicU64 = AtomicU64::new(0);
    let mut db_source = open_regular(db)?;
    let mut wal_source = open_regular(wal)?;
    prepare_root(root)?;
    let dir = root.dir.join(format!(
        "wal-{}-{}",
        std::process::id(),
        N.fetch_add(1, Ordering::SeqCst)
    ));
    pluxy_files::create_private_dir(&dir)?;
    #[allow(unused_mut)]
    let mut copy = Copy(dir, None);
    #[cfg(not(target_os = "linux"))]
    {
        let lock = pluxy_files::create_new_private(&copy.0.join(LOCK))?;
        lock.try_lock().map_err(std::io::Error::other)?;
        copy.1 = Some(lock);
    }
    for (source, name) in [(&mut db_source, "db"), (&mut wal_source, "db-wal")] {
        let mut dest = pluxy_files::create_new_private(&copy.0.join(name))?;
        std::io::copy(source, &mut dest)?;
    }
    Ok(copy)
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

/// Removes the copies in root that a process no longer running left behind (cut short); the copies
/// of this process and of every process still running stay. Only a folder named exactly
/// `wal-<pid>-<n>` is a copy.
fn prune(root: &Path) {
    let Ok(entries) = fs::read_dir(root) else {
        return;
    };
    for e in entries.flatten() {
        let pid = e.file_name().to_str().and_then(copy_pid);
        if let Some(pid) = pid {
            if pid != std::process::id()
                && e.file_type().is_ok_and(|t| t.is_dir())
                && stale(pid, &e.path())
            {
                let _ = fs::remove_dir_all(e.path());
            }
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

/// Whether the copy in folder dir, made by process pid, is left over: that process is no longer
/// running (as `/proc` tells; never when it cannot tell).
#[cfg(target_os = "linux")]
fn stale(pid: u32, _dir: &Path) -> bool {
    let proc = Path::new("/proc");
    proc.join("self").exists() && !proc.join(pid.to_string()).exists()
}

/// Whether the copy in folder dir is left over: its `lock` is held by no process (the lock goes
/// with the process that held it), or it has no `lock` and its folder is over a minute old (a copy
/// being made gets its lock right after its folder).
#[cfg(not(target_os = "linux"))]
fn stale(_pid: u32, dir: &Path) -> bool {
    match pluxy_files::open_regular(&dir.join(LOCK)) {
        Ok(lock) => lock.try_lock().is_ok(),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => fs::symlink_metadata(dir)
            .and_then(|m| m.modified())
            .ok()
            .and_then(|t| t.elapsed().ok())
            .is_some_and(|age| age > Duration::from_secs(60)),
        Err(_) => false,
    }
}

/// path as what follows `file:` in a URI. On Windows: separators as `/`, a `\\?\` prefix
/// dropped, a drive path as `/C:/…` and a share as `////server/share/…` (an empty authority, then
/// the share's own `//`), as SQLite reads them.
#[cfg(windows)]
fn uri_path(path: &Path) -> Vec<u8> {
    let s = path.to_string_lossy().replace('\\', "/");
    let s = match s.strip_prefix("//?/UNC/") {
        Some(share) => format!("//{share}"),
        None => s.strip_prefix("//?/").map(str::to_string).unwrap_or(s),
    };
    let s = if s.starts_with("//") {
        format!("//{s}")
    } else {
        format!("/{s}")
    };
    s.into_bytes()
}

#[cfg(not(windows))]
fn uri_path(path: &Path) -> Vec<u8> {
    path.as_os_str().as_encoded_bytes().to_vec()
}

/// A read-only connection to path (immutable when asked).
fn connect(path: &Path, immutable: bool) -> Result<Connection, Error> {
    let mut uri = String::from("file:");
    for b in &uri_path(path) {
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
