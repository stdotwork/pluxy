//! The app's own core versions, in managed mode: each version the app installed from a signed
//! release, or copied in from a core program the person chose ("adopted"), in a folder of its own
//! that the app never changes after installing it, and a selection of which one runs.
//!
//! A release comes from the core source the person set: an index file listing releases and, for
//! each, its signed artifacts (see the README's "The app's own core"). An artifact is a gzip tar
//! file of the core's folder (`pluxy`, on Windows `pluxy.exe`, and anything beside it, such as
//! `bundled/`), described by a descriptor signed with a trusted publisher key; `artifact.rs`
//! checks the signature, size and SHA-256 before anything is unpacked. Each installed file's
//! digest is kept and checked again before the core starts. Nothing in this module needs Tauri.
//!
//! Installing and starting a version open each folder and file without following a link
//! (`Dir`). The folder unpacked into (a staging folder) and a version's folder are opened by
//! their paths, refusing a link or other reparse point at the last part; below them every folder
//! and file is opened from the folder held open before it, on Unix by descriptor (`openat`), on
//! Windows by handle (`NtCreateFile` relative to the folder, `vault::at`), refusing a link or other
//! reparse point at each part. Moving a version into place, removing it, and making its files
//! read-only go by path. On Windows the store is in the local application data, on a fixed local
//! drive, and its folders are private by their access lists (this user and SYSTEM,
//! `private_windows.rs`).

use crate::artifact::{self, PublisherKeys, Tier, VerifiedArchive};
use crate::managed::{random_key, write_private};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::cell::Cell;
#[cfg(unix)]
use std::ffi::CString;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Seek, SeekFrom, Write};
#[cfg(unix)]
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
#[cfg(unix)]
use std::os::unix::ffi::OsStrExt;
#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt};
use std::path::{Component, Path, PathBuf};
use std::rc::Rc;

/// The publisher and ID of core releases.
pub const CORE_ID: &str = "pluxy/core";
/// The core's program in a version's folder: on Windows, which runs programs named `.exe` or
/// `.com`, `pluxy.exe`, as Go names it there.
pub const PROGRAM: &str = if cfg!(windows) { "pluxy.exe" } else { "pluxy" };
const FORMAT: u32 = 1;
const MAX_INDEX: u64 = 1024 * 1024;
const MAX_ARCHIVE: u64 = 512 * 1024 * 1024;
const MAX_UNPACKED: u64 = 512 * 1024 * 1024;
const MAX_ENTRIES: usize = 10_000;

/// This computer's platform, as descriptors name it, with Go's names: `linux/amd64`,
/// `darwin/arm64` (Rust says `macos`), `windows/amd64`.
pub fn platform() -> String {
    crate::device::platform()
}

/// The core source's index: the release feed's format, of which this reads only core releases'
/// version and artifacts; other fields and other products are left to their readers.
#[derive(Deserialize)]
struct Index {
    format: u32,
    releases: Vec<IndexRelease>,
}

#[derive(Deserialize)]
struct IndexRelease {
    product: String,
    version: String,
    #[serde(default)]
    artifacts: Vec<IndexArtifact>,
}

#[derive(Deserialize)]
struct IndexArtifact {
    platform: String,
    /// The archive's file name, beside the index.
    url: String,
    /// The exact signed descriptor bytes, as a JSON string.
    descriptor: String,
    /// The detached Ed25519 signature, 128 lower-case hex digits.
    signature: String,
    key_id: String,
}

/// A release the source offers for this platform.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Available {
    pub version: String,
}

fn read_index(source: &str) -> Result<Index, String> {
    let path = source_path(source)?;
    let f = File::open(&path).map_err(|e| format!("reading the core source {source}: {e}"))?;
    let mut bytes = Vec::new();
    f.take(MAX_INDEX + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| format!("reading the core source {source}: {e}"))?;
    if bytes.len() as u64 > MAX_INDEX {
        return Err("the core source's index is larger than 1 MiB".into());
    }
    let index: Index = serde_json::from_slice(&bytes)
        .map_err(|e| format!("the core source's index is not valid: {e}"))?;
    if index.format != 1 {
        return Err(format!(
            "the core source's index has format {}, which this app does not read",
            index.format
        ));
    }
    Ok(index)
}

/// The core source as a path: for now a local index file, given by its full path.
// shortcut: local index files only; an https source comes with the release feed (P8b), which
// will carry the same `artifacts` in its core releases.
pub fn source_path(source: &str) -> Result<PathBuf, String> {
    if source.starts_with("https://") || source.starts_with("http://") {
        return Err("the core source must be an index file on this computer for now".into());
    }
    let p = PathBuf::from(source);
    if !p.is_absolute() {
        return Err("the core source must be given as a full path".into());
    }
    Ok(p)
}

/// The versions the source offers for this platform, newest first as the index lists them.
pub fn available(source: &str) -> Result<Vec<Available>, String> {
    let platform = platform();
    Ok(read_index(source)?
        .releases
        .into_iter()
        .filter(|r| r.product == "core" && r.artifacts.iter().any(|a| a.platform == platform))
        .map(|r| Available { version: r.version })
        .collect())
}

/// One installed file, as unpacked.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq)]
pub struct FileRecord {
    pub path: String,
    pub size: u64,
    pub executable: bool,
    pub sha256: String,
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Kind {
    /// From a signed release.
    Signed,
    /// Copied in from a core program the person chose.
    Adopted,
    /// Copied in from the core that came with the app (`bundled_core.rs`).
    Bundled,
}

/// What the app keeps about an installed version, `versions/<id>.json`.
#[derive(Clone, Debug, Serialize, Deserialize)]
struct Record {
    format: u32,
    id: String,
    kind: Kind,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    version: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    source: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    key_id: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    descriptor: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    signature: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    adopted_from: Option<String>,
    files: Vec<FileRecord>,
}

/// An installed version, as the window sees it.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Installed {
    pub id: String,
    pub kind: Kind,
    /// The release's version; none for an adopted program.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub version: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub adopted_from: Option<String>,
}

/// A version that did not start, and why.
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct Failed {
    pub version: String,
    pub reason: String,
}

/// Who made the selection: the app (its bundled core) or the person (an install, a rollback, an
/// adoption). A selection with none recorded counts as the person's.
#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Chooser {
    App,
    Person,
}

/// Which version runs, `state.json`. selected is the last version that started and answered;
/// a candidate is being started in its place, and a crash then leaves it recorded here.
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct State {
    #[serde(default)]
    pub format: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub selected: Option<String>,
    /// Who chose selected.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub chosen_by: Option<Chooser>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub candidate: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failed: Option<Failed>,
    /// The app version that last applied its bundled core: set when it tries to, so a bundled
    /// core that fails to start is not tried again until the app's version changes.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bundled_for: Option<String>,
}

/// The app's core versions, `<data>/cores`.
#[derive(Clone, Debug)]
pub struct Store {
    dir: PathBuf,
}

/// Makes the folder at path, and any missing above it, this user's alone.
#[cfg(unix)]
fn private_dir(path: &Path) -> io::Result<()> {
    fs::create_dir_all(path)?;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))?;
    no_entries(path)
}

/// macOS: the folder at path has no extended access list entry, the 0700 mode's twin, so what
/// is made in it inherits none (`private_macos.rs`). Nothing elsewhere.
#[cfg(unix)]
fn no_entries(path: &Path) -> io::Result<()> {
    #[cfg(target_os = "macos")]
    crate::private_macos::clear_entries(&fs::File::open(path)?)?;
    let _ = path;
    Ok(())
}

#[cfg(windows)]
fn private_dir(path: &Path) -> io::Result<()> {
    crate::private_windows::create_private_dir_all(path)
}

/// Makes a new private folder at path, failing if anything is already there.
#[cfg(unix)]
fn exclusive_dir(path: &Path) -> io::Result<()> {
    fs::DirBuilder::new().mode(0o700).create(path)
}

#[cfg(windows)]
fn exclusive_dir(path: &Path) -> io::Result<()> {
    let (Some(parent), Some(name)) = (path.parent(), path.file_name().and_then(|n| n.to_str()))
    else {
        return Err(io::ErrorKind::InvalidInput.into());
    };
    Dir::open(parent)?.make_new(name)
}

/// Refused unless path is on a fixed local drive of this computer: the drive of the folder's real
/// path, every link and junction resolved (of its nearest folder that exists, as the store may not
/// yet), as `GetVolumePathNameW` and `GetDriveTypeW` find it. A network share, a drive letter
/// mapped to one, a link to one, and a removable drive are refused; a `subst` drive or a mounted
/// virtual disk on this computer counts as the local drive it is. A share path (`\\server\share`)
/// is refused without being opened.
#[cfg(windows)]
fn local_fixed(path: &Path) -> Result<(), String> {
    use std::os::windows::ffi::OsStrExt;
    use std::os::windows::fs::OpenOptionsExt;
    use std::path::Prefix;
    use windows_sys::Win32::Storage::FileSystem::{
        GetDriveTypeW, GetVolumePathNameW, FILE_FLAG_BACKUP_SEMANTICS, FILE_READ_ATTRIBUTES,
        FILE_SHARE_DELETE,
    };
    /// `GetDriveTypeW`'s answer for a fixed local drive.
    const DRIVE_FIXED: u32 = 3;
    let refused = || {
        format!(
            "{} is not on a local drive of this computer; the app keeps its core versions only there",
            path.display()
        )
    };
    match path.components().next() {
        Some(Component::Prefix(p))
            if matches!(p.kind(), Prefix::Disk(_) | Prefix::VerbatimDisk(_)) => {}
        _ => return Err(refused()),
    }
    let mut at = path;
    let f = loop {
        match OpenOptions::new()
            .access_mode(FILE_READ_ATTRIBUTES)
            .share_mode(win::SHARE | FILE_SHARE_DELETE)
            .custom_flags(FILE_FLAG_BACKUP_SEMANTICS)
            .open(at)
        {
            Ok(f) => break f,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                at = at.parent().ok_or_else(refused)?
            }
            Err(e) => return Err(format!("{}: {e}", at.display())),
        }
    };
    let real = crate::owned::windows::final_path(&f)?;
    let wide: Vec<u16> = real.as_os_str().encode_wide().chain([0]).collect();
    // The volume's root can be one character longer than the path it holds (a trailing
    // backslash, as for `C:` itself); wide's NUL and the `+ 1` leave room for it.
    let mut root = vec![0u16; wide.len() + 1];
    // SAFETY: wide is NUL-terminated; root holds the length given.
    if unsafe { GetVolumePathNameW(wide.as_ptr(), root.as_mut_ptr(), root.len() as u32) } == 0 {
        return Err(refused());
    }
    // SAFETY: GetVolumePathNameW wrote a NUL-terminated path into root.
    if unsafe { GetDriveTypeW(root.as_ptr()) } != DRIVE_FIXED {
        return Err(refused());
    }
    Ok(())
}

