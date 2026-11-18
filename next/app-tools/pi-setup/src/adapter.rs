//! The CLIProxyAPI provider adapter for Pi this program carries (`../adapter`, README.md): the
//! upstream `@router-for-me/pi-cliproxyapi-provider` package, reviewed and patched, written into
//! the agent folder as a local Pi package, `pluxy-cliproxyapi-provider/`. Pi loads a local package
//! from its folder and never installs or updates it, so nothing here runs Pi or npm, and the copy
//! changes only when this program writes the one it carries.
//!
//! The folder is pluxy's only when it holds pluxy's marker file; any other folder of that name is
//! the person's, never written or removed.

use crate::pi::FOLDER;
use pluxy_program::Error;
use serde_json::Value;
use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::Write;
#[cfg(unix)]
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

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
    let marker: Option<Value> = fs::read(dir.join(MARKER))
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok());
    match marker {
        Some(m) if m["package"] == PACKAGE => {
            State::Installed(m["version"].as_str().map(String::from))
        }
        _ => State::Foreign,
    }
}

fn io(what: &str, path: &Path, e: std::io::Error) -> Error {
    Error::unavailable(format!("cannot {what} {}: {e}", path.display()))
}

fn foreign(agent: &Path) -> Error {
    Error::invalid_request(format!(
        "{} is not pluxy's: move it away to install pluxy's Pi plugin",
        folder(agent).display()
    ))
}

/// A sibling of the adapter's folder that no one else uses: `.pluxy-cliproxyapi-provider.<what>-…`.
fn sibling(agent: &Path, what: &str) -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or_default();
    agent.join(format!(".{FOLDER}.{what}-{}-{nanos}", std::process::id()))
}

/// Writes the carried files into the new folder dir: on Linux and macOS folders 0755 and files
/// 0644 (code anyone may read, as npm installs it); on Windows each takes its folder's access list.
fn write_all(dir: &Path) -> Result<(), Error> {
    let mut builder = DirBuilder::new();
    #[cfg(unix)]
    builder.mode(0o755);
    builder.create(dir).map_err(|e| io("create", dir, e))?;
    builder.recursive(true);
    for (name, bytes) in FILES
        .iter()
        .copied()
        .chain([(MARKER, marker(VERSION).as_bytes())])
    {
        let path = dir.join(name);
        if let Some(parent) = path.parent() {
            builder
                .create(parent)
                .map_err(|e| io("create", parent, e))?;
        }
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        options.mode(0o644);
        let mut f: File = options.open(&path).map_err(|e| io("create", &path, e))?;
        f.write_all(bytes)
            .and_then(|_| f.sync_all())
            .map_err(|e| io("write", &path, e))?;
    }
    pluxy_files::sync_dir(dir).map_err(|e| io("sync", dir, e))
}

/// Puts the carried copy in place: a new folder written beside it, then renamed over pluxy's
/// older copy (moved aside and removed) or into the empty place. Refuses a folder that is not
/// pluxy's.
fn put(agent: &Path) -> Result<(), Error> {
    let dest = folder(agent);
    let old = match state(agent) {
        State::Foreign => return Err(foreign(agent)),
        State::Absent => None,
        State::Installed(_) => Some(sibling(agent, "old")),
    };
    let new = sibling(agent, "new");
    if let Err(e) = write_all(&new) {
        let _ = fs::remove_dir_all(&new);
        return Err(e);
    }
    if let Some(old) = &old {
        if let Err(e) = fs::rename(&dest, old) {
            let _ = fs::remove_dir_all(&new);
            return Err(io("move aside", &dest, e));
        }
    }
    if let Err(e) = fs::rename(&new, &dest) {
        let _ = fs::remove_dir_all(&new);
        if let Some(old) = &old {
            let _ = fs::rename(old, &dest);
        }
        return Err(io("put in place", &dest, e));
    }
    if let Some(old) = old {
        fs::remove_dir_all(&old).map_err(|e| io("remove", &old, e))?;
    }
    pluxy_files::sync_dir(agent).map_err(|e| io("sync", agent, e))
}

/// Install Pi Plugin: puts the carried copy in agent unless pluxy's copy is there already (an
/// update is explicit). True when it installed one.
pub fn ensure(agent: &Path) -> Result<bool, Error> {
    match state(agent) {
        State::Installed(_) => Ok(false),
        State::Foreign => Err(foreign(agent)),
        State::Absent => put(agent).map(|_| true),
    }
}

/// Update Pi Plugin: replaces pluxy's copy with the carried one.
pub fn update(agent: &Path) -> Result<(), Error> {
    match state(agent) {
        State::Installed(_) => put(agent),
        State::Foreign => Err(foreign(agent)),
        State::Absent => Err(Error::invalid_request(format!(
            "the Pi plugin is not installed in {}",
            agent.display()
        ))),
    }
}

/// Removes pluxy's copy (with the cache the adapter keeps in it); anything else stays. True when
/// it removed one.
pub fn remove(agent: &Path) -> Result<bool, Error> {
    if !matches!(state(agent), State::Installed(_)) {
        return Ok(false);
    }
    // Moved aside first: a stop part way through leaves a hidden sibling, never a half-removed
    // folder under the plugin's name that would then read as someone else's.
    let dest = folder(agent);
    let old = sibling(agent, "old");
    fs::rename(&dest, &old).map_err(|e| io("move aside", &dest, e))?;
    pluxy_files::sync_dir(agent).map_err(|e| io("sync", agent, e))?;
    fs::remove_dir_all(&old).map_err(|e| io("remove", &old, e))?;
    Ok(true)
}

#[cfg(test)]
pub fn files() -> &'static [(&'static str, &'static [u8])] {
    FILES
}
