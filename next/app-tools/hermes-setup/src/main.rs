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
use pluxy_transactions::{backup_call, data_dir, sha256_of, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
use std::ffi::OsString;
use std::path::PathBuf;
use yaml::Yamls;

/// The one set this tool applies: Hermes Agent's config.yaml.
const SET: &str = "hermes";
/// The file's name in a Hermes home folder.
const CONFIG: &str = "config.yaml";

struct Hermes;

/// What the tool reads of its environment: `std::env::var_os`, or a test's values.
type Var<'a> = &'a dyn Fn(&str) -> Option<OsString>;

fn env(name: &str) -> Option<OsString> {
    std::env::var_os(name)
}

/// The app's home folder.
fn home_of(var: Var) -> Result<PathBuf, Error> {
    var("HOME")
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The Hermes home with no HERMES_HOME (the tool's environment has none) on os, as Hermes picks
/// it (`hermes_constants.py`, `_platform_default_hermes_home`): `~/.hermes` on Linux and macOS;
/// on Windows `hermes` in `%LOCALAPPDATA%`, or in `AppData\Local` in the home folder when that is
/// not set, blank or not a full path.
fn hermes_root(os: &str, var: Var) -> Result<PathBuf, Error> {
    if os != "windows" {
        return Ok(home_of(var)?.join(".hermes"));
    }
    // Stripped of blanks as Hermes strips it, and only a full path: a relative one would name a
    // folder under wherever the tool runs.
    let local = var("LOCALAPPDATA")
        .map(|v| match v.to_str() {
            Some(t) => PathBuf::from(t.trim()),
            None => PathBuf::from(v),
        })
        .filter(|p| p.is_absolute());
    let local = match local {
        Some(l) => l,
        None => home_of(var)?.join("AppData").join("Local"),
    };
    Ok(local.join("hermes"))
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
/// none): its root (`hermes_root`), or the profile its `active_profile` names, as Hermes picks it.
fn default_home(os: &str, var: Var) -> Result<PathBuf, Error> {
    let root = hermes_root(os, var)?;
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
    Ok([Selection::File(
        default_home(std::env::consts::OS, &env)?.join(CONFIG),
    )])
}

/// A preview and the file's bytes, read after it.
type Read = (Value, Option<Vec<u8>>);

/// Runs read again once when the bytes it read are not the ones it pinned (another program wrote
/// the file between the two reads); refuses if they differ again, so the values shown are always
/// those of the file pinned.
fn pinned_read(mut read: impl FnMut() -> Result<Read, Error>) -> Result<Read, Error> {
    for _ in 0..2 {
        let (v, bytes) = read()?;
        if v["targets"][0]["sha256"] == json!(sha256_of(bytes.as_deref())) {
            return Ok((v, bytes));
        }
    }
    Err(Error::unavailable(
        "the configuration file changed while it was read: read it again",
    ))
}

/// What a preview shows of the file. A file Hermes would not take is pinned all the same, so
/// Restore Backup still reaches it: its `state` is `refused` and `reason` says why apply and clear
/// refuse it.
fn config_of(bytes: Option<&[u8]>, base_url: Option<&str>) -> Value {
    hermes::read_back(bytes, base_url)
        .unwrap_or_else(|e| json!({"state": "refused", "reason": e.message}))
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

/// How the launcher starts Hermes Agent: `hermes` in a terminal, with no arguments or variables
/// (the old app's launch; with no arguments Hermes starts a chat). The installer's copy comes
/// first, often not on a desktop session's PATH: `~/.local/bin/hermes` on Linux and macOS
/// (`install.sh` and `hermes_cli/_launchers.py`), `bin\hermes.exe` in the Hermes home on Windows
/// (`install.ps1`); then `hermes` from the PATH.
fn recipe(os: &str, var: Var) -> Result<Value, Error> {
    let installed = match os {
        "windows" => hermes_root(os, var)?.join("bin").join("hermes.exe"),
        _ => home_of(var)?.join(".local/bin/hermes"),
    };
    let program = if pluxy_files::executable(&installed) {
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
                let tx = Transactions::new(call, data_dir()?);
                let (mut v, bytes) = pinned_read(|| {
                    let v = tx.preview(SET, &sel)?;
                    let (_, b) = Target::resolve(&sel[0])?.read_now()?;
                    Ok((v, b))
                })?;
                v["config"] = config_of(bytes.as_deref(), input["base_url"].as_str());
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
            "clear" => {
                let pins = one(input, Pin::from_json)?;
                // A file Hermes would not take is refused before anything is written; a file that
                // no longer resolves as pinned is the library's to refuse.
                if let Ok((_, b)) = Target::place(&pins[0]).and_then(|t| t.read_now()) {
                    hermes::check(&yaml::parse(b.as_deref())?)?;
                }
                Transactions::new(call, data_dir()?).clear(SET, &pins, &Yamls, &mut |_| {})
            }
            // Restore Backup puts the original back whatever the file holds now: the person's
            // way back always works.
            "restore" => Transactions::new(call, data_dir()?).restore(
                SET,
                &one(input, Pin::from_json)?,
                &mut |_| {},
            ),
            "status" => Transactions::new(call, data_dir()?).status(),
            "recipe" => recipe(std::env::consts::OS, &env),
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
#[path = "main_tests.rs"]
mod main_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(Hermes) {
        eprintln!("hermes-setup: {e}");
        std::process::exit(1);
    }
}
