//! Opens another program's SQLite database strictly read-only, creating no file beside it.
//!
//! A WAL database whose `-shm` is missing (no process has it open) cannot be opened in place
//! without SQLite making that file, so:
//!
//! - the database path must be a regular file, not a link, a FIFO or anything else (it is never
//!   opened to be looked at otherwise), and a `-wal` or `-shm` beside it that exists must be one too;
//! - a database that is not in WAL mode, or whose `-shm` exists, is opened in place with
//!   `mode=ro`;
//! - one whose `-wal` is missing or empty is opened with `immutable=1`;
//! - one whose `-wal` holds changes and has no `-shm` is copied, with its `-wal`, into a private
//!   folder (0700) under `$PLUXY_TOOL_DATA_DIR/tmp` (the system's temporary folder when that is not
//!   set), read there, and removed when the [`Db`] is dropped. Before a copy is made in the tool's
//!   own folder, the copies of processes no longer running (`wal-<pid>-<n>`) are removed; those of
//!   this process and of every process still running stay.
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
    /// The named sidecar (`-wal` or `-shm`) exists and is not a regular file.
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

/// A private copy's folder, removed when dropped.
struct Copy(PathBuf);

impl Drop for Copy {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
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
    for (side, name) in [(&wal, "-wal"), (&shm, "-shm")] {
        if fs::symlink_metadata(side).is_ok_and(|m| !m.is_file()) {
            return Err(Error::SidecarNotRegular(name));
        }
    }
    if !is_wal(path) || shm.exists() {
        return Ok(Db {
            connection: connect(path, false)?,
            _copy: None,
        });
    }
    if fs::metadata(&wal).map_or(0, |m| m.len()) == 0 {
        return Ok(Db {
            connection: connect(path, true)?,
            _copy: None,
        });
    }
    let copy = private_copy(path, &wal, root).map_err(Error::Copy)?;
    Ok(Db {
        connection: connect(&copy.0.join("db"), false)?,
        _copy: Some(copy),
    })
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

/// Copies db and its -wal into a new folder of this user's that no one else may use.
fn private_copy(db: &Path, wal: &Path, root: &CopyRoot) -> std::io::Result<Copy> {
    use std::os::unix::fs::DirBuilderExt;
    use std::sync::atomic::{AtomicU64, Ordering};
    static N: AtomicU64 = AtomicU64::new(0);
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&root.dir)?;
    if root.own {
        prune(&root.dir);
    }
    let dir = root.dir.join(format!(
        "wal-{}-{}",
        std::process::id(),
        N.fetch_add(1, Ordering::SeqCst)
    ));
    fs::DirBuilder::new().mode(0o700).create(&dir)?;
    let copy = Copy(dir);
    fs::copy(db, copy.0.join("db"))?;
    fs::copy(wal, copy.0.join("db-wal"))?;
    Ok(copy)
}

/// Removes the copies in root that a process no longer running left behind (cut short); the copies
/// of this process and of every process still running stay.
fn prune(root: &Path) {
    let Ok(entries) = fs::read_dir(root) else {
        return;
    };
    for e in entries.flatten() {
        let name = e.file_name();
        let pid = name
            .to_str()
            .and_then(|n| n.strip_prefix("wal-"))
            .and_then(|n| n.split('-').next())
            .and_then(|p| p.parse::<u32>().ok());
        if let Some(pid) = pid {
            if pid != std::process::id() && !alive(pid) {
                let _ = fs::remove_dir_all(e.path());
            }
        }
    }
}

/// Whether process pid is running; true where that cannot be told (no `/proc`).
fn alive(pid: u32) -> bool {
    let proc = Path::new("/proc");
    !proc.join("self").exists() || proc.join(pid.to_string()).exists()
}

/// A read-only connection to path (immutable when asked).
fn connect(path: &Path, immutable: bool) -> Result<Connection, Error> {
    let mut uri = String::from("file:");
    for b in path.as_os_str().as_encoded_bytes() {
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

/// Whether path is a SQLite database in WAL mode, by its header.
fn is_wal(path: &Path) -> bool {
    let mut header = [0u8; 20];
    fs::File::open(path)
        .and_then(|mut f| f.read_exact(&mut header))
        .is_ok_and(|_| &header[..16] == b"SQLite format 3\0" && header[18] == 2)
}

#[cfg(test)]
mod tests;
