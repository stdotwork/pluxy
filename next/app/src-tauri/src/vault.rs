//! The host-private vault (`../app-host/README.md`, "Secrets, references and the vault"): the
//! secrets the person entered natively, the references to them the host gives native tools, and
//! each tool's backups and mutation journals. It lives in the app's data folder (`vault`, 0700;
//! files 0600), never in a folder a package can name, and no page or window API gives its paths,
//! a secret's value, a backup's bytes or a journal's intent. Same-user programs, native tools among
//! them, could read it directly: this separates APIs and storage, it is no OS sandbox.
//!
//! A secret has an opaque ID (`s-…`), a label and its value. The router (`route.rs`) gives a tool
//! a reference (`r-…`) to it for one call: bound to the secret, to the release of the tool it was
//! given to (ID, digest, generation) and to that call, and so to the call's chain. Only that tool,
//! running that release, may pass it on, and only in a call it makes for that call
//! (`Vault::present`); the reference ends with the call. References live in memory only.
//!
//! A client key (`client_keys.rs`) is a secret bound to the core it was made on or entered for
//! (the connection's mode and origin), to core's ID of it then, and to the one native tool (its
//! client) that may get it; the router checks that binding at every use (`ClientKey`).
//!
//! Backups (`b-…`) and journals (`j-…`) belong to a package, whatever its release: a tool reaches
//! its own through `vault` frames on its pipe (`Vault::frame`), only while it serves a host call
//! or answers describe. A journal is begun before a write and committed after it; one still open
//! (the tool crashed, or the app did) goes in the describe of the package's next start, and the
//! tool ends it as committed or rolled back from what it finds, never by writing again. Ending it
//! again with the same outcome is answered the same: delivery of an open journal may repeat.
//! Package removal keeps its vault folder: backups stay until they are removed explicitly.

use base64::Engine;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::{HashMap, VecDeque};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};

const VERSION: u32 = 1;
/// Secrets the vault keeps.
const MAX_SECRETS: usize = 64;
const MAX_LABEL: usize = 64;
/// A secret's value, in bytes. At least 8, so redacting it does not blank ordinary words.
const MIN_VALUE: usize = 8;
const MAX_VALUE: usize = 4096;
/// One backup image, in bytes (it travels base64-encoded in a frame of at most 1 MiB).
const MAX_IMAGE: usize = 512 << 10;
/// Backups a package keeps. shortcut: no retention yet: a full vault refuses new backups until
/// some are removed; P8f2's backup browser adds retention and removal.
const MAX_BACKUPS: usize = 256;
/// A journal's intent, as JSON.
const MAX_INTENT: usize = 16 << 10;
/// Ended journals a package keeps, for its status.
const KEEP_ENDED: usize = 32;
/// Ended references kept so a later use of one is refused saying why; the rest are forgotten.
const KEEP_REFS: usize = 256;
/// What replaces a secret's value or a reference wherever a tool's output carries one.
pub const REDACTED: &str = "[redacted]";

/// A vault operation's failure: a protocol error code and a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Refused {
    pub code: &'static str,
    pub message: String,
}

fn refused(code: &'static str, message: impl Into<String>) -> Refused {
    Refused {
        code,
        message: message.into(),
    }
}

/// A tool's release, as references are bound to it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Holder {
    pub id: String,
    pub sha256: String,
    pub generation: u64,
}

/// The core a client key belongs to: the connection's mode and origin, as `conn.rs` pins them.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct CoreTarget {
    pub mode: crate::conn::Mode,
    pub origin: String,
}

/// What binds a client key: the core it belongs to, core's ID of it when it was kept (core's IDs
/// change when core restarts; none when no single key of core's matched), its masked form, as
/// core lists it, and the native tool it is for.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct ClientKey {
    pub core: CoreTarget,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub key_id: Option<String>,
    pub masked: String,
    pub client: String,
}

#[derive(Clone, Serialize, Deserialize)]
struct Secret {
    id: String,
    label: String,
    created: u64,
    value: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    client_key: Option<ClientKey>,
}

/// A secret as the window sees it: never its value.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct SecretView {
    pub id: String,
    pub label: String,
    pub created: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub client_key: Option<ClientKey>,
}

#[derive(Serialize, Deserialize)]
struct SecretsFile {
    version: u32,
    secrets: Vec<Secret>,
}

struct Reference {
    secret: String,
    holder: Holder,
    call: u64,
    live: bool,
}

