//! The three-way merge that keeps the person's edits: given the original image (before the
//! writer's first apply, with the person's later edits promoted into it), the image the writer
//! last applied and the file now, what the file should be without the writer's changes.

use pluxy_program::Error;
use serde_json::{Map, Value};

/// A file format's three-way merge of whole images (None: no file).
pub trait Merge {
    /// The file as current would be without what applied changed from original: values the
    /// person did not touch since the apply go back to original's, and the person's edits win.
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error>;
}

/// Three-way merge of JSON values (None: absent). Where current equals applied, the person left
/// it alone and original's comes back; objects merge key by key; anything else the person
/// changed stays as they left it.
pub fn merge_value(
    original: Option<&Value>,
    applied: Option<&Value>,
    current: Option<&Value>,
) -> Option<Value> {
    if current == applied {
        return original.cloned();
    }
    match (
        applied.and_then(Value::as_object),
        current.and_then(Value::as_object),
    ) {
        (Some(a), Some(c)) => {
            let o = original.and_then(Value::as_object);
            let mut keys: Vec<&String> = a
                .keys()
                .chain(c.keys())
                .chain(o.into_iter().flat_map(Map::keys))
                .collect();
            keys.sort();
            keys.dedup();
            let mut merged = Map::new();
            for k in keys {
                if let Some(v) = merge_value(o.and_then(|o| o.get(k)), a.get(k), c.get(k)) {
                    merged.insert(k.clone(), v);
                }
            }
            Some(Value::Object(merged))
        }
        _ => current.cloned(),
    }
}

/// JSON files: parsed, merged with [`merge_value`] and written with two-space indentation and a
/// final newline. Keys come out sorted (the parser keeps no order) and comments are not JSON, so
/// a format that must keep them needs its own [`Merge`].
pub struct Json;

impl Json {
    /// The JSON value of a file's bytes.
    pub fn parse(bytes: &[u8]) -> Result<Value, Error> {
        serde_json::from_slice(bytes)
            .map_err(|e| Error::invalid_request(format!("the file is not JSON: {e}")))
    }

    /// The bytes the library writes for a JSON value.
    pub fn render(v: &Value) -> Result<Vec<u8>, Error> {
        let mut s = serde_json::to_string_pretty(v)
            .map_err(|e| Error::internal(format!("cannot write JSON: {e}")))?;
        s.push('\n');
        Ok(s.into_bytes())
    }
}

impl Merge for Json {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        if current == applied {
            return Ok(original.map(<[u8]>::to_vec));
        }
        let Some(current) = current else {
            return Ok(None);
        };
        let parse = |b: Option<&[u8]>| b.map(Json::parse).transpose();
        let (o, a, c) = (parse(original)?, parse(applied)?, Json::parse(current)?);
        merge_value(o.as_ref(), a.as_ref(), Some(&c))
            .map(|v| Json::render(&v))
            .transpose()
    }
}
