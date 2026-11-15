//! pluxy/claude-code: sets up Claude Code to use pluxy's core, and starts it, for pluxy's desktop
//! app (README.md). Its operations: `preview` (read: pins Claude Code's settings file and reads
//! its values back, never the key; not exported), `apply`, `clear` and `restore` (write: each
//! call asks the person; `apply` receives the client key as a secret's value and writes it where
//! Claude Code reads it), `status` (read: the applied set, the originals kept for restore, the
//! latest recovery reports), `recipe` (read: how the launcher starts Claude Code) and the backup
//! browser's standard operations (`backup-list` and `backup-diff`, reads; `backup-create`,
//! `backup-restore` and `backup-delete`, writes). Writes go through pluxy-transactions:
//! link-preserving, journalled, with the originals and manual backups in the host's vault.

mod settings;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{
    backup_call, data_dir, json_leaves, sha256_of, Pin, Selection, Target, Transactions,
};
use serde_json::{json, Value};
use settings::{Checked, Settings};
use std::ffi::OsString;
use std::path::PathBuf;

/// The one set this tool applies: Claude Code's settings file.
const SET: &str = "claude-code";

struct ClaudeCode;

/// Claude Code's user settings in home (the app's home folder). Claude Code makes `~/.claude`
/// when it first runs; before that there is nothing to set up, and the tool makes no folder.
fn default_selection(home: Option<OsString>) -> Result<Selection, Error> {
    let home = home
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))?;
    let folder = home.join(".claude");
    if !folder.is_dir() {
        return Err(Error::invalid_request(format!(
            "{} does not exist yet: run Claude Code once (it makes the folder), then read the settings again",
            folder.display()
        )));
    }
    Ok(Selection::File(folder.join("settings.json")))
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
fn selection(input: &Value) -> Result<Selection, Error> {
    match input.get("targets") {
        None => default_selection(std::env::var_os("HOME")),
        Some(_) => one(input, Selection::from_json),
    }
}

impl Handler for ClaudeCode {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of the settings file, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &json_leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                let sel = selection(input)?;
                let tx = Transactions::new(call, data_dir()?);
                let (mut v, bytes) = pinned_read(|| {
                    let v = tx.preview(SET, std::slice::from_ref(&sel))?;
                    let (_, bytes) = Target::resolve(&sel)?.read_now()?;
                    Ok((v, bytes))
                })?;
                v["settings"] = settings_of(bytes.as_deref(), input["base_url"].as_str());
                Ok(v)
            }
            "apply" => {
                let pin = one(input, Pin::from_json)?;
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
                    &[pin],
                    &|_, _, before| settings::apply(before, &s).map(Some),
                    &Checked,
                    &mut |_| {},
                )
            }
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
