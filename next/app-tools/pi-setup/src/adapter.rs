//! The CLIProxyAPI provider adapter for Pi this program carries (`../adapter`, README.md): the
//! upstream `@router-for-me/pi-cliproxyapi-provider` package, reviewed and patched, written into
//! the agent folder as a local Pi package, `pluxy-cliproxyapi-provider/`. Pi loads a local package
//! from its folder and never installs or updates it, so nothing here runs Pi or npm, and the copy
//! changes only when this program writes the one it carries.
//!
//! The folder is pluxy's only when it holds pluxy's marker file; any other folder of that name is
//! the person's, never written or removed. The folder is written, replaced and removed through
//! pluxy-transactions' [`Folder`]: through a handle on the agent folder, the copy moved aside
//! checked to be the one pinned, each hidden copy removed or put back only while it is the folder
//! the operation recorded, each change journalled and reconciled at the next start.

use crate::pi::FOLDER;
use pluxy_program::Error;
use pluxy_transactions::{sha256_of, Folder, Id, Pin, Selection, Target, Transactions};
use serde_json::Value;
use std::fs;
use std::path::{Path, PathBuf};

/// The version of the copy this program carries: the upstream version and pluxy's patch level.
pub const VERSION: &str = "1.4.25-pluxy.1";

/// The marker that makes the folder pluxy's, with this package and the copy's version.
pub const MARKER: &str = ".pluxy-adapter.json";
const PACKAGE: &str = "pluxy/pi-setup";
const ADAPTER: &str = "@router-for-me/pi-cliproxyapi-provider";

/// The adapter's files, by path inside its folder.
const FILES: &[(&str, &[u8])] = &[
    ("LICENSE", include_bytes!("../adapter/LICENSE")),
    ("package.json", include_bytes!("../adapter/package.json")),
    (
        "extensions/auto-compact.ts",
        include_bytes!("../adapter/extensions/auto-compact.ts"),
    ),
    (
        "extensions/codex-stream.ts",
        include_bytes!("../adapter/extensions/codex-stream.ts"),
    ),
    (
        "extensions/fast-footer.ts",
        include_bytes!("../adapter/extensions/fast-footer.ts"),
    ),
    (
        "extensions/fast.ts",
        include_bytes!("../adapter/extensions/fast.ts"),
    ),
    (
        "extensions/index.ts",
        include_bytes!("../adapter/extensions/index.ts"),
    ),
    (
        "extensions/lib.ts",
        include_bytes!("../adapter/extensions/lib.ts"),
    ),
    (
        "extensions/pause.ts",
        include_bytes!("../adapter/extensions/pause.ts"),
    ),
    (
        "extensions/retry.ts",
        include_bytes!("../adapter/extensions/retry.ts"),
    ),
    (
        "extensions/tps.ts",
        include_bytes!("../adapter/extensions/tps.ts"),
    ),
];

/// The marker's bytes for a version.
pub fn marker(version: &str) -> String {
    format!(
        "{{\n  \"package\": \"{PACKAGE}\",\n  \"adapter\": \"{ADAPTER}\",\n  \"version\": \"{version}\"\n}}\n"
    )
}

/// What the agent folder holds where the adapter goes.
#[derive(Debug, PartialEq, Eq)]
pub enum State {
    /// Nothing.
    Absent,
    /// pluxy's copy, of this version (None: the marker names none).
    Installed(Option<String>),
    /// Something that is not pluxy's.
    Foreign,
}

impl State {
    pub fn to_json(&self) -> Value {
        let (installed, version, foreign) = match self {
            State::Absent => (false, None, false),
            State::Installed(v) => (true, v.clone(), false),
            State::Foreign => (false, None, true),
        };
        serde_json::json!({
            "installed": installed,
            "version": version,
            "bundled": VERSION,
            "update": installed && version.as_deref() != Some(VERSION),
            "foreign": foreign,
        })
    }
}

/// The adapter's folder in agent.
pub fn folder(agent: &Path) -> PathBuf {
    agent.join(FOLDER)
}

/// What agent holds where the adapter goes.
pub fn state(agent: &Path) -> State {
    let dir = folder(agent);
    match fs::symlink_metadata(&dir) {
        Err(_) => return State::Absent,
        Ok(m) if !m.is_dir() => return State::Foreign,
        Ok(_) => {}
    }
    of_marker(fs::read(dir.join(MARKER)).ok().as_deref())
}

/// Whose a plugin folder is by its marker's bytes (None: no marker): pluxy's copy, of the version
/// it names, or someone else's.
fn of_marker(bytes: Option<&[u8]>) -> State {
    let marker: Option<Value> = bytes.and_then(|b| serde_json::from_slice(b).ok());
    match marker {
        Some(m) if m["package"] == PACKAGE => {
            State::Installed(m["version"].as_str().map(String::from))
        }
        _ => State::Foreign,
    }
}

