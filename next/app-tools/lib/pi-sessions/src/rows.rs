//! A Pi or Oh My Pi session file (`<time>_<id>.jsonl`, one JSON object a line) as token rows.
//!
//! Both write the same shape of usage, by their sources (`packages/ai/src/types.ts` of Pi,
//! `packages/catalog/src/types.ts` of Oh My Pi): `input` (already net of what was read from or written to
//! the cache), `output`, `cacheRead`, `cacheWrite`, `totalTokens`, and the reasoning tokens as `reasoning`
//! (Pi) or `reasoningTokens` (Oh My Pi), which are part of `output` and never added to it. One entry is
//! one record; the rules, by the old desktop app's reading of the same files:
//!
//! - an entry of `type` `message` has its usage in `message.usage` (an assistant message; a Pi tool
//!   result that did nested model work); an entry of `type` `usage` (Pi), `model_usage` (Oh My Pi),
//!   `compaction` or `branch_summary` (Pi) has it in its own `usage`. Nothing else is read: Oh My Pi
//!   keeps a `task` result's child usage in `message.details.usage`, and the child has its own file;
//! - the total is the entry's own `totalTokens`, else input + output + cache read + cache write;
//! - the model is `message.responseModel` (the model that answered), else `message.model`, for a message;
//!   `model` for another entry; a name that is not there is the empty name;
//! - the time is the entry's `timestamp` (an ISO time), else the message's (a number of milliseconds); an
//!   entry with neither readable is skipped (the old reader used the file's time, which a parser is not
//!   given);
//! - every entry with a usage object that holds a count is one sample, also one that adds nothing;
//! - an entry with an `id` and a `timestamp` is an event (schema 2) with a key made from the two: a
//!   session a tool forks or branches is a new file with copies of the parent's entries, `id`s and
//!   times unchanged, and the hub counts an event once whichever files hold it. A key is sent once per
//!   file. An entry without either is a plain row, as at schema 1.
//!
//! Only counters, a model name and a time are read. A prompt, a message, a title, a folder or a key is
//! never looked at beyond finding those fields, and none is kept.

use crate::hash;
use pluxy_program::Error;
use pluxy_usage::{LineReader, Sink, Tokens};
use serde_json::{Map, Value};
use std::collections::HashSet;

/// The longest model name kept, in characters.
const MAX_MODEL_NAME: usize = 120;
/// Lines between looks at whether the job was cancelled.
const CANCEL_EVERY: usize = 2048;

/// Where the rows go: a usage sink, or a list in a test.
pub trait Out {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error>;
    /// One event, counted once by its key across files.
    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error>;
    /// Whether the job was cancelled.
    fn cancelled(&self) -> bool;
}

impl Out for Sink<'_> {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        Sink::row(self, time, model, tokens)
    }

    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error> {
        Sink::event(self, time, model, key, tokens)
    }

    fn cancelled(&self) -> bool {
        Sink::cancelled(self)
    }
}

/// Gives out the time (unix seconds), model and tokens of each usage entry of a file's lines, in order;
/// looks now and then at whether the job was cancelled.
pub fn parse(lines: &mut LineReader, out: &mut dyn Out) -> Result<(), Error> {
    let mut n = 0usize;
    let mut seen = HashSet::new();
    while let Some(line) = lines.next_line()? {
        n += 1;
        if n % CANCEL_EVERY == 1 && out.cancelled() {
            return Err(Error::cancelled());
        }
        let Ok(value) = serde_json::from_slice::<Value>(line) else {
            continue;
        };
        let Some((usage, model, message)) = usage_of(&value) else {
            continue;
        };
        let Some(tokens) = tokens(usage) else {
            continue;
        };
        let Some(time) = time(&value, message) else {
            continue;
        };
        match key(&value) {
            // An entry a fork or branch copied into another file has the same key there: sent once
            // per file, counted once by the hub.
            Some(key) if !seen.insert(key.clone()) => {}
            Some(key) => out.event(time, &model, &key, &tokens)?,
            None => out.row(time, &model, &tokens)?,
        }
    }
    Ok(())
}

