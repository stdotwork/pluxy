//! Grok Build's user config (`config.toml`): which files it takes as its own, what Apply writes
//! into one, what a preview reads back, and the merge Clear makes. Keys are Grok Build's own, as
//! its CLI reads them (README.md). The file is edited in place with toml_edit, so comments,
//! formatting and everything Apply does not set stay as the person left them.

use pluxy_program::Error;
use pluxy_transactions::toml::toml_edit::{DocumentMut, Item, Table};
use pluxy_transactions::toml::{self, set, table_mut};
use serde_json::{json, Value as Json};

/// The prefix of the model entries Apply writes: `[model."pluxy/<model>"]`.
pub const PREFIX: &str = "pluxy/";
/// The prefix an older app's entries carry; they are left as they are and reported.
pub const LEGACY: &str = "cpa-gui/";
/// The API pluxy's entries use: OpenAI Chat Completions, as the older app wrote.
const BACKEND: &str = "chat_completions";
/// One model entry Apply writes.
#[derive(Debug, Clone, PartialEq)]
pub struct Model {
    /// The model's name at the core.
    pub model: String,
    /// The name Grok Build's model list shows.
    pub name: String,
    pub context_window: u64,
}

/// What Apply writes into config.toml.
#[derive(Debug)]
pub struct Settings {
    /// The core's address with `/v1`.
    pub base_url: String,
    pub key: String,
    /// The model Grok Build starts with: one of models.
    pub default: String,
    pub models: Vec<Model>,
}

/// What every refusal of a file starts with.
const REFUSAL: &str = "this is not a Grok Build config: ";

fn refuse(why: impl std::fmt::Display) -> Error {
    toml::refuse(REFUSAL, why)
}

/// The entry name Apply writes for model.
pub fn alias(model: &str) -> String {
    format!("{PREFIX}{model}")
}

/// A table at key of t, if there is one: refused when key holds anything but a table.
fn table_at<'a>(t: &'a Table, key: &str, name: &str) -> Result<Option<&'a Table>, Error> {
    toml::table_at(t, key, name, REFUSAL)
}

/// The config in bytes (none, or only white space: an empty file), refused unless Grok Build
/// would take it as its own and Apply can edit it: TOML, with `models` and `model` tables where
/// present, `models.default` a string, and every `model."pluxy/…"` entry a table.
pub fn parse(bytes: Option<&[u8]>) -> Result<DocumentMut, Error> {
    let doc = toml::read(bytes, REFUSAL)?;
    let root = doc.as_table();
    if let Some(models) = table_at(root, "models", "models")? {
        match models.get("default") {
            None => {}
            Some(i) if i.as_str().is_some() => {}
            Some(_) => return Err(refuse("models.default is not a string")),
        }
    }
    if let Some(model) = table_at(root, "model", "model")? {
        for (k, i) in model.iter() {
            if k.starts_with(PREFIX) && !matches!(i, Item::Table(_)) {
                return Err(refuse(format!("model.\"{k}\" is not a table")));
            }
        }
    }
    Ok(doc)
}

/// The config file as Apply leaves it: before (the file now, None if there is none) with Grok
/// Build's default model one of the core's, through s. pluxy's entries for models no longer
/// listed go; everything else in it stays, the person's own keys in pluxy's entries too.
pub fn apply(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let mut doc = parse(before)?;
    let root = doc.as_table_mut();
    set(
        table_mut(root, "models", false),
        "default",
        alias(&s.default),
    );
    let entries = table_mut(root, "model", true);
    let listed: Vec<String> = s.models.iter().map(|m| alias(&m.model)).collect();
    let stale: Vec<String> = entries
        .iter()
        .map(|(k, _)| k.to_string())
        .filter(|k| k.starts_with(PREFIX) && !listed.contains(k))
        .collect();
    for k in stale {
        entries.remove(&k);
    }
    for (m, a) in s.models.iter().zip(&listed) {
        let e = table_mut(entries, a, false);
        set(e, "model", m.model.as_str());
        set(e, "base_url", s.base_url.as_str());
        set(e, "name", m.name.as_str());
        set(e, "api_key", s.key.as_str());
        set(e, "api_backend", BACKEND);
        set(e, "context_window", m.context_window as i64);
    }
    Ok(doc.to_string().into_bytes())
}

