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
use pluxy_transactions::{backup_call, data_dir, sha256_of, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};

/// The one set this tool applies: OpenCode's config file.
const SET: &str = "opencode";

/// Where OpenCode's Linux packages install the desktop app.
const DESKTOP_DIR: &str = "/opt/OpenCode";

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

/// The desktop app's executable in folder: `ai.opencode` (2.x), else `ai.opencode.desktop` (1.x)
/// when only that exists, else `ai.opencode` (the launcher then says it is missing).
pub fn desktop_in(folder: &Path) -> PathBuf {
    let (now, older) = (
        folder.join("ai.opencode"),
        folder.join("ai.opencode.desktop"),
    );
    if !now.exists() && older.exists() {
        older
    } else {
        now
    }
}

/// What a preview shows of the config in bytes. A config OpenCode would not take is pinned all
/// the same, so Restore Backup still reaches it: its `state` is `refused` and `reason` says why
/// apply and clear refuse it.
fn config_of(bytes: Option<&[u8]>, base_url: Option<&str>) -> Value {
    config::read_back(bytes, base_url)
        .unwrap_or_else(|e| json!({"state": "refused", "reason": e.message}))
}

/// Runs read (a preview and the config file's bytes) again once when the bytes it read are not
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
        "the config file changed while it was read: read it again",
    ))
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

/// The CLI the launcher runs: the install script's copy in `~/.opencode/bin` when it is a
/// program there (`opencode`, `opencode.exe` on Windows, where the script runs under Git Bash; a
/// desktop session's PATH often lacks that folder), else `opencode` found on the PATH.
fn cli(os: &str, home: &Path) -> String {
    let name = if os == "windows" {
        "opencode.exe"
    } else {
        "opencode"
    };
    let installed = home.join(".opencode").join("bin").join(name);
    if pluxy_files::executable(&installed) {
        installed.to_string_lossy().into_owned()
    } else {
        "opencode".into()
    }
}

/// The desktop app's program on os, where OpenCode's installers put it (its
/// `electron-builder.config.ts` and Windows `installer.nsh`): on Linux in `/opt/OpenCode`
/// (`desktop_in`); on macOS the bundle `OpenCode.app` in the system's applications folder (apps,
/// here a parameter for the tests) or else the home folder's; on Windows the per-user install,
/// `Programs\ai.opencode.desktop\OpenCode.exe` in `%LOCALAPPDATA%`. A missing one is named all the
/// same: the launcher then says it is missing.
fn app(os: &str, home: &Path, apps: &Path, local: Option<PathBuf>) -> Result<PathBuf, Error> {
    match os {
        "windows" => local
            .filter(|l| l.is_absolute())
            .map(|l| {
                l.join("Programs")
                    .join("ai.opencode.desktop")
                    .join("OpenCode.exe")
            })
            .ok_or_else(|| {
                Error::unavailable("the app gave no local application data folder (LOCALAPPDATA)")
            }),
        "macos" => {
            let bundles = [
                apps.join("OpenCode.app"),
                home.join("Applications").join("OpenCode.app"),
            ];
            Ok(bundles
                .iter()
                .find(|b| b.is_dir())
                .unwrap_or(&bundles[0])
                .clone())
        }
        _ => Ok(desktop_in(Path::new(DESKTOP_DIR))),
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
                let tx = Transactions::new(call, data_dir()?);
                let (mut v, bytes) = pinned_read(|| {
                    let v = tx.preview(SET, std::slice::from_ref(&sel))?;
                    let (_, bytes) = Target::resolve(&sel)?.read_now()?;
                    Ok((v, bytes))
                })?;
                v["config"] = config_of(bytes.as_deref(), input["base_url"].as_str());
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
                    "program": cli(std::env::consts::OS, &home()?),
                    "args": [],
                })),
                "app" => Ok(json!({
                    "client": "OpenCode Desktop",
                    "kind": "direct",
                    "program": app(
                        std::env::consts::OS,
                        &home()?,
                        Path::new("/Applications"),
                        std::env::var_os("LOCALAPPDATA")
                            .filter(|v| !v.is_empty())
                            .map(PathBuf::from),
                    )?,
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
#[path = "main_tests.rs"]
mod main_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(OpenCode) {
        eprintln!("opencode: {e}");
        std::process::exit(1);
    }
}
