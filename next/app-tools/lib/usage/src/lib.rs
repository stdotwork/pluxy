//! Schema 1 of kind `pluxy/agent-usage/usage`, the usage hub's versioned ingest API
//! (`../../agent-usage/README.md`, "Schema 1"), and what a usage parser shares: finding its
//! source files, reading one safely and streaming its rows as a job's batches.
//!
//! A parser implements [`Parser`] and answers its produces operation with [`produce`]:
//!
//! ```no_run
//! use pluxy_program::{run, Call, Error, Handler};
//! use pluxy_usage::{produce, Parser, Sink, Tokens};
//! use std::path::{Path, PathBuf};
//!
//! struct Lines;
//! impl Parser for Lines {
//!     fn label(&self) -> &str {
//!         "Lines"
//!     }
//!     fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
//!         vec![home.join(".lines")]
//!     }
//!     fn wanted(&self, path: &Path) -> bool {
//!         path.extension().is_some_and(|e| e == "log")
//!     }
//!     fn parse(&self, bytes: &[u8], sink: &mut Sink) -> Result<(), Error> {
//!         let t = Tokens { input: bytes.len() as u64, total: bytes.len() as u64, samples: 1, ..Tokens::default() };
//!         sink.row(0, "model", &t)
//!     }
//! }
//!
//! struct Tool;
//! impl Handler for Tool {
//!     fn call(&self, call: &Call) -> Result<serde_json::Value, Error> {
//!         produce(call, &Lines)
//!     }
//! }
//!
//! fn main() -> std::io::Result<()> {
//!     run(Tool)
//! }
//! ```

use pluxy_program::{Call, Error};
use serde_json::{json, Map, Value};
use std::collections::{BTreeMap, HashSet};
use std::io::{BufRead, Read};
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path, PathBuf};

/// The kind the hub consumes, and the schema this crate speaks.
pub const KIND: &str = "pluxy/agent-usage/usage";
pub const SCHEMA: u64 = 1;
/// Files a `files` job lists at most per source; the newest are kept.
pub const MAX_FILES: usize = 20_000;
/// Folders below a root a source file may be at most.
pub const MAX_DEPTH: usize = 8;
/// The largest source file read, in bytes.
pub const MAX_DOCUMENT: u64 = 16 << 20;
/// The longest line `LineReader` gives; a longer one is skipped.
pub const MAX_LINE: u64 = 16 << 20;
/// Roots a job may name, and the longest root, file ID and model name.
pub const MAX_ROOTS: usize = 16;
pub const MAX_PATH: usize = 4096;
pub const MAX_MODEL: usize = 256;
/// File IDs a `read` job may name.
pub const MAX_READ: usize = 2000;
/// The largest count a token field may hold: JSON numbers stay exact up to here.
pub const MAX_COUNT: u64 = (1 << 53) - 1;
/// Directory entries a scan looks at before it stops, marking the listing truncated.
const MAX_VISITS: usize = 200_000;
/// A part's estimated size at which it is sent; the batch limit is `pluxy_program::MAX_BATCH`.
const PART_BUDGET: usize = 40 << 10;

/// Token counts of one row, or a sum of rows. `total` is the source's own total (by the old
/// app's rule: input + output + cache read + cache write when the source gives none; reasoning
/// is inside output, never added); `samples` counts the records summed.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Tokens {
    pub input: u64,
    pub output: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub reasoning: u64,
    pub total: u64,
    pub samples: u64,
}

/// The token fields, in the order rows and sums carry them.
pub const FIELDS: [&str; 7] = [
    "input",
    "output",
    "cache_read",
    "cache_write",
    "reasoning",
    "total",
    "samples",
];

impl Tokens {
    pub fn values(&self) -> [u64; 7] {
        [
            self.input,
            self.output,
            self.cache_read,
            self.cache_write,
            self.reasoning,
            self.total,
            self.samples,
        ]
    }