/// What a preview shows of the config in bytes, never the key: whether it points Grok Build at
/// the core (at base_url, when given), the default model, pluxy's model entries (each one's model
/// as the entry names it when it has no `model` of its own) and how many entries an older app left.
/// It is configured only when every pluxy entry is whole: a model, the Chat Completions API, the
/// default's base URL and a key; an entry missing any of them needs repair.
pub fn read_back(bytes: Option<&[u8]>, base_url: Option<&str>) -> Result<Json, Error> {
    let doc = parse(bytes)?;
    let root = doc.as_table();
    let s = |t: &Table, k: &str| {
        t.get(k)
            .and_then(Item::as_str)
            .filter(|v| !v.is_empty())
            .map(str::to_string)
    };
    let default = root
        .get("models")
        .and_then(Item::as_table)
        .and_then(|m| s(m, "default"));
    let entries = root.get("model").and_then(Item::as_table);
    let ours: Vec<(&str, &Table)> = entries
        .into_iter()
        .flat_map(|t| t.iter())
        .filter(|(k, _)| k.starts_with(PREFIX))
        .filter_map(|(k, i)| Some((k, i.as_table()?)))
        .collect();
    let legacy = entries
        .into_iter()
        .flat_map(|t| t.iter())
        .filter(|(k, _)| k.starts_with(LEGACY))
        .count();
    // An entry's model: its own `model`, else the one its name says.
    let model_of =
        |k: &str, t: &Table| s(t, "model").unwrap_or_else(|| k[PREFIX.len()..].to_string());
    let chosen = default
        .as_deref()
        .and_then(|d| ours.iter().find(|(k, _)| *k == d));
    let pointed = chosen.and_then(|(_, t)| s(t, "base_url"));
    let key_set = chosen.and_then(|(_, t)| s(t, "api_key")).is_some();
    let whole = |t: &Table| {
        s(t, "model").is_some()
            && s(t, "api_backend").as_deref() == Some(BACKEND)
            && s(t, "api_key").is_some()
            && s(t, "base_url") == pointed
    };
    let want = base_url.map(base_url_of);
    let state = if ours.is_empty() {
        "not-configured"
    } else if pointed.is_some()
        && want.as_ref().is_none_or(|w| Some(w) == pointed.as_ref())
        && ours.iter().all(|(_, t)| whole(t))
    {
        "configured"
    } else {
        "needs-update"
    };
    Ok(json!({
        "state": state,
        "base_url": pointed,
        "key_set": key_set,
        "default": default,
        "model": chosen.map(|(k, t)| model_of(k, t)),
        "models": ours.iter().map(|(k, t)| json!({
            "model": model_of(k, t),
            "name": s(t, "name"),
            "context_window": t.get("context_window").and_then(Item::as_integer),
        })).collect::<Vec<_>>(),
        "legacy": legacy,
    }))
}

/// The base URL of pluxy's entries for the core at address: the address with `/v1` once.
pub fn base_url_of(address: &str) -> String {
    let a = address.trim_end_matches('/');
    if a.ends_with("/v1") {
        a.to_string()
    } else {
        format!("{a}/v1")
    }
}

/// The config as current would be without what applied changed from original
/// ([`toml::merge`]), each file read through [`parse`].
pub fn merge(
    original: Option<&[u8]>,
    applied: Option<&[u8]>,
    current: &[u8],
) -> Result<Option<Vec<u8>>, Error> {
    toml::merge(parse, original, applied, current)
}

/// The merge for Clear: refuses a config Grok Build would not take as its own first.
pub struct Checked;

impl pluxy_transactions::Merge for Checked {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        if current == applied {
            return Ok(original.map(<[u8]>::to_vec));
        }
        match current {
            None => Ok(None),
            Some(cur) => merge(original, applied, cur),
        }
    }
}

/// A config file's settings for a backup's diff ([`toml::leaves`]). None when it is not TOML.
pub fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Json)>> {
    toml::leaves(parse, bytes)
}