#[derive(Clone, Serialize, Deserialize)]
struct Backup {
    id: String,
    label: String,
    size: u64,
    created: u64,
}

#[derive(Clone, Serialize, Deserialize)]
struct Journal {
    id: String,
    /// The operation of the host call it was begun in, and that release's generation.
    op: String,
    generation: u64,
    began: u64,
    intent: Value,
    /// `committed` or `rolled_back`, once ended.
    outcome: Option<String>,
    ended: Option<u64>,
    /// Ended while the tool answered describe: after a crash.
    reconciled: bool,
}

/// A journal as the window's status shows it: never its intent.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct JournalView {
    pub id: String,
    pub op: String,
    pub generation: u64,
    pub began: u64,
    pub outcome: Option<String>,
    pub ended: Option<u64>,
    pub reconciled: bool,
}

#[derive(Default, Serialize, Deserialize)]
struct PackageFile {
    version: u32,
    id: String,
    backups: Vec<Backup>,
    journals: Vec<Journal>,
}

#[derive(Default)]
struct State {
    secrets: Vec<Secret>,
    refs: HashMap<String, Reference>,
    ended: VecDeque<String>,
    /// Values of secrets removed since the app started: still redacted, until it restarts.
    removed: Vec<String>,
    calls: u64,
}

/// The vault. One per app.
pub struct Vault {
    dir: PathBuf,
    state: Mutex<State>,
    /// Why its secrets could not be read: it then keeps none, and changes nothing on disk.
    broken: Option<String>,
}

fn now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn opaque(prefix: &str) -> String {
    format!("{prefix}-{}", &crate::managed::random_key()[..32])
}

fn is_opaque(prefix: &str, id: &str) -> bool {
    id.strip_prefix(prefix)
        .and_then(|r| r.strip_prefix('-'))
        .is_some_and(|h| h.len() == 32 && h.bytes().all(|b| b.is_ascii_hexdigit()))
}

fn printable(s: &str) -> bool {
    !s.chars().any(char::is_control)
}

/// A vault folder, opened without following a link and used only relative to its handle: a
/// folder replaced by a link (to somewhere else) is refused, not followed.
struct Folder(fs::File);

fn os_err(what: &str) -> String {
    format!("{what}: {}", std::io::Error::last_os_error())
}

fn c_name(name: &str) -> Result<std::ffi::CString, String> {
    if name.is_empty() || name.contains('/') || name == "." || name == ".." {
        return Err(format!("{name:?} is not a vault file name"));
    }
    std::ffi::CString::new(name).map_err(|e| e.to_string())
}

/// The data folder of native tool id (`publisher/name`, the host-verified package ID) under root
/// (`<app data>/tools`, beside the vault, never in it): root, then the publisher, then the name,
/// each opened without following a link (from its parent's descriptor below root), made (0700)
/// when missing, and checked to be this user's folder that no one else may use. Its path, which
/// the tool gets as `PLUXY_TOOL_DATA_DIR`; it stays across releases and when the tool is removed.
pub(crate) fn tool_data_dir(root: &Path, id: &str) -> Result<PathBuf, String> {
    let part = |s: &str| {
        !s.is_empty()
            && !s.starts_with('.')
            && s.bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b"-_.".contains(&b))
    };
    let (publisher, name) = id
        .split_once('/')
        .filter(|(p, n)| part(p) && part(n))
        .ok_or_else(|| format!("{id:?} is not a package ID"))?;
    let missing = || format!("{}: could not be made", root.display());
    let tools = Folder::root(root, true)?.ok_or_else(missing)?;
    let publisher_dir = tools.folder(publisher, true)?.ok_or_else(missing)?;
    publisher_dir.folder(name, true)?.ok_or_else(missing)?;
    Ok(root.join(publisher).join(name))
}

impl Folder {
    /// A folder of this user's that no one else may use (made so), from its open handle.
    fn checked(f: fs::File, what: &str) -> Result<Folder, String> {
        use std::os::unix::fs::{MetadataExt, PermissionsExt};
        let m = f.metadata().map_err(|e| format!("{what}: {e}"))?;
        if !m.is_dir() || m.uid() != unsafe { libc::geteuid() } {
            return Err(format!("{what} is not a folder of this user's"));
        }
        if m.mode() & 0o077 != 0 {
            f.set_permissions(fs::Permissions::from_mode(0o700))
                .map_err(|e| format!("{what}: {e}"))?;
        }
        Ok(Folder(f))
    }

