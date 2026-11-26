//! Selected targets: resolving a selected config file or a file in a selected root (either may be
//! a link, as stow and chezmoi make), pinning what it resolves to, checking it again, and reading
//! and writing the file through a handle on its folder (cap-std), never by a path again.

use cap_std::ambient_authority;
use cap_std::fs::Dir;
#[cfg(unix)]
use cap_std::fs::MetadataExt;
#[cfg(unix)]
use cap_std::fs::OpenOptions;
use pluxy_program::Error;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::io::{ErrorKind, Read, Write};
use std::path::{Component, Path, PathBuf};

/// The largest file the library reads or writes: its backups must fit the vault's 512 KiB of
/// base64.
pub const MAX_FILE: usize = 256 << 10;

/// The mode of a file the library creates on Linux and macOS (config files may hold keys); on
/// Windows such a file is private (owned by this user, a protected access list allowing only this
/// user and SYSTEM, as `pluxy-files` makes it).
#[cfg(unix)]
const NEW_FILE_MODE: u32 = 0o600;

/// A file's permissions as the library records and gives them back: on Linux and macOS its
/// permission bits (setuid, setgid and sticky left out), a JSON number; on Windows its access list
/// in the system's text form (SDDL, protected or not), a JSON string.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Mode(#[cfg(unix)] u32, #[cfg(windows)] String);

impl Mode {
    pub fn to_json(&self) -> Value {
        json!(self.0)
    }

    /// The mode a record holds, None for null or one of another system's kind.
    pub fn from_json(v: &Value) -> Option<Mode> {
        #[cfg(unix)]
        return v.as_u64().map(|m| Mode((m & 0o777) as u32));
        #[cfg(windows)]
        return v.as_str().map(|s| Mode(s.to_string()));
    }
}

/// The permissions [`Target::replace`] gives the file it writes, from the file's own now, or the
/// `mode` it is given when there is no file (else a new file's: 0600, private on Windows).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WriteMode {
    /// The file's own permissions (clear, restore).
    Keep,
    /// As `Keep`, with the group's and others' bits taken away; on Windows the file is made
    /// private (apply: it writes a writer's values, a key among them).
    OwnerOnly,
    /// The `mode` given, whatever the file's permissions now (a rollback: those the file had
    /// before the write it undoes); as `Keep` when none is given.
    Exact,
}

/// What the person selected: a config file, or a file inside a selected root folder.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Selection {
    /// A config file, itself possibly a link (the link is kept; its target is written).
    File(PathBuf),
    /// A file inside a root folder, which may itself be a link; `path` is slash-separated parts
    /// inside it, none of them a link.
    Root { root: PathBuf, path: String },
}

impl Selection {
    /// The selection an operation's input names: `{"file": <absolute path>}` or
    /// `{"root": <absolute path>, "path": <parts inside it>}`.
    pub fn from_json(v: &Value) -> Result<Selection, Error> {
        let o = v
            .as_object()
            .ok_or_else(|| Error::invalid_request("a target is an object"))?;
        let s = |k: &str| o.get(k).and_then(Value::as_str);
        match (s("file"), s("root"), s("path")) {
            (Some(f), None, None) if o.len() == 1 => Ok(Selection::File(PathBuf::from(f))),
            (None, Some(r), Some(p)) if o.len() == 2 => Ok(Selection::Root {
                root: PathBuf::from(r),
                path: p.to_string(),
            }),
            _ => Err(Error::invalid_request(
                "a target is {\"file\": <path>} or {\"root\": <path>, \"path\": <parts>}",
            )),
        }
    }
}

/// A file or folder's identity: its device and inode.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Id {
    pub dev: u64,
    pub ino: u64,
}

impl Id {
    /// The identity in m: on Windows the volume serial number and file index, which a metadata
    /// read through a handle has (an error, never a panic, when one does not).
    fn of(m: &cap_std::fs::Metadata) -> Result<Id, Error> {
        #[cfg(unix)]
        return Ok(Id {
            dev: m.dev(),
            ino: m.ino(),
        });
        #[cfg(windows)]
        {
            use cap_primitives::fs::_WindowsByHandle;
            match (m.volume_serial_number(), m.file_index()) {
                (Some(dev), Some(ino)) => Ok(Id {
                    dev: dev.into(),
                    ino,
                }),
                _ => Err(Error::internal(
                    "the system gave no volume serial number or file index for a file",
                )),
            }
        }
    }

    fn to_json(self) -> Value {
        json!(format!("{}:{}", self.dev, self.ino))
    }

    fn from_json(v: &Value) -> Result<Option<Id>, Error> {
        if v.is_null() {
            return Ok(None);
        }
        let bad = || Error::invalid_request("an identity is \"<device>:<inode>\" or null");
        let (d, i) = v.as_str().and_then(|s| s.split_once(':')).ok_or_else(bad)?;
        Ok(Some(Id {
            dev: d.parse().map_err(|_| bad())?,
            ino: i.parse().map_err(|_| bad())?,
        }))
    }
}

/// What a selection resolved to when it was previewed: shown to the person in the operation's
/// native confirmation (as the input of the write that carries it), and checked again before
/// each write and after it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Pin {
    pub selection: Selection,
    /// The selected path's link text, when it is a link (the file's, or the root's).
    pub link: Option<String>,
    /// The link's own identity.
    pub link_id: Option<Id>,
    /// The root folder the selected root resolves to.
    pub root: Option<PathBuf>,
    pub root_id: Option<Id>,
    /// The file written: what the selection resolves to.
    pub target: PathBuf,
    /// The identity of the folder holding it.
    pub parent_id: Id,
    /// The file's identity, None when it does not exist.
    pub file_id: Option<Id>,
    /// The SHA-256 of its bytes, None when it does not exist.
    pub sha256: Option<String>,
}