#[cfg(unix)]
fn c_string(bytes: &[u8]) -> io::Result<CString> {
    CString::new(bytes).map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "a name holds NUL"))
}

#[cfg(unix)]
fn owned(fd: libc::c_int) -> io::Result<OwnedFd> {
    if fd < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(unsafe { OwnedFd::from_raw_fd(fd) })
    }
}

/// A folder of the store, held open, opened without following a link (see the module's notes):
/// on Unix its descriptor; on Windows its handle, held without `FILE_SHARE_DELETE`, so no one can
/// rename or remove the folder while it is held.
#[cfg(unix)]
struct Dir(OwnedFd);

#[cfg(windows)]
struct Dir(File);

#[cfg(unix)]
impl Dir {
    /// The folder at path; a link there is refused.
    fn open(path: &Path) -> io::Result<Dir> {
        let c = c_string(path.as_os_str().as_bytes())?;
        owned(unsafe {
            libc::open(
                c.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            )
        })
        .map(Dir)
    }

    fn try_clone(&self) -> io::Result<Dir> {
        self.0.try_clone().map(Dir)
    }

    /// The folder at path, only to read from; a link there is refused.
    fn open_read(path: &Path) -> io::Result<Dir> {
        Dir::open(path)
    }

    /// The folder name in this one, only to read from.
    fn sub_read(&self, name: &str) -> io::Result<Dir> {
        self.sub(name)
    }

    /// Opens name in this folder, following no link.
    fn at(&self, name: &str, flags: libc::c_int, mode: u32) -> io::Result<OwnedFd> {
        let c = c_string(name.as_bytes())?;
        owned(unsafe {
            libc::openat(
                self.0.as_raw_fd(),
                c.as_ptr(),
                flags | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                mode as libc::c_uint,
            )
        })
    }

    /// The folder name in this one.
    fn sub(&self, name: &str) -> io::Result<Dir> {
        self.at(name, libc::O_RDONLY | libc::O_DIRECTORY, 0)
            .map(Dir)
    }

    /// Makes the private folder name in this one, unless it is there already.
    fn make(&self, name: &str) -> io::Result<()> {
        let c = c_string(name.as_bytes())?;
        if unsafe { libc::mkdirat(self.0.as_raw_fd(), c.as_ptr(), 0o700) } != 0 {
            let e = io::Error::last_os_error();
            if e.kind() != io::ErrorKind::AlreadyExists {
                return Err(e);
            }
        }
        Ok(())
    }

    /// Creates the new file name in this one, private, for writing.
    fn create(&self, name: &str, executable: bool) -> io::Result<File> {
        self.at(
            name,
            libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL,
            if executable { 0o700 } else { 0o600 },
        )
        .map(File::from)
    }

    /// Opens the file name in this one for reading, without blocking (on a FIFO).
    fn read(&self, name: &str) -> io::Result<File> {
        self.at(name, libc::O_RDONLY | libc::O_NONBLOCK, 0)
            .map(File::from)
    }
}

#[cfg(windows)]
mod win {
    pub use windows_sys::Win32::Storage::FileSystem::{
        FILE_ADD_FILE, FILE_ADD_SUBDIRECTORY, FILE_ATTRIBUTE_REPARSE_POINT,
        FILE_FLAG_BACKUP_SEMANTICS, FILE_FLAG_OPEN_REPARSE_POINT, FILE_GENERIC_READ,
        FILE_GENERIC_WRITE, FILE_LIST_DIRECTORY, FILE_READ_ATTRIBUTES, FILE_SHARE_READ,
        FILE_SHARE_WRITE, FILE_TRAVERSE,
    };
    /// What a held folder is used for: list it, and open and make names in it.
    pub const FOLDER: u32 = FILE_LIST_DIRECTORY
        | FILE_TRAVERSE
        | FILE_ADD_FILE
        | FILE_ADD_SUBDIRECTORY
        | FILE_READ_ATTRIBUTES;
    /// What a folder only read from is used for: list it and open names in it.
    pub const READ_FOLDER: u32 = FILE_LIST_DIRECTORY | FILE_TRAVERSE | FILE_READ_ATTRIBUTES;
    /// Others may read a held folder or file, and add to a held folder, but not rename or remove
    /// it.
    pub const SHARE: u32 = FILE_SHARE_READ | FILE_SHARE_WRITE;
}

#[cfg(windows)]
impl Dir {
    /// The folder at path; a link or other reparse point there is refused.
    fn open(path: &Path) -> io::Result<Dir> {
        use std::os::windows::fs::OpenOptionsExt;
        OpenOptions::new()
            .access_mode(win::FOLDER)
            .share_mode(win::SHARE)
            .custom_flags(win::FILE_FLAG_BACKUP_SEMANTICS | win::FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
            .and_then(Dir::checked)
    }

    /// The folder at path, only to list it and open names in it (a folder of the app's install
    /// the person may not change); a link or other reparse point there is refused.
    fn open_read(path: &Path) -> io::Result<Dir> {
        use std::os::windows::fs::OpenOptionsExt;
        OpenOptions::new()
            .access_mode(win::READ_FOLDER)
            .share_mode(win::SHARE)
            .custom_flags(win::FILE_FLAG_BACKUP_SEMANTICS | win::FILE_FLAG_OPEN_REPARSE_POINT)
            .open(path)
            .and_then(Dir::checked)
    }

    /// The folder name in this one, only to read from.
    fn sub_read(&self, name: &str) -> io::Result<Dir> {
        crate::vault::at(&self.0, name, win::READ_FOLDER, win::SHARE, true, None)
            .and_then(Dir::checked)
    }

    /// f, a folder opened without following a link, unless it is a link or not a folder.
    fn checked(f: File) -> io::Result<Dir> {
        let m = f.metadata()?;
        if is_link(&m) || !m.is_dir() {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "not a folder: a link or other reparse point",
            ));
        }
        Ok(Dir(f))
    }

    fn try_clone(&self) -> io::Result<Dir> {
        self.0.try_clone().map(Dir)
    }

    /// The folder name in this one.
    fn sub(&self, name: &str) -> io::Result<Dir> {
        crate::vault::at(&self.0, name, win::FOLDER, win::SHARE, true, None).and_then(Dir::checked)
    }

    /// Makes the new private folder name in this one; anything already there is an error.
    fn make_new(&self, name: &str) -> io::Result<()> {
        let private = crate::private_windows::Private::new(true)?;
        crate::vault::at(
            &self.0,
            name,
            win::FILE_READ_ATTRIBUTES,
            win::SHARE,
            true,
            Some(&private),
        )
        .map(drop)
    }

    /// Makes the private folder name in this one, unless something is there already (`sub` then
    /// checks it is a folder).
    fn make(&self, name: &str) -> io::Result<()> {
        match self.make_new(name) {
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => Ok(()),
            r => r,
        }
    }

    /// Creates the new file name in this one, private, for writing: an existing name, a link
    /// too, is an error. Windows has no execute bit.
    fn create(&self, name: &str, _executable: bool) -> io::Result<File> {
        let private = crate::private_windows::Private::new(false)?;
        crate::vault::at(
            &self.0,
            name,
            win::FILE_GENERIC_WRITE,
            0,
            false,
            Some(&private),
        )
    }

    /// Opens the file name in this one for reading, refusing a link or other reparse point. No one
    /// may write to it, or delete or rename it, while it is open.
    fn read(&self, name: &str) -> io::Result<File> {
        let f = crate::vault::at(
            &self.0,
            name,
            win::FILE_GENERIC_READ,
            win::FILE_SHARE_READ,
            false,
            None,
        )?;
        if is_link(&f.metadata()?) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                "a link or other reparse point",
            ));
        }
        Ok(f)
    }
}

/// Whether m is of a link or another reparse point.
#[cfg(windows)]
fn is_link(m: &fs::Metadata) -> bool {
    use std::os::windows::fs::MetadataExt;
    m.file_attributes() & win::FILE_ATTRIBUTE_REPARSE_POINT != 0
}

/// The folder holding rel's last part, beneath root, and that part's name: each folder on the
/// way is opened from the one before it, following no link; with create, missing ones are made.
fn parent_beneath(root: &Dir, rel: &str, create: bool) -> io::Result<(Dir, String)> {
    let parts: Vec<&str> = rel.split('/').collect();
    let (last, folders) = parts.split_last().expect("split gives one part");
    let mut cur = root.try_clone()?;
    for f in folders {
        if create {
            cur.make(f)?;
        }
        cur = cur.sub(f)?;
    }
    Ok((cur, last.to_string()))
}

/// Whether path is a plain relative path, as unpacking writes them: `a/b`, no `.`, `..`, empty
/// parts or leading `/`.
fn plain_relative(path: &str) -> bool {
    entry_path(Path::new(path)).ok().flatten().as_deref() == Some(path)
}

