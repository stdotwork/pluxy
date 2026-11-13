//! The broker's store and reads (Unix): see `access.rs`.

use super::{supported, Declared, Facts, Signed};
use base64::Engine;
use cap_std::fs::{Dir, OpenOptions, OpenOptionsExt};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::HashMap;
use std::fs;
use std::io::{ErrorKind, Write};
use std::os::unix::fs::{FileExt, MetadataExt, OpenOptionsExt as _, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

const VERSION: u32 = 0;
/// The most a read returns at once; a page asks again from `offset` for more.
pub const MAX_READ: u64 = 1 << 20;
/// The most entries a listing returns.
pub const MAX_ENTRIES: usize = 1000;
const MAX_PATH: usize = 4096;
const FILE: &str = "access.json";
const ASIDE: &str = "access.json.corrupt-";

/// A signer the person approved in a native confirmation, for one source.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Signer {
    pub publisher: String,
    pub source: String,
    pub key_id: String,
    pub public_key: String,
    pub approved_at: u64,
}

/// The chosen path's own identity, when it is a link.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Link {
    pub dev: u64,
    pub ino: u64,
    pub text: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Grant {
    pub id: String,
    pub package: String,
    pub resource: String,
    pub kind: String,
    pub operations: Vec<String>,
    pub source: String,
    pub sha256: String,
    pub publisher: String,
    pub key_id: String,
    pub public_key: String,
    /// What the person chose.
    pub selected: PathBuf,
    /// What it resolved to at review.
    pub target: PathBuf,
    pub dev: u64,
    pub ino: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub link: Option<Link>,
    /// A document portal file: never saved, so it ends with this run of the app.
    #[serde(default)]
    pub session_only: bool,
    /// The link or target changed since review: refused until granted again.
    #[serde(default)]
    pub changed: bool,
    pub revision: u64,
    pub granted_at: u64,
}

#[derive(Default, Serialize, Deserialize)]
struct Saved {
    version: u32,
    revision: u64,
    signers: Vec<Signer>,
    grants: Vec<Grant>,
}

/// Why a page's call was refused: its channel error code.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Denial {
    /// Malformed parameters, an undeclared resource or operation, a path with a file grant.
    BadRequest,
    /// No grant for this package, release, source and signer.
    NotGranted,
    /// A path that leaves the folder, an absolute path, a link out, not a regular file.
    Refused,
    /// The chosen link or its target changed since review.
    Changed,
    /// The file is gone, or the document portal no longer offers it.
    Unavailable,
    /// The grant was revoked while the call was in flight.
    Revoked,
}

impl Denial {
    pub fn code(self) -> &'static str {
        match self {
            Self::BadRequest => "bad_request",
            Self::NotGranted => "not_granted",
            Self::Refused => "refused",
            Self::Changed => "changed",
            Self::Unavailable => "unavailable",
            Self::Revoked => "revoked",
        }
    }
    pub fn message(self) -> &'static str {
        match self {
            Self::BadRequest => "this call's parameters are not valid for this resource",
            Self::NotGranted => "this resource is not granted to this release of this package",
            Self::Refused => "this path is outside what was granted, or not a regular file",
            Self::Changed => "what was granted changed since it was granted; ask for it again",
            Self::Unavailable => "the granted file is not available",
            Self::Revoked => "the grant was revoked",
        }
    }
}

/// A chosen path, resolved for review.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Resolved {
    pub selected: PathBuf,
    pub target: PathBuf,
    pub dev: u64,
    pub ino: u64,
    pub link: Option<Link>,
    pub portal: bool,
}

/// The document portal's mount for this session.
fn portal_mount() -> PathBuf {
    match std::env::var_os("XDG_RUNTIME_DIR") {
        Some(d) if !d.is_empty() => PathBuf::from(d).join("doc"),
        // SAFETY: getuid has no preconditions.
        _ => PathBuf::from(format!("/run/user/{}/doc", unsafe { libc::getuid() })),
    }
}