impl Pin {
    /// The pin as an operation's output shows it and a write's input carries it back.
    pub fn to_json(&self) -> Value {
        let (selected, path) = match &self.selection {
            Selection::File(f) => (f, None),
            Selection::Root { root, path } => (root, Some(path)),
        };
        let mut v = json!({
            "selected": selected,
            "link": self.link,
            "target": self.target,
            "identity": {
                "link": self.link_id.map(Id::to_json),
                "parent": self.parent_id.to_json(),
                "file": self.file_id.map(Id::to_json),
            },
            "sha256": self.sha256,
        });
        if let Some(p) = path {
            v["path"] = json!(p);
            v["root"] = json!(self.root);
            v["identity"]["root"] = self.root_id.map(Id::to_json).into();
        }
        v
    }

    /// A pin a write's input carries back: exactly the fields [`Pin::to_json`] writes, nothing
    /// more, so an input cannot carry text the confirmation shows beside the pin.
    pub fn from_json(v: &Value) -> Result<Pin, Error> {
        let bad =
            |what: &str| Error::invalid_request(format!("a target's {what} is missing or wrong"));
        let only = |o: &Value, keys: &[&str], what: &str| -> Result<(), Error> {
            let o = o.as_object().ok_or_else(|| bad(what))?;
            match o.keys().find(|k| !keys.contains(&k.as_str())) {
                Some(k) => Err(Error::invalid_request(format!(
                    "a target has an unknown field {k:?}"
                ))),
                None => Ok(()),
            }
        };
        let rooted = v.get("path").is_some();
        let (fields, ids): (&[&str], &[&str]) = if rooted {
            (
                &[
                    "selected", "path", "root", "link", "target", "identity", "sha256",
                ],
                &["link", "root", "parent", "file"],
            )
        } else {
            (
                &["selected", "link", "target", "identity", "sha256"],
                &["link", "parent", "file"],
            )
        };
        only(v, fields, "pin")?;
        only(&v["identity"], ids, "identity")?;
        let s = |k: &str| v.get(k).and_then(Value::as_str);
        let opt = |k: &str| -> Result<Option<String>, Error> {
            match v.get(k) {
                None | Some(Value::Null) => Ok(None),
                Some(Value::String(s)) => Ok(Some(s.clone())),
                _ => Err(bad(k)),
            }
        };
        let selected = PathBuf::from(s("selected").ok_or_else(|| bad("selected"))?);
        let id = &v["identity"];
        let (selection, root, root_id) = match v.get("path") {
            None => (Selection::File(selected), None, None),
            Some(p) => (
                Selection::Root {
                    root: selected,
                    path: p.as_str().ok_or_else(|| bad("path"))?.to_string(),
                },
                Some(PathBuf::from(s("root").ok_or_else(|| bad("root"))?)),
                Some(Id::from_json(&id["root"])?.ok_or_else(|| bad("root identity"))?),
            ),
        };
        Ok(Pin {
            selection,
            link: opt("link")?,
            link_id: Id::from_json(&id["link"])?,
            root,
            root_id,
            target: PathBuf::from(s("target").ok_or_else(|| bad("target"))?),
            parent_id: Id::from_json(&id["parent"])?.ok_or_else(|| bad("parent identity"))?,
            file_id: Id::from_json(&id["file"])?,
            sha256: opt("sha256")?,
        })
    }

    /// Whether other names the same resolution (everything but the file's identity and bytes,
    /// which a write changes).
    fn same_place(&self, other: &Pin) -> bool {
        self.selection == other.selection
            && self.link == other.link
            && self.link_id == other.link_id
            && self.root == other.root
            && self.root_id == other.root_id
            && self.target == other.target
            && self.parent_id == other.parent_id
    }
}

/// The JSON Schema of a pin, in the host's schema subset, for a writer's operation inputs
/// (`"items"` of its `targets`): exactly the fields [`Pin::to_json`] writes.
pub fn pin_schema() -> Value {
    let path = json!({"type": "string", "minLength": 1, "maxLength": 4096});
    let id = json!({"type": ["string", "null"], "maxLength": 41});
    json!({
        "type": "object",
        "properties": {
            "selected": path,
            "path": path,
            "root": path,
            "link": {"type": ["string", "null"], "maxLength": 4096},
            "target": path,
            "identity": {
                "type": "object",
                "properties": {"link": id, "root": id, "parent": id, "file": id},
                "required": ["link", "parent", "file"],
                "additionalProperties": false
            },
            "sha256": {"type": ["string", "null"], "minLength": 64, "maxLength": 64}
        },
        "required": ["selected", "link", "target", "identity", "sha256"],
        "additionalProperties": false
    })
}

