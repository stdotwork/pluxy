//! A Codex rollout file (`rollout-….jsonl`, one JSON object a line) as token rows, by the rules of
//! the old desktop app's Codex reader:
//!
//! - a `turn_context` line sets the model for the lines after it;
//! - a `token_count` event's `info.total_token_usage` is a cumulative counter: each event counts
//!   what the counter grew by since the last one in the file, field by field, never below zero (a
//!   counter that falls starts again from the lower value); an event with no cumulative counter
//!   (`info.last_token_usage`, or `payload.usage`) counts as it is;
//! - input is net of cached input, and the total is the event's own `total_tokens`, else input +
//!   output + cached input;
//! - an event is one sample, even when it adds nothing;
//! - an event's time is its line's timestamp.
//!
//! Only counters, a model name and a time are read. A prompt, a message, a folder or a key is never
//! looked at beyond finding those fields, and none is kept.

use pluxy_program::Error;
use pluxy_usage::{Sink, Tokens};
use serde_json::{Map, Value};

/// The longest model name kept, in characters.
const MAX_MODEL_NAME: usize = 120;
/// Lines between looks at whether the job was cancelled.
const CANCEL_EVERY: usize = 2048;

/// Where the rows go: a usage sink, or a list in a test.
pub trait Out {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error>;
    /// Whether the job was cancelled.
    fn cancelled(&self) -> bool;
}

impl Out for Sink<'_> {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        Sink::row(self, time, model, tokens)
    }

    fn cancelled(&self) -> bool {
        Sink::cancelled(self)
    }
}

/// Gives out the time (unix seconds), model and tokens of each token_count event of bytes, in
/// order; looks now and then at whether the job was cancelled.
pub fn parse(bytes: &[u8], out: &mut dyn Out) -> Result<(), Error> {
    let mut total = Counter::default();
    let mut model: Option<String> = None;
    for (n, line) in bytes.split(|b| *b == b'\n').enumerate() {
        if n % CANCEL_EVERY == 0 && out.cancelled() {
            return Err(Error::cancelled());
        }
        let Ok(value) = serde_json::from_slice::<Value>(line) else {
            continue;
        };
        if value["type"] == "turn_context" {
            model = value["payload"]["model"].as_str().map(clean_model);
        }
        let Some((mut sample, own_model)) = event(&value) else {
            continue;
        };
        if value
            .pointer("/payload/info/total_token_usage")
            .is_some_and(Value::is_object)
        {
            let current = sample;
            sample = current.since(&total);
            total = current;
        }
        // A line with no readable time cannot be placed in an hour. Codex stamps every line.
        let Some(time) = timestamp(&value) else {
            continue;
        };
        let name = own_model.or_else(|| model.clone()).unwrap_or_default();
        out.row(time, &name, &sample.tokens())?;
    }
    Ok(())
}

/// The counters of one token_count event (the old reader's `UsageCounter`, less its cost).
#[derive(Clone, Copy, Default)]
struct Counter {
    input: u64,
    output: u64,
    cache_read: u64,
    cache_write: u64,
    reasoning: u64,
    total: u64,
}

impl Counter {
    /// What self grew by since earlier, field by field, not below zero.
    fn since(&self, earlier: &Counter) -> Counter {
        Counter {
            input: self.input.saturating_sub(earlier.input),
            output: self.output.saturating_sub(earlier.output),
            cache_read: self.cache_read.saturating_sub(earlier.cache_read),
            cache_write: self.cache_write.saturating_sub(earlier.cache_write),
            reasoning: self.reasoning.saturating_sub(earlier.reasoning),
            total: self.total.saturating_sub(earlier.total),
        }
    }

    fn tokens(&self) -> Tokens {
        Tokens {
            input: self.input,
            output: self.output,
            cache_read: self.cache_read,
            cache_write: self.cache_write,
            reasoning: self.reasoning,
            total: self.total,
            samples: 1,
        }
    }
}

/// A token_count event's counters and its own model name, if it is one.
fn event(value: &Value) -> Option<(Counter, Option<String>)> {
    let payload = value.get("payload")?.as_object()?;
    if payload.get("type").and_then(Value::as_str) != Some("token_count") {
        return None;
    }
    let info = payload.get("info").and_then(Value::as_object);
    let usage = info
        .and_then(|info| {
            info.get("total_token_usage")
                .and_then(Value::as_object)
                .or_else(|| info.get("last_token_usage").and_then(Value::as_object))
        })
        .or_else(|| payload.get("usage").and_then(Value::as_object))?;
    let mut c = counter(usage)?;
    c.input = c.input.saturating_sub(c.cache_read);
    if first(usage, &["totalTokens", "total_tokens"]).is_none() {
        c.total = c
            .input
            .saturating_add(c.output)
            .saturating_add(c.cache_write)
            .saturating_add(c.cache_read);
    }
    Some((c, model_name(usage)))
}

