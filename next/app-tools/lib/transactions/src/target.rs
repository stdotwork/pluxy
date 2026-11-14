//! Selected targets: resolving a selected config file or a file in a selected root (either may be
//! a link, as stow and chezmoi make), pinning what it resolves to, checking it again, and reading
//! and writing the file through a handle on its folder (cap-std), never by a path again.

use cap_std::ambient_authority;
use cap_std::fs::{Dir, MetadataExt, OpenOptions, OpenOptionsExt as _, PermissionsExt as _};
use pluxy_program::Error;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::io::{ErrorKind, Read, Write};
use std::os::unix::fs::PermissionsExt;
use std::path::{Component, Path, PathBuf};

/// The largest file the library reads or writes: its backups must fit the vault's 512 KiB of
/// base64.
pub const MAX_FILE: usize = 256 << 10;

/// The mode of a file the library creates (config files may hold keys).
const NEW_FILE_MODE: u32 = 0o600;

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
    fn of(m: &cap_std::fs::Metadata) -> Id {
        Id {
            dev: m.dev(),
            ino: m.ino(),
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

/// An absolute path with no `.` or `..` parts, as UTF-8.
fn absolute(p: &Path, what: &str) -> Result<(), Error> {
    let ok = p.is_absolute()
        && p.to_str().is_some()
        && p.components()
            .skip(1)
            .all(|c| matches!(c, Component::Normal(_)))
        && p.file_name().is_some();
    if ok {
        Ok(())
    } else {
        Err(Error::invalid_request(format!(
            "the {what} path must be absolute, in UTF-8, with no . or .. parts: {}",
            p.display()
        )))
    }
}

/// Opens folder by its (resolved) path.
fn open_dir(folder: &Path) -> Result<Dir, Error> {
    Dir::open_ambient_dir(folder, ambient_authority())
        .map_err(|e| io(&format!("{}", folder.display()), e))
}

fn dir_id(d: &Dir, at: &Path) -> Result<Id, Error> {
    d.dir_metadata()
        .map(|m| Id::of(&m))
        .map_err(|e| io(&format!("{}", at.display()), e))
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
            Ok(Some((text, Id::of(&m))))
        }
        Ok(_) => Ok(None),
        Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
        Err(e) => Err(io(&format!("{}", at.display()), e)),
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
    Ok(c)
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
                let (parent, name) = (
                    f.parent().unwrap_or(Path::new("/")),
                    f.file_name().unwrap_or_default(),
                );
                let pdir = open_dir(parent)?;
                let link = link_at(&pdir, Path::new(name), f)?;
                let target = if link.is_some() {
                    canonical(f)?
                } else {
                    canonical(parent)?.join(name)
                };
                let tparent = target.parent().unwrap_or(Path::new("/"));
                let dir = open_dir(tparent)?;
                let parent_id = dir_id(&dir, tparent)?;
                let name = PathBuf::from(target.file_name().unwrap_or_default());
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
                {
                    return Err(Error::invalid_request(format!(
                        "the path must stay inside the selected root, as parts with no . or ..: {path:?}"
                    )));
                }
                let (rparent, rname) = (
                    root.parent().unwrap_or(Path::new("/")),
                    root.file_name().unwrap_or_default(),
                );
                let link = link_at(&open_dir(rparent)?, Path::new(rname), root)?;
                let croot = canonical(root)?;
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
                    if dir_id(&next, &at)? != Id::of(&m) {
                        return Err(changed(&at, "a folder was replaced"));
                    }
                    dir = next;
                }
                let parent_id = dir_id(&dir, &at)?;
                let last = parts[parts.len() - 1];
                let target = croot.join(path);
                Target::finish(
                    selection,
                    link,
                    Some((croot, root_id)),
                    target,
                    dir,
                    parent_id,
                    PathBuf::from(last),
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
        let folder = pin.target.parent().unwrap_or(Path::new("/"));
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
            let folder = self.pin.target.parent().unwrap_or(Path::new("/"));
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
        if Id::of(&fm) != Id::of(&m) || !fm.is_file() {
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
        Ok((Some(Id::of(&m)), Some(bytes)))
    }

    /// The file's permission bits now (setuid, setgid and sticky left out), None when missing.
    pub fn mode_now(&self) -> Result<Option<u32>, Error> {
        match self.dir.symlink_metadata(&self.name) {
            Ok(m) => Ok(Some(m.permissions().mode() & 0o777)),
            Err(e) if e.kind() == ErrorKind::NotFound => Ok(None),
            Err(e) => Err(io(&format!("{}", self.pin.target.display()), e)),
        }
    }

    /// Removes temporary files a write left beside the target (`.<name>.pluxy-<hex>.tmp`, as
    /// [`Target::replace`] names them) when the program stopped between writing one and renaming
    /// it; they may hold the new bytes. The names removed.
    pub fn remove_temps(&self) -> Result<Vec<String>, Error> {
        let prefix = format!(".{}.pluxy-", self.name.to_string_lossy());
        let mut removed = Vec::new();
        let entries = self
            .dir
            .entries()
            .map_err(|e| io(&format!("{}", self.pin.target.display()), e))?;
        for e in entries.flatten() {
            let name = e.file_name().to_string_lossy().into_owned();
            let hex = name
                .strip_prefix(&prefix)
                .and_then(|r| r.strip_suffix(".tmp"))
                .is_some_and(|h| h.len() == 16 && h.bytes().all(|b| b.is_ascii_hexdigit()));
            if hex && self.dir.remove_file(&name).is_ok() {
                removed.push(name);
            }
        }
        Ok(removed)
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
    /// new file's identity (None once removed). The new file keeps the old one's permission bits
    /// (setuid, setgid and sticky left out), or gets `mode` (else 0600) when there was none; it is
    /// written 0600 and given them just before the rename. It is a new file: hard links to the
    /// old one keep the old bytes, and its owner, ACLs and extended attributes are not copied.
    pub fn replace(
        &self,
        from_id: Option<Option<Id>>,
        from: &Option<String>,
        bytes: Option<&[u8]>,
        mode: Option<u32>,
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
        let mode = match self.mode_now() {
            Ok(Some(m)) => m,
            _ => mode.unwrap_or(NEW_FILE_MODE) & 0o777,
        };
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
            f.set_permissions(std::fs::Permissions::from_mode(mode))
                .map_err(beside)?;
            Ok(id)
        });
        let new_id = match written {
            Ok(id) => id,
            Err(e) => {
                let _ = self.dir.remove_file(&tmp);
                return Err(before(e));
            }
        };
        if let Err(e) = self.dir.rename(&tmp, &self.dir, &self.name) {
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

    /// Writes bytes to a new file tmp beside the target, readable only by this user (0600) until
    /// the caller gives it the target's mode just before renaming it: its identity and handle.
    pub(crate) fn write_temp(
        &self,
        tmp: &Path,
        bytes: &[u8],
    ) -> Result<(Id, std::fs::File), Error> {
        let beside = |e| io(&format!("writing beside {}", self.pin.target.display()), e);
        let mut f = self
            .dir
            .open_with(
                tmp,
                OpenOptions::new()
                    .write(true)
                    .create_new(true)
                    .mode(NEW_FILE_MODE),
            )
            .map_err(beside)?
            .into_std();
        f.set_permissions(std::fs::Permissions::from_mode(NEW_FILE_MODE))
            .map_err(beside)?;
        f.write_all(bytes)
            .and_then(|_| f.sync_all())
            .map_err(beside)?;
        let f = cap_std::fs::File::from_std(f);
        let m = f.metadata().map_err(beside)?;
        Ok((Id::of(&m), f.into_std()))
    }

    /// Flushes the folder, so a rename or removal in it lasts.
    fn sync(&self) -> Result<(), Error> {
        self.dir
            .open(".")
            .and_then(|f| f.sync_all())
            .map_err(|e| io(&format!("{}", self.pin.target.display()), e))
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
