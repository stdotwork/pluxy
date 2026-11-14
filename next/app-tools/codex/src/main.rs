//! pluxy/codex-setup: sets up Codex to use pluxy's core, and starts it, for pluxy's desktop app
//! (README.md). Its operations: `preview` (read: pins Codex's config.toml and the model catalog
//! beside it and reads their values back, never the key), `import` (read: the catalog entries the
//! core's served models would get, from pluxy's Codex templates; nothing is written), `apply`,
//! `clear` and `restore` (write: each call asks the person; `apply` receives the client key as a
//! secret's value and writes it where Codex reads it), `status` (read), `recipe` (read: how the
//! launcher starts Codex CLI, Codex App, or restarts the Codex CLI background service) and the
//! backup browser's standard operations. Writes go through pluxy-transactions: link-preserving,
//! journalled, with the originals and manual backups in the host's vault.

mod catalog;
mod config;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_transactions::{
    backup_call, data_dir, json_leaves, Json, Merge, Pin, Selection, Target, Transactions,
};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};

/// The one set this tool applies: config.toml and the catalog, in that order.
const SET: &str = "codex";

struct Codex;

/// Codex's folder in the app's home folder (the tool's environment has no CODEX_HOME: a person
/// who moved it selects that folder).
fn codex_home() -> Result<PathBuf, Error> {
    std::env::var_os("HOME")
        .filter(|h| !h.is_empty())
        .map(|h| PathBuf::from(h).join(".codex"))
        .ok_or_else(|| Error::unavailable("the app gave no home folder (HOME)"))
}

/// The input's two targets, read with f: config.toml, then the catalog.
fn two<T>(input: &Value, f: fn(&Value) -> Result<T, Error>) -> Result<[T; 2], Error> {
    match input["targets"].as_array().map(Vec::as_slice) {
        Some([c, m]) => Ok([f(c)?, f(m)?]),
        _ => Err(Error::invalid_request(
            "targets holds config.toml and the model catalog",
        )),
    }
}

/// The selections a preview names: its two targets, or config.toml and the catalog in ~/.codex.
fn selections(input: &Value) -> Result<[Selection; 2], Error> {
    match input.get("targets") {
        None => {
            let home = codex_home()?;
            Ok([
                Selection::File(home.join("config.toml")),
                Selection::File(home.join(catalog::FILE)),
            ])
        }
        Some(_) => two(input, Selection::from_json),
    }
}

/// The path a selection names, as the person gave it (not the link's target).
fn selected(s: &Selection) -> PathBuf {
    match s {
        Selection::File(f) => f.clone(),
        Selection::Root { root, path } => root.join(path),
    }
}

/// `model_catalog_json`: the catalog's file name when it is in config.toml's folder (Codex reads
/// it from there), else its full path.
fn catalog_setting(config: &Selection, catalog: &Selection) -> String {
    let (c, m) = (selected(config), selected(catalog));
    if c.parent() == m.parent() {
        if let Some(name) = m.file_name() {
            return name.to_string_lossy().into_owned();
        }
    }
    m.to_string_lossy().into_owned()
}

/// Whether bytes are the catalog (JSON) rather than config.toml: TOML never starts with `{`.
fn is_json(bytes: &[u8]) -> bool {
    bytes.iter().find(|b| !b.is_ascii_whitespace()) == Some(&b'{')
}

/// The merge for Clear: the catalog's as JSON, config.toml's as TOML, each refusing a file Codex
/// would not take as its own first.
struct Checked;

impl Merge for Checked {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        if current == applied {
            return Ok(original.map(<[u8]>::to_vec));
        }
        let Some(cur) = current else {
            return Ok(None);
        };
        if is_json(cur) {
            catalog::parse(current)?;
            Json.merge(original, applied, current)
        } else {
            config::merge(original, applied, cur)
        }
    }
}

/// A file's settings for a backup's diff, whichever of the two it is.
fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Value)>> {
    if is_json(bytes) {
        json_leaves(bytes)
    } else {
        config::leaves(bytes)
    }
}

