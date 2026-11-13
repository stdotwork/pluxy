//! This device's extension catalog: page-only app packages (`app.tier` `web`) installed on this
//! computer, apart from the packages of the core the app is connected to
//! (`../app-host/README.md`). It reads release archives from a local file the person chose in
//! the native file chooser, or from a registry named in `device-registries.json`, checks them
//! before anything is kept, copies each release into an immutable store folder named by its
//! archive's SHA-256, and serves a page's files only while each still has the digest it had at
//! install.
//!
//! A package from a source that is not curated is Unverified, and one claiming a reserved
//! publisher (`pluxy`, or a publisher a trust root names) is refused. A native tool (`app.tier`
//! `native`) is admitted only from a curated registry with a first-party signature, and runs
//! only after the person enabled it in a native confirmation (`device_admission.rs`). No package
//! gets resources or settings here. The previous release stays as the last good one, so a
//! failed candidate keeps the active release and the person can roll back. With `--safe-mode`,
//! no device page is listed, opened or served and no native tool is admitted; the catalog can
//! still be managed.

#[cfg(target_os = "linux")]
pub use admission::main_window;
pub use admission::{
    admitted_unlocked, native_candidate_unlocked, Admission, Admitted, OperationDecl, Roots,
    Signed, StateDecl,
};
pub use imports::{Consent, ConsentTarget, ExportDecl, Grant, ImportDecl, Refused, LAUNCH_FIELDS};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
pub use swap::Swap;

/// The scheme that serves device pages' files to their page webviews.
pub const ASSET_SCHEME: &str = "pluxy-app";
/// The app host contract major version this app speaks.
pub const HOST_CONTRACT: i64 = 0;
/// Publishers only a curated source may install, besides those trust roots name.
const RESERVED: &[&str] = &["pluxy"];
/// The protocol's archive limits (protocol README, release archives).
const MAX_UNPACKED: u64 = 512 << 20;
const MAX_ENTRIES: usize = 10_000;
/// Bytes of an archive read at most: the same as its unpacked limit.
const MAX_ARCHIVE: u64 = 512 << 20;
const MAX_INDEX: u64 = 8 << 20;
const MAX_MANIFEST: u64 = 1 << 20;
/// The extension pages contract's ui folder limits.
const MAX_UI_FILES: usize = 1000;
const MAX_UI_BYTES: u64 = 32 << 20;
const MAX_PAGES: usize = 20;
const MANIFEST: &str = "pluxy-extension.json";
/// The start of the name a catalog that could not be read is moved aside to.
const ASIDE: &str = "catalog.json.corrupt-";

/// Where a release came from. A package keeps one source; a release from another is refused.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Source {
    /// `local` (a file the person chose) or `registry`.
    pub kind: String,
    /// The name of the file the active release came from, or the registry's name in
    /// `device-registries.json`.
    pub name: String,
    /// The registry's index, local path or https URL.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub index: Option<String>,
}

impl Source {
    /// Whether a release from other comes from the same source. Every local file is one
    /// source, files the person chose on this device: the file name only labels the release,
    /// so `notes-2.0.0.tar.gz` updates a package installed from `notes-1.0.0.tar.gz`. A
    /// registry is the same source when its name and index are.
    fn same(&self, other: &Source) -> bool {
        self.kind == other.kind
            && (self.kind == "local" || (self.name == other.name && self.index == other.index))
    }
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Page {
    pub id: String,
    pub title: String,
    pub short: String,
    pub path: String,
}

/// One installed release, as the store and catalog keep it.
#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Release {
    pub version: String,
    pub sha256: String,
    pub size: u64,
    pub installed_at: u64,
    pub manifest_version: i64,
    pub app_version: i64,
    pub host_versions: Vec<i64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub ui_version: Option<i64>,
    #[serde(default)]
    pub ui_dir: String,
    #[serde(default)]
    pub pages: Vec<Page>,
    /// SHA-256 of each file under ui_dir, by slash-separated path: only these are served.
    #[serde(default)]
    pub files: BTreeMap<String, String>,
    /// Resource IDs the package asks for. None is granted in this build.
    #[serde(default)]
    pub resources: Vec<String>,
    /// Whether the package declares settings. They are not served in this build.
    #[serde(default)]
    pub settings: bool,
    /// `web` or `native`.
    #[serde(default = "web")]
    pub tier: String,
    /// The curated signature it was installed with, if any.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub signed: Option<Signed>,
    /// Native: the program for this computer, by its path in the package.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub program: Option<String>,
    /// Native: the operations it declares.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub operations: Vec<OperationDecl>,
    /// Native: the state versions it writes and reads.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub state: Option<StateDecl>,
    /// Native: SHA-256 of every file in the package, by slash-separated path, checked again
    /// before it is admitted.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub tree: BTreeMap<String, String>,
    /// The other packages' operations it may call through the host.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub imports: Vec<ImportDecl>,
    /// Native: the operations other packages may import, and their contract.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub exports: Option<ExportDecl>,
}

fn web() -> String {
    "web".into()
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Package {
    pub enabled: bool,
    pub source: Source,
    pub active: Release,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_good: Option<Release>,
    /// A native tool's confirmed admission, bound to its active release and signer.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub admission: Option<Admission>,
    /// A curated web package's confirmed native imports, bound to its active release and to
    /// each target's release.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub consent: Option<Consent>,
}

/// The catalog format with a native tool in it. A build before native tools reads only version
/// 0, and would read a native release as a page package; it refuses (and moves aside) this one.
/// A catalog with no native release stays version 0.
const NATIVE_CATALOG: u32 = 1;

#[derive(Default, Serialize, Deserialize)]
struct Saved {
    version: u32,
    packages: BTreeMap<String, Package>,
}

/// Where the catalog keeps its files.
#[derive(Clone, Debug)]
pub struct Dirs {
    /// `<data>/device-packages`: `catalog.json`, `store/<sha256>/` and `staging/`.
    pub root: PathBuf,
    /// `<config>/device-registries.json`.
    pub registries: PathBuf,
}

impl Dirs {
    fn store(&self) -> PathBuf {
        self.root.join("store")
    }
    fn staging(&self) -> PathBuf {
        self.root.join("staging")
    }
    fn catalog(&self) -> PathBuf {
        self.root.join("catalog.json")
    }
}

pub struct Catalog {
    pub dirs: Dirs,
    pub safe_mode: bool,
    packages: BTreeMap<String, Package>,
    /// Why the saved catalog could not be read at start, if it could not.
    pub warning: Option<String>,
    /// A saved catalog that could not be read is kept aside: until it is removed, no store
    /// folder is deleted, so the releases it names can still be recovered.
    keep_store: bool,
    /// Why nothing can be changed, when a catalog that could not be read could not be moved
    /// aside either: saving would replace it.
    locked: Option<String>,
    /// What went wrong after a change was made, since the last `take_cleanup`: folders that
    /// could not be deleted, a catalog that could not be flushed to disk.
    cleanup: Vec<String>,
    /// Store folders of releases being swapped in (`device_swap.rs`), by package ID, kept until
    /// the swap ends: one update of a package at a time.
    holding: BTreeMap<String, String>,
}

/// A page as the window lists it.
#[derive(Clone, Debug, Serialize)]
pub struct PageView {
    pub id: String,
    pub title: String,
    pub short: String,
    pub url: String,
}

#[derive(Clone, Debug, Serialize)]
pub struct ReleaseView {
    pub version: String,
    pub sha256: String,
    pub size: u64,
    pub installed_at: u64,
    pub manifest_version: i64,
    pub app_version: i64,
    pub host_versions: Vec<i64>,
    pub ui_version: Option<i64>,
    pub pages: Vec<PageView>,
    pub resources: Vec<String>,
    pub settings: bool,
    pub tier: String,
    /// Native: the declared operations, by ID and description.
    pub operations: Vec<(String, String)>,
    /// The other packages' operations it may call through the host.
    pub imports: Vec<ImportDecl>,
    /// Native: the operations other packages may import.
    pub exports: Option<ExportDecl>,
    /// The signer of a curated release: publisher and key ID.
    pub signer: Option<String>,
}

#[derive(Clone, Debug, Serialize)]
pub struct PackageView {
    pub id: String,
    pub enabled: bool,
    /// `curated` for a release signed by a curated root, otherwise `unverified`.
    pub trust: &'static str,
    pub tier: String,
    /// A native tool: whether the person confirmed it for its active release.
    pub authorized: bool,
    /// A web package: whether the person allowed its native imports for its active release.
    pub imports_allowed: bool,
    pub source: Source,
    pub active: ReleaseView,
    pub last_good: Option<ReleaseView>,
}

#[derive(Clone, Debug, Serialize)]
pub struct View {
    pub safe_mode: bool,
    pub packages: Vec<PackageView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub warning: Option<String>,
}

/// The page webview address of file in package id's release digest, each part of file
/// percent-encoded (`asset_request` decodes it).
pub fn asset_url(id: &str, digest: &str, file: &str) -> String {
    let mut u = url::Url::parse(&asset_base()).expect("the asset scheme's root");
    u.path_segments_mut()
        .expect("the asset scheme's root has a path")
        .pop_if_empty()
        .extend(id.split('/'))
        .push(digest)
        .extend(file.split('/'));
    u.into()
}

/// One percent-encoded path part, decoded: None when it is not valid UTF-8 after decoding,
/// or holds a separator or NUL.
fn decode_part(s: &str) -> Option<String> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' {
            let h = s.get(i + 1..i + 3)?;
            if !h.bytes().all(|c| c.is_ascii_hexdigit()) {
                return None;
            }
            out.push(u8::from_str_radix(h, 16).ok()?);
            i += 3;
        } else {
            out.push(b[i]);
            i += 1;
        }
    }
    let s = String::from_utf8(out).ok()?;
    (!s.contains(['/', '\\', '\0'])).then_some(s)
}

