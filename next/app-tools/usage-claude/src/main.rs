//! pluxy/usage-claude: the usage hub's parser of Claude Code's session transcripts (`../README.md`).
//! It reads the transcripts (`*.jsonl`) under `~/.claude/projects` (the hub's `roots` name others),
//! read-only, and sends the hub token counts per message at schema 2: never a prompt, a message, a
//! folder name or a raw message, request or session ID. The rules that turn a file into rows are in
//! `transcript.rs`.
//!
//! A transcript's file ID is its path below the root with the first folder, Claude Code's project
//! folder (named after the project's working folder), replaced by a hash of its name, so a file ID
//! names no folder of the person's.

#[cfg(test)]
mod tests;
mod transcript;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_usage::{produce, LineReader, Parser, Sink};
use serde_json::Value;
use std::path::{Component, Path, PathBuf};

/// FNV-1a, 128 bits, of parts joined by the byte 0xff (never in UTF-8), as 32 hex digits: a hash
/// that stays the same across Rust versions, as the hub keeps it.
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

struct Claude;

impl Parser for Claude {
    fn label(&self) -> &str {
        "Claude Code"
    }

    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![home.join(".claude").join("projects")]
    }

    fn wanted(&self, path: &Path) -> bool {
        path.extension()
            .is_some_and(|e| e.eq_ignore_ascii_case("jsonl"))
    }

    /// Only an ID below a root names a transcript (`file_id_under`).
    fn file_id(&self, _path: &Path) -> Option<String> {
        None
    }

    /// The path below root, its first folder replaced by the hash of its name; a file directly in
    /// root is its name.
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

    /// The transcript id names: under the first root that has it, the project folder being the
    /// one whose name hashes to the ID's first part.
    fn locate(&self, roots: &[PathBuf], id: &str) -> Option<PathBuf> {
        let parts: Vec<&str> = id.split('/').collect();
        if parts
            .iter()
            .any(|p| p.is_empty() || *p == "." || *p == "..")
        {
            return None;
        }
        let wanted = |p: &Path| self.wanted(p);
        let found = |path: PathBuf| pluxy_usage::locate(roots, path.to_str()?, &wanted);
        match parts.as_slice() {
            [name] => roots.iter().find_map(|root| found(root.join(name))),
            [folder, rest @ ..] if is_hash(folder) => roots.iter().find_map(|root| {
                std::fs::read_dir(root).ok()?.flatten().find_map(|e| {
                    let name = e.file_name();
                    let name = name.to_str()?;
                    if hash(&[name]) != *folder {
                        return None;
                    }
                    let mut path = root.join(name);
                    path.extend(rest);
                    found(path)
                })
            }),
            _ => None,
        }
    }

    fn streamed(&self) -> bool {
        true
    }

    fn parse_lines(&self, lines: &mut LineReader, sink: &mut Sink) -> Result<(), Error> {
        transcript::parse(lines, sink)
    }
}

struct Tool;

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "scan" => produce(call, &Claude),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Tool)
}
