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
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read, Seek, SeekFrom, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Component, Path, PathBuf};

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

    fn record_path(&self, id: &str) -> PathBuf {
        self.versions().join(format!("{id}.json"))
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
        if r.format != FORMAT || r.id != id || !self.folder(id).is_dir() {
            return Err(format!("core {id} is not installed properly"));
        }
        Ok(r)
    }

    /// The installed versions, by ID.
    pub fn list(&self) -> Vec<Installed> {
        let mut ids: Vec<String> = fs::read_dir(self.versions())
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
        let folder = self.folder(id);
        let mut seen = 0usize;
        let mut program = None;
        for f in &r.files {
            let path = folder.join(&f.path);
            let file = OpenOptions::new()
                .read(true)
                .custom_flags(libc::O_NOFOLLOW)
                .open(&path)
                .map_err(|e| format!("core {id}: {}: {e}", f.path))?;
            let (sha, size) = sha256_of(&file).map_err(|e| format!("core {id}: {e}"))?;
            let m = file.metadata().map_err(|e| e.to_string())?;
            if !m.is_file()
                || sha != f.sha256
                || size != f.size
                || (m.permissions().mode() & 0o100 != 0) != f.executable
                || m.permissions().mode() & 0o222 != 0
            {
                return Err(format!(
                    "core {id} changed since it was installed ({}): install it again",
                    f.path
                ));
            }
            seen += 1;
            if f.path == PROGRAM {
                program = Some(file);
            }
        }
        if count_files(&folder) != seen {
            return Err(format!(
                "core {id} changed since it was installed (files were added): install it again"
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
        if self.installed(version) {
            return Err(format!(
                "core {version} is already installed: roll back to it instead"
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
        if !valid_id(&id) || self.installed(&id) {
            return Err(format!("core {id} is already installed"));
        }
        let tmp = self.staging().join(random_key());
        let result = (|| {
            private_dir(&tmp).map_err(|e| e.to_string())?;
            let files = unpack(verified.into_archive(), &tmp)?;
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
    /// last, so a version without a record is never used.
    fn finish(&self, tmp: &Path, r: Record) -> Result<(), String> {
        let folder = self.folder(&r.id);
        remove_tree(&folder);
        fs::rename(tmp, &folder).map_err(|e| format!("installing core {}: {e}", r.id))?;
        let bytes = serde_json::to_vec_pretty(&r).expect("JSON");
        let done = lock(&folder).map_err(|e| e.to_string()).and_then(|()| {
            write_private(&self.record_path(&r.id), &bytes).map_err(|e| e.to_string())
        });
        if let Err(e) = done {
            remove_tree(&folder);
            return Err(format!("installing core {}: {e}", r.id));
        }
        Ok(())
    }

    /// The core program at path, to adopt: its SHA-256 and size, for the person to confirm.
    pub fn inspect(path: &Path) -> Result<(String, u64), String> {
        if !path.is_absolute() {
            return Err("the core program must be given as a full path".into());
        }
        let f = File::open(path).map_err(|e| format!("{}: {e}", path.display()))?;
        let m = f.metadata().map_err(|e| e.to_string())?;
        if !m.is_file() || m.permissions().mode() & 0o111 == 0 {
            return Err(format!("{} is not an executable file", path.display()));
        }
        sha256_of(f).map_err(|e| e.to_string())
    }

    /// Copies the program at path into a version of its own, `custom-<first 12 digits of its
    /// SHA-256>`, if it still has the SHA-256 the person confirmed.
    pub fn adopt(&self, path: &Path, sha256: &str) -> Result<String, String> {
        let id = format!("custom-{}", &sha256[..12.min(sha256.len())]);
        if self.installed(&id) {
            return Ok(id);
        }
        let tmp = self.staging().join(random_key());
        let result = (|| {
            private_dir(&tmp).map_err(|e| e.to_string())?;
            let mut src = File::open(path).map_err(|e| format!("{}: {e}", path.display()))?;
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
            let (got, size) = (artifact::hex(&h.hash.finalize()), h.size);
            dst.sync_all().map_err(|e| e.to_string())?;
            if got != sha256 {
                return Err(format!(
                    "{} changed after you confirmed it: adopt it again",
                    path.display()
                ));
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
        result.map(|()| id)
    }
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

/// Unpacks a gzip tar archive into dir: regular files and folders only, no links, devices or
/// names that leave the folder, at most 10,000 entries and 512 MiB, as provider releases are.
fn unpack(archive: File, dir: &Path) -> Result<Vec<FileRecord>, String> {
    let mut tar = tar::Archive::new(flate2::read::GzDecoder::new(archive));
    let mut files = Vec::new();
    let mut total = 0u64;
    let entries = tar
        .entries()
        .map_err(|e| format!("reading the core archive: {e}"))?;
    for (n, entry) in entries.enumerate() {
        if n >= MAX_ENTRIES {
            return Err("the core archive has more than 10,000 entries".into());
        }
        let mut entry = entry.map_err(|e| format!("reading the core archive: {e}"))?;
        let raw = entry
            .path()
            .map_err(|e| format!("reading the core archive: {e}"))?
            .into_owned();
        let Some(path) = entry_path(&raw)? else {
            continue;
        };
        let target = dir.join(&path);
        let kind = entry.header().entry_type();
        if kind.is_dir() {
            private_dir(&target).map_err(|e| e.to_string())?;
            continue;
        }
        if !kind.is_file() {
            return Err(format!(
                "the core archive holds {path}, which is not a regular file or folder"
            ));
        }
        let mode = entry.header().mode().map_err(|e| e.to_string())?;
        let executable = mode & 0o111 != 0;
        if let Some(parent) = target.parent() {
            private_dir(parent).map_err(|e| e.to_string())?;
        }
        let mut out = OpenOptions::new()
            .write(true)
            .create_new(true)
            .custom_flags(libc::O_NOFOLLOW)
            .mode(if executable { 0o700 } else { 0o600 })
            .open(&target)
            .map_err(|e| format!("unpacking {path}: {e}"))?;
        let left = MAX_UNPACKED - total;
        let mut h = HashingWriter {
            inner: &mut out,
            hash: Sha256::new(),
            size: 0,
        };
        let copied = io::copy(&mut (&mut entry).take(left + 1), &mut h)
            .map_err(|e| format!("unpacking {path}: {e}"))?;
        if copied > left {
            return Err("the core archive unpacks to more than 512 MiB".into());
        }
        total += copied;
        let sha256 = artifact::hex(&h.hash.finalize());
        out.sync_all().map_err(|e| e.to_string())?;
        files.push(FileRecord {
            path,
            size: copied,
            executable,
            sha256,
        });
    }
    Ok(files)
}

/// What the person had in the core's config before a candidate started, to put back if it fails.
pub struct ConfigSnapshot {
    path: PathBuf,
    bytes: Option<Vec<u8>>,
}

impl ConfigSnapshot {
    /// Takes the config at path, and keeps a copy beside the store's state, so a crash while the
    /// candidate starts can still put it back.
    pub fn take(store: &Store, path: &Path) -> Result<ConfigSnapshot, String> {
        let bytes = match fs::read(path) {
            Ok(b) => Some(b),
            Err(e) if e.kind() == io::ErrorKind::NotFound => None,
            Err(e) => return Err(format!("{}: {e}", path.display())),
        };
        let kept = store.dir.join("config-last-good.json");
        match &bytes {
            Some(b) => write_private(&kept, b).map_err(|e| format!("{}: {e}", kept.display()))?,
            None => {
                let _ = fs::remove_file(&kept);
            }
        }
        Ok(ConfigSnapshot {
            path: path.into(),
            bytes,
        })
    }

    /// The copy a crashed switch left, for path.
    pub fn kept(store: &Store, path: &Path) -> ConfigSnapshot {
        ConfigSnapshot {
            path: path.into(),
            bytes: fs::read(store.dir.join("config-last-good.json")).ok(),
        }
    }

    /// Puts the config back as it was.
    pub fn restore(&self) -> Result<(), String> {
        match &self.bytes {
            Some(b) => write_private(&self.path, b),
            None => match fs::remove_file(&self.path) {
                Err(e) if e.kind() != io::ErrorKind::NotFound => Err(e),
                _ => Ok(()),
            },
        }
        .map_err(|e| format!("restoring {}: {e}", self.path.display()))
    }
}