    pub fn from_values(v: [u64; 7]) -> Tokens {
        Tokens {
            input: v[0],
            output: v[1],
            cache_read: v[2],
            cache_write: v[3],
            reasoning: v[4],
            total: v[5],
            samples: v[6],
        }
    }

    /// Adds other, saturating at `MAX_COUNT`.
    pub fn add(&mut self, other: &Tokens) {
        let (a, b) = (self.values(), other.values());
        *self = Tokens::from_values(std::array::from_fn(|i| {
            a[i].saturating_add(b[i]).min(MAX_COUNT)
        }));
    }

    pub fn is_zero(&self) -> bool {
        self.values() == [0; 7]
    }

    /// The fields into o, by name.
    pub fn put(&self, o: &mut Map<String, Value>) {
        for (k, v) in FIELDS.iter().zip(self.values()) {
            o.insert((*k).into(), json!(v));
        }
    }

    pub fn to_json(&self) -> Value {
        let mut o = Map::new();
        self.put(&mut o);
        Value::Object(o)
    }

    /// From an object holding every field as an integer from 0 to `MAX_COUNT`.
    pub fn from_json(v: &Value) -> Result<Tokens, String> {
        let mut out = [0u64; 7];
        for (i, k) in FIELDS.iter().enumerate() {
            out[i] = v[*k]
                .as_u64()
                .filter(|n| *n <= MAX_COUNT)
                .ok_or_else(|| format!("{k} is not a count"))?;
        }
        Ok(Tokens::from_values(out))
    }
}

/// One row of a `read` batch: an hour (unix seconds of its start, UTC), a model and its tokens.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Row {
    pub hour: i64,
    pub model: String,
    pub tokens: Tokens,
}

/// The latest hour a row may name (the start of the year 3000, UTC).
pub const MAX_HOUR: i64 = 32_503_680_000;

impl Row {
    pub fn to_json(&self) -> Value {
        let mut o = Map::new();
        o.insert("hour".into(), json!(self.hour));
        o.insert("model".into(), json!(self.model));
        self.tokens.put(&mut o);
        Value::Object(o)
    }

    /// From a batch's row: an hour from 0 to `MAX_HOUR`, a whole hour; a model of at most
    /// `MAX_MODEL` characters; every token field.
    pub fn from_json(v: &Value) -> Result<Row, String> {
        let hour = v["hour"]
            .as_i64()
            .filter(|h| (0..=MAX_HOUR).contains(h) && h % 3600 == 0)
            .ok_or("hour is not the start of an hour")?;
        let model = v["model"]
            .as_str()
            .filter(|m| m.chars().count() <= MAX_MODEL)
            .ok_or("model is not a short string")?
            .to_string();
        Ok(Row {
            hour,
            model,
            tokens: Tokens::from_json(v)?,
        })
    }
}

/// A source file a scan found: its path, size and modification time in nanoseconds.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Found {
    pub path: PathBuf,
    pub size: u64,
    pub mtime_ns: i64,
}

fn mtime_ns(m: &std::fs::Metadata) -> i64 {
    m.mtime()
        .saturating_mul(1_000_000_000)
        .saturating_add(m.mtime_nsec())
}