/// Whether target is a file the document portal offers.
pub fn is_portal(target: &Path) -> bool {
    let mount = portal_mount();
    target.starts_with(&mount) || fs::canonicalize(&mount).is_ok_and(|m| target.starts_with(m))
}

fn link_of(selected: &Path) -> Result<Option<Link>, String> {
    let m = fs::symlink_metadata(selected).map_err(|e| format!("{}: {e}", selected.display()))?;
    if !m.file_type().is_symlink() {
        return Ok(None);
    }
    let text = fs::read_link(selected).map_err(|e| format!("{}: {e}", selected.display()))?;
    let text = text
        .to_str()
        .ok_or("the chosen link's text is not UTF-8")?
        .to_string();
    Ok(Some(Link {
        dev: m.dev(),
        ino: m.ino(),
        text,
    }))
}

/// Resolves what the person chose for a resource of kind: an absolute UTF-8 path to a regular
/// file (`file`) or a folder (`subtree`), following a link once. A whole home folder or the
/// root folder is refused.
pub fn resolve(selected: &Path, kind: &str) -> Result<Resolved, String> {
    if !selected.is_absolute() || selected.to_str().is_none() {
        return Err("choose a file by an absolute UTF-8 path".into());
    }
    let link = link_of(selected)?;
    let target = fs::canonicalize(selected).map_err(|e| format!("{}: {e}", selected.display()))?;
    if target.to_str().is_none() {
        return Err("the chosen file resolves to a path that is not UTF-8".into());
    }
    let m = fs::metadata(&target).map_err(|e| format!("{}: {e}", target.display()))?;
    match kind {
        "file" if m.is_file() => {}
        "file" => return Err(format!("{} is not a regular file", target.display())),
        "subtree" if m.is_dir() => {
            let home = std::env::var_os("HOME").and_then(|h| fs::canonicalize(h).ok());
            if target == Path::new("/") || home.as_deref() == Some(target.as_path()) {
                return Err(
                    "a whole home folder or the root folder cannot be granted: choose a folder inside it"
                        .into(),
                );
            }
        }
        "subtree" => return Err(format!("{} is not a folder", target.display())),
        _ => return Err(format!("resources of kind {kind} cannot be granted")),
    }
    Ok(Resolved {
        selected: selected.into(),
        portal: is_portal(&target),
        target,
        dev: m.dev(),
        ino: m.ino(),
        link,
    })
}

/// Whether what g names is still what was reviewed: the chosen path's link, where it resolves
/// and the target's identity.
fn current(g: &Grant) -> Result<(), Denial> {
    let gone = |e: std::io::Error| {
        if e.kind() == ErrorKind::NotFound {
            Denial::Unavailable
        } else {
            Denial::Changed
        }
    };
    let link = link_of(&g.selected).map_err(|_| {
        if fs::symlink_metadata(&g.selected).is_err() {
            Denial::Unavailable
        } else {
            Denial::Changed
        }
    })?;
    if link != g.link {
        return Err(Denial::Changed);
    }
    let target = fs::canonicalize(&g.selected).map_err(gone)?;
    if target != g.target {
        return Err(Denial::Changed);
    }
    let m = fs::metadata(&target).map_err(gone)?;
    if (m.dev(), m.ino()) != (g.dev, g.ino) {
        return Err(Denial::Changed);
    }
    Ok(())
}

/// An open handle on a granted target, kept while the app runs.
pub enum Handle {
    File(fs::File),
    Dir(Dir),
}

fn log_open(path: &Path) {
    #[cfg(feature = "native-test")]
    eprintln!("native-test: access_io {}", json!({ "path": path }));
    #[cfg(not(feature = "native-test"))]
    let _ = path;
}

const NO_BLOCK: i32 = libc::O_NONBLOCK | libc::O_NOCTTY;

