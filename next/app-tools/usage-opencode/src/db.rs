//! OpenCode's database, read-only: its sessions with their revisions, and one session's messages.
//!
//! OpenCode keeps messages in `session_message` (today's table: `id`, `session_id`, `type`, `seq`,
//! `time_created`, `time_updated`, `data`) and, in a database made before that table, in `message`
//! (`id`, `session_id`, `time_created`, `time_updated`, `data`), which its migration copies into
//! `session_message` under the same IDs and leaves in place. Both are read; a message in both counts
//! once.
//!
//! A fork (`session_v2.fork_session_id`) copies its parent's settled rows under new IDs,
//! `<fork event>_<seq>`, with the same `seq` (OpenCode's `session/projector.ts`); such a copy takes
//! the key of the parent's row at that seq, followed up the fork chain, so a fork's copied history
//! counts once. A copy whose original is gone keys as the session it was copied from and the seq.
//!
//! A session's revision is the sum of its message rows' count and data lengths and their latest
//! `time_updated` (OpenCode updates a row's `time_updated` with every write), read through SQLite
//! so a change still in the `-wal` is in it.
//!
//! The database is opened by `pluxy-sqlite-ro`: read-only, nothing made beside it, a link or a
//! `-wal` or `-shm` that is one not read, errors naming no folder.

use crate::hash;
use crate::message::{self, Out};
use pluxy_program::Error;
use pluxy_sqlite_ro::{open_read_only, Db};
use rusqlite::{Connection, OptionalExtension};
use serde_json::Value;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::Mutex;

/// The largest message data read, in bytes, as the old reader had it.
const MAX_DATA: i64 = 16 << 20;
/// Rows between looks at whether the job was cancelled.
const CANCEL_EVERY: usize = 1024;
/// Forks followed back at most.
const MAX_FORKS: usize = 64;
/// The `session_message` rows that can carry tokens.
const TYPES: &str = "type IN ('assistant', 'compaction')";

/// A session of a database and its revision.
#[derive(Debug, PartialEq, Eq)]
pub struct Session {
    pub id: String,
    pub size: u64,
    pub mtime_ns: i64,
}

/// A database a call opened, and its sessions by the hash of their IDs once a read needs them.
struct Open {
    db: Db,
    by_hash: Option<HashMap<String, String>>,
}

/// The databases a call has open, kept until its end for the call's other sessions.
#[derive(Default)]
pub struct Cache(Mutex<HashMap<PathBuf, Open>>);

impl Cache {
    fn with<T>(&self, path: &Path, f: impl FnOnce(&mut Open) -> T) -> Result<T, String> {
        let mut open = self.0.lock().unwrap_or_else(|e| e.into_inner());
        if !open.contains_key(path) {
            let db = open_read_only(path).map_err(|e| e.to_string())?;
            open.insert(path.to_path_buf(), Open { db, by_hash: None });
        }
        let o = open.get_mut(path).ok_or("no database")?;
        Ok(f(o))
    }

    /// The database's sessions with their revisions.
    pub fn sessions(&self, path: &Path) -> Result<Vec<Session>, String> {
        self.with(path, |o| sessions(&o.db, None))?
    }

    /// The session whose ID hashes to wanted, with its revision, when the database has it.
    pub fn find(&self, path: &Path, wanted: &str) -> Result<Option<Session>, String> {
        self.with(path, |o| {
            if o.by_hash.is_none() {
                o.by_hash = Some(session_ids(&o.db)?);
            }
            let Some(id) = o.by_hash.as_ref().and_then(|m| m.get(wanted)) else {
                return Ok(None);
            };
            Ok(sessions(&o.db, Some(id))?.into_iter().next())
        })?
    }

    /// Gives out the messages of session, at its revision.
    pub fn read_session(&self, path: &Path, session: &str, out: &mut dyn Out) -> Result<(), Error> {
        self.with(path, |o| read_session(&o.db, session, out))
            .map_err(Error::internal)?
    }
}

