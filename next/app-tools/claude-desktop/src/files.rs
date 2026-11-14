//! Claude Desktop's third-party files: which ones it takes as its own, what Apply writes into
//! each, what a preview reads back and how Clear takes pluxy's values out again. Keys and values
//! are Claude Desktop's own, as its documentation and its Linux package name them (README.md):
//!
//! - `claude_desktop_config.json` in its third-party folder (`~/.config/Claude-3p`): the person's
//!   sign-in choice, `deploymentMode`;
//! - `configLibrary/<id>.json`: a saved configuration (here pluxy's gateway profile), flat keys;
//! - `configLibrary/_meta.json`: the library's index, `{"appliedId", "entries": [{"id", "name"}]}`,
//!   with an optional `hybridPointer` that wins over `appliedId`.

use pluxy_program::Error;
use pluxy_transactions::{merge_value, Json, Merge};
use serde_json::{json, Map, Value};

/// pluxy's configuration in Claude Desktop's library: the old desktop app's ID, so a person moving
/// from it updates the same entry.
pub const PROFILE_ID: &str = "00000000-0000-4000-8000-000000831700";
/// Its name in Claude Desktop's configuration list.
const PROFILE_NAME: &str = "pluxy";
/// The most model rows one apply takes.
pub const MODELS_MAX: usize = 16;
/// The longest model text the old app took.
const MODEL_MAX: usize = 240;

/// The key slot Claude Desktop seals in place (with the device's keystore) at its next launch.
const KEY: &str = "inferenceGatewayApiKey";

/// Model families Claude Desktop takes for an Anthropic model, and the other families whose
/// names it refuses, as Claude Desktop 5.11492.5 (Linux) checks a gateway model's name: it drops
/// an `inferenceModels` entry that fails, saying it "is not an Anthropic model".
const FAMILIES: [&str; 5] = ["sonnet", "opus", "haiku", "fable", "mythos"];
const OTHER_FAMILIES: &[&str] = &[
    "ark-code",
    "astron",
    "command-r",
    "deepseek",
    "doubao",
    "gemini",
    "gemma",
    "glm",
    "gpt",
    "grok",
    "hermes",
    "hy3",
    "kimi",
    "lfm",
    "llama",
    "longcat",
    "mimo",
    "minimax",
    "mistral",
    "mixtral",
    "moonshot",
    "nemotron",
    "openai",
    "phi-",
    "qianfan",
    "qwen",
    "tc-code",
    "yi-",
    "stepfun",
    "step-3",
    "seed-",
    "bytedance",
    "hunyuan",
    "granite",
    "amazon.nova",
    "nova-",
    "devstral",
    "ministral",
    "ernie",
    "codex",
    "arcee",
    "trinity",
    "abab",
    "jamba",
    "arctic",
    "solar",
    "mercury",
    "zamba",
    "kat-coder",
    "dpsk",
];
/// Its whole-word patterns: `ling`, `unic`, `k2.`, `m2.`, `ds-` after a word boundary.
const OTHER_WORDS: &[&str] = &["ling", "unic"];
const OTHER_WORD_PREFIXES: &[&str] = &["k2.", "m2.", "ds-"];

fn word_char(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_'
}

/// Whether name, in lower case, names another model family the way Claude Desktop's pattern does.
fn other_family(name: &str) -> bool {
    if OTHER_FAMILIES.iter().any(|f| name.contains(f)) {
        return true;
    }
    let b = name.as_bytes();
    // `phi` followed by a digit.
    if name
        .match_indices("phi")
        .any(|(i, _)| b.get(i + 3).is_some_and(u8::is_ascii_digit))
    {
        return true;
    }
    let starts_word = |i: usize| i == 0 || !word_char(b[i - 1]);
    let ends_word = |i: usize| i >= b.len() || !word_char(b[i]);
    OTHER_WORDS.iter().any(|w| {
        name.match_indices(w)
            .any(|(i, _)| starts_word(i) && ends_word(i + w.len()))
    }) || OTHER_WORD_PREFIXES
        .iter()
        .any(|w| name.match_indices(w).any(|(i, _)| starts_word(i)))
}

