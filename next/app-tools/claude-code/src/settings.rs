//! Claude Code's user settings (`settings.json`): which files it takes as its own, what Apply
//! writes into one, and what a preview reads back. Keys and variables are Claude Code's own, as
//! its documentation names them (README.md).

use pluxy_program::Error;
use pluxy_transactions::{Json, Merge};
use serde_json::{json, Map, Value};

/// The schema Claude Code's settings may name in `$schema`.
pub const SCHEMA: &str = "https://json.schemastore.org/claude-code-settings.json";

/// The longest model text the old app took (its pickers' custom values).
const MODEL_MAX: usize = 240;
/// Claude Code's range for the auto-compact window.
pub const WINDOW_MIN: u64 = 100_000;
pub const WINDOW_MAX: u64 = 1_000_000;
/// The suffix that asks for a model's 1M context window.
const ONE_M: &str = "[1m]";

/// The roles, as Claude Code's aliases name them, in the order their variables are written.
pub const ROLES: [&str; 4] = ["opus", "sonnet", "haiku", "fable"];

/// One role's model: the model text without `[1m]`, and whether `[1m]` is asked for.
#[derive(Clone, Debug, PartialEq)]
pub struct Role {
    pub model: String,
    pub one_m: bool,
}

impl Role {
    /// The model as Claude Code reads it: with `[1m]` when the 1M window is asked for.
    fn setting(&self) -> String {
        if self.one_m {
            format!("{}{ONE_M}", self.model)
        } else {
            self.model.clone()
        }
    }
}

/// What Apply writes: the core's address, the client key, the roles and the startup, subagent and
/// context settings.
#[derive(Debug)]
pub struct Settings {
    pub base_url: String,
    pub key: String,
    /// opus, sonnet, haiku and fable; an empty fable follows sonnet.
    pub roles: [Role; 4],
    pub startup_model: String,
    pub subagent_model: String,
    pub max_context_tokens: u64,
    pub auto_compact_pct: u64,
    pub disable_auto_compact: bool,
}

/// model without any trailing `[1m]` (in any case), and whether it had one.
pub fn split_one_m(model: &str) -> (String, bool) {
    let mut m = model.trim();
    let mut one_m = false;
    while m.len() >= ONE_M.len() && m[m.len() - ONE_M.len()..].eq_ignore_ascii_case(ONE_M) {
        m = m[..m.len() - ONE_M.len()].trim_end();
        one_m = true;
    }
    (m.to_string(), one_m)
}

fn text<'a>(input: &'a Value, field: &str) -> Result<&'a str, Error> {
    input[field]
        .as_str()
        .ok_or_else(|| Error::invalid_request(format!("{field} is a string")))
}

/// field of input as a model text: at most 240 characters, no control characters; empty only
/// when may_be_empty.
fn model(input: &Value, field: &str, may_be_empty: bool) -> Result<String, Error> {
    let m = text(input, field)?.trim();
    if m.is_empty() && !may_be_empty {
        return Err(Error::invalid_request(format!("{field} names a model")));
    }
    if m.chars().count() > MODEL_MAX || m.chars().any(char::is_control) {
        return Err(Error::invalid_request(format!(
            "{field} is at most {MODEL_MAX} characters, with no control characters"
        )));
    }
    Ok(m.to_string())
}

fn number(input: &Value, field: &str, min: u64, max: u64) -> Result<u64, Error> {
    input[field]
        .as_u64()
        .filter(|n| (min..=max).contains(n))
        .ok_or_else(|| {
            Error::invalid_request(format!("{field} is a whole number from {min} to {max}"))
        })
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
        let role = |name: &str| -> Result<Role, Error> {
            let (m, suffixed) = split_one_m(&model(input, name, name == "fable")?);
            let flag = input[format!("{name}_1m")].as_bool().unwrap_or(false);
            Ok(Role {
                one_m: !m.is_empty() && (flag || suffixed),
                model: m,
            })
        };
        Ok(Settings {
            base_url: base_url.to_string(),
            key: key.to_string(),
            roles: [
                role("opus")?,
                role("sonnet")?,
                role("haiku")?,
                role("fable")?,
            ],
            startup_model: model(input, "startup_model", true)?,
            subagent_model: model(input, "subagent_model", true)?,
            max_context_tokens: number(input, "max_context_tokens", WINDOW_MIN, WINDOW_MAX)?,
            auto_compact_pct: number(input, "auto_compact_pct", 1, 100)?,
            disable_auto_compact: input["disable_auto_compact"].as_bool().unwrap_or(false),
        })
    }

    /// The role at i, an empty fable following sonnet.
    fn role(&self, i: usize) -> &Role {
        if i == 3 && self.roles[3].model.is_empty() {
            &self.roles[1]
        } else {
            &self.roles[i]
        }
    }
}

