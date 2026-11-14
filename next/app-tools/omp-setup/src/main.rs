//! pluxy/omp-setup: sets up Oh My Pi to use pluxy's core, and starts it, for pluxy's desktop app
//! (README.md). Its operations: `preview` (read: pins Oh My Pi's models file and settings file and
//! reads their values back, never the key), `apply`, `clear` and `restore` (write: each call asks
//! the person; `apply` receives the client key as a secret's value and writes it where Oh My Pi
//! reads it), `status` (read), `recipe` (read: how the launcher starts Oh My Pi) and the backup
//! browser's standard operations. Writes go through pluxy-transactions: link-preserving,
//! journalled, with the originals and manual backups in the host's vault.

mod omp;
mod yaml;

use omp::Settings;
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{backup_call, data_dir, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};
use yaml::Yamls;

/// The one set this tool applies: the models file and the settings file, in that order.
const SET: &str = "omp";

/// Each file's names in Oh My Pi's order: the first that exists is read, else the first is made.
const MODELS: [&str; 2] = ["models.yml", "models.yaml"];
const CONFIG: [&str; 2] = ["config.yml", "config.yaml"];

struct Omp;

/// The app's home folder.
fn home() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The name of names that exists in dir, else the first.
fn pick(dir: &Path, names: [&str; 2]) -> String {
    names
        .iter()
        .find(|n| dir.join(n).exists())
        .unwrap_or(&names[0])
        .to_string()
}

/// The input's two targets, read with f: the models file, then the settings file.
fn two<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<[T; 2], Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([m, c]) => Ok([f(m)?, f(c)?]),
        _ => Err(Error::invalid_request(
            "targets holds the models file and the settings file",
        )),
    }
}

/// The selections a preview names: its two targets; the two files in a selected agent folder
/// (`folder`, which may be a link); or the two files in `~/.omp/agent` (the tool's environment has
/// no PI_CODING_AGENT_DIR or profile: a person who moved the folder selects it).
fn selections(input: &Value) -> Result<[Selection; 2], Error> {
    if input.get("targets").is_some() {
        return two(input, Selection::from_json);
    }
    if let Some(folder) = input.get("folder") {
        let root = PathBuf::from(folder.as_str().unwrap_or_default());
        if !root.is_absolute() {
            return Err(Error::invalid_request("folder is a full path"));
        }
        return Ok([
            Selection::Root {
                path: pick(&root, MODELS),
                root: root.clone(),
            },
            Selection::Root {
                path: pick(&root, CONFIG),
                root,
            },
        ]);
    }
    let agent = home()?.join(".omp").join("agent");
    Ok([
        Selection::File(agent.join(pick(&agent, MODELS))),
        Selection::File(agent.join(pick(&agent, CONFIG))),
    ])
}

/// The path a selection names, as the person gave it (not the link's target).
fn selected(s: &Selection) -> PathBuf {
    match s {
        Selection::File(f) => f.clone(),
        Selection::Root { root, path } => root.join(path),
    }
}

/// Refuses to make a models or settings file Oh My Pi has not migrated yet: with neither YAML
/// name in the folder, Oh My Pi moves a legacy `models.json` (or `settings.json`) into a new
/// YAML file when it next starts, which a file made here would stop.
fn unmigrated(sels: &[Selection; 2]) -> Result<(), Error> {
    for (s, names, legacy) in [
        (&sels[0], MODELS, "models.json"),
        (&sels[1], CONFIG, "settings.json"),
    ] {
        let Some(dir) = selected(s).parent().map(Path::to_path_buf) else {
            continue;
        };
        if names.iter().all(|n| !dir.join(n).exists()) && dir.join(legacy).exists() {
            return Err(Error::invalid_request(format!(
                "Oh My Pi has not moved {} to YAML yet: start omp once, then try again",
                dir.join(legacy).display()
            )));
        }
    }
    Ok(())
}

/// What an apply's input asks for, checked, with the key.
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
    // Oh My Pi runs a key starting with `!` as a command.
    if key.is_empty()
        || key.len() > 4096
        || key.starts_with('!')
        || key.chars().any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(Error::invalid_request(
            "the key is empty or not a usable key",
        ));
    }
    // A role's value is a selector: `,` separates fallbacks, `@` names a role, `*` the default.
    let model = input["model"].as_str().unwrap_or_default();
    if model.is_empty()
        || model.chars().count() > 240
        || model.starts_with(['@', '*'])
        || model
            .chars()
            .any(|c| c == ',' || c.is_whitespace() || c.is_control())
    {
        return Err(Error::invalid_request(
            "model is a model ID of at most 240 characters, without spaces or commas",
        ));
    }
    Ok(Settings {
        base_url: omp::base_url_of(base),
        key: key.to_string(),
        model: model.to_string(),
    })
}

/// Whether path is a file someone may run.
fn executable(path: &Path) -> bool {
    std::fs::metadata(path).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
}

/// How the launcher starts Oh My Pi: `omp` in a terminal, with no arguments or variables (the old
/// app's launch). bun's global copy comes first (the old app's order), then the install script's
/// `~/.local/bin` (often not on a desktop session's PATH), then `omp` from the PATH.
fn recipe() -> Result<Value, Error> {
    let home = home()?;
    let program = [home.join(".bun/bin/omp"), home.join(".local/bin/omp")]
        .into_iter()
        .find(|p| executable(p))
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_else(|| "omp".to_string());
    Ok(json!({"client": "Oh My Pi", "kind": "terminal", "program": program, "args": []}))
}

impl Handler for Omp {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of both files, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &yaml::leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                let sels = selections(input)?;
                unmigrated(&sels)?;
                // Files Oh My Pi would not take are refused before anything is pinned.
                let (_, models) = Target::resolve(&sels[0])?.read_now()?;
                let (_, config) = Target::resolve(&sels[1])?.read_now()?;
                let read = omp::read_back(
                    models.as_deref(),
                    config.as_deref(),
                    input["base_url"].as_str(),
                )?;
                let mut v = Transactions::new(call, data_dir()?).preview(SET, &sels)?;
                v["config"] = read;
                Ok(v)
            }
            "apply" => {
                let pins = two(input, Pin::from_json)?;
                unmigrated(&[pins[0].selection.clone(), pins[1].selection.clone()])?;
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
                    &|i, _, before| {
                        if i == 0 {
                            omp::apply_models(before, &s).map(Some)
                        } else {
                            omp::apply_config(before, &s).map(Some)
                        }
                    },
                    &Yamls,
                    &mut |_| {},
                )
            }
            "clear" => Transactions::new(call, data_dir()?).clear(
                SET,
                &two(input, Pin::from_json)?,
                &Yamls,
                &mut |_| {},
            ),
            // Restore Backup puts the originals back whatever the files hold now: the person's
            // way back always works.
            "restore" => Transactions::new(call, data_dir()?).restore(
                SET,
                &two(input, Pin::from_json)?,
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
#[path = "omp_tests.rs"]
mod omp_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(Omp) {
        eprintln!("omp-setup: {e}");
        std::process::exit(1);
    }
}
