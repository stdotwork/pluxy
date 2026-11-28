mod settings;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{
    backup_call, data_dir, json_leaves, sha256_of, Pin, Selection, Target, Transactions, Vault,
};
use serde_json::{json, Map, Value};
use settings::{Checked, Settings};
use std::ffi::OsString;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};

/// The one set this tool applies: Claude Code's settings file.
const SET: &str = "claude-code";

struct ClaudeCode;

/// Claude Code's folder in home (the app's home folder) and its user settings file in it.
fn default_file(home: Option<OsString>) -> Result<(PathBuf, PathBuf), Error> {
    let home = home
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))?;
    let folder = home.join(".claude");
    let file = folder.join("settings.json");
    Ok((folder, file))
}

/// Whether nothing at all is at path (not even a link to nowhere).
fn absent(path: &Path) -> bool {
    std::fs::symlink_metadata(path).is_err_and(|e| e.kind() == ErrorKind::NotFound)
}

/// What a preview shows as its target while Claude Code's folder does not exist yet (Claude Code
/// has not run here): the file Connect writes, at the path a preview pins once the folder is
/// there, with no identities and no bytes. It is no pin (`Pin::from_json` refuses a target whose
/// folder has no identity): only Connect takes it back, compared whole, with `create_folder`.
fn unmade(folder: &Path, file: &Path) -> Result<Value, Error> {
    let parent = folder.parent().unwrap_or(folder);
    let real = std::fs::canonicalize(parent)
        .map_err(|e| Error::unavailable(format!("{}: {e}", parent.display())))?;
    let target = real.join(".claude").join("settings.json");
    Ok(json!({
        "selected": file,
        "link": null,
        "target": target,
        "identity": {"link": null, "parent": null, "file": null},
        "sha256": null,
    }))
}

/// Claude Code's user settings, or, while `~/.claude` does not exist yet, the preview of the
/// file Connect makes there: Claude Code's own first start then reads pluxy's settings, so it
/// needs no run (and no sign-in of its own) before it is set up.
enum Default {
    File(Selection),
    Unmade { folder: PathBuf, target: Value },
}

fn default_selection(home: Option<OsString>) -> Result<Default, Error> {
    let (folder, file) = default_file(home)?;
    if absent(&folder) {
        let target = unmade(&folder, &file)?;
        return Ok(Default::Unmade { folder, target });
    }
    Ok(Default::File(Selection::File(file)))
}

/// Makes Claude Code's folder for Connect, as Claude Code makes it itself when it first runs
/// (the system's default permissions; the settings file is written private): whether it made it
/// (false when it is there now).
fn make_folder(folder: &Path) -> Result<bool, Error> {
    match std::fs::create_dir(folder) {
        Ok(()) => Ok(true),
        Err(e) if e.kind() == ErrorKind::AlreadyExists => Ok(false),
        Err(e) => Err(Error::unavailable(format!("{}: {e}", folder.display()))),
    }
}

/// What a write other than Connect says when it is given the target of a folder that does not
/// exist yet.
fn unmade_refusal(op: &str) -> Error {
    match op {
        // The page reads "is not applied to these targets" as: no copy from before pluxy.
        "clear" | "restore" => Error::invalid_request(format!(
            "{SET} is not applied to these targets: Claude Code's folder does not exist yet"
        )),
        _ => Error::invalid_request(
            "Claude Code's folder does not exist yet, so there is no settings file to back up or restore; Connect makes it",
        ),
    }
}

/// Whether input's one target is the preview's target of a folder that does not exist yet.
fn is_unmade(input: &Value) -> bool {
    input["targets"][0]["identity"]
        .get("parent")
        .is_some_and(Value::is_null)
}

/// What a preview shows of the settings in bytes. Settings Claude Code would not take are pinned
/// all the same, so Restore Backup still reaches them: their `state` is `refused` and `reason`
/// says why apply and clear refuse them.
fn settings_of(bytes: Option<&[u8]>, base_url: Option<&str>) -> Value {
    settings::read_back(bytes, base_url)
        .unwrap_or_else(|e| json!({"state": "refused", "reason": e.message}))
}

/// Runs read (a preview and the settings file's bytes) again once when the bytes it read are not
/// the ones it pinned (another program wrote the file between the two reads); refuses if they
/// differ again, so the values shown are always those of the file pinned.
fn pinned_read(
    mut read: impl FnMut() -> Result<(Value, Option<Vec<u8>>), Error>,
) -> Result<(Value, Option<Vec<u8>>), Error> {
    for _ in 0..2 {
        let (v, bytes) = read()?;
        if v["targets"][0]["sha256"] == json!(sha256_of(bytes.as_deref())) {
            return Ok((v, bytes));
        }
    }
    Err(Error::unavailable(
        "the settings file changed while it was read: read it again",
    ))
}

/// The input's one target, read with f: a settings file is written alone.
fn one<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<T, Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([t]) => f(t),
        _ => Err(Error::invalid_request(
            "targets holds the one settings file",
        )),
    }
}