/// A usage object's counters, under the names the old reader knew; None when it has none.
fn counter(map: &Map<String, Value>) -> Option<Counter> {
    let input = first(
        map,
        &[
            "inputTokens",
            "input_tokens",
            "input",
            "inputOther",
            "prompt_tokens",
            "promptTokenCount",
        ],
    );
    let output = first(
        map,
        &[
            "outputTokens",
            "output_tokens",
            "output",
            "completion_tokens",
            "candidatesTokenCount",
        ],
    );
    let cache_write = first(
        map,
        &[
            "cacheCreationTokens",
            "cache_creation_input_tokens",
            "cache_write_input_tokens",
            "inputCacheCreation",
            "cache_write",
            "cache_write_tokens",
            "cacheWrite",
        ],
    )
    .or_else(|| nested(map, "cache", "write"));
    let cache_read = first(
        map,
        &[
            "cacheReadTokens",
            "cache_read_input_tokens",
            "cached_input_tokens",
            "inputCacheRead",
            "cache_read",
            "cache_read_tokens",
            "cacheRead",
            "cached",
            "cachedContentTokenCount",
        ],
    )
    .or_else(|| nested(map, "cache", "read"))
    .or_else(|| nested(map, "prompt_tokens_details", "cached_tokens"));
    let reasoning = first(
        map,
        &[
            "reasoningOutputTokens",
            "reasoningTokens",
            "reasoning_tokens",
            "reasoning_output_tokens",
            "reasoning",
            "thinking",
            "thinking_tokens",
            "thoughts",
            "thoughtsTokenCount",
        ],
    )
    .or_else(|| nested(map, "completion_tokens_details", "reasoning_tokens"));
    if input.is_none()
        && output.is_none()
        && cache_write.is_none()
        && cache_read.is_none()
        && reasoning.is_none()
        && !map.contains_key("totalTokens")
        && !map.contains_key("total_tokens")
    {
        return None;
    }
    let mut input_tokens = input.unwrap_or(0);
    if map.contains_key("prompt_tokens")
        || map.contains_key("promptTokenCount")
        || map.contains_key("cached")
    {
        input_tokens = input_tokens.saturating_sub(cache_read.unwrap_or(0));
    }
    let output_tokens = output
        .unwrap_or(0)
        .saturating_add(first(map, &["thoughts", "thoughtsTokenCount"]).unwrap_or(0));
    let (cache_write, cache_read) = (cache_write.unwrap_or(0), cache_read.unwrap_or(0));
    let known = input_tokens
        .saturating_add(output_tokens)
        .saturating_add(cache_write)
        .saturating_add(cache_read);
    Some(Counter {
        input: input_tokens,
        output: output_tokens,
        cache_read,
        cache_write,
        reasoning: reasoning.unwrap_or(0),
        total: first(
            map,
            &["totalTokens", "total_tokens", "total", "totalTokenCount"],
        )
        .unwrap_or(known),
    })
}

/// The first of keys that holds a count.
fn first(map: &Map<String, Value>, keys: &[&str]) -> Option<u64> {
    keys.iter().find_map(|key| map.get(*key).and_then(count))
}

fn nested(map: &Map<String, Value>, object: &str, key: &str) -> Option<u64> {
    map.get(object)
        .and_then(Value::as_object)
        .and_then(|o| first(o, &[key]))
}

/// A whole number from 0 up, as an integer or an integral float.
fn count(value: &Value) -> Option<u64> {
    if let Some(n) = value.as_u64() {
        return Some(n);
    }
    let n = value.as_f64()?;
    (n.is_finite() && n >= 0.0 && n.fract() == 0.0 && n <= u64::MAX as f64).then_some(n as u64)
}

/// A usage object's own model name (a string, or an object with an `id` or `model`).
fn model_name(map: &Map<String, Value>) -> Option<String> {
    ["model", "modelName"]
        .iter()
        .find_map(|key| {
            map.get(*key).and_then(|v| {
                v.as_str()
                    .or_else(|| v.get("id").and_then(Value::as_str))
                    .or_else(|| v.get("model").and_then(Value::as_str))
            })
        })
        .map(clean_model)
        .filter(|name| !name.is_empty())
}