/// Adopted programs' IDs, `custom-<SHA-256>`; no release version may start with it.
const ADOPTED: &str = "custom-";

fn valid_id(id: &str) -> bool {
    !id.is_empty()
        && id.len() <= 80
        && id.as_bytes()[0].is_ascii_alphanumeric()
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b".+_-".contains(&b))
}

#[cfg(unix)]
pub(crate) fn remove_tree(path: &Path) {
    let _ = fs::remove_dir_all(path);
}

/// Windows: should removing fail (a read-only file, `lock`, whose access list allows no delete,
/// where its folder's rights do not make up for it), every file under path is made private again,
/// as its owner may, and the removal tried once more.
#[cfg(windows)]
pub(crate) fn remove_tree(path: &Path) {
    if fs::remove_dir_all(path).is_ok() {
        return;
    }
    unlock(path);
    let _ = fs::remove_dir_all(path);
}

/// Makes every file under path private again (writable by this user), following no link.
#[cfg(windows)]
fn unlock(path: &Path) {
    let Ok(m) = fs::symlink_metadata(path) else {
        return;
    };
    if m.is_dir() {
        for e in fs::read_dir(path).into_iter().flatten().flatten() {
            unlock(&e.path());
        }
    } else if m.is_file() {
        if let Ok(f) = crate::private_windows::open_for_access_list(path, false) {
            let _ = crate::private_windows::make_private(&f, false);
        }
    }
}

/// Makes the files under path read-only: this user may read them, and run the executable ones.
/// Folders stay writable by this user only (0700), so the store, or the person, can still
/// remove them; the digests checked before each start are what keep a version unchanged.
#[cfg(unix)]
fn lock(path: &Path) -> io::Result<()> {
    let m = fs::symlink_metadata(path)?;
    if m.is_dir() {
        for e in fs::read_dir(path)? {
            lock(&e?.path())?;
        }
        Ok(())
    } else {
        let mode = if m.permissions().mode() & 0o100 != 0 {
            0o500
        } else {
            0o400
        };
        fs::set_permissions(path, fs::Permissions::from_mode(mode))
    }
}

/// Windows: each file's access list lets only this user and SYSTEM read and run it
/// (`private_windows::make_read_only`); folders stay private, as on Unix. A link is refused.
#[cfg(windows)]
fn lock(path: &Path) -> io::Result<()> {
    let m = fs::symlink_metadata(path)?;
    if m.is_dir() {
        for e in fs::read_dir(path)? {
            lock(&e?.path())?;
        }
        Ok(())
    } else {
        let f = crate::private_windows::open_for_access_list(path, false)?;
        crate::private_windows::make_read_only(&f, false)
    }
}

/// Whether an installed file, open as f with metadata m, is still as `lock` left it: on Unix
/// writable by no one, with the execute bit it was installed with; on macOS also with no
/// extended access list entry, which could let another user change it whatever the mode
/// (`private_macos.rs`; a list that cannot be read counts as one).
#[cfg(unix)]
fn locked(f: &File, m: &fs::Metadata, executable: bool) -> bool {
    #[cfg(target_os = "macos")]
    if crate::private_macos::has_entries(f).unwrap_or(true) {
        return false;
    }
    let _ = f;
    let mode = m.permissions().mode();
    (mode & 0o100 != 0) == executable && mode & 0o222 == 0
}

/// Windows: its access list still lets no one write to it (it has no execute bit).
#[cfg(windows)]
fn locked(f: &File, _m: &fs::Metadata, _executable: bool) -> bool {
    crate::private_windows::is_read_only(f).unwrap_or(false)
}

fn sha256_of(mut r: impl Read) -> io::Result<(String, u64)> {
    let mut h = Sha256::new();
    let mut buf = [0u8; 64 * 1024];
    let mut n = 0u64;
    loop {
        let k = r.read(&mut buf)?;
        if k == 0 {
            break;
        }
        n += k as u64;
        h.update(&buf[..k]);
    }
    Ok((artifact::hex(&h.finalize()), n))
}

/// A core release's artifact for this computer in a core source's index, its descriptor read and
/// checked to name that version of the core; nothing of its archive is read yet.
struct Offered {
    a: IndexArtifact,
    signature: [u8; 64],
    d: artifact::Descriptor,
    /// The archive, beside the index.
    archive: PathBuf,
}

fn offered(source: &str, version: &str) -> Result<Offered, String> {
    if version.starts_with(ADOPTED) {
        return Err(format!(
            "core versions named {ADOPTED}… are reserved for adopted core programs"
        ));
    }
    let platform = platform();
    let index = read_index(source)?;
    let release = index
        .releases
        .into_iter()
        .find(|r| r.product == "core" && r.version == version)
        .ok_or_else(|| format!("the core source offers no core {version}"))?;
    let a = release
        .artifacts
        .into_iter()
        .find(|a| a.platform == platform)
        .ok_or_else(|| format!("the core source offers no core {version} for {platform}"))?;
    let name = Path::new(&a.url);
    if a.url.is_empty()
        || name.components().count() != 1
        || !matches!(name.components().next(), Some(Component::Normal(_)))
    {
        return Err("a core archive must be a file name beside the source's index".into());
    }
    let signature = artifact::decode_hex::<64>(&a.signature)
        .map_err(|_| "the core release's signature is not 128 hex digits")?;
    let d: artifact::Descriptor = serde_json::from_str(&a.descriptor)
        .map_err(|_| "the core release's descriptor is not valid")?;
    if d.id != CORE_ID || d.tier != Tier::Native || d.version != version {
        return Err(format!(
            "the core source's descriptor names {} {}, not {CORE_ID} {version}",
            d.id, d.version
        ));
    }
    if d.size > MAX_ARCHIVE {
        return Err("the core archive is larger than 512 MiB".into());
    }
    let archive = source_path(source)?
        .parent()
        .ok_or("the core source has no folder")?
        .join(name);
    Ok(Offered {
        a,
        signature,
        d,
        archive,
    })
}

/// Package time (`bundled_core::prepare`): checks core version's archive for this computer in the
/// core source's index against its signed descriptor with keys, as `Store::stage` does, and
/// unpacks it into dir, which must not exist yet, as `Store::install` does. Returns the SHA-256 of
/// the signed descriptor and the files unpacked.
pub fn unpack_release(
    keys: &impl PublisherKeys,
    source: &str,
    version: &str,
    dir: &Path,
) -> Result<(String, Vec<FileRecord>), String> {
    let Offered {
        a,
        signature,
        d: _,
        archive,
    } = offered(source, version)?;
    keys.publisher_key(source, &a.key_id, "pluxy")?;
    let file = File::open(&archive).map_err(|e| format!("{}: {e}", archive.display()))?;
    let verified = artifact::verify_trusted_archive(
        keys,
        source,
        &a.key_id,
        a.descriptor.as_bytes(),
        &signature,
        &platform(),
        file,
    )?;
    let descriptor = artifact::hex(&Sha256::digest(verified.descriptor_bytes()));
    exclusive_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let files = unpack(verified.into_archive(), dir, MAX_UNPACKED)?;
    let program = files
        .iter()
        .find(|f| f.path == PROGRAM)
        .ok_or_else(|| format!("the core archive has no {PROGRAM} program at its top"))?;
    if cfg!(unix) && !program.executable {
        return Err(format!("{PROGRAM} in the core archive is not executable"));
    }
    Ok((descriptor, files))
}

impl Store {
    /// The store in dir, made if missing.
    pub fn open(dir: &Path) -> Result<Store, String> {
        #[cfg(windows)]
        local_fixed(dir)?;
        let s = Store { dir: dir.into() };
        private_dir(&s.versions()).map_err(|e| format!("{}: {e}", dir.display()))?;
        private_dir(&s.records()).map_err(|e| format!("{}: {e}", dir.display()))?;
        private_dir(&s.staging()).map_err(|e| format!("{}: {e}", dir.display()))?;
        #[cfg(unix)]
        fs::set_permissions(dir, fs::Permissions::from_mode(0o700))
            .and_then(|_| no_entries(dir))
            .map_err(|e| format!("{}: {e}", dir.display()))?;
        #[cfg(windows)]
        crate::private_windows::make_private_dir(dir)
            .map_err(|e| format!("{}: {e}", dir.display()))?;
        Ok(s)
    }

    /// Removes what an interrupted install left. Only when no change can be waiting: at the
    /// app's start.
    pub fn clear_staging(&self) {
        remove_tree(&self.staging());
        let _ = private_dir(&self.staging());
    }

    fn versions(&self) -> PathBuf {
        self.dir.join("versions")
    }

    fn staging(&self) -> PathBuf {
        self.dir.join("staging")
    }

    /// Records live apart from the version folders, so no version's name can be another's record.
    fn records(&self) -> PathBuf {
        self.dir.join("records")
    }

    fn record_path(&self, id: &str) -> PathBuf {
        self.records().join(format!("{id}.json"))
    }

    /// A version's folder.
    pub fn folder(&self, id: &str) -> PathBuf {
        self.versions().join(id)
    }

    pub fn state(&self) -> Result<State, String> {
        let path = self.dir.join("state.json");
        match fs::read(&path) {
            Ok(b) => serde_json::from_slice(&b).map_err(|e| format!("{}: {e}", path.display())),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(State::default()),
            Err(e) => Err(format!("{}: {e}", path.display())),
        }
    }

    pub fn save_state(&self, s: &State) -> Result<(), String> {
        let mut s = s.clone();
        s.format = FORMAT;
        let path = self.dir.join("state.json");
        write_private(&path, &serde_json::to_vec_pretty(&s).expect("JSON"))
            .map_err(|e| format!("saving {}: {e}", path.display()))
    }

