//! The hub's SQLite store, the only place usage is kept: per source file, its revision (size and
//! modification time) and its rows per UTC hour and model; per source, its keyed events (schema 2:
//! one record counted once whichever files hold it) and the files that reference each; the
//! sources seen, with their labels and custom roots; the settings and the last sync time. Only the
//! hub writes it.

use pluxy_usage::Tokens;
use rusqlite::{params, Connection, OptionalExtension};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::path::{Path, PathBuf};

/// The store's own format version, in `meta`: 2 adds keyed events. A store of format 1 is moved to
/// 2 in place when opened, its rows kept.
const FORMAT: &str = "2";
/// The sync intervals the person may choose, in minutes (0: off), and the default.
pub const INTERVALS: [u64; 5] = [0, 5, 15, 30, 60];
pub const DEFAULT_INTERVAL: u64 = 5;

/// A file's rows, summed per (UTC hour, model).
pub type Rows = BTreeMap<(i64, String), Tokens>;
/// A file's keyed events: key to (UTC hour, model, tokens).
pub type Events = BTreeMap<String, (i64, String, Tokens)>;

/// The tables format 2 adds.
const EVENTS: &str = "CREATE TABLE IF NOT EXISTS events (
       id INTEGER PRIMARY KEY, source TEXT NOT NULL, key TEXT NOT NULL,
       hour INTEGER NOT NULL, model TEXT NOT NULL,
       input INTEGER NOT NULL, output INTEGER NOT NULL, cache_read INTEGER NOT NULL,
       cache_write INTEGER NOT NULL, reasoning INTEGER NOT NULL, total INTEGER NOT NULL,
       samples INTEGER NOT NULL,
       UNIQUE (source, key));
     CREATE INDEX IF NOT EXISTS events_hour ON events (hour);
     CREATE TABLE IF NOT EXISTS refs (
       file INTEGER NOT NULL REFERENCES files (id) ON DELETE CASCADE,
       event INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
       PRIMARY KEY (file, event)) WITHOUT ROWID;
     CREATE INDEX IF NOT EXISTS refs_event ON refs (event);";

/// Every row the queries count: a file's rows, and each keyed event once.
const ALL_ROWS: &str = "(SELECT f.source AS source, u.hour AS hour, u.model AS model,
       u.input AS input, u.output AS output, u.cache_read AS cache_read,
       u.cache_write AS cache_write, u.reasoning AS reasoning, u.total AS total,
       u.samples AS samples
     FROM usage u JOIN files f ON f.id = u.file
     UNION ALL
     SELECT source, hour, model, input, output, cache_read, cache_write, reasoning, total, samples
     FROM events)";

fn e(err: rusqlite::Error) -> String {
    err.to_string()
}

/// A source the store knows: its label, custom roots (none: the parser's defaults), last scan,
/// and how many files and rows it holds.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Source {
    pub id: String,
    pub label: String,
    pub roots: Option<Vec<String>>,
    pub last_scan: Option<i64>,
    pub files: u64,
    pub rows: u64,
}

pub struct Store {
    db: Connection,
    path: PathBuf,
}

