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
    backup_call, data_dir, json_leaves, Pin, Selection, Target, Transactions,
};
use serde_json::{json, Value};
use settings::{Checked, Settings};
use std::path::PathBuf;

/// The one set this tool applies: Claude Code's settings file.
const SET: &str = "claude-code";

struct ClaudeCode;

/// Claude Code's user settings in the app's home folder.
fn default_file() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(|h| PathBuf::from(h).join(".claude").join("settings.json"))
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
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
        None => Ok(Selection::File(default_file()?)),
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
                // Settings Claude Code would not take are refused before anything is pinned.
                let (_, bytes) = Target::resolve(&sel)?.read_now()?;
                let base_url = input["base_url"].as_str();
                let read = settings::read_back(bytes.as_deref(), base_url)?;
                let mut v = Transactions::new(call, data_dir()?).preview(SET, &[sel])?;
                v["settings"] = read;
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
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(ClaudeCode) {
        eprintln!("claude-code: {e}");
        std::process::exit(1);
    }
}