    fn record(&self, id: &str) -> Result<Record, String> {
        if !valid_id(id) {
            return Err(format!("core {id} is not installed"));
        }
        let path = self.record_path(id);
        let bytes = match fs::read(&path) {
            Ok(b) => b,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                return Err(format!("core {id} is not installed"))
            }
            Err(e) => return Err(format!("{}: {e}", path.display())),
        };
        let r: Record =
            serde_json::from_slice(&bytes).map_err(|e| format!("{}: {e}", path.display()))?;
        let folder = fs::symlink_metadata(self.folder(id)).is_ok_and(|m| m.is_dir());
        let mut paths = std::collections::HashSet::new();
        if r.format != FORMAT
            || r.id != id
            || !folder
            || !r
                .files
                .iter()
                .all(|f| plain_relative(&f.path) && paths.insert(f.path.as_str()))
        {
            return Err(format!(
                "core {id} is not installed properly: install it again"
            ));
        }
        Ok(r)
    }

    /// The installed versions, by ID.
    pub fn list(&self) -> Vec<Installed> {
        let mut ids: Vec<String> = fs::read_dir(self.records())
            .into_iter()
            .flatten()
            .flatten()
            .filter_map(|e| {
                let name = e.file_name().into_string().ok()?;
                name.strip_suffix(".json").map(String::from)
            })
            .collect();
        ids.sort();
        ids.into_iter()
            .filter_map(|id| self.record(&id).ok())
            .map(|r| Installed {
                id: r.id,
                kind: r.kind,
                version: r.version,
                adopted_from: r.adopted_from,
            })
            .collect()
    }

    pub fn installed(&self, id: &str) -> bool {
        self.record(id).is_ok()
    }

    /// The release version an installed version must report when it runs; none for an adopted one.
    pub fn version(&self, id: &str) -> Result<Option<String>, String> {
        Ok(self.record(id)?.version)
    }

    /// Checks every file of version id against the digests kept when it was installed, and opens
    /// its program: the core is started from this open file, not from its path again.
    pub fn program(&self, id: &str) -> Result<File, String> {
        let r = self.record(id)?;
        let again = match r.kind {
            Kind::Signed => "install it again",
            Kind::Adopted => "adopt it again",
            Kind::Bundled => "the app copies it in again at its next start",
        };
        let folder = self.folder(id);
        let root = Dir::open(&folder).map_err(|e| format!("core {id}: {e}"))?;
        let mut seen = 0usize;
        let mut program = None;
        for f in &r.files {
            let changed = || {
                format!(
                    "core {id} changed since it was installed ({}): {again}",
                    f.path
                )
            };
            let (dir, name) = parent_beneath(&root, &f.path, false).map_err(|_| changed())?;
            let file = dir.read(&name).map_err(|_| changed())?;
            // Type and size first: never read a device or a FIFO, nor more than was installed.
            let m = file.metadata().map_err(|e| e.to_string())?;
            if !m.is_file() || m.len() != f.size || !locked(&file, &m, f.executable) {
                return Err(changed());
            }
            let (sha, size) =
                sha256_of((&file).take(f.size + 1)).map_err(|e| format!("core {id}: {e}"))?;
            if sha != f.sha256 || size != f.size {
                return Err(changed());
            }
            seen += 1;
            if f.path == PROGRAM {
                program = Some(file);
            }
        }
        if count_files(&folder) != seen {
            return Err(format!(
                "core {id} changed since it was installed (files were added): {again}"
            ));
        }
        let mut program = program.ok_or_else(|| format!("core {id} has no {PROGRAM} program"))?;
        program
            .seek(SeekFrom::Start(0))
            .map_err(|e| e.to_string())?;
        Ok(program)
    }

    /// Removes version id.
    pub fn remove(&self, id: &str) {
        if !valid_id(id) {
            return;
        }
        remove_tree(&self.folder(id));
        let _ = fs::remove_file(self.record_path(id));
    }

    /// Removes every version but keep's.
    pub fn prune(&self, keep: &[&str]) {
        for v in self.list() {
            if !keep.contains(&v.id.as_str()) {
                self.remove(&v.id);
            }
        }
    }

    /// Finds version in source's index, copies its archive into the store and checks it against
    /// its signed descriptor, with a key keys trusts for that source. Nothing is unpacked yet.
    pub fn stage(
        &self,
        keys: &impl PublisherKeys,
        source: &str,
        version: &str,
    ) -> Result<Staged, String> {
        let Offered {
            a,
            signature,
            d,
            archive: from,
        } = offered(source, version)?;
        let platform = platform();
        // Trust first: an unknown key or source is refused before the archive is read.
        keys.publisher_key(source, &a.key_id, "pluxy")?;
        let staged = self.staging().join(format!("{}.tar.gz", random_key()));
        let copied = (|| -> Result<File, String> {
            let src = File::open(&from).map_err(|e| format!("{}: {e}", from.display()))?;
            #[cfg(unix)]
            let mut dst = OpenOptions::new()
                .read(true)
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&staged)
                .map_err(|e| e.to_string())?;
            #[cfg(windows)]
            let mut dst =
                crate::private_windows::create_new_private(&staged).map_err(|e| e.to_string())?;
            let n = io::copy(&mut src.take(d.size + 1), &mut dst).map_err(|e| e.to_string())?;
            if n != d.size {
                return Err("archive size differs from signed descriptor".into());
            }
            dst.sync_all().map_err(|e| e.to_string())?;
            // Windows: the private file is made for writing only; it is read from here on, and
            // no one may write to it while the store holds it.
            #[cfg(windows)]
            let dst = {
                use std::os::windows::fs::OpenOptionsExt;
                drop(dst);
                OpenOptions::new()
                    .read(true)
                    .share_mode(windows_sys::Win32::Storage::FileSystem::FILE_SHARE_READ)
                    .open(&staged)
                    .map_err(|e| e.to_string())?
            };
            Ok(dst)
        })();
        let file = match copied {
            Ok(f) => f,
            Err(e) => {
                let _ = fs::remove_file(&staged);
                return Err(e);
            }
        };
        match artifact::verify_trusted_archive(
            keys,
            source,
            &a.key_id,
            a.descriptor.as_bytes(),
            &signature,
            &platform,
            file,
        ) {
            Ok(verified) => Ok(Staged {
                verified: Some(verified),
                path: staged,
                source: source.into(),
                key_id: a.key_id,
            }),
            Err(e) => {
                let _ = fs::remove_file(&staged);
                Err(e)
            }
        }
    }

    /// Unpacks a checked archive into a new version's folder, named after its version.
    pub fn install(&self, mut staged: Staged) -> Result<String, String> {
        let verified = staged.verified.take().ok_or("already installed")?;
        let d = verified.descriptor().clone();
        let descriptor = String::from_utf8(verified.descriptor_bytes().to_vec())
            .map_err(|_| "descriptor is not UTF-8")?;
        let signature = artifact::hex(verified.signature_bytes());
        let id = d.version.clone();
        if !valid_id(&id) || id.starts_with(ADOPTED) {
            return Err(format!("core {id} cannot be installed under that name"));
        }
        let tmp = self.staging().join(random_key());
        let result = (|| {
            exclusive_dir(&tmp).map_err(|e| e.to_string())?;
            let files = unpack(verified.into_archive(), &tmp, MAX_UNPACKED)?;
            let program = files
                .iter()
                .find(|f| f.path == PROGRAM)
                .ok_or_else(|| format!("the core archive has no {PROGRAM} program at its top"))?;
            // Windows has no execute bit: there the program is checked to be one as it starts
            // (`owned::windows::pin_file`).
            if cfg!(unix) && !program.executable {
                return Err(format!("{PROGRAM} in the core archive is not executable"));
            }
            self.finish(
                &tmp,
                Record {
                    format: FORMAT,
                    id: id.clone(),
                    kind: Kind::Signed,
                    version: Some(d.version.clone()),
                    source: Some(staged.source.clone()),
                    key_id: Some(staged.key_id.clone()),
                    descriptor: Some(descriptor),
                    signature: Some(signature),
                    adopted_from: None,
                    files,
                },
            )
        })();
        if result.is_err() {
            remove_tree(&tmp);
        }
        result.map(|()| id)
    }

    /// Moves a new version's unpacked folder into place, locks its files, and writes its record
    /// last, so a version without a record is never used. A version installed again replaces
    /// its folder; until the new record is written, the old one no longer matches and the
    /// version does not start.
    fn finish(&self, tmp: &Path, r: Record) -> Result<(), String> {
        let fail = |e: String| format!("installing core {}: {e}", r.id);
        lock(tmp).map_err(|e| fail(e.to_string()))?;
        let folder = self.folder(&r.id);
        let old = match fs::symlink_metadata(&folder) {
            Ok(_) => {
                let aside = self.staging().join(random_key());
                fs::rename(&folder, &aside).map_err(|e| fail(e.to_string()))?;
                Some(aside)
            }
            Err(_) => None,
        };
        let bytes = serde_json::to_vec_pretty(&r).expect("JSON");
        let done = fs::rename(tmp, &folder)
            .map_err(|e| e.to_string())
            .and_then(|()| {
                write_private(&self.record_path(&r.id), &bytes).map_err(|e| e.to_string())
            });
        match (done, old) {
            (Ok(()), old) => {
                if let Some(old) = old {
                    remove_tree(&old);
                }
                Ok(())
            }
            (Err(e), old) => {
                if fs::symlink_metadata(tmp).is_err() {
                    remove_tree(&folder);
                }
                if let Some(old) = old {
                    let _ = fs::rename(&old, &folder);
                }
                Err(fail(e))
            }
        }
    }

    /// The core program at path, to adopt: its SHA-256 and size, for the person to confirm.
    pub fn inspect(path: &Path) -> Result<(String, u64), String> {
        if !path.is_absolute() {
            return Err("the core program must be given as a full path".into());
        }
        let f = open_program(path)?;
        let size = f.metadata().map_err(|e| e.to_string())?.len();
        let (sha, n) = sha256_of((&f).take(size + 1)).map_err(|e| e.to_string())?;
        if n != size {
            return Err(format!("{} changed while it was read", path.display()));
        }
        Ok((sha, size))
    }

    /// The ID an adopted program with this SHA-256 gets.
    pub fn adopted_id(sha256: &str) -> String {
        format!("{ADOPTED}{sha256}")
    }

    /// Copies the program at path into a version of its own, `custom-<its SHA-256>`, if it still
    /// is the regular file of the size and SHA-256 the person confirmed. An adopted version that
    /// is still intact is kept; a damaged one is copied in again. Returns the version, and
    /// whether the program was copied in (false: the intact version was kept).
    pub fn adopt(&self, path: &Path, sha256: &str, size: u64) -> Result<(String, bool), String> {
        artifact::decode_hex::<32>(sha256).map_err(|_| "not a SHA-256")?;
        let id = Self::adopted_id(sha256);
        if let Ok(r) = self.record(&id) {
            match r.files.as_slice() {
                [f] if r.kind == Kind::Adopted && f.sha256 == sha256 => {
                    if self.program(&id).is_ok() {
                        return Ok((id, false));
                    }
                }
                _ => return Err(format!("core {id} is installed, but not as this program")),
            }
        }
        let changed = || {
            format!(
                "{} changed after you confirmed it: adopt it again",
                path.display()
            )
        };
        let tmp = self.staging().join(random_key());
        let result = (|| {
            exclusive_dir(&tmp).map_err(|e| e.to_string())?;
            let src = open_program(path).map_err(|_| changed())?;
            if src.metadata().map_err(|e| e.to_string())?.len() != size {
                return Err(changed());
            }
            let mut src = src.take(size + 1);
            let mut dst = Dir::open(&tmp)
                .and_then(|d| d.create(PROGRAM, true))
                .map_err(|e| e.to_string())?;
            let mut h = HashingWriter {
                inner: &mut dst,
                hash: Sha256::new(),
                size: 0,
            };
            io::copy(&mut src, &mut h).map_err(|e| e.to_string())?;
            let (got, copied) = (artifact::hex(&h.hash.finalize()), h.size);
            dst.sync_all().map_err(|e| e.to_string())?;
            // Closed before the copy is moved into place and run: a file open for writing
            // cannot be run (ETXTBSY).
            drop(dst);
            if got != sha256 || copied != size {
                return Err(changed());
            }
            self.finish(
                &tmp,
                Record {
                    format: FORMAT,
                    id: id.clone(),
                    kind: Kind::Adopted,
                    version: None,
                    source: None,
                    key_id: None,
                    descriptor: None,
                    signature: None,
                    adopted_from: Some(path.display().to_string()),
                    files: vec![FileRecord {
                        path: PROGRAM.into(),
                        size,
                        executable: true,
                        sha256: got,
                    }],
                },
            )
        })();
        if result.is_err() {
            remove_tree(&tmp);
        }
        result.map(|()| (id, true))
    }
}