/// What a request on the asset scheme from a page webview bound to release folder (an
/// `asset_url` with no file) asks for: the package ID, the release digest and the file under
/// its ui folder, decoded. None when the request is outside folder.
pub fn asset_request(folder: &str, request_path: &str) -> Option<(String, String, String)> {
    let folder = url::Url::parse(folder).ok()?.path().to_string();
    let file = request_path.strip_prefix(&folder)?;
    let mut parts = folder.trim_matches('/').splitn(3, '/');
    let id = format!("{}/{}", parts.next()?, parts.next()?);
    let digest = parts.next()?.to_string();
    let file = file
        .split('/')
        .map(decode_part)
        .collect::<Option<Vec<_>>>()?
        .join("/");
    Some((id, digest, file))
}

/// The asset scheme's root, as page webviews address it.
pub fn asset_base() -> String {
    #[cfg(any(windows, target_os = "android"))]
    return format!("http://{ASSET_SCHEME}.localhost/");
    #[cfg(not(any(windows, target_os = "android")))]
    format!("{ASSET_SCHEME}://localhost/")
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn release_view(id: &str, r: &Release) -> ReleaseView {
    ReleaseView {
        version: r.version.clone(),
        sha256: r.sha256.clone(),
        size: r.size,
        installed_at: r.installed_at,
        manifest_version: r.manifest_version,
        app_version: r.app_version,
        host_versions: r.host_versions.clone(),
        ui_version: r.ui_version,
        pages: r
            .pages
            .iter()
            .map(|p| PageView {
                id: p.id.clone(),
                title: p.title.clone(),
                short: p.short.clone(),
                url: asset_url(id, &r.sha256, &p.path),
            })
            .collect(),
        resources: r.resources.clone(),
        settings: r.settings,
        tier: r.tier.clone(),
        operations: r
            .operations
            .iter()
            .map(|o| (o.id.clone(), o.description.clone()))
            .collect(),
        signer: r
            .signed
            .as_ref()
            .map(|s| format!("{} key {}", s.publisher, s.key_id)),
        imports: r.imports.clone(),
        exports: r.exports.clone(),
    }
}

#[cfg(unix)]
fn private_dir(p: &Path) -> Result<(), String> {
    use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(p)
        .map_err(|e| format!("{}: {e}", p.display()))?;
    fs::set_permissions(p, fs::Permissions::from_mode(0o700))
        .map_err(|e| format!("{}: {e}", p.display()))
}
#[cfg(not(unix))]
fn private_dir(p: &Path) -> Result<(), String> {
    fs::create_dir_all(p).map_err(|e| format!("{}: {e}", p.display()))
}

/// Removes a store or staging folder, whose files and folders are read-only.
fn remove_tree(p: &Path) -> Result<(), String> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fn writable(p: &Path) {
            if let Ok(m) = fs::symlink_metadata(p) {
                if m.is_dir() {
                    let _ = fs::set_permissions(p, fs::Permissions::from_mode(0o700));
                    if let Ok(entries) = fs::read_dir(p) {
                        for e in entries.flatten() {
                            writable(&e.path());
                        }
                    }
                }
            }
        }
        writable(p);
    }
    match fs::remove_dir_all(p) {
        Err(e) if e.kind() != std::io::ErrorKind::NotFound => Err(format!("{}: {e}", p.display())),
        _ => Ok(()),
    }
}

/// Makes the folders under dir (and dir itself, when root is set) read-only and flushes them
/// to disk, deepest first. The files in it were written read-only and flushed already.
fn freeze(dir: &Path, root: bool) -> Result<(), String> {
    for e in fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
        let e = e.map_err(|e| format!("{}: {e}", dir.display()))?;
        if e.file_type().map_err(|e| e.to_string())?.is_dir() {
            freeze(&e.path(), true)?;
        }
    }
    #[cfg(unix)]
    if root {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(dir, fs::Permissions::from_mode(0o500))
            .map_err(|e| format!("{}: {e}", dir.display()))?;
    }
    #[cfg(not(unix))]
    let _ = root;
    sync_dir(dir)
}

/// Flushes a folder's entries to disk, so a file renamed into it stays after a power loss.
fn sync_dir(dir: &Path) -> Result<(), String> {
    #[cfg(unix)]
    fs::File::open(dir)
        .and_then(|f| f.sync_all())
        .map_err(|e| format!("{}: {e}", dir.display()))?;
    #[cfg(not(unix))]
    let _ = dir;
    Ok(())
}

