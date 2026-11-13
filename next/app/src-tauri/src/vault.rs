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

#[derive(Clone, Serialize, Deserialize)]
struct Secret {
    id: String,
    label: String,
    created: u64,
    value: String,
}

/// A secret as the window sees it: never its value.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct SecretView {
    pub id: String,
    pub label: String,
    pub created: u64,
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

fn private_dir(p: &Path) -> Result<(), String> {
    use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(p)
        .map_err(|e| format!("{}: {e}", p.display()))?;
    let m = fs::symlink_metadata(p).map_err(|e| format!("{}: {e}", p.display()))?;
    if !m.is_dir() {
        return Err(format!("{} is not a folder", p.display()));
    }
    fs::set_permissions(p, fs::Permissions::from_mode(0o700))
        .map_err(|e| format!("{}: {e}", p.display()))
}

/// The file at p, which must be a regular file of this user's that no one else may read.
fn read_private(p: &Path) -> Result<Option<Vec<u8>>, String> {
    use std::io::Read;
    use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
    let mut f = match fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_NOCTTY)
        .open(p)
    {
        Ok(f) => f,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(format!("{}: {e}", p.display())),
    };
    let m = f.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() || m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o077 != 0 {
        return Err(format!(
            "{} must be a file of this user's that no one else may read",
            p.display()
        ));
    }
    let mut b = Vec::new();
    f.read_to_end(&mut b).map_err(|e| e.to_string())?;
    Ok(Some(b))
}

fn write(p: &Path, b: &[u8]) -> Result<(), String> {
    crate::managed::write_private(p, b).map_err(|e| format!("{}: {e}", p.display()))
}