/// Why the core that came with the app was not installed (`Store::install_bundled`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum BundledRefusal {
    /// Its files beside the app differ from the manifest.
    Damaged(String),
    /// The store holds its version with other files.
    Conflict(String),
    /// The store could not take it.
    Store(String),
}

impl BundledRefusal {
    pub fn message(&self) -> &str {
        match self {
            BundledRefusal::Damaged(m) | BundledRefusal::Conflict(m) | BundledRefusal::Store(m) => {
                m
            }
        }
    }
}

impl Store {
    /// Installs the core that came with the app (`bundled_core.rs`): manifest lists its files,
    /// which place says where to find beside the app. Only those files are read, each opened
    /// without following a link from the folder held open before it, a regular file of the
    /// listed size, its SHA-256 computed while it is copied into staging; anything else beside
    /// them is never read. The version is installed under its release version, recorded as
    /// bundled with app_version.
    ///
    /// Nothing is done when the store already holds that version with the same files and they
    /// still pass their checks; a copy that no longer does is copied in again. When the store
    /// holds the version with other files (a release of that number installed from somewhere
    /// else), nothing is installed: a conflict. Returns whether it was copied in now.
    pub fn install_bundled(
        &self,
        manifest: &crate::bundled_manifest::Manifest,
        place: &crate::bundled_core::Place,
        app_version: &str,
    ) -> Result<bool, BundledRefusal> {
        let id = manifest.version.as_str();
        let damaged = |why: String| {
            BundledRefusal::Damaged(format!(
                "The core that came with the app (core {id}) is damaged ({why}): reinstall or update the app."
            ))
        };
        if !valid_id(id) || id.starts_with(ADOPTED) {
            return Err(damaged(format!("{id} is not a core version")));
        }
        let wanted: Vec<FileRecord> = manifest
            .files
            .iter()
            .map(|f| FileRecord {
                path: f.path.clone(),
                size: f.size,
                executable: f.executable,
                sha256: f.sha256.clone(),
            })
            .collect();
        if let Ok(r) = self.record(id) {
            let key = |f: &FileRecord| (f.path.clone(), f.size, f.executable, f.sha256.clone());
            let mut have: Vec<_> = r.files.iter().map(key).collect();
            let mut want: Vec<_> = wanted.iter().map(key).collect();
            have.sort();
            want.sort();
            if have != want {
                return Err(BundledRefusal::Conflict(format!(
                    "The app's own cores hold a core {id} with other files than the core {id} that came with the app, so the app did not install its own. The one installed stays as it is."
                )));
            }
            if self.program(id).is_ok() {
                return Ok(false);
            }
        }
        let tmp = self.staging().join(random_key());
        let result = (|| {
            exclusive_dir(&tmp).map_err(|e| BundledRefusal::Store(format!("{}: {e}", tmp.display())))?;
            let root = Dir::open(&tmp).map_err(|e| BundledRefusal::Store(e.to_string()))?;
            let open_source = |p: &Path| {
                Dir::open_read(p).map_err(|e| damaged(format!("{}: {e}", p.display())))
            };
            let program_name = place
                .program
                .file_name()
                .and_then(|n| n.to_str())
                .ok_or_else(|| damaged("the core program has no name".into()))?;
            let programs = open_source(place.program.parent().unwrap_or(Path::new("")))?;
            let mut bundled = None;
            for f in &wanted {
                let (from, rel) = if f.path == PROGRAM {
                    (&programs, program_name)
                } else {
                    let rel = f.path.strip_prefix("bundled/").ok_or_else(|| {
                        damaged(format!("{} is not under bundled/", f.path))
                    })?;
                    if bundled.is_none() {
                        bundled = Some(open_source(&place.bundled)?);
                    }
                    (bundled.as_ref().expect("opened above"), rel)
                };
                let missing = |e: io::Error| damaged(format!("{}: {e}", f.path));
                let (parent, name) = parent_beneath_read(from, rel).map_err(missing)?;
                let src = parent.read(&name).map_err(missing)?;
                // Type and size first: never read a device or a FIFO, nor more than listed.
                let m = src.metadata().map_err(missing)?;
                if !m.is_file() {
                    return Err(damaged(format!("{} is not a regular file", f.path)));
                }
                if m.len() != f.size {
                    return Err(damaged(format!(
                        "{} is {} bytes, not {}",
                        f.path,
                        m.len(),
                        f.size
                    )));
                }
                let (dir, dname) = parent_beneath(&root, &f.path, true)
                    .map_err(|e| BundledRefusal::Store(format!("{}: {e}", f.path)))?;
                let mut out = dir
                    .create(&dname, f.executable)
                    .map_err(|e| BundledRefusal::Store(format!("{}: {e}", f.path)))?;
                let mut h = HashingWriter {
                    inner: &mut out,
                    hash: Sha256::new(),
                    size: 0,
                };
                io::copy(&mut (&src).take(f.size + 1), &mut h).map_err(missing)?;
                let (got, copied) = (artifact::hex(&h.hash.finalize()), h.size);
                if got != f.sha256 || copied != f.size {
                    return Err(damaged(format!("{} differs from what the app lists", f.path)));
                }
                out.sync_all()
                    .map_err(|e| BundledRefusal::Store(format!("{}: {e}", f.path)))?;
            }
            // Closed before the copy is moved into place.
            drop((programs, bundled, root));
            self.finish(
                &tmp,
                Record {
                    format: FORMAT,
                    id: id.to_string(),
                    kind: Kind::Bundled,
                    version: Some(id.to_string()),
                    source: Some(format!("bundled with app {app_version}")),
                    key_id: None,
                    descriptor: None,
                    signature: None,
                    adopted_from: None,
                    files: wanted.clone(),
                },
            )
            .map_err(BundledRefusal::Store)
        })();
        if result.is_err() {
            remove_tree(&tmp);
        }
        result.map(|()| true)
    }
}

/// The folder holding rel's last part beneath root, for reading, and that part's name: each folder
/// on the way opened from the one before it, following no link.
fn parent_beneath_read(root: &Dir, rel: &str) -> io::Result<(Dir, String)> {
    let parts: Vec<&str> = rel.split('/').collect();
    let (last, folders) = parts.split_last().expect("split gives one part");
    let mut cur = root.try_clone()?;
    for f in folders {
        cur = cur.sub_read(f)?;
    }
    Ok((cur, last.to_string()))
}

/// Opens the program at path (the person's choice, so a link there is followed) without
/// blocking, and checks it is a regular, executable file of at most 512 MiB.
#[cfg(unix)]
fn open_program(path: &Path) -> Result<File, String> {
    let f = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NONBLOCK | libc::O_NOCTTY)
        .open(path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    let m = f.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() {
        return Err(format!("{} is not a regular file", path.display()));
    }
    if m.permissions().mode() & 0o111 == 0 {
        return Err(format!("{} is not executable", path.display()));
    }
    if m.len() > MAX_ARCHIVE {
        return Err(format!("{} is larger than 512 MiB", path.display()));
    }
    Ok(f)
}

