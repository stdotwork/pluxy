//! The model catalog Codex reads from `model_catalog_json` (README.md): its own JSON format, a
//! `models` list of model metadata. Apply writes the models the person keeps, each with the
//! settings the form edits; a new model is made from pluxy's Codex client template for its
//! upstream name (the file the core serves Codex, `next/extensions/codex/client_models.json`),
//! trimmed to the fields Codex needs. Entries already in the file keep every other field.

use pluxy_program::Error;
use pluxy_transactions::{Json as JsonFile, MAX_FILE};
use serde_json::{json, Map, Value};
use std::sync::OnceLock;

/// The catalog's file name, beside config.toml by default.
pub const FILE: &str = "pluxy-model-catalog.json";

/// pluxy's Codex client templates, embedded: refreshed with the core's (next/README.md).
const TEMPLATES: &str = include_str!("../../../extensions/codex/client_models.json");

/// The fields a new entry takes from its template: those Codex requires and those that shape
/// how it drives the model. The template's other model messages are left out: a full template
/// is about 88 KB and the catalog at most 256 KiB.
const FROM_TEMPLATE: [&str; 24] = [
    "slug",
    "display_name",
    "description",
    "default_reasoning_level",
    "supported_reasoning_levels",
    "shell_type",
    "visibility",
    "supported_in_api",
    "priority",
    "support_verbosity",
    "default_verbosity",
    "apply_patch_tool_type",
    "web_search_tool_type",
    "truncation_policy",
    "supports_image_detail_original",
    "context_window",
    "max_context_window",
    "auto_compact_token_limit",
    "effective_context_window_percent",
    "experimental_supported_tools",
    "input_modalities",
    "supports_search_tool",
    "tool_mode",
    "default_reasoning_summary",
];

/// What a model served by another provider than Codex's gets instead of its template's value,
/// as the core does for the same catalog.
fn non_codex() -> [(&'static str, Value); 4] {
    [
        ("apply_patch_tool_type", Value::Null),
        ("tool_mode", json!("direct")),
        ("supports_search_tool", json!(false)),
        ("supports_image_detail_original", json!(false)),
    ]
}

/// The reasoning efforts Codex knows, and the description a level the template lacks gets.
const EFFORTS: [(&str, &str); 8] = [
    ("none", "No reasoning"),
    ("minimal", "Minimal reasoning effort"),
    ("low", "Low reasoning effort"),
    ("medium", "Medium reasoning effort"),
    ("high", "High reasoning effort"),
    ("xhigh", "Extra high reasoning effort"),
    ("max", "Maximum reasoning effort"),
    ("ultra", "Ultra reasoning effort"),
];
const MODALITIES: [&str; 3] = ["text", "image", "audio"];
/// The longest name or description (the old app's limit) and system prompt.
const TEXT_MAX: usize = 4000;
const PROMPT_MAX: usize = 32 << 10;
/// The most models one catalog lists.
pub const MODELS_MAX: usize = 64;

fn templates() -> &'static Map<String, Value> {
    static T: OnceLock<Map<String, Value>> = OnceLock::new();
    T.get_or_init(|| {
        let v: Value = serde_json::from_str(TEMPLATES).expect("the embedded templates are JSON");
        v["models"]
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(|m| Some((m["slug"].as_str()?.to_string(), m.clone())))
            .collect()
    })
}

fn refuse(why: impl std::fmt::Display) -> Error {
    Error::invalid_request(format!("this is not a Codex model catalog: {why}"))
}

/// The catalog in bytes (None, or only white space: no catalog yet), refused unless it is one
/// Codex reads: a JSON object whose `models` is a list of objects, each with a string `slug`.
pub fn parse(bytes: Option<&[u8]>) -> Result<Option<Map<String, Value>>, Error> {
    let Some(bytes) = bytes.filter(|b| !b.iter().all(u8::is_ascii_whitespace)) else {
        return Ok(None);
    };
    let Value::Object(root) = JsonFile::parse(bytes)? else {
        return Err(refuse("the file is not a JSON object"));
    };
    let models = root
        .get("models")
        .and_then(Value::as_array)
        .ok_or_else(|| refuse("it has no models list"))?;
    if !models.iter().all(|m| m["slug"].is_string()) {
        return Err(refuse("a model has no slug"));
    }
    Ok(Some(root))
}