/// The files under roots that wanted accepts: regular files at most `MAX_DEPTH` folders below a
/// root, reached without following a link (links are skipped, the roots themselves excepted),
/// newest first (then by path), at most `MAX_FILES`; and whether that cut any.
pub fn scan(
    roots: &[PathBuf],
    wanted: &dyn Fn(&Path) -> bool,
    cancelled: &dyn Fn() -> bool,
) -> Result<(Vec<Found>, bool), Error> {
    let mut found = Vec::new();
    let mut seen = HashSet::new();
    let mut visits = 0usize;
    let mut truncated = false;
    for root in roots {
        let mut stack = vec![(root.clone(), 0usize)];
        while let Some((dir, depth)) = stack.pop() {
            if cancelled() {
                return Err(Error::cancelled());
            }
            let Ok(entries) = std::fs::read_dir(&dir) else {
                continue;
            };
            for e in entries.flatten() {
                visits += 1;
                if visits > MAX_VISITS {
                    truncated = true;
                    break;
                }
                let path = e.path();
                let Ok(m) = std::fs::symlink_metadata(&path) else {
                    continue;
                };
                if m.is_dir() && depth < MAX_DEPTH {
                    stack.push((path, depth + 1));
                } else if m.is_file() && wanted(&path) && seen.insert(path.clone()) {
                    found.push(Found {
                        path,
                        size: m.len(),
                        mtime_ns: mtime_ns(&m),
                    });
                }
            }
        }
    }
    found.sort_by(|a, b| b.mtime_ns.cmp(&a.mtime_ns).then(a.path.cmp(&b.path)));
    if found.len() > MAX_FILES {
        found.truncate(MAX_FILES);
        truncated = true;
    }
    Ok((found, truncated))
}

/// The path of file ID id when it is a source file a scan of roots would find: an absolute path
/// under a root, at most `MAX_DEPTH` folders below it, no part of it below the root a link, a
/// regular file that wanted accepts.
pub fn locate(roots: &[PathBuf], id: &str, wanted: &dyn Fn(&Path) -> bool) -> Option<PathBuf> {
    let path = Path::new(id);
    if !path.is_absolute()
        || id.len() > MAX_PATH
        || path
            .components()
            .any(|c| matches!(c, Component::ParentDir | Component::CurDir))
    {
        return None;
    }
    let root = roots.iter().find(|r| path.starts_with(r))?;
    let rest: Vec<_> = path.strip_prefix(root).ok()?.components().collect();
    if rest.is_empty() || rest.len() > MAX_DEPTH + 1 {
        return None;
    }
    let mut at = root.clone();
    for (i, c) in rest.iter().enumerate() {
        at.push(c);
        let m = std::fs::symlink_metadata(&at).ok()?;
        let last = i + 1 == rest.len();
        if m.file_type().is_symlink() || (last && !m.is_file()) || (!last && !m.is_dir()) {
            return None;
        }
    }
    wanted(&at).then_some(at)
}

/// The file found at path, opened, with its size and modification time: it must still be that
/// regular file (not replaced by a link meanwhile).
pub fn open_file(path: &Path) -> Result<(std::fs::File, u64, i64), String> {
    let before = std::fs::symlink_metadata(path).map_err(|e| e.to_string())?;
    if !before.is_file() {
        return Err("not a regular file".into());
    }
    let f = std::fs::File::open(path).map_err(|e| e.to_string())?;
    let m = f.metadata().map_err(|e| e.to_string())?;
    if (m.dev(), m.ino()) != (before.dev(), before.ino()) || !m.is_file() {
        return Err("the file changed while it was opened".into());
    }
    Ok((f, m.len(), mtime_ns(&m)))
}

/// A source file's bytes, size and modification time, read from the file found at path (see
/// `open_file`), at most `MAX_DOCUMENT` bytes.
pub fn read_file(path: &Path) -> Result<(Vec<u8>, u64, i64), String> {
    let (f, size, mtime) = open_file(path)?;
    if size > MAX_DOCUMENT {
        return Err(format!("larger than {MAX_DOCUMENT} bytes"));
    }
    let mut bytes = Vec::with_capacity(size as usize);
    f.take(MAX_DOCUMENT + 1)
        .read_to_end(&mut bytes)
        .map_err(|e| e.to_string())?;
    if bytes.len() as u64 > MAX_DOCUMENT {
        return Err(format!("larger than {MAX_DOCUMENT} bytes"));
    }
    Ok((bytes, size, mtime))
}

/// The lines of a file one at a time, with no line end, for a parser of a line-based format
/// that reads a file of any size: only the longest line is in memory. A line over `MAX_LINE`
/// bytes is skipped and counted.
pub struct LineReader<'a> {
    reader: Box<dyn BufRead + 'a>,
    line: Vec<u8>,
    skipped: u64,
}

