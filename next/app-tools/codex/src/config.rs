//! Codex's user config (`config.toml`): which files it takes as its own, what Apply writes into
//! one, what a preview reads back, and the merge Clear makes. Keys are Codex's own, as its source
//! names them (README.md). The file is edited in place with toml_edit, so comments, formatting
//! and everything Apply does not set stay as the person left them.

use pluxy_program::Error;
use pluxy_transactions::toml::toml_edit::{DocumentMut, Item, Table};
use pluxy_transactions::toml::{self, set, table_mut};
use serde_json::{json, Value as Json};

/// The provider Apply adds and points Codex at.
pub const PROVIDER: &str = "pluxy";
/// The only wire protocol Codex speaks to a provider today.
const WIRE_API: &str = "responses";
/// Keys of the provider table that would change how Codex authenticates to it; Apply removes
/// them so the bearer token is what it sends.
const OTHER_AUTH: [&str; 2] = ["env_key", "requires_openai_auth"];

/// Codex's subagent settings (`[agents]`), as the form holds them.
#[derive(Debug, Clone, PartialEq)]
pub struct Agents {
    pub enabled: bool,
    pub max_threads: Option<u64>,
    pub model: String,
    pub effort: String,
    pub interrupt: bool,
}

impl Agents {
    /// Codex's own defaults: what a file without `[agents]` means.
    pub fn codex_default() -> Agents {
        Agents {
            enabled: true,
            max_threads: None,
            model: String::new(),
            effort: String::new(),
            interrupt: true,
        }
    }
}

/// What Apply writes into config.toml.
#[derive(Debug)]
pub struct Settings {
    /// The provider's base URL: the core's address with `/v1`.
    pub base_url: String,
    pub key: String,
    pub model: String,
    /// `model_catalog_json`: the catalog's file name, or its absolute path.
    pub catalog: String,
    pub remote_compaction: bool,
    pub agents: Agents,
}

/// What every refusal of a file starts with.
const REFUSAL: &str = "these are not Codex settings: ";

fn refuse(why: impl std::fmt::Display) -> Error {
    toml::refuse(REFUSAL, why)
}

/// A table at key of t, if there is one: refused when key holds anything but a table.
fn table_at<'a>(t: &'a Table, key: &str, name: &str) -> Result<Option<&'a Table>, Error> {
    toml::table_at(t, key, name, REFUSAL)
}

/// The config in bytes (none, or only white space: an empty file), refused unless Codex would
/// take it as its own and Apply can edit it: TOML, with `model_providers`, the pluxy provider,
/// its `capabilities` and `agents` tables where present, and the provider's `wire_api`, if set,
/// "responses".
pub fn parse(bytes: Option<&[u8]>) -> Result<DocumentMut, Error> {
    let doc = toml::read(bytes, REFUSAL)?;
    let root = doc.as_table();
    if let Some(providers) = table_at(root, "model_providers", "model_providers")? {
        if let Some(p) = table_at(providers, PROVIDER, "model_providers.pluxy")? {
            table_at(p, "capabilities", "model_providers.pluxy.capabilities")?;
            match p.get("wire_api") {
                None => {}
                Some(i) if i.as_str() == Some(WIRE_API) => {}
                Some(_) => {
                    return Err(refuse(
                        "model_providers.pluxy.wire_api is not \"responses\"",
                    ))
                }
            }
        }
    }
    table_at(root, "agents", "agents")?;
    Ok(doc)
}