    /// The vault's own folder at path, made (0700) when create is set; None when it is missing.
    /// Its parent is the app's data folder; the folder itself is never a link.
    fn root(path: &Path, create: bool) -> Result<Option<Folder>, String> {
        use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
        let what = path.display().to_string();
        if create {
            if let Some(parent) = path.parent() {
                fs::create_dir_all(parent).map_err(|e| format!("{}: {e}", parent.display()))?;
            }
            match fs::DirBuilder::new().mode(0o700).create(path) {
                Err(e) if e.kind() != std::io::ErrorKind::AlreadyExists => {
                    return Err(format!("{what}: {e}"))
                }
                _ => {}
            }
        }
        match fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC)
            .open(path)
        {
            Ok(f) => Folder::checked(f, &what).map(Some),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound && !create => Ok(None),
            Err(e) => Err(format!("{what}: {e}")),
        }
    }

    fn fd(&self) -> i32 {
        use std::os::fd::AsRawFd;
        self.0.as_raw_fd()
    }

    /// The folder name inside it, made (0700) when create is set; None when it is missing.
    fn folder(&self, name: &str, create: bool) -> Result<Option<Folder>, String> {
        use std::os::fd::FromRawFd;
        let c = c_name(name)?;
        if create && unsafe { libc::mkdirat(self.fd(), c.as_ptr(), 0o700) } != 0 {
            let e = std::io::Error::last_os_error();
            if e.kind() != std::io::ErrorKind::AlreadyExists {
                return Err(format!("{name}: {e}"));
            }
        }
        let fd = unsafe {
            libc::openat(
                self.fd(),
                c.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_CLOEXEC,
            )
        };
        if fd < 0 {
            let e = std::io::Error::last_os_error();
            if e.kind() == std::io::ErrorKind::NotFound && !create {
                return Ok(None);
            }
            return Err(format!("{name}: {e}"));
        }
        Folder::checked(unsafe { fs::File::from_raw_fd(fd) }, name).map(Some)
    }

    /// The file name inside it, which must be a regular file of this user's that no one else
    /// may read; None when it is missing.
    fn read(&self, name: &str) -> Result<Option<Vec<u8>>, String> {
        use std::io::Read;
        use std::os::fd::FromRawFd;
        use std::os::unix::fs::MetadataExt;
        let c = c_name(name)?;
        let fd = unsafe {
            libc::openat(
                self.fd(),
                c.as_ptr(),
                libc::O_RDONLY
                    | libc::O_NOFOLLOW
                    | libc::O_NONBLOCK
                    | libc::O_NOCTTY
                    | libc::O_CLOEXEC,
            )
        };
        if fd < 0 {
            let e = std::io::Error::last_os_error();
            if e.kind() == std::io::ErrorKind::NotFound {
                return Ok(None);
            }
            return Err(format!("{name}: {e}"));
        }
        let mut f = unsafe { fs::File::from_raw_fd(fd) };
        let m = f.metadata().map_err(|e| e.to_string())?;
        if !m.is_file() || m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o077 != 0 {
            return Err(format!(
                "{name} must be a file of this user's that no one else may read"
            ));
        }
        let mut b = Vec::new();
        f.read_to_end(&mut b).map_err(|e| e.to_string())?;
        Ok(Some(b))
    }

    /// Writes name (0600), replacing it in one step, and flushes it and the folder to disk.
    fn write(&self, name: &str, b: &[u8]) -> Result<(), String> {
        use std::io::Write;
        use std::os::fd::FromRawFd;
        let c = c_name(name)?;
        let tmp = c_name(&format!(
            ".{name}.{}.tmp",
            &crate::managed::random_key()[..16]
        ))?;
        let fd = unsafe {
            libc::openat(
                self.fd(),
                tmp.as_ptr(),
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                0o600 as libc::c_uint,
            )
        };
        if fd < 0 {
            return Err(os_err(name));
        }
        let mut f = unsafe { fs::File::from_raw_fd(fd) };
        let done = f
            .write_all(b)
            .and_then(|_| f.sync_all())
            .map_err(|e| format!("{name}: {e}"))
            .and_then(|_| {
                if unsafe { libc::renameat(self.fd(), tmp.as_ptr(), self.fd(), c.as_ptr()) } != 0 {
                    return Err(os_err(name));
                }
                self.0.sync_all().map_err(|e| format!("{name}: {e}"))
            });
        if done.is_err() {
            unsafe { libc::unlinkat(self.fd(), tmp.as_ptr(), 0) };
        }
        done
    }

    /// Removes the file name inside it; one already gone is not an error.
    fn remove(&self, name: &str) -> Result<(), String> {
        let c = c_name(name)?;
        if unsafe { libc::unlinkat(self.fd(), c.as_ptr(), 0) } != 0 {
            let e = std::io::Error::last_os_error();
            if e.kind() != std::io::ErrorKind::NotFound {
                return Err(format!("{name}: {e}"));
            }
        }
        Ok(())
    }

    /// The names inside it.
    fn names(&self) -> Result<Vec<String>, String> {
        let mut out = Vec::new();
        for e in fs::read_dir(format!("/proc/self/fd/{}", self.fd())).map_err(|e| e.to_string())? {
            let e = e.map_err(|e| e.to_string())?;
            if let Some(n) = e.file_name().to_str() {
                out.push(n.to_string());
            }
        }
        Ok(out)
    }
}