/// The selection a preview names: its one target, or Claude Code's user settings.
fn selection(input: &Value, home: Option<OsString>) -> Result<Default, Error> {
    match input.get("targets") {
        None => default_selection(home),
        Some(_) => one(input, Selection::from_json).map(Default::File),
    }
}

/// Reads the settings file and pins it (`preview`). While Claude Code's folder does not exist yet,
/// nothing is read or made: the settings are not set up, and `create_folder` names the folder
/// Connect makes.
fn preview(
    vault: &dyn Vault,
    data: PathBuf,
    input: &Value,
    home: Option<OsString>,
) -> Result<Value, Error> {
    let base_url = input["base_url"].as_str();
    let sel = match selection(input, home)? {
        Default::File(sel) => sel,
        Default::Unmade { folder, target } => {
            return Ok(json!({
                "set": SET,
                "targets": [target],
                "applied": false,
                "edited": [false],
                "settings": settings_of(None, base_url),
                "create_folder": folder,
            }))
        }
    };
    let tx = Transactions::new(vault, data);
    let (mut v, bytes) = pinned_read(|| {
        let v = tx.preview(SET, std::slice::from_ref(&sel))?;
        let (_, bytes) = Target::resolve(&sel)?.read_now()?;
        Ok((v, bytes))
    })?;
    v["settings"] = settings_of(bytes.as_deref(), base_url);
    Ok(v)
}

/// Connect (`apply`): writes pluxy's settings into the pinned file. With `create_folder` (the
/// preview found no `~/.claude`), it first makes that folder, then pins the file in it, which must
/// still be the one the preview named and still not exist; a folder it made goes again, while
/// still empty, when the write fails.
fn apply(
    vault: &dyn Vault,
    data: PathBuf,
    input: &Value,
    secrets: &Map<String, Value>,
    home: Option<OsString>,
) -> Result<Value, Error> {
    let key = input["key"]
        .as_str()
        .and_then(|r| secrets.get(r))
        .and_then(Value::as_str)
        .ok_or_else(|| Error::invalid_request("key names no secret the app delivered"))?;
    let s = Settings::from_input(input, key)?;
    let Some(asked) = input.get("create_folder") else {
        let pin = one(input, Pin::from_json)?;
        return write(vault, data, pin, &s);
    };
    let (folder, file) = default_file(home)?;
    let target = unmade(&folder, &file)?;
    if *asked != json!(folder) || input["targets"] != json!([target]) {
        return Err(Error::invalid_request(
            "create_folder is Claude Code's folder in the home folder, and targets the preview's target in it",
        ));
    }
    let made = make_folder(&folder)?;
    let r = Target::resolve(&Selection::File(file)).and_then(|t| {
        let p = t.pin;
        if json!(p.target) != target["target"] || p.link.is_some() || p.sha256.is_some() {
            return Err(Error::invalid_request(format!(
                "{} changed since it was read: read the settings again",
                folder.display()
            )));
        }
        write(vault, data, p, &s)
    });
    if r.is_err() && made {
        // Only an empty folder goes: anything Claude Code or the write put there stays.
        let _ = std::fs::remove_dir(&folder);
    }
    r
}

/// Writes pluxy's settings s into the file pin names.
fn write(vault: &dyn Vault, data: PathBuf, pin: Pin, s: &Settings) -> Result<Value, Error> {
    Transactions::new(vault, data).apply(
        SET,
        &[pin],
        &|_, _, before| settings::apply(before, s).map(Some),
        &Checked,
        &mut |_| {},
    )
}

impl Handler for ClaudeCode {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        let home = || std::env::var_os("HOME");
        // The target of a folder that does not exist yet goes only to Connect.
        if !matches!(call.op.as_str(), "preview" | "apply") && is_unmade(input) {
            return Err(unmade_refusal(&call.op));
        }
        // The backup browser: manual backups of the settings file, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &json_leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => preview(call, data_dir()?, input, home()),
            "apply" => apply(call, data_dir()?, input, &call.secrets, home()),
            "clear" => Transactions::new(call, data_dir()?).clear(
                SET,
                &[one(input, Pin::from_json)?],
                &Checked,
                &mut |_| {},
            ),
            // Restore Backup puts the original back whatever the file holds now: the person's
            // way back always works.
            "restore" => Transactions::new(call, data_dir()?).restore(
                SET,
                &[one(input, Pin::from_json)?],
                &mut |_| {},
            ),
            "status" => Transactions::new(call, data_dir()?).status(),
            // Claude Code in a terminal, with no arguments and no variables of its own: what it
            // needs is in its settings (the old app's launch).
            "recipe" => Ok(json!({
                "client": "Claude Code",
                "kind": "terminal",
                "program": "claude",
                "args": [],
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
#[path = "settings_tests.rs"]
mod settings_tests;

#[cfg(test)]
#[path = "main_tests.rs"]
mod main_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(ClaudeCode) {
        eprintln!("claude-code: {e}");
        std::process::exit(1);
    }
}