/// The config file as Apply leaves it: before (the file now, None if there is none) with Codex
/// pointed at the core through s. Everything else in it stays.
pub fn apply(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let mut doc = parse(before)?;
    let had_agents = doc.contains_key("agents");
    let root = doc.as_table_mut();
    set(root, "model_provider", PROVIDER);
    set(root, "model", s.model.as_str());
    set(root, "model_catalog_json", s.catalog.as_str());
    let p = table_mut(table_mut(root, "model_providers", true), PROVIDER, false);
    set(p, "name", PROVIDER);
    set(p, "base_url", s.base_url.as_str());
    set(p, "wire_api", WIRE_API);
    set(p, "experimental_bearer_token", s.key.as_str());
    for k in OTHER_AUTH {
        p.remove(k);
    }
    if s.remote_compaction {
        set(
            table_mut(p, "capabilities", false),
            "remote_compaction",
            "v2",
        );
    } else if let Some(c) = p.get_mut("capabilities").and_then(Item::as_table_mut) {
        c.remove("remote_compaction");
        if c.is_empty() {
            p.remove("capabilities");
        }
    }
    // A file without [agents] gets one only when the settings differ from Codex's defaults.
    if had_agents || s.agents != Agents::codex_default() {
        let a = table_mut(root, "agents", false);
        let g = &s.agents;
        set(a, "enabled", g.enabled);
        set(a, "interrupt_message", g.interrupt);
        // Codex also reads `max_threads` as this setting; one is written, so the other goes.
        a.remove("max_threads");
        match g.max_threads {
            Some(n) => set(a, "max_concurrent_threads_per_session", n as i64),
            None => {
                a.remove("max_concurrent_threads_per_session");
            }
        }
        for (k, v) in [
            ("default_subagent_model", &g.model),
            ("default_subagent_reasoning_effort", &g.effort),
        ] {
            if v.is_empty() {
                a.remove(k);
            } else {
                set(a, k, v.as_str());
            }
        }
    }
    Ok(doc.to_string().into_bytes())
}

/// What a preview shows of the config in bytes, never the key: whether it points Codex at the
/// core (at base_url, when given), the provider it uses now, and the values the form shows.
pub fn read_back(bytes: Option<&[u8]>, base_url: Option<&str>) -> Result<Json, Error> {
    let doc = parse(bytes)?;
    let root = doc.as_table();
    let s = |t: &Table, k: &str| t.get(k).and_then(Item::as_str).map(str::to_string);
    let provider = root
        .get("model_providers")
        .and_then(Item::as_table)
        .and_then(|t| t.get(PROVIDER))
        .and_then(Item::as_table);
    let key_set = provider
        .and_then(|p| s(p, "experimental_bearer_token"))
        .is_some_and(|k| !k.is_empty());
    let current = s(root, "model_provider");
    let pointed = provider.and_then(|p| s(p, "base_url"));
    let want = base_url.map(base_url_of);
    let state = match &pointed {
        None => "not-configured",
        Some(b)
            if key_set
                && current.as_deref() == Some(PROVIDER)
                && root.contains_key("model_catalog_json")
                && want.as_ref().is_none_or(|w| w == b) =>
        {
            "configured"
        }
        Some(_) => "needs-update",
    };
    let agents = root.get("agents").and_then(Item::as_table);
    let d = Agents::codex_default();
    let ab = |k: &str, def: bool| {
        agents
            .and_then(|a| a.get(k))
            .and_then(Item::as_bool)
            .unwrap_or(def)
    };
    let a_s = |k: &str| agents.and_then(|a| s(a, k)).unwrap_or_default();
    Ok(json!({
        "state": state,
        "base_url": pointed,
        "key_set": key_set,
        "provider": current,
        "model": s(root, "model"),
        "profile": s(root, "profile"),
        "catalog": s(root, "model_catalog_json"),
        "remote_compaction": provider
            .and_then(|p| p.get("capabilities"))
            .and_then(Item::as_table)
            .and_then(|c| s(c, "remote_compaction"))
            .as_deref()
            == Some("v2"),
        "agents": {
            "enabled": ab("enabled", d.enabled),
            "max_threads": agents
                .and_then(|a| {
                    a.get("max_concurrent_threads_per_session")
                        .or_else(|| a.get("max_threads"))
                })
                .and_then(Item::as_integer),
            "model": a_s("default_subagent_model"),
            "effort": a_s("default_subagent_reasoning_effort"),
            "interrupt": ab("interrupt_message", d.interrupt),
        },
    }))
}

/// The provider's base URL for the core at address: the address with `/v1` once.
pub fn base_url_of(address: &str) -> String {
    let a = address.trim_end_matches('/');
    if a.ends_with("/v1") {
        a.to_string()
    } else {
        format!("{a}/v1")
    }
}

/// The config as current would be without what applied changed from original
/// ([`toml::merge`]), each file read through [`parse`].
pub fn merge(
    original: Option<&[u8]>,
    applied: Option<&[u8]>,
    current: &[u8],
) -> Result<Option<Vec<u8>>, Error> {
    toml::merge(parse, original, applied, current)
}

/// A config file's settings for a backup's diff ([`toml::leaves`]). None when it is not TOML.
pub fn leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Json)>> {
    toml::leaves(parse, bytes)
}