/// Opens g's target and checks it is the one reviewed.
fn open(g: &Grant) -> Result<Handle, Denial> {
    let map = |e: std::io::Error| {
        if e.kind() == ErrorKind::NotFound {
            Denial::Unavailable
        } else {
            Denial::Changed
        }
    };
    if g.kind == "subtree" {
        let d = Dir::open_ambient_dir(&g.target, cap_std::ambient_authority()).map_err(map)?;
        let m = d.dir_metadata().map_err(map)?;
        if (
            cap_std::fs::MetadataExt::dev(&m),
            cap_std::fs::MetadataExt::ino(&m),
        ) != (g.dev, g.ino)
        {
            return Err(Denial::Changed);
        }
        Ok(Handle::Dir(d))
    } else {
        let f = fs::OpenOptions::new()
            .read(true)
            .custom_flags(NO_BLOCK)
            .open(&g.target)
            .map_err(map)?;
        log_open(&g.target);
        let m = f.metadata().map_err(map)?;
        if !m.is_file() || (m.dev(), m.ino()) != (g.dev, g.ino) {
            return Err(Denial::Changed);
        }
        Ok(Handle::File(f))
    }
}

/// A path inside a granted folder, as a page names it: slash-separated parts, none empty, `.`
/// or `..`, not absolute. Empty means the folder itself, where allowed.
fn relative(p: &str, folder_itself: bool) -> Result<PathBuf, Denial> {
    if p.len() > MAX_PATH {
        return Err(Denial::BadRequest);
    }
    if p.is_empty() {
        return if folder_itself {
            Ok(PathBuf::from("."))
        } else {
            Err(Denial::Refused)
        };
    }
    if p.starts_with('/')
        || p.split('/')
            .any(|part| part.is_empty() || part == "." || part == ".." || part.contains('\0'))
    {
        return Err(Denial::Refused);
    }
    Ok(PathBuf::from(p))
}

fn read_chunk(f: &fs::File, offset: u64) -> Result<Value, Denial> {
    let size = f.metadata().map_err(|_| Denial::Unavailable)?.len();
    let want = size.saturating_sub(offset).min(MAX_READ) as usize;
    let mut buf = vec![0u8; want];
    let mut got = 0;
    while got < want {
        match f.read_at(&mut buf[got..], offset + got as u64) {
            Ok(0) => break,
            Ok(n) => got += n,
            Err(e) if e.kind() == ErrorKind::Interrupted => {}
            Err(_) => return Err(Denial::Unavailable),
        }
    }
    buf.truncate(got);
    Ok(json!({
        "data": base64::engine::general_purpose::STANDARD.encode(&buf),
        "size": size,
        "offset": offset,
        "eof": offset + got as u64 >= size,
    }))
}

fn open_error(e: std::io::Error) -> Denial {
    match e.kind() {
        ErrorKind::NotFound => Denial::Unavailable,
        _ => Denial::Refused,
    }
}

/// What a page asked for, checked.
#[derive(Clone, Debug, PartialEq)]
pub enum Op {
    Read { path: Option<String>, offset: u64 },
    List { path: String },
}

/// A call admitted against a grant, its I/O not yet done.
pub struct Begun {
    pub grant: Grant,
    pub op: Op,
    pub handle: Option<Arc<Handle>>,
}

/// Does the I/O of a begun call, outside the store's lock. Returns the result and the handle
/// it opened, if it had none.
pub fn run(b: &Begun) -> (Result<Value, Denial>, Option<Arc<Handle>>) {
    if let Err(d) = current(&b.grant) {
        return (Err(d), None);
    }
    let (handle, opened) = match &b.handle {
        Some(h) => (h.clone(), None),
        None => match open(&b.grant) {
            Ok(h) => {
                let h = Arc::new(h);
                (h.clone(), Some(h))
            }
            Err(d) => return (Err(d), None),
        },
    };
    let result = match (&*handle, &b.op) {
        (Handle::File(f), Op::Read { path: None, offset }) => read_chunk(f, *offset),
        (
            Handle::Dir(d),
            Op::Read {
                path: Some(p),
                offset,
            },
        ) => read_in(&b.grant, d, p, *offset),
        (Handle::Dir(d), Op::List { path }) => list_in(d, path),
        _ => Err(Denial::BadRequest),
    };
    (result, opened)
}

