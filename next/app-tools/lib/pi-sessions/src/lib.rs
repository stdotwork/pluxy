//! What the Pi (`pluxy/usage-pi`) and Oh My Pi (`pluxy/usage-omp`) usage parsers share: their session
//! files as the usage hub's rows (`rows.rs`) and where those files are. Each parser is a package of its
//! own; this library holds the one format they both read, so neither copies the other.
//!
//! A tool's session files are `*.jsonl` under `~/.<name>/agent/sessions` (where each writes them) and
//! `~/.<name>/sessions` (where the old desktop app also looked), read-only; the hub's `roots` name
//! others. A file's ID is its path below the root it is under, with the first folder (the tool names it
//! after the session's working folder) replaced by a hash of its name, so an ID names no folder of the
//! person's; a session present under both roots is one file, and the newer copy is the one read.
//! Each usage entry is an event of schema 2 (`rows.rs`).

mod rows;
#[cfg(test)]
mod tests;

use pluxy_program::{Call, Error};
use pluxy_usage::{produce, LineReader, Parser, Sink, MAX_PATH};
use serde_json::Value;
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path, PathBuf};

/// FNV-1a, 128 bits, of parts joined by the byte 0xff (never in UTF-8), as 32 hex digits: a hash that
/// stays the same across Rust versions, as the hub keeps it.
pub fn hash(parts: &[&str]) -> String {
    const OFFSET: u128 = 0x6c62_272e_07bb_0142_62b8_2175_6295_c58d;
    const PRIME: u128 = 0x0000_0000_0100_0000_0000_0000_0000_013b;
    let mut h = OFFSET;
    for (i, part) in parts.iter().enumerate() {
        let sep: &[u8] = if i > 0 { &[0xff] } else { &[] };
        for b in sep.iter().chain(part.as_bytes()) {
            h ^= u128::from(*b);
            h = h.wrapping_mul(PRIME);
        }
    }
    format!("{h:032x}")
}

/// Whether s is a hash as `hash` writes it.
fn is_hash(s: &str) -> bool {
    s.len() == 32 && s.bytes().all(|b| matches!(b, b'0'..=b'9' | b'a'..=b'f'))
}

/// The parser of one tool's sessions: its display name and the folder it keeps its files in (`.omp`).
pub struct Sessions {
    label: &'static str,
    folder: &'static str,
}

impl Sessions {
    pub fn new(label: &'static str, folder: &'static str) -> Sessions {
        Sessions { label, folder }
    }
}

fn default_roots(folder: &str, home: &Path) -> Vec<PathBuf> {
    vec![
        home.join(folder).join("agent").join("sessions"),
        home.join(folder).join("sessions"),
    ]
}

/// Answers a parser's produces operation for a tool: `files` and `read` jobs of schema 2.
pub fn scan_call(call: &Call, label: &'static str, folder: &'static str) -> Result<Value, Error> {
    produce(call, &Sessions::new(label, folder))
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

    /// Only an ID below a root names a session file (`file_id_under`).
    fn file_id(&self, _path: &Path) -> Option<String> {
        None
    }

    /// The path below root, its first folder replaced by the hash of its name; a file directly in root
    /// is its name.
    fn file_id_under(&self, root: &Path, path: &Path) -> Option<String> {
        let parts: Vec<&str> = path
            .strip_prefix(root)
            .ok()?
            .components()
            .map(|c| match c {
                Component::Normal(s) => s.to_str(),
                _ => None,
            })
            .collect::<Option<_>>()?;
        match parts.as_slice() {
            [] => None,
            [name] => Some(name.to_string()),
            [folder, rest @ ..] => Some(format!("{}/{}", hash(&[folder]), rest.join("/"))),
        }
    }

    /// The file whose ID is id: under each root, the folder whose name hashes to the ID's first part,
    /// then the rest of the path; of several, the newest, as the file listing puts it first.
    fn locate(&self, roots: &[PathBuf], id: &str) -> Option<PathBuf> {
        let parts: Vec<&str> = id.split('/').collect();
        if id.len() > MAX_PATH
            || parts
                .iter()
                .any(|p| p.is_empty() || *p == "." || *p == "..")
        {
            return None;
        }
        let wanted = |p: &Path| self.wanted(p);
        let found = |path: PathBuf| pluxy_usage::locate(roots, path.to_str()?, &wanted);
        let candidates: Vec<PathBuf> = match parts.as_slice() {
            [name] => roots
                .iter()
                .filter_map(|root| found(root.join(name)))
                .collect(),
            [folder, rest @ ..] if is_hash(folder) => roots
                .iter()
                .flat_map(|root| {
                    std::fs::read_dir(root)
                        .into_iter()
                        .flatten()
                        .flatten()
                        .filter_map(|e| {
                            let name = e.file_name();
                            let name = name.to_str()?;
                            if hash(&[name]) != *folder {
                                return None;
                            }
                            let mut path = root.join(name);
                            path.extend(rest);
                            found(path)
                        })
                        .collect::<Vec<_>>()
                })
                .collect(),
            _ => return None,
        };
        let mut found: Vec<(i64, PathBuf)> = candidates
            .into_iter()
            .filter_map(|path| {
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
