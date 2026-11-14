//! OpenCode's config (`opencode.json` or `opencode.jsonc`): which files it takes as its own, what
//! Apply writes into one, what a preview reads back, and the merge Clear uses. Keys are OpenCode's
//! own (README.md). Every write edits the file's syntax tree in place, so comments, formatting and
//! keys pluxy does not write stay where the person left them.

use jsonc_parser::cst::{CstInputValue, CstObject, CstRootNode};
use jsonc_parser::ParseOptions;
use pluxy_program::Error;
use pluxy_transactions::{json_leaves, merge_value, Merge};
use serde_json::{json, Map, Value};

/// The schema OpenCode's config may name in `$schema`, and the one Apply adds when it names none.
pub const SCHEMA: &str = "https://opencode.ai/config.json";
/// The provider Apply writes, under `provider`.
pub const PROVIDER: &str = "pluxy";
/// The provider the old desktop app wrote: left alone, shown so the person can remove it.
pub const OLD_PROVIDER: &str = "cpa-gui";
/// The AI SDK package OpenCode loads for an OpenAI-compatible provider.
const NPM: &str = "@ai-sdk/openai-compatible";
/// The longest model ID or name taken (the old app's pickers' custom values).
const TEXT_MAX: usize = 240;
/// The most models one apply writes.
pub const MODELS_MAX: usize = 256;

/// OpenCode's parser rules: JSON with comments and trailing commas (jsonc-parser with
/// `allowTrailingComma`), nothing looser.
fn rules() -> ParseOptions {
    ParseOptions {
        allow_comments: true,
        allow_loose_object_property_names: false,
        allow_trailing_commas: true,
        allow_missing_commas: false,
        allow_single_quoted_strings: false,
        allow_hexadecimal_numbers: false,
        allow_unary_plus_numbers: false,
        allow_bare_decimal_point_numbers: false,
        allow_non_finite_numbers: false,
        allow_extended_string_escapes: false,
    }
}

fn not_opencode(why: impl std::fmt::Display) -> Error {
    Error::invalid_request(format!("this is not an OpenCode config: {why}"))
}

/// A model Apply lists under the provider: its ID, the name OpenCode shows, and whether it takes
/// attachments (images and PDFs).
#[derive(Clone, Debug, PartialEq)]
pub struct Model {
    pub id: String,
    pub name: String,
    pub attachment: bool,
}

/// What Apply writes: the core's address, the client key, the models and the default one.
#[derive(Debug)]
pub struct Settings {
    pub base_url: String,
    pub key: String,
    pub model: String,
    pub models: Vec<Model>,
}

fn text<'a>(v: &'a Value, field: &str) -> Result<&'a str, Error> {
    v[field]
        .as_str()
        .ok_or_else(|| Error::invalid_request(format!("{field} is a string")))
}

/// A model ID: 1 to 240 characters, no control characters and no `#` (OpenCode's variant mark).
fn model_id(v: &Value, field: &str) -> Result<String, Error> {
    let m = text(v, field)?.trim();
    if m.is_empty() || m.chars().count() > TEXT_MAX || m.chars().any(|c| c.is_control() || c == '#')
    {
        return Err(Error::invalid_request(format!(
            "{field} is a model ID of 1 to {TEXT_MAX} characters, with no control characters or #"
        )));
    }
    Ok(m.to_string())
}

impl Settings {
    /// The settings an apply's input names, with key, the secret its `key` field delivered.
    pub fn from_input(input: &Value, key: &str) -> Result<Settings, Error> {
        let base_url = text(input, "base_url")?;
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
        let mut models: Vec<Model> = Vec::new();
        for m in list {
            let id = model_id(m, "id")?;
            if models.iter().any(|x| x.id == id) {
                return Err(Error::invalid_request(format!("models lists {id} twice")));
            }
            let name = m["name"].as_str().unwrap_or_default().trim();
            if name.chars().count() > TEXT_MAX || name.chars().any(char::is_control) {
                return Err(Error::invalid_request(format!(
                    "a model's name is at most {TEXT_MAX} characters, with no control characters"
                )));
            }
            models.push(Model {
                name: if name.is_empty() {
                    id.clone()
                } else {
                    name.into()
                },
                id,
                attachment: m["attachment"].as_bool().unwrap_or(false),
            });
        }
        let model = model_id(input, "model")?;
        if !models.iter().any(|m| m.id == model) {
            return Err(Error::invalid_request(format!(
                "model {model} is not one of the models listed"
            )));
        }
        Ok(Settings {
            base_url: base_url.to_string(),
            key: key.to_string(),
            model,
            models,
        })
    }