/// Windows: a regular file of at most 512 MiB that is a program image, named `.exe` or `.com` and
/// starting `MZ` (`owned::windows::image`), the only kind the app starts there.
#[cfg(windows)]
fn open_program(path: &Path) -> Result<File, String> {
    let f = File::open(path).map_err(|e| format!("{}: {e}", path.display()))?;
    let m = f.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() {
        return Err(format!("{} is not a regular file", path.display()));
    }
    crate::owned::windows::image(&f, path)?;
    if m.len() > MAX_ARCHIVE {
        return Err(format!("{} is larger than 512 MiB", path.display()));
    }
    // Reading its first bytes moved the file's position (Windows' positional reads do).
    (&f).seek(SeekFrom::Start(0)).map_err(|e| e.to_string())?;
    Ok(f)
}

/// The spelling Windows compares a name by: each character uppercased where that gives one
/// character, as the file system's table maps them one to one (so `ς` and `σ` match, while `ß`
/// stays apart from `ss`).
fn windows_name_key(name: &str) -> String {
    name.chars()
        .map(|c| {
            let mut upper = c.to_uppercase();
            match (upper.next(), upper.next()) {
                (Some(u), None) => u,
                _ => c,
            }
        })
        .collect()
}

struct HashingWriter<'a, W> {
    inner: &'a mut W,
    hash: Sha256,
    size: u64,
}

impl<W: Write> Write for HashingWriter<'_, W> {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        let n = self.inner.write(buf)?;
        self.hash.update(&buf[..n]);
        self.size += n as u64;
        Ok(n)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.inner.flush()
    }
}

fn count_files(dir: &Path) -> usize {
    let mut n = 0;
    for e in fs::read_dir(dir).into_iter().flatten().flatten() {
        match e.file_type() {
            Ok(t) if t.is_dir() => n += count_files(&e.path()),
            _ => n += 1,
        }
    }
    n
}

/// A checked archive in the store, not yet unpacked; dropping it removes the copy.
pub struct Staged {
    verified: Option<VerifiedArchive<File>>,
    path: PathBuf,
    source: String,
    key_id: String,
}

impl Staged {
    pub fn version(&self) -> &str {
        self.verified
            .as_ref()
            .map_or("", |v| v.descriptor().version.as_str())
    }
    pub fn sha256(&self) -> &str {
        self.verified
            .as_ref()
            .map_or("", |v| v.descriptor().sha256.as_str())
    }
    pub fn key_id(&self) -> &str {
        &self.key_id
    }
    pub fn source(&self) -> &str {
        &self.source
    }
    /// The copy in the store, which the native confirmation binds.
    pub fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for Staged {
    fn drop(&mut self) {
        // Closed first: Windows removes no file the store still holds open.
        drop(self.verified.take());
        let _ = fs::remove_file(&self.path);
    }
}

/// An entry's path inside the archive, without `./`; None for the archive's top folder itself.
fn entry_path(raw: &Path) -> Result<Option<String>, String> {
    let mut parts = Vec::new();
    for c in raw.components() {
        match c {
            Component::CurDir => {}
            Component::Normal(p) => {
                let p = p.to_str().ok_or("an archive entry's name is not UTF-8")?;
                // A name Windows cannot hold as it is (a device name, a stream, a trailing dot).
                #[cfg(windows)]
                if crate::vault::windows_refuses(p) {
                    return Err(format!(
                        "archive entry {} has a name Windows cannot hold",
                        raw.display()
                    ));
                }
                parts.push(p.to_string());
            }
            _ => return Err(format!("archive entry {} leaves its folder", raw.display())),
        }
    }
    Ok((!parts.is_empty()).then(|| parts.join("/")))
}

/// Counts every byte the archive decompresses to, and fails past the limit.
struct Bounded<R> {
    inner: R,
    count: u64,
    limit: u64,
    over: Rc<Cell<bool>>,
}

impl<R: Read> Read for Bounded<R> {
    fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
        let n = self.inner.read(buf)?;
        self.count += n as u64;
        if self.count > self.limit {
            self.over.set(true);
            return Err(io::Error::other("the core archive is too large"));
        }
        Ok(n)
    }
}

/// Unpacks a gzip tar archive into the new, empty folder dir: regular files and folders only,
/// no links, devices or names that leave the folder, at most 10,000 entries, and at most limit
/// bytes decompressed in all (headers and skipped data too), as provider releases are. Every
/// file is made from the folder before it, following no links.
fn unpack(archive: impl Read, dir: &Path, limit: u64) -> Result<Vec<FileRecord>, String> {
    let over = Rc::new(Cell::new(false));
    let bounded = Bounded {
        inner: flate2::read::GzDecoder::new(archive),
        count: 0,
        limit,
        over: over.clone(),
    };
    let too_large = || format!("the core archive unpacks to more than {} MiB", limit >> 20);
    let read_err = |e: io::Error| {
        if over.get() {
            too_large()
        } else {
            format!("reading the core archive: {e}")
        }
    };
    let root = Dir::open(dir).map_err(|e| e.to_string())?;
    let mut tar = tar::Archive::new(bounded);
    let mut files = Vec::new();
    // Windows: each path unpacked so far and each folder on its way, by `windows_name_key`.
    let mut spelled: std::collections::HashMap<String, String> = Default::default();
    let entries = tar.entries().map_err(read_err)?;
    for (n, entry) in entries.enumerate() {
        if n >= MAX_ENTRIES {
            return Err("the core archive has more than 10,000 entries".into());
        }
        let mut entry = entry.map_err(read_err)?;
        let raw = entry.path().map_err(read_err)?.into_owned();
        let Some(path) = entry_path(&raw)? else {
            continue;
        };
        // On Windows names that differ only in case are one name: a second spelling would
        // merge with the first, so it is refused.
        let mut end = 0;
        for part in path.split('/').filter(|_| cfg!(windows)) {
            end += part.len();
            let prefix = &path[..end];
            let first = spelled
                .entry(windows_name_key(prefix))
                .or_insert_with(|| prefix.to_string());
            if first != prefix {
                return Err(format!(
                    "archive entry {path} differs only in case from {first}, the same name on Windows"
                ));
            }
            end += 1;
        }
        let kind = entry.header().entry_type();
        if kind.is_dir() {
            let (parent, name) =
                parent_beneath(&root, &path, true).map_err(|e| format!("unpacking {path}: {e}"))?;
            parent
                .make(&name)
                .and_then(|()| parent.sub(&name))
                .map_err(|e| format!("unpacking {path}: {e}"))?;
            continue;
        }
        if !kind.is_file() {
            return Err(format!(
                "the core archive holds {path}, which is not a regular file or folder"
            ));
        }
        let mode = entry.header().mode().map_err(|e| e.to_string())?;
        let executable = mode & 0o111 != 0;
        let (parent, name) =
            parent_beneath(&root, &path, true).map_err(|e| format!("unpacking {path}: {e}"))?;
        let mut out = parent
            .create(&name, executable)
            .map_err(|e| format!("unpacking {path}: {e}"))?;
        let mut h = HashingWriter {
            inner: &mut out,
            hash: Sha256::new(),
            size: 0,
        };
        let copied = io::copy(&mut entry, &mut h).map_err(|e| {
            if over.get() {
                too_large()
            } else {
                format!("unpacking {path}: {e}")
            }
        })?;
        let sha256 = artifact::hex(&h.hash.finalize());
        out.sync_all().map_err(|e| e.to_string())?;
        files.push(FileRecord {
            path,
            size: copied,
            executable,
            sha256,
        });
    }
    if over.get() {
        return Err(too_large());
    }
    Ok(files)
}

/// What the person had in the core's config before a candidate started, to put back if it fails.
pub struct ConfigSnapshot {
    path: PathBuf,
    bytes: Option<Vec<u8>>,
}

impl ConfigSnapshot {
    fn kept_path(store: &Store) -> PathBuf {
        store.dir.join("config-last-good.json")
    }

    /// Says the config did not exist; never both this and the kept copy.
    fn absent_path(store: &Store) -> PathBuf {
        store.dir.join("config-last-good.absent")
    }

    /// Says the kept record was put back already, so a later start leaves the config (and the
    /// person's edits since) alone. Beside the config, whose folder putting it back just wrote.
    fn restored_path(path: &Path) -> PathBuf {
        path.with_extension("restored")
    }

    /// Takes the config at path, and keeps a record of it beside the store's state (a copy, or
    /// a mark that there was none), so a crash while the candidate starts can still put it back.
    /// The record that no longer holds is removed first: a crash in between leaves neither, and
    /// nothing is put back then without the person.
    pub fn take(store: &Store, path: &Path) -> Result<ConfigSnapshot, String> {
        let bytes = match fs::read(path) {
            Ok(b) => Some(b),
            Err(e) if e.kind() == io::ErrorKind::NotFound => None,
            Err(e) => return Err(format!("{}: {e}", path.display())),
        };
        let (keep, drop) = match bytes {
            Some(_) => (Self::kept_path(store), Self::absent_path(store)),
            None => (Self::absent_path(store), Self::kept_path(store)),
        };
        for drop in [Self::restored_path(path), drop] {
            match fs::remove_file(&drop) {
                Err(e) if e.kind() != io::ErrorKind::NotFound => {
                    return Err(format!("{}: {e}", drop.display()))
                }
                _ => {}
            }
        }
        write_private(&keep, bytes.as_deref().unwrap_or_default())
            .map_err(|e| format!("{}: {e}", keep.display()))?;
        Ok(ConfigSnapshot {
            path: path.into(),
            bytes,
        })
    }

