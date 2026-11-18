//! Native effect review, on Linux, Windows and macOS. Native callers provide verified provenance
//! and recipes; none of these types is deserialized from a webview. This module never launches
//! programs.
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::HashMap;
#[cfg(unix)]
use std::fs::OpenOptions;
use std::fs::{self, File};
use std::io::{Read, Write};
#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::Manager;

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
    /// The program a terminal runs (one of argv): opened, hashed and bound as the executable is,
    /// so its replacement (a self-update) asks again too.
    pub program: Option<PathBuf>,
    /// The file the launch opens (one of argv) and the folder it must be inside: both opened and
    /// resolved, shown and bound, so a link to or a replacement by another file is refused.
    pub opens: Option<(PathBuf, PathBuf)>,
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
    #[cfg(unix)]
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
    /// Volume and file index for dev and ino, last-write and change times (in 100 ns units) and
    /// the file's attributes for mode (`confirmation_windows.rs`).
    #[cfg(windows)]
    fn read(f: &File) -> Result<Self, String> {
        let i = windows_files::info(f)?;
        Ok(Self {
            dev: i.dev,
            ino: i.ino,
            size: i.size,
            mtime: (i.mtime, 0),
            ctime: (i.ctime, 0),
            mode: i.attributes,
        })
    }
}

/// Whether the opened regular file at path with metadata m is a program: on Unix, any execute
/// bit; on Windows, which has none, its extension (`confirmation_windows.rs`).
fn runnable(path: &Path, m: &Metadata) -> bool {
    #[cfg(unix)]
    {
        let _ = path;
        m.mode & 0o111 != 0
    }
    #[cfg(windows)]
    {
        let _ = m;
        windows_files::executable(path)
    }
}

