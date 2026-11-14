//! pluxy/usage-codex: the usage hub's parser of Codex's session files (`../README.md`). It reads
//! the rollout files under `~/.codex/sessions` and `~/.codex/archived_sessions` (the hub's `roots`
//! name others), read-only, and sends the hub only token counts per hour and model: never a prompt,
//! a message, a folder or a key. The rules that turn a file into rows are in `rollout.rs`.
//!
//! A session's file ID is its file name, so a session Codex moves to the archive is the same file
//! (and one copied there is still one session, the newer file being the one read).

mod rollout;
#[cfg(test)]
mod tests;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_usage::{produce, scan, Parser, Sink};
use serde_json::Value;
use std::os::unix::fs::MetadataExt;
use std::path::{Component, Path, PathBuf};

struct Codex;

impl Parser for Codex {
    fn label(&self) -> &str {
        "Codex"
    }

    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![
            home.join(".codex").join("sessions"),
            home.join(".codex").join("archived_sessions"),
        ]
    }

    fn wanted(&self, path: &Path) -> bool {
        path.extension()
            .is_some_and(|e| e.eq_ignore_ascii_case("jsonl"))
    }

    fn file_id(&self, path: &Path) -> Option<String> {
        path.file_name()?.to_str().map(String::from)
    }

    /// The file named id: where Codex puts it (`<root>/<name>` in the archive, `<root>/YYYY/MM/DD/
    /// <name>` for a name that starts `rollout-YYYY-MM-DDT`), else, for a layout of another kind,
    /// what a scan finds. Of several files of that name, the newest, as the file listing puts it
    /// first.
    fn locate(&self, roots: &[PathBuf], id: &str) -> Option<PathBuf> {
        let mut parts = Path::new(id).components();
        let (Some(Component::Normal(_)), None) = (parts.next(), parts.next()) else {
            return None;
        };
        let wanted = |p: &Path| self.wanted(p);
        let dated = id
            .strip_prefix("rollout-")
            .and_then(|r| r.get(..10))
            .map(|d| d.replace('-', "/"));
        let mut found: Vec<(i64, PathBuf)> = roots
            .iter()
            .flat_map(|root| {
                std::iter::once(root.join(id))
                    .chain(dated.iter().map(move |d| root.join(d).join(id)))
            })
            .filter_map(|path| {
                let path = pluxy_usage::locate(roots, path.to_str()?, &wanted)?;
                Some((std::fs::symlink_metadata(&path).ok()?, path))
            })
            .map(|(m, path)| {
                let ns = m
                    .mtime()
                    .saturating_mul(1_000_000_000)
                    .saturating_add(m.mtime_nsec());
                (ns, path)
            })
            .collect();
        if found.is_empty() {
            let (all, _) = scan(roots, &wanted, &|| false).ok()?;
            return all
                .into_iter()
                .find(|f| self.file_id(&f.path).as_deref() == Some(id))
                .map(|f| f.path);
        }
        found.sort_by(|a, b| b.0.cmp(&a.0).then(a.1.cmp(&b.1)));
        found.into_iter().next().map(|f| f.1)
    }

    fn parse(&self, bytes: &[u8], sink: &mut Sink) -> Result<(), Error> {
        rollout::parse(bytes, sink)
    }
}

struct Tool;

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "scan" => produce(call, &Codex),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Tool)
}