/// An entry's key: the hash of its `id` and `timestamp`, which a fork or branch keeps when it copies the
/// entry into a new file. The `id` alone is 8 random hex digits, unique within one session only, so the
/// time to the millisecond is part of it. An entry without either is no event.
fn key(entry: &Value) -> Option<String> {
    let field = |name: &str| {
        entry
            .get(name)
            .and_then(Value::as_str)
            .filter(|s| !s.is_empty())
    };
    Some(hash(&["entry", field("id")?, field("timestamp")?]))
}

/// An entry's usage object, its model name, and its message when it is one.
fn usage_of(entry: &Value) -> Option<(&Map<String, Value>, String, Option<&Value>)> {
    match entry.get("type")?.as_str()? {
        "message" => {
            let message = entry.get("message")?;
            let usage = message.get("usage")?.as_object()?;
            let model = ["responseModel", "model"]
                .iter()
                .find_map(|key| message.get(*key).and_then(Value::as_str))
                .map(clean_model)
                .filter(|name| !name.is_empty());
            Some((usage, model.unwrap_or_default(), Some(message)))
        }
        "usage" | "model_usage" | "compaction" | "branch_summary" => {
            let usage = entry.get("usage")?.as_object()?;
            let model = entry
                .get("model")
                .and_then(Value::as_str)
                .map(clean_model)
                .unwrap_or_default();
            Some((usage, model, None))
        }
        _ => None,
    }
}

/// A usage object's counters, None when it holds none.
fn tokens(usage: &Map<String, Value>) -> Option<Tokens> {
    let get = |key: &str| usage.get(key).and_then(count);
    let reasoning = get("reasoning").or_else(|| get("reasoningTokens"));
    let own_total = get("totalTokens");
    if get("input").is_none()
        && get("output").is_none()
        && get("cacheRead").is_none()
        && get("cacheWrite").is_none()
        && reasoning.is_none()
        && own_total.is_none()
    {
        return None;
    }
    let (input, output) = (get("input").unwrap_or(0), get("output").unwrap_or(0));
    let (cache_read, cache_write) = (
        get("cacheRead").unwrap_or(0),
        get("cacheWrite").unwrap_or(0),
    );
    let sum = input
        .saturating_add(output)
        .saturating_add(cache_read)
        .saturating_add(cache_write);
    Some(Tokens {
        input,
        output,
        cache_read,
        cache_write,
        reasoning: reasoning.unwrap_or(0),
        total: own_total.unwrap_or(sum),
        samples: 1,
    })
}

/// A whole number from 0 up, as an integer or an integral float.
fn count(value: &Value) -> Option<u64> {
    if let Some(n) = value.as_u64() {
        return Some(n);
    }
    let n = value.as_f64()?;
    (n.is_finite() && n >= 0.0 && n.fract() == 0.0 && n <= u64::MAX as f64).then_some(n as u64)
}

/// A model name without control characters, cut to `MAX_MODEL_NAME` characters.
fn clean_model(name: &str) -> String {
    name.chars()
        .filter(|c| !c.is_control())
        .take(MAX_MODEL_NAME)
        .collect()
}

/// An entry's time in unix seconds: its `timestamp`, else its message's.
fn time(entry: &Value, message: Option<&Value>) -> Option<i64> {
    entry
        .get("timestamp")
        .and_then(stamp)
        .or_else(|| message?.get("timestamp").and_then(stamp))
}

/// A time as an RFC 3339 string, a `YYYY-MM-DD` date (that day's start in UTC), or a number of seconds
/// or, past 10^10, of milliseconds.
fn stamp(field: &Value) -> Option<i64> {
    if let Some(text) = field.as_str() {
        return rfc3339(text).or_else(|| date(text));
    }
    let n = field.as_f64().filter(|n| n.is_finite())?;
    let millis = if n.abs() > 10_000_000_000.0 {
        n
    } else {
        n * 1000.0
    };
    (millis >= i64::MIN as f64 && millis <= i64::MAX as f64)
        .then(|| (millis as i64).div_euclid(1000))
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
