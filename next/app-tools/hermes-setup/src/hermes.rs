//! What pluxy writes into Hermes Agent's `config.yaml`, and what it reads back from it.
//!
//! The file gets a keyed provider, `providers.pluxy`, in the form Hermes documents: the core's
//! OpenAI-compatible address (`api`), the client key (`api_key`), the chat completions transport
//! and the chosen model (`default_model`). Keys in that entry that would win over these at run time
//! (`api_mode`, `key_env`, `api_key_env`, `key_cmd`, the address as `base_url` or `url`, `model`,
//! the camelCase spellings `baseUrl`, `apiKey`, `apiMode`, `keyEnv`, `apiKeyEnv`, `defaultModel`,
//! and `enabled` when it is not true) are taken out; the person's other keys there (`models`, `extra_headers`, ...) and every other provider
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

/// Keys of `providers.pluxy` that Hermes reads before pluxy's own, or in their place (its
/// camelCase spellings, and the address and model under their other names): removed by an apply.
const OVERRIDES: [&str; 13] = [
    "api_mode",
    "key_env",
    "api_key_env",
    "key_cmd",
    "base_url",
    "url",
    "model",
    "baseUrl",
    "apiKey",
    "apiMode",
    "keyEnv",
    "apiKeyEnv",
    "defaultModel",
];
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

/// Whether a YAML 1.1 reader (Hermes reads config.yaml with ruamel's YAML 1.1 rules) takes the
/// plain text s for something other than text: a boolean (`y`, `yes`, `on`, ... in YAML 1.1's
/// spellings), a null, the value key `=`, an integer (decimal, `0b`, `0x`, octal, sexagesimal), a
/// float (also `.inf` and `.nan`) or a timestamp. Anything else, such as a hex key or a UUID, is
/// text.
pub fn yaml11_not_text(s: &str) -> bool {
    const WORDS: [&str; 33] = [
        "y", "Y", "yes", "Yes", "YES", "n", "N", "no", "No", "NO", "true", "True", "TRUE", "false",
        "False", "FALSE", "on", "On", "ON", "off", "Off", "OFF", "~", "null", "Null", "NULL", "=",
        ".nan", ".NaN", ".NAN", ".inf", ".Inf", ".INF",
    ];
    if s.is_empty() || WORDS.contains(&s) {
        return true;
    }
    let unsigned = s.strip_prefix(['-', '+']).unwrap_or(s);
    matches!(unsigned, ".inf" | ".Inf" | ".INF")
        || int11(unsigned)
        || float11(s, unsigned)
        || timestamp11(s)
}

/// `[0-9][0-9_]*` at the start of s: its length (0 when s does not start with a digit).
fn digits(s: &str) -> usize {
    match s.chars().next() {
        Some(c) if c.is_ascii_digit() => s
            .find(|c: char| !(c.is_ascii_digit() || c == '_'))
            .unwrap_or(s.len()),
        _ => 0,
    }
}

/// `(:[0-5]?[0-9])+` at the start of s: its length (0 when there is none).
fn sexagesimal(s: &str) -> usize {
    let b = s.as_bytes();
    let mut i = 0;
    loop {
        if b.get(i) != Some(&b':') {
            return i;
        }
        let d = |j: usize| b.get(j).is_some_and(u8::is_ascii_digit);
        let n = if d(i + 1) && d(i + 2) && b[i + 1] <= b'5' {
            2
        } else if d(i + 1) {
            1
        } else {
            return i;
        };
        i += 1 + n;
    }
}

/// YAML 1.1's integers, without their sign.
fn int11(s: &str) -> bool {
    let all = |r: &str, f: fn(char) -> bool| !r.is_empty() && r.chars().all(|c| f(c) || c == '_');
    if let Some(r) = s.strip_prefix("0b") {
        return all(r, |c| c == '0' || c == '1');
    }
    if let Some(r) = s.strip_prefix("0x") {
        return all(r, |c| c.is_ascii_hexdigit());
    }
    if let Some(r) = s.strip_prefix('0') {
        let octal = |r: &str| r.chars().all(|c| matches!(c, '0'..='7' | '_'));
        return match r.strip_prefix('o') {
            Some(o) => !o.is_empty() && octal(o),
            None => octal(r),
        };
    }
    if !s.starts_with(|c: char| matches!(c, '1'..='9')) {
        return false;
    }
    let n = digits(s);
    n == s.len() || (sexagesimal(&s[n..]) == s.len() - n)
}

