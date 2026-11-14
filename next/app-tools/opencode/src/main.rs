//! pluxy/opencode: sets up OpenCode to use pluxy's core, and starts its CLI or desktop app, for
//! pluxy's desktop app (README.md). Its operations: `preview` (read: pins OpenCode's config file
//! and reads its values back, never the key; not exported), `apply`, `clear` and `restore` (write:
//! each call asks the person; `apply` receives the client key as a secret's value and writes it
//! where OpenCode reads it), `status` (read: the applied set, the originals kept for restore, the
//! latest recovery reports), `recipe` (read: how the launcher starts the CLI or the desktop app)
//! and the backup browser's standard operations (`backup-list` and `backup-diff`, reads;
//! `backup-create`, `backup-restore` and `backup-delete`, writes). Writes go through
//! pluxy-transactions: link-preserving, journalled, with the originals and manual backups in the
//! host's vault.

mod config;

use config::{Checked, Settings};
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{backup_call, data_dir, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

/// The one set this tool applies: OpenCode's config file.
const SET: &str = "opencode";

/// The desktop app's executable, as OpenCode's Linux packages install it.
const DESKTOP: &str = "/opt/OpenCode/ai.opencode";

struct OpenCode;

fn home() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The global config file in folder, as OpenCode picks the one it writes: `opencode.jsonc` if it
/// exists, else `opencode.json` if it exists, else a new `opencode.jsonc`. OpenCode reads both,
/// `opencode.jsonc` last, so its values win.
pub fn config_in(folder: &Path) -> PathBuf {
    let (jsonc, json) = (folder.join("opencode.jsonc"), folder.join("opencode.json"));
    let exists = |p: &Path| p.symlink_metadata().is_ok();
    if !exists(&jsonc) && exists(&json) {
        json
    } else {
        jsonc
    }
}

/// OpenCode's global config in the app's home folder.
fn default_file() -> Result<PathBuf, Error> {
    Ok(config_in(&home()?.join(".config").join("opencode")))
}

/// The input's one target, read with f: a config file is written alone.
fn one<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<T, Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([t]) => f(t),
        _ => Err(Error::invalid_request("targets holds the one config file")),
    }
}

/// The selection a preview names: its one target, or OpenCode's global config.
fn selection(input: &Value) -> Result<Selection, Error> {
    match input.get("targets") {
        None => Ok(Selection::File(default_file()?)),
        Some(_) => one(input, Selection::from_json),
    }
}

/// The CLI the launcher runs: the install script's `~/.opencode/bin/opencode` when it is an
/// executable file there (a desktop session's PATH often lacks that folder), else `opencode`
/// found on the PATH.
fn cli(home: &Path) -> String {
    let installed = home.join(".opencode").join("bin").join("opencode");
    let runnable = std::fs::metadata(&installed)
        .is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0);
    if runnable {
        installed.to_string_lossy().into_owned()
    } else {
        "opencode".into()
    }
}

impl Handler for OpenCode {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of the config file, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &config::leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                let sel = selection(input)?;
                // A config OpenCode would not take is refused before anything is pinned.
                let (_, bytes) = Target::resolve(&sel)?.read_now()?;
                let read = config::read_back(bytes.as_deref(), input["base_url"].as_str())?;
                let mut v = Transactions::new(call, data_dir()?).preview(SET, &[sel])?;
                v["config"] = read;
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
                    &|_, _, before| config::apply(before, &s).map(Some),
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
            // The CLI in a terminal, or the desktop app, with no arguments and no variables of
            // their own: what OpenCode needs is in its config (the old app's launches).
            "recipe" => match input["target"].as_str().unwrap_or("cli") {
                "cli" => Ok(json!({
                    "client": "OpenCode",
                    "kind": "terminal",
                    "program": cli(&home()?),
                    "args": [],
                })),
                "app" => Ok(json!({
                    "client": "OpenCode Desktop",
                    "kind": "direct",
                    "program": DESKTOP,
                    "args": [],
                    "cwd": home()?,
                })),
                t => Err(Error::invalid_request(format!(
                    "target is cli or app, not {t}"
                ))),
            },
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
#[path = "config_tests.rs"]
mod config_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(OpenCode) {
        eprintln!("opencode: {e}");
        std::process::exit(1);
    }
}
