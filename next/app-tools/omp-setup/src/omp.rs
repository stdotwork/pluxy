//! What pluxy writes into Oh My Pi's two files, and what it reads back from them.
//!
//! The models file (`models.yml`) gets a `pluxy` provider: the core's OpenAI-compatible address,
//! the client key, the OpenAI Responses API and the discovery of the core's models from its
//! `/v1/models` list (Oh My Pi's `openai-models-list`). Only these keys of the provider are pluxy's:
//! the person's own entries under it (`models`, `modelOverrides`, `headers`, ...) and every other
//! provider stay. The settings file (`config.yml`) gets `modelRoles.default: pluxy/<model>`; the
//! other roles and settings stay.

use crate::yaml::{edit, parse};
use pluxy_program::Error;
use serde_json::{json, Map, Value};

/// The provider ID pluxy writes.
pub const PROVIDER: &str = "pluxy";
/// The old desktop app's provider ID, left as it is.
pub const LEGACY_PROVIDER: &str = "easy-cliproxyapi";
/// The API the provider speaks.
pub const API: &str = "openai-responses";
/// The model discovery the provider uses: the core's `/v1/models` list.
pub const DISCOVERY: &str = "openai-models-list";

/// What an apply writes, checked.
pub struct Settings {
    /// The core's OpenAI-compatible address, ending in `/v1`.
    pub base_url: String,
    pub key: String,
    pub model: String,
}

/// The provider's `baseUrl` for the core's address: with `/v1`, as Oh My Pi's openai-models-list
/// discovery and requests expect.
pub fn base_url_of(core: &str) -> String {
    let core = core.trim_end_matches('/');
    if core.ends_with("/v1") {
        core.to_string()
    } else {
        format!("{core}/v1")
    }
}

/// v[k] when it is a mapping or absent; anything else is a format Oh My Pi would not take.
fn mapping<'a>(v: &'a Value, k: &str, what: &str) -> Result<Option<&'a Map<String, Value>>, Error> {
    match v.get(k) {
        None => Ok(None),
        Some(Value::Object(m)) => Ok(Some(m)),
        Some(_) => Err(Error::invalid_request(format!("{what} is not a mapping"))),
    }
}

/// The models file's `providers.pluxy`, refusing a file whose shape Oh My Pi would not take.
fn provider(models: &Value) -> Result<Option<&Map<String, Value>>, Error> {
    let Some(providers) = mapping(models, "providers", "the models file's providers")? else {
        return Ok(None);
    };
    let Some(p) = providers.get(PROVIDER) else {
        return Ok(None);
    };
    let p = p.as_object().ok_or_else(|| {
        Error::invalid_request("the models file's pluxy provider is not a mapping")
    })?;
    mapping(
        &Value::Object(p.clone()),
        "discovery",
        "the pluxy provider's discovery",
    )?;
    Ok(Some(p))
}

/// The settings file's `modelRoles.default`, refusing a shape Oh My Pi would not take.
fn default_role(config: &Value) -> Result<Option<&str>, Error> {
    let Some(roles) = mapping(config, "modelRoles", "the settings file's modelRoles")? else {
        return Ok(None);
    };
    match roles.get("default") {
        None | Some(Value::Null) => Ok(None),
        Some(Value::String(s)) => Ok(Some(s)),
        Some(_) => Err(Error::invalid_request(
            "the settings file's default model role is not text",
        )),
    }
}

/// `v[k]` as a mapping to edit, made when absent (the shape was checked before).
fn child<'a>(v: &'a mut Value, k: &str) -> &'a mut Value {
    let m = v.as_object_mut().expect("a checked mapping");
    m.entry(k).or_insert_with(|| json!({}))
}

/// The models file after an apply.
pub fn apply_models(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let cur = parse(before)?;
    provider(&cur)?;
    let mut want = cur.clone();
    let p = child(child(&mut want, "providers"), PROVIDER);
    p["baseUrl"] = json!(s.base_url);
    p["apiKey"] = json!(s.key);
    p["api"] = json!(API);
    child(p, "discovery")["type"] = json!(DISCOVERY);
    edit(before, &cur, &want)
}

/// The settings file after an apply.
pub fn apply_config(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let cur = parse(before)?;
    default_role(&cur)?;
    let mut want = cur.clone();
    child(&mut want, "modelRoles")["default"] = json!(format!("{PROVIDER}/{}", s.model));
    edit(before, &cur, &want)
}

/// What the files hold, never the key: `state` (configured, needs-update, not-configured), the
/// provider's address, whether it has a key, the default role and the pluxy model it names, and
/// whether the old app's provider is still there. base_url: the core's address, when known.
pub fn read_back(
    models: Option<&[u8]>,
    config: Option<&[u8]>,
    base_url: Option<&str>,
) -> Result<Value, Error> {
    let (m, c) = (parse(models)?, parse(config)?);
    let p = provider(&m)?;
    let role = default_role(&c)?;
    let legacy = mapping(&m, "providers", "the models file's providers")?
        .is_some_and(|ps| ps.contains_key(LEGACY_PROVIDER));
    let s = |k: &str| p.and_then(|p| p.get(k)).and_then(Value::as_str);
    let base = s("baseUrl");
    let key_set = s("apiKey").is_some_and(|k| !k.is_empty());
    let model = role
        .and_then(|r| r.strip_prefix(&format!("{PROVIDER}/")))
        .filter(|m| !m.is_empty());
    let discovery = p
        .and_then(|p| p.get("discovery"))
        .and_then(|d| d.get("type"))
        .and_then(Value::as_str);
    let base_ok = match (base, base_url) {
        (Some(b), Some(want)) => b == base_url_of(want),
        (Some(b), None) => !b.is_empty(),
        _ => false,
    };
    let state = if p.is_none() && model.is_none() {
        "not-configured"
    } else if base_ok
        && key_set
        && matches!(s("api"), Some("openai-responses" | "openai-completions"))
        && discovery == Some(DISCOVERY)
        && model.is_some()
    {
        "configured"
    } else {
        "needs-update"
    };
    Ok(json!({
        "state": state,
        "base_url": base,
        "key_set": key_set,
        "model": model,
        "default_role": role,
        "legacy": legacy,
    }))
}