/// YAML 1.1's floats (ruamel's spellings): s as written, and without its sign.
fn float11(s: &str, unsigned: &str) -> bool {
    // `.5`, `.5e+3`: no sign.
    if let Some(r) = s.strip_prefix('.') {
        let n = digits(r);
        return n > 0 && (n == r.len() || exponent(&r[n..], true));
    }
    let n = digits(unsigned);
    if n == 0 {
        return false;
    }
    let r = &unsigned[n..];
    if let Some(f) = r.strip_prefix('.') {
        let m = f
            .find(|c: char| !(c.is_ascii_digit() || c == '_'))
            .unwrap_or(f.len());
        return m == f.len() || exponent(&f[m..], false);
    }
    if exponent(r, false) {
        return true;
    }
    // Sexagesimal with a fraction: `1:30.5`.
    let m = sexagesimal(r);
    m > 0
        && r[m..]
            .strip_prefix('.')
            .is_some_and(|f| f.chars().all(|c| c.is_ascii_digit() || c == '_'))
}

/// `[eE][-+]?[0-9]+` is all of s (with a required sign when signed).
fn exponent(s: &str, signed: bool) -> bool {
    let Some(r) = s.strip_prefix(['e', 'E']) else {
        return false;
    };
    let r = match r.strip_prefix(['-', '+']) {
        Some(r) => r,
        None if signed => return false,
        None => r,
    };
    !r.is_empty() && r.chars().all(|c| c.is_ascii_digit())
}

/// YAML 1.1's timestamps: `2001-12-14`, or with a time (`T` or `t`, no spaces here as pluxy's
/// values hold none), seconds' fraction and zone.
fn timestamp11(s: &str) -> bool {
    let b = s.as_bytes();
    let d = |i: usize| b.get(i).is_some_and(u8::is_ascii_digit);
    // `n` digits (1 or 2 when one_or_two) from i: the index after them.
    let num = |i: usize, one_or_two: bool| -> Option<usize> {
        if !d(i) {
            return None;
        }
        if d(i + 1) {
            Some(i + 2)
        } else if one_or_two {
            Some(i + 1)
        } else {
            None
        }
    };
    if !(d(0) && d(1) && d(2) && d(3)) || b.get(4) != Some(&b'-') {
        return false;
    }
    // The date alone takes exactly two-digit month and day.
    if b.len() == 10 && d(5) && d(6) && b[7] == b'-' && d(8) && d(9) {
        return true;
    }
    let step = || -> Option<()> {
        let i = num(5, true)?;
        (b.get(i) == Some(&b'-')).then_some(())?;
        let i = num(i + 1, true)?;
        matches!(b.get(i), Some(b'T' | b't')).then_some(())?;
        let i = num(i + 1, true)?;
        (b.get(i) == Some(&b':')).then_some(())?;
        let i = num(i + 1, false)?;
        (b.get(i) == Some(&b':')).then_some(())?;
        let mut i = num(i + 1, false)?;
        if b.get(i) == Some(&b'.') {
            i += 1;
            while d(i) {
                i += 1;
            }
        }
        match b.get(i) {
            None => Some(()),
            Some(b'Z') if i + 1 == b.len() => Some(()),
            Some(b'-' | b'+') => {
                let mut j = num(i + 1, true)?;
                if b.get(j) == Some(&b':') {
                    j = num(j + 1, false)?;
                }
                (j == b.len()).then_some(())
            }
            _ => None,
        }
    };
    step().is_some()
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
        None | Some(Value::Null) => Ok(None),
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
