//! pluxy/usage-opencode: the usage hub's parser of OpenCode's local store (`../README.md`). It reads
//! OpenCode's database (`opencode.db`, or `opencode-<channel>.db`) and its older JSON message files
//! (`storage/message/<session>/<message>.json`) under `~/.local/share/opencode` and `~/.opencode`
//! (the hub's `roots` name others), read-only, and sends the hub token counts per message at schema
//! 2: never a prompt, a reply, a title, a folder name or a raw session or message ID.
//!
//! Each session in a database is a file of the hub's of its own (`Parser::entries`), with the
//! session's own revision, so the hub reads again only the sessions that changed and a database of
//! many sessions never passes the hub's rows of one file. The rules that turn a message into a row
//! are in `message.rs`; the database is read in `db.rs`.

mod db;
mod message;
#[cfg(test)]
mod tests;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_usage::{produce, Entry, Found, Listing, Parser, Sink};
use serde_json::Value;
use std::path::{Component, Path, PathBuf};

/// FNV-1a, 128 bits, of parts joined by the byte 0xff (never in UTF-8), as 32 hex digits: a hash
/// that stays the same across Rust versions, as the hub keeps it (as usage-claude's).
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

/// Whether name is OpenCode's database: `opencode.db`, or `opencode-<channel>.db` for a build of
/// another channel (OpenCode's `packages/cli/src/database-path.ts`).
fn is_database(name: &str) -> bool {
    name == "opencode.db"
        || name
            .strip_prefix("opencode-")
            .and_then(|n| n.strip_suffix(".db"))
            .is_some_and(|channel| !channel.is_empty())
}

/// The parts of path below root, each valid UTF-8.
fn parts_below<'a>(root: &Path, path: &'a Path) -> Option<Vec<&'a str>> {
    path.strip_prefix(root)
        .ok()?
        .components()
        .map(|c| match c {
            Component::Normal(s) => s.to_str(),
            _ => None,
        })
        .collect()
}

/// Whether parts (below a root) name a JSON message file: `…/storage/message/<session>/<id>.json`.
fn is_message_file(parts: &[&str]) -> bool {
    matches!(parts, [.., "storage", "message", _, file] if file.len() > 5 && file.ends_with(".json"))
}

struct OpenCode {
    /// The database this call has open, so a job's sessions share one open (and one copy).
    db: db::Cache,
}

impl Parser for OpenCode {
    fn label(&self) -> &str {
        "OpenCode"
    }

    fn default_roots(&self, home: &Path) -> Vec<PathBuf> {
        vec![
            home.join(".local").join("share").join("opencode"),
            home.join(".opencode"),
        ]
    }

    fn wanted(&self, path: &Path) -> bool {
        let Some(name) = path.file_name().and_then(|n| n.to_str()) else {
            return false;
        };
        if is_database(name) {
            return true;
        }
        let parts: Vec<&str> = path
            .components()
            .rev()
            .take(4)
            .filter_map(|c| c.as_os_str().to_str())
            .collect();
        parts.len() == 4 && {
            let parts: Vec<&str> = parts.into_iter().rev().collect();
            is_message_file(&parts)
        }
    }

    /// Only an ID below a root names a file (`file_id_under`).
    fn file_id(&self, _path: &Path) -> Option<String> {
        None
    }

    /// A database: its path below root. A JSON message file: its path below root with the session
    /// folder and the file's name replaced by their hashes.
    fn file_id_under(&self, root: &Path, path: &Path) -> Option<String> {
        let parts = parts_below(root, path)?;
        match parts.as_slice() {
            [.., session, file] if is_message_file(&parts) => {
                let stem = file.strip_suffix(".json")?;
                let prefix = &parts[..parts.len() - 2];
                Some(format!(
                    "{}/{}/{}.json",
                    prefix.join("/"),
                    hash(&[session]),
                    hash(&[stem])
                ))
            }
            [.., name] if is_database(name) => Some(parts.join("/")),
            _ => None,
        }
    }

    /// A JSON message file an ID names: under the first root that has it, the session folder and
    /// file being those whose names hash to the ID's last two parts. A database is never read
    /// whole: its sessions are (`read_entry`).
    fn locate(&self, roots: &[PathBuf], id: &str) -> Option<PathBuf> {
        let parts: Vec<&str> = id.split('/').collect();
        if parts
            .iter()
            .any(|p| p.is_empty() || *p == "." || *p == "..")
        {
            return None;
        }
        let [prefix @ .., session, file] = parts.as_slice() else {
            return None;
        };
        let file = file.strip_suffix(".json")?;
        if !matches!(prefix, [.., "storage", "message"]) || !is_hash(session) || !is_hash(file) {
            return None;
        }
        let wanted = |p: &Path| self.wanted(p);
        let named = |dir: &Path, h: &str, suffix: &str| {
            std::fs::read_dir(dir).ok()?.flatten().find_map(|e| {
                let name = e.file_name();
                let stem = name.to_str()?.strip_suffix(suffix)?;
                (hash(&[stem]) == h).then(|| dir.join(&name))
            })
        };
        roots.iter().find_map(|root| {
            let mut dir = root.clone();
            dir.extend(prefix);
            let folder = named(&dir, session, "")?;
            let path = named(&folder, file, ".json")?;
            pluxy_usage::locate(roots, path.to_str()?, &wanted)
        })
    }

