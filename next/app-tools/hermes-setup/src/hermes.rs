//! What pluxy writes into Hermes Agent's `config.yaml`, and what it reads back from it.
//!
//! The file gets a keyed provider, `providers.pluxy`, in the form Hermes documents: the core's
//! OpenAI-compatible address (`api`), the client key (`api_key`), the chat completions transport
//! and the chosen model (`default_model`). Keys in that entry that would win over these at run time
//! (`api_mode`, `key_env`, `api_key_env`, `key_cmd`, and `enabled` when it is not true) are taken
//! out; the person's other keys there (`models`, `extra_headers`, ...) and every other provider
//! stay. The `model` section then selects it as `hermes model` does for a keyed provider:
//! `provider: custom:pluxy`, `default: <model>`, `api_mode: chat_completions`, without the
//! `base_url`, `api_key`, `key_env` and `api_key_env` of an earlier endpoint (`key_env` would win
//! over the entry's key). Everything else in the file stays.

use crate::yaml::{edit, parse};
use pluxy_program::Error;
use serde_json::{json, Map, Value};

/// The provider key pluxy writes.
pub const PROVIDER: &str = "pluxy";
/// `model.provider` for it: Hermes's own spelling for a keyed provider.
pub const SELECTOR: &str = "custom:pluxy";
/// The old desktop app's provider name, left as it is.
pub const LEGACY_PROVIDER: &str = "cpa-gui";
/// The transport the provider speaks.
pub const TRANSPORT: &str = "chat_completions";

/// Keys of `providers.pluxy` that Hermes reads before pluxy's own: removed by an apply.
const OVERRIDES: [&str; 4] = ["api_mode", "key_env", "api_key_env", "key_cmd"];
/// Keys of `model` that belong to an earlier endpoint: removed by an apply.
const STALE: [&str; 4] = ["base_url", "api_key", "key_env", "api_key_env"];

/// What an apply writes, checked.
pub struct Settings {
    /// The core's OpenAI-compatible address, ending in `/v1`.
    pub base_url: String,
    pub key: String,
    pub model: String,
}

/// The provider's `api` for the core's address: with `/v1`, as an OpenAI-compatible base URL.
pub fn base_url_of(core: &str) -> String {
    let core = core.trim_end_matches('/');
    if core.ends_with("/v1") {
        core.to_string()
    } else {
        format!("{core}/v1")
    }
}

/// Whether a YAML 1.1 reader (Hermes reads config.yaml with ruamel's YAML 1.1 rules) could take
/// the plain text s for something other than text: a boolean or null word in any case, or text
/// starting with a digit, sign or dot made only of characters numbers, dates and times use.
pub fn yaml11_not_text(s: &str) -> bool {
    const WORDS: [&str; 11] = [
        "y", "yes", "n", "no", "true", "false", "on", "off", "null", "~", "=",
    ];
    if s.is_empty() || WORDS.iter().any(|w| s.eq_ignore_ascii_case(w)) {
        return true;
    }
    s.starts_with(|c: char| c.is_ascii_digit() || matches!(c, '+' | '-' | '.'))
        && s.chars().all(|c| {
            c.is_ascii_hexdigit()
                || matches!(
                    c,
                    '+' | '-' | '.' | '_' | ':' | 'x' | 'X' | 'o' | 'O' | 't' | 'T' | 'z' | 'Z'
                )
                || c.eq_ignore_ascii_case(&'i')
                || c.eq_ignore_ascii_case(&'n')
        })
}

/// v[k] when it is a mapping or absent; anything else is a format Hermes would not take here.
fn mapping<'a>(v: &'a Value, k: &str, what: &str) -> Result<Option<&'a Map<String, Value>>, Error> {
    match v.get(k) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Object(m)) => Ok(Some(m)),
        Some(_) => Err(Error::invalid_request(format!("{what} is not a mapping"))),
    }
}

/// The file's `providers.pluxy`, refusing a shape Hermes would not take.
fn provider(cfg: &Value) -> Result<Option<&Map<String, Value>>, Error> {
    let Some(providers) = mapping(cfg, "providers", "the file's providers")? else {
        return Ok(None);
    };
    match providers.get(PROVIDER) {
        None => Ok(None),
        Some(Value::Object(p)) => Ok(Some(p)),
        Some(_) => Err(Error::invalid_request(
            "the file's pluxy provider is not a mapping",
        )),
    }
}