    /// The provider's `baseURL`: the core's OpenAI-compatible root.
    pub fn provider_url(&self) -> String {
        provider_url(&self.base_url)
    }
}

/// The OpenAI-compatible root OpenCode is given for the core at base_url.
pub fn provider_url(base_url: &str) -> String {
    format!("{}/v1", base_url.trim_end_matches('/'))
}

/// A parsed config: its text (blank: an empty object) and syntax tree, and its value.
pub struct Doc {
    root: CstRootNode,
    trailing_newline: bool,
    pub value: Map<String, Value>,
}

/// Refuses an object that names a key twice, at any depth.
fn no_duplicates(o: &CstObject) -> Result<(), Error> {
    let mut seen: Vec<String> = Vec::new();
    for p in o.properties() {
        let name = p.decoded_name().unwrap_or_default();
        if seen.contains(&name) {
            return Err(not_opencode(format!("it names {name:?} twice")));
        }
        if let Some(inner) = p.object_value() {
            no_duplicates(&inner)?;
        }
        for a in p.array_value().iter().flat_map(|a| a.elements()) {
            if let Some(inner) = a.as_object() {
                no_duplicates(&inner)?;
            }
        }
        seen.push(name);
    }
    Ok(())
}

/// The members at path in root, refused unless an object (absent: None).
fn object_at<'a>(
    root: &'a Map<String, Value>,
    path: &[&str],
) -> Result<Option<&'a Map<String, Value>>, Error> {
    let mut at = root;
    for (i, k) in path.iter().enumerate() {
        match at.get(*k) {
            None => return Ok(None),
            Some(Value::Object(o)) => at = o,
            Some(_) => {
                return Err(not_opencode(format!(
                    "{} is not an object",
                    path[..=i].join(".")
                )))
            }
        }
    }
    Ok(Some(at))
}

/// The config in bytes (none, or only white space: an empty one), refused unless OpenCode would
/// take it as its own: JSON or JSONC by OpenCode's parser rules, no key named twice, an object,
/// `$schema`, if any, OpenCode's, and the parts Apply writes of the shapes OpenCode reads.
pub fn parse(bytes: Option<&[u8]>) -> Result<Doc, Error> {
    let blank = bytes.is_none_or(|b| b.iter().all(u8::is_ascii_whitespace));
    let text = if blank {
        "{}"
    } else {
        std::str::from_utf8(bytes.unwrap_or_default())
            .map_err(|_| not_opencode("the file is not UTF-8 text"))?
    };
    let root = CstRootNode::parse(text, &rules())
        .map_err(|e| not_opencode(format!("the file is not JSON or JSONC OpenCode reads: {e}")))?;
    let Some(obj) = root.object_value() else {
        return Err(not_opencode("the file is not a JSON object"));
    };
    no_duplicates(&obj)?;
    let Some(Value::Object(value)) = root.to_serde_value() else {
        return Err(not_opencode("the file is not a JSON object"));
    };
    match value.get("$schema") {
        None => {}
        Some(Value::String(s)) if s == SCHEMA => {}
        Some(other) => {
            return Err(not_opencode(format!(
                "the file names another schema ({other}), not {SCHEMA}"
            )))
        }
    }
    object_at(&value, &["provider", PROVIDER, "options"])?;
    object_at(&value, &["provider", PROVIDER, "models"])?;
    if let Some(models) = object_at(&value, &["provider", PROVIDER, "models"])? {
        for (id, m) in models {
            if !m.is_object() {
                return Err(not_opencode(format!(
                    "provider.{PROVIDER}.models.{id} is not an object"
                )));
            }
        }
    }
    if value.get("model").is_some_and(|m| !m.is_string()) {
        return Err(not_opencode("model is not a string"));
    }
    Ok(Doc {
        root,
        trailing_newline: blank || text.ends_with('\n'),
        value,
    })
}

