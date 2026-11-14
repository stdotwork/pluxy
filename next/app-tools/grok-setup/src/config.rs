//! Grok Build's user config (`config.toml`): which files it takes as its own, what Apply writes
//! into one, what a preview reads back, and the merge Clear makes. Keys are Grok Build's own, as
//! its CLI reads them (README.md). The file is edited in place with toml_edit, so comments,
//! formatting and everything Apply does not set stay as the person left them.

use pluxy_program::Error;
use serde_json::{json, Map, Value as Json};
use toml_edit::{DocumentMut, Item, Table, TableLike, Value};

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

fn refuse(why: impl std::fmt::Display) -> Error {
    Error::invalid_request(format!("this is not a Grok Build config: {why}"))
}

/// The entry name Apply writes for model.
pub fn alias(model: &str) -> String {
    format!("{PREFIX}{model}")
}

/// A table at key of t, if there is one: refused when key holds anything but a table.
fn table_at<'a>(t: &'a Table, key: &str, name: &str) -> Result<Option<&'a Table>, Error> {
    match t.get(key) {
        None => Ok(None),
        Some(Item::Table(x)) => Ok(Some(x)),
        Some(_) => Err(refuse(format!("{name} is not a table"))),
    }
}

/// The config in bytes (none, or only white space: an empty file), refused unless Grok Build
/// would take it as its own and Apply can edit it: TOML, with `models` and `model` tables where
/// present, `models.default` a string, and every `model."pluxy/…"` entry a table.
pub fn parse(bytes: Option<&[u8]>) -> Result<DocumentMut, Error> {
    let Some(bytes) = bytes.filter(|b| !b.iter().all(u8::is_ascii_whitespace)) else {
        return Ok(DocumentMut::new());
    };
    let text = std::str::from_utf8(bytes).map_err(|_| refuse("the file is not UTF-8 text"))?;
    let doc: DocumentMut = text
        .parse()
        .map_err(|e| Error::invalid_request(format!("the file is not TOML: {e}")))?;
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

/// Sets key of t to v, keeping the key's place and the old value's comments and spacing.
fn set(t: &mut Table, key: &str, v: impl Into<Value>) {
    let mut v = v.into();
    match t.get_mut(key) {
        Some(Item::Value(old)) => {
            *v.decor_mut() = old.decor().clone();
            *old = v;
        }
        Some(other) => *other = Item::Value(v),
        None => {
            t.insert(key, Item::Value(v));
        }
    }
}

/// The table at key of t, made (implicit: no header of its own while it holds only tables) if
/// there is none. parse checked it is a table where present.
fn table_mut<'a>(t: &'a mut Table, key: &str, implicit: bool) -> &'a mut Table {
    if !t.contains_key(key) {
        let mut n = Table::new();
        n.set_implicit(implicit);
        t.insert(key, Item::Table(n));
    }
    t[key].as_table_mut().expect("parse checked the table")
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

/// An item as plain data, for comparing values whatever their formatting: None when absent.
pub fn plain(item: Option<&Item>) -> Option<Json> {
    match item? {
        Item::None => None,
        Item::Value(v) => Some(plain_value(v)),
        Item::Table(t) => Some(plain_table(t)),
        Item::ArrayOfTables(a) => Some(Json::Array(a.iter().map(|t| plain_table(t)).collect())),
    }
}

fn plain_table(t: &dyn TableLike) -> Json {
    let mut m = Map::new();
    for (k, i) in t.iter() {
        if let Some(v) = plain(Some(i)) {
            m.insert(k.to_string(), v);
        }
    }
    Json::Object(m)
}

fn plain_value(v: &Value) -> Json {
    match v {
        Value::String(s) => json!(s.value()),
        Value::Integer(i) => json!(i.value()),
        Value::Float(f) => json!({ "float": f.value().to_string() }),
        Value::Boolean(b) => json!(b.value()),
        Value::Datetime(d) => json!({ "datetime": d.value().to_string() }),
        Value::Array(a) => Json::Array(a.iter().map(plain_value).collect()),
        Value::InlineTable(t) => plain_table(t),
    }
}

