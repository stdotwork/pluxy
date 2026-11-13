//! The app's own core versions, in managed mode: each version the app installed from a signed
//! release, or copied in from a core program the person chose ("adopted"), in a folder of its own
//! that the app never changes after installing it, and a selection of which one runs.
//!
//! A release comes from the core source the person set: an index file listing releases and, for
//! each, its signed artifacts (see the README's "The app's own core"). An artifact is a gzip tar
//! file of the core's folder (`pluxy` and anything beside it, such as `bundled/`), described by a
//! descriptor signed with a trusted publisher key; `artifact.rs` checks the signature, size and
//! SHA-256 before anything is unpacked. Each installed file's digest is kept and checked again
//! before the core starts. Nothing in this module needs Tauri.

use crate::artifact::{self, PublisherKeys, Tier, VerifiedArchive};
use crate::managed::{random_key, write_private};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::cell::Cell;
use std::ffi::CString;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Seek, SeekFrom, Write};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt};
use std::path::{Component, Path, PathBuf};
use std::rc::Rc;

/// The publisher and ID of core releases.
pub const CORE_ID: &str = "pluxy/core";
/// The core's program in a version's folder.
pub const PROGRAM: &str = "pluxy";
const FORMAT: u32 = 1;
const MAX_INDEX: u64 = 1024 * 1024;
const MAX_ARCHIVE: u64 = 512 * 1024 * 1024;
const MAX_UNPACKED: u64 = 512 * 1024 * 1024;
const MAX_ENTRIES: usize = 10_000;

/// This computer's platform, as descriptors name it, such as `linux/amd64`.
pub fn platform() -> String {
    let arch = match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        a => a,
    };
    format!("{}/{arch}", std::env::consts::OS)
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
struct FileRecord {
    path: String,
    size: u64,
    executable: bool,
    sha256: String,
}

#[derive(Clone, Copy, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum Kind {
    /// From a signed release.
    Signed,
    /// Copied in from a core program the person chose.
    Adopted,
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

/// Which version runs, `state.json`. selected is the last version that started and answered;
/// a candidate is being started in its place, and a crash then leaves it recorded here.
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq)]
pub struct State {
    #[serde(default)]
    pub format: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub selected: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub candidate: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub failed: Option<Failed>,
}

/// The app's core versions, `<data>/cores`.
#[derive(Clone, Debug)]
pub struct Store {
    dir: PathBuf,
}

fn private_dir(path: &Path) -> io::Result<()> {
    fs::create_dir_all(path)?;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
}

/// Makes a new private folder at path, failing if anything is already there.
fn exclusive_dir(path: &Path) -> io::Result<()> {
    fs::DirBuilder::new().mode(0o700).create(path)
}

fn c_string(bytes: &[u8]) -> io::Result<CString> {
    CString::new(bytes).map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "a name holds NUL"))
}

fn owned(fd: libc::c_int) -> io::Result<OwnedFd> {
    if fd < 0 {
        Err(io::Error::last_os_error())
    } else {
        Ok(unsafe { OwnedFd::from_raw_fd(fd) })
    }
}

/// Opens the folder at path; a link there is refused.
fn open_dir(path: &Path) -> io::Result<OwnedFd> {
    let c = c_string(path.as_os_str().as_bytes())?;
    owned(unsafe {
        libc::open(
            c.as_ptr(),
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
        )
    })
}

/// Opens name in the folder dir, following no link.
fn open_at(dir: &OwnedFd, name: &str, flags: libc::c_int, mode: u32) -> io::Result<OwnedFd> {
    let c = c_string(name.as_bytes())?;
    owned(unsafe {
        libc::openat(
            dir.as_raw_fd(),
            c.as_ptr(),
            flags | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            mode as libc::c_uint,
        )
    })
}