/// Whether Claude Desktop keeps name as a gateway model: it looks like an Anthropic model (a
/// family name, `claude` or `anthropic` in it) and names no other family.
pub fn anthropic_name(name: &str) -> bool {
    let n = name.to_ascii_lowercase();
    if other_family(&n) {
        return false;
    }
    let bare_family = FAMILIES.iter().any(|f| {
        n.strip_prefix(f).is_some_and(|rest| {
            rest.is_empty()
                || rest.strip_prefix('-').is_some_and(|v| {
                    !v.is_empty() && v.bytes().all(|c| c.is_ascii_digit() || c == b'.')
                })
        })
    });
    bare_family
        || ["claude", "anthropic"]
            .iter()
            .chain(FAMILIES.iter())
            .any(|f| n.contains(f))
}

/// The family tier Claude Desktop groups a model under, from its name (the old app's rule).
fn family_tier(name: &str) -> Option<&'static str> {
    let n = name.to_ascii_lowercase();
    FAMILIES.into_iter().find(|f| {
        n == format!("claude-{f}")
            || n.starts_with(&format!("claude-{f}-"))
            || n.contains(&format!("-{f}-"))
    })
}

/// One model row: the model the core serves, the name Claude Desktop uses for it (empty: the
/// model's own) and whether its 1M-context variant is offered.
#[derive(Clone, Debug, PartialEq)]
pub struct Row {
    pub model: String,
    pub alias: String,
    pub one_m: bool,
}

impl Row {
    /// The name Claude Desktop sends to the gateway.
    fn name(&self) -> &str {
        if self.alias.is_empty() {
            &self.model
        } else {
            &self.alias
        }
    }
}

/// What Apply writes: the core's address, the client key and the model rows.
#[derive(Debug)]
pub struct Settings {
    pub base_url: String,
    pub key: String,
    pub rows: Vec<Row>,
}

fn model_text(v: &Value, what: &str, may_be_empty: bool) -> Result<String, Error> {
    let m = v
        .as_str()
        .ok_or_else(|| Error::invalid_request(format!("{what} is a string")))?
        .trim();
    if (m.is_empty() && !may_be_empty)
        || m.len() > MODEL_MAX
        || m.chars().any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(Error::invalid_request(format!(
            "{what} is a model ID of at most {MODEL_MAX} characters, with no spaces"
        )));
    }
    Ok(m.to_string())
}

impl Settings {
    /// The settings an apply's input names, with key, the secret its `key` field delivered.
    pub fn from_input(input: &Value, key: &str) -> Result<Settings, Error> {
        let base_url = input["base_url"].as_str().unwrap_or_default();
        let url_ok = (base_url.starts_with("http://") || base_url.starts_with("https://"))
            && base_url.len() <= 2048
            && !base_url
                .chars()
                .any(|c| c.is_whitespace() || c.is_control());
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
        let list = input["models"]
            .as_array()
            .filter(|l| (1..=MODELS_MAX).contains(&l.len()))
            .ok_or_else(|| {
                Error::invalid_request(format!("models lists 1 to {MODELS_MAX} models"))
            })?;
        let mut rows: Vec<Row> = Vec::new();
        for (i, r) in list.iter().enumerate() {
            let n = i + 1;
            let model = model_text(&r["model"], &format!("model {n}"), false)?;
            let mut alias = model_text(&r["alias"], &format!("the alias of model {n}"), true)?;
            if alias.eq_ignore_ascii_case(&model) {
                alias.clear();
            }
            let row = Row {
                model,
                alias,
                one_m: r["one_m"].as_bool().unwrap_or(false),
            };
            if !anthropic_name(row.name()) {
                return Err(Error::invalid_request(format!(
                    "Claude Desktop takes only Claude model names: give model {n} ({}) an alias the core serves it as, such as claude-sonnet-5-mine, with no other model family's name in it",
                    row.name()
                )));
            }
            if rows
                .iter()
                .any(|o| o.name().eq_ignore_ascii_case(row.name()))
            {
                return Err(Error::invalid_request(format!(
                    "{} is listed twice: each row needs its own model ID",
                    row.name()
                )));
            }
            rows.push(row);
        }
        Ok(Settings {
            base_url: base_url.to_string(),
            key: key.to_string(),
            rows,
        })
    }