/// One model as the form edits it.
#[derive(Debug, Clone, PartialEq)]
pub struct Model {
    pub slug: String,
    /// For a model the catalog does not hold yet: its upstream name (its template) and whether
    /// another provider than Codex's serves it.
    pub upstream: Option<String>,
    pub non_codex: bool,
    pub display_name: String,
    pub description: String,
    pub context_window: Option<i64>,
    pub max_context_window: Option<i64>,
    pub percent: Option<i64>,
    pub compact: Option<i64>,
    pub reasoning: Vec<String>,
    pub default_reasoning: String,
    pub modalities: Vec<String>,
    pub visible: bool,
    pub tool_search: bool,
    /// A new system prompt; None keeps the entry's (or its template's).
    pub system_prompt: Option<String>,
}

fn text(v: &Value, field: &str, max: usize, may_be_empty: bool) -> Result<String, Error> {
    let s = v[field]
        .as_str()
        .ok_or_else(|| Error::invalid_request(format!("{field} is a string")))?
        .trim();
    if (s.is_empty() && !may_be_empty) || s.chars().count() > max {
        return Err(Error::invalid_request(format!(
            "{field} is {}at most {max} characters",
            if may_be_empty { "" } else { "1 to " }
        )));
    }
    Ok(s.to_string())
}

fn tokens(v: &Value, field: &str) -> Result<Option<i64>, Error> {
    match &v[field] {
        Value::Null => Ok(None),
        n => n
            .as_i64()
            .filter(|n| *n > 0 && *n <= 1 << 53)
            .map(Some)
            .ok_or_else(|| Error::invalid_request(format!("{field} is a positive whole number"))),
    }
}

fn names(v: &Value, field: &str, allowed: &[&str]) -> Result<Vec<String>, Error> {
    let list: Vec<String> = v[field]
        .as_array()
        .into_iter()
        .flatten()
        .map(|x| x.as_str().unwrap_or_default().to_string())
        .collect();
    let mut seen = Vec::new();
    for n in &list {
        if !allowed.contains(&n.as_str()) || seen.contains(&n) {
            return Err(Error::invalid_request(format!(
                "{field} lists each of {} at most once",
                allowed.join(", ")
            )));
        }
        seen.push(n);
    }
    Ok(list)
}

impl Model {
    /// One model of an apply's input, checked as the old catalog editor checked it.
    pub fn from_input(v: &Value) -> Result<Model, Error> {
        let slug = text(v, "slug", 240, false)?;
        if slug.chars().any(char::is_control) {
            return Err(Error::invalid_request("slug has control characters"));
        }
        let efforts: Vec<&str> = EFFORTS.iter().map(|(e, _)| *e).collect();
        let m = Model {
            upstream: match &v["upstream"] {
                Value::Null => None,
                _ => Some(text(v, "upstream", 240, false)?),
            },
            non_codex: v["non_codex"].as_bool().unwrap_or(false),
            display_name: text(v, "display_name", TEXT_MAX, false)?,
            description: text(v, "description", TEXT_MAX, true)?,
            context_window: tokens(v, "context_window")?,
            max_context_window: tokens(v, "max_context_window")?,
            percent: tokens(v, "effective_context_window_percent")?,
            compact: tokens(v, "auto_compact_token_limit")?,
            reasoning: names(v, "reasoning", &efforts)?,
            default_reasoning: v["default_reasoning"]
                .as_str()
                .unwrap_or_default()
                .to_string(),
            modalities: names(v, "input_modalities", &MODALITIES)?,
            visible: v["visible"].as_bool().unwrap_or(true),
            tool_search: v["tool_search"].as_bool().unwrap_or(false),
            system_prompt: match &v["system_prompt"] {
                Value::Null => None,
                _ => Some(text(v, "system_prompt", PROMPT_MAX, false)?),
            },
            slug,
        };
        let bad = |why: &str| Err(Error::invalid_request(format!("{}: {why}", m.slug)));
        if let (Some(c), Some(x)) = (m.context_window, m.max_context_window) {
            if x < c {
                return bad("the maximum context must be at least the context window");
            }
        }
        if m.percent.is_some_and(|p| p > 100) {
            return bad("the effective context is a whole number from 1 to 100");
        }
        if let (Some(t), Some(c)) = (m.compact, m.context_window) {
            if t > c {
                return bad("the compaction threshold is no greater than the context window");
            }
        }
        if !m.default_reasoning.is_empty() && !m.reasoning.contains(&m.default_reasoning) {
            return bad("the default reasoning level is one of the selected levels");
        }
        if m.modalities.is_empty() {
            return bad("select at least one input type");
        }
        Ok(m)
    }
}