impl<'a> LineReader<'a> {
    pub fn new(reader: impl BufRead + 'a) -> LineReader<'a> {
        LineReader {
            reader: Box::new(reader),
            line: Vec::new(),
            skipped: 0,
        }
    }

    /// The next line that is at most `MAX_LINE` bytes, or None at the end.
    pub fn next_line(&mut self) -> Result<Option<&[u8]>, Error> {
        let io = |e: std::io::Error| Error::internal(e.to_string());
        loop {
            self.line.clear();
            let n = (&mut self.reader)
                .take(MAX_LINE + 1)
                .read_until(b'\n', &mut self.line)
                .map_err(io)?;
            if n == 0 {
                return Ok(None);
            }
            if self.line.last() == Some(&b'\n') {
                self.line.pop();
                return Ok(Some(&self.line));
            }
            if self.line.len() as u64 <= MAX_LINE {
                // The last line, with no line end.
                return Ok(Some(&self.line));
            }
            // Too long: drop the rest of it.
            loop {
                let chunk = self.reader.fill_buf().map_err(io)?;
                let end = chunk.iter().position(|b| *b == b'\n');
                let used = end.map_or(chunk.len(), |i| i + 1);
                self.reader.consume(used);
                if end.is_some() || used == 0 {
                    break;
                }
            }
            self.skipped += 1;
        }
    }

    /// The lines skipped so far for being too long.
    pub fn skipped(&self) -> u64 {
        self.skipped
    }
}

/// A usage parser: where its source files are, which files are its, and how a file's bytes
/// become rows. Parsers are stateless; the hub keeps every checkpoint.
pub trait Parser: Send + Sync {
    /// The source's display name, such as "Codex".
    fn label(&self) -> &str;
    /// The roots it reads when the hub names none, from the app's home folder.
    fn default_roots(&self, home: &Path) -> Vec<PathBuf>;
    /// Whether a regular file found under a root is one of its source files.
    fn wanted(&self, path: &Path) -> bool;
    /// A file's stable ID: by default its absolute path.
    fn file_id(&self, path: &Path) -> Option<String> {
        path.to_str().map(String::from)
    }
    /// The file a file ID names, when it is one of its source files under roots.
    fn locate(&self, roots: &[PathBuf], id: &str) -> Option<PathBuf> {
        locate(roots, id, &|p| self.wanted(p))
    }
    /// Turns one file's bytes (a file of at most `MAX_DOCUMENT` bytes) into rows, handed to sink
    /// as they are read. A record counted twice in one file (a repeated message ID, a cumulative
    /// counter) is the parser's to deduplicate; the hub replaces a file's rows as a whole.
    /// A parser that is `streamed` implements `parse_lines` instead, and this reads lines.
    fn parse(&self, bytes: &[u8], sink: &mut Sink) -> Result<(), Error> {
        self.parse_lines(&mut LineReader::new(bytes), sink)
    }
    /// Whether the parser reads a file line by line (`parse_lines`, a file of any size) instead of
    /// whole (`parse`, at most `MAX_DOCUMENT` bytes).
    fn streamed(&self) -> bool {
        false
    }
    /// Like `parse`, from a file's lines, in order.
    fn parse_lines(&self, _lines: &mut LineReader, _sink: &mut Sink) -> Result<(), Error> {
        Err(Error::unsupported("the parser reads no lines"))
    }
}

/// Where a parser puts one file's rows: summed per hour and model into parts, each sent as a
/// batch `{file, size, mtime_ns, part, last, rows}` once it nears the batch limit, the last one
/// when the file is done.
pub struct Sink<'a> {
    call: &'a Call,
    file: &'a str,
    size: u64,
    mtime_ns: i64,
    part: u64,
    rows: BTreeMap<(i64, String), Tokens>,
    bytes: usize,
}

/// A row's JSON size, at most, apart from its model.
const ROW_SIZE: usize = 220;