fn read_in(g: &Grant, d: &Dir, p: &str, offset: u64) -> Result<Value, Denial> {
    let rel = relative(p, false)?;
    let f = d
        .open_with(&rel, OpenOptions::new().read(true).custom_flags(NO_BLOCK))
        .map_err(open_error)?
        .into_std();
    log_open(&g.target.join(&rel));
    if !f.metadata().map_err(|_| Denial::Unavailable)?.is_file() {
        return Err(Denial::Refused);
    }
    read_chunk(&f, offset)
}

fn list_in(d: &Dir, p: &str) -> Result<Value, Denial> {
    let rel = relative(p, true)?;
    let entries = if rel == Path::new(".") {
        d.entries()
    } else {
        d.read_dir(&rel)
    }
    .map_err(open_error)?;
    let mut out = Vec::new();
    for e in entries {
        let Ok(e) = e else { continue };
        let Some(name) = e.file_name().to_str().map(String::from) else {
            continue;
        };
        let Ok(t) = e.file_type() else { continue };
        let entry = if t.is_symlink() {
            json!({"name": name, "kind": "link"})
        } else if t.is_dir() {
            json!({"name": name, "kind": "folder"})
        } else if t.is_file() {
            let size = e.metadata().map(|m| m.len()).unwrap_or(0);
            json!({"name": name, "kind": "file", "size": size})
        } else {
            json!({"name": name, "kind": "other"})
        };
        out.push((name, entry));
    }
    out.sort_by(|a, b| a.0.cmp(&b.0));
    let truncated = out.len() > MAX_ENTRIES;
    out.truncate(MAX_ENTRIES);
    let entries: Vec<Value> = out.into_iter().map(|(_, e)| e).collect();
    Ok(json!({"entries": entries, "truncated": truncated}))
}