impl Vault {
    /// The vault in folder dir (created, 0700, on first use), with its secrets read now. One
    /// whose secrets cannot be read keeps none and refuses every change (`broken`), so the file
    /// stays as it is for the person to look at.
    pub fn open(dir: PathBuf) -> Vault {
        let read = || -> Result<Vec<Secret>, String> {
            let Some(root) = Folder::root(&dir, false)? else {
                return Ok(Vec::new());
            };
            match root.read("secrets.json")? {
                None => Ok(Vec::new()),
                Some(b) => {
                    let f: SecretsFile = serde_json::from_slice(&b)
                        .map_err(|e| format!("the vault's secrets are damaged: {e}"))?;
                    if f.version != VERSION {
                        return Err(format!("the vault's secrets have version {}", f.version));
                    }
                    Ok(f.secrets)
                }
            }
        };
        let (secrets, broken) = match read() {
            Ok(s) => (s, None),
            Err(e) => (Vec::new(), Some(e)),
        };
        Vault {
            dir,
            state: Mutex::new(State {
                secrets,
                ..Default::default()
            }),
            broken,
        }
    }

    /// Why the vault refuses changes, if it does.
    pub fn broken(&self) -> Option<&str> {
        self.broken.as_deref()
    }

    fn lock(&self) -> MutexGuard<'_, State> {
        self.state.lock().unwrap()
    }

    fn save_secrets(&self, secrets: &[Secret]) -> Result<(), String> {
        if let Some(why) = &self.broken {
            return Err(why.clone());
        }
        let root = Folder::root(&self.dir, true)?.expect("a vault folder made");
        let b = serde_json::to_vec(&json!({"version": VERSION, "secrets": secrets}))
            .map_err(|e| e.to_string())?;
        root.write("secrets.json", &b)
    }

    /// Whether label may name a secret.
    pub fn check_label(label: &str) -> Result<(), String> {
        if label.trim().is_empty() || label.chars().count() > MAX_LABEL || !printable(label) {
            return Err(format!(
                "a secret's label is 1 to {MAX_LABEL} printable characters"
            ));
        }
        Ok(())
    }

    /// Whether the vault can keep one more secret now: not broken, and not full.
    pub fn can_add(&self) -> Result<(), String> {
        if let Some(why) = &self.broken {
            return Err(why.clone());
        }
        if self.lock().secrets.len() >= MAX_SECRETS {
            return Err(format!("the vault keeps at most {MAX_SECRETS} secrets"));
        }
        Ok(())
    }

    /// Keeps a secret the person entered natively, as label; gives its ID.
    pub fn add_secret(&self, label: &str, value: &str) -> Result<String, String> {
        self.add(label, value, None)
    }

    /// Keeps client key value, bound as client_key says, as label; gives its ID.
    pub fn add_client_key(
        &self,
        label: &str,
        value: &str,
        client_key: ClientKey,
    ) -> Result<String, String> {
        self.add(label, value, Some(client_key))
    }

    fn add(
        &self,
        label: &str,
        value: &str,
        client_key: Option<ClientKey>,
    ) -> Result<String, String> {
        Vault::check_label(label)?;
        if value.len() < MIN_VALUE || value.len() > MAX_VALUE || !printable(value) {
            return Err(format!(
                "a secret is {MIN_VALUE} to {MAX_VALUE} bytes of printable characters"
            ));
        }
        let mut st = self.lock();
        if st.secrets.len() >= MAX_SECRETS {
            return Err(format!("the vault keeps at most {MAX_SECRETS} secrets"));
        }
        let mut secrets = st.secrets.clone();
        let id = opaque("s");
        secrets.push(Secret {
            id: id.clone(),
            label: label.into(),
            created: now(),
            value: value.into(),
            client_key,
        });
        self.save_secrets(&secrets)?;
        st.secrets = secrets;
        Ok(id)
    }

    /// Removes secret id; references to it are refused from now on.
    pub fn remove_secret(&self, id: &str) -> Result<(), String> {
        let mut st = self.lock();
        let secrets: Vec<Secret> = st.secrets.iter().filter(|s| s.id != id).cloned().collect();
        if secrets.len() == st.secrets.len() {
            return Err(format!("the vault has no secret {id}"));
        }
        self.save_secrets(&secrets)?;
        let gone = st
            .secrets
            .iter()
            .find(|s| s.id == id)
            .map(|s| s.value.clone());
        st.removed.extend(gone);
        st.secrets = secrets;
        Ok(())
    }

    pub fn secrets(&self) -> Vec<SecretView> {
        self.lock()
            .secrets
            .iter()
            .map(|s| SecretView {
                id: s.id.clone(),
                label: s.label.clone(),
                created: s.created,
                client_key: s.client_key.clone(),
            })
            .collect()
    }

    pub fn secret(&self, id: &str) -> Option<SecretView> {
        self.secrets().into_iter().find(|s| s.id == id)
    }

    /// Secret id's value, for the host to deliver on a tool's pipe.
    pub fn value(&self, id: &str) -> Option<String> {
        self.lock()
            .secrets
            .iter()
            .find(|s| s.id == id)
            .map(|s| s.value.clone())
    }

    /// A new call's token, which references given in it are bound to.
    pub fn begin_call(&self) -> u64 {
        let mut st = self.lock();
        st.calls += 1;
        st.calls
    }

    /// The call with token call has ended: its references end with it.
    pub fn end_call(&self, call: u64) {
        let mut st = self.lock();
        let ended: Vec<String> = st
            .refs
            .iter()
            .filter(|(_, r)| r.call == call && r.live)
            .map(|(id, _)| id.clone())
            .collect();
        for id in ended {
            if let Some(r) = st.refs.get_mut(&id) {
                r.live = false;
            }
            st.ended.push_back(id);
        }
        while st.ended.len() > KEEP_REFS {
            if let Some(old) = st.ended.pop_front() {
                st.refs.remove(&old);
            }
        }
    }

    /// A reference to secret for holder, in call; None once the secret is gone.
    pub fn mint(&self, secret: &str, holder: &Holder, call: u64) -> Option<String> {
        let mut st = self.lock();
        if !st.secrets.iter().any(|s| s.id == secret) {
            return None;
        }
        let id = opaque("r");
        st.refs.insert(
            id.clone(),
            Reference {
                secret: secret.into(),
                holder: holder.clone(),
                call,
                live: true,
            },
        );
        Some(id)
    }

    /// The secret reference names, when tool caller, running that release, passes it on in a
    /// call it makes for the call with token call: the one it was given in.
    pub fn present(&self, reference: &str, caller: &Holder, call: u64) -> Result<String, String> {
        let st = self.lock();
        let r = st
            .refs
            .get(reference)
            .ok_or_else(|| "it is not a reference the host gave".to_string())?;
        if r.holder.id != caller.id {
            return Err(format!(
                "the reference was given to {}, not {}",
                r.holder.id, caller.id
            ));
        }
        if r.holder.sha256 != caller.sha256 || r.holder.generation != caller.generation {
            return Err(format!(
                "the reference was given to another release of {} (generation {}), not the one running now",
                r.holder.id, r.holder.generation
            ));
        }
        if r.call != call || !r.live {
            return Err(format!(
                "the reference belongs to another call of {}",
                r.holder.id
            ));
        }
        if !st.secrets.iter().any(|s| s.id == r.secret) {
            return Err("its secret was removed from the vault".into());
        }
        Ok(r.secret.clone())
    }

    /// Every value the host must never show: secrets' values (those removed since the app
    /// started too) and, when text may hold one, the references it gave.
    fn needles<'a>(st: &'a State, text: &str) -> Vec<&'a str> {
        let mut n: Vec<&str> = st
            .secrets
            .iter()
            .map(|s| s.value.as_str())
            .chain(st.removed.iter().map(String::as_str))
            .collect();
        if text.contains("r-") {
            n.extend(st.refs.keys().map(String::as_str));
        }
        n
    }

    /// Whether text holds a secret's value or a reference the host gave.
    pub fn carries(&self, text: &str) -> bool {
        Vault::carries_in(&self.lock(), text)
    }

    fn carries_in(st: &State, text: &str) -> bool {
        Vault::needles(st, text).iter().any(|n| text.contains(n))
    }

    /// Whether any key, string or number at any depth of v holds a secret's value or a reference,
    /// each looked at as it is, as a call's input is checked.
    fn holds_secret(st: &State, v: &Value) -> bool {
        match v {
            Value::String(s) => Vault::carries_in(st, s),
            Value::Number(n) => Vault::carries_in(st, &n.to_string()),
            Value::Array(a) => a.iter().any(|x| Vault::holds_secret(st, x)),
            Value::Object(o) => o
                .iter()
                .any(|(k, x)| Vault::carries_in(st, k) || Vault::holds_secret(st, x)),
            Value::Null | Value::Bool(_) => false,
        }
    }

    /// Text with every secret's value and every reference replaced by `REDACTED`. Every place
    /// any of them occurs is found in the text as given, and overlapping or adjoining places are
    /// replaced as one, so no part of an overlapping value is left.
    pub fn redact(&self, text: &str) -> String {
        let st = self.lock();
        let mut spans: Vec<(usize, usize)> = Vec::new();
        for n in Vault::needles(&st, text) {
            spans.extend(text.match_indices(n).map(|(at, m)| (at, at + m.len())));
        }
        if spans.is_empty() {
            return text.to_string();
        }
        spans.sort_unstable();
        let mut out = String::with_capacity(text.len());
        let mut at = 0;
        let mut i = 0;
        while i < spans.len() {
            let (start, mut end) = spans[i];
            while i + 1 < spans.len() && spans[i + 1].0 <= end {
                i += 1;
                end = end.max(spans[i].1);
            }
            out.push_str(&text[at..start]);
            out.push_str(REDACTED);
            at = end;
            i += 1;
        }
        out.push_str(&text[at..]);
        out
    }

    /// Package id's folder's name, under `packages`.
    fn package_name(id: &str) -> String {
        Sha256::digest(id.as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    }

    /// Package id's folder, made when create is set; None when it is missing.
    fn package(&self, id: &str, create: bool) -> Result<Option<Folder>, String> {
        let Some(root) = Folder::root(&self.dir, create)? else {
            return Ok(None);
        };
        let Some(packages) = root.folder("packages", create)? else {
            return Ok(None);
        };
        packages.folder(&Vault::package_name(id), create)
    }

    /// Package id's records and its folder, if it has one. Files there its records do not name
    /// (a backup whose record was never saved or whose removal failed, a write cut short) are
    /// removed: nothing in the vault is out of every API's reach.
    fn load(&self, id: &str) -> Result<(Option<Folder>, PackageFile), String> {
        let empty = || PackageFile {
            version: VERSION,
            id: id.into(),
            ..Default::default()
        };
        let Some(dir) = self.package(id, false)? else {
            return Ok((None, empty()));
        };
        let f = match dir.read("index.json")? {
            None => empty(),
            Some(b) => {
                let f: PackageFile = serde_json::from_slice(&b)
                    .map_err(|e| format!("{id}'s vault records are damaged: {e}"))?;
                if f.version != VERSION || f.id != id {
                    return Err(format!("{id}'s vault records are not its own"));
                }
                f
            }
        };
        for name in dir.names()? {
            if name != "index.json" && !f.backups.iter().any(|b| b.id == name) {
                dir.remove(&name)?;
            }
        }
        Ok((Some(dir), f))
    }

    fn save(&self, dir: &Folder, f: &PackageFile) -> Result<(), String> {
        dir.write(
            "index.json",
            &serde_json::to_vec(f).map_err(|e| e.to_string())?,
        )
    }

    /// Package id's open journals, as describe names them; an error when its records cannot be
    /// read, so the start fails rather than miss one.
    pub fn open_journals(&self, id: &str) -> Result<Vec<Value>, String> {
        let _st = self.lock();
        let (_, f) = self.load(id)?;
        Ok(f.journals
            .iter()
            .filter(|j| j.outcome.is_none())
            .map(|j| {
                json!({"id": j.id, "op": j.op, "generation": j.generation,
                    "began": j.began, "intent": j.intent})
            })
            .collect())
    }

    /// Package id's journals, open and recently ended, for its status.
    pub fn journals(&self, id: &str) -> Result<Vec<JournalView>, String> {
        let _st = self.lock();
        let (_, f) = self.load(id)?;
        Ok(f.journals
            .iter()
            .map(|j| JournalView {
                id: j.id.clone(),
                op: j.op.clone(),
                generation: j.generation,
                began: j.began,
                outcome: j.outcome.clone(),
                ended: j.ended,
                reconciled: j.reconciled,
            })
            .collect())
    }

    /// A `vault` frame of tool (a release of its package), sent for a host call of op it serves,
    /// or, with describing, for the describe it is answering on its package's turn, naming the
    /// open journals it was told of. In describe it may only read backups and end those journals;
    /// in a call, it ends only journals its own release began. Gives the answer's output.
    pub fn request(
        &self,
        tool: &Holder,
        op: &str,
        describing: Option<&[String]>,
        req: &Value,
    ) -> Result<Value, Refused> {
        let bad = |m: &str| refused("invalid_request", m);
        let id_of = |prefix: &str, field: &str| -> Result<String, Refused> {
            req[field]
                .as_str()
                .filter(|v| is_opaque(prefix, v))
                .map(String::from)
                .ok_or_else(|| bad(&format!("{field} is not a {prefix}- ID")))
        };
        let unavailable = |e: String| refused("unavailable", e);
        if let Some(why) = &self.broken {
            return Err(unavailable(why.clone()));
        }
        // One vault operation at a time: each reads and writes the package's records whole.
        let st = self.lock();
        // Text a tool keeps in the vault never holds a secret or a reference: a later release,
        // never given it, would be told of the text in describe.
        let holds_secret = |v: &Value| Vault::holds_secret(&st, v);
        let (dir, mut f) = self.load(&tool.id).map_err(unavailable)?;
        let kind = req["op"].as_str().unwrap_or_default();
        if describing.is_some()
            && !matches!(
                kind,
                "backup.get" | "backup.list" | "journal.end" | "journal.commit"
            )
        {
            return Err(refused(
                "permission",
                "in describe a tool reads its backups and ends the journals it was told of, nothing else",
            ));
        }
        // Its folder, made for a write; a read of a package with none finds nothing.
        let made = || -> Result<Folder, Refused> {
            self.package(&tool.id, true)
                .map_err(unavailable)?
                .ok_or_else(|| unavailable(format!("{}'s vault folder", tool.id)))
        };
        let no_journal = || refused("not_found", format!("{} has no such open journal", tool.id));
        let out = match kind {
            "backup.put" => {
                let label = req["label"].as_str().unwrap_or_default();
                if label.chars().count() > MAX_LABEL || !printable(label) {
                    return Err(bad(&format!(
                        "a backup's label is at most {MAX_LABEL} printable characters"
                    )));
                }
                if holds_secret(&req["label"]) {
                    return Err(bad("a backup's label must not hold a secret or reference"));
                }
                let data = base64::engine::general_purpose::STANDARD
                    .decode(req["data"].as_str().unwrap_or_default())
                    .map_err(|_| bad("data is not base64"))?;
                if data.len() > MAX_IMAGE {
                    return Err(bad(&format!("a backup is at most {MAX_IMAGE} bytes")));
                }
                if f.backups.len() >= MAX_BACKUPS {
                    return Err(refused(
                        "unavailable",
                        format!("{} keeps {MAX_BACKUPS} backups already", tool.id),
                    ));
                }
                let b = Backup {
                    id: opaque("b"),
                    label: label.into(),
                    size: data.len() as u64,
                    created: now(),
                };
                let dir = made()?;
                dir.write(&b.id, &data).map_err(unavailable)?;
                f.backups.push(b.clone());
                if let Err(e) = self.save(&dir, &f) {
                    let _ = dir.remove(&b.id);
                    return Err(unavailable(e));
                }
                json!({"backup": b.id})
            }
            "backup.get" => {
                let id = id_of("b", "backup")?;
                let b = f.backups.iter().find(|b| b.id == id).ok_or_else(|| {
                    refused("not_found", format!("{} has no such backup", tool.id))
                })?;
                let data = dir
                    .as_ref()
                    .map(|d| d.read(&b.id))
                    .transpose()
                    .map_err(unavailable)?
                    .flatten()
                    .ok_or_else(|| refused("not_found", "that backup's data is gone"))?;
                json!({"backup": b.id, "label": b.label, "size": b.size, "created": b.created,
                    "data": base64::engine::general_purpose::STANDARD.encode(data)})
            }
            "backup.list" => json!({"backups": f.backups.iter().map(|b| json!({
                "backup": b.id, "label": b.label, "size": b.size, "created": b.created}))
                .collect::<Vec<_>>()}),
            "backup.delete" => {
                let id = id_of("b", "backup")?;
                if !f.backups.iter().any(|b| b.id == id) {
                    return Err(refused(
                        "not_found",
                        format!("{} has no such backup", tool.id),
                    ));
                }
                f.backups.retain(|b| b.id != id);
                let dir = made()?;
                self.save(&dir, &f).map_err(unavailable)?;
                // A failed removal is retried at the next load: its record is gone already.
                let _ = dir.remove(&id);
                json!({})
            }
            "journal.begin" => {
                if !req["intent"].is_object()
                    || serde_json::to_vec(&req["intent"]).map_or(usize::MAX, |b| b.len())
                        > MAX_INTENT
                {
                    return Err(bad(&format!(
                        "intent is a JSON object of at most {MAX_INTENT} bytes"
                    )));
                }
                if holds_secret(&req["intent"]) {
                    return Err(bad("intent must not hold a secret or reference"));
                }
                if f.journals.iter().any(|j| j.outcome.is_none()) {
                    return Err(refused(
                        "unavailable",
                        format!("{} has a journal still open: end it first", tool.id),
                    ));
                }
                let j = Journal {
                    id: opaque("j"),
                    op: op.into(),
                    generation: tool.generation,
                    began: now(),
                    intent: req["intent"].clone(),
                    outcome: None,
                    ended: None,
                    reconciled: false,
                };
                let id = j.id.clone();
                f.journals.push(j);
                self.save(&made()?, &f).map_err(unavailable)?;
                json!({"journal": id})
            }
            kind @ ("journal.commit" | "journal.end") => {
                let id = id_of("j", "journal")?;
                let outcome = if kind == "journal.commit" {
                    "committed"
                } else {
                    match req["outcome"].as_str() {
                        Some(o @ ("committed" | "rolled_back")) => o,
                        _ => return Err(bad("outcome is committed or rolled_back")),
                    }
                };
                // In describe, a journal it was told of, whichever release began it; in a call,
                // one its own release began.
                let j = f
                    .journals
                    .iter_mut()
                    .find(|j| {
                        j.id == id
                            && match describing {
                                Some(told) => told.contains(&id),
                                None => j.generation == tool.generation,
                            }
                    })
                    .ok_or_else(no_journal)?;
                match j.outcome.as_deref() {
                    Some(o) if o == outcome => {
                        return Ok(json!({"journal": id, "outcome": outcome, "again": true}))
                    }
                    Some(o) => {
                        return Err(refused(
                            "invalid_request",
                            format!("that journal ended as {o} already"),
                        ))
                    }
                    None => {}
                }
                j.outcome = Some(outcome.into());
                j.ended = Some(now());
                j.reconciled = describing.is_some();
                let ended: Vec<usize> = (0..f.journals.len())
                    .filter(|i| f.journals[*i].outcome.is_some())
                    .collect();
                if ended.len() > KEEP_ENDED {
                    let drop = ended.len() - KEEP_ENDED;
                    let gone: Vec<String> = ended[..drop]
                        .iter()
                        .map(|i| f.journals[*i].id.clone())
                        .collect();
                    f.journals.retain(|j| !gone.contains(&j.id));
                }
                self.save(&made()?, &f).map_err(unavailable)?;
                json!({"journal": id, "outcome": outcome})
            }
            other => {
                return Err(refused(
                    "unsupported",
                    format!("the vault has no operation {other:?}"),
                ))
            }
        };
        Ok(out)
    }
}

/// The vault as the program host reaches it.
impl crate::program::Private for Vault {
    fn journals(&self, id: &str) -> Result<Vec<Value>, String> {
        self.open_journals(id)
    }

    fn frame(
        &self,
        tool: &crate::program::Hop,
        op: &str,
        describing: Option<&[String]>,
        request: &Value,
    ) -> Result<Value, crate::program::CallError> {
        let crate::program::Hop::Package {
            id,
            sha256,
            generation,
        } = tool
        else {
            return Err(crate::program::CallError {
                code: "permission".into(),
                message: "only a tool has a vault".into(),
                unknown: false,
            });
        };
        let holder = Holder {
            id: id.clone(),
            sha256: sha256.clone(),
            generation: *generation,
        };
        self.request(&holder, op, describing, request)
            .map_err(|e| crate::program::CallError {
                code: e.code.into(),
                message: e.message,
                unknown: false,
            })
    }

    fn redact(&self, text: &str) -> String {
        Vault::redact(self, text)
    }
}

#[cfg(test)]
#[path = "vault_tests.rs"]
mod tests;
