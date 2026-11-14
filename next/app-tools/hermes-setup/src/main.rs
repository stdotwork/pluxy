//! pluxy/hermes-setup: sets up Hermes Agent to use pluxy's core, and starts it, for pluxy's desktop
//! app (README.md). Its operations: `preview` (read: pins Hermes Agent's `config.yaml` and reads its
//! values back, never the key), `apply`, `clear` and `restore` (write: each call asks the person;
//! `apply` receives the client key as a secret's value and writes it where Hermes reads it),
//! `status` (read), `recipe` (read: how the launcher starts Hermes Agent) and the backup browser's
//! standard operations. Writes go through pluxy-transactions: link-preserving, journalled, with the
//! originals and manual backups in the host's vault.

mod hermes;
mod yaml;

use hermes::{yaml11_not_text, Settings};
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{backup_call, data_dir, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use yaml::Yamls;

/// The one set this tool applies: Hermes Agent's config.yaml.
const SET: &str = "hermes";
/// The file's name in a Hermes home folder.
const CONFIG: &str = "config.yaml";

struct Hermes;

/// The app's home folder.
fn home() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The input's one target, read with f.
fn one<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<[T; 1], Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([t]) => Ok([f(t)?]),
        _ => Err(Error::invalid_request(
            "targets holds Hermes Agent's config file",
        )),
    }
}

/// Whether name is a Hermes profile ID (`^[a-z0-9][a-z0-9_-]{0,63}$`).
fn profile_id(name: &str) -> bool {
    name.len() <= 64
        && name.starts_with(|c: char| c.is_ascii_lowercase() || c.is_ascii_digit())
        && name
            .chars()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_' || c == '-')
}

/// The Hermes home `hermes` uses with no flags and no HERMES_HOME (the tool's environment has
/// none): `~/.hermes`, or the profile `~/.hermes/active_profile` names, as Hermes picks it.
fn default_home() -> Result<PathBuf, Error> {
    let root = home()?.join(".hermes");
    let name = match std::fs::read(root.join("active_profile")) {
        Ok(b) => String::from_utf8_lossy(&b)
            .trim_start_matches('\u{feff}')
            .trim()
            .to_string(),
        Err(_) => return Ok(root),
    };
    if name.is_empty() || name.eq_ignore_ascii_case("default") {
        return Ok(root);
    }
    let id = name.to_lowercase();
    let dir = root.join("profiles").join(&id);
    if profile_id(&id) && dir.is_dir() {
        return Ok(dir);
    }
    Err(Error::invalid_request(format!(
        "Hermes Agent's active profile ({}) is not a folder under {}: select the folder to set up",
        name.chars().take(64).collect::<String>(),
        root.join("profiles").display()
    )))
}

/// The selection a preview names: its target; `config.yaml` in a selected Hermes home folder
/// (`folder`, which may be a link: the home override); or `config.yaml` in the home Hermes uses.
fn selection(input: &Value) -> Result<[Selection; 1], Error> {
    if input.get("targets").is_some() {
        return one(input, Selection::from_json);
    }
    if let Some(folder) = input.get("folder") {
        let root = PathBuf::from(folder.as_str().unwrap_or_default());
        if !root.is_absolute() {
            return Err(Error::invalid_request("folder is a full path"));
        }
        return Ok([Selection::Root {
            path: CONFIG.to_string(),
            root,
        }]);
    }
    Ok([Selection::File(default_home()?.join(CONFIG))])
}

/// A value pluxy writes as plain YAML text: refused when Hermes would read it as something else,
/// expand it (`${`), or it holds whitespace or control characters.
fn plain(s: &str) -> bool {
    !yaml11_not_text(s)
        && !s.contains("${")
        && !s.chars().any(|c| c.is_whitespace() || c.is_control())
}

/// What an apply's input asks for, checked, with the key.
fn settings(input: &Value, key: &str) -> Result<Settings, Error> {
    let base = input["base_url"].as_str().unwrap_or_default();
    let url_ok = (base.starts_with("http://") || base.starts_with("https://"))
        && base.len() <= 2048
        && !base.contains("${")
        && !base.chars().any(|c| c.is_whitespace() || c.is_control());
    if !url_ok {
        return Err(Error::invalid_request(
            "base_url is the core's http:// or https:// address",
        ));
    }
    if key.len() > 4096 || !plain(key) {
        return Err(Error::invalid_request(
            "the key is empty or not a usable key",
        ));
    }
    let model = input["model"].as_str().unwrap_or_default();
    if model.chars().count() > 240 || !plain(model) {
        return Err(Error::invalid_request(
            "model is a model ID of at most 240 characters, without spaces, `${` or a value YAML reads as a number, date or yes/no",
        ));
    }
    Ok(Settings {
        base_url: hermes::base_url_of(base),
        key: key.to_string(),
        model: model.to_string(),
    })
}

/// Whether path is a file someone may run.
fn executable(path: &Path) -> bool {
    std::fs::metadata(path).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
}

/// How the launcher starts Hermes Agent: `hermes` in a terminal, with no arguments or variables
/// (the old app's launch; with no arguments Hermes starts a chat). The installer's
/// `~/.local/bin/hermes` comes first (often not on a desktop session's PATH), then `hermes` from
/// the PATH.
fn recipe() -> Result<Value, Error> {
    let installed = home()?.join(".local/bin/hermes");
    let program = if executable(&installed) {
        installed.to_string_lossy().into_owned()
    } else {
        "hermes".to_string()
    };
    Ok(json!({"client": "Hermes Agent", "kind": "terminal", "program": program, "args": []}))
}

impl Handler for Hermes {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of the file, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &yaml::leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                let sel = selection(input)?;
                // A file Hermes would not take is refused before anything is pinned.
                let (_, bytes) = Target::resolve(&sel[0])?.read_now()?;
                let read = hermes::read_back(bytes.as_deref(), input["base_url"].as_str())?;
                let mut v = Transactions::new(call, data_dir()?).preview(SET, &sel)?;
                v["config"] = read;
                Ok(v)
            }
            "apply" => {
                let pins = one(input, Pin::from_json)?;
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
                    &pins,
                    &|_, _, before| hermes::apply(before, &s).map(Some),
                    &Yamls,
                    &mut |_| {},
                )
            }
            "clear" => Transactions::new(call, data_dir()?).clear(
                SET,
                &one(input, Pin::from_json)?,
                &Yamls,
                &mut |_| {},
            ),
            // Restore Backup puts the original back whatever the file holds now: the person's
            // way back always works.
            "restore" => Transactions::new(call, data_dir()?).restore(
                SET,
                &one(input, Pin::from_json)?,
                &mut |_| {},
            ),
            "status" => Transactions::new(call, data_dir()?).status(),
            "recipe" => recipe(),
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
#[path = "hermes_tests.rs"]
mod hermes_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(Hermes) {
        eprintln!("hermes-setup: {e}");
        std::process::exit(1);
    }
}