impl<'a> Sink<'a> {
    /// A sink sending the parts of file (its ID, size and modification time) as call's batches.
    pub fn new(call: &'a Call, file: &'a str, size: u64, mtime_ns: i64) -> Sink<'a> {
        Sink {
            call,
            file,
            size,
            mtime_ns,
            part: 0,
            rows: BTreeMap::new(),
            bytes: 0,
        }
    }

    /// Adds tokens of model at time (unix seconds) to its hour's row; a model name is cut to
    /// `MAX_MODEL` characters.
    pub fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        let hour = time.div_euclid(3600) * 3600;
        if !(0..=MAX_HOUR).contains(&hour) {
            return Ok(());
        }
        let model: String = model.chars().take(MAX_MODEL).collect();
        let key = (hour, model);
        if !self.rows.contains_key(&key) {
            self.bytes += ROW_SIZE + json!(key.1).to_string().len();
        }
        self.rows.entry(key).or_default().add(tokens);
        if self.bytes >= PART_BUDGET {
            self.send(false)?;
        }
        Ok(())
    }

    /// Sends the rows so far as a part of their own, if there are any.
    pub fn end_part(&mut self) -> Result<(), Error> {
        if self.rows.is_empty() {
            return Ok(());
        }
        self.send(false)
    }

    /// Whether the job was cancelled; a long parse looks now and then and stops.
    pub fn cancelled(&self) -> bool {
        self.call.cancelled()
    }

    /// The parts sent so far.
    pub fn parts(&self) -> u64 {
        self.part
    }

    fn send(&mut self, last: bool) -> Result<(), Error> {
        self.call.check()?;
        let rows: Vec<Value> = std::mem::take(&mut self.rows)
            .into_iter()
            .map(|((hour, model), tokens)| {
                Row {
                    hour,
                    model,
                    tokens,
                }
                .to_json()
            })
            .collect();
        self.bytes = 0;
        let batch = json!({"file": self.file, "size": self.size, "mtime_ns": self.mtime_ns,
            "part": self.part, "last": last, "rows": rows});
        self.call.batch(batch)?;
        self.part += 1;
        Ok(())
    }

    /// Sends the last part (empty when the file had no rows left).
    fn finish(mut self) -> Result<(), Error> {
        self.send(true)
    }
}

/// The roots a job's input names (absolute paths, at most `MAX_ROOTS`), else the parser's
/// defaults from the app's `HOME`.
fn roots(input: &Value, parser: &dyn Parser) -> Result<Vec<PathBuf>, Error> {
    match input.get("roots") {
        None | Some(Value::Null) => Ok(std::env::var_os("HOME")
            .map(|h| parser.default_roots(Path::new(&h)))
            .unwrap_or_default()),
        Some(Value::Array(a)) if a.len() <= MAX_ROOTS => a
            .iter()
            .map(|r| {
                r.as_str()
                    .filter(|s| s.len() <= MAX_PATH && Path::new(s).is_absolute())
                    .map(PathBuf::from)
                    .ok_or_else(|| Error::invalid_request("a root is not a full path"))
            })
            .collect(),
        _ => Err(Error::invalid_request("roots is not a short list of paths")),
    }
}

/// Batches of items, each `{key: [...]}` within the part budget.
fn chunked(call: &Call, key: &str, items: Vec<Value>) -> Result<(), Error> {
    let mut chunk = Vec::new();
    let mut bytes = 0;
    for item in items {
        let n = item.to_string().len() + 1;
        if !chunk.is_empty() && bytes + n > PART_BUDGET {
            call.batch(json!({key: std::mem::take(&mut chunk)}))?;
            bytes = 0;
        }
        bytes += n;
        chunk.push(item);
    }
    if !chunk.is_empty() {
        call.batch(json!({key: chunk}))?;
    }
    Ok(())
}