fn foreign(agent: &Path) -> Error {
    Error::invalid_request(format!(
        "{} is not pluxy's: move it away to install pluxy's Pi plugin",
        folder(agent).display()
    ))
}

/// The refusal of a plugin folder that is not the one the preview pinned, saying why.
pub fn changed(agent: &Path, why: &str) -> Error {
    Error::invalid_request(format!(
        "the Pi plugin's folder changed since the preview ({why}): {}; preview it again",
        folder(agent).display()
    ))
}

/// What the plugin's pin selects in agent: pluxy's marker in the plugin's folder, so the pin holds
/// the agent folder's link and identity, the plugin folder's identity and the marker's identity
/// and bytes (the copy's version).
pub fn selection(agent: &Path) -> Selection {
    Selection::Root {
        root: agent.to_path_buf(),
        path: format!("{FOLDER}/{MARKER}"),
    }
}

/// pluxy's copy the preview pinned, held open through the agent folder it pinned: refused as
/// changed since the preview when it is not that copy now (the agent folder's link pointed
/// elsewhere, the agent or plugin folder moved or replaced, the marker changed).
pub fn pinned(agent: &Path, pin: &Pin) -> Result<Folder, Error> {
    Folder::pinned(pin).map_err(|e| {
        if e.message.contains("preview it again") {
            e
        } else {
            changed(agent, &e.message)
        }
    })
}

/// Puts the carried copy in folder's place (pluxy's copy pinned, or the empty place), through the
/// library: a new folder written beside it and renamed in, the old copy moved aside, checked to
/// be the one pinned, and removed once checked again; journalled, so the next start puts back what
/// a stop left aside while nothing else is under the name (else names it in its report). The copy
/// put, pinned.
fn put(tx: &Transactions, folder: &Folder) -> Result<Folder, Error> {
    let marker = marker(VERSION);
    let mut files: Vec<(&str, &[u8])> = FILES.to_vec();
    files.push((MARKER, marker.as_bytes()));
    tx.put_folder(crate::SET, folder, &files)
}

/// Install Pi Plugin: puts the carried copy in agent unless pluxy's copy is there already (an
/// update is explicit). The copy it installed, pinned in the agent folder held open (so a failed
/// Apply removes that very copy), or None when it kept the one there.
pub fn ensure(tx: &Transactions, agent: &Path) -> Result<Option<Folder>, Error> {
    match state(agent) {
        State::Installed(_) => Ok(None),
        State::Foreign => Err(foreign(agent)),
        State::Absent => put(tx, &Folder::empty(agent, FOLDER, MARKER)?).map(Some),
    }
}

/// Update Pi Plugin: replaces pluxy's copy the preview pinned (see [`pinned`]) with the carried
/// one.
pub fn update(tx: &Transactions, pinned: &Folder) -> Result<(), Error> {
    put(tx, pinned).map(|_| ())
}

/// Removes the copy pinned (with the cache the adapter keeps in it): the one the preview pinned
/// (see [`pinned`]) or the one [`ensure`] installed, through the agent folder held open, while it
/// is still that copy.
pub fn remove(tx: &Transactions, pinned: &Folder) -> Result<(), Error> {
    tx.remove_folder(crate::SET, pinned)
}

/// Removes pluxy's copy as it is now in the agent folder a write's file transaction pinned (root,
/// its resolved path, while the folder there is still the one of identity id), never through the
/// selected agent folder's link again; the copy is pinned and held open as it is read. Anything
/// else stays. True when it removed one.
pub fn remove_in(tx: &Transactions, root: &Path, id: Id) -> Result<bool, Error> {
    if !matches!(state(root), State::Installed(_)) {
        return Ok(false);
    }
    let t = Target::resolve(&selection(root))?;
    // Another folder at that path now: not the agent folder the files were written in.
    if t.pin.root_id != Some(id) {
        return Ok(false);
    }
    // pluxy's by the marker's bytes the pin holds, read again through its folder's handle.
    let (_, bytes) = t.read_now()?;
    if sha256_of(bytes.as_deref()) != t.pin.sha256
        || !matches!(of_marker(bytes.as_deref()), State::Installed(_))
    {
        return Ok(false);
    }
    // The handle on the plugin's folder closed first: Windows does not rename a folder held open.
    let pin = t.pin.clone();
    drop(t);
    tx.remove_folder(crate::SET, &Folder::pinned(&pin)?)?;
    Ok(true)
}

#[cfg(test)]
pub fn files() -> &'static [(&'static str, &'static [u8])] {
    FILES
}