/// The SHA-256 of bytes, in lower-case hexadecimal.
pub fn sha256(bytes: &[u8]) -> String {
    Sha256::digest(bytes)
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

/// A file's SHA-256, None when it does not exist.
pub fn sha256_of(image: Option<&[u8]>) -> Option<String> {
    image.map(sha256)
}

fn io(what: &str, e: std::io::Error) -> Error {
    Error::unavailable(format!("{what}: {e}"))
}

/// The error for a selection that resolves differently than when it was previewed.
fn changed(target: &Path, what: &str) -> Error {
    Error::invalid_request(format!(
        "the target changed since the preview ({what}): {}; preview it again",
        target.display()
    ))
}

/// An absolute path with no `.` or `..` parts, as UTF-8 (on Windows, not in a WSL distribution:
/// see [`in_wsl`]).
fn absolute(p: &Path, what: &str) -> Result<(), Error> {
    let ok = p.is_absolute()
        && p.to_str().is_some()
        && p.components()
            .skip_while(|c| matches!(c, Component::Prefix(_) | Component::RootDir))
            .all(|c| matches!(c, Component::Normal(_)))
        && p.file_name().is_some();
    if !ok {
        return Err(Error::invalid_request(format!(
            "the {what} path must be absolute, in UTF-8, with no . or .. parts: {}",
            p.display()
        )));
    }
    not_in_wsl(p)?;
    windows_plain(p)
}

/// On Windows, refuses a path whose names Windows would read as another file (a stream, a device,
/// a name it trims: see [`pluxy_files::windows_name_problem`]), or one in a device namespace
/// (`\\.\…`, `\\?\GLOBALROOT\…`) rather than on a drive or a share.
fn windows_plain(p: &Path) -> Result<(), Error> {
    #[cfg(windows)]
    for c in p.components() {
        use std::path::Prefix;
        let why = match c {
            Component::Prefix(pre) => match pre.kind() {
                Prefix::Disk(_)
                | Prefix::VerbatimDisk(_)
                | Prefix::UNC(..)
                | Prefix::VerbatimUNC(..) => None,
                Prefix::Verbatim(_) | Prefix::DeviceNS(_) => {
                    Some("it is in a device namespace, not on a drive or a share")
                }
            },
            Component::Normal(name) => pluxy_files::windows_name_problem(&name.to_string_lossy()),
            _ => None,
        };
        if let Some(why) = why {
            return Err(Error::invalid_request(format!(
                "{} is not a path this program writes on Windows: {why}",
                p.display()
            )));
        }
    }
    let _ = p;
    Ok(())
}

/// Whether path is inside a WSL distribution as Windows reaches it (`\\wsl.localhost\…` or
/// `\\wsl$\…`, in any case, either slash, also after `\\?\UNC\`).
pub(crate) fn in_wsl(path: &str) -> bool {
    let p = path.replace('/', "\\").to_ascii_lowercase();
    let share = p
        .strip_prefix(r"\\?\unc\")
        .or_else(|| p.strip_prefix(r"\\.\unc\"))
        .or_else(|| p.strip_prefix(r"\\"));
    share.is_some_and(|s| {
        // Windows drops a host name's trailing dots and blanks (`\\wsl.localhost.\`).
        let host = s
            .split('\\')
            .next()
            .unwrap_or_default()
            .trim_end_matches(['.', ' ']);
        host == "wsl$" || host == "wsl.localhost"
    })
}

/// On Windows, refuses a path inside a WSL distribution: the package's Linux program serves those,
/// inside the distribution, never Windows writes over its network share.
fn not_in_wsl(p: &Path) -> Result<(), Error> {
    if cfg!(windows) && in_wsl(&p.to_string_lossy()) {
        return Err(Error::invalid_request(format!(
            "{} is inside a WSL distribution, which this program does not write",
            p.display()
        )));
    }
    Ok(())
}

/// The folder holding p, or p itself for a root.
fn folder_of(p: &Path) -> &Path {
    p.parent().unwrap_or(p)
}

/// Opens folder by its (resolved) path.
fn open_dir(folder: &Path) -> Result<Dir, Error> {
    Dir::open_ambient_dir(folder, ambient_authority())
        .map_err(|e| io(&format!("{}", folder.display()), e))
}

fn dir_id(d: &Dir, at: &Path) -> Result<Id, Error> {
    let m = d
        .dir_metadata()
        .map_err(|e| io(&format!("{}", at.display()), e))?;
    Id::of(&m)
}

/// The link at name in d: its text and identity, or None when name is not a link (or missing).
fn link_at(d: &Dir, name: &Path, at: &Path) -> Result<Option<(String, Id)>, Error> {
    match d.symlink_metadata(name) {
        Ok(m) if m.file_type().is_symlink() => {
            let text = d
                .read_link_contents(name)
                .map_err(|e| io(&format!("{}", at.display()), e))?;
            let text = text.to_str().map(String::from).ok_or_else(|| {
                Error::invalid_request(format!("{}: the link's text is not UTF-8", at.display()))
            })?;
            Ok(Some((text, Id::of(&m)?)))
        }
        Ok(_) => Ok(None),
        Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
        Err(e) => Err(io(&format!("{}", at.display()), e)),
    }
}

/// The name the file `name` in d has on disk, so an existing file is written under its own name:
/// where the file system ignores case (as macOS and Windows do by default), or Windows reads a short
/// (8.3) name, the selected spelling can name a file whose own name differs, and a rename to the
/// selected spelling would change it. name itself when nothing is there (and on Linux).
fn on_disk_name(d: &Dir, name: &Path, at: &Path) -> Result<PathBuf, Error> {
    #[cfg(target_os = "linux")]
    {
        let _ = (d, at);
        Ok(name.to_path_buf())
    }
    #[cfg(not(target_os = "linux"))]
    {
        let fail = |e| io(&format!("{}", at.display()), e);
        let id = match d.symlink_metadata(name) {
            Ok(m) => Id::of(&m)?,
            Err(e) if e.kind() == ErrorKind::NotFound => return Ok(name.to_path_buf()),
            Err(e) => return Err(fail(e)),
        };
        let mut names = Vec::new();
        for entry in d.entries().map_err(fail)? {
            let n = entry.map_err(fail)?.file_name();
            if n == name.as_os_str() {
                return Ok(name.to_path_buf());
            }
            names.push(n);
        }
        // The same file under another spelling: those equal but for case first, then (for a
        // short name) any.
        let lower = |n: &std::ffi::OsStr| n.to_string_lossy().to_lowercase();
        let wanted = lower(name.as_os_str());
        let (alike, rest): (Vec<_>, Vec<_>) = names.into_iter().partition(|n| lower(n) == wanted);
        for n in alike.into_iter().chain(rest) {
            if let Ok(m) = d.symlink_metadata(&n) {
                if Id::of(&m)? == id {
                    return Ok(PathBuf::from(n));
                }
            }
        }
        Ok(name.to_path_buf())
    }
}

/// The resolved, canonical path of an existing path.
fn canonical(p: &Path) -> Result<PathBuf, Error> {
    let c = std::fs::canonicalize(p).map_err(|e| match e.kind() {
        ErrorKind::NotFound => Error::invalid_request(format!(
            "{} does not resolve to an existing file or folder",
            p.display()
        )),
        _ => io(&format!("{}", p.display()), e),
    })?;
    c.to_str().ok_or_else(|| {
        Error::invalid_request(format!(
            "{} resolves to a path that is not UTF-8",
            p.display()
        ))
    })?;
    not_in_wsl(&c)?;
    windows_plain(&c)?;
    Ok(c)
}

/// The mount points and types in `/proc/self/mountinfo` text, mount points unescaped (`\040`).
#[cfg(target_os = "linux")]
fn mounts(text: &str) -> Vec<(PathBuf, String)> {
    let unescape = |s: &str| {
        let b = s.as_bytes();
        let mut out = Vec::with_capacity(b.len());
        let mut i = 0;
        while i < b.len() {
            let octal = b[i] == b'\\'
                && i + 3 < b.len()
                && b[i + 1..i + 4].iter().all(|c| (b'0'..=b'7').contains(c));
            let v = octal
                .then(|| {
                    b[i + 1..i + 4]
                        .iter()
                        .fold(0u32, |v, c| v * 8 + u32::from(c - b'0'))
                })
                .and_then(|v| u8::try_from(v).ok());
            if let Some(v) = v {
                out.push(v);
                i += 4;
            } else {
                out.push(b[i]);
                i += 1;
            }
        }
        use std::os::unix::ffi::OsStringExt;
        PathBuf::from(std::ffi::OsString::from_vec(out))
    };
    text.lines()
        .filter_map(|l| {
            let f: Vec<&str> = l.split(' ').collect();
            let dash = f.iter().position(|x| *x == "-")?;
            (dash >= 6 && f.len() > dash + 1).then(|| (unescape(f[4]), f[dash + 1].to_string()))
        })
        .collect()
}

/// The types WSL mounts Windows drives with.
#[cfg(target_os = "linux")]
fn windows_type(fstype: &str) -> bool {
    matches!(fstype, "drvfs" | "9p" | "virtiofs")
}

/// Whether path (absolute and resolved) is on a Windows drive, by `/proc/self/mountinfo` text: the
/// mount holding it (the last of the longest mount points it is inside) is of a type WSL mounts
/// drives with, or is mounted in the folder WSL mounts the drives in (the automount root: the
/// folder holding a drive mount, one whose last part is one letter), as `/mnt/wsl` is.
#[cfg(target_os = "linux")]
pub(crate) fn on_windows_drive(mountinfo: &str, path: &Path) -> bool {
    let all = mounts(mountinfo);
    let holding = all
        .iter()
        .filter(|(point, _)| path.starts_with(point))
        .max_by_key(|(point, _)| point.components().count());
    let Some((point, fstype)) = holding else {
        return false;
    };
    if windows_type(fstype) {
        return true;
    }
    let drive = |p: &Path| {
        p.file_name()
            .and_then(|n| n.to_str())
            .is_some_and(|n| n.len() == 1 && n.as_bytes()[0].is_ascii_alphabetic())
    };
    let roots = all
        .iter()
        .filter(|(p, t)| windows_type(t) && drive(p))
        .filter_map(|(p, _)| p.parent());
    let parent = point.parent();
    roots
        .into_iter()
        .any(|r| parent == Some(r) && r != Path::new("/"))
}

#[cfg(all(test, target_os = "linux"))]
thread_local! {
    /// Unit tests only: this thread runs as in WSL, with these mounts (`/proc/self/mountinfo` text).
    pub(crate) static WSL_MOUNTS: std::cell::RefCell<Option<String>> =
        const { std::cell::RefCell::new(None) };
}

/// In WSL (the host set `PLUXY_ENVIRONMENT`), the mounts now; None elsewhere.
#[cfg(target_os = "linux")]
fn wsl_mounts() -> Result<Option<String>, Error> {
    #[cfg(test)]
    if let Some(m) = WSL_MOUNTS.with(|m| m.borrow().clone()) {
        return Ok(Some(m));
    }
    if std::env::var_os("PLUXY_ENVIRONMENT").is_none_or(|v| v.is_empty()) {
        return Ok(None);
    }
    std::fs::read_to_string("/proc/self/mountinfo")
        .map(Some)
        .map_err(|e| io("/proc/self/mountinfo", e))
}

/// Off Linux, nothing is refused: WSL runs Linux programs, and `lib/program` refuses
/// `PLUXY_ENVIRONMENT` anywhere but Linux.
#[cfg(not(target_os = "linux"))]
pub(crate) fn refuse_windows_drive(_paths: &[&Path]) -> Result<(), Error> {
    Ok(())
}

/// In WSL (the host set `PLUXY_ENVIRONMENT`), refuses paths (resolved folders, and the file itself,
/// which may be a mount of its own) on a Windows drive:
/// without its metadata mode, a drive keeps no owner-only permission bits, and Windows' own
/// writer already sets up its files.
#[cfg(target_os = "linux")]
pub(crate) fn refuse_windows_drive(paths: &[&Path]) -> Result<(), Error> {
    let Some(info) = wsl_mounts()? else {
        return Ok(());
    };
    if paths.iter().any(|p| on_windows_drive(&info, p)) {
        return Err(Error::invalid_request(
            "This file is on a Windows drive; set it up from Windows local.",
        ));
    }
    Ok(())
}

/// A selection resolved now: the pin, and a handle on the target's folder with the file's name.
pub struct Target {
    pub pin: Pin,
    dir: Dir,
    name: PathBuf,
    /// Opened at the pinned target itself ([`Target::pinned`]), not through the selection.
    pinned: bool,
}

impl Target {
    /// Resolves selection now and pins what it resolves to, reading the file's bytes for its
    /// SHA-256. A file inside a root that is a link, a part that leaves the root and anything
    /// that is not a regular file are refused.
    pub fn resolve(selection: &Selection) -> Result<Target, Error> {
        match selection {
            Selection::File(f) => {
                absolute(f, "selected file")?;
                let (parent, name) = (folder_of(f), f.file_name().unwrap_or_default());
                let pdir = open_dir(parent)?;
                let link = link_at(&pdir, Path::new(name), f)?;
                let cparent = canonical(parent)?;
                let target = if link.is_some() {
                    canonical(f)?
                } else {
                    cparent.join(name)
                };
                let tparent = folder_of(&target).to_path_buf();
                refuse_windows_drive(&[&cparent, &tparent, &target])?;
                let dir = open_dir(&tparent)?;
                let parent_id = dir_id(&dir, &tparent)?;
                let name = on_disk_name(
                    &dir,
                    Path::new(target.file_name().unwrap_or_default()),
                    &target,
                )?;
                let target = tparent.join(&name);
                Target::finish(selection, link, None, target, dir, parent_id, name)
            }
            Selection::Root { root, path } => {
                absolute(root, "selected root")?;
                let parts: Vec<&str> = path.split('/').collect();
                if path.is_empty()
                    || parts
                        .iter()
                        .any(|p| p.is_empty() || *p == "." || *p == "..")
                    || path.contains('\0')
                    // A backslash is a separator on Windows: refused everywhere, so a part never
                    // holds a `..` or a link the checks below would not see.
                    || path.contains('\\')
                {
                    return Err(Error::invalid_request(format!(
                        "the path must stay inside the selected root, as /-separated parts with no . or .. and no backslash: {path:?}"
                    )));
                }
                #[cfg(windows)]
                for part in &parts {
                    if let Some(why) = pluxy_files::windows_name_problem(part) {
                        return Err(Error::invalid_request(format!(
                            "{part:?} in the path is not a name this program writes on Windows: {why}"
                        )));
                    }
                }
                let (rparent, rname) = (folder_of(root), root.file_name().unwrap_or_default());
                let link = link_at(&open_dir(rparent)?, Path::new(rname), root)?;
                let croot = canonical(root)?;
                refuse_windows_drive(&[&canonical(rparent)?, &croot])?;
                let mut dir = open_dir(&croot)?;
                let root_id = dir_id(&dir, &croot)?;
                let mut at = croot.clone();
                for part in &parts[..parts.len() - 1] {
                    at.push(part);
                    let m = dir
                        .symlink_metadata(part)
                        .map_err(|e| io(&format!("{}", at.display()), e))?;
                    if m.file_type().is_symlink() {
                        return Err(Error::invalid_request(format!(
                            "{} is a link inside the selected root; select it itself",
                            at.display()
                        )));
                    }
                    if !m.is_dir() {
                        return Err(Error::invalid_request(format!(
                            "{} is not a folder",
                            at.display()
                        )));
                    }
                    let next = dir
                        .open_dir(part)
                        .map_err(|e| io(&format!("{}", at.display()), e))?;
                    if dir_id(&next, &at)? != Id::of(&m)? {
                        return Err(changed(&at, "a folder was replaced"));
                    }
                    dir = next;
                }
                let last = parts[parts.len() - 1];
                let last = on_disk_name(&dir, Path::new(last), &at.join(last))?;
                // Part by part: a long-form Windows path (`\\?\…`) takes `/` as a letter.
                let mut target = croot.clone();
                target.extend(&parts[..parts.len() - 1]);
                target.push(&last);
                // A folder inside the root, or the file itself, may be a mount of its own.
                refuse_windows_drive(&[&at, &target])?;
                let parent_id = dir_id(&dir, &at)?;
                Target::finish(
                    selection,
                    link,
                    Some((croot, root_id)),
                    target,
                    dir,
                    parent_id,
                    last,
                )
            }
        }
    }

    fn finish(
        selection: &Selection,
        link: Option<(String, Id)>,
        root: Option<(PathBuf, Id)>,
        target: PathBuf,
        dir: Dir,
        parent_id: Id,
        name: PathBuf,
    ) -> Result<Target, Error> {
        let (link, link_id) = link.map_or((None, None), |(t, i)| (Some(t), Some(i)));
        let (root, root_id) = root.map_or((None, None), |(r, i)| (Some(r), Some(i)));
        let mut t = Target {
            pin: Pin {
                selection: selection.clone(),
                link,
                link_id,
                root,
                root_id,
                target,
                parent_id,
                file_id: None,
                sha256: None,
            },
            dir,
            name,
            pinned: false,
        };
        let (id, bytes) = t.read_now()?;
        t.pin.file_id = id;
        t.pin.sha256 = sha256_of(bytes.as_deref());
        Ok(t)
    }

    /// Resolves pin's selection again: the target, if it still resolves exactly as pinned, the
    /// file's identity and bytes included.
    pub fn open(pin: &Pin) -> Result<Target, Error> {
        let t = Target::place(pin)?;
        if t.pin.file_id != pin.file_id {
            return Err(changed(&pin.target, "the file was replaced"));
        }
        if t.pin.sha256 != pin.sha256 {
            return Err(Error::invalid_request(format!(
                "{} was modified since the preview; preview it again",
                pin.target.display()
            )));
        }
        Ok(t)
    }

    /// Resolves pin's selection again: the target, if it still resolves to the same place as
    /// pinned, whatever its file's identity and bytes are now.
    pub fn place(pin: &Pin) -> Result<Target, Error> {
        let t = Target::resolve(&pin.selection)?;
        if !t.pin.same_place(pin) {
            return Err(changed(&pin.target, "the link, or what it resolves to"));
        }
        Ok(t)
    }

    /// The file pin names, opened at its pinned target path, whatever the selection resolves to
    /// now: for reconciling what a journal wrote there. The target's folder must still be the
    /// pinned one.
    pub fn pinned(pin: &Pin) -> Result<Target, Error> {
        let folder = folder_of(&pin.target);
        refuse_windows_drive(&[&canonical(folder)?, &pin.target])?;
        let dir = open_dir(folder)?;
        if dir_id(&dir, folder)? != pin.parent_id {
            return Err(changed(&pin.target, "its folder was replaced"));
        }
        let name = PathBuf::from(pin.target.file_name().unwrap_or_default());
        let mut t = Target {
            pin: pin.clone(),
            dir,
            name,
            pinned: true,
        };
        let (id, bytes) = t.read_now()?;
        t.pin.file_id = id;
        t.pin.sha256 = sha256_of(bytes.as_deref());
        Ok(t)
    }

    /// Whether the selection still resolves to this target's folder and name, as pinned (the
    /// file itself is checked by the caller); for a [`Target::pinned`] one, whether the target's
    /// folder path still opens the pinned folder.
    pub fn check_place(&self) -> Result<(), Error> {
        if self.pinned {
            let folder = folder_of(&self.pin.target);
            if dir_id(&open_dir(folder)?, folder)? != self.pin.parent_id {
                return Err(changed(&self.pin.target, "its folder was replaced"));
            }
            return Ok(());
        }
        let now = Target::resolve(&self.pin.selection)?;
        if !now.pin.same_place(&self.pin) {
            return Err(changed(
                &self.pin.target,
                "the link, or what it resolves to",
            ));
        }
        Ok(())
    }

    /// The file's identity and bytes now, through the folder handle; (None, None) when missing.
    pub fn read_now(&self) -> Result<(Option<Id>, Option<Vec<u8>>), Error> {
        let at = &self.pin.target;
        let m = match self.dir.symlink_metadata(&self.name) {
            Ok(m) => m,
            Err(e) if e.kind() == ErrorKind::NotFound => return Ok((None, None)),
            Err(e) => return Err(io(&format!("{}", at.display()), e)),
        };
        if m.file_type().is_symlink() {
            return Err(Error::invalid_request(format!(
                "{} is a link; select it itself",
                at.display()
            )));
        }
        if !m.is_file() {
            return Err(Error::invalid_request(format!(
                "{} is not a regular file",
                at.display()
            )));
        }
        let mut f = self
            .dir
            .open(&self.name)
            .map_err(|e| io(&format!("{}", at.display()), e))?;
        let fm = f
            .metadata()
            .map_err(|e| io(&format!("{}", at.display()), e))?;
        if Id::of(&fm)? != Id::of(&m)? || !fm.is_file() {
            return Err(changed(at, "the file was replaced while it was read"));
        }
        let mut bytes = Vec::new();
        (&mut f)
            .take(MAX_FILE as u64 + 1)
            .read_to_end(&mut bytes)
            .map_err(|e| io(&format!("{}", at.display()), e))?;
        if bytes.len() > MAX_FILE {
            return Err(Error::invalid_request(format!(
                "{} is larger than {} KiB",
                at.display(),
                MAX_FILE >> 10
            )));
        }
        Ok((Some(Id::of(&m)?), Some(bytes)))
    }

    /// The file's permissions now (see [`Mode`]), None when missing.
    pub fn mode_now(&self) -> Result<Option<Mode>, Error> {
        let at = &self.pin.target;
        let m = match self.dir.symlink_metadata(&self.name) {
            Ok(m) => m,
            Err(e) if e.kind() == ErrorKind::NotFound => return Ok(None),
            Err(e) => return Err(io(&format!("{}", at.display()), e)),
        };
        #[cfg(unix)]
        {
            use cap_std::fs::PermissionsExt as _;
            Ok(Some(Mode(m.permissions().mode() & 0o777)))
        }
        // The access list is read on the file itself, checked to be the one just looked at.
        #[cfg(windows)]
        {
            if m.file_type().is_symlink() || !m.is_file() {
                return Err(Error::invalid_request(format!(
                    "{} is a link or not a regular file",
                    at.display()
                )));
            }
            let f = self
                .dir
                .open(&self.name)
                .map_err(|e| io(&format!("{}", at.display()), e))?;
            let fm = f
                .metadata()
                .map_err(|e| io(&format!("{}", at.display()), e))?;
            if Id::of(&fm)? != Id::of(&m)? {
                return Err(changed(at, "the file was replaced while it was read"));
            }
            pluxy_files::access_list(&f.into_std())
                .map(|sddl| Some(Mode(sddl)))
                .map_err(|e| io(&format!("{}", at.display()), e))
        }
    }

    /// Removes temporary files a write left beside the target (`.<name>.pluxy-<hex>.tmp`, as
    /// [`Target::replace`] names them) when the program stopped between writing one and renaming
    /// it; they may hold the new bytes. The names removed.
    pub fn remove_temps(&self) -> Result<Vec<String>, Error> {
        remove_temps_in(&self.dir, &self.name, &self.pin.target)
    }

    /// The same, for a pin whose target cannot be opened as a [`Target`] now (it became a link,
    /// or grew past the size limit): through the pinned folder, never reading the file. The
    /// folder must still be the pinned one.
    pub fn remove_temps_of(pin: &Pin) -> Result<Vec<String>, Error> {
        let folder = folder_of(&pin.target);
        let dir = open_dir(folder)?;
        if dir_id(&dir, folder)? != pin.parent_id {
            return Err(changed(&pin.target, "its folder was replaced"));
        }
        let name = PathBuf::from(pin.target.file_name().unwrap_or_default());
        remove_temps_in(&dir, &name, &pin.target)
    }

    /// Whether the file is now the one expected: its identity (when given) and its SHA-256.
    fn is(&self, id: Option<Option<Id>>, sha: &Option<String>) -> Result<bool, Error> {
        let (now_id, bytes) = self.read_now()?;
        Ok(id.is_none_or(|id| id == now_id) && sha256_of(bytes.as_deref()) == *sha)
    }

    /// Replaces the file, which must still have identity `from_id` (when given) and SHA-256
    /// `from`, with bytes (or removes it, for None): a new file in the same folder, renamed over
    /// it, so a link to it stays a link. The selection's resolution is checked again before the
    /// rename and after it, and the file after the write must be the one written. Returns the
    /// new file's identity (None once removed). The new file's permission bits (setuid, setgid
    /// and sticky left out) follow `rule` (see [`WriteMode`]); it is written 0600 and given them
    /// just before the rename. It is a new file: hard links to the old one keep the old bytes, and
    /// its owner, ACLs and extended attributes are not copied.
    pub fn replace(
        &self,
        from_id: Option<Option<Id>>,
        from: &Option<String>,
        bytes: Option<&[u8]>,
        mode: Option<&Mode>,
        rule: WriteMode,
    ) -> Result<Option<Id>, Failed> {
        let at = &self.pin.target;
        let modified = || {
            Error::invalid_request(format!(
                "{} was modified by another program meanwhile",
                at.display()
            ))
        };
        let before = |e: Error| Failed {
            error: e,
            written: None,
        };
        self.check_place().map_err(before)?;
        if !self.is(from_id, from).map_err(before)? {
            return Err(before(modified()));
        }
        let Some(bytes) = bytes else {
            self.dir
                .remove_file(&self.name)
                .map_err(|e| before(io(&format!("removing {}", at.display()), e)))?;
            let after = |e: Error| Failed {
                error: e,
                written: Some(None),
            };
            self.sync().map_err(after)?;
            self.check_place().map_err(after)?;
            if self.read_now().map_err(after)?.0.is_some() {
                return Err(after(changed(at, "a file appeared where it was removed")));
            }
            return Ok(None);
        };
        if bytes.len() > MAX_FILE {
            return Err(before(Error::invalid_request(format!(
                "the new {} is larger than {} KiB",
                at.display(),
                MAX_FILE >> 10
            ))));
        }
        // The permissions now; failing to read them fails the write (never taken as no file).
        let now = self.mode_now().map_err(before)?;
        let give = protection(rule, mode, now);
        let tmp = PathBuf::from(format!(
            ".{}.pluxy-{}.tmp",
            self.name.to_string_lossy(),
            random_hex().map_err(before)?
        ));
        let beside = |e| io(&format!("writing beside {}", at.display()), e);
        let written = self.write_temp(&tmp, bytes).and_then(|(id, f)| {
            self.check_place()?;
            if !self.is(from_id, from)? {
                return Err(modified());
            }
            protect(&f, &give).map_err(beside)?;
            Ok((id, f))
        });
        let (new_id, f) = match written {
            Ok(w) => w,
            Err(e) => {
                let _ = self.dir.remove_file(&tmp);
                return Err(before(e));
            }
        };
        if let Err(e) = rename_temp(f, &self.dir, &tmp, &self.name) {
            let _ = self.dir.remove_file(&tmp);
            return Err(before(io(&format!("replacing {}", at.display()), e)));
        }
        let after = |e: Error| Failed {
            error: e,
            written: Some(Some(new_id)),
        };
        self.sync().map_err(after)?;
        self.check_place().map_err(after)?;
        if !self
            .is(Some(Some(new_id)), &Some(sha256(bytes)))
            .map_err(after)?
        {
            return Err(after(modified()));
        }
        Ok(Some(new_id))
    }

    /// Writes bytes to a new file tmp beside the target, readable only by this user (0600; on
    /// Windows private, and open to no other handle) until the caller gives it the target's
    /// permissions just before renaming it: its identity and handle.
    pub(crate) fn write_temp(
        &self,
        tmp: &Path,
        bytes: &[u8],
    ) -> Result<(Id, std::fs::File), Error> {
        let beside = |e| io(&format!("writing beside {}", self.pin.target.display()), e);
        let f = write_private_temp(&self.dir, tmp, bytes).map_err(beside)?;
        let f = cap_std::fs::File::from_std(f);
        let m = f.metadata().map_err(beside)?;
        Ok((Id::of(&m)?, f.into_std()))
    }

    /// Flushes the folder, so a rename or removal in it lasts.
    fn sync(&self) -> Result<(), Error> {
        sync_dir(&self.dir).map_err(|e| io(&format!("{}", self.pin.target.display()), e))
    }
}

/// The permissions a written file gets (see [`WriteMode`]): on Linux and macOS its bits.
#[cfg(unix)]
type Give = u32;

/// On Windows an access list in SDDL, or None to keep it private (as the temporary file is made).
#[cfg(windows)]
type Give = Option<String>;

#[cfg(unix)]
fn protection(rule: WriteMode, given: Option<&Mode>, now: Option<Mode>) -> Give {
    let bits = match (rule, given, now) {
        (WriteMode::Exact, Some(m), _) => m.0 & 0o777,
        (_, _, Some(m)) => m.0,
        (_, given, None) => given.map_or(NEW_FILE_MODE, |m| m.0) & 0o777,
    };
    if rule == WriteMode::OwnerOnly {
        bits & !0o077
    } else {
        bits
    }
}

#[cfg(windows)]
fn protection(rule: WriteMode, given: Option<&Mode>, now: Option<Mode>) -> Give {
    match (rule, given, now) {
        (WriteMode::OwnerOnly, _, _) => None,
        (WriteMode::Exact, Some(m), _) => Some(m.0.clone()),
        (_, _, Some(m)) => Some(m.0),
        (_, given, None) => given.map(|m| m.0.clone()),
    }
}

/// Gives the written file f its permissions.
fn protect(f: &std::fs::File, give: &Give) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        f.set_permissions(std::fs::Permissions::from_mode(*give))
    }
    #[cfg(windows)]
    match give {
        Some(sddl) => pluxy_files::set_access_list(f, sddl),
        None => Ok(()),
    }
}

/// Writes bytes to a new file tmp in dir, private from the moment it is made (on Linux and macOS
/// created 0600; on Windows created with its protected list in one step, relative to dir's handle,
/// and shared with no other handle, so nothing else can open it until it is renamed and closed),
/// and flushes it: its handle, which on Windows the rename goes through ([`rename_temp`]).
pub(crate) fn write_private_temp(
    dir: &Dir,
    tmp: &Path,
    bytes: &[u8],
) -> std::io::Result<std::fs::File> {
    #[cfg(unix)]
    let mut f = {
        use cap_std::fs::OpenOptionsExt as _;
        let f = dir
            .open_with(
                tmp,
                OpenOptions::new()
                    .write(true)
                    .create_new(true)
                    .mode(NEW_FILE_MODE),
            )?
            .into_std();
        // 0600 whatever the umask left.
        pluxy_files::make_private_open(&f)?;
        f
    };
    #[cfg(windows)]
    let mut f = pluxy_files::create_private_in(&borrowed(dir), tmp.as_os_str())?;
    f.write_all(bytes)?;
    f.sync_all()?;
    Ok(f)
}

/// Windows: dir's handle as a File, borrowed (the ManuallyDrop never closes it).
#[cfg(windows)]
fn borrowed(dir: &Dir) -> std::mem::ManuallyDrop<std::fs::File> {
    use std::os::windows::io::{AsRawHandle, FromRawHandle};
    std::mem::ManuallyDrop::new(unsafe { std::fs::File::from_raw_handle(dir.as_raw_handle()) })
}

/// Renames the temporary file tmp in dir, open as f, to name, replacing what is there: on Linux
/// and macOS through dir's handle, on Windows through f's own handle (no other could open it).
pub(crate) fn rename_temp(
    f: std::fs::File,
    dir: &Dir,
    tmp: &Path,
    name: &Path,
) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        drop(f);
        dir.rename(tmp, dir, name)
    }
    #[cfg(windows)]
    {
        let _ = tmp;
        let r = pluxy_files::rename_open(&f, &borrowed(dir), name.as_os_str());
        drop(f);
        r
    }
}

