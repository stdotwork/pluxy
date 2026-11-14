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
use pluxy_usage::fields::{clean_model, counts, first, model_name, timestamp};
use pluxy_usage::{LineReader, Sink, Tokens};
use serde_json::Value;

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

/// Gives out the time (unix seconds), model and tokens of each token_count event of a file's
/// lines, in order; looks now and then at whether the job was cancelled.
pub fn parse(lines: &mut LineReader, out: &mut dyn Out) -> Result<(), Error> {
    let mut total = Tokens::default();
    let mut model: Option<String> = None;
    let mut n = 0usize;
    while let Some(line) = lines.next_line()? {
        n += 1;
        if n % CANCEL_EVERY == 1 && out.cancelled() {
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
            sample = since(&current, &total);
            total = current;
        }
        // A line with no readable time cannot be placed in an hour. Codex stamps every line.
        let Some(time) = timestamp(&value) else {
            continue;
        };
        let name = own_model.or_else(|| model.clone()).unwrap_or_default();
        out.row(time, &name, &sample)?;
    }
    Ok(())
}

/// What now grew by since earlier, field by field, not below zero: one sample.
fn since(now: &Tokens, earlier: &Tokens) -> Tokens {
    Tokens {
        input: now.input.saturating_sub(earlier.input),
        output: now.output.saturating_sub(earlier.output),
        cache_read: now.cache_read.saturating_sub(earlier.cache_read),
        cache_write: now.cache_write.saturating_sub(earlier.cache_write),
        reasoning: now.reasoning.saturating_sub(earlier.reasoning),
        total: now.total.saturating_sub(earlier.total),
        samples: 1,
    }
}

/// A token_count event's counters and its own model name, if it is one.
fn event(value: &Value) -> Option<(Tokens, Option<String>)> {
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
    let mut c = counts(usage)?;
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