/// path's device and inode (volume and file index on Windows), following links.
fn identity(path: &Path) -> Result<(u64, u64), String> {
    #[cfg(unix)]
    {
        let m = fs::metadata(path).map_err(|e| e.to_string())?;
        Ok((m.dev(), m.ino()))
    }
    #[cfg(windows)]
    {
        let m = Metadata::read(&windows_files::open_any(path).map_err(|e| e.to_string())?)?;
        Ok((m.dev, m.ino))
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
        if !runnable(&e.identity.canonical, &m) {
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
        #[cfg(unix)]
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NONBLOCK | libc::O_NOCTTY)
            .open(&canonical)
            .map_err(|e| e.to_string())?;
        #[cfg(windows)]
        let file = windows_files::open_any(&canonical).map_err(|e| e.to_string())?;
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
        let now = identity(&canonical)?;
        let held = Metadata::read(&self.file)?;
        if canonical != self.identity.canonical
            || now != (self.identity.dev, self.identity.ino)
            || (held.dev, held.ino) != now
        {
            return Err("selected target changed; prepare and review again".into());
        }
        Ok(())
    }
}
/// A launch's program other than its executable (the agent a terminal runs), as confirmed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct ExactProgram {
    file: Target,
    metadata: Metadata,
    sha256: String,
}
/// The file a launch opens, its metadata and content type, and the folder it is inside, as
/// confirmed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct ExactOpen {
    file: Target,
    metadata: Metadata,
    content_type: String,
    within: Target,
}
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct ExactLaunch {
    executable: Target,
    metadata: Metadata,
    sha256: String,
    argv: Vec<String>,
    cwd: Target,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    program: Option<ExactProgram>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    opens: Option<ExactOpen>,
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
/// Set only by native entry points, never deserialized from page data. Page and Import never use
/// a remembered approval.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[allow(dead_code)] // NativeUi: native-test fixtures only so far.
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
    program: Option<OpenTarget>,
    /// The file a launch opens and its folder, held open.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    opens: Option<(OpenTarget, OpenTarget)>,
}
impl Prepared {
    fn check(&self) -> Result<(), String> {
        for f in &self.files {
            f.check()?;
        }
        #[cfg(target_os = "linux")]
        if let (Some((file, within)), Some(x)) = (
            &self.opens,
            self.exact.launch.as_ref().and_then(|l| l.opens.as_ref()),
        ) {
            open_rules(file, within, x)?;
        }
        if let (Some(e), Some(l)) = (&self.executable, &self.exact.launch) {
            e.check()?;
            if Metadata::read(&e.file)? != l.metadata {
                return Err("executable metadata changed; review again".into());
            }
        }
        if let (Some(p), Some(x)) = (
            &self.program,
            self.exact.launch.as_ref().and_then(|l| l.program.as_ref()),
        ) {
            p.check()?;
            if Metadata::read(&p.file)? != x.metadata {
                return Err("program metadata changed; review again".into());
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
    /// The Remember checkbox's text (with its mnemonic): what a remembered approval is bound to,
    /// naming the secrets the launch uses, which a later launch must use the same.
    pub(crate) fn remember_text(&self) -> String {
        let secrets: Vec<String> = self
            .exact
            .launch
            .iter()
            .flat_map(|l| &l.secret_uses)
            .map(|s| format!("{} for {}'s {}", s.reference, s.recipient, s.purpose))
            .collect();
        if secrets.is_empty() {
            return "_Remember this exact executable, argv and cwd".into();
        }
        format!(
            "_Remember this exact executable, argv, cwd and secrets: {}",
            secrets.join("; ").replace('_', "__")
        )
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
        // What a launch that opens a file opens, with which program, from the resolved paths.
        if let Some(l) = &self.exact.launch {
            if let Some(o) = &l.opens {
                let line = escape_format_characters(&format!(
                    "Opens the file {} ({}, inside {}) with {}.",
                    o.file.canonical.display(),
                    o.content_type,
                    o.within.canonical.display(),
                    l.executable.canonical.display()
                ));
                s.insert_str(0, &format!("{line}\n\n"));
            }
        }
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
/// The content types a launch may open: plain text and text data, which the desktop opens with a
/// text editor or viewer. The desktop picks the app by type, so this is the limit that holds: an
/// HTML, SVG, script or archive file, even named `.jsonl`, could open in an app that runs it.
#[cfg(target_os = "linux")]
const OPEN_TYPES: &[&str] = &[
    "text/plain",
    "application/json",
    "application/x-ndjson",
    "application/jsonl",
    "text/markdown",
    "text/x-log",
];
/// The openers a launch that opens a file may run, with the arguments before the file (the
/// execution program's `open` launch), and the folders they are taken from.
#[cfg(target_os = "linux")]
const OPENERS: &[(&str, &[&str])] = &[("xdg-open", &[]), ("gio", &["open"])];
#[cfg(target_os = "linux")]
const OPENER_FOLDERS: &[&str] = &[
    "/usr/local/bin",
    "/usr/bin",
    "/bin",
    "/run/current-system/sw/bin",
];
/// How many bytes of a file its content type is guessed from: more than GIO reads (its sniff
/// length is the longest shared-mime-info magic), so the guess sees at least what the opener's
/// does.
#[cfg(target_os = "linux")]
const SNIFF: usize = 65536;

/// The content type of the file held open in f, as GIO guesses it from its name and first bytes:
/// the same shared-mime-info rules `gio open` and xdg-open's helpers use.
#[cfg(target_os = "linux")]
fn content_type(f: &OpenTarget) -> Result<String, String> {
    use std::os::unix::fs::FileExt;
    let mut b = vec![0u8; SNIFF];
    let mut n = 0;
    while n < b.len() {
        match f.file.read_at(&mut b[n..], n as u64) {
            Ok(0) => break,
            Ok(k) => n += k,
            Err(e) => return Err(e.to_string()),
        }
    }
    let name = f.identity.canonical.file_name().map(PathBuf::from);
    Ok(gtk::gio::content_type_guess(name, &b[..n]).0.to_string())
}
/// Refused unless m (a file or folder's metadata) is the effective user's and no one else may
/// write it: another user could otherwise replace what was confirmed.
#[cfg(target_os = "linux")]
fn own(m: &fs::Metadata, what: &str) -> Result<(), String> {
    if m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o022 != 0 {
        return Err(format!("{what} must be your own, writable by no one else"));
    }
    Ok(())
}
/// Refused unless every folder from file's parent up to and including within (both resolved, file
/// inside within) is a real folder (not a link, which may have appeared since the path was
/// resolved) that is the effective user's and writable by neither group nor others: another user
/// could otherwise rename a different file into place between the last check and the opener's
/// read.
#[cfg(target_os = "linux")]
fn folders_own(file: &Path, within: &Path) -> Result<(), String> {
    for d in file.ancestors().skip(1) {
        let m = fs::symlink_metadata(d).map_err(|e| format!("{}: {e}", d.display()))?;
        if !m.is_dir() {
            return Err(format!("{} is no longer a folder", d.display()));
        }
        own(
            &m,
            &format!("the folder {} of the file a launch opens", d.display()),
        )?;
        if d == within {
            return Ok(());
        }
    }
    Err("the file a launch opens must be inside its within folder".into())
}
/// The rules for a file a launch opens, held in f, inside folder w, both held open: a regular file
/// with no execute bit (a desktop may run an executable it is asked to open), not a `.desktop`
/// file, inside w, the file and w and every folder between them the effective user's and writable
/// by no one else, of a plain-text type. Its content type.
#[cfg(target_os = "linux")]
fn open_file_rules(f: &OpenTarget, w: &OpenTarget) -> Result<String, String> {
    let m = f.file.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() {
        return Err("the file a launch opens must be a regular file".into());
    }
    if m.mode() & 0o111 != 0 {
        return Err("the file a launch opens must not be executable".into());
    }
    if !f.identity.canonical.starts_with(&w.identity.canonical) {
        return Err("the file a launch opens must be inside its within folder".into());
    }
    let desktop = |p: &Path| {
        p.extension()
            .is_some_and(|e| e.eq_ignore_ascii_case("desktop"))
    };
    if desktop(&f.identity.selected) || desktop(&f.identity.canonical) {
        return Err("a launch never opens a .desktop file".into());
    }
    own(&m, "the file a launch opens")?;
    own(
        &w.file.metadata().map_err(|e| e.to_string())?,
        "a launch's within folder",
    )?;
    folders_own(&f.identity.canonical, &w.identity.canonical)?;
    let t = content_type(f)?;
    if !OPEN_TYPES.contains(&t.as_str()) {
        return Err(format!(
            "the file a launch opens must be plain text, not {t}"
        ));
    }
    Ok(t)
}
/// The home-folder rule for a launch's within folder, given its resolved path and the home folder
/// as resolved (`None` when it could not be): the folder is neither `/`, the home folder nor a
/// folder above it. With no home folder to check against, the rule refuses rather than passes.
#[cfg(target_os = "linux")]
fn within_home_rule(within: &Path, home: Option<&Path>) -> Result<(), String> {
    let Some(home) = home else {
        return Err(
            "the home folder could not be found, so a launch's within folder cannot be checked against it"
                .into(),
        );
    };
    if within == Path::new("/") || home.starts_with(within) {
        return Err(
            "a launch's within folder cannot be /, the home folder or a folder above it".into(),
        );
    }
    Ok(())
}
/// A launch's file to open and the folder it must be inside, both opened and resolved, each named
/// by its resolved path: the folder neither `/`, the home folder nor a folder above it (refused
/// too when the home folder cannot be found), the file as `open_file_rules` says.
#[cfg(target_os = "linux")]
fn open_within(file: &Path, within: &Path) -> Result<(OpenTarget, OpenTarget, String), String> {
    let w = OpenTarget::open(within)?;
    if !w.file.metadata().map_err(|e| e.to_string())?.is_dir() {
        return Err("a launch's within folder is not a folder".into());
    }
    let home = std::env::home_dir().and_then(|h| fs::canonicalize(h).ok());
    within_home_rule(&w.identity.canonical, home.as_deref())?;
    let f = OpenTarget::open(file)?;
    let t = open_file_rules(&f, &w)?;
    if f.identity.selected != f.identity.canonical || w.identity.selected != w.identity.canonical {
        return Err(
            "a launch names the file it opens and its within folder by their resolved paths".into(),
        );
    }
    Ok((f, w, t))
}
/// The rules again for a confirmed open x, with its file and folder held open: the same file and
/// folder, its metadata unchanged (no write, no change of mode), the same content type.
#[cfg(target_os = "linux")]
fn open_rules(f: &OpenTarget, w: &OpenTarget, x: &ExactOpen) -> Result<(), String> {
    f.check()?;
    w.check()?;
    if Metadata::read(&f.file)? != x.metadata {
        return Err("the file to open changed since it was confirmed; review again".into());
    }
    if open_file_rules(f, w)? != x.content_type {
        return Err("the file to open changed its type since it was confirmed".into());
    }
    Ok(())
}
/// The shape a launch that opens file (resolved) must have: an opener of `OPENERS` taken from
/// folders, executable (resolved) one of them, argv exactly the opener's arguments then the file,
/// the file's folder as its cwd, no terminal program.
#[cfg(target_os = "linux")]
fn open_shape(
    folders: &[PathBuf],
    executable: &Path,
    argv: &[String],
    cwd: &Path,
    program: Option<&PathBuf>,
    file: &Path,
) -> Result<(), String> {
    let flags = folders
        .iter()
        .flat_map(|d| {
            OPENERS
                .iter()
                .map(move |(name, flags)| (d.join(name), *flags))
        })
        .find(|(p, _)| fs::canonicalize(p).is_ok_and(|p| p == executable))
        .map(|(_, flags)| flags)
        .ok_or_else(|| {
            format!(
                "a launch that opens a file runs xdg-open or gio from {}, not {}",
                OPENER_FOLDERS.join(", "),
                executable.display()
            )
        })?;
    let mut want: Vec<String> = flags.iter().map(|f| f.to_string()).collect();
    want.push(file.to_string_lossy().into_owned());
    if argv != want.as_slice() {
        return Err(format!(
            "an open launch runs exactly {} {}",
            executable.display(),
            serde_json::to_string(&want).unwrap_or_default()
        ));
    }
    if Some(cwd) != file.parent() {
        return Err("an open launch runs in the folder of the file it opens".into());
    }
    if program.is_some() {
        return Err("an open launch runs no terminal program".into());
    }
    Ok(())
}
/// Whether ch is a Unicode format character (Cf): GLib's data on Linux, the same table elsewhere
/// (`format_chars.rs`, checked against GLib's).
fn format_character(ch: char) -> bool {
    #[cfg(target_os = "linux")]
    {
        use gtk::glib::Unichar;
        ch.unicode_type() == gtk::glib::UnicodeType::Format
    }
    #[cfg(not(target_os = "linux"))]
    {
        crate::format_chars::is_format(ch)
    }
}
pub(crate) fn escape_format_characters(text: &str) -> String {
    let mut out = String::new();
    for ch in text.chars() {
        if ch.is_control() || matches!(ch, '\u{2028}' | '\u{2029}') || format_character(ch) {
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
    /// The remembered approval that authorized it, or that its confirmation saved.
    pub approval: Option<String>,
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
    /// The program a terminal runs, as opened and as the launch names it.
    #[cfg_attr(not(native_stack), allow(dead_code))]
    pub fn program(&self) -> Option<(&File, &Path)> {
        self.prepared
            .program
            .as_ref()
            .map(|p| (&p.file, p.identity.selected.as_path()))
    }
    /// Refused once a reviewed target changed since it was confirmed: the executable replaced or
    /// written in place (its metadata), or the cwd replaced.
    #[cfg_attr(not(native_stack), allow(dead_code))]
    pub(crate) fn recheck(&self) -> Result<(), String> {
        self.prepared.check()
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
    #[cfg_attr(not(native_stack), allow(dead_code))]
    origin: Origin,
}
impl Gesture {
    /// A click on one of the tray's Start… items, made in its menu event handler.
    #[cfg_attr(not(native_stack), allow(dead_code))]
    pub(crate) fn tray() -> Gesture {
        Gesture {
            created: Instant::now(),
            origin: Origin::Tray,
        }
    }
    /// The launcher's Start in the app's own window (`launcher_start`, after its caller check),
    /// naming a remembered launch of the host's own list: it can only start exactly that launch
    /// again, and anything changed asks.
    #[cfg_attr(not(native_stack), allow(dead_code))]
    pub(crate) fn main_window() -> Gesture {
        Gesture {
            created: Instant::now(),
            origin: Origin::Main,
        }
    }
}
#[derive(Serialize, Deserialize)]
struct SettingsFile {
    version: u32,
    approvals: Vec<Approval>,
}
#[cfg(unix)]
fn private_file(file: &File) -> Result<(), String> {
    let m = file.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() || m.uid() != unsafe { libc::geteuid() } || m.mode() & 0o022 != 0 {
        return Err(
            "settings must be a regular file owned by this user and not group/world-writable"
                .into(),
        );
    }
    // macOS: an extended access list entry could let another user change it whatever the mode
    // (`private_macos.rs`).
    #[cfg(target_os = "macos")]
    if crate::private_macos::has_entries(file).map_err(|e| e.to_string())? {
        return Err("settings must have no extended access list".into());
    }
    Ok(())
}
/// Windows: a regular file with an accepted owner and a protected access list allowing only this
/// user and SYSTEM (`confirmation_windows.rs`).
#[cfg(windows)]
fn private_file(file: &File) -> Result<(), String> {
    let m = file.metadata().map_err(|e| e.to_string())?;
    if !m.is_file() {
        return Err("settings must be a regular file".into());
    }
    windows_files::private(file, "settings").map_err(|e| {
        format!("settings must be owned by this user and writable by no one else: {e}")
    })
}
/// Opens the settings file at path without following a link at its end.
fn open_settings(path: &Path) -> std::io::Result<File> {
    #[cfg(unix)]
    {
        OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK | libc::O_NOCTTY)
            .open(path)
    }
    #[cfg(windows)]
    {
        windows_files::open_no_follow(path)
    }
}
/// Creates new file tmp, readable and writable only by this user (on Windows with a protected
/// access list of its own, given as it is made).
fn create_private(tmp: &Path) -> std::io::Result<File> {
    #[cfg(unix)]
    {
        OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(tmp)
    }
    #[cfg(windows)]
    {
        crate::private_windows::create_new_private(tmp)
    }
}
fn refuse_settings_symlink(path: &Path) -> Result<(), String> {
    match fs::symlink_metadata(path) {
        Ok(m) if m.is_symlink() => Err("symlinked settings refused".into()),
        Ok(_) => Ok(()),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e.to_string()),
    }
}
#[cfg(unix)]
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
    // macOS: no extended access list entry either, so no one else may change it through one and
    // the settings file inherits none (`private_macos.rs`).
    #[cfg(target_os = "macos")]
    crate::private_macos::clear_entries(&f).map_err(|e| format!("settings folder: {e}"))?;
    Ok(())
}
/// Windows: the folder and any folders missing above it made private as they are made, and the
/// folder refused when it is a link, not a folder, owned by another account or its permissions
/// cannot be read, and made private through a handle that is the folder itself when it is not
/// (`private_windows.rs`).
#[cfg(windows)]
fn secure_parent(path: &Path, create: bool) -> Result<(), String> {
    let dir = path.parent().ok_or("settings has no parent")?;
    let made = if create {
        crate::private_windows::create_private_dir_all(dir)
    } else {
        match crate::private_windows::make_private_dir(dir) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(()),
            r => r,
        }
    };
    made.map_err(|e| format!("settings folder refused: {e}"))
}
fn load_settings(path: &Path) -> Result<Vec<Approval>, String> {
    secure_parent(path, false)?;
    refuse_settings_symlink(path)?;
    let mut f = match open_settings(path) {
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
    /// The folders a launch that opens a file takes its opener from (`OPENER_FOLDERS`, and in a
    /// native-test build the folder `PLUXY_APP_TEST_OPENERS` names, for the check's stand-in).
    #[cfg(target_os = "linux")]
    opener_folders: Vec<PathBuf>,
    #[cfg(test)]
    hashes: usize,
}
/// `OPENER_FOLDERS`, and in a native-test build the stand-in opener's folder.
#[cfg(target_os = "linux")]
fn opener_folders() -> Vec<PathBuf> {
    #[allow(unused_mut)]
    let mut f: Vec<PathBuf> = OPENER_FOLDERS.iter().map(PathBuf::from).collect();
    #[cfg(feature = "native-test")]
    if let Some(d) = std::env::var_os("PLUXY_APP_TEST_OPENERS") {
        f.push(d.into());
    }
    f
}
impl Confirmations {
    pub fn open(settings: PathBuf) -> Result<Self, String> {
        let mut warning = None;
        let mut persistence_blocked = false;
        let approvals = match load_settings(&settings) {
            Ok(a) => a,
            Err(e) => {
                let safe = secure_parent(&settings, false).is_ok()
                    && open_settings(&settings)
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
            #[cfg(target_os = "linux")]
            opener_folders: opener_folders(),
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
        match open_settings(&self.settings) {
            Ok(f) => private_file(&f)?,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(format!("settings refused: {e}")),
        }
        let tmp = dir.join(format!(".confirmation-{}", crate::managed::random_key()));
        let result = (|| {
            let mut f = create_private(&tmp).map_err(|e| e.to_string())?;
            #[cfg(windows)]
            private_file(&f)?;
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
    /// A call with a tool hop in its chain that no page started: queued with the page limits, it
    /// never uses or saves a remembered approval, and its dialog offers no Remember.
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
    /// native code built from verified records (enabling a native tool, granting a file or
    /// folder through Access, installing or rolling back the app's own core from a signed
    /// release), from a setting or program it checked (changing the release feed, adopting a
    /// core program whose path, size and SHA-256 it read) and, for the router, the window's own
    /// direct call of a launch. Never page data, and never a call with a tool hop
    /// (`prepare_import`). Those actions have no launch, except the router's, which may use a
    /// remembered approval after a native gesture.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    pub(crate) fn prepare_main(&mut self, action: Action) -> Result<Prepared, String> {
        self.prepare(action, Origin::Main)
    }
    /// `prepare_main` for a call a native gesture started: prepared with the gesture's origin
    /// (the window's launcher or the tray), which `remembered` may then use.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    pub(crate) fn prepare_gestured(
        &mut self,
        action: Action,
        gesture: &Gesture,
    ) -> Result<Prepared, String> {
        self.prepare(action, gesture.origin)
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
        let mut program = None;
        let mut opens = None;
        let launch = if let Some(l) = action.launch {
            let mut e = OpenTarget::open(&l.executable)?;
            if !e.file.metadata().map_err(|e| e.to_string())?.is_file() {
                return Err("executable is not a regular file".into());
            }
            let m = Metadata::read(&e.file)?;
            if !runnable(&e.identity.canonical, &m) {
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
            let exact_program = match &l.program {
                Some(path) => {
                    let (p, x) = self.open_executable(path)?;
                    program = Some(p);
                    Some(x)
                }
                None => None,
            };
            let exact_opens = match &l.opens {
                // Opening a file needs the desktop's opener and its file types: Linux's only, for
                // now (xdg-open or gio, shared-mime-info).
                #[cfg(not(target_os = "linux"))]
                Some(_) => {
                    let _ = &mut opens;
                    return Err(
                        "a launch that opens a file is supported on Linux only for now".into(),
                    );
                }
                #[cfg(target_os = "linux")]
                Some((file, within)) => {
                    let (f, w, content_type) = open_within(file, within)?;
                    open_shape(
                        &self.opener_folders,
                        &e.identity.canonical,
                        &l.argv,
                        &c.identity.canonical,
                        l.program.as_ref(),
                        &f.identity.canonical,
                    )?;
                    let x = ExactOpen {
                        file: f.identity.clone(),
                        metadata: Metadata::read(&f.file)?,
                        content_type,
                        within: w.identity.clone(),
                    };
                    opens = Some((f, w));
                    Some(x)
                }
                None => None,
            };
            let exact = ExactLaunch {
                executable: e.identity.clone(),
                metadata: m,
                sha256: hash,
                argv: l.argv,
                cwd: c.identity.clone(),
                program: exact_program,
                opens: exact_opens,
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
            program,
            opens,
        };
        p.check()?;
        Ok(p)
    }
    /// Opens and hashes (or reuses the cached hash of) an executable file, a launch's program.
    fn open_executable(&mut self, path: &Path) -> Result<(OpenTarget, ExactProgram), String> {
        let mut e = OpenTarget::open(path)?;
        if !e.file.metadata().map_err(|e| e.to_string())?.is_file() {
            return Err("program is not a regular file".into());
        }
        let m = Metadata::read(&e.file)?;
        if !runnable(&e.identity.canonical, &m) {
            return Err("program is not executable".into());
        }
        let hash = match self
            .cache
            .get(&e.identity.canonical)
            .filter(|(cached, _)| *cached == m)
        {
            Some((_, h)) => h.clone(),
            None => {
                let h = hash_file(&mut e.file, &m)?;
                self.cache
                    .insert(e.identity.canonical.clone(), (m.clone(), h.clone()));
                h
            }
        };
        let x = ExactProgram {
            file: e.identity.clone(),
            metadata: m,
            sha256: hash,
        };
        Ok((e, x))
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
            approval: t.approval,
        })
    }
    /// Completion is available to native callers only and is removed when read.
    pub fn take_result(&mut self, id: &str) -> Option<Result<Option<String>, String>> {
        self.results.remove(id)
    }
    pub fn token_action(&self, token: &str) -> Option<&str> {
        self.issued.get(token).map(|t| t.action.as_str())
    }
    /// Whether remembered approval id still stands.
    #[cfg_attr(not(target_os = "linux"), allow(dead_code))]
    pub(crate) fn has_approval(&self, id: &str) -> bool {
        self.approvals.iter().any(|a| a.id == id)
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
/// The dialogs' fixed texts, shared by the GTK, Win32 and AppKit dialogs and the native tests.
/// Labels are in GTK's mnemonic form (`_` before the access key, `__` for a `_`); `mnemonic`
/// renders them for Win32 and AppKit.
pub(crate) mod text {
    pub(crate) const CONFIRM_TITLE: &str = "pluxy native confirmation";
    pub(crate) const SETTINGS_TITLE: &str = "pluxy remembered launches";
    pub(crate) const NOTICE_TITLE: &str = "pluxy launch notice";
    pub(crate) const CANCEL: &str = "_Cancel";
    pub(crate) const CONFIRM: &str = "C_onfirm";
    pub(crate) const SETTINGS_CLOSE: &str = "Close";
    pub(crate) const NO_LAUNCHES: &str = "No remembered launches.";
    pub(crate) const REVOKE: &str = "_Revoke remembered launch";
    pub(crate) const REVOKED: &str = "Revoked";
    pub(crate) const COULD_NOT_REVOKE: &str = "Could not revoke: ";

    /// Label, GTK mnemonic form, without its access key: `_` dropped, `__` kept as one `_`.
    #[cfg_attr(target_os = "linux", allow(dead_code))]
    pub(crate) fn plain(label: &str) -> String {
        let mut out = String::new();
        let mut chars = label.chars().peekable();
        while let Some(c) = chars.next() {
            if c == '_' {
                if chars.peek() == Some(&'_') {
                    chars.next();
                    out.push('_');
                }
                continue;
            }
            out.push(c);
        }
        out
    }

    /// Label, GTK mnemonic form, in Win32's: `&` before the access key, `&&` for a `&`.
    #[cfg_attr(not(windows), allow(dead_code))]
    pub(crate) fn win32(label: &str) -> String {
        let mut out = String::new();
        let mut chars = label.chars().peekable();
        while let Some(c) = chars.next() {
            match c {
                '&' => out.push_str("&&"),
                '_' if chars.peek() == Some(&'_') => {
                    chars.next();
                    out.push('_');
                }
                '_' => out.push('&'),
                c => out.push(c),
            }
        }
        out
    }

    /// The access key of label, GTK mnemonic form, lower-cased: the letter after its first single
    /// `_`.
    #[cfg_attr(not(any(test, feature = "native-test")), allow(dead_code))]
    pub(crate) fn access_key(label: &str) -> Option<char> {
        let mut chars = label.chars().peekable();
        while let Some(c) = chars.next() {
            if c == '_' {
                match chars.next() {
                    Some('_') => continue,
                    Some(k) => return k.to_lowercase().next(),
                    None => return None,
                }
            }
        }
        None
    }

    #[cfg(test)]
    mod tests {
        #[test]
        fn labels_render_for_each_toolkit() {
            assert_eq!(super::plain("C_onfirm"), "Confirm");
            assert_eq!(super::win32("C_onfirm"), "C&onfirm");
            assert_eq!(super::access_key("C_onfirm"), Some('o'));
            let secrets = "_Remember this exact executable, argv, cwd and secrets: a__b & c";
            assert_eq!(
                super::plain(secrets),
                "Remember this exact executable, argv, cwd and secrets: a_b & c"
            );
            assert_eq!(
                super::win32(secrets),
                "&Remember this exact executable, argv, cwd and secrets: a_b && c"
            );
            assert_eq!(super::access_key(secrets), Some('r'));
            assert_eq!(super::access_key("Close"), None);
        }
    }
}

/// What a confirmation dialog shows, from `begin`.
pub(crate) struct Review {
    /// The whole review text.
    pub review: String,
    /// The Remember choice's text (GTK mnemonic form), when the dialog offers it: only a launch
    /// from the window, the tray or native UI, while remembering works. A page or import chain
    /// never uses a remembered approval, so it would save one nothing can use.
    pub remember: Option<String>,
    /// Requester and action, for the launch notice.
    pub summary: String,
    /// Started by a page: Cancel focused, Confirm only after a second.
    pub page: bool,
    /// The app's own action, which shows its own outcome (no launch notice).
    pub own: bool,
}

/// Marks pending confirmation id as showing and says what its dialog shows. Refused when it is
/// not fresh, was confirmed already or shows already.
pub(crate) fn begin(app: &tauri::AppHandle, id: &str) -> Result<Review, String> {
    let st = app.state::<crate::App>();
    let mut c = st.confirmations.lock().unwrap();
    let (review, launch) = c.review(id)?;
    if !c.showing.insert(id.to_string()) {
        return Err("confirmation already showing".into());
    }
    let pending = c.pending.get(id).unwrap();
    Ok(Review {
        review,
        remember: (launch && !pending.page).then(|| pending.prepared.remember_text()),
        summary: pending.prepared.notice(),
        page: pending.page,
        own: pending.prepared.own,
    })
}

/// The answer to confirmation id's dialog, from its native handler only (every toolkit's calls
/// this once): finishes it, keeps the result for its native caller and, unless it is the app's
/// own action, shows the launch notice with the outcome.
pub(crate) fn respond(
    app: &tauri::AppHandle,
    id: &str,
    confirmed: bool,
    remember: bool,
    summary: &str,
    own: bool,
) {
    let st = app.state::<crate::App>();
    let mut c = st.confirmations.lock().unwrap();
    let result = c.finish(id, confirmed, remember);
    c.showing.remove(id);
    let outcome = match &result {
        Ok(Some(_)) => "confirmed",
        Ok(None) => "cancelled",
        Err(_) => "expired, changed or revoked",
    };
    let mut text = format!("{summary}\nOutcome: {outcome}");
    if let Some(warning) = c.saving_warning(id) {
        text.push_str(&format!("\n{warning}"));
    }
    if c.results.len() >= 64 {
        c.results.clear();
    }
    c.results.insert(id.to_string(), result);
    drop(c);
    // The app's own action shows its own outcome (such as the window with a new key).
    if !own {
        notice(app, text);
    }
}

/// Whether confirmation id was withdrawn (its caller's call was cancelled) or is otherwise no
/// longer pending: its dialog then closes, as cancelled.
pub(crate) fn withdrawn(app: &tauri::AppHandle, id: &str) -> bool {
    !app.state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .pending
        .contains_key(id)
}

/// A remembered approval as native settings show it.
fn approval_text(a: &Approval) -> String {
    review_text(&a.exact).unwrap_or_else(std::convert::identity)
}

/// The remembered approvals, newest first: each one's ID and text, for native settings.
#[cfg_attr(target_os = "linux", allow(dead_code))]
pub(crate) fn remembered_list(app: &tauri::AppHandle) -> Vec<(String, String)> {
    let approvals = app
        .state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .approvals
        .clone();
    approvals
        .iter()
        .rev()
        .map(|a| (a.id.clone(), approval_text(a)))
        .collect()
}

/// Revokes remembered approval id from native settings.
pub(crate) fn revoke_approval(app: &tauri::AppHandle, id: &str) -> Result<(), String> {
    app.state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .revoke(id)?;
    // The tray's Start… menu lists only launches still remembered.
    #[cfg(native_stack)]
    crate::launcher::refresh_tray(app);
    Ok(())
}

/// Reviews an app action started in the app's window, and waits for the person's answer: true
/// only after Confirm, with its single-use token spent here. Never call it on the GTK thread,
/// which shows the dialog.
pub(crate) fn confirm_app(
    app: &tauri::AppHandle,
    what: String,
    generation: u64,
) -> Result<bool, String> {
    let id = {
        let st = app.state::<crate::App>();
        let mut c = st.confirmations.lock().unwrap();
        let prepared = c.prepare_app(what, generation)?;
        c.queue(prepared)?
    };
    let st = app.state::<crate::App>();
    // Withdrawn on every way out that is not an answer, so a dialog that never showed does not
    // keep one of the pending places until its lifetime ends.
    if let Err(e) = request(app, id.clone()) {
        st.confirmations.lock().unwrap().withdraw(&id);
        return Err(e);
    }
    // The dialog cancels itself when its lifetime ends; this is only a backstop.
    wait_app(
        &st.confirmations,
        &id,
        Instant::now() + LIFETIME + Duration::from_secs(10),
    )
}

/// Waits for the result of app confirmation id until deadline: true after Confirm, with its
/// single-use token spent. One that does not finish by then is withdrawn.
fn wait_app(
    confirmations: &std::sync::Mutex<Confirmations>,
    id: &str,
    deadline: Instant,
) -> Result<bool, String> {
    loop {
        let mut c = confirmations.lock().unwrap();
        match c.take_result(id) {
            Some(Ok(Some(token))) => {
                c.consume(id, &token)?;
                return Ok(true);
            }
            Some(Ok(None)) => return Ok(false),
            Some(Err(e)) => return Err(e),
            None if Instant::now() > deadline => {
                c.withdraw(id);
                return Err("the confirmation did not finish".into());
            }
            None => {}
        }
        drop(c);
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// A host-resolved page Start always reviews, even when an identical approval exists.
pub fn page_start(app: &tauri::AppHandle, prepared: Prepared) -> Result<String, String> {
    if prepared.origin != Origin::Page {
        return Err("page Start requires a page-origin preparation".into());
    }
    let review = prepared.notice();
    let result = app
        .state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .queue(prepared);
    let outcome = match &result {
        Ok(_) => "awaiting native confirmation".to_string(),
        Err(e) => e.clone(),
    };
    notice(app, format!("{review}\nOutcome: {outcome}"));
    let id = result?;
    request(app, id.clone())?;
    Ok(id)
}

#[cfg(windows)]
#[path = "confirmation_windows.rs"]
mod windows_files;

#[cfg(target_os = "linux")]
#[path = "confirmation_ui.rs"]
mod ui;
#[cfg(windows)]
#[path = "confirmation_ui_windows.rs"]
mod ui;
#[cfg(target_os = "macos")]
#[path = "confirmation_ui_macos.rs"]
mod ui;
pub(crate) use ui::{notice, notice as ui_notice, request, settings};

#[cfg(test)]
#[path = "confirmation_tests.rs"]
mod tests;

#[cfg(feature = "native-test")]
#[path = "confirmation_native.rs"]
pub(crate) mod native;