/// A model name without control characters, cut to `MAX_MODEL_NAME` characters.
fn clean_model(name: &str) -> String {
    name.chars()
        .filter(|c| !c.is_control())
        .take(MAX_MODEL_NAME)
        .collect()
}

/// A line's time in unix seconds: the first of `timestamp`, `time`, `created_at`, `createdAt` and
/// `date` that reads as one (an RFC 3339 time, a `YYYY-MM-DD` date as that day's start in UTC, or
/// a number of seconds or, past 10^10, of milliseconds, also as `{"created": …}`), else the same
/// in `message`, `payload` or `data`.
fn timestamp(value: &Value) -> Option<i64> {
    let object = value.as_object()?;
    for key in ["timestamp", "time", "created_at", "createdAt", "date"] {
        let Some(field) = object.get(key) else {
            continue;
        };
        if let Some(text) = field.as_str() {
            if let Some(t) = rfc3339(text).or_else(|| date(text)) {
                return Some(t);
            }
        }
        let inner = field.get("created").unwrap_or(field);
        if let Some(n) = inner.as_f64().filter(|n| n.is_finite()) {
            let millis = if n.abs() > 10_000_000_000.0 {
                n
            } else {
                n * 1000.0
            };
            if millis >= i64::MIN as f64 && millis <= i64::MAX as f64 {
                return Some((millis as i64).div_euclid(1000));
            }
        }
    }
    ["message", "payload", "data"]
        .iter()
        .find_map(|key| object.get(*key).and_then(timestamp))
}

/// `YYYY-MM-DDTHH:MM:SS[.fraction]` then `Z` or `±HH:MM`, as unix seconds.
fn rfc3339(text: &str) -> Option<i64> {
    let b = text.as_bytes();
    let num = |from: usize, len: usize| -> Option<i64> {
        let digits = b.get(from..from + len)?;
        digits
            .iter()
            .all(u8::is_ascii_digit)
            .then(|| digits.iter().fold(0, |n, d| n * 10 + i64::from(d - b'0')))
    };
    let at = |i: usize, c: &[u8]| b.get(i).is_some_and(|x| c.contains(x));
    if !(at(4, b"-") && at(7, b"-") && at(10, b"Tt ") && at(13, b":") && at(16, b":")) {
        return None;
    }
    let days = days(num(0, 4)?, num(5, 2)?, num(8, 2)?)?;
    let (h, m, s) = (num(11, 2)?, num(14, 2)?, num(17, 2)?);
    if h > 23 || m > 59 || s > 60 {
        return None;
    }
    let mut i = 19;
    if at(i, b".") {
        i += 1;
        let start = i;
        while b.get(i).is_some_and(u8::is_ascii_digit) {
            i += 1;
        }
        if i == start {
            return None;
        }
    }
    let offset = if at(i, b"Zz") && i + 1 == b.len() {
        0
    } else if at(i, b"+-") && i + 6 == b.len() && at(i + 3, b":") {
        let (oh, om) = (num(i + 1, 2)?, num(i + 4, 2)?);
        if oh > 23 || om > 59 {
            return None;
        }
        let east = (oh * 60 + om) * 60;
        if b[i] == b'+' {
            east
        } else {
            -east
        }
    } else {
        return None;
    };
    Some(days * 86400 + h * 3600 + m * 60 + s - offset)
}

/// `YYYY-MM-DD` as the start of that day in UTC.
fn date(text: &str) -> Option<i64> {
    let b = text.as_bytes();
    if b.len() != 10 || b[4] != b'-' || b[7] != b'-' {
        return None;
    }
    let part = |r: std::ops::Range<usize>| text.get(r)?.parse::<i64>().ok();
    Some(days(part(0..4)?, part(5..7)?, part(8..10)?)? * 86400)
}

/// Days from 1970-01-01 to a date of the civil calendar; None for a date that does not exist.
fn days(year: i64, month: i64, day: i64) -> Option<i64> {
    let leap = (year % 4 == 0 && year % 100 != 0) || year % 400 == 0;
    let length = match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if leap => 29,
        2 => 28,
        _ => return None,
    };
    if !(1..=length).contains(&day) {
        return None;
    }
    // Howard Hinnant's days_from_civil.
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let doy = (153 * (if month > 2 { month - 3 } else { month + 9 }) + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    Some(era * 146_097 + doe - 719_468)
}