/// The database's session IDs by their hashes.
fn session_ids(c: &Connection) -> Result<HashMap<String, String>, String> {
    let t = tables(c);
    let mut by_hash = HashMap::new();
    for (table, has) in [("session_message", t.current), ("message", t.old)] {
        if has {
            let mut st = c
                .prepare(&format!("SELECT DISTINCT session_id FROM {table}"))
                .map_err(|e| e.to_string())?;
            let rows = st
                .query_map([], |r| r.get::<_, String>(0))
                .map_err(|e| e.to_string())?;
            for id in rows {
                let id = id.map_err(|e| e.to_string())?;
                by_hash.insert(hash(&[&id]), id);
            }
        }
    }
    Ok(by_hash)
}

/// Which of OpenCode's tables a database has.
struct Tables {
    current: bool,
    old: bool,
    forks: bool,
}

fn tables(c: &Connection) -> Tables {
    let has = |table: &str, column: &str| {
        c.query_row(
            "SELECT count(*) FROM pragma_table_info(?1) WHERE name = ?2",
            [table, column],
            |r| r.get::<_, i64>(0),
        )
        .is_ok_and(|n| n > 0)
    };
    Tables {
        current: has("session_message", "seq") && has("session_message", "type"),
        old: has("message", "session_id") && has("message", "data"),
        forks: has("session_v2", "fork_session_id"),
    }
}

/// The sessions (all, or the one named) with their revisions, over both tables.
fn sessions(c: &Connection, only: Option<&str>) -> Result<Vec<Session>, String> {
    let t = tables(c);
    let mut by: BTreeMap<String, (u64, i64)> = BTreeMap::new();
    let filter = if only.is_some() {
        "session_id = ?1"
    } else {
        "?1 IS NULL"
    };
    let mut queries = Vec::new();
    if t.current {
        queries.push(format!(
            "SELECT session_id, count(*) + coalesce(sum(octet_length(data)), 0), coalesce(max(time_updated), 0)
             FROM session_message WHERE {TYPES} AND {filter} GROUP BY session_id"
        ));
    }
    if t.old {
        queries.push(format!(
            "SELECT session_id, count(*) + coalesce(sum(octet_length(data)), 0), coalesce(max(time_updated), 0)
             FROM message WHERE {filter} GROUP BY session_id"
        ));
    }
    for q in queries {
        let mut st = c.prepare(&q).map_err(|e| e.to_string())?;
        let rows = st
            .query_map([only], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    r.get::<_, i64>(1)?,
                    r.get::<_, i64>(2)?,
                ))
            })
            .map_err(|e| e.to_string())?;
        for row in rows {
            let (id, size, updated) = row.map_err(|e| e.to_string())?;
            let e = by.entry(id).or_default();
            e.0 = e.0.saturating_add(size.max(0) as u64);
            e.1 = e.1.max(updated.saturating_mul(1_000_000));
        }
    }
    Ok(by
        .into_iter()
        .map(|(id, (size, mtime_ns))| Session { id, size, mtime_ns })
        .collect())
}

/// Whether id is a fork's copy of the row at seq: `<fork event>_<seq>`.
fn is_copy(id: &str, seq: i64) -> bool {
    id.matches('_').count() >= 2
        && id
            .rsplit_once('_')
            .is_some_and(|(_, n)| n == seq.to_string())
}

/// The key of the row a fork's copy at seq in session (whose parent is parent) copies.
fn copied_key(c: &Connection, mut parent: String, seq: i64) -> Result<String, String> {
    for _ in 0..MAX_FORKS {
        let row: Option<String> = c
            .query_row(
                "SELECT id FROM session_message WHERE session_id = ?1 AND seq = ?2",
                rusqlite::params![parent, seq],
                |r| r.get(0),
            )
            .optional()
            .map_err(|e| e.to_string())?;
        let Some(id) = row else {
            break;
        };
        match fork_parent(c, &parent)? {
            Some(grand) if is_copy(&id, seq) => parent = grand,
            _ => return Ok(message::key(&id, &parent)),
        }
    }
    Ok(hash(&["opencode-fork", &parent, &seq.to_string()]))
}