impl Vault {
    /// The vault in folder dir (created, 0700, on first use), with its secrets read now. One
    /// whose secrets cannot be read keeps none and refuses every change (`broken`), so the file
    /// stays as it is for the person to look at.
    pub fn open(dir: PathBuf) -> Vault {
        let read = || -> Result<Vec<Secret>, String> {
            match read_private(&dir.join("secrets.json"))? {
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
        private_dir(&self.dir)?;
        let b = serde_json::to_vec(&json!({"version": VERSION, "secrets": secrets}))
            .map_err(|e| e.to_string())?;
        write(&self.dir.join("secrets.json"), &b)
    }

    /// Keeps a secret the person entered natively, as label; gives its ID.
    pub fn add_secret(&self, label: &str, value: &str) -> Result<String, String> {
        if label.trim().is_empty() || label.chars().count() > MAX_LABEL || !printable(label) {
            return Err(format!(
                "a secret's label is 1 to {MAX_LABEL} printable characters"
            ));
        }
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

    /// Whether text holds a secret's value or a reference the host gave.
    pub fn carries(&self, text: &str) -> bool {
        let st = self.lock();
        st.secrets.iter().any(|s| text.contains(&s.value))
            || st.refs.keys().any(|r| text.contains(r.as_str()))
    }

    /// Text with every secret's value and every reference replaced by `REDACTED`.
    pub fn redact(&self, text: &str) -> String {
        let st = self.lock();
        let mut out = text.to_string();
        for s in &st.secrets {
            if out.contains(&s.value) {
                out = out.replace(&s.value, REDACTED);
            }
        }
        if out.contains("r-") {
            for r in st.refs.keys() {
                if out.contains(r.as_str()) {
                    out = out.replace(r.as_str(), REDACTED);
                }
            }
        }
        out
    }

    /// v with `redact` applied to every string in it, keys included.
    pub fn redact_value(&self, v: &Value) -> Value {
        match v {
            Value::String(s) => Value::String(self.redact(s)),
            Value::Array(a) => Value::Array(a.iter().map(|x| self.redact_value(x)).collect()),
            Value::Object(m) => Value::Object(
                m.iter()
                    .map(|(k, x)| (self.redact(k), self.redact_value(x)))
                    .collect(),
            ),
            other => other.clone(),
        }
    }

    fn package_dir(&self, id: &str) -> PathBuf {
        let h: String = Sha256::digest(id.as_bytes())
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect();
        self.dir.join("packages").join(h)
    }

    fn load(&self, id: &str) -> Result<PackageFile, String> {
        match read_private(&self.package_dir(id).join("index.json"))? {
            None => Ok(PackageFile {
                version: VERSION,
                id: id.into(),
                ..Default::default()
            }),
            Some(b) => {
                let f: PackageFile = serde_json::from_slice(&b)
                    .map_err(|e| format!("{id}'s vault records are damaged: {e}"))?;
                if f.version != VERSION || f.id != id {
                    return Err(format!("{id}'s vault records are not its own"));
                }
                Ok(f)
            }
        }
    }

    fn save(&self, f: &PackageFile) -> Result<(), String> {
        let dir = self.package_dir(&f.id);
        private_dir(&dir)?;
        write(
            &dir.join("index.json"),
            &serde_json::to_vec(f).map_err(|e| e.to_string())?,
        )
    }

    /// Package id's open journals, as describe names them.
    pub fn open_journals(&self, id: &str) -> Vec<Value> {
        let _st = self.lock();
        self.load(id)
            .map(|f| {
                f.journals
                    .iter()
                    .filter(|j| j.outcome.is_none())
                    .map(|j| {
                        json!({"id": j.id, "op": j.op, "generation": j.generation,
                            "began": j.began, "intent": j.intent})
                    })
                    .collect()
            })
            .unwrap_or_default()
    }

    /// Package id's journals, open and recently ended, for its status.
    pub fn journals(&self, id: &str) -> Vec<JournalView> {
        let _st = self.lock();
        self.load(id)
            .map(|f| {
                f.journals
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
                    .collect()
            })
            .unwrap_or_default()
    }

    /// A `vault` frame of tool (a release of its package), sent for a host call of op it serves
    /// (describing: the describe it is answering, when op is empty). Gives the answer's output.
    pub fn request(
        &self,
        tool: &Holder,
        op: &str,
        describing: bool,
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
        let _st = self.lock();
        let mut f = self.load(&tool.id).map_err(unavailable)?;
        let out = match req["op"].as_str().unwrap_or_default() {
            "backup.put" => {
                let label = req["label"].as_str().unwrap_or_default();
                if label.chars().count() > MAX_LABEL || !printable(label) {
                    return Err(bad(&format!(
                        "a backup's label is at most {MAX_LABEL} printable characters"
                    )));
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
                let dir = self.package_dir(&tool.id);
                private_dir(&dir).map_err(unavailable)?;
                write(&dir.join(&b.id), &data).map_err(unavailable)?;
                f.backups.push(b.clone());
                if let Err(e) = self.save(&f) {
                    let _ = fs::remove_file(dir.join(&b.id));
                    return Err(unavailable(e));
                }
                json!({"backup": b.id})
            }
            "backup.get" => {
                let id = id_of("b", "backup")?;
                let b = f.backups.iter().find(|b| b.id == id).ok_or_else(|| {
                    refused("not_found", format!("{} has no backup {id}", tool.id))
                })?;
                let data = read_private(&self.package_dir(&tool.id).join(&b.id))
                    .map_err(unavailable)?
                    .ok_or_else(|| refused("not_found", format!("backup {id} is gone")))?;
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
                        format!("{} has no backup {id}", tool.id),
                    ));
                }
                f.backups.retain(|b| b.id != id);
                self.save(&f).map_err(unavailable)?;
                let _ = fs::remove_file(self.package_dir(&tool.id).join(&id));
                json!({})
            }
            "journal.begin" => {
                if describing {
                    return Err(bad("a journal is begun in a call, not in describe"));
                }
                if !req["intent"].is_object()
                    || serde_json::to_vec(&req["intent"]).map_or(usize::MAX, |b| b.len())
                        > MAX_INTENT
                {
                    return Err(bad(&format!(
                        "intent is a JSON object of at most {MAX_INTENT} bytes"
                    )));
                }
                if let Some(j) = f.journals.iter().find(|j| j.outcome.is_none()) {
                    return Err(refused(
                        "unavailable",
                        format!("journal {} is still open: end it first", j.id),
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
                self.save(&f).map_err(unavailable)?;
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
                let j = f.journals.iter_mut().find(|j| j.id == id).ok_or_else(|| {
                    refused("not_found", format!("{} has no journal {id}", tool.id))
                })?;
                match j.outcome.as_deref() {
                    Some(o) if o == outcome => {
                        return Ok(json!({"journal": id, "outcome": outcome, "again": true}))
                    }
                    Some(o) => {
                        return Err(refused(
                            "invalid_request",
                            format!("journal {id} ended as {o} already"),
                        ))
                    }
                    None => {}
                }
                j.outcome = Some(outcome.into());
                j.ended = Some(now());
                j.reconciled = describing;
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
                self.save(&f).map_err(unavailable)?;
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
    fn journals(&self, id: &str) -> Vec<Value> {
        self.open_journals(id)
    }

    fn frame(
        &self,
        tool: &crate::program::Hop,
        op: &str,
        describing: bool,
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