/// Writes bytes to path through a temporary file, readable only by this user. Err: path is as
/// it was. Ok(Some): the new file is in place, but its folder could not be flushed to disk.
fn write_private(path: &Path, bytes: &[u8]) -> Result<Option<String>, String> {
    let tmp = path.with_extension(format!("tmp-{}", std::process::id()));
    let mut o = fs::OpenOptions::new();
    o.write(true).create(true).truncate(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::mode(&mut o, 0o600);
    let mut f = o
        .open(&tmp)
        .map_err(|e| format!("{}: {e}", tmp.display()))?;
    f.write_all(bytes)
        .and_then(|_| f.sync_all())
        .map_err(|e| format!("{}: {e}", tmp.display()))?;
    drop(f);
    fs::rename(&tmp, path).map_err(|e| {
        let _ = fs::remove_file(&tmp);
        format!("{}: {e}", path.display())
    })?;
    Ok(sync_dir(path.parent().unwrap_or(Path::new(".")))
        .err()
        .map(|e| format!("{} was saved, but not flushed to disk: {e}", path.display())))
}

impl Catalog {
    /// Opens the catalog in dirs. Staging leftovers and store folders no release names go.
    pub fn open(dirs: Dirs, safe_mode: bool, roots: &dyn Roots) -> Catalog {
        let mut warning = None;
        let mut locked = None;
        let read = match fs::read(dirs.catalog()) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(BTreeMap::new()),
            Err(e) => Err(e.to_string()),
            Ok(b) => match serde_json::from_slice::<Saved>(&b) {
                Ok(s) if s.version == 0 || s.version == NATIVE_CATALOG => Ok(s.packages),
                Ok(s) => Err(format!("version {} is not supported", s.version)),
                Err(e) => Err(e.to_string()),
            },
        };
        let failed = read.is_err();
        let packages = read.unwrap_or_else(|why| {
            let aside = dirs.root.join(format!("{ASIDE}{}", now()));
            warning = Some(match fs::rename(dirs.catalog(), &aside) {
                Ok(()) => format!(
                    "this device's extension catalog could not be read ({why}); it was moved to {}",
                    aside.display()
                ),
                Err(e) => {
                    let w = format!(
                        "this device's extension catalog could not be read ({why}) or moved aside ({e}); nothing on this device can be changed until {} is fixed or removed",
                        dirs.catalog().display()
                    );
                    locked = Some(w.clone());
                    w
                }
            });
            BTreeMap::new()
        });
        // A catalog moved aside, now or before: its releases stay until it is removed.
        // A folder that cannot be listed may hold one: then nothing is deleted either.
        let aside = match fs::read_dir(&dirs.root) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e.to_string()),
            Ok(entries) => entries
                .map(|e| e.map(|e| e.path()))
                .find(|e| {
                    e.as_ref().map_or(true, |p| {
                        p.file_name()
                            .is_some_and(|n| n.to_string_lossy().starts_with(ASIDE))
                    })
                })
                .transpose()
                .map_err(|e| e.to_string()),
        };
        let keep_store = failed || !matches!(aside, Ok(None));
        if warning.is_none() {
            match &aside {
                Ok(Some(a)) => warning = Some(format!(
                    "a damaged catalog of this device's extensions is kept at {}; the releases it may name are kept until it is removed",
                    a.display()
                )),
                Err(e) => warning = Some(format!(
                    "{} could not be listed ({e}); no release is deleted until it can be",
                    dirs.root.display()
                )),
                Ok(None) => {}
            }
        }
        let mut c = Catalog {
            dirs,
            safe_mode,
            packages,
            warning,
            keep_store,
            holding: BTreeMap::new(),
            locked,
            cleanup: vec![],
        };
        // A root this build has may reserve a publisher a package was installed under before:
        // such a package stays disabled, so nothing of it is served, until it is removed.
        let held: Vec<String> = c
            .packages
            .iter_mut()
            .filter_map(|(id, p)| {
                let why = held(id, p, roots).filter(|_| p.enabled)?;
                p.enabled = false;
                Some(why)
            })
            .collect();
        if !held.is_empty() {
            let held = held.join("; ");
            c.warning = Some(match c.warning.take() {
                Some(w) => format!("{w}; {held}"),
                None => held,
            });
        }
        if let Err(e) = remove_tree(&c.dirs.staging()) {
            c.cleanup.push(format!("could not delete {e}"));
        }
        c.collect();
        if let Some(left) = c.take_cleanup() {
            c.warning = Some(match c.warning.take() {
                Some(w) => format!("{w}; {left}"),
                None => left,
            });
        }
        c
    }

    /// What went wrong after a change was made, since the last call, if anything: the change
    /// itself stands.
    pub fn take_cleanup(&mut self) -> Option<String> {
        (!self.cleanup.is_empty()).then(|| std::mem::take(&mut self.cleanup).join("; "))
    }

    pub fn view(&self, roots: &dyn Roots) -> View {
        View {
            safe_mode: self.safe_mode,
            warning: self.warning.clone(),
            packages: self
                .packages
                .iter()
                .map(|(id, p)| PackageView {
                    id: id.clone(),
                    enabled: p.enabled,
                    // Against the roots this app has now, not the ones it was installed with.
                    trust: if p.curated_signature(id, roots).is_ok() {
                        "curated"
                    } else {
                        "unverified"
                    },
                    tier: p.active.tier.clone(),
                    authorized: p.authorized(),
                    imports_allowed: self.imports_allowed(id, roots),
                    source: p.source.clone(),
                    active: release_view(id, &p.active),
                    last_good: p.last_good.as_ref().map(|r| release_view(id, r)),
                })
                .collect(),
        }
    }

    pub fn get(&self, id: &str) -> Option<&Package> {
        self.packages.get(id)
    }

    /// Saves the catalog. Err: nothing was saved, and the caller restores what it changed. A
    /// failure once the new catalog is in place does not undo the change: it is kept for
    /// `take_cleanup`.
    fn save(&mut self) -> Result<(), String> {
        let flushed = self.write()?;
        self.cleanup.extend(flushed);
        Ok(())
    }

    fn write(&self) -> Result<Option<String>, String> {
        if let Some(why) = &self.locked {
            return Err(why.clone());
        }
        private_dir(&self.dirs.root)?;
        let native = self.packages.values().any(|p| {
            std::iter::once(&p.active)
                .chain(p.last_good.as_ref())
                .any(|r| r.tier == "native")
        });
        let saved = Saved {
            version: if native { NATIVE_CATALOG } else { 0 },
            packages: self.packages.clone(),
        };
        let bytes = serde_json::to_vec_pretty(&saved).map_err(|e| e.to_string())?;
        write_private(&self.dirs.catalog(), &bytes)
    }

    /// Removes store folders no active or last good release names, unless a catalog that could
    /// not be read is kept aside.
    fn collect(&mut self) {
        if self.keep_store {
            return;
        }
        let keep: BTreeSet<String> = self
            .packages
            .values()
            .flat_map(|p| std::iter::once(&p.active).chain(p.last_good.as_ref()))
            .map(|r| r.sha256.clone())
            .chain(self.holding.values().cloned())
            .collect();
        let store = self.dirs.store();
        let entries = match fs::read_dir(&store) {
            Ok(entries) => entries,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return,
            Err(e) => {
                let e = format!("could not list {}: {e}", store.display());
                return self.cleanup.push(e);
            }
        };
        for e in entries {
            let e = match e {
                Ok(e) => e,
                Err(e) => {
                    self.cleanup
                        .push(format!("could not list {}: {e}", store.display()));
                    continue;
                }
            };
            if !keep.contains(e.file_name().to_string_lossy().as_ref()) {
                if let Err(e) = remove_tree(&e.path()) {
                    self.cleanup.push(format!("could not delete {e}"));
                }
            }
        }
    }

    /// Makes a checked candidate the package's active release, keeping the one it replaces as
    /// the last good release. A candidate that cannot be committed leaves the catalog as it was.
    pub fn commit(&mut self, c: Candidate) -> Result<(), String> {
        let result = self.commit_inner(&c);
        // The staged folder is either in the store now or no longer needed.
        if let Err(e) = remove_tree(&c.staged) {
            self.cleanup.push(format!("could not delete {e}"));
        }
        if result.is_err() {
            self.collect();
        }
        result
    }

    fn commit_inner(&mut self, c: &Candidate) -> Result<(), String> {
        let id = &c.id;
        let previous = self.packages.get(id).cloned();
        if let Some(p) = &previous {
            if !p.source.same(&c.source) {
                return Err(format!(
                    "{id} is installed from {} {}; remove it first to install it from {} {}",
                    p.source.kind, p.source.name, c.source.kind, c.source.name
                ));
            }
            if p.active.sha256 == c.release.sha256 {
                return Err(format!("{id} {} is already installed", c.release.version));
            }
        }
        if let Some(p) = &previous {
            swap::state_readable(id, &p.active, &c.release)?;
        }
        self.place(c)?;
        // A native tool's admission names one release: any new release waits, disabled, for a
        // fresh native confirmation.
        let native = c.release.tier == "native";
        let package = match previous.clone() {
            Some(p) => Package {
                enabled: p.enabled && !native && p.active.tier != "native",
                // The same source; a local release's file name labels the new release.
                source: c.source.clone(),
                last_good: Some(p.active),
                active: c.release.clone(),
                admission: None,
                consent: None,
            },
            None => Package {
                enabled: !native,
                source: c.source.clone(),
                active: c.release.clone(),
                last_good: None,
                admission: None,
                consent: None,
            },
        };
        self.packages.insert(id.clone(), package);
        if let Err(e) = self.save() {
            match previous {
                Some(p) => self.packages.insert(id.clone(), p),
                None => self.packages.remove(id),
            };
            return Err(e);
        }
        self.collect();
        Ok(())
    }

    /// Puts candidate c's checked folder in the store, read-only.
    fn place(&mut self, c: &Candidate) -> Result<(), String> {
        let store = self.dirs.store();
        private_dir(&store)?;
        let dest = store.join(&c.release.sha256);
        // A folder of this digest already in the store (the last good release, installed
        // again) is used only while its files are unchanged; otherwise the checked copy
        // replaces it.
        if !dest.exists()
            || verify(&dest, &c.release).is_err()
            || (c.release.tier == "native"
                && admission::verify_tree(&dest, &c.release.tree).is_err())
        {
            if dest.exists() {
                remove_tree(&dest)?;
            }
            fs::rename(&c.staged, &dest).map_err(|e| format!("{}: {e}", dest.display()))?;
            sync_dir(&store)?;
        }
        freeze(&dest, true)
    }

    /// Enables or disables package id. A native tool is enabled only by `admit`, after a
    /// native confirmation; disabling it ends its admission.
    pub fn set_enabled(
        &mut self,
        id: &str,
        enabled: bool,
        roots: &dyn Roots,
    ) -> Result<(), String> {
        let p = self
            .packages
            .get_mut(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if enabled && p.active.tier == "native" {
            return Err(format!(
                "{id} is a native tool: enabling it needs a native confirmation"
            ));
        }
        if let Some(why) = held(id, p, roots).filter(|_| enabled) {
            return Err(why);
        }
        let was = (p.enabled, p.admission.take(), p.consent.take());
        p.enabled = enabled;
        if let Err(e) = self.save() {
            if let Some(p) = self.packages.get_mut(id) {
                (p.enabled, p.admission, p.consent) = was;
            }
            return Err(e);
        }
        Ok(())
    }

    pub fn remove(&mut self, id: &str) -> Result<(), String> {
        let p = self
            .packages
            .remove(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if let Err(e) = self.save() {
            self.packages.insert(id.into(), p);
            return Err(e);
        }
        self.collect();
        Ok(())
    }

    /// Makes the last good release active again, after checking its files are unchanged.
    pub fn rollback(&mut self, id: &str) -> Result<(), String> {
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        let last = p
            .last_good
            .clone()
            .ok_or_else(|| format!("{id} has no earlier release to roll back to"))?;
        verify(&self.dirs.store().join(&last.sha256), &last)?;
        if last.tier == "native" {
            admission::verify_tree(&self.dirs.store().join(&last.sha256), &last.tree)?;
        }
        swap::state_readable(id, &p.active, &last)?;
        let p = self.packages.get_mut(id).unwrap();
        let active = std::mem::replace(&mut p.active, last);
        p.last_good = Some(active);
        // A native tool's admission named the release that is no longer active.
        let was = (p.enabled, p.admission.take(), p.consent.take());
        if p.active.tier == "native" || p.last_good.as_ref().is_some_and(|r| r.tier == "native") {
            p.enabled = false;
        }
        if let Err(e) = self.save() {
            let p = self.packages.get_mut(id).unwrap();
            let last = std::mem::replace(&mut p.active, p.last_good.take().unwrap());
            p.last_good = Some(last);
            (p.enabled, p.admission, p.consent) = was;
            return Err(e);
        }
        Ok(())
    }

    /// The address of package id's page, while its pages may be shown.
    pub fn page(&self, id: &str, page: &str) -> Result<(String, String, String), String> {
        if self.safe_mode {
            return Err("the app runs in safe mode: pages from this device are not loaded".into());
        }
        let p = self
            .packages
            .get(id)
            .ok_or_else(|| format!("{id} is not installed on this device"))?;
        if !p.enabled {
            return Err(format!("{id} is disabled"));
        }
        let pg = p
            .active
            .pages
            .iter()
            .find(|x| x.id == page)
            .ok_or_else(|| format!("{id} has no page {page}"))?;
        Ok((
            asset_url(id, &p.active.sha256, &pg.path),
            asset_url(id, &p.active.sha256, ""),
            pg.title.clone(),
        ))
    }

    /// Where a file of package id's active release is, by its path under the release's ui
    /// folder, and the digest it had at install. The file is read with `Asset::read`, without
    /// the catalog.
    pub fn asset_file(&self, id: &str, digest: &str, file: &str) -> Option<Asset> {
        if self.safe_mode {
            return None;
        }
        let p = self.packages.get(id)?;
        if !p.enabled || p.active.sha256 != digest {
            return None;
        }
        let sha256 = p.active.files.get(file)?.clone();
        let kind = content_type(file)?;
        let path = self
            .dirs
            .store()
            .join(digest)
            .join(&p.active.ui_dir)
            .join(file);
        Some(Asset { path, sha256, kind })
    }

    #[cfg(test)]
    fn asset(&self, id: &str, digest: &str, file: &str) -> Option<(Vec<u8>, &'static str)> {
        let a = self.asset_file(id, digest, file)?;
        Some((a.read()?, a.kind))
    }
}

/// A file a page webview may be served: see `Catalog::asset_file`.
pub struct Asset {
    path: PathBuf,
    sha256: String,
    pub kind: &'static str,
}

impl Asset {
    /// The file's bytes, while it still has the digest it had at install.
    pub fn read(&self) -> Option<Vec<u8>> {
        let body = read_bounded(&self.path, MAX_UI_BYTES).ok()?;
        (hex(&Sha256::digest(&body)) == self.sha256).then_some(body)
    }
}

/// The content type a page file is served with; other files are not served.
fn content_type(file: &str) -> Option<&'static str> {
    let ext = file.rsplit_once('.')?.1.to_ascii_lowercase();
    Some(match ext.as_str() {
        "html" => "text/html; charset=utf-8",
        "js" | "mjs" => "text/javascript; charset=utf-8",
        "css" => "text/css; charset=utf-8",
        "json" => "application/json",
        "svg" => "image/svg+xml",
        "png" => "image/png",
        "jpg" | "jpeg" => "image/jpeg",
        "gif" => "image/gif",
        "webp" => "image/webp",
        "ico" => "image/x-icon",
        "woff" => "font/woff",
        "woff2" => "font/woff2",
        "txt" => "text/plain; charset=utf-8",
        _ => return None,
    })
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// Reads a regular file of at most max bytes. Opened without blocking, so a FIFO or device in
/// its place is refused rather than waited on.
fn read_bounded(path: &Path, max: u64) -> Result<Vec<u8>, String> {
    let mut o = fs::OpenOptions::new();
    o.read(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::custom_flags(&mut o, libc::O_NONBLOCK);
    let f = o
        .open(path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    read_open(f, path, max)
}

/// The SHA-256 and length of the regular file at path, of at most max bytes, read in 64 KiB
/// chunks, so a large file is never held whole.
fn hash_file(path: &Path, max: u64) -> Result<(String, u64), String> {
    let mut o = fs::OpenOptions::new();
    o.read(true);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::custom_flags(&mut o, libc::O_NONBLOCK);
    let mut f = o
        .open(path)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    if !f.metadata().is_ok_and(|m| m.is_file()) {
        return Err(format!("{} is not a regular file", path.display()));
    }
    let mut h = Sha256::new();
    let mut len = 0u64;
    let mut buf = vec![0u8; 64 * 1024];
    loop {
        let n = f
            .read(&mut buf)
            .map_err(|e| format!("{}: {e}", path.display()))?;
        if n == 0 {
            break;
        }
        len += n as u64;
        if len > max {
            return Err(format!("{} is larger than {max} bytes", path.display()));
        }
        h.update(&buf[..n]);
    }
    Ok((hex(&h.finalize()), len))
}

/// Reads the open file f (named path in errors), which must be a regular file of at most max
/// bytes.
fn read_open(f: fs::File, path: &Path, max: u64) -> Result<Vec<u8>, String> {
    if !f.metadata().is_ok_and(|m| m.is_file()) {
        return Err(format!("{} is not a regular file", path.display()));
    }
    let mut b = Vec::new();
    f.take(max + 1)
        .read_to_end(&mut b)
        .map_err(|e| format!("{}: {e}", path.display()))?;
    if b.len() as u64 > max {
        return Err(format!("{} is larger than {max} bytes", path.display()));
    }
    Ok(b)
}

/// Checks that every file of release r in dir still has its digest.
fn verify(dir: &Path, r: &Release) -> Result<(), String> {
    for (file, want) in &r.files {
        let b = read_bounded(&dir.join(&r.ui_dir).join(file), MAX_UI_BYTES)?;
        if hex(&Sha256::digest(&b)) != *want {
            return Err(format!(
                "{file} of release {} changed since it was installed",
                r.version
            ));
        }
    }
    Ok(())
}

// ---- Manifests: the common format (protocol README; app-host README) ----

#[derive(Deserialize, Default)]
#[serde(default)]
struct Manifest {
    manifest_version: i64,
    id: String,
    version: String,
    protocol: Vec<i64>,
    programs: BTreeMap<String, String>,
    ui: Option<Ui>,
    config: Option<Config>,
    app: Option<Value>,
}

#[derive(Deserialize, Default, Clone)]
#[serde(default)]
struct Ui {
    version: i64,
    dir: String,
    permissions: Vec<String>,
    pages: Vec<Page>,
}

#[derive(Deserialize, Default, Clone)]
#[serde(default)]
struct Config {
    version: i64,
    schema: Value,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct App {
    version: i64,
    tier: String,
    host_versions: Vec<i64>,
    ui: Option<Ui>,
    config: Option<Config>,
    resources: Option<Vec<Resource>>,
    programs: Option<BTreeMap<String, String>>,
    operations: Option<Vec<Operation>>,
    state: Option<State>,
    imports: Option<Vec<imports::ImportSpec>>,
    exports: Option<imports::ExportSpec>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Resource {
    id: String,
    kind: String,
    operations: Vec<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Operation {
    id: String,
    description: String,
    input_schema: Value,
    output_schema: Value,
    effect: Option<String>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct State {
    version: i64,
    readable_versions: Vec<i64>,
}

fn name_part(s: &str) -> bool {
    let b = s.as_bytes();
    !b.is_empty()
        && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit())
        && b.iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'.' || *c == b'-')
}

/// `publisher/name`: lower-case letters, digits, `.` and `-`, each part starting with a letter
/// or digit.
pub fn valid_id(id: &str) -> bool {
    id.split_once('/')
        .is_some_and(|(p, n)| name_part(p) && name_part(n))
}

fn valid_version(v: &str) -> bool {
    let b = v.as_bytes();
    (1..=64).contains(&b.len())
        && b[0].is_ascii_alphanumeric()
        && b.iter()
            .all(|c| c.is_ascii_alphanumeric() || b".+_-".contains(c))
}

fn valid_page_id(id: &str) -> bool {
    let b = id.as_bytes();
    (1..=32).contains(&b.len())
        && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit())
        && b.iter()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || *c == b'-')
}

/// A relative slash-separated path that stays inside its folder: no empty, `.` or `..` part, no
/// backslash. Stricter than Go's filepath.IsLocal, which also allows `a/./b` and `a/../b`.
fn local_path(p: &str) -> bool {
    !p.is_empty()
        && !p.contains('\\')
        && !p.contains('\0')
        && p.split('/').all(|s| !s.is_empty() && s != "." && s != "..")
}

/// A local path that also stays inside its folder on Windows: no `:`, which names a drive or an
/// alternate data stream there. Archive entries, the ui folder and local registry artifacts
/// must be one.
fn store_path(p: &str) -> bool {
    local_path(p) && !p.contains(':')
}

impl Ui {
    fn validate(&self) -> Result<(), String> {
        if self.version != 0 {
            return Err(format!(
                "ui.version {} is not supported (this app serves 0)",
                self.version
            ));
        }
        if !local_path(&self.dir) {
            return Err("ui.dir must be a folder inside the package folder".into());
        }
        for p in &self.permissions {
            if p != "config.read" && p != "config.write" {
                return Err(format!("ui.permissions: {p:?} is not a permission"));
            }
        }
        if self.pages.is_empty() || self.pages.len() > MAX_PAGES {
            return Err(format!("ui.pages must list 1 to {MAX_PAGES} pages"));
        }
        let mut seen = BTreeSet::new();
        for (i, p) in self.pages.iter().enumerate() {
            if !valid_page_id(&p.id) {
                return Err(format!(
                    "ui.pages[{i}].id {:?} is not 1 to 32 lower-case letters, digits and '-'",
                    p.id
                ));
            }
            if !seen.insert(p.id.as_str()) {
                return Err(format!("ui.pages[{i}].id {:?} is used twice", p.id));
            }
            if p.title.is_empty() || p.title.chars().count() > 60 {
                return Err(format!("ui.pages[{i}].title must be 1 to 60 characters"));
            }
            if p.short.is_empty() || p.short.chars().count() > 8 {
                return Err(format!("ui.pages[{i}].short must be 1 to 8 characters"));
            }
            if !p.path.ends_with(".html") || !local_path(&p.path) {
                return Err(format!(
                    "ui.pages[{i}].path must be an .html file inside ui.dir"
                ));
            }
        }
        Ok(())
    }
}

impl Config {
    /// shortcut: checks the schema's outline only (an object of typed fields), not every rule
    /// core's settings parser applies; device settings are not served yet, so upgrade this when
    /// they are.
    fn validate(&self) -> Result<(), String> {
        if self.version != 0 {
            return Err(format!("config.version {} is not supported", self.version));
        }
        let ok = self.schema["type"] == "object"
            && self.schema["properties"].as_object().is_some_and(|props| {
                props.values().all(|f| {
                    f["type"]
                        .as_str()
                        .is_some_and(|t| ["string", "integer", "number", "boolean"].contains(&t))
                })
            });
        if ok {
            Ok(())
        } else {
            Err(
                "config: schema must be an object of string, integer, number and boolean fields"
                    .into(),
            )
        }
    }
}

impl App {
    fn validate(&self) -> Result<(), String> {
        if self.version != 0 {
            return Err(format!("app.version {} is not supported", self.version));
        }
        if self.tier != "web" && self.tier != "native" {
            return Err("app.tier must be web or native".into());
        }
        if self.host_versions.is_empty() {
            return Err("app.host_versions must list compatible host contracts".into());
        }
        if self.host_versions.iter().any(|v| *v < 0) {
            return Err("app.host_versions must be nonnegative".into());
        }
        if let Some(ui) = &self.ui {
            ui.validate().map_err(|e| format!("app: {e}"))?;
        }
        if let Some(c) = &self.config {
            c.validate().map_err(|e| format!("app: {e}"))?;
        }
        imports::validate_imports(self.imports.as_deref())?;
        if self.tier == "web" {
            if self.programs.is_some()
                || self.operations.is_some()
                || self.state.is_some()
                || self.exports.is_some()
            {
                return Err("web app cannot declare programs, operations, exports or state".into());
            }
            let mut seen = BTreeSet::new();
            for r in self.resources.iter().flatten() {
                if !valid_page_id(&r.id)
                    || !seen.insert(r.id.as_str())
                    || (r.kind != "file" && r.kind != "subtree")
                    || r.operations.is_empty()
                    || r.operations.iter().any(|o| o.trim().is_empty())
                {
                    return Err(
                        "app.resources require unique IDs, file or subtree kind and operations"
                            .into(),
                    );
                }
            }
            return Ok(());
        }
        if self.resources.is_some() {
            return Err("native app cannot declare resources".into());
        }
        let programs = self
            .programs
            .as_ref()
            .filter(|p| !p.is_empty())
            .ok_or("native app programs must list at least one platform".to_string())?;
        for (platform, prog) in programs {
            if !local_path(prog) {
                return Err(format!(
                    "app program for {platform} must be a path inside the package folder"
                ));
            }
        }
        let ops = self
            .operations
            .as_ref()
            .filter(|o| !o.is_empty())
            .ok_or("native app operations must list at least one operation".to_string())?;
        let mut seen = BTreeSet::new();
        for op in ops {
            if !valid_page_id(&op.id)
                || !seen.insert(op.id.as_str())
                || op.description.trim().is_empty()
            {
                return Err("app.operations require unique IDs and descriptions".into());
            }
            if !op.input_schema.is_object() || !op.output_schema.is_object() {
                return Err("app.operations schemas must be JSON objects".into());
            }
            imports::validate_effect(op)?;
        }
        imports::validate_exports(self.exports.as_ref(), ops)?;
        match &self.state {
            Some(s) if s.version >= 0 && !s.readable_versions.is_empty() => {
                if s.readable_versions.iter().any(|v| *v < 0) {
                    return Err("app.state.readable_versions must be nonnegative".into());
                }
                Ok(())
            }
            _ => Err("native app state requires version and readable_versions".into()),
        }
    }
}

/// Checks the common manifest format, as core does, except that an app-only package is a valid
/// manifest here. Returns the app contribution, when there is one.
fn validate(m: &Manifest) -> Result<Option<App>, String> {
    if m.manifest_version != 0 && m.manifest_version != 1 {
        return Err(format!(
            "manifest_version {} is not supported",
            m.manifest_version
        ));
    }
    if !valid_id(&m.id) {
        return Err(format!("id {:?} is not of the form publisher/name", m.id));
    }
    if !valid_version(&m.version) {
        return Err(format!("version {:?} is not a package version", m.version));
    }
    let app = match &m.app {
        Some(v) if m.manifest_version == 1 && !v.is_null() => {
            let app: App = serde_json::from_value(v.clone()).map_err(|e| format!("app: {e}"))?;
            app.validate()?;
            Some(app)
        }
        _ => None,
    };
    let provider = !m.protocol.is_empty() || !m.programs.is_empty();
    if app.is_some() && !provider {
        return Ok(app);
    }
    if app.is_some() && m.programs.is_empty() {
        return Err("provider programs must be declared separately from app.programs".into());
    }
    if m.protocol.is_empty() {
        return Err("protocol must list at least one version".into());
    }
    for (platform, prog) in &m.programs {
        if !local_path(prog) {
            return Err(format!(
                "program for {platform} must be a path inside the package folder"
            ));
        }
    }
    if let Some(ui) = &m.ui {
        ui.validate()?;
    }
    if let Some(c) = &m.config {
        c.validate()?;
    }
    Ok(app)
}

/// Why installed package p may not be enabled, if it may not: its ID claims a reserved
/// publisher, and its active release has no curated signature that verifies against roots now.
fn held(id: &str, p: &Package, roots: &dyn Roots) -> Option<String> {
    reserved(id, roots)?;
    p.curated_signature(id, roots).is_err().then(|| {
        format!(
            "{id} is disabled: its publisher is reserved for a curated source, and its release has no curated signature this app can verify; remove it"
        )
    })
}

/// Why a package from a source that is not curated may not claim id, if it may not: its
/// publisher is `pluxy` or one a trust root names.
fn reserved(id: &str, roots: &dyn Roots) -> Option<String> {
    let publisher = id.split('/').next().unwrap_or_default();
    (RESERVED.contains(&publisher) || roots.reserved(publisher)).then(|| {
        if roots.configured() {
            format!(
                "{id}: the publisher {publisher} is reserved for pluxy's curated index, and this source is not curated"
            )
        } else {
            format!(
                "{id}: the publisher {publisher} is reserved for pluxy's curated index, and native-tier trust is not configured in this build, so no source is curated"
            )
        }
    })
}

// ---- Archives ----

/// Unpacks a release archive into dest (which must not exist): regular files and folders only,
/// no links, devices or names that leave the folder, within the protocol's limits.
fn unpack(archive: &[u8], dest: &Path) -> Result<(), String> {
    private_dir(dest)?;
    // Headers add at most a few blocks per entry; past this the stream is over the limit.
    let cap = MAX_UNPACKED + (MAX_ENTRIES as u64 + 16) * 1536;
    let gz = flate2::read::GzDecoder::new(archive).take(cap);
    let mut ar = tar::Archive::new(gz);
    let mut total = 0u64;
    let mut count = 0usize;
    for entry in ar.entries().map_err(|e| format!("archive: {e}"))? {
        let mut e = entry.map_err(|e| format!("archive: {e}"))?;
        count += 1;
        if count > MAX_ENTRIES {
            return Err(format!("archive holds more than {MAX_ENTRIES} entries"));
        }
        let raw = e.path_bytes().into_owned();
        let name = std::str::from_utf8(&raw).map_err(|_| "archive: a name is not UTF-8")?;
        let kind = e.header().entry_type();
        if kind.is_pax_global_extensions() {
            continue;
        }
        let rel = name.trim_start_matches("./").trim_end_matches('/');
        if kind.is_dir() && (rel.is_empty() || rel == ".") {
            continue;
        }
        let path = dest.join(rel);
        if !store_path(rel) || !path.starts_with(dest) {
            return Err(format!("archive: {name:?} leaves the package folder"));
        }
        if kind.is_dir() {
            private_dir(&path)?;
            continue;
        }
        if !kind.is_file() {
            return Err(format!("archive: {name:?} is not a regular file or folder"));
        }
        let size = e.size();
        total = total.saturating_add(size);
        if total > MAX_UNPACKED {
            return Err(format!("archive unpacks to more than {MAX_UNPACKED} bytes"));
        }
        if let Some(parent) = path.parent() {
            private_dir(parent)?;
        }
        let exec = e.header().mode().map(|m| m & 0o111 != 0).unwrap_or(false);
        let mut o = fs::OpenOptions::new();
        o.write(true).create_new(true);
        #[cfg(unix)]
        std::os::unix::fs::OpenOptionsExt::mode(&mut o, if exec { 0o555 } else { 0o444 });
        let mut f = o
            .open(&path)
            .map_err(|err| format!("archive: {name:?}: {err}"))?;
        let copied = std::io::copy(&mut (&mut e).take(size), &mut f)
            .map_err(|err| format!("archive: {name:?}: {err}"))?;
        if copied != size {
            return Err(format!("archive: {name:?} is cut short"));
        }
        f.sync_all()
            .map_err(|err| format!("archive: {name:?}: {err}"))?;
    }
    // The installed tree is read-only, every folder in it too, except the top one until it is
    // in the store: moving a folder into the store needs write access to it (its `..` changes).
    freeze(dest, false)
}

/// The files under the ui folder dir, by slash-separated path, with their SHA-256 digests.
fn hash_tree(dir: &Path) -> Result<BTreeMap<String, String>, String> {
    hash_files(dir, MAX_UI_FILES, MAX_UI_BYTES, "the ui folder")
}

/// The files under dir, by slash-separated path, with their SHA-256 digests: at most max_files
/// regular files of max_bytes in all, nothing else (no link, device or socket).
fn hash_files(
    dir: &Path,
    max_files: usize,
    max_bytes: u64,
    what: &str,
) -> Result<BTreeMap<String, String>, String> {
    let mut out = BTreeMap::new();
    let mut total = 0u64;
    let mut todo = vec![(dir.to_path_buf(), String::new())];
    while let Some((d, prefix)) = todo.pop() {
        for e in fs::read_dir(&d).map_err(|e| format!("{}: {e}", d.display()))? {
            let e = e.map_err(|e| e.to_string())?;
            let name = e
                .file_name()
                .into_string()
                .map_err(|_| "a file name is not UTF-8")?;
            let rel = format!("{prefix}{name}");
            let t = e.file_type().map_err(|e| e.to_string())?;
            if t.is_dir() {
                todo.push((e.path(), format!("{rel}/")));
            } else if t.is_file() {
                let (digest, len) = hash_file(&e.path(), max_bytes)?;
                total += len;
                out.insert(rel, digest);
                if out.len() > max_files || total > max_bytes {
                    return Err(format!(
                        "{what} holds more than {max_files} files or {} MiB",
                        max_bytes >> 20
                    ));
                }
            } else {
                return Err(format!("{rel} in {what} is not a regular file"));
            }
        }
    }
    Ok(out)
}

/// What an index pins about the release it offers.
#[derive(Clone, Debug)]
pub struct Expect {
    pub id: String,
    pub version: String,
    pub sha256: String,
    pub size: u64,
}

/// A checked release, staged and waiting for `Catalog::commit`.
#[derive(Debug)]
pub struct Candidate {
    pub id: String,
    pub source: Source,
    pub release: Release,
    staged: PathBuf,
}

impl Drop for Candidate {
    fn drop(&mut self) {
        // A staged folder left behind goes when the catalog next opens.
        let _ = remove_tree(&self.staged);
    }
}

/// Checks an archive with no curated signature and stages it in dirs, before the catalog is
/// touched: its pinned size and digest, if an index gave them; its entries; its manifest, which
/// must be a page-only app package this app can host, not claiming a reserved publisher.
pub fn stage(
    dirs: &Dirs,
    archive: &[u8],
    source: Source,
    expect: Option<&Expect>,
    roots: &dyn Roots,
) -> Result<Candidate, String> {
    stage_signed(dirs, archive, source, expect, None, roots)
}

/// `stage`, for an archive whose descriptor signing verified against a curated root: it may
/// claim the root's publisher, and only such an archive may be a native tool.
fn stage_signed(
    dirs: &Dirs,
    archive: &[u8],
    source: Source,
    expect: Option<&Expect>,
    signing: Option<Signing>,
    roots: &dyn Roots,
) -> Result<Candidate, String> {
    let sha256 = hex(&Sha256::digest(archive));
    if let Some(x) = expect {
        if archive.len() as u64 != x.size || sha256 != x.sha256 {
            return Err(format!(
                "{} {}: the archive is not the one the registry lists (size or SHA-256 differs)",
                x.id, x.version
            ));
        }
    }
    private_dir(&dirs.staging())?;
    let mut rnd = [0u8; 8];
    getrandom::fill(&mut rnd).map_err(|e| e.to_string())?;
    let staged = dirs.staging().join(hex(&rnd));
    let mut c = Candidate {
        id: String::new(),
        source,
        release: Release {
            version: String::new(),
            sha256: sha256.clone(),
            size: archive.len() as u64,
            installed_at: now(),
            manifest_version: 0,
            app_version: 0,
            host_versions: vec![],
            ui_version: None,
            ui_dir: String::new(),
            pages: vec![],
            files: BTreeMap::new(),
            resources: vec![],
            settings: false,
            tier: web(),
            signed: None,
            program: None,
            operations: vec![],
            state: None,
            tree: BTreeMap::new(),
            imports: vec![],
            exports: None,
        },
        staged: staged.clone(),
    };
    unpack(archive, &staged)?;
    let bytes = read_bounded(&staged.join(MANIFEST), MAX_MANIFEST)
        .map_err(|_| format!("the archive has no {MANIFEST} at its top"))?;
    let m: Manifest = serde_json::from_slice(&bytes).map_err(|e| format!("{MANIFEST}: {e}"))?;
    let app = validate(&m)?;
    match &signing {
        // The descriptor's root names this publisher; its ID, version and tier are the
        // manifest's.
        Some(s) => s.verified.check_manifest(&bytes)?,
        None => {
            if let Some(why) = reserved(&m.id, roots) {
                return Err(why);
            }
        }
    }
    if let Some(x) = expect {
        if m.id != x.id || m.version != x.version {
            return Err(format!(
                "the archive holds {} {}, not {} {} as the registry lists",
                m.id, m.version, x.id, x.version
            ));
        }
    }
    let app = app.ok_or_else(|| {
        format!(
            "{} {} has no app contribution: install it in core instead",
            m.id, m.version
        )
    })?;
    if app.tier == "native" && signing.is_none() {
        return Err(format!(
            "{} {} is a native tool; native tools are admitted only from a curated source with a first-party signature",
            m.id, m.version
        ));
    }
    if !app.host_versions.contains(&HOST_CONTRACT) {
        return Err(format!(
            "{} {} needs app host contract {:?}; this app speaks host contract {HOST_CONTRACT}",
            m.id, m.version, app.host_versions
        ));
    }
    let r = &mut c.release;
    r.version = m.version.clone();
    r.manifest_version = m.manifest_version;
    r.app_version = app.version;
    r.host_versions = app.host_versions.clone();
    r.resources = app
        .resources
        .iter()
        .flatten()
        .map(|x| x.id.clone())
        .collect();
    r.settings = app.config.is_some();
    r.imports = imports::import_decls(&m.id, &app)?;
    match signing {
        Some(s) if app.tier == "native" => {
            admission::native_release(r, &app, &staged, s.signed)?;
        }
        // A curated web package: its signature makes it curated, which native imports need.
        Some(s) => r.signed = Some(s.signed),
        None => {}
    }
    if let Some(ui) = &app.ui {
        if !store_path(&ui.dir) {
            return Err(format!(
                "ui.dir {:?} must be a folder inside the package",
                ui.dir
            ));
        }
        let dir = staged.join(&ui.dir);
        if !fs::symlink_metadata(&dir).is_ok_and(|m| m.is_dir()) {
            return Err(format!("ui.dir {} is not a folder in the archive", ui.dir));
        }
        r.files = hash_tree(&dir)?;
        for p in &ui.pages {
            if !r.files.contains_key(&p.path) {
                return Err(format!("page {}: {} is not in the ui folder", p.id, p.path));
            }
        }
        r.ui_version = Some(ui.version);
        r.ui_dir = ui.dir.clone();
        r.pages = ui.pages.clone();
    }
    c.id = m.id;
    Ok(c)
}

/// Reads a local archive the person chose, within the archive limit.
pub fn read_archive(path: &Path) -> Result<Vec<u8>, String> {
    read_bounded(path, MAX_ARCHIVE)
}

// ---- Registries ----

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Registry {
    pub name: String,
    /// The index: an absolute local path or an https URL.
    pub url: String,
}

#[derive(Deserialize)]
struct RegistriesFile {
    #[serde(default)]
    version: u32,
    #[serde(default)]
    registries: Vec<Registry>,
}

/// The registries named in the registries file; none when it does not exist.
pub fn registries(dirs: &Dirs) -> Result<Vec<Registry>, String> {
    let b = match fs::read(&dirs.registries) {
        Ok(b) => b,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(vec![]),
        Err(e) => return Err(format!("{}: {e}", dirs.registries.display())),
    };
    let f: RegistriesFile =
        serde_json::from_slice(&b).map_err(|e| format!("{}: {e}", dirs.registries.display()))?;
    if f.version != 0 {
        return Err(format!(
            "{}: version {} is not supported",
            dirs.registries.display(),
            f.version
        ));
    }
    Ok(f.registries)
}

#[derive(Deserialize)]
struct Index {
    registry_version: u32,
    #[serde(default)]
    packages: BTreeMap<String, IndexPackage>,
}

#[derive(Deserialize)]
struct IndexPackage {
    #[serde(default)]
    releases: Vec<IndexRelease>,
}

#[derive(Deserialize)]
struct IndexRelease {
    version: String,
    #[serde(default)]
    artifacts: BTreeMap<String, Artifact>,
}

#[derive(Deserialize, Clone)]
struct Artifact {
    url: String,
    sha256: String,
    size: u64,
    /// A publisher signature (app host README, signed descriptor bytes): the key's ID, its
    /// Ed25519 public key and the detached signature in lower-case hex, and the exact
    /// descriptor text signed. All four or none.
    #[serde(default)]
    key_id: Option<String>,
    #[serde(default)]
    public_key: Option<String>,
    #[serde(default)]
    descriptor: Option<String>,
    #[serde(default)]
    signature: Option<String>,
}

/// Go's platform name for this computer, as registry artifacts are keyed.
pub fn platform() -> String {
    go_platform(std::env::consts::OS, std::env::consts::ARCH)
}

/// Go's name for Rust's operating system and architecture names.
fn go_platform(os: &str, arch: &str) -> String {
    let os = if os == "macos" { "darwin" } else { os };
    let arch = match arch {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        "x86" => "386",
        a => a,
    };
    format!("{os}/{arch}")
}

fn agent() -> ureq::Agent {
    ureq::Agent::config_builder()
        .proxy(ureq::Proxy::try_from_env())
        .http_status_as_error(false)
        .max_redirects(0)
        .https_only(true)
        .timeout_connect(Some(Duration::from_secs(10)))
        .timeout_global(Some(Duration::from_secs(300)))
        .user_agent("pluxy-app")
        .build()
        .into()
}

fn https_get(url: &url::Url, max: u64) -> Result<Vec<u8>, String> {
    if url.scheme() != "https" {
        return Err(format!("{url}: only https is used"));
    }
    let mut resp = agent()
        .get(url.as_str())
        .call()
        .map_err(|e| format!("{url}: {e}"))?;
    if resp.status() != 200 {
        return Err(format!("{url}: the server answered {}", resp.status()));
    }
    let b = resp
        .body_mut()
        .with_config()
        .limit(max + 1)
        .read_to_vec()
        .map_err(|e| format!("{url}: {e}"))?;
    if b.len() as u64 > max {
        return Err(format!("{url} is larger than {max} bytes"));
    }
    Ok(b)
}

enum Base {
    Local(PathBuf),
    Https(url::Url),
}

fn read_index(r: &Registry) -> Result<(Index, Base), String> {
    let (bytes, base) = if r.url.starts_with("https://") {
        let u = url::Url::parse(&r.url).map_err(|e| format!("{}: {e}", r.url))?;
        (https_get(&u, MAX_INDEX)?, Base::Https(u))
    } else {
        let p = PathBuf::from(&r.url);
        if !p.is_absolute() {
            return Err(format!(
                "registry {}: its index must be an https URL or an absolute path",
                r.name
            ));
        }
        let dir = p.parent().map(Path::to_path_buf).unwrap_or_default();
        (read_bounded(&p, MAX_INDEX)?, Base::Local(dir))
    };
    let index: Index = serde_json::from_slice(&bytes)
        .map_err(|e| format!("registry {}: its index: {e}", r.name))?;
    if index.registry_version != 0 {
        return Err(format!(
            "registry {}: index version {} is not supported",
            r.name, index.registry_version
        ));
    }
    Ok((index, base))
}

fn fetch_artifact(base: &Base, a: &Artifact) -> Result<Vec<u8>, String> {
    if a.size > MAX_ARCHIVE {
        return Err(format!("{} is larger than {MAX_ARCHIVE} bytes", a.url));
    }
    match base {
        Base::Https(index) => {
            let u = index.join(&a.url).map_err(|e| format!("{}: {e}", a.url))?;
            https_get(&u, a.size)
        }
        Base::Local(dir) => {
            if a.url.starts_with("https://") {
                let u = url::Url::parse(&a.url).map_err(|e| format!("{}: {e}", a.url))?;
                return https_get(&u, a.size);
            }
            let outside = || format!("{}: not a file inside the index's folder", a.url);
            if !store_path(&a.url) {
                return Err(outside());
            }
            #[cfg(unix)]
            {
                let f = open_beneath(dir, &a.url).map_err(|e| format!("{}: {e}", outside()))?;
                read_open(f, &dir.join(&a.url), a.size)
            }
            // shortcut: checks the path once, then opens it again, so a folder swapped for a
            // link in between is followed; open beneath the folder there too when other
            // platforms are supported.
            #[cfg(not(unix))]
            {
                let inside = fs::canonicalize(dir)
                    .ok()
                    .zip(fs::canonicalize(dir.join(&a.url)).ok());
                match inside {
                    Some((d, p)) if p.starts_with(&d) => read_bounded(&p, a.size),
                    _ => Err(outside()),
                }
            }
        }
    }
}

/// Opens rel, a `store_path`, under dir one part at a time, following no link at any step, so
/// it cannot leave dir even while its folders change. dir itself may be reached through links.
#[cfg(unix)]
fn open_beneath(dir: &Path, rel: &str) -> Result<fs::File, String> {
    use std::os::fd::{AsRawFd, FromRawFd};
    let mut cur = fs::File::open(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
    let parts: Vec<&str> = rel.split('/').collect();
    for (i, part) in parts.iter().enumerate() {
        let name = std::ffi::CString::new(*part).map_err(|e| e.to_string())?;
        // The file itself without blocking (`read_open` refuses what is not a regular file).
        let kind = if i + 1 == parts.len() {
            libc::O_NONBLOCK
        } else {
            libc::O_DIRECTORY
        };
        let flags = libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_CLOEXEC | kind;
        // SAFETY: cur is an open folder and name a NUL-terminated string; the result is checked.
        let fd = unsafe { libc::openat(cur.as_raw_fd(), name.as_ptr(), flags) };
        if fd < 0 {
            return Err(format!("{part}: {}", std::io::Error::last_os_error()));
        }
        // SAFETY: fd was just opened and nothing else owns it.
        cur = unsafe { fs::File::from_raw_fd(fd) };
    }
    Ok(cur)
}

/// A package a registry offers.
#[derive(Clone, Debug, Serialize)]
pub struct Offer {
    pub id: String,
    /// Versions with an archive for this computer, newest first.
    pub versions: Vec<String>,
    /// Why this app would refuse it whatever the archive holds, if it would.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub refused: Option<String>,
}

#[derive(Clone, Debug, Serialize)]
pub struct RegistryView {
    pub name: String,
    pub url: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    pub packages: Vec<Offer>,
}

/// What each registry offers for this computer. A registry that cannot be read says why; the
/// installed packages stay as they are.
pub fn offers(dirs: &Dirs, roots: &dyn Roots) -> Result<Vec<RegistryView>, String> {
    let platform = platform();
    Ok(registries(dirs)?
        .into_iter()
        .map(|r| match read_index(&r) {
            Ok((index, _)) => RegistryView {
                packages: index
                    .packages
                    .into_iter()
                    .filter(|(id, _)| valid_id(id))
                    .map(|(id, p)| {
                        let releases: Vec<&IndexRelease> = p
                            .releases
                            .iter()
                            .filter(|x| x.artifacts.contains_key(&platform))
                            .collect();
                        // What install would say of the release offered, from the index alone.
                        let refused = releases.first().and_then(|x| {
                            let a = &x.artifacts[&platform];
                            to_verify(roots, &r, &id, &x.version, a)
                                .and_then(|l| match l {
                                    Some(l) => admission::check_listed(
                                        roots,
                                        &r.url,
                                        &l,
                                        &platform,
                                        (&id, &x.version),
                                    ),
                                    None => Ok(()),
                                })
                                .err()
                        });
                        Offer {
                            refused,
                            versions: releases.iter().map(|x| x.version.clone()).collect(),
                            id,
                        }
                    })
                    .filter(|o| !o.versions.is_empty())
                    .collect(),
                name: r.name,
                url: r.url,
                error: None,
            },
            Err(e) => RegistryView {
                name: r.name,
                url: r.url,
                error: Some(e),
                packages: vec![],
            },
        })
        .collect())
}

/// The signature registry entry a lists for id's release version: all four fields, or none.
/// Part of one is refused, as is a key ID that is not 1 to 64 letters, digits, `.`, `_` or `-`.
fn listed<'a>(
    id: &str,
    version: &str,
    a: &'a Artifact,
) -> Result<Option<admission::Listed<'a>>, String> {
    let (key_id, public_key, descriptor, signature) =
        match (&a.key_id, &a.public_key, &a.descriptor, &a.signature) {
            (None, None, None, None) => return Ok(None),
            (Some(k), Some(p), Some(d), Some(s)) => (k, p, d, s),
            _ => {
                return Err(format!(
                    "{id} {version}: the registry lists part of a signature (key_id, public_key, descriptor and signature go together)"
                ))
            }
        };
    if key_id.is_empty()
        || key_id.len() > 64
        || !key_id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    {
        return Err(format!(
            "{id} {version}: the signature's key_id is not valid"
        ));
    }
    Ok(Some(admission::Listed {
        key_id,
        public_key,
        descriptor,
        signature,
    }))
}

/// What install makes of registry r's entry a for id's release version, before anything is
/// fetched: the signature to verify against r's root, None for a package installed unsigned
/// (Unverified), or why it is refused. Only a reserved publisher's package, from a curated
/// registry, is verified; any other signature is ignored, since no root vouches for it.
fn to_verify<'a>(
    roots: &dyn Roots,
    r: &Registry,
    id: &str,
    version: &str,
    a: &'a Artifact,
) -> Result<Option<admission::Listed<'a>>, String> {
    let l = listed(id, version, a)?;
    let Some(why) = reserved(id, roots) else {
        return Ok(None);
    };
    if !roots.curated(&r.url) {
        return Err(why);
    }
    l.map(Some).ok_or_else(|| {
        format!(
            "{id}: a reserved publisher's package needs a curated first-party signature, and registry {} lists none for {id} {version}",
            r.name
        )
    })
}

