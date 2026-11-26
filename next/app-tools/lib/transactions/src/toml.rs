//! TOML config files, edited in place (feature `toml`). A writer tool keeps what it knows about
//! its own file (which keys, which provider block, what it refuses); this is the machinery all of
//! them need: reading a file as a document that keeps its comments and formatting, setting a key
//! where it stands, and the three-way merge Clear makes into the file as it is now.
//!
//! A tool's refusals start with its own text (its `refusal` prefix, such as `"these are not
//! Codex settings: "`), so what the person reads names the tool they used.

pub use toml_edit;

use pluxy_program::Error;
use serde_json::{json, Map, Value as Json};
use toml_edit::{DocumentMut, Item, Table, TableLike, Value};

/// A tool's check of a config file's bytes (None: no file): the document, or the refusal.
pub type Parse = fn(Option<&[u8]>) -> Result<DocumentMut, Error>;

/// A refusal of the file: `refusal` (the tool's own opening) then why.
pub fn refuse(refusal: &str, why: impl std::fmt::Display) -> Error {
    Error::invalid_request(format!("{refusal}{why}"))
}

/// The bytes read as a TOML document: none, or only white space, is an empty document; anything
/// else must be UTF-8 text holding TOML. A tool's own checks of what the file holds come after.
pub fn read(bytes: Option<&[u8]>, refusal: &str) -> Result<DocumentMut, Error> {
    let Some(bytes) = bytes.filter(|b| !b.iter().all(u8::is_ascii_whitespace)) else {
        return Ok(DocumentMut::new());
    };
    let text =
        std::str::from_utf8(bytes).map_err(|_| refuse(refusal, "the file is not UTF-8 text"))?;
    text.parse()
        .map_err(|e| Error::invalid_request(format!("the file is not TOML: {e}")))
}

/// A table at key of t, if there is one: refused when key holds anything but a table.
pub fn table_at<'a>(
    t: &'a Table,
    key: &str,
    name: &str,
    refusal: &str,
) -> Result<Option<&'a Table>, Error> {
    match t.get(key) {
        None => Ok(None),
        Some(Item::Table(x)) => Ok(Some(x)),
        Some(_) => Err(refuse(refusal, format!("{name} is not a table"))),
    }
}

/// Sets key of t to v, keeping the key's place and the old value's comments and spacing.
pub fn set(t: &mut Table, key: &str, v: impl Into<Value>) {
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
/// there is none. The tool's parse checked it is a table where present.
pub fn table_mut<'a>(t: &'a mut Table, key: &str, implicit: bool) -> &'a mut Table {
    if !t.contains_key(key) {
        let mut n = Table::new();
        n.set_implicit(implicit);
        t.insert(key, Item::Table(n));
    }
    t[key].as_table_mut().expect("parse checked the table")
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

/// The config as current would be without what applied changed from original ([`merge_table`]);
/// each file is read through the tool's parse, so a file the tool would not take is refused.
pub fn merge(
    parse: Parse,
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

/// A config file's settings for a backup's diff: each table's keys, down to values that are not
/// tables (an array is one setting), and an empty table as one setting. None when the tool's
/// parse refuses the file.
pub fn leaves(parse: Parse, bytes: &[u8]) -> Option<Vec<(Vec<String>, Json)>> {
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