/// Checks a page's `resources.read` or `resources.list` parameters, as the native ingress
/// admitted them (an object): `resource`, and `path` and `offset` where they apply.
pub fn parse(method: &str, params: &Value) -> Result<(String, Option<String>, u64), Denial> {
    let o = params.as_object().ok_or(Denial::BadRequest)?;
    let allowed: &[&str] = match method {
        "resources.read" => &["resource", "path", "offset"],
        "resources.list" => &["resource", "path"],
        _ => return Err(Denial::BadRequest),
    };
    if o.keys().any(|k| !allowed.contains(&k.as_str())) {
        return Err(Denial::BadRequest);
    }
    let resource = o
        .get("resource")
        .and_then(Value::as_str)
        .filter(|r| !r.is_empty() && r.len() <= 64)
        .ok_or(Denial::BadRequest)?
        .to_string();
    let path = match o.get("path") {
        None => None,
        Some(Value::String(p)) => Some(p.clone()),
        Some(_) => return Err(Denial::BadRequest),
    };
    let offset = match o.get("offset") {
        None => 0,
        Some(v) => v.as_u64().ok_or(Denial::BadRequest)?,
    };
    Ok((resource, path, offset))
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

fn random_id() -> Result<String, String> {
    let mut b = [0u8; 16];
    getrandom::fill(&mut b).map_err(|e| e.to_string())?;
    Ok(b.iter().map(|x| format!("{x:02x}")).collect())
}

/// Approved signers and grants, saved in `access.json` (session-only grants are not).
pub struct Store {
    path: PathBuf,
    saved: Saved,
    /// Grants for this run of the app only.
    session: Vec<Grant>,
    handles: HashMap<String, Arc<Handle>>,
    inflight: HashMap<String, usize>,
    /// Why the saved file could not be used at start, if it could not.
    pub warning: Option<String>,
    /// Why nothing can be saved: a damaged file that could not be moved aside.
    locked: Option<String>,
}

impl Store {
    /// Opens the store in folder dir (the app's config folder).
    pub fn open(dir: &Path) -> Store {
        let path = dir.join(FILE);
        let mut s = Store {
            path: path.clone(),
            saved: Saved::default(),
            session: vec![],
            handles: HashMap::new(),
            inflight: HashMap::new(),
            warning: None,
            locked: None,
        };
        let bytes = match fs::symlink_metadata(&path) {
            Err(e) if e.kind() == ErrorKind::NotFound => return s,
            Err(e) => Err(e.to_string()),
            Ok(m) if !m.is_file() => Err("it is not a regular file".to_string()),
            Ok(_) => fs::OpenOptions::new()
                .read(true)
                .custom_flags(libc::O_NOFOLLOW)
                .open(&path)
                .and_then(|mut f| {
                    let mut b = vec![];
                    std::io::Read::read_to_end(&mut f, &mut b).map(|_| b)
                })
                .map_err(|e| e.to_string()),
        };
        let parsed = bytes.and_then(|b| {
            serde_json::from_slice::<Saved>(&b)
                .map_err(|e| e.to_string())
                .and_then(|saved| {
                    if saved.version == VERSION {
                        Ok(saved)
                    } else {
                        Err(format!("version {} is not supported", saved.version))
                    }
                })
        });
        match parsed {
            Ok(saved) => s.saved = saved,
            Err(why) => {
                let aside = dir.join(format!("{ASIDE}{}", now()));
                match fs::rename(&path, &aside) {
                    Ok(()) => {
                        s.warning = Some(format!(
                            "{} could not be read ({why}); it was moved to {} and Access starts with no grants",
                            path.display(),
                            aside.display()
                        ))
                    }
                    Err(e) => {
                        let why = format!(
                            "{} could not be read ({why}) or moved aside ({e}): Access is off until it is fixed",
                            path.display()
                        );
                        s.warning = Some(why.clone());
                        s.locked = Some(why);
                    }
                }
            }
        }
        s
    }

    fn save(&self) -> Result<(), String> {
        if let Some(why) = &self.locked {
            return Err(why.clone());
        }
        let dir = self.path.parent().ok_or("no folder for access.json")?;
        fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        let m = fs::metadata(dir).map_err(|e| e.to_string())?;
        if m.permissions().mode() & 0o077 != 0 {
            fs::set_permissions(dir, fs::Permissions::from_mode(0o700))
                .map_err(|e| e.to_string())?;
        }
        let tmp = dir.join(format!(".{FILE}.{}", random_id()?));
        let result = (|| {
            let mut f = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&tmp)
                .map_err(|e| e.to_string())?;
            f.write_all(&serde_json::to_vec_pretty(&self.saved).map_err(|e| e.to_string())?)
                .map_err(|e| e.to_string())?;
            f.sync_all().map_err(|e| e.to_string())?;
            fs::rename(&tmp, &self.path).map_err(|e| e.to_string())?;
            fs::File::open(dir)
                .and_then(|d| d.sync_all())
                .map_err(|e| e.to_string())
        })();
        let _ = fs::remove_file(&tmp);
        result
    }

    fn usable(&self) -> Result<(), String> {
        match &self.locked {
            Some(why) => Err(why.clone()),
            None => Ok(()),
        }
    }

    pub fn signers(&self) -> &[Signer] {
        &self.saved.signers
    }

    /// Every grant, saved and session-only.
    pub fn grants(&self) -> impl Iterator<Item = &Grant> {
        self.saved.grants.iter().chain(self.session.iter())
    }

    pub fn approved(&self, s: &Signed) -> bool {
        self.saved.signers.iter().any(|a| {
            a.publisher == s.publisher
                && a.source == s.source
                && a.key_id == s.key_id
                && a.public_key == s.public_key
        })
    }

    /// Records the signer of s as approved for its source, after native confirmation.
    pub fn approve(&mut self, s: &Signed) -> Result<(), String> {
        self.usable()?;
        if self.approved(s) {
            return Ok(());
        }
        self.saved.signers.push(Signer {
            publisher: s.publisher.clone(),
            source: s.source.clone(),
            key_id: s.key_id.clone(),
            public_key: s.public_key.clone(),
            approved_at: now(),
        });
        if let Err(e) = self.save() {
            self.saved.signers.pop();
            return Err(e);
        }
        Ok(())
    }

    /// The declaration of resource, if package f may be granted it; otherwise why not.
    pub fn may_grant(&self, f: &Facts, resource: &str) -> Result<Declared, String> {
        self.usable()?;
        let d = f
            .declared
            .iter()
            .find(|d| d.id == resource)
            .cloned()
            .ok_or_else(|| format!("{} does not declare a resource {resource}", f.id))?;
        let Some(s) = &f.signed else {
            return Err(format!(
                "{} is not signed: only a package signed by a signer you approved can be granted files",
                f.id
            ));
        };
        if f.source.as_deref() != Some(s.source.as_str()) {
            return Err(format!("{}: its signature names another source", f.id));
        }
        if !self.approved(s) {
            return Err(format!(
                "the signer of {} ({} key {}) is not approved for its source {}: approve the signer first",
                f.id, s.publisher, s.key_id, s.source
            ));
        }
        if !d.operations.iter().any(|o| supported(&d.kind, o)) {
            return Err(format!(
                "{} {resource}: this app can grant none of its operations",
                f.id
            ));
        }
        Ok(d)
    }

    /// Records a grant the person confirmed, replacing any earlier grant of the same resource.
    pub fn add(&mut self, f: &Facts, d: &Declared, r: &Resolved) -> Result<Grant, String> {
        self.may_grant(f, &d.id)?;
        let s = f.signed.as_ref().ok_or("not signed")?;
        let before = (
            self.saved.grants.clone(),
            self.session.clone(),
            self.saved.revision,
        );
        self.drop_where(|g| g.package == f.id && g.resource == d.id);
        self.saved.revision += 1;
        let g = Grant {
            id: random_id()?,
            package: f.id.clone(),
            resource: d.id.clone(),
            kind: d.kind.clone(),
            operations: d
                .operations
                .iter()
                .filter(|o| supported(&d.kind, o))
                .cloned()
                .collect(),
            source: s.source.clone(),
            sha256: f.sha256.clone(),
            publisher: s.publisher.clone(),
            key_id: s.key_id.clone(),
            public_key: s.public_key.clone(),
            selected: r.selected.clone(),
            target: r.target.clone(),
            dev: r.dev,
            ino: r.ino,
            link: r.link.clone(),
            session_only: r.portal,
            changed: false,
            revision: self.saved.revision,
            granted_at: now(),
        };
        if g.session_only {
            self.session.push(g.clone());
        } else {
            self.saved.grants.push(g.clone());
        }
        if let Err(e) = self.save() {
            (self.saved.grants, self.session, self.saved.revision) = before;
            return Err(e);
        }
        Ok(g)
    }

    fn drop_where(&mut self, f: impl Fn(&Grant) -> bool) -> usize {
        let mut n = 0;
        for list in [&mut self.saved.grants, &mut self.session] {
            list.retain(|g| {
                let gone = f(g);
                if gone {
                    n += 1;
                }
                !gone
            });
        }
        let live: Vec<String> = self.grants().map(|g| g.id.clone()).collect();
        self.handles.retain(|id, _| live.contains(id));
        n
    }

    /// Revokes grant id. Returns how many of its calls are still in flight: they answer
    /// `revoked`, and `in_flight` reaches zero when they have.
    pub fn revoke(&mut self, id: &str) -> Result<usize, String> {
        self.usable()?;
        if !self.grants().any(|g| g.id == id) {
            return Err(format!("no grant {id}"));
        }
        let before = (
            self.saved.grants.clone(),
            self.session.clone(),
            self.saved.revision,
        );
        self.drop_where(|g| g.id == id);
        self.saved.revision += 1;
        if let Err(e) = self.save() {
            (self.saved.grants, self.session, self.saved.revision) = before;
            return Err(e);
        }
        Ok(self.in_flight(id))
    }

    pub fn in_flight(&self, id: &str) -> usize {
        self.inflight.get(id).copied().unwrap_or(0)
    }

    /// Forgets every grant of a package removed from this device.
    pub fn remove_package(&mut self, id: &str) -> Result<(), String> {
        if self.drop_where(|g| g.package == id) > 0 {
            self.saved.revision += 1;
            self.save()?;
        }
        Ok(())
    }

    /// The grant of resource that applies to package f as it is now.
    fn active(&self, f: &Facts, resource: &str) -> Option<&Grant> {
        let s = f.signed.as_ref()?;
        if !self.approved(s) || f.source.as_deref() != Some(s.source.as_str()) {
            return None;
        }
        self.grants().find(|g| {
            g.package == f.id
                && g.resource == resource
                && g.source == s.source
                && g.sha256 == f.sha256
                && g.publisher == s.publisher
                && g.key_id == s.key_id
                && g.public_key == s.public_key
        })
    }

    /// `resources.status`: the resources the page's release declares, and which are granted.
    pub fn status(&self, f: &Facts, page_digest: &str) -> Value {
        let resources: Vec<Value> = f
            .declared
            .iter()
            .map(|d| {
                let g = (f.enabled && page_digest == f.sha256 && self.locked.is_none())
                    .then(|| self.active(f, &d.id))
                    .flatten();
                json!({
                    "id": d.id,
                    "kind": d.kind,
                    "operations": d.operations,
                    "granted": g.is_some_and(|g| !g.changed),
                    "session_only": g.is_some_and(|g| g.session_only),
                })
            })
            .collect();
        json!({ "resources": resources })
    }

    /// Admits a page's read or list against its package's grants, before any I/O.
    pub fn begin(
        &mut self,
        f: &Facts,
        page_digest: &str,
        method: &str,
        params: &Value,
    ) -> Result<Begun, Denial> {
        let (resource, path, offset) = parse(method, params)?;
        let d = f
            .declared
            .iter()
            .find(|d| d.id == resource)
            .ok_or(Denial::BadRequest)?;
        let (op, name) = match (method, d.kind.as_str(), path) {
            ("resources.read", "file", None) => (Op::Read { path: None, offset }, "read"),
            ("resources.read", "subtree", Some(p)) => (
                Op::Read {
                    path: Some(p),
                    offset,
                },
                "read",
            ),
            ("resources.list", "subtree", p) => (
                Op::List {
                    path: p.unwrap_or_default(),
                },
                "list",
            ),
            _ => return Err(Denial::BadRequest),
        };
        if !d.operations.iter().any(|o| o == name) {
            return Err(Denial::BadRequest);
        }
        if self.locked.is_some() || !f.enabled || page_digest != f.sha256 {
            return Err(Denial::NotGranted);
        }
        let g = self.active(f, &resource).ok_or(Denial::NotGranted)?;
        if g.changed {
            return Err(Denial::Changed);
        }
        if !g.operations.iter().any(|o| o == name) {
            return Err(Denial::NotGranted);
        }
        let b = Begun {
            grant: g.clone(),
            op,
            handle: self.handles.get(&g.id).cloned(),
        };
        *self.inflight.entry(b.grant.id.clone()).or_default() += 1;
        Ok(b)
    }

    /// Ends a begun call: the result stands only if its grant is still there at the revision it
    /// began with. A changed link or target ends the grant until it is granted again.
    pub fn finish(
        &mut self,
        b: &Begun,
        result: Result<Value, Denial>,
        opened: Option<Arc<Handle>>,
    ) -> Result<Value, Denial> {
        let id = &b.grant.id;
        if let Some(n) = self.inflight.get_mut(id) {
            *n = n.saturating_sub(1);
            if *n == 0 {
                self.inflight.remove(id);
            }
        }
        let live = self
            .grants()
            .any(|g| g.id == *id && g.revision == b.grant.revision);
        if !live {
            return Err(Denial::Revoked);
        }
        if result == Err(Denial::Changed) {
            for g in self.saved.grants.iter_mut().chain(self.session.iter_mut()) {
                if g.id == *id {
                    g.changed = true;
                }
            }
            self.handles.remove(id);
            if let Err(e) = self.save() {
                eprintln!("pluxy-app: could not save a changed grant: {e}");
            }
        } else if let Some(h) = opened {
            self.handles.insert(id.clone(), h);
        }
        result
    }
}

#[cfg(test)]
#[path = "access_tests.rs"]
mod tests;