/// Flushes dir, so a rename or removal in it lasts. Windows flushes a folder's entries with the
/// file system's own log; a folder cannot be flushed through a handle opened to read it.
pub(crate) fn sync_dir(dir: &Dir) -> std::io::Result<()> {
    #[cfg(unix)]
    return dir.open(".").and_then(|f| f.sync_all());
    #[cfg(windows)]
    {
        let _ = dir;
        Ok(())
    }
}

/// A failed [`Target::replace`]: why, and, when the file was already replaced (or removed), the
/// identity of what is there now, so the caller puts it back.
#[derive(Debug)]
pub struct Failed {
    pub error: Error,
    pub written: Option<Option<Id>>,
}

/// 16 random hexadecimal digits.
pub(crate) fn random_hex() -> Result<String, Error> {
    let mut b = [0u8; 8];
    getrandom::fill(&mut b).map_err(|e| Error::unavailable(format!("no randomness: {e}")))?;
    Ok(b.iter().map(|b| format!("{b:02x}")).collect())
}

/// Removes the temporary files of a write to `name` in `dir` (`.<name>.pluxy-<16 hex>.tmp`).
fn remove_temps_in(dir: &Dir, name: &Path, at: &Path) -> Result<Vec<String>, Error> {
    let prefix = format!(".{}.pluxy-", name.to_string_lossy());
    let mut removed = Vec::new();
    let entries = dir
        .entries()
        .map_err(|e| io(&format!("{}", at.display()), e))?;
    for e in entries.flatten() {
        let name = e.file_name().to_string_lossy().into_owned();
        let hex = name
            .strip_prefix(&prefix)
            .and_then(|r| r.strip_suffix(".tmp"))
            .is_some_and(|h| h.len() == 16 && h.bytes().all(|b| b.is_ascii_hexdigit()));
        if hex && dir.remove_file(&name).is_ok() {
            removed.push(name);
        }
    }
    Ok(removed)
}
