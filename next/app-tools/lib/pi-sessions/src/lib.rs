//! What the Pi (`pluxy/usage-pi`) and Oh My Pi (`pluxy/usage-omp`) usage parsers share: their session
//! files as the usage hub's rows (`rows.rs`) and where those files are. Each parser is a package of its
//! own; this library holds the one format they both read, so neither copies the other.
//!
//! A tool's session files are `*.jsonl` under `~/.<name>/agent/sessions` (where each writes them) and
//! `~/.<name>/sessions` (where the old desktop app also looked), read-only; the hub's `roots` name
//! others. A file's ID is its path relative to the root it is under, so a session present under both
//! roots is one file, and the newer copy is the one read.

mod rows;
#[cfg(test)]
mod tests;

use pluxy_program::{Call, Error};
use pluxy_usage::{produce, LineReader, Parser, Sink, MAX_PATH};
use serde_json::Value;
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path, PathBuf};

/// The parser of one tool's sessions: its display name, the folder it keeps its files in (`.omp`), and
/// the roots this call reads (the ones `pluxy_usage::produce` works out from the same input).
pub struct Sessions {
    label: &'static str,
    folder: &'static str,
    roots: Vec<PathBuf>,
}

impl Sessions {
    pub fn new(label: &'static str, folder: &'static str, roots: Vec<PathBuf>) -> Sessions {
        Sessions {
            label,
            folder,
            roots,
        }
    }

    /// The parser of a call's job: its roots are the call's `roots` input, else the defaults from `HOME`
    /// (an input that names no usable roots gives none; `produce` then refuses the call itself).
    pub fn for_call(label: &'static str, folder: &'static str, call: &Call) -> Sessions {
        let roots = match call.input.get("roots") {
            None | Some(Value::Null) => std::env::var_os("HOME")
                .map(|h| default_roots(folder, Path::new(&h)))
                .unwrap_or_default(),
            Some(Value::Array(a)) => a
                .iter()
                .filter_map(Value::as_str)
                .map(PathBuf::from)
                .collect(),
            _ => Vec::new(),
        };
        Sessions::new(label, folder, roots)
    }
}

fn default_roots(folder: &str, home: &Path) -> Vec<PathBuf> {
    vec![
        home.join(folder).join("agent").join("sessions"),
        home.join(folder).join("sessions"),
    ]
}

/// Answers a parser's produces operation for a tool: `files` and `read` jobs of schema 1.
pub fn scan_call(call: &Call, label: &'static str, folder: &'static str) -> Result<Value, Error> {
    produce(call, &Sessions::for_call(label, folder, call))
}

impl Parser for Sessions {
    fn label(&self) -> &str {
        self.label
    }

    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        default_roots(self.folder, home)
    }

    fn wanted(&self, path: &Path) -> bool {
        path.extension()
            .is_some_and(|e| e.eq_ignore_ascii_case("jsonl"))
    }

    /// The path below the longest root the file is under, joined with `/`.
    fn file_id(&self, path: &Path) -> Option<String> {
        let rest = self
            .roots
            .iter()
            .filter_map(|root| path.strip_prefix(root).ok())
            .min_by_key(|rest| rest.components().count())?;
        let parts: Option<Vec<&str>> = rest
            .components()
            .map(|c| match c {
                Component::Normal(p) => p.to_str(),
                _ => None,
            })
            .collect();
        let id = parts?.join("/");
        (!id.is_empty()).then_some(id)
    }

    /// The file whose ID is id: that relative path under each root; of several, the newest, as the
    /// file listing puts it first.
    fn locate(&self, roots: &[PathBuf], id: &str) -> Option<PathBuf> {
        let relative = Path::new(id);
        if id.is_empty()
            || id.len() > MAX_PATH
            || relative.is_absolute()
            || !relative
                .components()
                .all(|c| matches!(c, Component::Normal(_)))
        {
            return None;
        }
        let wanted = |p: &Path| self.wanted(p);
        let mut found: Vec<(i64, PathBuf)> = roots
            .iter()
            .filter_map(|root| {
                let path = pluxy_usage::locate(roots, root.join(relative).to_str()?, &wanted)?;
                let m = std::fs::symlink_metadata(&path).ok()?;
                let ns = m
                    .mtime()
                    .saturating_mul(1_000_000_000)
                    .saturating_add(m.mtime_nsec());
                Some((ns, path))
            })
            .collect();
        found.sort_by(|a, b| b.0.cmp(&a.0).then(a.1.cmp(&b.1)));
        found.into_iter().next().map(|f| f.1)
    }

    fn streamed(&self) -> bool {
        true
    }

    fn parse_lines(&self, lines: &mut LineReader, sink: &mut Sink) -> Result<(), Error> {
        rows::parse(lines, sink)
    }
}

/// The files a job's listing would find under roots, newest first (for the tests).
#[cfg(test)]
fn listed(roots: &[PathBuf], parser: &Sessions) -> Vec<PathBuf> {
    let (found, _) = pluxy_usage::scan(roots, &|p| parser.wanted(p), &|| false).unwrap();
    found.into_iter().map(|f| f.path).collect()
}