fn input(v: &Value) -> CstInputValue {
    match v {
        Value::Null => CstInputValue::Null,
        Value::Bool(b) => CstInputValue::Bool(*b),
        Value::Number(n) => CstInputValue::Number(n.to_string()),
        Value::String(s) => CstInputValue::String(s.clone()),
        Value::Array(a) => CstInputValue::Array(a.iter().map(input).collect()),
        Value::Object(m) => {
            CstInputValue::Object(m.iter().map(|(k, v)| (k.clone(), input(v))).collect())
        }
    }
}

/// Changes obj, whose value is now, into want: members want lacks go, members whose value differs
/// are replaced (objects member by member), members only want has are added after the member that
/// comes before them in want. Members left as they are keep their comments and formatting.
fn patch(obj: &CstObject, now: &Map<String, Value>, want: &Map<String, Value>) {
    for prop in obj.properties() {
        let Some(name) = prop.decoded_name() else {
            continue;
        };
        match (want.get(&name), now.get(&name)) {
            (None, _) => prop.remove(),
            (Some(w), Some(n)) if w == n => {}
            (Some(Value::Object(w)), Some(Value::Object(n))) => match prop.object_value() {
                Some(inner) => patch(&inner, n, w),
                None => prop.set_value(input(&Value::Object(w.clone()))),
            },
            (Some(w), _) => prop.set_value(input(w)),
        }
    }
    let keys: Vec<&String> = want.keys().collect();
    for (i, k) in keys.iter().enumerate() {
        if now.contains_key(*k) {
            continue;
        }
        let names: Vec<String> = obj
            .properties()
            .iter()
            .filter_map(|p| p.decoded_name())
            .collect();
        let at = keys[..i]
            .iter()
            .rev()
            .find_map(|p| names.iter().position(|n| n == *p))
            .map_or(0, |j| j + 1);
        obj.insert(at, k, input(&want[*k]));
    }
}

impl Doc {
    /// The document's bytes once its value is want: only what differs is edited.
    pub fn edit(self, want: &Map<String, Value>) -> Vec<u8> {
        let obj = self.root.object_value_or_set();
        patch(&obj, &self.value, want);
        let mut out = self.root.to_string();
        if self.trailing_newline && !out.ends_with('\n') {
            out.push('\n');
        }
        out.into_bytes()
    }
}

/// The config's value once Apply has pointed OpenCode at the core through s: `$schema` first if
/// it names none, `provider.pluxy` (its other members kept) with the package, name, address, key
/// and exactly s's models (each one's other members kept), and `model` the default one.
pub fn applied(now: &Map<String, Value>, s: &Settings) -> Map<String, Value> {
    let mut root = Map::new();
    if !now.contains_key("$schema") {
        root.insert("$schema".into(), json!(SCHEMA));
    }
    root.extend(now.clone());
    let providers = root
        .entry("provider")
        .or_insert_with(|| json!({}))
        .as_object_mut()
        .expect("parse checked provider");
    let old = providers
        .get(PROVIDER)
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    let mut p = old.clone();
    p.insert("npm".into(), json!(NPM));
    p.insert("name".into(), json!(PROVIDER));
    let mut options = old
        .get("options")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    options.insert("baseURL".into(), json!(s.provider_url()));
    options.insert("apiKey".into(), json!(s.key));
    p.insert("options".into(), Value::Object(options));
    let empty = Map::new();
    let old_models = old
        .get("models")
        .and_then(Value::as_object)
        .unwrap_or(&empty);
    let mut models = Map::new();
    for m in &s.models {
        let mut e = old_models
            .get(&m.id)
            .and_then(Value::as_object)
            .cloned()
            .unwrap_or_default();
        e.insert("name".into(), json!(m.name));
        let modalities = if m.attachment {
            e.insert("attachment".into(), json!(true));
            json!({"input": ["text", "image", "pdf"], "output": ["text"]})
        } else {
            e.shift_remove("attachment");
            json!({"input": ["text"], "output": ["text"]})
        };
        e.insert("modalities".into(), modalities);
        models.insert(m.id.clone(), Value::Object(e));
    }
    p.insert("models".into(), Value::Object(models));
    providers.insert(PROVIDER.into(), Value::Object(p));
    root.insert("model".into(), json!(format!("{PROVIDER}/{}", s.model)));
    root
}

