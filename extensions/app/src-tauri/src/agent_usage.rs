use chrono::{DateTime, Utc};
use serde::Serialize;
use serde_json::{Map, Value};
use std::collections::BTreeMap;
use std::env;
use std::fs::{self, File};
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::time::UNIX_EPOCH;
use tauri::Manager;

const MAX_SCAN_FILES: usize = 20_000;
const MAX_JSON_FILE_BYTES: u64 = 16 * 1024 * 1024;
const MAX_MODEL_NAME_CHARS: usize = 120;

const SOURCE_NAMES: &[&str] = &[
    "claude",
    "codex",
    "opencode",
    "omp",
    "pi",
    "amp",
    "droid",
    "codebuff",
    "hermes",
    "goose",
    "kilo",
    "copilot",
    "gemini",
    "antigravity",
    "kimi",
    "qwen",
    "openclaw",
    "grok",
    "zcode",
];

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct AgentUsageReport {
    schema_version: u8,
    generated_at: String,
    sources: Vec<AgentUsageSource>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct AgentUsageSource {
    source: String,
    status: String,
    error: String,
    daily: Vec<AgentUsageDaily>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct AgentUsageDaily {
    date: String,
    input_tokens: u64,
    output_tokens: u64,
    cache_creation_tokens: u64,
    cache_read_tokens: u64,
    #[serde(rename = "reasoningOutputTokens")]
    reasoning_tokens: u64,
    total_tokens: u64,
    #[serde(rename = "costUSD")]
    cost_usd: f64,
    models: Vec<AgentUsageModel>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct AgentUsageModel {
    model: String,
    input_tokens: u64,
    output_tokens: u64,
    cache_creation_tokens: u64,
    cache_read_tokens: u64,
    #[serde(rename = "reasoningOutputTokens")]
    reasoning_tokens: u64,
    total_tokens: u64,
    #[serde(rename = "costUSD")]
    cost_usd: Option<f64>,
}

#[derive(Debug, Default, Clone)]
struct UsageCounter {
    input_tokens: u64,
    output_tokens: u64,
    cache_creation_tokens: u64,
    cache_read_tokens: u64,
    reasoning_tokens: u64,
    total_tokens: u64,
    cost_usd: f64,
}

#[derive(Debug, Default)]
struct DayAggregate {
    totals: UsageCounter,
    models: BTreeMap<String, UsageCounter>,
}

#[derive(Debug, Clone)]
struct UsageSample {
    counter: UsageCounter,
    model: Option<String>,
}

#[tauri::command]
pub(crate) async fn get_agent_usage(app: tauri::AppHandle) -> Result<AgentUsageReport, String> {
    let home = app
        .path()
        .home_dir()
        .map_err(|error| format!("Failed to get user directory: {error}"))?;

    tauri::async_runtime::spawn_blocking(move || build_report(&home))
        .await
        .map_err(|error| format!("Failed to read local Agent usage: {error}"))
}

fn build_report(home: &Path) -> AgentUsageReport {
    let sources = SOURCE_NAMES
        .iter()
        .map(|source| read_source(home, source))
        .collect();

    AgentUsageReport {
        schema_version: 1,
        generated_at: Utc::now().to_rfc3339(),
        sources,
    }
}

fn read_source(home: &Path, source: &str) -> AgentUsageSource {
    let files = source_files(home, source);
    if files.is_empty() {
        return empty_source(source);
    }

    let mut aggregate = BTreeMap::new();
    let mut readable_files = 0usize;
    let mut failed_files = 0usize;

    for path in files {
        match read_usage_file(&path, source, &mut aggregate) {
            Ok(()) => readable_files += 1,
            Err(_) => failed_files += 1,
        }
    }

    if readable_files == 0 && failed_files > 0 {
        return AgentUsageSource {
            source: source.to_string(),
            status: "error".to_string(),
            error: "Unable to read local usage records".to_string(),
            daily: Vec::new(),
        };
    }

    let daily = aggregate
        .into_iter()
        .map(|(date, day)| AgentUsageDaily {
            date,
            input_tokens: day.totals.input_tokens,
            output_tokens: day.totals.output_tokens,
            cache_creation_tokens: day.totals.cache_creation_tokens,
            cache_read_tokens: day.totals.cache_read_tokens,
            reasoning_tokens: day.totals.reasoning_tokens,
            total_tokens: day.totals.total_tokens,
            cost_usd: day.totals.cost_usd,
            models: day
                .models
                .into_iter()
                .map(|(model, counter)| AgentUsageModel {
                    model,
                    input_tokens: counter.input_tokens,
                    output_tokens: counter.output_tokens,
                    cache_creation_tokens: counter.cache_creation_tokens,
                    cache_read_tokens: counter.cache_read_tokens,
                    reasoning_tokens: counter.reasoning_tokens,
                    total_tokens: counter.total_tokens,
                    cost_usd: (counter.cost_usd > 0.0).then_some(counter.cost_usd),
                })
                .collect(),
        })
        .collect::<Vec<_>>();

    AgentUsageSource {
        source: source.to_string(),
        status: if daily.is_empty() {
            "empty".to_string()
        } else {
            "ok".to_string()
        },
        error: String::new(),
        daily,
    }
}

fn empty_source(source: &str) -> AgentUsageSource {
    AgentUsageSource {
        source: source.to_string(),
        status: "empty".to_string(),
        error: String::new(),
        daily: Vec::new(),
    }
}

fn source_files(home: &Path, source: &str) -> Vec<PathBuf> {
    let mut roots = Vec::new();
    let mut extensions = vec!["jsonl"];

    match source {
        "codex" => {
            let root = env_directory("CODEX_HOME").unwrap_or_else(|| home.join(".codex"));
            roots.push(root.join("sessions"));
            roots.push(root.join("archived_sessions"));
        }
        "claude" => {
            let root = env_directory("CLAUDE_CONFIG_DIR").unwrap_or_else(|| home.join(".claude"));
            roots.push(root.join("projects"));
        }
        "gemini" | "antigravity" => {
            roots.push(home.join(".gemini"));
            roots.push(home.join(".antigravity-ide"));
        }
        "kimi" => {
            let root = env_directory("KIMI_CODE_HOME").unwrap_or_else(|| home.join(".kimi-code"));
            roots.push(root.join("sessions"));
        }
        "opencode" => {
            roots.push(home.join(".opencode"));
            roots.push(home.join(".local").join("share").join("opencode"));
            if let Some(app_data) = env::var_os("APPDATA") {
                roots.push(PathBuf::from(app_data).join("opencode"));
            }
            if let Some(local_app_data) = env::var_os("LOCALAPPDATA") {
                roots.push(PathBuf::from(local_app_data).join("opencode"));
            }
            extensions.push("json");
        }
        "copilot" => {
            roots.push(home.join(".copilot"));
            if let Some(app_data) = env::var_os("APPDATA") {
                roots.push(PathBuf::from(app_data).join("GitHub Copilot"));
            }
        }
        // These sources are listed in the UI for compatibility with the
        // common local Agent formats. Their readers can be added independently
        // without changing the response schema.
        _ => return Vec::new(),
    }

    let mut files = Vec::new();
    for root in roots {
        collect_files(&root, 0, &extensions, &mut files);
        if files.len() >= MAX_SCAN_FILES {
            break;
        }
    }
    files.truncate(MAX_SCAN_FILES);
    files
}

fn env_directory(name: &str) -> Option<PathBuf> {
    env::var_os(name)
        .map(PathBuf::from)
        .filter(|path| path.is_dir())
}

fn collect_files(root: &Path, depth: usize, extensions: &[&str], output: &mut Vec<PathBuf>) {
    if output.len() >= MAX_SCAN_FILES || depth > 8 {
        return;
    }
    let Ok(entries) = fs::read_dir(root) else {
        return;
    };
    for entry in entries.flatten() {
        if output.len() >= MAX_SCAN_FILES {
            return;
        }
        let path = entry.path();
        let Ok(file_type) = entry.file_type() else {
            continue;
        };
        if file_type.is_dir() {
            collect_files(&path, depth + 1, extensions, output);
        } else if file_type.is_file()
            && path
                .extension()
                .and_then(|extension| extension.to_str())
                .is_some_and(|extension| {
                    extensions
                        .iter()
                        .any(|candidate| extension.eq_ignore_ascii_case(candidate))
                })
        {
            output.push(path);
        }
    }
}

fn read_usage_file(
    path: &Path,
    source: &str,
    aggregate: &mut BTreeMap<String, DayAggregate>,
) -> Result<(), ()> {
    let metadata = fs::metadata(path).map_err(|_| ())?;
    if metadata.len() > MAX_JSON_FILE_BYTES
        && path.extension().and_then(|value| value.to_str()) == Some("json")
    {
        return Err(());
    }

    let file = File::open(path).map_err(|_| ())?;
    if path.extension().and_then(|value| value.to_str()) == Some("json") {
        let value: Value = serde_json::from_reader(file).map_err(|_| ())?;
        add_value_usage(&value, source, path, aggregate);
        return Ok(());
    }

    let reader = BufReader::new(file);
    let mut latest_codex_usage: Option<Value> = None;
    for line in reader.lines() {
        let line = line.map_err(|_| ())?;
        if line.len() > MAX_JSON_FILE_BYTES as usize
            || (!line.contains("usage") && !line.contains("token"))
        {
            continue;
        }
        let Ok(value) = serde_json::from_str::<Value>(&line) else {
            continue;
        };
        if source == "codex" {
            if extract_codex_usage(&value).is_some() {
                latest_codex_usage = Some(value);
            }
        } else {
            add_value_usage(&value, source, path, aggregate);
        }
    }
    if let Some(value) = latest_codex_usage.as_ref() {
        add_value_usage(value, source, path, aggregate);
    }
    Ok(())
}

fn add_value_usage(
    value: &Value,
    source: &str,
    path: &Path,
    aggregate: &mut BTreeMap<String, DayAggregate>,
) {
    let Some(sample) = extract_usage(value, source) else {
        return;
    };
    let Some(date) = extract_date(value).or_else(|| file_date(path)) else {
        return;
    };

    let day = aggregate.entry(date).or_default();
    add_counter(&mut day.totals, &sample.counter);
    if let Some(model) = sample.model {
        add_counter(day.models.entry(model).or_default(), &sample.counter);
    }
}

fn extract_usage(value: &Value, source: &str) -> Option<UsageSample> {
    match source {
        "codex" => extract_codex_usage(value),
        "kimi" => extract_top_level_usage(value),
        "claude" => extract_claude_usage(value),
        "opencode" | "gemini" | "antigravity" => extract_generic_usage(value),
        _ => None,
    }
}

fn extract_codex_usage(value: &Value) -> Option<UsageSample> {
    let payload = value.get("payload")?.as_object()?;
    if payload.get("type").and_then(Value::as_str) != Some("token_count") {
        return None;
    }
    let info = payload.get("info").and_then(Value::as_object);
    let usage = info
        .and_then(|object| object.get("last_token_usage"))
        .and_then(Value::as_object)
        .or_else(|| payload.get("usage").and_then(Value::as_object))?;
    let mut sample = sample_from_map(usage)?;
    sample.model = string_field(usage, &["model", "modelName"]);
    Some(sample)
}

fn extract_top_level_usage(value: &Value) -> Option<UsageSample> {
    let object = value.as_object()?;
    let usage = object.get("usage")?.as_object()?;
    let mut sample = sample_from_map(usage)?;
    sample.model = string_field(object, &["model", "modelName", "modelAlias"]);
    Some(sample)
}

fn extract_claude_usage(value: &Value) -> Option<UsageSample> {
    let object = value.as_object()?;
    let (usage, model) = if let Some(message) = object.get("message").and_then(Value::as_object) {
        (
            message.get("usage").and_then(Value::as_object),
            string_field(message, &["model", "modelName"]),
        )
    } else {
        (
            object.get("usage").and_then(Value::as_object),
            string_field(object, &["model", "modelName"]),
        )
    };
    let mut sample = sample_from_map(usage?)?;
    sample.model = model;
    Some(sample)
}

fn extract_generic_usage(value: &Value) -> Option<UsageSample> {
    let object = value.as_object()?;
    for key in ["usage", "tokens"] {
        if let Some(usage) = object.get(key).and_then(Value::as_object) {
            let mut sample = sample_from_map(usage)?;
            sample.model = string_field(object, &["model", "modelName", "modelAlias"]);
            return Some(sample);
        }
    }
    if let Some(message) = object.get("message").and_then(Value::as_object) {
        if let Some(usage) = message.get("usage").and_then(Value::as_object) {
            let mut sample = sample_from_map(usage)?;
            sample.model = string_field(message, &["model", "modelName"]);
            return Some(sample);
        }
    }
    None
}

fn sample_from_map(map: &Map<String, Value>) -> Option<UsageSample> {
    let input_tokens = first_counter(map, &["inputTokens", "input_tokens", "input", "inputOther"]);
    let output_tokens = first_counter(map, &["outputTokens", "output_tokens", "output"]);
    let cache_creation_tokens = first_counter(
        map,
        &[
            "cacheCreationTokens",
            "cache_creation_input_tokens",
            "cache_write_input_tokens",
            "inputCacheCreation",
            "cache_write",
        ],
    );
    let cache_read_tokens = first_counter(
        map,
        &[
            "cacheReadTokens",
            "cache_read_input_tokens",
            "cached_input_tokens",
            "inputCacheRead",
            "cache_read",
        ],
    );
    let reasoning_tokens = first_counter(
        map,
        &["reasoningOutputTokens", "reasoning_tokens", "reasoning"],
    );
    let has_usage_field = input_tokens.is_some()
        || output_tokens.is_some()
        || cache_creation_tokens.is_some()
        || cache_read_tokens.is_some()
        || reasoning_tokens.is_some()
        || map.contains_key("totalTokens")
        || map.contains_key("total_tokens");
    if !has_usage_field {
        return None;
    }

    let input_tokens = input_tokens.unwrap_or(0);
    let output_tokens = output_tokens.unwrap_or(0);
    let cache_creation_tokens = cache_creation_tokens.unwrap_or(0);
    let cache_read_tokens = cache_read_tokens.unwrap_or(0);
    let reasoning_tokens = reasoning_tokens.unwrap_or(0);
    let known_total = input_tokens
        .saturating_add(output_tokens)
        .saturating_add(cache_creation_tokens)
        .saturating_add(cache_read_tokens);
    let total_tokens = first_counter(map, &["totalTokens", "total_tokens"])
        .unwrap_or(known_total.max(known_total.saturating_add(reasoning_tokens)));

    Some(UsageSample {
        counter: UsageCounter {
            input_tokens,
            output_tokens,
            cache_creation_tokens,
            cache_read_tokens,
            reasoning_tokens,
            total_tokens,
            cost_usd: first_cost(map, &["costUSD", "costUsd", "cost_usd", "cost"]).unwrap_or(0.0),
        },
        model: None,
    })
}

fn add_counter(target: &mut UsageCounter, value: &UsageCounter) {
    target.input_tokens = target.input_tokens.saturating_add(value.input_tokens);
    target.output_tokens = target.output_tokens.saturating_add(value.output_tokens);
    target.cache_creation_tokens = target
        .cache_creation_tokens
        .saturating_add(value.cache_creation_tokens);
    target.cache_read_tokens = target
        .cache_read_tokens
        .saturating_add(value.cache_read_tokens);
    target.reasoning_tokens = target
        .reasoning_tokens
        .saturating_add(value.reasoning_tokens);
    target.total_tokens = target.total_tokens.saturating_add(value.total_tokens);
    if value.cost_usd.is_finite() && value.cost_usd >= 0.0 {
        target.cost_usd += value.cost_usd;
    }
}

fn first_counter(map: &Map<String, Value>, keys: &[&str]) -> Option<u64> {
    keys.iter()
        .find_map(|key| map.get(*key).and_then(as_counter))
}

fn first_cost(map: &Map<String, Value>, keys: &[&str]) -> Option<f64> {
    keys.iter().find_map(|key| {
        let value = map.get(*key)?;
        let cost = value
            .as_f64()
            .or_else(|| value.as_str()?.parse::<f64>().ok())?;
        (cost.is_finite() && cost >= 0.0).then_some(cost)
    })
}

fn as_counter(value: &Value) -> Option<u64> {
    if let Some(number) = value.as_u64() {
        return Some(number);
    }
    let number = value.as_f64()?;
    (number.is_finite() && number >= 0.0 && number.fract() == 0.0 && number <= u64::MAX as f64)
        .then_some(number as u64)
}

fn string_field(map: &Map<String, Value>, keys: &[&str]) -> Option<String> {
    keys.iter()
        .find_map(|key| map.get(*key).and_then(Value::as_str))
        .map(sanitize_model_name)
        .filter(|value| !value.is_empty())
}

fn sanitize_model_name(value: &str) -> String {
    value
        .chars()
        .filter(|character| !character.is_control())
        .take(MAX_MODEL_NAME_CHARS)
        .collect()
}

fn extract_date(value: &Value) -> Option<String> {
    let object = value.as_object()?;
    for key in ["timestamp", "time", "created_at", "createdAt", "date"] {
        if let Some(date) = object.get(key).and_then(date_value) {
            return Some(date);
        }
    }
    None
}

fn date_value(value: &Value) -> Option<String> {
    if let Some(string) = value.as_str() {
        return valid_date_prefix(string).map(str::to_string);
    }
    let seconds = value.as_i64()?;
    let millis = if seconds.unsigned_abs() > 10_000_000_000 {
        seconds
    } else {
        seconds.saturating_mul(1_000)
    };
    DateTime::<Utc>::from_timestamp_millis(millis).map(|date| date.format("%Y-%m-%d").to_string())
}

fn valid_date_prefix(value: &str) -> Option<&str> {
    let prefix = value.get(..10)?;
    let bytes = prefix.as_bytes();
    if bytes[4] == b'-'
        && bytes[7] == b'-'
        && bytes[..4].iter().all(u8::is_ascii_digit)
        && bytes[5..7].iter().all(u8::is_ascii_digit)
        && bytes[8..10].iter().all(u8::is_ascii_digit)
    {
        Some(prefix)
    } else {
        None
    }
}

fn file_date(path: &Path) -> Option<String> {
    let modified = fs::metadata(path).ok()?.modified().ok()?;
    let duration = modified.duration_since(UNIX_EPOCH).ok()?;
    DateTime::<Utc>::from_timestamp_millis(duration.as_millis().try_into().ok()?)
        .map(|date| date.format("%Y-%m-%d").to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::io::Write;

    #[test]
    fn parses_codex_token_count_without_private_fields() {
        let value = json!({
            "timestamp": "2026-10-29T12:00:00Z",
            "payload": {
                "type": "token_count",
                "info": {
                    "last_token_usage": {
                        "input_tokens": 90,
                        "cached_input_tokens": 10,
                        "output_tokens": 50,
                        "reasoning_output_tokens": 5,
                        "total_tokens": 155,
                        "model": "gpt-5",
                        "cwd": "C:\\private"
                    }
                }
            }
        });
        let mut aggregate = BTreeMap::new();
        add_value_usage(&value, "codex", Path::new("rollout.jsonl"), &mut aggregate);
        let row = &aggregate["2026-10-29"];
        assert_eq!(row.totals.total_tokens, 155);
        assert_eq!(row.totals.cache_read_tokens, 10);
        assert!(row.models.contains_key("gpt-5"));
        let output = serde_json::to_string(&row.models.keys().collect::<Vec<_>>()).unwrap();
        assert!(!output.contains("private"));
    }

    #[test]
    fn parses_kimi_usage_and_uses_the_request_model() {
        let value = json!({
            "time": "2026-10-29T12:00:00Z",
            "model": "kimi-k2",
            "usage": {
                "inputOther": 20,
                "inputCacheCreation": 3,
                "inputCacheRead": 4,
                "output": 9
            }
        });
        let mut aggregate = BTreeMap::new();
        add_value_usage(&value, "kimi", Path::new("wire.jsonl"), &mut aggregate);
        let row = &aggregate["2026-10-29"];
        assert_eq!(row.totals.total_tokens, 36);
        assert_eq!(row.totals.input_tokens, 20);
        assert!(row.models.contains_key("kimi-k2"));
    }

    #[test]
    fn parses_claude_message_usage() {
        let value = json!({
            "timestamp": "2026-10-29T12:00:00Z",
            "message": {
                "model": "claude-sonnet",
                "usage": {
                    "input_tokens": 11,
                    "cache_creation_input_tokens": 5,
                    "cache_read_input_tokens": 3,
                    "output_tokens": 7
                }
            }
        });
        let mut aggregate = BTreeMap::new();
        add_value_usage(&value, "claude", Path::new("session.jsonl"), &mut aggregate);
        assert_eq!(aggregate["2026-10-29"].totals.total_tokens, 26);
    }

    #[test]
    fn malformed_lines_are_ignored_without_leaking_content() {
        let path =
            std::env::temp_dir().join(format!("cpa-agent-usage-{}.jsonl", std::process::id()));
        let mut file = File::create(&path).unwrap();
        writeln!(file, "not json").unwrap();
        writeln!(file, "{{\"usage\":{{\"input_tokens\":1,\"output_tokens\":2}},\"time\":\"2026-10-29T00:00:00Z\",\"model\":\"safe\",\"prompt\":\"private\"}}").unwrap();
        drop(file);
        let mut aggregate = BTreeMap::new();
        read_usage_file(&path, "kimi", &mut aggregate).unwrap();
        assert_eq!(aggregate["2026-10-29"].totals.total_tokens, 3);
        assert!(aggregate["2026-10-29"]
            .models
            .keys()
            .all(|model| model != "private"));
        let _ = fs::remove_file(path);
    }
}