/// What a preview shows of the config in bytes, never the key: whether it points Grok Build at
/// the core (at base_url, when given), the default model, pluxy's model entries and how many
/// entries an older app left.
pub fn read_back(bytes: Option<&[u8]>, base_url: Option<&str>) -> Result<Json, Error> {
    let doc = parse(bytes)?;
    let root = doc.as_table();
    let s = |t: &Table, k: &str| t.get(k).and_then(Item::as_str).map(str::to_string);
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
    let chosen = default
        .as_deref()
        .and_then(|d| ours.iter().find(|(k, _)| *k == d))
        .map(|(_, t)| *t);
    let pointed = chosen.and_then(|t| s(t, "base_url"));
    let key_set = chosen
        .and_then(|t| s(t, "api_key"))
        .is_some_and(|k| !k.is_empty());
    let want = base_url.map(base_url_of);
    let state = if ours.is_empty() {
        "not-configured"
    } else if key_set
        && pointed.is_some()
        && want.as_ref().is_none_or(|w| Some(w) == pointed.as_ref())
        && ours.iter().all(|(_, t)| s(t, "base_url") == pointed)
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
        "model": chosen.and_then(|t| s(t, "model")),
        "models": ours.iter().map(|(_, t)| json!({
            "model": s(t, "model"),
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

/// Three-way merge of TOML tables into current, edited in place so its comments and formatting
/// stay: where current holds what applied did, the person left it alone and original's comes
/// back (or goes, if original had none); tables merge key by key; anything else the person
/// changed stays as they left it. Keys coming back from original go at the table's end.
fn merge_table(o: Option<&dyn TableLike>, a: Option<&dyn TableLike>, c: &mut dyn TableLike) {
    let mut keys: Vec<String> = Vec::new();
    for k in c
        .iter()
        .map(|(k, _)| k.to_string())
        .chain(
            o.into_iter()
                .flat_map(|o| o.iter().map(|(k, _)| k.to_string())),
        )
        .chain(
            a.into_iter()
                .flat_map(|a| a.iter().map(|(k, _)| k.to_string())),
        )
    {
        if !keys.contains(&k) {
            keys.push(k);
        }
    }
    for k in keys {
        let (ov, av) = (o.and_then(|o| o.get(&k)), a.and_then(|a| a.get(&k)));
        let untouched = plain(c.get(&k)) == plain(av);
        let tables = c.get(&k).is_some_and(|i| i.as_table_like().is_some())
            && av.is_some_and(|i| i.as_table_like().is_some());
        if untouched && ov.is_none() {
            c.remove(&k);
        } else if tables && (!untouched || ov.is_some_and(|i| i.as_table_like().is_some())) {
            let child = c
                .get_mut(&k)
                .and_then(Item::as_table_like_mut)
                .expect("a table");
            merge_table(
                ov.and_then(Item::as_table_like),
                av.and_then(Item::as_table_like),
                child,
            );
            if child.is_empty() && ov.is_none() {
                c.remove(&k);
            }
        } else if untouched {
            let ov = ov.expect("checked above").clone();
            match c.get_mut(&k) {
                Some(slot) => *slot = ov,
                None => {
                    c.insert(&k, ov);
                }
            }
        }
    }
}

/// The config as current would be without what applied changed from original ([`merge_table`]).
pub fn merge(
    original: Option<&[u8]>,
    applied: Option<&[u8]>,
    current: &[u8],
) -> Result<Option<Vec<u8>>, Error> {
    let (o, a) = (parse(original)?, parse(applied)?);
    let mut c = parse(Some(current))?;
    merge_table(
        original.map(|_| o.as_table() as &dyn TableLike),
        applied.map(|_| a.as_table() as &dyn TableLike),
        c.as_table_mut(),
    );
    // A file Apply made, holding nothing of the person's, goes again.
    let text = c.to_string();
    if original.is_none() && text.trim().is_empty() {
        return Ok(None);
    }
    Ok(Some(text.into_bytes()))
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

/// A config file's settings for a backup's diff: each table's keys, down to values that are not
/// tables (an array is one setting), and an empty table as one setting. None when it is not TOML.
pub fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Json)>> {
    let doc = parse(Some(bytes)).ok()?;
    let mut out = Vec::new();
    walk(&mut Vec::new(), doc.as_table(), &mut out);
    Some(out)
}

fn walk(path: &mut Vec<String>, t: &dyn TableLike, out: &mut Vec<(Vec<String>, Json)>) {
    for (k, i) in t.iter() {
        path.push(k.to_string());
        match i.as_table_like() {
            Some(sub) if !sub.is_empty() => walk(path, sub, out),
            _ => {
                if let Some(v) = plain(Some(i)) {
                    out.push((path.clone(), v));
                }
            }
        }
        path.pop();
    }
}