/// The config file as Apply leaves it: before (the file now, None if there is none) with
/// OpenCode pointed at the core through s.
pub fn apply(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let doc = parse(before)?;
    let want = applied(&doc.value, s);
    Ok(doc.edit(&want))
}

/// What a preview shows of the config in bytes, never the key: whether it points OpenCode at a
/// core (at base_url, when given), the models it lists and the default one.
pub fn read_back(bytes: Option<&[u8]>, base_url: Option<&str>) -> Result<Value, Error> {
    let root = parse(bytes)?.value;
    let p = object_at(&root, &["provider", PROVIDER])?;
    let empty = Map::new();
    let options = object_at(&root, &["provider", PROVIDER, "options"])?.unwrap_or(&empty);
    let url = options.get("baseURL").and_then(Value::as_str);
    let key_set = options
        .get("apiKey")
        .and_then(Value::as_str)
        .is_some_and(|k| !k.is_empty());
    let models: Vec<Value> = object_at(&root, &["provider", PROVIDER, "models"])?
        .unwrap_or(&empty)
        .iter()
        .map(|(id, m)| {
            json!({
                "id": id,
                "name": m["name"].as_str().unwrap_or(id),
                "attachment": m["attachment"] == true,
            })
        })
        .collect();
    let default = root.get("model").and_then(Value::as_str);
    let ours = default.and_then(|m| m.strip_prefix(&format!("{PROVIDER}/")));
    let state = match p {
        None => "not-configured",
        Some(_)
            if key_set
                && ours.is_some_and(|m| models.iter().any(|x| x["id"] == m))
                && url.is_some()
                && base_url.is_none_or(|b| url == Some(provider_url(b).as_str())) =>
        {
            "configured"
        }
        Some(_) => "needs-update",
    };
    Ok(json!({
        "state": state,
        "base_url": url,
        "key_set": key_set,
        "model": ours,
        "default_model": default,
        "models": models,
        "old_provider": object_at(&root, &["provider", OLD_PROVIDER])?.is_some(),
    }))
}

/// A config's settings for the backup browser's diff: as `json_leaves` reads JSON; None when the
/// bytes are not a config this tool reads.
pub fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Value)>> {
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Some(Vec::new());
    }
    let value = parse(Some(bytes)).ok()?.value;
    json_leaves(&serde_json::to_vec(&Value::Object(value)).ok()?)
}

/// The three-way merge for Clear (and for a later Apply's original), on values, written into the
/// current document in place; refusing a config OpenCode would not take as its own first.
pub struct Checked;

impl Merge for Checked {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        let doc = current.map(|c| parse(Some(c))).transpose()?;
        if current == applied {
            return Ok(original.map(<[u8]>::to_vec));
        }
        let Some(doc) = doc else {
            return Ok(None);
        };
        let value = |b: Option<&[u8]>| -> Result<Option<Value>, Error> {
            b.map(|b| parse(Some(b)).map(|d| Value::Object(d.value)))
                .transpose()
        };
        let (o, a) = (value(original)?, value(applied)?);
        let c = Value::Object(doc.value.clone());
        match merge_value(o.as_ref(), a.as_ref(), Some(&c)) {
            None => Ok(None),
            Some(Value::Object(want)) => Ok(Some(doc.edit(&want))),
            Some(_) => Err(Error::internal("the merged config is not an object")),
        }
    }
}
