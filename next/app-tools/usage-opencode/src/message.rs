//! An OpenCode message as a token row, in each of the shapes OpenCode has stored one:
//!
//! - its JSON store (`storage/message/<session>/<id>.json`, before its database) and its first
//!   database table, `message`: an assistant message (`role` `assistant`) with `modelID` and
//!   `tokens`;
//! - its current table, `session_message`: an `assistant` row (`model.id` and `tokens`), or a
//!   completed `compaction` row, which carries the compaction request's own `tokens` and `model`.
//!
//! Every message is one event, counted once across files by its key, a hash of its message and
//! session IDs: OpenCode's migrations from the JSON store to the database, and from the `message`
//! table to `session_message`, keep both IDs, so a message stored more than one way counts once.
//!
//! OpenCode's `tokens` are `{input, output, reasoning, cache: {read, write}}`, input without cache
//! reads and writes, output the visible output without reasoning (OpenCode's
//! `packages/core/src/session/usage.ts`). In the hub's terms reasoning is inside output, so output is
//! output + reasoning, and the total is input + output + reasoning + cache read + cache write,
//! OpenCode's own total (`packages/schema/src/token-usage.ts`); a provider total some older messages
//! carry is not used, so each of a message's shapes gives the same counts. A time is the message's
//! `time.created`. Only counters, a model name, a time and the IDs hashed into a key are read.

use crate::hash;
use pluxy_program::Error;
use pluxy_usage::fields::{clean_model, count, timestamp};
use pluxy_usage::{Sink, Tokens, MAX_COUNT};
use serde_json::{Map, Value};

/// Where rows go: a usage sink, or a list in a test.
pub trait Out {
    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error>;
    /// Sets the revision the rows are for (a session of a database), before any row is sent.
    fn revise(&mut self, size: u64, mtime_ns: i64) -> Result<(), Error>;
    /// Whether the job was cancelled.
    fn cancelled(&self) -> bool;
}

impl Out for Sink<'_> {
    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error> {
        Sink::event(self, time, model, key, tokens)
    }

    fn revise(&mut self, size: u64, mtime_ns: i64) -> Result<(), Error> {
        Sink::revise(self, size, mtime_ns)
    }

    fn cancelled(&self) -> bool {
        Sink::cancelled(self)
    }
}

/// A message's counted usage: its time (unix seconds), model and tokens.
#[derive(Debug, PartialEq, Eq)]
pub struct Usage {
    pub time: i64,
    pub model: String,
    pub tokens: Tokens,
}

/// The key of the message id of session: the same in every shape OpenCode stores it in.
pub fn key(id: &str, session: &str) -> String {
    hash(&["opencode", id, session])
}

/// The tokens of an OpenCode `tokens` object, as the hub counts them; None when it is not one.
pub fn tokens(t: &Map<String, Value>) -> Option<Tokens> {
    let n = |v: Option<&Value>| v.and_then(count).unwrap_or(0).min(MAX_COUNT);
    let cache = t.get("cache").and_then(Value::as_object);
    let input = n(t.get("input"));
    let output = n(t.get("output"));
    let reasoning = n(t.get("reasoning"));
    let cache_read = n(cache.and_then(|c| c.get("read")));
    let cache_write = n(cache.and_then(|c| c.get("write")));
    let output = output.saturating_add(reasoning).min(MAX_COUNT);
    let total = [input, output, cache_read, cache_write]
        .iter()
        .fold(0u64, |a, b| a.saturating_add(*b))
        .min(MAX_COUNT);
    Some(Tokens {
        input,
        output,
        cache_read,
        cache_write,
        reasoning,
        total,
        samples: 1,
    })
}

/// A message's model: `modelID` (the JSON store and the `message` table) or `model.id`
/// (`session_message`).
fn model(data: &Map<String, Value>) -> String {
    data.get("modelID")
        .and_then(Value::as_str)
        .or_else(|| {
            data.get("model")
                .and_then(|m| m.get("id"))
                .and_then(Value::as_str)
        })
        .map(clean_model)
        .unwrap_or_default()
}

/// The usage of a message's data when it carries tokens, at its `time.created`, else at time (the
/// database row's own, in milliseconds); None for a message with no tokens or no time.
pub fn usage(data: &Value, time: Option<i64>) -> Option<Usage> {
    let object = data.as_object()?;
    let tokens = tokens(object.get("tokens")?.as_object()?)?;
    let time = timestamp(data).or_else(|| time.map(|ms| ms.div_euclid(1000)))?;
    Some(Usage {
        time,
        model: model(object),
        tokens,
    })
}

/// Whether data (from the JSON store or the `message` table) is an assistant message.
pub fn is_assistant(data: &Value) -> bool {
    data.get("role").and_then(Value::as_str) == Some("assistant")
}

/// A JSON message file's message, if an assistant one with tokens, keyed by the message and session
/// IDs its file and folder are named after (OpenCode's `json-migration.ts` took them from there).
pub fn json_file(value: &Value, id: &str, session: &str, out: &mut dyn Out) -> Result<(), Error> {
    if !is_assistant(value) {
        return Ok(());
    }
    let Some(u) = usage(value, None) else {
        return Ok(());
    };
    out.event(u.time, &u.model, &key(id, session), &u.tokens)
}
