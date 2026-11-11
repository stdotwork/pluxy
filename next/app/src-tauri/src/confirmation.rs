//! Linux native effect review. Native callers provide verified provenance and recipes;
//! none of these types is deserialized from a webview. This module never launches programs.
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::fs::{self, File, OpenOptions};
use std::io::{Read, Write};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

const LIFETIME: Duration = Duration::from_secs(60);
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Package {
    pub id: String,
    pub source: String,
    pub digest: String,
    pub signer: String,
    pub contract: String,
    pub generation: u64,
}
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SecretUse {
    /// Opaque reference, never its value.
    pub reference: String,
    pub recipient: String,
    pub purpose: String,
}
#[derive(Clone, Debug)]
pub struct Launch {
    pub executable: PathBuf,
    pub argv: Vec<String>,
    pub cwd: PathBuf,
    pub terminal_adapter: Package,
    pub environment_revision: String,
    /// Names and non-secret changed values only; no secret bytes.
    pub environment_review: Vec<(String, Option<String>)>,
    pub secret_uses: Vec<SecretUse>,
}
#[derive(Clone, Debug)]
pub struct Action {
    pub what: String,
    /// Original requester first, followed by every host-resolved hop.
    pub chain: Vec<Package>,
    pub executor: Package,
    pub native_tool: bool,
    pub files: Vec<PathBuf>,
    pub launch: Option<Launch>,
}
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct Metadata {
    dev: u64,
    ino: u64,
    size: u64,
    mtime: (i64, i64),
    ctime: (i64, i64),
    mode: u32,
}
impl Metadata {
    fn read(f: &File) -> Result<Self, String> {
        let m = f.metadata().map_err(|e| e.to_string())?;
        Ok(Self {
            dev: m.dev(),
            ino: m.ino(),
            size: m.size(),
            mtime: (m.mtime(), m.mtime_nsec()),
            ctime: (m.ctime(), m.ctime_nsec()),
            mode: m.mode(),
        })
    }
}
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct Target {
    selected: PathBuf,
    canonical: PathBuf,
    dev: u64,
    ino: u64,
}
struct OpenTarget {
    identity: Target,
    file: File,
}
impl OpenTarget {
    fn open(path: &Path) -> Result<Self, String> {
        if !path.is_absolute() {
            return Err("review requires an absolute path".into());
        }
        let canonical = fs::canonicalize(path).map_err(|e| e.to_string())?;
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NONBLOCK)
            .open(&canonical)
            .map_err(|e| e.to_string())?;
        let kind = file.metadata().map_err(|e| e.to_string())?;
        if !kind.is_file() && !kind.is_dir() {
            return Err("review target must be a regular file or directory".into());
        }
        let m = Metadata::read(&file)?;
        let t = Self {
            identity: Target {
                selected: path.into(),
                canonical,
                dev: m.dev,
                ino: m.ino,
            },
            file,
        };
        t.check()?;
        Ok(t)
    }
    fn check(&self) -> Result<(), String> {
        let canonical = fs::canonicalize(&self.identity.selected).map_err(|e| e.to_string())?;
        let m = fs::metadata(&canonical).map_err(|e| e.to_string())?;
        let held = self.file.metadata().map_err(|e| e.to_string())?;
        if canonical != self.identity.canonical
            || (m.dev(), m.ino()) != (self.identity.dev, self.identity.ino)
            || (held.dev(), held.ino()) != (m.dev(), m.ino())
        {
            return Err("selected target changed; prepare and review again".into());
        }
        Ok(())
    }
}
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct ExactLaunch {
    executable: Target,
    metadata: Metadata,
    sha256: String,
    argv: Vec<String>,
    cwd: Target,
    terminal_adapter: Package,
    environment_revision: String,
    environment_review: Vec<(String, Option<String>)>,
    secret_uses: Vec<SecretUse>,
}
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct ExactAction {
    what: String,
    chain: Vec<Package>,
    executor: Package,
    native_tool: bool,
    files: Vec<Target>,
    launch: Option<ExactLaunch>,
}
/// Immutable native preparation, including open identities. Never returned to JavaScript.
pub struct Prepared {
    exact: ExactAction,
    files: Vec<OpenTarget>,
    executable: Option<OpenTarget>,
    cwd: Option<OpenTarget>,
}
impl Prepared {
    fn check(&self) -> Result<(), String> {
        for f in &self.files {
            f.check()?;
        }
        if let (Some(e), Some(l)) = (&self.executable, &self.exact.launch) {
            e.check()?;
            if Metadata::read(&e.file)? != l.metadata {
                return Err("executable metadata changed; review again".into());
            }
        }
        if let Some(c) = &self.cwd {
            c.check()?;
        }
        Ok(())
    }
    pub fn argv(&self) -> Option<&[String]> {
        self.exact.launch.as_ref().map(|l| l.argv.as_slice())
    }
    pub fn environment_revision(&self) -> Option<&str> {
        self.exact
            .launch
            .as_ref()
            .map(|l| l.environment_revision.as_str())
    }
    pub(crate) fn notice(&self) -> String {
        format!(
            "Requester: {}\nAction: {}",
            self.exact.chain[0].id, self.exact.what
        )
    }
    pub fn review(&self) -> String {
        // JSON quoting makes argv boundaries, newlines and control characters unambiguous.
        let mut s = serde_json::to_string_pretty(&self.exact).expect("review JSON");
        if self.exact.native_tool {
            s.insert_str(
                0,
                "This native tool has full access to your user account.

",
            );
        }
        s
    }
}
/// A launcher should execute this opened handle with fexecve where practical and use the opened
/// cwd. Wrapper scripts check the script only, not the interpreter or transitively loaded code.
pub struct Authorized {
    pub prepared: Prepared,
}
impl Authorized {
    pub fn executable(&self) -> Option<&File> {
        self.prepared.executable.as_ref().map(|e| &e.file)
    }
    pub fn cwd(&self) -> Option<&File> {
        self.prepared.cwd.as_ref().map(|e| &e.file)
    }
}
struct Pending {
    prepared: Prepared,
    expires: Instant,
    page: bool,
    reviewed: bool,
}
struct Issued {
    action: String,
    expires: Instant,
    approval: Option<String>,
}
#[derive(Clone, Serialize, Deserialize)]
struct Approval {
    id: String,
    exact: ExactAction,
}
/// Only native event handlers construct this. There is no JS gesture flag constructor.
pub struct Gesture {
    created: Instant,
}
impl Gesture {
    // Real launcher gesture handlers are added with the launcher slice.
    #[cfg_attr(not(any(test, feature = "native-test")), allow(dead_code))]
    pub(crate) fn native_event() -> Self {
        Self {
            created: Instant::now(),
        }
    }
}

