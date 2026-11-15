//! Pi's files in its agent folder, as Pi and the CLIProxyAPI provider adapter read them:
//! `cliproxyapi.json` (the adapter's `baseUrl`, `apiKey` and optional `providerId`) and
//! `settings.json` (Pi's `packages`, `defaultProvider` and `defaultModel`), both strict JSON
//! objects (Pi and the adapter parse them with `JSON.parse`). The adapter's model cache,
//! `cliproxyapi-models.json`, is only ever removed.

use pluxy_program::Error;
use pluxy_transactions::{merge_value, Json, Merge};
use serde_json::{json, Map, Value};

/// The adapter's folder in the agent folder, and the local package source that lists it, in the
/// relative form Pi itself writes for a package in the agent folder.
pub const FOLDER: &str = "pluxy-cliproxyapi-provider";
pub const ENTRY: &str = "./pluxy-cliproxyapi-provider";

/// The npm package an older app installed: the same adapter, from the npm registry.
const LEGACY: &str = "npm:@router-for-me/pi-cliproxyapi-provider";

/// The provider ID the adapter registers unless cliproxyapi.json names another.
pub const PROVIDER: &str = "cliproxyapi";

/// What an apply writes, checked.
pub struct Settings {
    pub base_url: String,
    pub key: String,
    pub model: String,
}

/// The JSON object a file holds (None: no file, an empty one), or why Pi would not take it.
fn object(bytes: Option<&[u8]>, name: &str) -> Result<Map<String, Value>, Error> {
    let Some(b) = bytes else {
        return Ok(Map::new());
    };
    if b.starts_with(b"\xef\xbb\xbf") {
        return Err(Error::invalid_request(format!(
            "{name} starts with a byte order mark: save it as plain UTF-8 first"
        )));
    }
    match Json::parse(b).map_err(|e| Error::invalid_request(format!("{name}: {}", e.message)))? {
        Value::Object(m) => Ok(m),
        _ => Err(Error::invalid_request(format!(
            "{name} does not hold a JSON object"
        ))),
    }
}

/// Refuses a key whose value is not a string (absent is fine).
fn string_or_absent(m: &Map<String, Value>, key: &str, name: &str) -> Result<(), Error> {
    match m.get(key) {
        None | Some(Value::String(_)) => Ok(()),
        Some(_) => Err(Error::invalid_request(format!(
            "{name}: {key} is not a string"
        ))),
    }
}

/// A `packages` entry's source: the string, or an object's `source`.
fn source(entry: &Value) -> Option<&str> {
    match entry {
        Value::String(s) => Some(s),
        Value::Object(o) => o.get("source").and_then(Value::as_str),
        _ => None,
    }
}

/// Whether a `packages` entry lists pluxy's adapter folder (`pluxy-cliproxyapi-provider`, with or
/// without `./` and a final `/`).
pub fn is_ours(entry: &Value) -> bool {
    source(entry).is_some_and(|s| {
        let s = s.trim().trim_end_matches('/');
        s.strip_prefix("./").unwrap_or(s) == FOLDER
    })
}

/// Whether a `packages` entry is the older app's npm copy of the adapter, at any version.
pub fn is_legacy(entry: &Value) -> bool {
    source(entry).is_some_and(|s| {
        let s = s.trim();
        s == LEGACY || s.strip_prefix(LEGACY).is_some_and(|v| v.starts_with('@'))
    })
}

/// settings.json, checked: an object whose `packages` (if any) lists strings or objects with a
/// string `source`, and whose default provider and model (if any) are strings.
fn settings_object(bytes: Option<&[u8]>) -> Result<Map<String, Value>, Error> {
    const NAME: &str = "settings.json";
    let m = object(bytes, NAME)?;
    match m.get("packages") {
        None => {}
        Some(Value::Array(a)) if a.iter().all(|e| source(e).is_some()) => {}
        Some(_) => {
            return Err(Error::invalid_request(
                "settings.json: packages is not a list of package sources",
            ))
        }
    }
    for k in ["defaultProvider", "defaultModel"] {
        string_or_absent(&m, k, NAME)?;
    }
    Ok(m)
}

/// cliproxyapi.json, checked: an object whose address, key and provider ID (if any) are strings.
fn config_object(bytes: Option<&[u8]>) -> Result<Map<String, Value>, Error> {
    const NAME: &str = "cliproxyapi.json";
    let m = object(bytes, NAME)?;
    for k in ["baseUrl", "apiKey", "providerId"] {
        string_or_absent(&m, k, NAME)?;
    }
    Ok(m)
}

/// Refuses cliproxyapi.json or settings.json when Pi or the plugin would not take it.
pub fn check_files(config: Option<&[u8]>, settings: Option<&[u8]>) -> Result<(), Error> {
    config_object(config)?;
    settings_object(settings).map(|_| ())
}

/// The provider ID the adapter registers: cliproxyapi.json's `providerId`, else `cliproxyapi`.
/// (A `CLIPROXYAPI_PROVIDER_ID` in Pi's environment would win; the tool cannot see it.)
pub fn provider_of(config: Option<&[u8]>) -> Result<String, Error> {
    let m = config_object(config)?;
    Ok(m.get("providerId")
        .and_then(Value::as_str)
        .map(str::trim)
        .filter(|p| !p.is_empty())
        .unwrap_or(PROVIDER)
        .to_string())
}

/// The core's address as the adapter takes it: its root, without a final `/` or `/v1` (the
/// adapter adds `/v1/models` and `/backend-api` itself).
pub fn base_url_of(base: &str) -> String {
    let b = base.trim_end_matches('/');
    b.strip_suffix("/v1").unwrap_or(b).to_string()
}