/// A new entry for slug from the template of upstream, or None when pluxy has no template for it.
fn from_template(slug: &str, upstream: &str, non_codex_model: bool) -> Option<Map<String, Value>> {
    let t = templates().get(upstream)?;
    let mut m = Map::new();
    for k in FROM_TEMPLATE {
        if let Some(v) = t.get(k) {
            m.insert(k.to_string(), v.clone());
        }
    }
    m.insert("slug".into(), json!(slug));
    if non_codex_model {
        for (k, v) in non_codex() {
            if m.contains_key(k) || k == "tool_mode" {
                m.insert(k.into(), v);
            }
        }
    }
    let prompt = t["model_messages"]["instructions_template"]
        .as_str()
        .or_else(|| t["base_instructions"].as_str())?;
    m.insert(
        "model_messages".into(),
        json!({ "instructions_template": prompt }),
    );
    Some(m)
}

/// What the form shows of one entry: its editable settings, never its system prompt's text.
fn editable(e: &Map<String, Value>) -> Value {
    let prompt = e
        .get("model_messages")
        .and_then(|m| m["instructions_template"].as_str())
        .or_else(|| e.get("base_instructions").and_then(Value::as_str));
    json!({
        "slug": e["slug"],
        "display_name": e.get("display_name").cloned().unwrap_or(Value::Null),
        "description": e.get("description").and_then(Value::as_str).unwrap_or_default(),
        "context_window": e.get("context_window").cloned().unwrap_or(Value::Null),
        "max_context_window": e.get("max_context_window").cloned().unwrap_or(Value::Null),
        "effective_context_window_percent": e.get("effective_context_window_percent").cloned().unwrap_or(Value::Null),
        "auto_compact_token_limit": e.get("auto_compact_token_limit").cloned().unwrap_or(Value::Null),
        "reasoning": e.get("supported_reasoning_levels").and_then(Value::as_array).into_iter().flatten()
            .filter_map(|l| l["effort"].as_str()).collect::<Vec<_>>(),
        "default_reasoning": e.get("default_reasoning_level").and_then(Value::as_str).unwrap_or_default(),
        "input_modalities": e.get("input_modalities").cloned().unwrap_or_else(|| json!(["text", "image"])),
        "visible": e.get("visibility").and_then(Value::as_str).is_none_or(|v| v == "list"),
        "tool_search": e.get("supports_search_tool").and_then(Value::as_bool).unwrap_or(false),
        "system_prompt_chars": prompt.map_or(0, |p| p.chars().count()),
    })
}

/// What a preview shows of the catalog in bytes: each model's editable settings and the approval
/// model (the first entry's `auto_review_model_override`; Apply sets every entry's).
pub fn read_back(bytes: Option<&[u8]>) -> Result<Value, Error> {
    let root = parse(bytes)?.unwrap_or_default();
    let models: Vec<&Map<String, Value>> = root
        .get("models")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(Value::as_object)
        .collect();
    Ok(json!({
        "models": models.iter().map(|e| editable(e)).collect::<Vec<_>>(),
        "approval_model": models.first()
            .and_then(|e| e.get("auto_review_model_override"))
            .and_then(Value::as_str)
            .unwrap_or_default(),
    }))
}

/// Explicit core import: for each model the core serves (`slug`, `upstream`, `display_name`,
/// `non_codex`), the editable settings its new entry would have, and the slugs pluxy has no
/// Codex template for (not listed, as the core leaves them out of the catalog it serves).
pub fn import(served: &Value) -> Result<Value, Error> {
    let list = served
        .as_array()
        .filter(|l| l.len() <= 1024)
        .ok_or_else(|| Error::invalid_request("models lists at most 1024 served models"))?;
    let (mut models, mut missing) = (Vec::new(), Vec::new());
    for s in list {
        let slug = text(s, "slug", 240, false)?;
        let upstream = s["upstream"].as_str().unwrap_or(&slug).to_string();
        match from_template(&slug, &upstream, s["non_codex"].as_bool().unwrap_or(false)) {
            Some(mut e) => {
                if let Some(d) = s["display_name"].as_str().filter(|d| !d.trim().is_empty()) {
                    e.insert("display_name".into(), json!(d.trim()));
                }
                let mut v = editable(&e);
                v["upstream"] = json!(upstream);
                v["non_codex"] = json!(s["non_codex"].as_bool().unwrap_or(false));
                models.push(v);
            }
            None => missing.push(json!(slug)),
        }
    }
    Ok(json!({"models": models, "missing": missing}))
}