/// A file's outcome once its parser is through: its last part sent, or why it failed; a
/// cancellation ends the job.
fn finished(parsed: Result<(), Error>, sink: Sink) -> Result<Result<(), String>, Error> {
    match parsed {
        Ok(()) => Ok(sink.finish().map_err(|e| e.message)),
        Err(e) if e.code == "cancelled" => Err(e),
        Err(e) => Ok(Err(e.message)),
    }
}

/// Runs a job of schema 1 for parser, as the hub's job input asks:
///
/// - `{"mode": "files", "roots"?}`: lists the source files, newest first, in batches
///   `{"files": [{"file", "size", "mtime_ns"}]}`, and answers
///   `{"label", "roots", "count", "truncated"}`;
/// - `{"mode": "read", "roots"?, "files": [IDs]}`: reads each file named, sending its rows in
///   parts `{"file", "size", "mtime_ns", "part", "last", "rows"}` (`part` from 0, `last` on the
///   final one) or `{"file", "error"}` for one it cannot read, and answers `{"read", "failed"}`.
pub fn produce(call: &Call, parser: &dyn Parser) -> Result<Value, Error> {
    if call.job.is_none() {
        return Err(Error::invalid_request("this operation runs only as a job"));
    }
    let input = &call.input;
    let roots = roots(input, parser)?;
    let shown: Vec<String> = roots
        .iter()
        .map(|r| r.to_string_lossy().into_owned())
        .collect();
    match input["mode"].as_str() {
        Some("files") => {
            let wanted = |p: &Path| parser.wanted(p);
            let (found, truncated) = scan(&roots, &wanted, &|| call.cancelled())?;
            let items: Vec<Value> = found
                .iter()
                .filter_map(|f| {
                    let id = parser.file_id(&f.path)?;
                    Some(json!({"file": id, "size": f.size, "mtime_ns": f.mtime_ns}))
                })
                .collect();
            let count = items.len();
            chunked(call, "files", items)?;
            Ok(
                json!({"label": parser.label(), "roots": shown, "count": count,
                "truncated": truncated}),
            )
        }
        Some("read") => {
            let files = input["files"]
                .as_array()
                .filter(|a| a.len() <= MAX_READ)
                .ok_or_else(|| Error::invalid_request("files is not a list of file IDs"))?;
            let (mut read, mut failed) = (0u64, 0u64);
            let mut done = HashSet::new();
            for id in files {
                call.check()?;
                let id = id
                    .as_str()
                    .filter(|s| !s.is_empty() && s.len() <= MAX_PATH)
                    .ok_or_else(|| Error::invalid_request("a file ID is not a short string"))?;
                if !done.insert(id) {
                    continue;
                }
                let located = parser
                    .locate(&roots, id)
                    .ok_or_else(|| "not one of this source's files".to_string());
                // A streamed file is read as far as its size when opened, as that is the
                // revision its rows are for.
                let outcome = match located {
                    Ok(path) if parser.streamed() => match open_file(&path) {
                        Ok((file, size, mtime)) => {
                            let mut sink = Sink::new(call, id, size, mtime);
                            let reader = std::io::BufReader::new(file.take(size));
                            let parsed =
                                parser.parse_lines(&mut LineReader::new(reader), &mut sink);
                            finished(parsed, sink)?
                        }
                        Err(e) => Err(e),
                    },
                    Ok(path) => match read_file(&path) {
                        Ok((bytes, size, mtime)) => {
                            let mut sink = Sink::new(call, id, size, mtime);
                            let parsed = parser.parse(&bytes, &mut sink);
                            finished(parsed, sink)?
                        }
                        Err(e) => Err(e),
                    },
                    Err(e) => Err(e),
                };
                call.check()?;
                match outcome {
                    Ok(()) => read += 1,
                    Err(e) => {
                        failed += 1;
                        let e: String = e.chars().take(512).collect();
                        call.batch(json!({"file": id, "error": e}))?;
                    }
                }
            }
            Ok(json!({"read": read, "failed": failed}))
        }
        _ => Err(Error::invalid_request(
            "mode is neither \"files\" nor \"read\"",
        )),
    }
}

#[cfg(test)]
mod tests;
