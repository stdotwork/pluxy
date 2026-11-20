//! pluxy/claude-desktop: sets up Claude Desktop to use pluxy's core as its third-party inference
//! gateway, and starts it, for pluxy's desktop app (README.md). Its operations: `preview` (read:
//! pins Claude Desktop's three third-party files and reads their values back, never the key),
//! `folders` (write: creates the default configuration library folder when Claude Desktop has not
//! yet), `apply`, `clear` and `restore` (write: each call asks the person; `apply` receives the
//! client key as a secret's value and writes it where Claude Desktop reads it), `status` (read:
//! the applied set, the originals kept for restore, the latest recovery reports), `recipe` (read:
//! how the launcher starts Claude Desktop) and the backup browser's standard operations. Writes go
//! through pluxy-transactions: link-preserving, journalled, with the originals and manual backups
//! in the host's vault.

mod files;

use files::{Checked, File, Settings, PROFILE_ID};
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{
    backup_call, data_dir, json_leaves, sha256_of, Pin, Selection, Target, Transactions,
};
use serde_json::{json, Value};
use std::ffi::OsString;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};

/// The one set this tool applies: the deployment file, the gateway profile and the library index,
/// in that order.
const SET: &str = "claude-desktop";

struct ClaudeDesktop;

/// What the tool reads of its environment: `std::env::var_os`, or a test's values.
type Var<'a> = &'a dyn Fn(&str) -> Option<OsString>;

/// A folder the environment names, when it is set and not empty.
fn folder_of(var: Var, name: &str) -> Option<PathBuf> {
    var(name).filter(|v| !v.is_empty()).map(PathBuf::from)
}

fn home_of(var: Var) -> Result<PathBuf, Error> {
    folder_of(var, "HOME").ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

fn home() -> Result<PathBuf, Error> {
    home_of(&|k| std::env::var_os(k))
}

/// The Windows 10 package of Claude Desktop's MSIX installer keeps its local application data
/// here, under `%LOCALAPPDATA%` (Anthropic's data-storage page).
const MSIX_LOCAL: [&str; 4] = ["Packages", "Claude_pzs8sxrjxfjjc", "LocalCache", "Local"];

/// Claude Desktop's third-party folder, `Claude-3p`, where it keeps its data on os (Anthropic's
/// data-storage and configuration-format pages): `~/.config/Claude-3p` on Linux (the tool's
/// environment has no XDG_CONFIG_HOME: a person who moved it selects that folder),
/// `~/Library/Application Support/Claude-3p` on macOS and `%LOCALAPPDATA%\Claude-3p` on Windows.
/// On Windows 10 the MSIX package keeps it in its own folder instead, which is taken when only
/// that one exists (after an upgrade to Windows 11 the old one can stay behind).
fn threep_in(os: &str, var: Var) -> Result<PathBuf, Error> {
    match os {
        "windows" => {
            let local = folder_of(var, "LOCALAPPDATA")
                .filter(|p| p.is_absolute())
                .ok_or_else(|| {
                    Error::unavailable(
                        "the app gave no local application data folder (LOCALAPPDATA)",
                    )
                })?;
            let plain = local.join("Claude-3p");
            let packaged = MSIX_LOCAL
                .iter()
                .fold(local, |p, part| p.join(part))
                .join("Claude-3p");
            let absent =
                std::fs::symlink_metadata(&plain).is_err_and(|e| e.kind() == ErrorKind::NotFound);
            Ok(if absent && packaged.is_dir() {
                packaged
            } else {
                plain
            })
        }
        "macos" => Ok(home_of(var)?
            .join("Library")
            .join("Application Support")
            .join("Claude-3p")),
        _ => Ok(home_of(var)?.join(".config").join("Claude-3p")),
    }
}

fn threep() -> Result<PathBuf, Error> {
    threep_in(std::env::consts::OS, &|k| std::env::var_os(k))
}

/// What a default preview says, and the page looks for, when the library folder is not there:
/// the `folders` operation creates it.
const MISSING_LIBRARY: &str = "Claude Desktop's configuration library does not exist yet";

/// The three files' paths inside the third-party folder.
fn paths() -> [String; 3] {
    [
        "claude_desktop_config.json".into(),
        format!("configLibrary/{PROFILE_ID}.json"),
        "configLibrary/_meta.json".into(),
    ]
}

/// The input's three targets, read with f.
fn three<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<[T; 3], Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([d, p, m]) => Ok([f(d)?, f(p)?, f(m)?]),
        _ => Err(Error::invalid_request(
            "targets holds claude_desktop_config.json, the gateway profile and configLibrary/_meta.json",
        )),
    }
}