/// cliproxyapi.json with pluxy's address and key, everything else kept.
pub fn apply_config(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let mut m = config_object(before)?;
    m.insert("baseUrl".into(), json!(s.base_url));
    m.insert("apiKey".into(), json!(s.key));
    Json::render(&Value::Object(m))
}

/// settings.json listing pluxy's adapter (and not the older app's npm copy) with pluxy's provider
/// and model as Pi's defaults, everything else kept.
pub fn apply_settings(
    before: Option<&[u8]>,
    s: &Settings,
    provider: &str,
) -> Result<Vec<u8>, Error> {
    let mut m = settings_object(before)?;
    let mut packages: Vec<Value> = m
        .get("packages")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default()
        .into_iter()
        .filter(|e| !is_legacy(e))
        .collect();
    if !packages.iter().any(is_ours) {
        packages.push(json!(ENTRY));
    }
    m.insert("packages".into(), Value::Array(packages));
    m.insert("defaultProvider".into(), json!(provider));
    m.insert("defaultModel".into(), json!(s.model));
    Json::render(&Value::Object(m))
}

/// settings.json without pluxy's adapter in `packages`, everything else kept (the bytes as they
/// are when it is not listed).
pub fn uninstall_settings(before: Option<&[u8]>) -> Result<Option<Vec<u8>>, Error> {
    let mut m = settings_object(before)?;
    let Some(Value::Array(packages)) = m.get_mut("packages") else {
        return Ok(before.map(<[u8]>::to_vec));
    };
    if !packages.iter().any(is_ours) {
        return Ok(before.map(<[u8]>::to_vec));
    }
    packages.retain(|e| !is_ours(e));
    Json::render(&Value::Object(m)).map(Some)
}

/// Whether settings.json lists pluxy's adapter.
pub fn registered(settings: Option<&[u8]>) -> Result<bool, Error> {
    Ok(settings_object(settings)?
        .get("packages")
        .and_then(Value::as_array)
        .is_some_and(|p| p.iter().any(is_ours)))
}

/// What the two files hold, never the key: whether pluxy's adapter is listed, the older app's npm
/// copy, the address, whether a key is set, the provider and Pi's defaults. `state` (with the
/// adapter's folder state given) is `configured` when it all points at base_url, `not-configured`
/// when settings.json does not list the adapter, else `needs-update`.
pub fn read_back(
    config: Option<&[u8]>,
    settings: Option<&[u8]>,
    base_url: Option<&str>,
    adapter_ready: bool,
) -> Result<Value, Error> {
    let c = config_object(config)?;
    let st = settings_object(settings)?;
    let provider = provider_of(config)?;
    let packages = st.get("packages").and_then(Value::as_array);
    let registered = packages.is_some_and(|p| p.iter().any(is_ours));
    let legacy = packages.is_some_and(|p| p.iter().any(is_legacy));
    let file_url = c.get("baseUrl").and_then(Value::as_str);
    let key_set = c
        .get("apiKey")
        .and_then(Value::as_str)
        .is_some_and(|k| !k.is_empty());
    let default_provider = st.get("defaultProvider").and_then(Value::as_str);
    let model = st.get("defaultModel").and_then(Value::as_str);
    let url_ok = match (file_url, base_url) {
        (Some(f), Some(b)) => base_url_of(f) == base_url_of(b),
        (Some(_), None) => true,
        _ => false,
    };
    let state = if !registered {
        "not-configured"
    } else if adapter_ready
        && url_ok
        && key_set
        && default_provider == Some(provider.as_str())
        && model.is_some_and(|m| !m.is_empty())
    {
        "configured"
    } else {
        "needs-update"
    };
    Ok(json!({
        "state": state,
        "base_url": file_url,
        "key_set": key_set,
        "provider": provider,
        "default_provider": default_provider,
        "model": model,
        "registered": registered,
        "legacy": legacy,
    }))
}

/// Clear's three-way merge of Pi's files: the library's JSON merge (the person's edits win,
/// pluxy's untouched values go back to the original's), plus Pi's own rules:
/// - pluxy's adapter never stays listed in `packages`, and entries the apply took out of it (the
///   older app's npm copy) come back unless the person has them already;
/// - a file the apply removed (the model cache) is removed again when it did not exist before the
///   apply, and otherwise stays as the adapter last wrote it.
pub struct PiMerge;

impl Merge for PiMerge {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        if current == applied {
            return Ok(original.map(<[u8]>::to_vec));
        }
        if applied.is_none() {
            return Ok(if original.is_none() {
                None
            } else {
                current.map(<[u8]>::to_vec)
            });
        }
        let Some(current) = current else {
            return Ok(None);
        };
        let parse = |b: Option<&[u8]>| b.map(Json::parse).transpose();
        let (o, a, c) = (parse(original)?, parse(applied)?, Json::parse(current)?);
        let Some(mut merged) = merge_value(o.as_ref(), a.as_ref(), Some(&c)) else {
            return Ok(None);
        };
        let entries = |v: Option<&Value>| -> Vec<Value> {
            v.and_then(|v| v["packages"].as_array())
                .cloned()
                .unwrap_or_default()
        };
        let (from_original, from_applied) = (entries(o.as_ref()), entries(a.as_ref()));
        if let Some(packages) = merged.get_mut("packages").and_then(Value::as_array_mut) {
            packages.retain(|e| !is_ours(e));
            for e in from_original {
                if !is_ours(&e) && !from_applied.contains(&e) && !packages.contains(&e) {
                    packages.push(e);
                }
            }
        }
        Json::render(&merged).map(Some)
    }
}