impl Store {
    /// Opens (making it when missing) the store at path, readable only by this user.
    pub fn open(path: &Path) -> Result<Store, String> {
        use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
        std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(false)
            .mode(0o600)
            .open(path)
            .map_err(|err| err.to_string())?;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600))
            .map_err(|err| err.to_string())?;
        let db = Connection::open(path).map_err(e)?;
        db.execute_batch(
            "PRAGMA foreign_keys = ON;
             CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
             CREATE TABLE IF NOT EXISTS sources (
               source TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', roots TEXT,
               last_scan INTEGER) WITHOUT ROWID;
             CREATE TABLE IF NOT EXISTS files (
               id INTEGER PRIMARY KEY, source TEXT NOT NULL, file TEXT NOT NULL,
               size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL, present INTEGER NOT NULL DEFAULT 1,
               UNIQUE (source, file));
             CREATE TABLE IF NOT EXISTS usage (
               file INTEGER NOT NULL REFERENCES files (id) ON DELETE CASCADE,
               hour INTEGER NOT NULL, model TEXT NOT NULL,
               input INTEGER NOT NULL, output INTEGER NOT NULL, cache_read INTEGER NOT NULL,
               cache_write INTEGER NOT NULL, reasoning INTEGER NOT NULL, total INTEGER NOT NULL,
               samples INTEGER NOT NULL,
               PRIMARY KEY (file, hour, model)) WITHOUT ROWID;
             CREATE INDEX IF NOT EXISTS usage_hour ON usage (hour);",
        )
        .map_err(e)?;
        let format: Option<String> = db
            .query_row("SELECT value FROM meta WHERE key = 'format'", [], |r| {
                r.get(0)
            })
            .optional()
            .map_err(e)?;
        match format.as_deref() {
            Some(FORMAT) => {}
            // New, or format 1: add format 2's tables and say so, in one transaction.
            None | Some("1") => {
                db.execute_batch(&format!(
                    "BEGIN; {EVENTS}
                     INSERT INTO meta VALUES ('format', '{FORMAT}')
                       ON CONFLICT (key) DO UPDATE SET value = '{FORMAT}';
                     COMMIT;"
                ))
                .map_err(|err| {
                    let _ = db.execute_batch("ROLLBACK");
                    e(err)
                })?;
            }
            Some(other) => return Err(format!("the store's format {other} is not known")),
        }
        Ok(Store {
            db,
            path: path.to_path_buf(),
        })
    }

    /// Removes the store at path (with its journal) and opens a new, empty one.
    pub fn reset(path: &Path) -> Result<Store, String> {
        for suffix in ["", "-journal", "-wal", "-shm"] {
            let mut p = path.as_os_str().to_owned();
            p.push(suffix);
            match std::fs::remove_file(&p) {
                Err(err) if err.kind() != std::io::ErrorKind::NotFound => {
                    return Err(err.to_string())
                }
                _ => {}
            }
        }
        Store::open(path)
    }

    fn meta(&self, key: &str) -> Result<Option<String>, String> {
        self.db
            .query_row("SELECT value FROM meta WHERE key = ?1", [key], |r| r.get(0))
            .optional()
            .map_err(e)
    }

    fn set_meta(&self, key: &str, value: Option<String>) -> Result<(), String> {
        match value {
            Some(v) => self.db.execute(
                "INSERT INTO meta VALUES (?1, ?2) ON CONFLICT (key) DO UPDATE SET value = ?2",
                params![key, v],
            ),
            None => self.db.execute("DELETE FROM meta WHERE key = ?1", [key]),
        }
        .map(|_| ())
        .map_err(e)
    }

    /// The sync interval in minutes.
    pub fn interval(&self) -> Result<u64, String> {
        Ok(self
            .meta("interval")?
            .and_then(|v| v.parse().ok())
            .filter(|v| INTERVALS.contains(v))
            .unwrap_or(DEFAULT_INTERVAL))
    }

    pub fn set_interval(&self, minutes: u64) -> Result<(), String> {
        self.set_meta("interval", Some(minutes.to_string()))
    }

    /// When the last sync ended (unix seconds).
    pub fn last_sync(&self) -> Result<Option<i64>, String> {
        Ok(self.meta("last_sync")?.and_then(|v| v.parse().ok()))
    }

    pub fn set_last_sync(&self, at: i64) -> Result<(), String> {
        self.set_meta("last_sync", Some(at.to_string()))
    }

    /// Records source when new.
    pub fn ensure_source(&self, source: &str) -> Result<(), String> {
        self.db
            .execute(
                "INSERT INTO sources (source) VALUES (?1) ON CONFLICT DO NOTHING",
                [source],
            )
            .map(|_| ())
            .map_err(e)
    }

    pub fn set_label(&self, source: &str, label: &str) -> Result<(), String> {
        self.ensure_source(source)?;
        self.db
            .execute(
                "UPDATE sources SET label = ?2 WHERE source = ?1",
                params![source, label],
            )
            .map(|_| ())
            .map_err(e)
    }

    pub fn set_scanned(&self, source: &str, at: i64) -> Result<(), String> {
        self.ensure_source(source)?;
        self.db
            .execute(
                "UPDATE sources SET last_scan = ?2 WHERE source = ?1",
                params![source, at],
            )
            .map(|_| ())
            .map_err(e)
    }

    /// Source's custom roots (none: the parser's defaults).
    pub fn roots(&self, source: &str) -> Result<Option<Vec<String>>, String> {
        let r: Option<Option<String>> = self
            .db
            .query_row(
                "SELECT roots FROM sources WHERE source = ?1",
                [source],
                |r| r.get(0),
            )
            .optional()
            .map_err(e)?;
        Ok(r.flatten()
            .and_then(|s| serde_json::from_str::<Vec<String>>(&s).ok()))
    }

    pub fn set_roots(&self, source: &str, roots: Option<&[String]>) -> Result<(), String> {
        self.ensure_source(source)?;
        let v = roots.map(|r| serde_json::to_string(r).unwrap_or_default());
        self.db
            .execute(
                "UPDATE sources SET roots = ?2 WHERE source = ?1",
                params![source, v],
            )
            .map(|_| ())
            .map_err(e)
    }

    /// Every source the store knows, by ID.
    pub fn sources(&self) -> Result<Vec<Source>, String> {
        let mut st = self
            .db
            .prepare(
                "SELECT s.source, s.label, s.roots, s.last_scan,
                   (SELECT COUNT(*) FROM files f WHERE f.source = s.source),
                   (SELECT COUNT(*) FROM usage u JOIN files f ON f.id = u.file WHERE f.source = s.source)
                   + (SELECT COUNT(*) FROM events v WHERE v.source = s.source)
                 FROM sources s ORDER BY s.source",
            )
            .map_err(e)?;
        let rows = st
            .query_map([], |r| {
                let roots: Option<String> = r.get(2)?;
                Ok(Source {
                    id: r.get(0)?,
                    label: r.get(1)?,
                    roots: roots.and_then(|s| serde_json::from_str(&s).ok()),
                    last_scan: r.get(3)?,
                    files: r.get::<_, i64>(4)? as u64,
                    rows: r.get::<_, i64>(5)? as u64,
                })
            })
            .map_err(e)?;
        rows.collect::<Result<_, _>>().map_err(e)
    }

    /// Source's files' revisions: file ID to (size, modification time in ns).
    pub fn revisions(&self, source: &str) -> Result<HashMap<String, (u64, i64)>, String> {
        let mut st = self
            .db
            .prepare("SELECT file, size, mtime_ns FROM files WHERE source = ?1")
            .map_err(e)?;
        let rows = st
            .query_map([source], |r| {
                Ok((
                    r.get::<_, String>(0)?,
                    (r.get::<_, i64>(1)? as u64, r.get(2)?),
                ))
            })
            .map_err(e)?;
        rows.collect::<Result<_, _>>().map_err(e)
    }

    /// Replaces file's rows, references and revision with rows, events, size and mtime_ns, in one
    /// transaction: a reader sees the old rows or the new, never part of either. Each event is
    /// kept once per source by its key, with this read's values, and referenced by the file; an
    /// event the file referenced before and no file references now is deleted.
    pub fn replace_file(
        &mut self,
        source: &str,
        file: &str,
        size: u64,
        mtime_ns: i64,
        rows: &Rows,
        events: &Events,
    ) -> Result<(), String> {
        let tx = self.db.transaction().map_err(e)?;
        tx.execute(
            "INSERT INTO sources (source) VALUES (?1) ON CONFLICT DO NOTHING",
            [source],
        )
        .map_err(e)?;
        tx.execute(
            "INSERT INTO files (source, file, size, mtime_ns, present) VALUES (?1, ?2, ?3, ?4, 1)
             ON CONFLICT (source, file) DO UPDATE SET size = ?3, mtime_ns = ?4, present = 1",
            params![source, file, size as i64, mtime_ns],
        )
        .map_err(e)?;
        let id: i64 = tx
            .query_row(
                "SELECT id FROM files WHERE source = ?1 AND file = ?2",
                params![source, file],
                |r| r.get(0),
            )
            .map_err(e)?;
        tx.execute("DELETE FROM usage WHERE file = ?1", [id])
            .map_err(e)?;
        let before: Vec<i64> = {
            let mut st = tx
                .prepare("SELECT event FROM refs WHERE file = ?1")
                .map_err(e)?;
            let ids = st
                .query_map([id], |r| r.get(0))
                .map_err(e)?
                .collect::<Result<_, _>>()
                .map_err(e)?;
            ids
        };
        tx.execute("DELETE FROM refs WHERE file = ?1", [id])
            .map_err(e)?;
        {
            let mut st = tx
                .prepare("INSERT INTO usage VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10)")
                .map_err(e)?;
            for ((hour, model), t) in rows {
                let v = t.values().map(|n| n as i64);
                st.execute(params![
                    id, hour, model, v[0], v[1], v[2], v[3], v[4], v[5], v[6]
                ])
                .map_err(e)?;
            }
            let mut event = tx
                .prepare(
                    "INSERT INTO events (source, key, hour, model, input, output, cache_read,
                       cache_write, reasoning, total, samples)
                     VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)
                     ON CONFLICT (source, key) DO UPDATE SET hour = ?3, model = ?4, input = ?5,
                       output = ?6, cache_read = ?7, cache_write = ?8, reasoning = ?9,
                       total = ?10, samples = ?11
                     RETURNING id",
                )
                .map_err(e)?;
            let mut refer = tx
                .prepare("INSERT OR IGNORE INTO refs (file, event) VALUES (?1, ?2)")
                .map_err(e)?;
            for (key, (hour, model, t)) in events {
                let v = t.values().map(|n| n as i64);
                let event_id: i64 = event
                    .query_row(
                        params![source, key, hour, model, v[0], v[1], v[2], v[3], v[4], v[5], v[6]],
                        |r| r.get(0),
                    )
                    .map_err(e)?;
                refer.execute(params![id, event_id]).map_err(e)?;
            }
            let mut orphan = tx
                .prepare(
                    "DELETE FROM events WHERE id = ?1
                       AND NOT EXISTS (SELECT 1 FROM refs WHERE event = ?1)",
                )
                .map_err(e)?;
            for event_id in before {
                orphan.execute([event_id]).map_err(e)?;
            }
        }
        tx.commit().map_err(e)
    }

    /// Marks source's files not in listed as gone (their rows stay), and those in it as present.
    pub fn mark_present(&mut self, source: &str, listed: &HashSet<String>) -> Result<(), String> {
        let tx = self.db.transaction().map_err(e)?;
        {
            let mut ids = tx
                .prepare("SELECT id, file FROM files WHERE source = ?1")
                .map_err(e)?;
            let all: Vec<(i64, String)> = ids
                .query_map([source], |r| Ok((r.get(0)?, r.get(1)?)))
                .map_err(e)?
                .collect::<Result<_, _>>()
                .map_err(e)?;
            let mut set = tx
                .prepare("UPDATE files SET present = ?2 WHERE id = ?1")
                .map_err(e)?;
            for (id, file) in all {
                set.execute(params![id, listed.contains(&file)])
                    .map_err(e)?;
            }
        }
        tx.commit().map_err(e)
    }

    /// Rows from hour from to hour to (UTC, inclusive), summed per source, hour and model.
    pub fn rows(&self, from: i64, to: i64) -> Result<Vec<(String, i64, String, Tokens)>, String> {
        let mut st = self
            .db
            .prepare(&format!(
                "SELECT source, hour, model, SUM(input), SUM(output), SUM(cache_read),
                   SUM(cache_write), SUM(reasoning), SUM(total), SUM(samples)
                 FROM {ALL_ROWS}
                 WHERE hour >= ?1 AND hour <= ?2
                 GROUP BY source, hour, model"
            ))
            .map_err(e)?;
        let rows = st
            .query_map([from, to], |r| {
                let mut v = [0u64; 7];
                for (i, n) in v.iter_mut().enumerate() {
                    *n = r.get::<_, i64>(3 + i)?.max(0) as u64;
                }
                Ok((r.get(0)?, r.get(1)?, r.get(2)?, Tokens::from_values(v)))
            })
            .map_err(e)?;
        rows.collect::<Result<_, _>>().map_err(e)
    }

    /// Every hour's total per source, from the first row on.
    pub fn hourly_totals(&self) -> Result<Vec<(String, i64, u64)>, String> {
        let mut st = self
            .db
            .prepare(&format!(
                "SELECT source, hour, SUM(total) FROM {ALL_ROWS} GROUP BY source, hour"
            ))
            .map_err(e)?;
        let rows = st
            .query_map([], |r| {
                Ok((r.get(0)?, r.get(1)?, r.get::<_, i64>(2)?.max(0) as u64))
            })
            .map_err(e)?;
        rows.collect::<Result<_, _>>().map_err(e)
    }

    /// The first hour any row has.
    pub fn first_hour(&self) -> Result<Option<i64>, String> {
        self.db
            .query_row(
                "SELECT MIN(hour) FROM (SELECT MIN(hour) AS hour FROM usage
                   UNION ALL SELECT MIN(hour) FROM events)",
                [],
                |r| r.get(0),
            )
            .map_err(e)
    }

    /// Deletes every row and file revision and the last sync time; sources keep their labels and
    /// roots, and the settings stay. The next sync reads every file again.
    pub fn clear(&mut self) -> Result<(), String> {
        let tx = self.db.transaction().map_err(e)?;
        tx.execute_batch(
            "DELETE FROM refs; DELETE FROM events; DELETE FROM usage; DELETE FROM files;
             UPDATE sources SET last_scan = NULL;
             DELETE FROM meta WHERE key = 'last_sync';",
        )
        .map_err(e)?;
        tx.commit().map_err(e)?;
        self.db.execute_batch("VACUUM").map_err(e)
    }

    /// The store's size on disk (with its journal), its rows (a file's rows and each keyed event
    /// once) and files.
    pub fn storage(&self) -> Result<(u64, u64, u64), String> {
        let mut bytes = 0;
        for suffix in ["", "-journal", "-wal", "-shm"] {
            let mut p = self.path.clone().into_os_string();
            p.push(suffix);
            bytes += std::fs::metadata(&p).map_or(0, |m| m.len());
        }
        let count = |sql: &str| -> Result<u64, String> {
            self.db
                .query_row(sql, [], |r| r.get::<_, i64>(0))
                .map(|n| n as u64)
                .map_err(e)
        };
        Ok((
            bytes,
            count("SELECT COUNT(*) FROM usage")? + count("SELECT COUNT(*) FROM events")?,
            count("SELECT COUNT(*) FROM files")?,
        ))
    }
}