    /// The rows as Claude Desktop's `inferenceModels`: the first is its default model, the first
    /// of each tier that tier's default.
    fn models(&self) -> Value {
        let mut tiers: Vec<&str> = Vec::new();
        let list = self.rows.iter().map(|r| {
            let mut e = Map::new();
            e.insert("name".into(), json!(r.name()));
            if !r.alias.is_empty() {
                e.insert("labelOverride".into(), json!(r.model));
            }
            if let Some(t) = family_tier(r.name()) {
                e.insert("anthropicFamilyTier".into(), json!(t));
                e.insert("isFamilyDefault".into(), json!(!tiers.contains(&t)));
                tiers.push(t);
            }
            if r.one_m {
                e.insert("supports1m".into(), json!(true));
                e.insert("prefer1m".into(), json!(true));
            }
            Value::Object(e)
        });
        Value::Array(list.collect())
    }
}

/// Which of the three files: their order in the set.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum File {
    Deployment,
    Profile,
    Meta,
}

impl File {
    pub const ALL: [File; 3] = [File::Deployment, File::Profile, File::Meta];

    fn label(self) -> &'static str {
        match self {
            File::Deployment => "claude_desktop_config.json",
            File::Profile => "the gateway profile",
            File::Meta => "configLibrary/_meta.json",
        }
    }
}

/// The file in bytes (none, or only white space: an empty one), refused unless Claude Desktop
/// would take it as its own: strict JSON, an object, and for the profile and the index the
/// shapes Claude Desktop reads.
pub fn parse(file: File, bytes: Option<&[u8]>) -> Result<Map<String, Value>, Error> {
    let Some(bytes) = bytes.filter(|b| !b.iter().all(u8::is_ascii_whitespace)) else {
        return Ok(Map::new());
    };
    let not = |why: &str| {
        Error::invalid_request(format!(
            "this is not Claude Desktop's {}: {why}",
            file.label()
        ))
    };
    let Value::Object(root) = Json::parse(bytes).map_err(|e| not(&e.to_string()))? else {
        return Err(not("the file is not a JSON object"));
    };
    match file {
        File::Deployment => {}
        File::Profile => {
            if root.get("inferenceModels").is_some_and(|m| !m.is_array()) {
                return Err(not("inferenceModels is not a list"));
            }
        }
        File::Meta => {
            let entries_ok = root
                .get("entries")
                .is_none_or(|e| e.as_array().is_some_and(|l| l.iter().all(Value::is_object)));
            if !entries_ok {
                return Err(not("entries is not a list of configurations"));
            }
            if root.get("appliedId").is_some_and(|a| !a.is_string()) {
                return Err(not("appliedId is not a configuration ID"));
            }
        }
    }
    Ok(root)
}

/// The file as Apply leaves it: before (the file now, None if there is none) with Claude Desktop
/// pointed at the core through s. Everything else in it stays.
pub fn apply(file: File, before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let mut root = parse(file, before)?;
    match file {
        // The person's sign-in choice, as Claude Desktop's own chooser writes it.
        File::Deployment => {
            root.insert("deploymentMode".into(), json!("3p"));
        }
        File::Profile => {
            for (k, v) in [
                ("inferenceProvider", json!("gateway")),
                ("inferenceGatewayBaseUrl", json!(s.base_url)),
                (KEY, json!(s.key)),
                ("inferenceGatewayAuthScheme", json!("bearer")),
                // Only the key above, no helper or sign-in that would win over it.
                ("inferenceCredentialKind", json!("static")),
                // The model IDs below are full ones: no discovery, no bootstrap overlay.
                ("modelDiscoveryEnabled", json!(false)),
                ("bootstrapEnabled", json!(false)),
                ("disableDeploymentModeChooser", json!(true)),
                ("inferenceModels", s.models()),
            ] {
                root.insert(k.into(), v);
            }
            // A session host would take inference away from the gateway.
            root.shift_remove("selfHostedUrl");
        }
        File::Meta => {
            // The hybrid pointer wins over appliedId; Claude Desktop's own "apply locally" drops
            // it too, and Clear and Restore Backup bring it back.
            root.shift_remove("hybridPointer");
            root.insert("appliedId".into(), json!(PROFILE_ID));
            let entries = root
                .entry("entries")
                .or_insert_with(|| json!([]))
                .as_array_mut()
                .expect("parse checked entries");
            match entries.iter_mut().find(|e| e["id"] == PROFILE_ID) {
                Some(e) => e["name"] = json!(PROFILE_NAME),
                None => entries.push(json!({"id": PROFILE_ID, "name": PROFILE_NAME})),
            }
        }
    }
    Json::render(&Value::Object(root))
}