/// The file's `model` section as (provider, default model), refusing a shape Hermes would not
/// take. A `model:` that is plain text is the default model with no provider.
fn model(cfg: &Value) -> Result<(Option<&str>, Option<&str>), Error> {
    fn text<'a>(m: &'a Map<String, Value>, k: &str) -> Result<Option<&'a str>, Error> {
        match m.get(k) {
            None | Some(Value::Null) => Ok(None),
            Some(Value::String(s)) => Ok(Some(s.as_str())),
            Some(_) => Err(Error::invalid_request(format!(
                "the file's model {k} is not text"
            ))),
        }
    }
    match cfg.get("model") {
        None | Some(Value::Null) => Ok((None, None)),
        Some(Value::String(s)) => Ok((None, Some(s.as_str()))),
        Some(Value::Object(m)) => Ok((text(m, "provider")?, text(m, "default")?)),
        Some(_) => Err(Error::invalid_request(
            "the file's model is not a mapping or text",
        )),
    }
}

/// Checks the shapes an apply writes into.
pub fn check(cfg: &Value) -> Result<(), Error> {
    provider(cfg)?;
    model(cfg)?;
    Ok(())
}

/// `m[k]` as a mapping to edit, made when absent or null (the shape was checked before).
fn child<'a>(m: &'a mut Map<String, Value>, k: &str) -> &'a mut Map<String, Value> {
    let c = m.entry(k).or_insert_with(|| json!({}));
    if !c.is_object() {
        *c = json!({});
    }
    c.as_object_mut().expect("a mapping")
}

/// The file after an apply.
pub fn apply(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let cur = parse(before)?;
    check(&cur)?;
    let mut want = cur.clone();
    let root = want.as_object_mut().expect("a parsed file is a mapping");
    let p = child(child(root, "providers"), PROVIDER);
    p.insert("api".into(), json!(s.base_url));
    p.insert("api_key".into(), json!(s.key));
    p.insert("transport".into(), json!(TRANSPORT));
    p.insert("default_model".into(), json!(s.model));
    for k in OVERRIDES {
        p.shift_remove(k);
    }
    if p.get("enabled").is_some_and(|e| e != &json!(true)) {
        p.shift_remove("enabled");
    }
    // A plain-text model is the default model: it becomes a mapping, as Hermes itself does.
    if root.get("model").is_some_and(Value::is_string) {
        root.insert("model".into(), json!({}));
    }
    let m = child(root, "model");
    m.insert("provider".into(), json!(SELECTOR));
    m.insert("default".into(), json!(s.model));
    m.insert("api_mode".into(), json!(TRANSPORT));
    for k in STALE {
        m.shift_remove(k);
    }
    edit(before, &cur, &want)
}

/// Whether a `model.provider` value names pluxy's entry (Hermes matches the key, its name and
/// `custom:<key>`, case-insensitively).
fn selects_pluxy(p: &str) -> bool {
    let p = p.trim().to_ascii_lowercase();
    p == PROVIDER || p == SELECTOR
}

/// What the file holds, never the key: `state` (configured, needs-update, not-configured), the
/// provider's address, whether it has a key, the selected provider and default model, and whether
/// the old app's provider is still there. base_url: the core's address, when known.
pub fn read_back(bytes: Option<&[u8]>, base_url: Option<&str>) -> Result<Value, Error> {
    let cfg = parse(bytes)?;
    let p = provider(&cfg)?;
    let (selected, default) = model(&cfg)?;
    let legacy = cfg
        .get("custom_providers")
        .and_then(Value::as_array)
        .is_some_and(|l| {
            l.iter()
                .any(|e| e.get("name") == Some(&json!(LEGACY_PROVIDER)))
        })
        || mapping(&cfg, "providers", "the file's providers")?
            .is_some_and(|ps| ps.contains_key(LEGACY_PROVIDER));
    let s = |k: &str| p.and_then(|p| p.get(k)).and_then(Value::as_str);
    let api = s("api");
    let key_set = s("api_key").is_some_and(|k| !k.is_empty());
    let ours = selected.is_some_and(selects_pluxy);
    let model = default.filter(|m| ours && !m.is_empty());
    let base_ok = match (api, base_url) {
        (Some(a), Some(want)) => a == base_url_of(want),
        (Some(a), None) => !a.is_empty(),
        _ => false,
    };
    let overridden = p.is_some_and(|p| OVERRIDES.iter().any(|k| p.contains_key(*k)))
        || s("transport") != Some(TRANSPORT)
        || p.and_then(|p| p.get("enabled"))
            .is_some_and(|e| e != &json!(true));
    let state = if p.is_none() && !ours {
        "not-configured"
    } else if base_ok && key_set && !overridden && model.is_some() {
        "configured"
    } else {
        "needs-update"
    };
    Ok(json!({
        "state": state,
        "base_url": api,
        "key_set": key_set,
        "provider": selected,
        "model": model,
        "legacy": legacy,
    }))
}