    /// The record a crashed switch left, for path; none when it was put back already. Any error
    /// reading it is an error, never a config to delete.
    pub fn kept(store: &Store, path: &Path) -> Result<Option<ConfigSnapshot>, String> {
        if fs::symlink_metadata(Self::restored_path(path)).is_ok() {
            return Ok(None);
        }
        let kept = Self::kept_path(store);
        let bytes = match fs::read(&kept) {
            Ok(b) => Some(b),
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                let absent = Self::absent_path(store);
                match fs::symlink_metadata(&absent) {
                    Ok(_) => None,
                    Err(e) => {
                        return Err(format!(
                            "the app was switching cores, and its copy of the config is missing ({}: {e}); check {} and remove the candidate from {}",
                            kept.display(),
                            path.display(),
                            store.dir.join("state.json").display()
                        ))
                    }
                }
            }
            Err(e) => return Err(format!("reading {}: {e}", kept.display())),
        };
        Ok(Some(ConfigSnapshot {
            path: path.into(),
            bytes,
        }))
    }

    /// Where this snapshot's record is kept: the copy, or the mark that there was no config.
    pub fn kept_at(&self, store: &Store) -> PathBuf {
        match self.bytes {
            Some(_) => Self::kept_path(store),
            None => Self::absent_path(store),
        }
    }

    /// Puts the config back as it was, and marks it put back: when the switch's end cannot be
    /// recorded, the next start must not put it back again over the person's later edits.
    pub fn restore(&self) -> Result<(), String> {
        match &self.bytes {
            Some(b) => write_private(&self.path, b),
            None => match fs::remove_file(&self.path) {
                Err(e) if e.kind() != io::ErrorKind::NotFound => Err(e),
                _ => Ok(()),
            },
        }
        .map_err(|e| format!("restoring {}: {e}", self.path.display()))?;
        // The config is back either way; without the mark, only a later start's restore repeats.
        let _ = write_private(&Self::restored_path(&self.path), b"");
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// macOS: the store's folders keep no extended access list entry, as they keep no group or
    /// other mode bits, so nothing made in them inherits one.
    #[cfg(target_os = "macos")]
    #[test]
    fn the_store_passes_no_access_list_on() {
        let dir = std::env::temp_dir().join(format!("pluxy-cores-acl-{}", random_key()));
        let cores = dir.join("cores");
        fs::create_dir_all(cores.join("versions")).unwrap();
        for d in [&cores, &cores.join("versions")] {
            crate::private_macos::add_entry(
                d,
                "everyone allow list,file_inherit,directory_inherit",
            );
        }
        let store = Store::open(&cores).unwrap();
        let has =
            |p: &Path| crate::private_macos::has_entries(&fs::File::open(p).unwrap()).unwrap();
        for d in [
            &cores,
            &store.versions(),
            &store.records(),
            &store.staging(),
        ] {
            assert!(!has(d), "{}", d.display());
        }
        fs::remove_dir_all(dir).unwrap();
    }

    /// macOS: a version whose program an extended access list entry lets others change is not
    /// run, as one with a write mode bit is not.
    #[cfg(target_os = "macos")]
    #[test]
    fn a_program_with_an_access_list_entry_is_not_run() {
        let dir = std::env::temp_dir().join(format!("pluxy-cores-acl-{}", random_key()));
        fs::create_dir_all(&dir).unwrap();
        let src = dir.join("pluxy");
        fs::write(&src, b"#!/bin/sh\nexit 0\n").unwrap();
        fs::set_permissions(&src, fs::Permissions::from_mode(0o755)).unwrap();
        let (sha, size) = sha256_of(File::open(&src).unwrap()).unwrap();
        let store = Store::open(&dir.join("cores")).unwrap();
        let (id, _) = store.adopt(&src, &sha, size).unwrap();
        // The positive control: the version as installed is run.
        store.program(&id).unwrap();
        crate::private_macos::add_entry(&store.folder(&id).join(PROGRAM), "everyone allow write");
        let e = store.program(&id).unwrap_err();
        assert!(e.contains("changed since it was installed"), "{e}");
        let _ = std::process::Command::new("/bin/chmod")
            .args(["-R", "u+w"])
            .arg(&dir)
            .status();
        fs::remove_dir_all(dir).unwrap();
    }

    /// N5: a config that did not exist is recorded by the absent mark, and that is what a
    /// failed restore names.
    #[test]
    fn the_kept_record_named_is_the_one_kept() {
        let dir = std::env::temp_dir().join(format!("pluxy-kept-{}", random_key()));
        let store = Store::open(&dir.join("cores")).unwrap();
        let config = dir.join("config.json");
        let none = ConfigSnapshot::take(&store, &config).unwrap();
        assert_eq!(
            none.kept_at(&store),
            dir.join("cores/config-last-good.absent")
        );
        assert!(none.kept_at(&store).exists());
        fs::write(&config, b"{}").unwrap();
        let some = ConfigSnapshot::take(&store, &config).unwrap();
        assert_eq!(
            some.kept_at(&store),
            dir.join("cores/config-last-good.json")
        );
        assert!(some.kept_at(&store).exists());
        fs::remove_dir_all(dir).unwrap();
    }

    fn gz_tar(entries: &[(&str, tar::EntryType, Vec<u8>)]) -> Vec<u8> {
        let gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
        let mut b = tar::Builder::new(gz);
        for (name, kind, data) in entries {
            let mut h = tar::Header::new_ustar();
            h.as_old_mut().name[..name.len()].copy_from_slice(name.as_bytes());
            h.set_entry_type(*kind);
            h.set_size(data.len() as u64);
            h.set_mode(0o755);
            h.set_cksum();
            b.append(&h, &data[..]).unwrap();
        }
        b.into_inner().unwrap().finish().unwrap()
    }

    /// A5: what an archive decompresses counts toward the limit, also the data of entries that
    /// are not unpacked as files (the top folder, folders).
    #[test]
    fn every_decompressed_byte_counts_toward_the_limit() {
        let limit = 1 << 20;
        for (name, kind) in [
            (".", tar::EntryType::Directory),
            ("dir", tar::EntryType::Directory),
        ] {
            let dir = std::env::temp_dir().join(format!("pluxy-unpack-{}", random_key()));
            fs::create_dir_all(&dir).unwrap();
            let archive = gz_tar(&[
                (name, kind, vec![0u8; 2 << 20]),
                ("pluxy", tar::EntryType::Regular, b"x".to_vec()),
            ]);
            let r = unpack(&archive[..], &dir, limit);
            fs::remove_dir_all(&dir).unwrap();
            assert!(r.unwrap_err().contains("more than 1 MiB"), "{name}");
        }
        // Within the limit, the same layout unpacks.
        let dir = std::env::temp_dir().join(format!("pluxy-unpack-{}", random_key()));
        fs::create_dir_all(&dir).unwrap();
        let archive = gz_tar(&[
            (".", tar::EntryType::Directory, Vec::new()),
            ("pluxy", tar::EntryType::Regular, b"x".to_vec()),
        ]);
        let files = unpack(&archive[..], &dir, limit).unwrap();
        fs::remove_dir_all(&dir).unwrap();
        assert_eq!(files.len(), 1);
    }

    fn temp(name: &str) -> PathBuf {
        std::env::temp_dir().join(format!("pluxy-cores-{name}-{}", random_key()))
    }

    /// The layout of a core version: its program, and its bundled packages beside it.
    fn layout() -> Vec<(&'static str, tar::EntryType, Vec<u8>)> {
        vec![
            (PROGRAM, tar::EntryType::Regular, b"MZ program".to_vec()),
            ("bundled", tar::EntryType::Directory, Vec::new()),
            (
                "bundled/index.json",
                tar::EntryType::Regular,
                b"{}".to_vec(),
            ),
        ]
    }

    /// Installs version id from an archive of entries as a confirmed install does once the
    /// release's signature is checked: unpacked into the staging folder, then moved into place.
    fn install(store: &Store, id: &str, entries: &[(&str, tar::EntryType, Vec<u8>)]) {
        let tmp = store.staging().join(random_key());
        exclusive_dir(&tmp).unwrap();
        let files = unpack(&gz_tar(entries)[..], &tmp, MAX_UNPACKED).unwrap();
        store
            .finish(
                &tmp,
                Record {
                    format: FORMAT,
                    id: id.into(),
                    kind: Kind::Signed,
                    version: Some(id.into()),
                    source: None,
                    key_id: None,
                    descriptor: None,
                    signature: None,
                    adopted_from: None,
                    files,
                },
            )
            .unwrap();
    }

    /// Makes an installed file writable again, as its owner may (its execute bit as it was).
    fn writable(path: &Path) {
        #[cfg(unix)]
        {
            let mode = fs::metadata(path).unwrap().permissions().mode();
            fs::set_permissions(path, fs::Permissions::from_mode(mode | 0o200)).unwrap();
        }
        #[cfg(windows)]
        {
            let f = crate::private_windows::open_for_access_list(path, false).unwrap();
            crate::private_windows::make_private(&f, false).unwrap();
        }
    }

    /// Puts a link at link to the folder target.
    fn link_folder(target: &Path, link: &Path) {
        #[cfg(unix)]
        std::os::unix::fs::symlink(target, link).unwrap();
        #[cfg(windows)]
        crate::private_windows::tests::junction(target, link);
    }

    /// A version starts only as it was installed: its program is checked and opened, and one
    /// whose file was left writable, changed, added to, or swapped for a link is refused before
    /// it starts. Removing a version removes its read-only files.
    #[test]
    fn an_installed_version_starts_only_as_installed() {
        let dir = temp("check");
        let store = Store::open(&dir).unwrap();
        for v in ["1.0.0", "2.0.0", "3.0.0", "4.0.0"] {
            install(&store, v, &layout());
        }
        // The positive control: an unchanged version opens its program, at its start.
        let mut program = store.program("1.0.0").unwrap();
        let mut bytes = Vec::new();
        program.read_to_end(&mut bytes).unwrap();
        assert_eq!(bytes, b"MZ program");
        drop(program);
        let index = |v: &str| store.folder(v).join("bundled").join("index.json");
        assert!(
            OpenOptions::new()
                .append(true)
                .open(index("1.0.0"))
                .is_err(),
            "an installed file can be written to"
        );
        let changed = |v: &str| {
            let e = store.program(v).unwrap_err();
            assert!(e.contains("changed since it was installed"), "{v}: {e}");
        };

        // Made writable, its bytes the same.
        writable(&index("1.0.0"));
        changed("1.0.0");
        // Changed, and made read-only again.
        writable(&index("2.0.0"));
        fs::write(index("2.0.0"), b"{ }").unwrap();
        lock(&index("2.0.0")).unwrap();
        changed("2.0.0");
        // A file added.
        fs::write(store.folder("3.0.0").join("extra"), b"x").unwrap();
        changed("3.0.0");
        // Its folder swapped for a link to a copy with the same files, locked as installed ones
        // are (the test archive's files are 0755), so only the link can make it fail.
        let copy = dir.join("copy");
        fs::create_dir(&copy).unwrap();
        fs::write(copy.join("index.json"), b"{}").unwrap();
        #[cfg(unix)]
        fs::set_permissions(copy.join("index.json"), fs::Permissions::from_mode(0o755)).unwrap();
        lock(&copy).unwrap();
        let bundled = store.folder("4.0.0").join("bundled");
        fs::rename(&bundled, dir.join("aside")).unwrap();
        link_folder(&copy, &bundled);
        changed("4.0.0");

        for v in ["1.0.0", "2.0.0", "3.0.0", "4.0.0"] {
            store.remove(v);
            assert!(!store.folder(v).exists(), "{v} was not removed");
        }
        remove_tree(&dir);
    }

    /// An adopted program is copied into a version of its own, which then starts; the program
    /// itself never runs from where it is.
    #[test]
    fn an_adopted_program_is_copied_in_and_checked() {
        let dir = temp("adopt");
        let store = Store::open(&dir.join("cores")).unwrap();
        let program = dir.join(if cfg!(windows) {
            "custom.exe"
        } else {
            "custom"
        });
        fs::write(&program, b"MZ custom program").unwrap();
        #[cfg(unix)]
        fs::set_permissions(&program, fs::Permissions::from_mode(0o755)).unwrap();
        let (sha, size) = Store::inspect(&program).unwrap();
        assert_eq!(size, 17);
        let (id, copied) = store.adopt(&program, &sha, size).unwrap();
        assert!(copied);
        assert_eq!(id, Store::adopted_id(&sha));
        let mut bytes = Vec::new();
        store.program(&id).unwrap().read_to_end(&mut bytes).unwrap();
        assert_eq!(bytes, b"MZ custom program");
        // Changed after the person confirmed it: not copied in.
        fs::write(&program, b"MZ other program!").unwrap();
        store.remove(&id);
        assert!(store
            .adopt(&program, &sha, size)
            .unwrap_err()
            .contains("changed after you confirmed it"));
        // A file that is no program (on Unix not executable, on Windows no program image) is
        // refused before anything is copied.
        let text = dir.join(if cfg!(windows) { "text.exe" } else { "text" });
        fs::write(&text, b"just some text").unwrap();
        assert!(Store::inspect(&text).is_err());
        remove_tree(&dir);
    }

    /// Windows: the store and each version's folders are private by their access lists, and an
    /// installed file is read-only by its own.
    #[cfg(windows)]
    #[test]
    fn the_store_is_private_and_its_files_read_only_on_windows() {
        use crate::private_windows::{is_private, is_read_only, open_for_access_list};
        // The store's folder made beforehand, as any folder in the temporary folder is: not
        // private (the control that the check below can fail), until the store opens it.
        let dir = temp("private");
        fs::create_dir_all(&dir).unwrap();
        let f = open_for_access_list(&dir, true).unwrap();
        assert!(
            !is_private(&f).unwrap(),
            "a new temporary folder is private already"
        );
        drop(f);
        let store = Store::open(&dir).unwrap();
        install(&store, "1.0.0", &layout());
        let version = store.folder("1.0.0");
        for d in [
            dir.clone(),
            store.versions(),
            store.records(),
            store.staging(),
            version.clone(),
            version.join("bundled"),
        ] {
            let f = open_for_access_list(&d, true).unwrap();
            assert!(is_private(&f).unwrap(), "{} is not private", d.display());
        }
        for file in [
            version.join(PROGRAM),
            version.join("bundled").join("index.json"),
        ] {
            let f = open_for_access_list(&file, false).unwrap();
            assert!(
                is_read_only(&f).unwrap(),
                "{} is not read-only",
                file.display()
            );
        }
        // The positive control: a private file that can be written is not read-only.
        let record = store.record_path("1.0.0");
        let f = open_for_access_list(&record, false).unwrap();
        assert!(is_private(&f).unwrap() && !is_read_only(&f).unwrap());
        drop(f);
        remove_tree(&dir);
    }

    /// Windows: an archive entry whose name Windows cannot hold plainly (a stream, a device, a
    /// trailing dot) is refused, not unpacked as another name.
    #[cfg(windows)]
    #[test]
    fn names_windows_cannot_hold_are_refused() {
        // Not `a:stream`: a one-letter name before `:` is a drive on Windows, refused as leaving
        // the folder before this rule is reached.
        for name in [
            "ab:stream",
            "CON",
            "nul.txt",
            "NUL .txt",
            "COM\u{b9}",
            "dot.",
            "space ",
        ] {
            let dir = temp("names");
            fs::create_dir_all(&dir).unwrap();
            let archive = gz_tar(&[
                (PROGRAM, tar::EntryType::Regular, b"MZ".to_vec()),
                (name, tar::EntryType::Regular, b"x".to_vec()),
            ]);
            let e = unpack(&archive[..], &dir, MAX_UNPACKED).unwrap_err();
            assert!(e.contains("Windows cannot hold"), "{name}: {e}");
            remove_tree(&dir);
        }
    }

    /// Two names that differ only in case, of files or of folders on the way to them, are one
    /// name on Windows: there the second spelling is refused, never merged with the first.
    /// Elsewhere the store does not check them, as before. Names that differ otherwise unpack.
    #[test]
    fn names_differing_only_in_case_are_refused() {
        let twins: [&[(&str, tar::EntryType, Vec<u8>)]; 3] = [
            &[
                ("a.txt", tar::EntryType::Regular, b"one".to_vec()),
                ("A.txt", tar::EntryType::Regular, b"two".to_vec()),
            ],
            &[
                ("bundled/a", tar::EntryType::Regular, b"one".to_vec()),
                ("Bundled/b", tar::EntryType::Regular, b"two".to_vec()),
            ],
            &[
                ("\u{3c3}.txt", tar::EntryType::Regular, b"one".to_vec()),
                ("\u{3c2}.txt", tar::EntryType::Regular, b"two".to_vec()),
            ],
        ];
        for entries in twins {
            let dir = temp("case");
            fs::create_dir_all(&dir).unwrap();
            let r = unpack(&gz_tar(entries)[..], &dir, MAX_UNPACKED);
            if cfg!(windows) {
                let e = r.unwrap_err();
                assert!(e.contains("differs only in case"), "{e}");
            } else if cfg!(target_os = "linux") {
                // Two names there, as before.
                assert_eq!(r.unwrap().len(), 2);
            } else if let Err(e) = r {
                // macOS: its file system decides, as before (by default one name, so the second
                // file's exclusive create fails); the case check itself does not run.
                assert!(!e.contains("differs only in case"), "{e}");
            }
            remove_tree(&dir);
        }
        // The key Windows compares by, on every system: final and medial sigma, long s, dotless i
        // and the micro sign match their twins; `ß` is not `ss`.
        for (a, b) in [
            ("\u{3c3}", "\u{3c2}"),
            ("s", "\u{17f}"),
            ("i", "\u{131}"),
            ("\u{3bc}", "\u{b5}"),
        ] {
            assert_eq!(windows_name_key(a), windows_name_key(b), "{a} {b}");
        }
        assert_ne!(windows_name_key("\u{df}"), windows_name_key("ss"));
        // The control: other names, also in the same folder, unpack.
        let dir = temp("case");
        fs::create_dir_all(&dir).unwrap();
        let entries = [
            ("bundled/a", tar::EntryType::Regular, b"one".to_vec()),
            ("bundled/b", tar::EntryType::Regular, b"two".to_vec()),
        ];
        assert_eq!(
            unpack(&gz_tar(&entries)[..], &dir, MAX_UNPACKED)
                .unwrap()
                .len(),
            2
        );
        remove_tree(&dir);
    }

    /// Windows: a store that is not on a fixed local drive (here a network share) is refused
    /// before anything is made there.
    #[cfg(windows)]
    #[test]
    fn a_store_off_a_fixed_local_drive_is_refused() {
        let e = Store::open(Path::new(r"\\pluxy-test-host\share\cores")).unwrap_err();
        assert!(e.contains("not on a local drive"), "{e}");
        // The control: the test's own temporary folder is on one.
        let dir = temp("fixed");
        Store::open(&dir).unwrap();
        // A link on that drive to the same folder through a share of this computer (the
        // runner's administrative share `\\localhost\<drive>$`): its path starts with a fixed
        // drive's letter, but its real path is the share.
        let text = dir.to_string_lossy().into_owned();
        let (drive, rest) = text.split_once(":\\").expect("a drive path");
        let share = PathBuf::from(format!(r"\\localhost\{drive}$\{rest}"));
        assert!(
            fs::metadata(&share).is_ok(),
            "{} is not reachable",
            share.display()
        );
        let link = temp("fixed-link");
        std::os::windows::fs::symlink_dir(&share, &link).unwrap();
        let e = Store::open(&link.join("cores")).unwrap_err();
        assert!(e.contains("not on a local drive"), "{e}");
        fs::remove_dir(&link).unwrap();
        remove_tree(&dir);
    }
}