/// The folder holding rel's last part, beneath root, and that part's name: each folder on the
/// way is opened from the one before it, following no link; with create, missing ones are made.
fn parent_beneath(root: &OwnedFd, rel: &str, create: bool) -> io::Result<(OwnedFd, String)> {
    let parts: Vec<&str> = rel.split('/').collect();
    let (last, folders) = parts.split_last().expect("split gives one part");
    let mut cur = root.try_clone()?;
    for f in folders {
        if create {
            let c = c_string(f.as_bytes())?;
            if unsafe { libc::mkdirat(cur.as_raw_fd(), c.as_ptr(), 0o700) } != 0 {
                let e = io::Error::last_os_error();
                if e.kind() != io::ErrorKind::AlreadyExists {
                    return Err(e);
                }
            }
        }
        cur = open_at(&cur, f, libc::O_RDONLY | libc::O_DIRECTORY, 0)?;
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

fn remove_tree(path: &Path) {
    let _ = fs::remove_dir_all(path);
}

/// Makes the files under path read-only: this user may read them, and run the executable ones.
/// Folders stay writable by this user only (0700), so the store, or the person, can still
/// remove them; the digests checked before each start are what keep a version unchanged.
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

impl Store {
    /// The store in dir, made if missing.
    pub fn open(dir: &Path) -> Result<Store, String> {
        let s = Store { dir: dir.into() };
        private_dir(&s.versions()).map_err(|e| format!("{}: {e}", dir.display()))?;
        private_dir(&s.records()).map_err(|e| format!("{}: {e}", dir.display()))?;
        private_dir(&s.staging()).map_err(|e| format!("{}: {e}", dir.display()))?;
        fs::set_permissions(dir, fs::Permissions::from_mode(0o700))
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
        };
        let folder = self.folder(id);
        let root = open_dir(&folder).map_err(|e| format!("core {id}: {e}"))?;
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
            let file = File::from(
                open_at(&dir, &name, libc::O_RDONLY | libc::O_NONBLOCK, 0)
                    .map_err(|_| changed())?,
            );
            // Type and size first: never read a device or a FIFO, nor more than was installed.
            let m = file.metadata().map_err(|e| e.to_string())?;
            if !m.is_file()
                || m.len() != f.size
                || (m.permissions().mode() & 0o100 != 0) != f.executable
                || m.permissions().mode() & 0o222 != 0
            {
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
        // Trust first: an unknown key or source is refused before the archive is read.
        keys.publisher_key(source, &a.key_id, "pluxy")?;
        let from = source_path(source)?
            .parent()
            .ok_or("the core source has no folder")?
            .join(name);
        let staged = self.staging().join(format!("{}.tar.gz", random_key()));
        let copied = (|| -> Result<File, String> {
            let src = File::open(&from).map_err(|e| format!("{}: {e}", from.display()))?;
            let mut dst = OpenOptions::new()
                .read(true)
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&staged)
                .map_err(|e| e.to_string())?;
            let n = io::copy(&mut src.take(d.size + 1), &mut dst).map_err(|e| e.to_string())?;
            if n != d.size {
                return Err("archive size differs from signed descriptor".into());
            }
            dst.sync_all().map_err(|e| e.to_string())?;
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
            if !program.executable {
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
            let mut dst = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o700)
                .open(tmp.join(PROGRAM))
                .map_err(|e| e.to_string())?;
            let mut h = HashingWriter {
                inner: &mut dst,
                hash: Sha256::new(),
                size: 0,
            };
            io::copy(&mut src, &mut h).map_err(|e| e.to_string())?;
            let (got, copied) = (artifact::hex(&h.hash.finalize()), h.size);
            dst.sync_all().map_err(|e| e.to_string())?;
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

/// Opens the program at path (the person's choice, so a link there is followed) without
/// blocking, and checks it is a regular, executable file of at most 512 MiB.
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
    let root = open_dir(dir).map_err(|e| e.to_string())?;
    let mut tar = tar::Archive::new(bounded);
    let mut files = Vec::new();
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
        let kind = entry.header().entry_type();
        if kind.is_dir() {
            let (parent, name) =
                parent_beneath(&root, &path, true).map_err(|e| format!("unpacking {path}: {e}"))?;
            let c = c_string(name.as_bytes()).map_err(|e| e.to_string())?;
            if unsafe { libc::mkdirat(parent.as_raw_fd(), c.as_ptr(), 0o700) } != 0 {
                let e = io::Error::last_os_error();
                if e.kind() != io::ErrorKind::AlreadyExists {
                    return Err(format!("unpacking {path}: {e}"));
                }
            }
            open_at(&parent, &name, libc::O_RDONLY | libc::O_DIRECTORY, 0)
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
        let mut out = File::from(
            open_at(
                &parent,
                &name,
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL,
                if executable { 0o700 } else { 0o600 },
            )
            .map_err(|e| format!("unpacking {path}: {e}"))?,
        );
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
}