    /// A database lists each of its sessions; one that cannot be opened now is listed as itself
    /// (`Listing::Failed`), so a read of it gives the reason. A JSON message file is itself.
    fn entries(&self, root: &Path, found: &Found) -> Listing {
        let name = found.path.file_name().and_then(|n| n.to_str());
        let Some(id) = name
            .filter(|n| is_database(n))
            .and_then(|_| self.file_id_under(root, &found.path))
        else {
            return Listing::Itself;
        };
        match self.db.sessions(&found.path) {
            Ok(sessions) => Listing::Parts(
                sessions
                    .into_iter()
                    .map(|s| Entry {
                        id: format!("{id}#{}", hash(&[&s.id])),
                        size: s.size,
                        mtime_ns: s.mtime_ns,
                    })
                    .collect(),
            ),
            Err(_) => Listing::Failed,
        }
    }

    /// A session of a database (`<database ID>#<hash of the session ID>`), from the root whose
    /// database has it at the newest revision, as the listing keeps it; a database listed as
    /// itself, which fails with the reason; or a JSON message file.
    fn read_entry(
        &self,
        roots: &[PathBuf],
        id: &str,
        sink: &mut Sink,
    ) -> Option<Result<(), Error>> {
        if id.ends_with(".json") && !id.contains('#') {
            return Some(self.read_message_file(roots, id, sink));
        }
        let (file, session) = id.rsplit_once('#').unwrap_or((id, ""));
        let parts: Vec<&str> = file.split('/').collect();
        let not_ours = || Error::invalid_request("not one of this source's files");
        if parts
            .iter()
            .any(|p| p.is_empty() || *p == "." || *p == "..")
            || !parts.last().is_some_and(|n| is_database(n))
        {
            return Some(Err(not_ours()));
        }
        let wanted = |p: &Path| self.wanted(p);
        let databases = roots
            .iter()
            .filter_map(|root| pluxy_usage::locate(roots, root.join(file).to_str()?, &wanted));
        // The newest revision of the session (the first root's on a tie), or the reason a
        // database could not be read.
        let mut newest: Option<(PathBuf, db::Session)> = None;
        let mut failed = None;
        for path in databases {
            if !is_hash(session) {
                failed = Some(match self.db.sessions(&path) {
                    Ok(_) => "the database was listed by its sessions".to_string(),
                    Err(e) => e,
                });
                continue;
            }
            match self.db.find(&path, session) {
                Ok(Some(s)) => {
                    if newest
                        .as_ref()
                        .is_none_or(|(_, n)| (s.mtime_ns, s.size) > (n.mtime_ns, n.size))
                    {
                        newest = Some((path, s));
                    }
                }
                Ok(None) => {}
                Err(e) => failed = Some(e),
            }
        }
        Some(match (newest, failed) {
            (Some((path, s)), _) => self.db.read_session(&path, &s.id, sink),
            (None, Some(why)) => Err(Error::internal(why)),
            (None, None) => Err(not_ours()),
        })
    }
}

impl OpenCode {
    /// A JSON message file, keyed by its file's name and its folder's, as OpenCode's migration to
    /// its database took a message's and its session's IDs.
    fn read_message_file(&self, roots: &[PathBuf], id: &str, sink: &mut Sink) -> Result<(), Error> {
        let path = self
            .locate(roots, id)
            .ok_or_else(|| Error::invalid_request("not one of this source's files"))?;
        let names = || {
            let message = path.file_stem()?.to_str()?;
            let session = path.parent()?.file_name()?.to_str()?;
            Some((message, session))
        };
        let (message, session) =
            names().ok_or_else(|| Error::invalid_request("not a message file"))?;
        let (bytes, size, mtime) = pluxy_usage::read_file(&path).map_err(Error::invalid_request)?;
        sink.revise(size, mtime)?;
        let value = serde_json::from_slice::<Value>(&bytes)
            .map_err(|_| Error::invalid_request("not a JSON message"))?;
        message::json_file(&value, message, session, sink)
    }
}

struct Tool;

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        match call.op.as_str() {
            "scan" => produce(
                call,
                &OpenCode {
                    db: db::Cache::default(),
                },
            ),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

fn main() -> std::io::Result<()> {
    run(Tool)
}
