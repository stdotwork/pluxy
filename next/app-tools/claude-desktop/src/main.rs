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
use std::os::unix::fs::DirBuilderExt;
use std::path::{Path, PathBuf};

/// The one set this tool applies: the deployment file, the gateway profile and the library index,
/// in that order.
const SET: &str = "claude-desktop";

struct ClaudeDesktop;

fn home() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// Claude Desktop's third-party folder in the home folder (the tool's environment has no
/// XDG_CONFIG_HOME: a person who moved it selects that folder).
fn threep_in(home: &Path) -> PathBuf {
    home.join(".config").join("Claude-3p")
}

fn threep() -> Result<PathBuf, Error> {
    Ok(threep_in(&home()?))
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

/// The three files in ~/.config/Claude-3p of home, refused with MISSING_LIBRARY when nothing is
/// at its `configLibrary` yet (and the folders above it are folders or missing too), which the
/// `folders` operation creates; anything else that is not a folder there (a link to nowhere, a
/// file) is left to the preview, which names what it found.
fn default_selections(home: Option<OsString>) -> Result<[Selection; 3], Error> {
    let home = home
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))?;
    let dir = threep_in(&home);
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

/// The selections a preview names: its three targets, or the files in ~/.config/Claude-3p.
fn selections(input: &Value) -> Result<[Selection; 3], Error> {
    match input.get("targets") {
        None => default_selections(std::env::var_os("HOME")),
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
                std::fs::DirBuilder::new()
                    .recursive(true)
                    .mode(0o700)
                    .create(&library)
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
            // Claude Desktop's own command, with no arguments, in the home folder: what it needs
            // is in its files (the old app's launch).
            "recipe" => Ok(json!({
                "client": "Claude Desktop",
                "kind": "direct",
                "program": "claude-desktop",
                "args": [],
                "cwd": home()?,
            })),
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
