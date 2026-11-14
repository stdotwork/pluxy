//! Hermes Agent's config.yaml, read and edited in place (copied from pluxy/omp-setup's yaml.rs).
//!
//! A file is read with saphyr (YAML 1.2) into a JSON value. Hermes reads it with YAML 1.1 rules,
//! which differ only in how some plain scalars resolve (yes/no/on/off, numbers with `_` or `:`,
//! dates); the values pluxy writes are checked against those rules (hermes.rs), and the person's
//! own values are kept as written.
//! That value is what the writer reasons about, merges and diffs. Edits go through yaml-edit's
//! lossless tree, so comments, key order, quoting and indentation the person chose stay; only the
//! keys whose values change are touched. Every edited text is read again and must hold exactly the
//! intended value; otherwise nothing is written.

use pluxy_program::Error;
use pluxy_transactions::{json_leaves, merge_value, Merge};
use saphyr::{LoadableYamlNode, Scalar, Yaml};
use serde_json::{Map, Number, Value};
use std::str::FromStr;
use yaml_edit::{Document, Mapping, MappingBuilder, Sequence, SequenceBuilder, YamlFile};

/// The value of a file's bytes (None: no file). No file, an empty one, one holding only comments
/// and one whose document is `null` are an empty mapping; anything else must be one mapping.
pub fn parse(bytes: Option<&[u8]>) -> Result<Value, Error> {
    let Some(bytes) = bytes else {
        return Ok(Value::Object(Map::new()));
    };
    let text = std::str::from_utf8(bytes)
        .map_err(|_| Error::invalid_request("the file is not UTF-8 text"))?;
    let docs = Yaml::load_from_str(text)
        .map_err(|e| Error::invalid_request(format!("the file is not YAML: {e}")))?;
    match docs.as_slice() {
        [] => Ok(Value::Object(Map::new())),
        [Yaml::Value(Scalar::Null)] => Ok(Value::Object(Map::new())),
        [doc @ Yaml::Mapping(_)] => value(doc),
        [_] => Err(Error::invalid_request(
            "the file's top level is not a mapping of settings",
        )),
        _ => Err(Error::invalid_request(
            "the file holds more than one YAML document",
        )),
    }
}

/// The JSON value of a YAML node: scalars as JSON scalars, mappings with their keys as text.
fn value(y: &Yaml) -> Result<Value, Error> {
    Ok(match y {
        Yaml::Value(s) => match s {
            Scalar::Null => Value::Null,
            Scalar::Boolean(b) => Value::Bool(*b),
            Scalar::Integer(i) => Value::Number((*i).into()),
            Scalar::FloatingPoint(f) => Value::Number(Number::from_f64(f.0).ok_or_else(|| {
                Error::invalid_request("the file holds a number that is not finite")
            })?),
            Scalar::String(s) => Value::String(s.to_string()),
        },
        Yaml::Sequence(items) => Value::Array(items.iter().map(value).collect::<Result<_, _>>()?),
        Yaml::Mapping(m) => {
            let mut out = Map::new();
            for (k, v) in m {
                let key = match k {
                    Yaml::Value(Scalar::String(s)) => s.to_string(),
                    Yaml::Value(Scalar::Integer(i)) => i.to_string(),
                    Yaml::Value(Scalar::Boolean(b)) => b.to_string(),
                    _ => {
                        return Err(Error::invalid_request(
                            "the file has a key that is not text",
                        ))
                    }
                };
                if out.insert(key, value(v)?).is_some() {
                    return Err(Error::invalid_request("the file repeats a key"));
                }
            }
            Value::Object(out)
        }
        _ => {
            return Err(Error::invalid_request(
                "the file uses a YAML tag or a value Hermes Agent's settings do not use",
            ))
        }
    })
}

/// The file's bytes edited from what they hold (current, their value) to want: keys want drops
/// are removed, keys whose values differ are set, mappings on both sides are edited key by key.
/// The result is read again and must be want exactly.
pub fn edit(bytes: Option<&[u8]>, current: &Value, want: &Value) -> Result<Vec<u8>, Error> {
    if current == want {
        if let Some(b) = bytes {
            return Ok(b.to_vec());
        }
    }
    let text = bytes
        .map(std::str::from_utf8)
        .transpose()
        .map_err(|_| Error::invalid_request("the file is not UTF-8 text"))?
        .unwrap_or_default();
    let (Some(cur), Some(want_map)) = (current.as_object(), want.as_object()) else {
        return Err(Error::internal("a settings file's value is a mapping"));
    };
    let file = YamlFile::from_str(text)
        .map_err(|e| Error::invalid_request(format!("the file cannot be edited: {e}")))?;
    let doc = file.ensure_document();
    match doc.as_mapping() {
        Some(root) => sync(&root, cur, want_map)?,
        // An empty document: each top-level key is set on it.
        None => {
            for (k, v) in want_map {
                set_doc(&doc, k, v)?;
            }
        }
    }
    let mut out = file.to_string();
    if !out.is_empty() && !out.ends_with('\n') {
        out.push('\n');
    }
    if &parse(Some(out.as_bytes()))? != want {
        return Err(Error::invalid_request(
            "the file cannot be changed here without changing other settings in it; nothing was written",
        ));
    }
    Ok(out.into_bytes())
}