/// The catalog as Apply leaves it: before (the file now, None if there is none) listing exactly
/// models, in order, each with the form's settings and approval as its approval model ("" for
/// Codex's default). Fields the form does not edit, and other top-level keys, stay.
pub fn apply(before: Option<&[u8]>, models: &[Model], approval: &str) -> Result<Vec<u8>, Error> {
    let mut root = parse(before)?.unwrap_or_default();
    let old: Vec<Map<String, Value>> = root
        .get("models")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
        .filter_map(|m| m.as_object().cloned())
        .collect();
    let mut out = Vec::new();
    for (i, m) in models.iter().enumerate() {
        let mut e = match old.iter().find(|e| e["slug"] == json!(m.slug)) {
            Some(e) => e.clone(),
            None => {
                let up = m.upstream.as_deref().unwrap_or(&m.slug);
                from_template(&m.slug, up, m.non_codex).ok_or_else(|| {
                    Error::invalid_request(format!(
                        "{}: pluxy has no Codex template for {up}",
                        m.slug
                    ))
                })?
            }
        };
        let mut put = |k: &str, v: Option<Value>| match v {
            Some(v) => {
                e.insert(k.into(), v);
            }
            None => {
                e.shift_remove(k);
            }
        };
        put("display_name", Some(json!(m.display_name)));
        let description = if m.description.is_empty() {
            Value::Null
        } else {
            json!(m.description)
        };
        put("description", Some(description));
        put("context_window", m.context_window.map(|n| json!(n)));
        put("max_context_window", m.max_context_window.map(|n| json!(n)));
        put(
            "effective_context_window_percent",
            m.percent.map(|n| json!(n)),
        );
        put("auto_compact_token_limit", m.compact.map(|n| json!(n)));
        put(
            "default_reasoning_level",
            (!m.default_reasoning.is_empty()).then(|| json!(m.default_reasoning)),
        );
        put("input_modalities", Some(json!(m.modalities)));
        put(
            "visibility",
            Some(json!(if m.visible { "list" } else { "hide" })),
        );
        put("supports_search_tool", Some(json!(m.tool_search)));
        put("priority", Some(json!(i)));
        put(
            "auto_review_model_override",
            (!approval.is_empty()).then(|| json!(approval)),
        );
        let levels: Vec<Value> = m
            .reasoning
            .iter()
            .map(|r| {
                e.get("supported_reasoning_levels")
                    .and_then(Value::as_array)
                    .into_iter()
                    .flatten()
                    .find(|l| l["effort"] == json!(r))
                    .cloned()
                    .unwrap_or_else(|| {
                        let d = EFFORTS.iter().find(|(x, _)| x == r).map_or("", |(_, d)| *d);
                        json!({"effort": r, "description": d})
                    })
            })
            .collect();
        e.insert("supported_reasoning_levels".into(), Value::Array(levels));
        if let Some(p) = &m.system_prompt {
            let mm = e.entry("model_messages").or_insert_with(|| json!({}));
            if !mm.is_object() {
                *mm = json!({});
            }
            mm["instructions_template"] = json!(p);
            e.shift_remove("base_instructions");
        }
        let has_prompt = e
            .get("model_messages")
            .and_then(|m| m["instructions_template"].as_str())
            .or_else(|| e.get("base_instructions").and_then(Value::as_str))
            .is_some_and(|p| !p.is_empty());
        if !has_prompt {
            return Err(Error::invalid_request(format!(
                "{}: the model has no system prompt, which Codex needs",
                m.slug
            )));
        }
        out.push(Value::Object(e));
    }
    root.insert("models".into(), Value::Array(out));
    let bytes = JsonFile::render(&Value::Object(root))?;
    if bytes.len() > MAX_FILE {
        return Err(Error::invalid_request(format!(
            "the catalog would be {} KiB, more than the {} KiB a file may be: list fewer models",
            bytes.len() >> 10,
            MAX_FILE >> 10
        )));
    }
    Ok(bytes)
}