/// A window as Claude Code's picker text shows it: 1M, 200K, or the number.
fn window_label(w: u64) -> String {
    if w.is_multiple_of(1_000_000) {
        format!("{}M", w / 1_000_000)
    } else if w.is_multiple_of(1_000) {
        format!("{}K", w / 1_000)
    } else {
        w.to_string()
    }
}

/// The `/model` picker's name and description for a role (or the custom option, with no role).
fn presentation(role: Option<&str>, r: &Role, window: u64) -> (String, String) {
    let name = match role {
        Some(label) => format!("{label} · {}", r.setting()),
        None => r.setting(),
    };
    let w = window_label(window);
    let description = if r.one_m {
        format!("1M enabled · Auto-compact window: {w}")
    } else {
        format!("Auto-compact window: {w}")
    };
    (name, description)
}

/// The settings in bytes (none, or only white space: an empty file), refused unless Claude Code
/// would take them as its own: strict JSON, an object, `env` an object of strings, and `$schema`,
/// if any, Claude Code's.
pub fn parse(bytes: Option<&[u8]>) -> Result<Map<String, Value>, Error> {
    let Some(bytes) = bytes.filter(|b| !b.iter().all(u8::is_ascii_whitespace)) else {
        return Ok(Map::new());
    };
    let Value::Object(root) = Json::parse(bytes)? else {
        return Err(Error::invalid_request(
            "these are not Claude Code settings: the file is not a JSON object",
        ));
    };
    match root.get("$schema") {
        None => {}
        Some(Value::String(s)) if s == SCHEMA => {}
        Some(other) => {
            return Err(Error::invalid_request(format!(
                "these are not Claude Code settings: the file names another schema ({other}), not {SCHEMA}"
            )))
        }
    }
    match root.get("env") {
        None => {}
        Some(Value::Object(env)) if env.values().all(Value::is_string) => {}
        Some(_) => {
            return Err(Error::invalid_request(
                "these are not Claude Code settings: env is not an object of strings",
            ))
        }
    }
    Ok(root)
}

/// The settings file as Apply leaves it: before (the file now, None if there is none) with
/// Claude Code pointed at the core through s. Everything else in it stays.
pub fn apply(before: Option<&[u8]>, s: &Settings) -> Result<Vec<u8>, Error> {
    let mut root = parse(before)?;
    {
        let env = root
            .entry("env")
            .or_insert_with(|| json!({}))
            .as_object_mut()
            .expect("parse checked env");
        // An API key would win over the bearer token; the startup model goes in `model`, so
        // `/model` can still change it.
        env.shift_remove("ANTHROPIC_API_KEY");
        env.shift_remove("ANTHROPIC_MODEL");
        let mut set = |k: &str, v: String| {
            env.insert(k.to_string(), Value::String(v));
        };
        set("ANTHROPIC_BASE_URL", s.base_url.clone());
        set("ANTHROPIC_AUTH_TOKEN", s.key.clone());
        let upper = ["OPUS", "SONNET", "HAIKU", "FABLE"];
        for (i, u) in upper.iter().enumerate() {
            set(&format!("ANTHROPIC_DEFAULT_{u}_MODEL"), s.role(i).setting());
        }
        let labels = ["Opus", "Sonnet", "Haiku", "Fable"];
        for (i, u) in upper.iter().enumerate() {
            let (name, description) =
                presentation(Some(labels[i]), s.role(i), s.max_context_tokens);
            set(&format!("ANTHROPIC_DEFAULT_{u}_MODEL_NAME"), name);
            set(
                &format!("ANTHROPIC_DEFAULT_{u}_MODEL_DESCRIPTION"),
                description,
            );
        }
        // The custom picker entry is the Sonnet model, as the old app wrote it.
        let (name, description) = presentation(None, s.role(1), s.max_context_tokens);
        set("ANTHROPIC_CUSTOM_MODEL_OPTION", s.role(1).setting());
        set("ANTHROPIC_CUSTOM_MODEL_OPTION_NAME", name);
        set("ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION", description);
        if s.subagent_model.is_empty() {
            env.shift_remove("CLAUDE_CODE_SUBAGENT_MODEL");
        } else {
            env.insert(
                "CLAUDE_CODE_SUBAGENT_MODEL".into(),
                Value::String(s.subagent_model.clone()),
            );
        }
        let window = s.max_context_tokens.to_string();
        for (k, v) in [
            (
                "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY",
                "1".to_string(),
            ),
            ("CLAUDE_CODE_AUTO_MODE_SERVER", "0".to_string()),
            ("CLAUDE_CODE_MAX_CONTEXT_TOKENS", window.clone()),
            ("CLAUDE_CODE_AUTO_COMPACT_WINDOW", window),
            (
                "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE",
                s.auto_compact_pct.to_string(),
            ),
            (
                "DISABLE_AUTO_COMPACT",
                if s.disable_auto_compact { "1" } else { "0" }.to_string(),
            ),
        ] {
            env.insert(k.to_string(), Value::String(v));
        }
    }
    if s.startup_model.is_empty() {
        root.shift_remove("model");
    } else {
        root.insert("model".into(), Value::String(s.startup_model.clone()));
    }
    root.insert("autoCompactWindow".into(), json!(s.max_context_tokens));
    root.insert("autoCompactEnabled".into(), json!(!s.disable_auto_compact));
    Json::render(&Value::Object(root))
}

