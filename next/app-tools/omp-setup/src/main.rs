mod omp;
mod yaml;

use omp::Settings;
use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{backup_call, data_dir, sha256_of, Pin, Selection, Target, Transactions};
use serde_json::{json, Value};
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

/// A preview and the two files' bytes, read after it.
type Read = (Value, [Option<Vec<u8>>; 2]);

/// Runs read again once when the bytes it read are not the ones it pinned (another program wrote
/// a file between the two reads); refuses if they differ again, so the values shown are always
/// those of the files pinned.
fn pinned_read(mut read: impl FnMut() -> Result<Read, Error>) -> Result<Read, Error> {
    for _ in 0..2 {
        let (v, bytes) = read()?;
        let same =
            (0..2).all(|i| v["targets"][i]["sha256"] == json!(sha256_of(bytes[i].as_deref())));
        if same {
            return Ok((v, bytes));
        }
    }
    Err(Error::unavailable(
        "a configuration file changed while it was read: read it again",
    ))
}

/// What a preview shows of the two files. Files Oh My Pi would not take (or would migrate first)
/// are pinned all the same, so Restore Backup still reaches them: their `state` is `refused` and
/// `reason` says why apply and clear refuse them.
fn config_of(
    sels: &[Selection; 2],
    models: Option<&[u8]>,
    config: Option<&[u8]>,
    base_url: Option<&str>,
) -> Value {
    unmigrated(sels)
        .and_then(|_| omp::read_back(models, config, base_url))
        .unwrap_or_else(|e| json!({"state": "refused", "reason": e.message}))
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

/// Where Oh My Pi's installs put `omp` on os, in the order the launcher looks: bun's global copy
/// first (the old app's order; `~/.bun/bin`, as bun's installation page says), then the install
/// script's (`~/.local/bin/omp` from `omp.sh/install`; `omp.exe` in `%LOCALAPPDATA%\omp` from
/// `omp.sh/install.ps1` on Windows, when the app gave that folder).
fn installed(os: &str, home: &Path, local: Option<&Path>) -> Vec<PathBuf> {
    if os != "windows" {
        return vec![home.join(".bun/bin/omp"), home.join(".local/bin/omp")];
    }
    let mut found = vec![home.join(".bun").join("bin").join("omp.exe")];
    found.extend(
        local
            .filter(|l| l.is_absolute())
            .map(|l| l.join("omp").join("omp.exe")),
    );
    found
}

/// How the launcher starts Oh My Pi: `omp` in a terminal, with no arguments or variables (the old
/// app's launch): the first installed copy this system runs (often not on a desktop session's
/// PATH), else `omp` from the PATH.
fn recipe(os: &str, home: &Path, local: Option<&Path>) -> Value {
    let program = installed(os, home, local)
        .into_iter()
        .find(|p| pluxy_files::executable(p))
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_else(|| "omp".to_string());
    json!({"client": "Oh My Pi", "kind": "terminal", "program": program, "args": []})
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
                let tx = Transactions::new(call, data_dir()?);
                let (mut v, [models, config]) = pinned_read(|| {
                    let v = tx.preview(SET, &sels)?;
                    let (_, a) = Target::resolve(&sels[0])?.read_now()?;
                    let (_, b) = Target::resolve(&sels[1])?.read_now()?;
                    Ok((v, [a, b]))
                })?;
                v["config"] = config_of(
                    &sels,
                    models.as_deref(),
                    config.as_deref(),
                    input["base_url"].as_str(),
                );
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
            "clear" => {
                let pins = two(input, Pin::from_json)?;
                // Files Oh My Pi would not take are refused before anything is written; a file
                // that no longer resolves as pinned is the library's to refuse.
                let now = |p: &Pin| Target::place(p).and_then(|t| t.read_now()).ok();
                if let (Some((_, m)), Some((_, c))) = (now(&pins[0]), now(&pins[1])) {
                    omp::check(m.as_deref(), c.as_deref())?;
                }
                Transactions::new(call, data_dir()?).clear(SET, &pins, &Yamls, &mut |_| {})
            }
            // Restore Backup puts the originals back whatever the files hold now: the person's
            // way back always works.
            "restore" => Transactions::new(call, data_dir()?).restore(
                SET,
                &two(input, Pin::from_json)?,
                &mut |_| {},
            ),
            "status" => Transactions::new(call, data_dir()?).status(),
            "recipe" => Ok(recipe(
                std::env::consts::OS,
                &home()?,
                std::env::var_os("LOCALAPPDATA")
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
#[path = "omp_tests.rs"]
mod omp_tests;

#[cfg(test)]
#[path = "main_tests.rs"]
mod main_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(Omp) {
        eprintln!("omp-setup: {e}");
        std::process::exit(1);
    }
}