/// The session a session was forked from.
fn fork_parent(c: &Connection, session: &str) -> Result<Option<String>, String> {
    c.query_row(
        "SELECT fork_session_id FROM session_v2 WHERE id = ?1",
        [session],
        |r| r.get::<_, Option<String>>(0),
    )
    .optional()
    .map(Option::flatten)
    .map_err(|e| e.to_string())
}

/// Gives out the messages of session in one read transaction: its revision, then each message
/// with tokens once.
fn read_session(c: &Connection, session: &str, out: &mut dyn Out) -> Result<(), Error> {
    let internal = |e: rusqlite::Error| Error::internal(e.to_string());
    let tx = c.unchecked_transaction().map_err(internal)?;
    let t = tables(&tx);
    let session = session.to_string();
    let revision = sessions(&tx, Some(&session)).map_err(Error::internal)?;
    let (size, mtime_ns) = revision.first().map_or((0, 0), |s| (s.size, s.mtime_ns));
    out.revise(size, mtime_ns)?;
    let parent = if t.forks {
        fork_parent(&tx, &session).map_err(Error::internal)?
    } else {
        None
    };
    let mut seen = HashSet::new();
    let mut n = 0usize;
    // Each message with tokens once; from the old table, only assistant messages.
    let mut give =
        |out: &mut dyn Out, key: String, data: &str, time: i64, old: bool| -> Result<(), Error> {
            n += 1;
            if n.is_multiple_of(CANCEL_EVERY) && out.cancelled() {
                return Err(Error::cancelled());
            }
            let Ok(value) = serde_json::from_str::<Value>(data) else {
                return Ok(());
            };
            if old && !message::is_assistant(&value) {
                return Ok(());
            }
            let Some(u) = message::usage(&value, Some(time)) else {
                return Ok(());
            };
            if seen.insert(key.clone()) {
                out.event(u.time, &u.model, &key, &u.tokens)?;
            }
            Ok(())
        };
    if t.current {
        let mut st = tx
            .prepare(&format!(
                "SELECT id, seq, time_created, data FROM session_message
                 WHERE session_id = ?1 AND {TYPES} AND octet_length(data) <= {MAX_DATA} ORDER BY seq"
            ))
            .map_err(internal)?;
        let mut rows = st.query([&session]).map_err(internal)?;
        while let Some(r) = rows.next().map_err(internal)? {
            let (id, seq, time, data): (String, i64, i64, String) = (
                r.get(0).map_err(internal)?,
                r.get(1).map_err(internal)?,
                r.get(2).map_err(internal)?,
                r.get(3).map_err(internal)?,
            );
            let key = match &parent {
                Some(p) if is_copy(&id, seq) => {
                    copied_key(&tx, p.clone(), seq).map_err(Error::internal)?
                }
                _ => message::key(&id, &session),
            };
            give(out, key, &data, time, false)?;
        }
    }
    if t.old {
        let mut st = tx
            .prepare(&format!(
                "SELECT id, time_created, data FROM message
                 WHERE session_id = ?1 AND octet_length(data) <= {MAX_DATA} ORDER BY time_created, id"
            ))
            .map_err(internal)?;
        let mut rows = st.query([&session]).map_err(internal)?;
        while let Some(r) = rows.next().map_err(internal)? {
            let (id, time, data): (String, i64, String) = (
                r.get(0).map_err(internal)?,
                r.get(1).map_err(internal)?,
                r.get(2).map_err(internal)?,
            );
            give(out, message::key(&id, &session), &data, time, true)?;
        }
    }
    tx.commit().map_err(internal)?;
    Ok(())
}