/// The three files in Claude Desktop's third-party folder (`threep_in`), refused with
/// MISSING_LIBRARY when nothing is at its `configLibrary` yet (and the folders above it are
/// folders or missing too), which the `folders` operation creates; anything else that is not a
/// folder there (a link to nowhere, a file) is left to the preview, which names what it found.
fn default_selections(os: &str, var: Var) -> Result<[Selection; 3], Error> {
    let dir = threep_in(os, var)?;
    let library = dir.join("configLibrary");
    let absent =
        |p: &Path| std::fs::symlink_metadata(p).is_err_and(|e| e.kind() == ErrorKind::NotFound);
    if absent(&library) && (absent(&dir) || dir.is_dir()) {
        return Err(Error::not_found(format!(
            "{MISSING_LIBRARY}: {}; create it here, or save a configuration in Claude Desktop (Developer → Configure Third-Party Inference…)",
            library.display()
        )));
    }
    Ok(paths().map(|p| Selection::File(dir.join(p))))
}

/// The selections a preview names: its three targets, or the files in the third-party folder.
fn selections(input: &Value) -> Result<[Selection; 3], Error> {
    match input.get("targets") {
        None => default_selections(std::env::consts::OS, &|k| std::env::var_os(k)),
        Some(_) => three(input, Selection::from_json),
    }
}

/// What a preview shows of the three files. Files Claude Desktop would not take are pinned all
/// the same, so Restore Backup still reaches them: their `state` is `refused` and `reason` says
/// why apply and clear refuse them.
fn settings_of(files: &[Option<Vec<u8>>; 3], base_url: Option<&str>) -> Value {
    files::read_back(
        files[0].as_deref(),
        files[1].as_deref(),
        files[2].as_deref(),
        base_url,
    )
    .unwrap_or_else(|e| json!({"state": "refused", "reason": e.message}))
}

/// A preview and the three files' bytes.
type Read = (Value, [Option<Vec<u8>>; 3]);

/// Runs read again once when the bytes it read are not the ones it pinned (another program wrote
/// a file between the two reads); refuses if they differ again, so the values shown are always
/// those of the files pinned.
fn pinned_read(mut read: impl FnMut() -> Result<Read, Error>) -> Result<Read, Error> {
    for _ in 0..2 {
        let (v, bytes) = read()?;
        let same =
            (0..3).all(|i| v["targets"][i]["sha256"] == json!(sha256_of(bytes[i].as_deref())));
        if same {
            return Ok((v, bytes));
        }
    }
    Err(Error::unavailable(
        "a configuration file changed while it was read: read it again",
    ))
}

/// Makes the library folder: any missing folders above it (on Linux and macOS private, as
/// `mkdir -p` with 0700; on Windows with what their folder passes down), then the library itself,
/// private. One made meanwhile by Claude Desktop is taken as it is.
fn make_library(library: &Path) -> std::io::Result<()> {
    if let Some(parent) = library.parent() {
        pluxy_files::create_private_dir_all(parent)?;
    }
    match pluxy_files::create_private_dir(library) {
        Err(e) if e.kind() == ErrorKind::AlreadyExists && library.is_dir() => Ok(()),
        r => r,
    }
}