/// What an apply's input asks for: config.toml's settings, with key, and the catalog's models.
fn settings(
    input: &Value,
    key: &str,
    sels: &[Selection; 2],
) -> Result<(config::Settings, Vec<catalog::Model>, String), Error> {
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
    let short = |field: &str, may_be_empty: bool| -> Result<String, Error> {
        let s = input[field].as_str().unwrap_or_default().trim().to_string();
        if (s.is_empty() && !may_be_empty)
            || s.chars().count() > 240
            || s.chars().any(char::is_control)
        {
            return Err(Error::invalid_request(format!(
                "{field} is a model name of at most 240 characters"
            )));
        }
        Ok(s)
    };
    let model = short("model", false)?;
    let approval = short("approval_model", true)?;
    let list = input["models"]
        .as_array()
        .filter(|l| (1..=catalog::MODELS_MAX).contains(&l.len()))
        .ok_or_else(|| {
            Error::invalid_request(format!(
                "models lists 1 to {} catalog models: import them from the core first",
                catalog::MODELS_MAX
            ))
        })?;
    let models = list
        .iter()
        .map(catalog::Model::from_input)
        .collect::<Result<Vec<_>, _>>()?;
    for (i, m) in models.iter().enumerate() {
        if models[..i].iter().any(|x| x.slug == m.slug) {
            return Err(Error::invalid_request(format!(
                "{} is listed twice",
                m.slug
            )));
        }
    }
    if !models.iter().any(|m| m.slug == model) {
        return Err(Error::invalid_request(format!(
            "the model {model} is not in the catalog"
        )));
    }
    let a = &input["agents"];
    let agents = config::Agents {
        enabled: a["enabled"].as_bool().unwrap_or(true),
        max_threads: match &a["max_threads"] {
            Value::Null => None,
            n => Some(
                n.as_u64()
                    .filter(|n| (1..=1024).contains(n))
                    .ok_or_else(|| {
                        Error::invalid_request("max_threads is a whole number from 1 to 1024")
                    })?,
            ),
        },
        model: {
            let s = a["model"].as_str().unwrap_or_default().trim().to_string();
            if s.chars().count() > 240 || s.chars().any(char::is_control) {
                return Err(Error::invalid_request(
                    "the subagent model is at most 240 characters",
                ));
            }
            s
        },
        effort: {
            let s = a["effort"].as_str().unwrap_or_default().to_string();
            let ok = [
                "", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra",
            ];
            if !ok.contains(&s.as_str()) {
                return Err(Error::invalid_request(format!("no reasoning effort {s:?}")));
            }
            s
        },
        interrupt: a["interrupt"].as_bool().unwrap_or(true),
    };
    Ok((
        config::Settings {
            base_url: config::base_url_of(base),
            key: key.to_string(),
            model,
            catalog: catalog_setting(&sels[0], &sels[1]),
            remote_compaction: input["remote_compaction"].as_bool().unwrap_or(false),
            agents,
        },
        models,
        approval,
    ))
}

/// The launch of target: Codex CLI in a terminal, Codex App (a program the person names: Codex
/// has no Linux app), or the Codex CLI background service's restart.
fn recipe(target: &str) -> Result<Value, Error> {
    let home = codex_home()?;
    let home = home.parent().unwrap_or(Path::new("/"));
    match target {
        "cli" => Ok(json!({"client": "Codex", "kind": "terminal", "program": "codex", "args": []})),
        // No program of its own: the launch needs the person's program path (its override).
        "app" => Ok(json!({"client": "Codex App", "kind": "direct", "program": "", "args": []})),
        "service" => Ok(
            json!({"client": "Codex CLI service", "kind": "direct", "program": "codex",
            "args": ["app-server", "daemon", "restart"], "cwd": home}),
        ),
        t => Err(Error::invalid_request(format!(
            "target is cli, app or service, not {t:?}"
        ))),
    }
}

impl Handler for Codex {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        let input = &call.input;
        // The backup browser: manual backups of both files, diffed by setting name.
        if call.op.starts_with("backup-") {
            let tx = Transactions::new(call, data_dir()?);
            if let Some(r) = backup_call(call, &tx, SET, &leaves) {
                return r;
            }
        }
        match call.op.as_str() {
            "preview" => {
                let sels = selections(input)?;
                // Files Codex would not take are refused before anything is pinned.
                let (_, config_bytes) = Target::resolve(&sels[0])?.read_now()?;
                let (_, catalog_bytes) = Target::resolve(&sels[1])?.read_now()?;
                let mut read =
                    config::read_back(config_bytes.as_deref(), input["base_url"].as_str())?;
                let cat = catalog::read_back(catalog_bytes.as_deref())?;
                read["models"] = cat["models"].clone();
                read["approval_model"] = cat["approval_model"].clone();
                let mut v = Transactions::new(call, data_dir()?).preview(SET, &sels)?;
                v["settings"] = read;
                Ok(v)
            }
            "import" => catalog::import(&input["models"]),
            "apply" => {
                let pins = two(input, Pin::from_json)?;
                let key = input["key"]
                    .as_str()
                    .and_then(|r| call.secrets.get(r))
                    .and_then(Value::as_str)
                    .ok_or_else(|| {
                        Error::invalid_request("key names no secret the app delivered")
                    })?;
                let sels = [pins[0].selection.clone(), pins[1].selection.clone()];
                let (s, models, approval) = settings(input, key, &sels)?;
                Transactions::new(call, data_dir()?).apply(
                    SET,
                    &pins,
                    &|i, _, before| {
                        if i == 0 {
                            config::apply(before, &s).map(Some)
                        } else {
                            catalog::apply(before, &models, &approval).map(Some)
                        }
                    },
                    &Checked,
                    &mut |_| {},
                )
            }
            "clear" => Transactions::new(call, data_dir()?).clear(
                SET,
                &two(input, Pin::from_json)?,
                &Checked,
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
            "recipe" => recipe(input["target"].as_str().unwrap_or("cli")),
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
#[path = "catalog_tests.rs"]
mod catalog_tests;

#[cfg(test)]
#[path = "manifest_tests.rs"]
mod manifest_tests;

fn main() {
    if let Err(e) = run(Codex) {
        eprintln!("codex-setup: {e}");
        std::process::exit(1);
    }
}
