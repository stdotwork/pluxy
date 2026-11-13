//! Linux native effect review. Native callers provide verified provenance and recipes;
//! none of these types is deserialized from a webview. This module never launches programs.
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
use std::fs::{self, File, OpenOptions};
use std::io::{Read, Write};
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

const PAGE_PENDING_LIMIT: usize = 16;
const TRUSTED_PENDING_LIMIT: usize = 64;
const SETTINGS_VERSION: u32 = 1;
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

/// The SHA-256 of executable file f, whose metadata m was read before: refused if it changed.
fn hash_file(f: &mut File, m: &Metadata) -> Result<String, String> {
    let mut hash = Sha256::new();
    let mut b = [0u8; 65536];
    loop {
        let n = f.read(&mut b).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        hash.update(&b[..n]);
    }
    if Metadata::read(f)? != *m {
        return Err("executable changed while hashing".into());
    }
    Ok(format!("{:x}", hash.finalize()))
}

/// A launch's executable, hashed without the confirmations' lock: `Confirmations::warm` keeps it
/// in the cache `prepare` checks, so preparing it then hashes nothing more under the lock.
pub struct Hashed(PathBuf, Metadata, String);

impl Hashed {
    pub fn of(executable: &Path) -> Result<Hashed, String> {
        let mut e = OpenTarget::open(executable)?;
        if !e.file.metadata().map_err(|e| e.to_string())?.is_file() {
            return Err("executable is not a regular file".into());
        }
        let m = Metadata::read(&e.file)?;
        if m.mode & 0o111 == 0 {
            return Err("file is not executable".into());
        }
        let h = hash_file(&mut e.file, &m)?;
        Ok(Hashed(e.identity.canonical, m, h))
    }
}
impl OpenTarget {
    fn open(path: &Path) -> Result<Self, String> {
        if !path.is_absolute() {
            return Err("review requires an absolute path".into());
        }
        if path.to_str().is_none() {
            return Err("review requires UTF-8 paths".into());
        }
        let canonical = fs::canonicalize(path).map_err(|e| e.to_string())?;
        if canonical.to_str().is_none() {
            return Err("resolved review target requires a UTF-8 path".into());
        }
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NONBLOCK | libc::O_NOCTTY)
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
// Supersede an earlier approval for this launch recipe even after executable metadata,
// environment policy or terminal-adapter revision changes; those still require fresh review.
fn same_launch_recipe(a: &ExactAction, b: &ExactAction) -> bool {
    a.chain == b.chain
        && a.executor == b.executor
        && match (&a.launch, &b.launch) {
            (Some(a), Some(b)) => {
                a.executable.selected == b.executable.selected
                    && a.executable.canonical == b.executable.canonical
                    && a.argv == b.argv
                    && a.cwd == b.cwd
            }
            _ => false,
        }
}
/// Set only by native entry points, never deserialized from page data.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(dead_code)] // Tray and native UI launcher handlers are added by the launcher slice.
enum Origin {
    Main,
    Tray,
    NativeUi,
    Page,
    Import,
}
impl Origin {
    fn untrusted(self) -> bool {
        matches!(self, Self::Page | Self::Import)
    }
}

/// Immutable native preparation, including open identities. Never returned to JavaScript.
pub struct Prepared {
    origin: Origin,
    /// The app's own action (prepare_app): no package, file or launch to show, so its review is
    /// its description alone, and no launch notice follows it.
    own: bool,
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
        escape_format_characters(&format!(
            "Requester: {}\nAction: {}",
            self.exact.chain[0].id, self.exact.what
        ))
    }
    pub fn review(&self) -> Result<String, String> {
        if self.own {
            return Ok(self
                .exact
                .what
                .split('\n')
                .map(escape_format_characters)
                .collect::<Vec<_>>()
                .join("\n"));
        }
        // JSON quoting makes argv boundaries, newlines and control characters unambiguous.
        let mut s = review_text(&self.exact)?;
        if self.exact.native_tool {
            s.insert_str(
                0,
                "This native tool has full access to your user account.

",
            );
        }
        Ok(s)
    }
}
pub(crate) fn escape_format_characters(text: &str) -> String {
    use gtk::glib::Unichar;
    let mut out = String::new();
    for ch in text.chars() {
        if ch.is_control()
            || matches!(ch, '\u{2028}' | '\u{2029}')
            || ch.unicode_type() == gtk::glib::UnicodeType::Format
        {
            for unit in ch.encode_utf16(&mut [0; 2]).iter() {
                out.push_str(&format!("\\u{unit:04x}"));
            }
        } else {
            out.push(ch);
        }
    }
    out
}
fn review_text(exact: &ExactAction) -> Result<String, String> {
    serde_json::to_string_pretty(exact)
        .map(|s| {
            s.split('\n')
                .map(escape_format_characters)
                .collect::<Vec<_>>()
                .join("\n")
        })
        .map_err(|e| format!("cannot render native review: {e}"))
}
/// A launcher should execute this opened handle with fexecve where practical and use the opened
/// cwd. Wrapper scripts check the script only, not the interpreter or transitively loaded code.
pub struct Authorized {
    pub prepared: Prepared,
}
impl Authorized {
    /// Opened with O_CLOEXEC. In the child, clear FD_CLOEXEC before fexecve for #! scripts
    /// (otherwise Linux returns ENOENT). The interpreter sees /dev/fd/N as the script path.
    /// Only the script is checked; its interpreter and loaded code are outside this identity.
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
// No general constructor: tray/native-UI handlers in this module (and native-test fixtures)
// create this only in their native event callbacks. DOM and IPC paths cannot create one.
pub struct Gesture {
    created: Instant,
}
#[derive(Serialize, Deserialize)]
struct SettingsFile {
    version: u32,
    approvals: Vec<Approval>,
}
fn private_file(file: &File) -> Result<(), String> {
    let m = file.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() || m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o022 != 0 {
        return Err(
            "settings must be a regular file owned by this user and not group/world-writable"
                .into(),
        );
    }
    Ok(())
}
fn refuse_settings_symlink(path: &Path) -> Result<(), String> {
    match fs::symlink_metadata(path) {
        Ok(m) if m.is_symlink() => Err("symlinked settings refused".into()),
        Ok(_) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e.to_string()),
    }
}
fn secure_parent(path: &Path, create: bool) -> Result<(), String> {
    let dir = path.parent().ok_or("settings has no parent")?;
    if create {
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(dir)
            .map_err(|e| e.to_string())?;
    }
    let f = match OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_NOCTTY)
        .open(dir)
    {
        Ok(f) => f,
        Err(e) if !create && e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(format!("settings folder refused: {e}")),
    };
    let m = f.metadata().map_err(|e| e.to_string())?;
    if m.uid() != unsafe { libc::geteuid() } {
        return Err("settings folder is not owned by this user".into());
    }
    if m.mode() & 0o022 != 0 {
        f.set_permissions(fs::Permissions::from_mode(0o700))
            .map_err(|e| e.to_string())?;
    }
    Ok(())
}
fn load_settings(path: &Path) -> Result<Vec<Approval>, String> {
    secure_parent(path, false)?;
    refuse_settings_symlink(path)?;
    let mut f = match OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_NOCTTY)
        .open(path)
    {
        Ok(f) => f,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(e) => return Err(e.to_string()),
    };
    private_file(&f)?;
    let mut b = Vec::new();
    f.read_to_end(&mut b).map_err(|e| e.to_string())?;
    let settings: SettingsFile =
        serde_json::from_slice(&b).map_err(|e| format!("damaged or unsupported settings: {e}"))?;
    if settings.version != SETTINGS_VERSION {
        return Err("unsupported settings version".into());
    }
    Ok(settings.approvals)
}