/// Fetches and stages package id's release version (the newest with an archive for this
/// computer, when none is named) from registry name. From a curated registry (one a trust root
/// names by its index), a signed artifact is verified against the root before it is unpacked.
pub fn stage_from_registry(
    dirs: &Dirs,
    roots: &dyn Roots,
    name: &str,
    id: &str,
    version: Option<&str>,
) -> Result<Candidate, String> {
    let r = registries(dirs)?
        .into_iter()
        .find(|r| r.name == name)
        .ok_or_else(|| format!("no registry {name} in {}", dirs.registries.display()))?;
    let curated = roots.curated(&r.url);
    // Refused before anything is fetched: an index's listing is no provenance.
    if !curated {
        if let Some(why) = reserved(id, roots) {
            return Err(why);
        }
    }
    let (index, base) = read_index(&r)?;
    let platform = platform();
    let p = index
        .packages
        .get(id)
        .ok_or_else(|| format!("registry {name} does not list {id}"))?;
    let release = p
        .releases
        .iter()
        .filter(|x| version.is_none_or(|v| x.version == v))
        .find(|x| x.artifacts.contains_key(&platform))
        .ok_or_else(|| {
            format!(
                "registry {name} lists no {id}{} for {platform}",
                version.map(|v| format!(" {v}")).unwrap_or_default()
            )
        })?;
    let a = &release.artifacts[&platform];
    let listed = to_verify(roots, &r, id, &release.version, a)?;
    let mut bytes = fetch_artifact(&base, a)?;
    let signing = match listed {
        Some(l) => {
            let (s, archive) =
                admission::verify(roots, &r.url, &l, &platform, (id, &release.version), bytes)?;
            bytes = archive;
            Some(s)
        }
        None => None,
    };
    stage_signed(
        dirs,
        &bytes,
        Source {
            kind: "registry".into(),
            name: r.name.clone(),
            index: Some(r.url.clone()),
        },
        Some(&Expect {
            id: id.into(),
            version: release.version.clone(),
            sha256: a.sha256.to_ascii_lowercase(),
            size: a.size,
        }),
        signing,
        roots,
    )
}

#[path = "device_admission.rs"]
mod admission;
#[path = "device_imports.rs"]
mod imports;
#[path = "device_swap.rs"]
mod swap;
use admission::Signing;

#[cfg(test)]
#[path = "device_tests.rs"]
mod tests;