/// What a preview shows of the settings in bytes, never the key: whether they point Claude Code
/// at a core (at base_url, when given), and the values the form shows.
pub fn read_back(bytes: Option<&[u8]>, base_url: Option<&str>) -> Result<Value, Error> {
    let root = parse(bytes)?;
    let empty = Map::new();
    let env = root.get("env").and_then(Value::as_object).unwrap_or(&empty);
    let var = |k: &str| env.get(k).and_then(Value::as_str);
    let key_set = var("ANTHROPIC_AUTH_TOKEN").is_some_and(|k| !k.is_empty());
    let state = match var("ANTHROPIC_BASE_URL") {
        None => "not-configured",
        Some(b)
            if key_set
                && var("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY") == Some("1")
                && var("CLAUDE_CODE_AUTO_MODE_SERVER") == Some("0")
                && base_url.is_none_or(|want| want == b) =>
        {
            "configured"
        }
        Some(_) => "needs-update",
    };
    let mut out = json!({
        "state": state,
        "base_url": var("ANTHROPIC_BASE_URL"),
        "key_set": key_set,
        "startup_model": root.get("model").and_then(Value::as_str),
        "subagent_model": var("CLAUDE_CODE_SUBAGENT_MODEL").unwrap_or_default(),
        "max_context_tokens": var("CLAUDE_CODE_AUTO_COMPACT_WINDOW")
            .and_then(|w| w.parse::<u64>().ok())
            .or_else(|| root.get("autoCompactWindow").and_then(Value::as_u64)),
        "auto_compact_pct": var("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE").and_then(|p| p.parse::<u64>().ok()),
        "disable_auto_compact": var("DISABLE_AUTO_COMPACT") == Some("1")
            || root.get("autoCompactEnabled") == Some(&Value::Bool(false)),
    });
    for (role, upper) in ROLES.iter().zip(["OPUS", "SONNET", "HAIKU", "FABLE"]) {
        let (m, one_m) =
            split_one_m(var(&format!("ANTHROPIC_DEFAULT_{upper}_MODEL")).unwrap_or_default());
        out[*role] = json!(m);
        out[format!("{role}_1m")] = json!(one_m);
    }
    Ok(out)
}

/// The JSON merge for Clear, refusing settings Claude Code would not take as its own first.
pub struct Checked;

impl Merge for Checked {
    fn merge(
        &self,
        original: Option<&[u8]>,
        applied: Option<&[u8]>,
        current: Option<&[u8]>,
    ) -> Result<Option<Vec<u8>>, Error> {
        parse(current)?;
        Json.merge(original, applied, current)
    }
}