pub struct Confirmations {
    settings: PathBuf,
    saving_warnings: HashMap<String, String>,
    pub(crate) startup_warning: Option<String>,
    persistence_blocked: bool,
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
        let mut warning = None;
        let mut persistence_blocked = false;
        let approvals = match load_settings(&settings) {
            Ok(a) => a,
            Err(e) => {
                let safe = secure_parent(&settings, false).is_ok()
                    && OpenOptions::new()
                        .read(true)
                        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_NOCTTY)
                        .open(&settings)
                        .ok()
                        .is_some_and(|f| private_file(&f).is_ok());
                let preservation = if safe {
                    let time = std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap_or_default()
                        .as_nanos();
                    let backup = settings.with_file_name(format!(
                        "{}.corrupt-{time}",
                        settings.file_name().unwrap_or_default().to_string_lossy()
                    ));
                    match fs::rename(&settings, &backup) {
                        Ok(()) => format!("Moved aside to {}.", backup.display()),
                        Err(error) => {
                            persistence_blocked = true;
                            format!(
                                "Could not preserve settings: {error}. Remembering is disabled."
                            )
                        }
                    }
                } else {
                    persistence_blocked = true;
                    "Unsafe or unreadable settings refused. Remembering is disabled.".into()
                };
                let message = escape_format_characters(&format!("Remembered launches were not loaded: {e}. {preservation} No remembered approvals are active."));
                eprintln!("pluxy-app: {message}");
                warning = Some(message);
                Vec::new()
            }
        };
        Ok(Self {
            settings,
            startup_warning: warning,
            saving_warnings: HashMap::new(),
            persistence_blocked,
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
        if self.persistence_blocked {
            return Err("unsafe settings refused; remembering is disabled".into());
        }
        let dir = self.settings.parent().ok_or("settings has no parent")?;
        secure_parent(&self.settings, true)?;
        refuse_settings_symlink(&self.settings)?;
        match OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_NOCTTY)
            .open(&self.settings)
        {
            Ok(f) => private_file(&f)?,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(format!("settings refused: {e}")),
        }
        let tmp = dir.join(format!(".confirmation-{}", crate::managed::random_key()));
        let result = (|| {
            let mut f = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&tmp)
                .map_err(|e| e.to_string())?;
            f.write_all(
                &serde_json::to_vec_pretty(&SettingsFile {
                    version: SETTINGS_VERSION,
                    approvals: approvals.to_vec(),
                })
                .map_err(|e| e.to_string())?,
            )
            .map_err(|e| e.to_string())?;
            f.sync_all().map_err(|e| e.to_string())?;
            fs::rename(&tmp, &self.settings).map_err(|e| e.to_string())
        })();
        let _ = fs::remove_file(tmp);
        result
    }
    /// Page routing fixes origin here; it cannot request a trusted origin or gesture.
    pub fn prepare_page(&mut self, action: Action) -> Result<Prepared, String> {
        self.prepare(action, Origin::Page)
    }
    pub fn prepare_import(&mut self, action: Action) -> Result<Prepared, String> {
        self.prepare(action, Origin::Import)
    }
    /// The app's own action, started in the app's window: no package, file or launch. The
    /// requester is the app itself; generation ties the review to the state it describes.
    pub(crate) fn prepare_app(
        &mut self,
        what: String,
        generation: u64,
    ) -> Result<Prepared, String> {
        let app = Package {
            id: "pluxy app".into(),
            source: "the app's window".into(),
            digest: String::new(),
            signer: String::new(),
            contract: "management API v0".into(),
            generation,
        };
        let mut p = self.prepare(
            Action {
                what,
                chain: vec![app.clone()],
                executor: app,
                native_tool: false,
                files: vec![],
                launch: None,
            },
            Origin::Main,
        )?;
        p.own = true;
        Ok(p)
    }

    /// For the main window's command handlers only, after their caller check, with an action
    /// native code built from verified records (enabling a native tool). Never page data.
    pub(crate) fn prepare_main(&mut self, action: Action) -> Result<Prepared, String> {
        self.prepare(action, Origin::Main)
    }
    // Trusted preparations are created only by native main/tray/UI handlers in this module.
    fn prepare(&mut self, action: Action, origin: Origin) -> Result<Prepared, String> {
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
                // shortcut: a hash not warmed first (`Hashed::of`) runs under the mutex on the
                // caller's thread; move preparation off the GTK thread when a real launcher lands.
                let h = hash_file(&mut e.file, &m)?;
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
            origin,
            own: false,
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
        self.saving_warnings
            .retain(|id, _| self.pending.contains_key(id));
        self.issued.retain(|_, t| t.expires > now);
    }
    /// Whether a confirmation for package could be queued now, from a page (`page`) or a trusted
    /// origin: the same rate and pending limits `queue` applies, checked before any preparation
    /// work (such as hashing an executable). `queue` checks them again.
    pub fn room(&mut self, page: bool, package: &str) -> Result<(), String> {
        self.prune();
        if page {
            if self
                .last_page
                .get(package)
                .is_some_and(|t| t.elapsed() < Duration::from_secs(1))
            {
                return Err("one launch confirmation attempt per second".into());
            }
            if self
                .pending
                .values()
                .any(|p| p.page && p.prepared.exact.chain[0].id == package)
            {
                return Err("a launch confirmation is already pending for this package".into());
            }
        }
        let limit = if page {
            PAGE_PENDING_LIMIT
        } else {
            TRUSTED_PENDING_LIMIT
        };
        if self.pending.values().filter(|p| p.page == page).count() >= limit {
            return Err("too many pending confirmations".into());
        }
        Ok(())
    }

    /// Keeps an executable hashed without the lock, for `prepare` to use while it is unchanged.
    pub fn warm(&mut self, h: Hashed) {
        self.cache.insert(h.0, (h.1, h.2));
    }

    /// Takes back pending confirmation id: its caller no longer waits for it (the call was
    /// cancelled). Its dialog closes as cancelled, and a Confirm racing with that confirms nothing.
    pub fn withdraw(&mut self, id: &str) {
        self.pending.remove(id);
        self.saving_warnings.remove(id);
    }

    /// Limits follow immutable native origin, never a page-supplied flag.
    pub fn queue(&mut self, prepared: Prepared) -> Result<String, String> {
        self.prune();
        prepared.check()?;
        let package = &prepared.exact.chain[0].id;
        let page = prepared.origin.untrusted();
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
        let limit = if page {
            PAGE_PENDING_LIMIT
        } else {
            TRUSTED_PENDING_LIMIT
        };
        if self.pending.values().filter(|p| p.page == page).count() >= limit {
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
        Ok((
            p.prepared.review()?,
            p.prepared.exact.launch.is_some() && !self.persistence_blocked,
        ))
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
            let approval_id = crate::managed::random_key();
            let mut approvals: Vec<_> = self
                .approvals
                .iter()
                .filter(|a| !same_launch_recipe(&a.exact, &p.prepared.exact))
                .cloned()
                .collect();
            approvals.push(Approval {
                id: approval_id.clone(),
                exact: p.prepared.exact.clone(),
            });
            let saved = self.save(&approvals);
            if let Err(e) = saved {
                let warning = escape_format_characters(&format!(
                    "Confirmed for this use only. Could not remember launch: {e}"
                ));
                eprintln!("pluxy-app: {warning}");
                self.saving_warnings.insert(id.to_string(), warning);
                None
            } else {
                self.approvals = approvals;
                self.issued.retain(|_, t| {
                    t.approval
                        .as_ref()
                        .is_none_or(|id| self.approvals.iter().any(|a| &a.id == id))
                });
                Some(approval_id)
            }
        } else {
            None
        };
        p.reviewed = true;
        self.pending.insert(id.into(), p);
        Ok(Some(self.issue(id, approval)))
    }
    pub(crate) fn saving_warning(&self, id: &str) -> Option<&str> {
        self.saving_warnings.get(id).map(String::as_str)
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
        if p.origin.untrusted()
            || gesture.created.elapsed() >= Duration::from_secs(5)
            || self.pending.values().filter(|p| !p.page).count() >= TRUSTED_PENDING_LIMIT
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
        self.saving_warnings.clear();
    }
}
#[path = "confirmation_ui.rs"]
mod ui;
pub use ui::page_start;
pub(crate) use ui::{confirm_app, notice as ui_notice, request, settings};

#[cfg(test)]
#[path = "confirmation_tests.rs"]
mod tests;

#[cfg(feature = "native-test")]
#[path = "confirmation_native.rs"]
pub(crate) mod native;
