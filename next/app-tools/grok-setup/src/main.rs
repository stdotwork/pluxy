//! pluxy/grok-setup: sets up Grok Build to use pluxy's core, and starts it, for pluxy's desktop
//! app (README.md). Its operations: `preview` (read: pins Grok Build's config.toml and reads its
//! values back, never the key), `apply`, `clear` and `restore` (write: each call asks the person;
//! `apply` receives the client key as a secret's value and writes it where Grok Build reads it),
//! `status` (read), `recipe` (read: how the launcher starts Grok Build) and the backup browser's
//! standard operations. Writes go through pluxy-transactions: link-preserving, journalled, with
//! the originals and manual backups in the host's vault.

mod config;

use config::{Checked, Model, Settings};
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{backup_call, data_dir, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

/// The one set this tool applies: Grok Build's config.toml.
const SET: &str = "grok";
/// The most model entries one apply writes.
pub const MODELS_MAX: usize = 256;
/// The longest model text the old app took (its pickers' custom values).
const TEXT_MAX: usize = 240;

struct Grok;

/// Grok Build's config in the app's home folder (the tool's environment has no GROK_HOME: a
/// person who moved it selects that file or folder).
fn default_file() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(|h| PathBuf::from(h).join(".grok").join("config.toml"))
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The input's one target, read with f: the config is written alone.
fn one<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<T, Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([t]) => f(t),
        _ => Err(Error::invalid_request("targets holds the one config file")),
    }
}

/// The selection a preview names: its one target, or Grok Build's user config.
fn selection(input: &Value) -> Result<Selection, Error> {
    match input.get("targets") {
        None => Ok(Selection::File(default_file()?)),
        Some(_) => one(input, Selection::from_json),
    }
}

/// field of v as a short text: 1 to 240 characters with no control characters (none empty when
/// may_be_empty), and with no white space when bare.
fn text(v: &Value, field: &str, may_be_empty: bool, bare: bool) -> Result<String, Error> {
    let s = v[field].as_str().unwrap_or_default().trim().to_string();
    let bad = (s.is_empty() && !may_be_empty)
        || s.chars().count() > TEXT_MAX
        || s.chars().any(char::is_control)
        || (bare && s.chars().any(char::is_whitespace));
    if bad {
        return Err(Error::invalid_request(format!(
            "{field} is at most {TEXT_MAX} characters, with no control characters{}",
            if bare { " or spaces" } else { "" }
        )));
    }
    Ok(s)
}

/// What an apply's input asks for, with key, the secret its `key` field delivered.
fn settings(input: &Value, key: &str) -> Result<Settings, Error> {
    let base = input["base_url"].as_str().unwrap_or_default();
    let url_ok = (base.starts_with("http://") || base.starts_with("https://"))
        && base.len() <= 2048
        && !base.chars().any(|c| c.is_whitespace() || c.is_control());
    if !url_ok {
        return Err(Error::invalid_request(
            "base_url is the core's http:// or https:// address",
        ));
    }
    if key.is_empty()
        || key.len() > 4096
        || key.chars().any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(Error::invalid_request(
            "the key is empty or not a usable key",
        ));
    }
    let list = input["models"]
        .as_array()
        .filter(|l| (1..=MODELS_MAX).contains(&l.len()))
        .ok_or_else(|| {
            Error::invalid_request(format!(
                "models lists 1 to {MODELS_MAX} of the core's models"
            ))
        })?;
    let mut models: Vec<Model> = Vec::with_capacity(list.len());
    for m in list {
        let model = text(m, "model", false, true)?;
        let name = text(m, "name", true, false)?;
        let context_window = m["context_window"]
            .as_u64()
            .filter(|n| (1..=100_000_000).contains(n))
            .ok_or_else(|| {
                Error::invalid_request("context_window is a whole number from 1 to 100000000")
            })?;
        if models.iter().any(|x| x.model == model) {
            return Err(Error::invalid_request(format!("{model} is listed twice")));
        }
        models.push(Model {
            name: if name.is_empty() { model.clone() } else { name },
            model,
            context_window,
        });
    }
    let default = text(input, "model", false, true)?;
    if !models.iter().any(|m| m.model == default) {
        return Err(Error::invalid_request(format!(
            "the model {default} is not among the listed models"
        )));
    }
    Ok(Settings {
        base_url: config::base_url_of(base),
        key: key.to_string(),
        default,
        models,
    })
}

/// How the launcher starts Grok Build: in a terminal, with no arguments (its interactive session,
/// in the launcher's working directory). The program is the copy Grok Build's install script puts
/// in `~/.grok/bin` (often not on a desktop session's PATH), when home has one, else `grok` from
/// PATH.
fn recipe(home: Option<&Path>) -> Value {
    let installed = home.map(|h| h.join(".grok").join("bin").join("grok"));
    let program = match installed {
        Some(p)
            if std::fs::metadata(&p)
                .is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0) =>
        {
            p.to_string_lossy().into_owned()
        }
        _ => "grok".to_string(),
    };
    json!({"client": "Grok Build", "kind": "terminal", "program": program, "args": []})
}

impl Handler for Grok {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of the config, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &config::leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                let sel = selection(input)?;
                // A config Grok Build would not take is refused before anything is pinned.
                let (_, bytes) = Target::resolve(&sel)?.read_now()?;
                let read = config::read_back(bytes.as_deref(), input["base_url"].as_str())?;
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
                let s = settings(input, key)?;
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
            "recipe" => Ok(recipe(
                std::env::var_os("HOME")
                    .filter(|h| !h.is_empty())
                    .map(PathBuf::from)
                    .as_deref(),
            )),
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
    if let Err(e) = run(Grok) {
        eprintln!("grok-setup: {e}");
        std::process::exit(1);
    }
}