/// How the launcher starts Claude Desktop, with no arguments, in the home folder: what it needs
/// is in its files (the old app's launch). On Linux its own command, `claude-desktop`
/// (code.claude.com's Linux page); on macOS its application bundle, `Claude.app`, in the
/// system's applications folder (apps, here a parameter for the tests) or else the home folder's
/// (Anthropic's installation page has it dragged to Applications). On Windows Anthropic names no
/// program or packaged-app ID for its installer, so there is none to start.
fn recipe(os: &str, home: &Path, apps: &Path) -> Result<Value, Error> {
    let program = match os {
        "windows" => {
            return Err(Error::unsupported(
                "pluxy does not know how to start Claude Desktop on Windows yet: start it from the Start menu",
            ))
        }
        "macos" => {
            let bundles = [apps.join("Claude.app"), home.join("Applications").join("Claude.app")];
            bundles
                .iter()
                .find(|b| b.is_dir())
                .unwrap_or(&bundles[0])
                .clone()
        }
        _ => PathBuf::from("claude-desktop"),
    };
    Ok(json!({
        "client": "Claude Desktop",
        "kind": "direct",
        "program": program,
        "args": [],
        "cwd": home,
    }))
}

impl Handler for ClaudeDesktop {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of the three files, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &json_leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                // A missing folder refuses the preview before anything is pinned; files Claude
                // Desktop would not take are pinned, their refusal in `settings`.
                let sels = selections(input)?;
                let tx = Transactions::new(call, data_dir()?);
                let (mut v, bytes) = pinned_read(|| {
                    let v = tx.preview(SET, &sels)?;
                    let mut bytes: [Option<Vec<u8>>; 3] = Default::default();
                    for (b, sel) in bytes.iter_mut().zip(&sels) {
                        *b = Target::resolve(sel)?.read_now()?.1;
                    }
                    Ok((v, bytes))
                })?;
                v["settings"] = settings_of(&bytes, input["base_url"].as_str());
                Ok(v)
            }
            // The default library folder, which Claude Desktop makes only once a configuration is
            // saved in its own window.
            "folders" => {
                let library = threep()?.join("configLibrary");
                if library.is_dir() {
                    return Ok(json!({"created": false, "folder": library}));
                }
                make_library(&library)
                    .map_err(|e| Error::unavailable(format!("{}: {e}", library.display())))?;
                Ok(json!({"created": true, "folder": library}))
            }
            "apply" => {
                let pins = three(input, Pin::from_json)?;
                let key = input["key"]
                    .as_str()
                    .and_then(|r| call.secrets.get(r))
                    .and_then(Value::as_str)
                    .ok_or_else(|| {
                        Error::invalid_request("key names no secret the app delivered")
                    })?;
                let s = Settings::from_input(input, key)?;
                Transactions::new(call, data_dir()?).apply(
                    SET,
                    &pins,
                    &|i, _, before| files::apply(File::ALL[i], before, &s).map(Some),
                    &Checked,
                    &mut |_| {},
                )
            }
            "clear" => Transactions::new(call, data_dir()?).clear(
                SET,
                &three(input, Pin::from_json)?,
                &Checked,
                &mut |_| {},
            ),
            // Restore Backup puts the originals back whatever the files hold now: the person's
            // way back always works.
            "restore" => Transactions::new(call, data_dir()?).restore(
                SET,
                &three(input, Pin::from_json)?,
                &mut |_| {},
            ),
            "status" => Transactions::new(call, data_dir()?).status(),
            "recipe" => recipe(std::env::consts::OS, &home()?, Path::new("/Applications")),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }

    fn reconcile(&self, call: &Call, _unknown: &[Value], journals: &[Value]) -> Result<(), Error> {
        Transactions::new(call, data_dir()?)
            .reconcile(journals)
            .map(|_| ())
    }
}

#[cfg(test)]
#[path = "files_tests.rs"]
mod files_tests;

#[cfg(test)]
#[path = "main_tests.rs"]
mod main_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(ClaudeDesktop) {
        eprintln!("claude-desktop: {e}");
        std::process::exit(1);
    }
}