/// Edits mapping m (holding cur) to hold want.
fn sync(m: &Mapping, cur: &Map<String, Value>, want: &Map<String, Value>) -> Result<(), Error> {
    for k in cur.keys().filter(|k| !want.contains_key(*k)) {
        m.remove(k.as_str());
    }
    for (k, w) in want {
        match (cur.get(k), w) {
            (Some(c), w) if c == w => {}
            (Some(Value::Object(c)), Value::Object(w)) if !c.is_empty() && !w.is_empty() => {
                match m.get_mapping(k.as_str()) {
                    Some(child) => sync(&child, c, w)?,
                    None => set(m, k, &Value::Object(w.clone()))?,
                }
            }
            _ => set(m, k, w)?,
        }
    }
    Ok(())
}

/// What a value is set to: a block mapping or sequence built in v's order, or a scalar.
enum Built {
    Map(Mapping),
    Seq(Sequence),
    Str(String),
    Int(i64),
    Float(f64),
    Bool(bool),
    Null,
}

fn built(v: &Value) -> Result<Built, Error> {
    let none = || Error::internal("cannot build a YAML value");
    Ok(match v {
        Value::Object(m) => Built::Map(mapping(m)?.build_document().as_mapping().ok_or_else(none)?),
        Value::Array(a) => Built::Seq(
            sequence(a)?
                .build_document()
                .as_sequence()
                .ok_or_else(none)?,
        ),
        Value::Null => Built::Null,
        Value::Bool(b) => Built::Bool(*b),
        Value::String(s) => Built::Str(s.clone()),
        Value::Number(n) => match n.as_i64() {
            Some(i) => Built::Int(i),
            None => Built::Float(n.as_f64().unwrap_or_default()),
        },
    })
}

/// Sets key k of m to v.
fn set(m: &Mapping, k: &str, v: &Value) -> Result<(), Error> {
    match built(v)? {
        Built::Map(x) => m.set(k, x),
        Built::Seq(x) => m.set(k, x),
        Built::Str(x) => m.set(k, x.as_str()),
        Built::Int(x) => m.set(k, x),
        Built::Float(x) => m.set(k, x),
        Built::Bool(x) => m.set(k, x),
        Built::Null => m.set(k, None::<i64>),
    };
    Ok(())
}

/// Sets key k of an empty document d to v.
fn set_doc(d: &Document, k: &str, v: &Value) -> Result<(), Error> {
    match built(v)? {
        Built::Map(x) => d.set(k, x),
        Built::Seq(x) => d.set(k, x),
        Built::Str(x) => d.set(k, x.as_str()),
        Built::Int(x) => d.set(k, x),
        Built::Float(x) => d.set(k, x),
        Built::Bool(x) => d.set(k, x),
        Built::Null => d.set(k, None::<i64>),
    };
    Ok(())
}

fn mapping(m: &Map<String, Value>) -> Result<MappingBuilder, Error> {
    let mut b = MappingBuilder::new();
    for (k, v) in m {
        b = pair(b, k, v)?;
    }
    Ok(b)
}

fn sequence(a: &[Value]) -> Result<SequenceBuilder, Error> {
    let mut b = SequenceBuilder::new();
    for v in a {
        b = match v {
            Value::Object(m) => b.insert_mapping(mapping(m)?),
            Value::Array(a) => b.insert_sequence(sequence(a)?),
            Value::Null => b.item(None::<i64>),
            Value::Bool(x) => b.item(*x),
            Value::String(s) => b.item(s.as_str()),
            Value::Number(n) => match n.as_i64() {
                Some(i) => b.item(i),
                None => b.item(n.as_f64().unwrap_or_default()),
            },
        };
    }
    Ok(b)
}

fn pair(b: MappingBuilder, k: &str, v: &Value) -> Result<MappingBuilder, Error> {
    Ok(match v {
        Value::Object(m) => b.insert_mapping(k, mapping(m)?),
        Value::Array(a) => b.insert_sequence(k, sequence(a)?),
        Value::Null => b.pair(k, None::<i64>),
        Value::Bool(x) => b.pair(k, *x),
        Value::String(s) => b.pair(k, s.as_str()),
        Value::Number(n) => match n.as_i64() {
            Some(i) => b.pair(k, i),
            None => b.pair(k, n.as_f64().unwrap_or_default()),
        },
    })
}

/// A YAML file's settings for a backup's diff (the library's JSON reading of its value).
pub fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Value)>> {
    let v = parse(Some(bytes)).ok()?;
    json_leaves(&serde_json::to_vec(&v).ok()?)
}

/// Clear's three-way merge for YAML files: the library's merge of the values, written into the
/// current file as the smallest edit. A file the writer created that ends up empty is removed.
pub struct Yamls;

impl Merge for Yamls {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        if current == applied {
            return Ok(original.map(<[u8]>::to_vec));
        }
        let Some(cur_bytes) = current else {
            return Ok(None);
        };
        let cur = parse(current)?;
        let (o, a) = (
            original.map(|b| parse(Some(b))).transpose()?,
            applied.map(|b| parse(Some(b))).transpose()?,
        );
        match merge_value(o.as_ref(), a.as_ref(), Some(&cur)) {
            None => Ok(None),
            Some(Value::Object(m)) if m.is_empty() && original.is_none() => Ok(None),
            Some(want) => edit(Some(cur_bytes), &cur, &want).map(Some),
        }
    }
}
