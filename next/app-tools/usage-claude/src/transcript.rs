//! A Claude Code session transcript (`<session>.jsonl`, one JSON object a line) as token rows, by
//! the rules of the old desktop app's Claude reader:
//!
//! - a line's usage is its `message.usage` (or, with no `message` object, its own `usage`), read
//!   under the field names the shared readers know (`pluxy_usage::fields`); its model is the
//!   message's (or the line's) `model`, and `<synthetic>` (Claude Code's own placeholder) is none;
//! - Claude Code writes one line per content block of a response, each with the response's
//!   `message.id` (and `requestId`) and usage, so the lines with one message ID (and request ID,
//!   when they have one) are one message: the last line wins, at its own time; an empty message
//!   ID is none;
//! - a message is an event, counted once across files by a key (schema 2): a hash of its ID and
//!   its `requestId`, else of its ID and its `sessionId`; with neither it is a plain row of the
//!   file;
//! - a line with usage and no message ID counts as it is, and so does each item of a line that is
//!   a list (or holds a `messages` list);
//! - every message or line counted is one sample; a time is the line's `timestamp` (a line with
//!   no readable time is skipped; the old reader used the file's modification time);
//! - a file's messages are held, one small record each, until the file is read; a file of more
//!   than `MAX_MESSAGES` fails (the hub keeps no more rows of one file).
//!
//! Only counters, a model name, a time and the IDs hashed into a key are read. A prompt, a reply,
//! a folder or a raw ID is never kept or sent.

use crate::hash;
use pluxy_program::Error;
use pluxy_usage::fields::{counts, model_name, timestamp};
use pluxy_usage::{LineReader, Sink, Tokens};
use serde_json::Value;
use std::collections::HashMap;

/// Lines between looks at whether the job was cancelled.
const CANCEL_EVERY: usize = 2048;
/// The model Claude Code names its own placeholder messages with.
const SYNTHETIC: &str = "<synthetic>";
/// Distinct messages a file may have: the hub's rows of one file at most.
pub const MAX_MESSAGES: usize = 100_000;

/// Where the rows go: a usage sink, or a list in a test.
pub trait Out {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error>;
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

/// A message's last line: its time (none when that line has no readable one), model, tokens and
/// key.
struct Message {
    time: Option<i64>,
    model: String,
    tokens: Tokens,
    key: Option<String>,
}

/// Gives out a file's messages (in the order they first appear) and its other usage lines; looks
/// now and then at whether the job was cancelled.
pub fn parse(lines: &mut LineReader, out: &mut dyn Out) -> Result<(), Error> {
    let mut messages: Vec<Message> = Vec::new();
    // A message is its ID with its request ID, when it has one, as its key is.
    let mut by_id: HashMap<(String, String), usize> = HashMap::new();
    let mut n = 0usize;
    while let Some(line) = lines.next_line()? {
        n += 1;
        if n % CANCEL_EVERY == 1 && out.cancelled() {
            return Err(Error::cancelled());
        }
        let Ok(value) = serde_json::from_slice::<Value>(line) else {
            continue;
        };
        if let (Some((tokens, model)), Some(id)) = (
            usage(&value),
            value
                .pointer("/message/id")
                .and_then(Value::as_str)
                .filter(|id| !id.is_empty()),
        ) {
            let message = Message {
                time: timestamp(&value),
                model,
                tokens,
                key: key(&value, id),
            };
            let at = (
                id.to_string(),
                field(&value, "requestId").unwrap_or_default().to_string(),
            );
            match by_id.get(&at) {
                Some(&i) => messages[i] = message,
                None if messages.len() == MAX_MESSAGES => {
                    return Err(Error::internal(format!(
                        "more than {MAX_MESSAGES} messages, more rows than the hub keeps of a file"
                    )));
                }
                None => {
                    by_id.insert(at, messages.len());
                    messages.push(message);
                }
            }
            continue;
        }
        plain(&value, out)?;
    }
    for m in messages {
        let Some(time) = m.time else {
            continue;
        };
        match &m.key {
            Some(key) => out.event(time, &m.model, key, &m.tokens)?,
            None => out.row(time, &m.model, &m.tokens)?,
        }
    }
    Ok(())
}

/// A line counted as it is: each item of a list (or of its `messages` list), else its usage.
fn plain(value: &Value, out: &mut dyn Out) -> Result<(), Error> {
    if let Some(items) = value
        .as_array()
        .or_else(|| value.get("messages").and_then(Value::as_array))
    {
        for item in items {
            plain(item, out)?;
        }
        return Ok(());
    }
    let (Some((tokens, model)), Some(time)) = (usage(value), timestamp(value)) else {
        return Ok(());
    };
    out.row(time, &model, &tokens)
}

/// A line's usage and model: its message's when it has a `message` object, else its own.
fn usage(value: &Value) -> Option<(Tokens, String)> {
    let object = value.as_object()?;
    let holder = match object.get("message") {
        Some(Value::Object(message)) => message,
        _ => object,
    };
    let tokens = counts(holder.get("usage")?.as_object()?)?;
    let model = model_name(holder)
        .filter(|m| m != SYNTHETIC)
        .unwrap_or_default();
    Some((tokens, model))
}

/// A line's string field name, when it is not empty.
fn field<'a>(value: &'a Value, name: &str) -> Option<&'a str> {
    value
        .get(name)
        .and_then(Value::as_str)
        .filter(|s| !s.is_empty())
}

/// A message's key: the hash of its ID and request ID, else of its ID and session ID.
fn key(value: &Value, id: &str) -> Option<String> {
    if let Some(request) = field(value, "requestId") {
        Some(hash(&["request", id, request]))
    } else {
        field(value, "sessionId").map(|session| hash(&["session", id, session]))
    }
}