/// What a preview shows of the three files, never the key: whether they point Claude Desktop at
/// a core (at base_url, when given), and the rows the form shows.
pub fn read_back(
    deployment: Option<&[u8]>,
    profile: Option<&[u8]>,
    meta: Option<&[u8]>,
    base_url: Option<&str>,
) -> Result<Value, Error> {
    let d = parse(File::Deployment, deployment)?;
    let p = parse(File::Profile, profile)?;
    let m = parse(File::Meta, meta)?;
    let text = |k: &str| p.get(k).and_then(Value::as_str);
    let key_set = text(KEY).is_some_and(|k| !k.is_empty());
    let rows: Vec<Value> = p
        .get("inferenceModels")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|e| {
            // An entry may be a bare model ID.
            let (name, label) = match e {
                Value::String(n) => (n.as_str(), None),
                Value::Object(o) => (
                    o.get("name")?.as_str()?,
                    o.get("labelOverride").and_then(Value::as_str),
                ),
                _ => return None,
            };
            let one_m = e["supports1m"] == true;
            Some(match label {
                Some(l) if l != name => json!({"model": l, "alias": name, "one_m": one_m}),
                _ => json!({"model": name, "alias": "", "one_m": one_m}),
            })
        })
        .collect();
    let applied = m.get("appliedId").and_then(Value::as_str) == Some(PROFILE_ID)
        && m.get("hybridPointer").is_none();
    let state = match text("inferenceGatewayBaseUrl") {
        None => "not-configured",
        Some(b)
            if applied
                && key_set
                && text("inferenceProvider") == Some("gateway")
                && text("inferenceCredentialKind").is_none_or(|k| k == "static")
                && text("inferenceGatewayAuthScheme").is_none_or(|k| k == "bearer")
                && d.get("deploymentMode").and_then(Value::as_str) == Some("3p")
                && !rows.is_empty()
                && base_url.is_none_or(|want| want == b) =>
        {
            "configured"
        }
        Some(_) => "needs-update",
    };
    Ok(json!({
        "state": state,
        "base_url": text("inferenceGatewayBaseUrl"),
        "key_set": key_set,
        "deployment_mode": d.get("deploymentMode").and_then(Value::as_str),
        "applied_id": m.get("appliedId").and_then(Value::as_str),
        "hybrid_pointer": m.contains_key("hybridPointer"),
        "models": rows,
    }))
}

/// The JSON merge for Clear (and for keeping the person's edits in the original on a new apply),
/// refusing a file Claude Desktop would not take first, with Claude Desktop's own rewrites left
/// out of the person's edits: the key slot, which it seals in place, stays pluxy's while applied,
/// and pluxy's entry leaves the library's index unless the original had it.
pub struct Checked;

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
        let parse = |b: Option<&[u8]>| b.map(Json::parse).transpose();
        let (o, a) = (parse(original)?, parse(applied)?);
        let mut c = Json::parse(cur)?;
        // Which file this is, from what pluxy wrote into it.
        let ours = |k: &str, v: &Value| a.as_ref().is_some_and(|a| a.get(k) == Some(v));
        let file = if a.as_ref().is_some_and(|a| a.get(KEY).is_some()) {
            File::Profile
        } else if ours("appliedId", &json!(PROFILE_ID)) {
            File::Meta
        } else {
            File::Deployment
        };
        parse_value(file, &c)?;
        if file == File::Profile {
            if let (Some(c), Some(k)) = (c.as_object_mut(), a.as_ref().and_then(|a| a.get(KEY))) {
                if c.contains_key(KEY) {
                    c.insert(KEY.into(), k.clone());
                }
            }
        }
        let mut merged = merge_value(o.as_ref(), a.as_ref(), Some(&c));
        if file == File::Meta {
            let had = o.as_ref().is_some_and(has_entry);
            if let Some(entries) = merged
                .as_mut()
                .and_then(|m| m.get_mut("entries"))
                .and_then(Value::as_array_mut)
            {
                if !had {
                    entries.retain(|e| e["id"] != PROFILE_ID);
                }
            }
        }
        merged.map(|v| Json::render(&v)).transpose()
    }
}

fn has_entry(meta: &Value) -> bool {
    meta["entries"]
        .as_array()
        .is_some_and(|l| l.iter().any(|e| e["id"] == PROFILE_ID))
}

fn parse_value(file: File, v: &Value) -> Result<(), Error> {
    let bytes = serde_json::to_vec(v).map_err(|e| Error::internal(e.to_string()))?;
    parse(file, Some(&bytes)).map(|_| ())
}