pub struct Confirmations {
    settings: PathBuf,
    approvals: Vec<Approval>,
    cache: HashMap<PathBuf, (Metadata, String)>,
    pending: HashMap<String, Pending>,
    issued: HashMap<String, Issued>,
    last_page: HashMap<String, Instant>,
    pub(crate) showing: std::collections::HashSet<String>,
    pub(crate) results: HashMap<String, Result<Option<String>, String>>,
    #[cfg(test)]
    hashes: usize,
}
impl Confirmations {
    pub fn open(settings: PathBuf) -> Result<Self, String> {
        let approvals = match fs::read(&settings) {
            Ok(b) => {
                serde_json::from_slice(&b).map_err(|e| format!("confirmation settings: {e}"))?
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Vec::new(),
            Err(e) => return Err(e.to_string()),
        };
        Ok(Self {
            settings,
            approvals,
            cache: HashMap::new(),
            pending: HashMap::new(),
            issued: HashMap::new(),
            last_page: HashMap::new(),
            showing: Default::default(),
            results: HashMap::new(),
            #[cfg(test)]
            hashes: 0,
        })
    }
    fn save(&self, approvals: &[Approval]) -> Result<(), String> {
        let dir = self.settings.parent().ok_or("settings has no parent")?;
        fs::create_dir_all(dir).map_err(|e| e.to_string())?;
        let tmp = dir.join(format!(".confirmation-{}", crate::managed::random_key()));
        let result = (|| {
            let mut f = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&tmp)
                .map_err(|e| e.to_string())?;
            f.write_all(&serde_json::to_vec_pretty(approvals).map_err(|e| e.to_string())?)
                .map_err(|e| e.to_string())?;
            f.sync_all().map_err(|e| e.to_string())?;
            fs::rename(&tmp, &self.settings).map_err(|e| e.to_string())
        })();
        let _ = fs::remove_file(tmp);
        result
    }
    pub fn prepare(&mut self, action: Action) -> Result<Prepared, String> {
        if action.chain.is_empty() {
            return Err("action needs the whole requester chain".into());
        }
        let files = action
            .files
            .iter()
            .map(|p| OpenTarget::open(p))
            .collect::<Result<Vec<_>, _>>()?;
        let mut executable = None;
        let mut cwd = None;
        let launch = if let Some(l) = action.launch {
            let mut e = OpenTarget::open(&l.executable)?;
            if !e.file.metadata().map_err(|e| e.to_string())?.is_file() {
                return Err("executable is not a regular file".into());
            }
            let m = Metadata::read(&e.file)?;
            if m.mode & 0o111 == 0 {
                return Err("file is not executable".into());
            }
            let hash = if let Some((_, h)) = self
                .cache
                .get(&e.identity.canonical)
                .filter(|(cached, _)| *cached == m)
            {
                h.clone()
            } else {
                let mut hash = Sha256::new();
                let mut b = [0u8; 65536];
                loop {
                    let n = e.file.read(&mut b).map_err(|e| e.to_string())?;
                    if n == 0 {
                        break;
                    }
                    hash.update(&b[..n]);
                }
                if Metadata::read(&e.file)? != m {
                    return Err("executable changed while hashing".into());
                }
                let h = format!("{:x}", hash.finalize());
                self.cache
                    .insert(e.identity.canonical.clone(), (m.clone(), h.clone()));
                #[cfg(test)]
                {
                    self.hashes += 1;
                }
                h
            };
            let c = OpenTarget::open(&l.cwd)?;
            if !c.file.metadata().map_err(|e| e.to_string())?.is_dir() {
                return Err("cwd is not a directory".into());
            }
            let exact = ExactLaunch {
                executable: e.identity.clone(),
                metadata: m,
                sha256: hash,
                argv: l.argv,
                cwd: c.identity.clone(),
                terminal_adapter: l.terminal_adapter,
                environment_revision: l.environment_revision,
                environment_review: l.environment_review,
                secret_uses: l.secret_uses,
            };
            executable = Some(e);
            cwd = Some(c);
            Some(exact)
        } else {
            None
        };
        let p = Prepared {
            exact: ExactAction {
                what: action.what,
                chain: action.chain,
                executor: action.executor,
                native_tool: action.native_tool,
                files: files.iter().map(|f| f.identity.clone()).collect(),
                launch,
            },
            files,
            executable,
            cwd,
        };
        p.check()?;
        Ok(p)
    }
    fn prune(&mut self) {
        let now = Instant::now();
        self.pending.retain(|_, p| p.expires > now);
        self.issued.retain(|_, t| t.expires > now);
    }
    /// Native routing selects page ingress; never take this flag from a page message.
    pub fn queue(&mut self, prepared: Prepared, page: bool) -> Result<String, String> {
        self.prune();
        prepared.check()?;
        let package = &prepared.exact.chain[0].id;
        if page {
            if self
                .last_page
                .get(package)
                .is_some_and(|t| t.elapsed() < Duration::from_secs(1))
            {
                return Err("one launch confirmation attempt per second".into());
            }
            self.last_page
                .retain(|_, t| t.elapsed() < Duration::from_secs(1));
            self.last_page.insert(package.clone(), Instant::now());
            if self
                .pending
                .values()
                .any(|p| p.page && p.prepared.exact.chain[0].id == *package)
            {
                return Err("a launch confirmation is already pending for this package".into());
            }
        }
        if self.pending.len() >= 64 {
            return Err("too many pending confirmations".into());
        }
        let id = crate::managed::random_key();
        self.pending.insert(
            id.clone(),
            Pending {
                prepared,
                expires: Instant::now() + LIFETIME,
                page,
                reviewed: false,
            },
        );
        Ok(id)
    }
    pub(crate) fn review(&mut self, id: &str) -> Result<(String, bool), String> {
        self.prune();
        let p = self.pending.get(id).ok_or("no fresh prepared action")?;
        if p.reviewed {
            return Err("action was already confirmed".into());
        }
        p.prepared.check()?;
        Ok((p.prepared.review(), p.prepared.exact.launch.is_some()))
    }
    /// Only the GTK response handler calls this; there is no webview approval-result command.
    fn finish(
        &mut self,
        id: &str,
        confirmed: bool,
        remember: bool,
    ) -> Result<Option<String>, String> {
        self.prune();
        let mut p = self
            .pending
            .remove(id)
            .ok_or("confirmation expired or revoked")?;
        if p.reviewed {
            return Err("action was already confirmed".into());
        }
        if !confirmed {
            return Ok(None);
        }
        p.prepared.check()?;
        let approval = if remember && p.prepared.exact.launch.is_some() {
            let id = crate::managed::random_key();
            let mut approvals: Vec<_> = self
                .approvals
                .iter()
                .filter(|a| a.exact != p.prepared.exact)
                .cloned()
                .collect();
            approvals.push(Approval {
                id: id.clone(),
                exact: p.prepared.exact.clone(),
            });
            self.save(&approvals)?;
            self.approvals = approvals;
            self.issued.retain(|_, t| {
                t.approval
                    .as_ref()
                    .is_none_or(|id| self.approvals.iter().any(|a| &a.id == id))
            });
            Some(id)
        } else {
            None
        };
        p.reviewed = true;
        self.pending.insert(id.into(), p);
        Ok(Some(self.issue(id, approval)))
    }
    fn issue(&mut self, action: &str, approval: Option<String>) -> String {
        let token = crate::managed::random_key();
        self.issued.insert(
            token.clone(),
            Issued {
                action: action.into(),
                expires: Instant::now() + LIFETIME,
                approval,
            },
        );
        token
    }
    pub fn remembered(&mut self, p: Prepared, gesture: Gesture) -> Result<String, Box<Prepared>> {
        self.prune();
        if gesture.created.elapsed() >= Duration::from_secs(5)
            || self.pending.len() >= 64
            || p.check().is_err()
        {
            return Err(Box::new(p));
        }
        let Some(approval) = self
            .approvals
            .iter()
            .find(|a| a.exact == p.exact)
            .map(|a| a.id.clone())
        else {
            return Err(Box::new(p));
        };
        let id = crate::managed::random_key();
        self.pending.insert(
            id.clone(),
            Pending {
                prepared: p,
                expires: Instant::now() + LIFETIME,
                page: false,
                reviewed: true,
            },
        );
        Ok(self.issue(&id, Some(approval)))
    }
    /// Native consumers supply both token and preparation ID. Even a mismatch spends the token.
    /// Host record changes must call invalidate before any further consumption.
    pub fn consume(&mut self, id: &str, token: &str) -> Result<Authorized, String> {
        self.prune();
        let t = self
            .issued
            .remove(token)
            .ok_or("no fresh single-use token")?;
        if t.action != id {
            return Err("token belongs to another action".into());
        }
        if t.approval
            .as_ref()
            .is_some_and(|id| !self.approvals.iter().any(|a| &a.id == id))
        {
            return Err("approval revoked".into());
        }
        let p = self.pending.remove(id).ok_or("action expired or revoked")?;
        p.prepared.check()?;
        Ok(Authorized {
            prepared: p.prepared,
        })
    }
    /// Completion is available to native callers only and is removed when read.
    pub fn take_result(&mut self, id: &str) -> Option<Result<Option<String>, String>> {
        self.results.remove(id)
    }
    pub fn token_action(&self, token: &str) -> Option<&str> {
        self.issued.get(token).map(|t| t.action.as_str())
    }
    pub fn revoke(&mut self, approval: &str) -> Result<(), String> {
        let approvals: Vec<_> = self
            .approvals
            .iter()
            .filter(|a| a.id != approval)
            .cloned()
            .collect();
        self.save(&approvals)?;
        self.approvals = approvals;
        self.issued
            .retain(|_, t| t.approval.as_deref() != Some(approval));
        Ok(())
    }
    /// On package/source/contract/profile/secret-policy change; cancels in-flight reviews too.
    pub fn invalidate(&mut self) {
        self.pending.clear();
        self.issued.clear();
        self.results.clear();
    }
}
#[path = "confirmation_ui.rs"]
mod ui;
pub use ui::page_start;
pub(crate) use ui::{request, settings};

#[cfg(test)]
#[path = "confirmation_tests.rs"]
mod tests;

#[cfg(feature = "native-test")]
#[path = "confirmation_native.rs"]
pub(crate) mod native;
