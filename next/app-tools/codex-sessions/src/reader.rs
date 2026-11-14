//! Reads Codex's local sessions from a Codex home, read-only: the session list (SQLite databases,
//! rollout file names and `session_index.jsonl`, merged) and one session's detail (its rollout's
//! messages). The behaviour follows the old desktop app's session manager; see the README.

use rusqlite::{Connection, OpenFlags};
use serde_json::{json, Value};
use std::collections::{HashMap, HashSet};
use std::ffi::OsStr;
use std::fs;
use std::io::{BufRead, BufReader, Read};
use std::path::{Path, PathBuf};
use std::time::{Duration, UNIX_EPOCH};

pub const DEFAULT_PAGE_SIZE: usize = 50;
pub const MAX_PAGE_SIZE: usize = 100;
const SQLITE_BUSY_TIMEOUT: Duration = Duration::from_secs(5);
const SESSION_DIRS: [&str; 2] = ["sessions", "archived_sessions"];
/// Session IDs longer than this are refused.
const MAX_ID: usize = 128;
/// The most of one message's text a detail page holds, in bytes; the rest is cut.
pub const MAX_MESSAGE: usize = 8 << 10;
/// The most a detail page's messages take as JSON, well under the host's 1 MiB frame.
pub const MAX_DETAIL: usize = 512 << 10;
/// Rollout lines longer than this are counted, not parsed.
const MAX_LINE: usize = 16 << 20;

/// One session in the list.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Summary {
    pub id: String,
    pub title: String,
    pub cwd: String,
    pub provider: String,
    pub archived: bool,
    pub updated_ms: Option<i64>,
    /// The database it came from; empty for a rollout file.
    pub database: String,
}

impl Summary {
    fn to_json(&self) -> Value {
        json!({
            "id": self.id, "title": self.title, "cwd": self.cwd, "provider": self.provider,
            "archived": self.archived, "updated_ms": self.updated_ms, "database": self.database,
        })
    }
}

/// One page of the list.
#[derive(Debug)]
pub struct Page {
    pub codex_home: PathBuf,
    pub databases: Vec<PathBuf>,
    pub sessions: Vec<Summary>,
    pub total: usize,
    pub offset: usize,
    pub limit: usize,
    pub has_more: bool,
    pub warnings: Vec<String>,
}

impl Page {
    pub fn to_json(&self) -> Value {
        let active = self.sessions.iter().filter(|s| !s.archived).count();
        json!({
            "codex_home": self.codex_home,
            "databases": self.databases,
            "sessions": self.sessions.iter().map(Summary::to_json).collect::<Vec<_>>(),
            "total": self.total,
            "offset": self.offset,
            "limit": self.limit,
            "has_more": self.has_more,
            "active": active,
            "archived": self.sessions.len() - active,
            "warnings": self.warnings,
        })
    }
}

/// Opens a database read-only. A WAL database whose shared-memory file is missing (no Codex
/// process has it open) is opened as immutable, so that reading it creates no `-wal` or `-shm`
/// file beside it.
fn open_read_only(path: &Path) -> Result<Connection, String> {
    let mut uri = String::from("file:");
    for b in path.as_os_str().as_encoded_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' | b'/' => {
                uri.push(*b as char)
            }
            _ => uri.push_str(&format!("%{b:02X}")),
        }
    }
    uri.push_str("?mode=ro");
    if is_wal(path) && !sidecar(path, "-shm").exists() {
        uri.push_str("&immutable=1");
    }
    let connection = Connection::open_with_flags(
        &uri,
        OpenFlags::SQLITE_OPEN_READ_ONLY
            | OpenFlags::SQLITE_OPEN_NO_MUTEX
            | OpenFlags::SQLITE_OPEN_URI,
    )
    .map_err(|e| format!("Failed to open database {}: {e}", path.display()))?;
    connection
        .busy_timeout(SQLITE_BUSY_TIMEOUT)
        .map_err(|e| format!("Failed to set database timeout {}: {e}", path.display()))?;
    Ok(connection)
}

fn sidecar(path: &Path, suffix: &str) -> PathBuf {
    let mut s = path.as_os_str().to_os_string();
    s.push(suffix);
    PathBuf::from(s)
}

/// Whether path is a SQLite database in WAL mode, by its header.
fn is_wal(path: &Path) -> bool {
    let mut header = [0u8; 20];
    fs::File::open(path)
        .and_then(|mut f| f.read_exact(&mut header))
        .is_ok_and(|_| &header[..16] == b"SQLite format 3\0" && header[18] == 2)
}

fn sqlite_candidate(path: &Path) -> bool {
    path.extension().and_then(OsStr::to_str).is_some_and(|e| {
        ["db", "sqlite", "sqlite3"]
            .iter()
            .any(|c| e.eq_ignore_ascii_case(c))
    })
}

/// The databases with a sessions table, in the old app's order (codex-dev.db first, the legacy
/// state_5.sqlite last), and a warning for each that could not be inspected.
pub fn discover_databases(codex_home: &Path) -> (Vec<PathBuf>, Vec<String>) {
    let mut candidates = Vec::new();
    let mut warnings = Vec::new();
    if let Ok(entries) = fs::read_dir(codex_home.join("sqlite")) {
        for entry in entries.flatten() {
            let path = entry.path();
            if path.is_file() && sqlite_candidate(&path) {
                candidates.push(path);
            }
        }
    }
    let legacy = codex_home.join("state_5.sqlite");
    if legacy.is_file() && !candidates.contains(&legacy) {
        candidates.push(legacy.clone());
    }
    candidates.sort_by_key(|path| {
        (
            path == &legacy,
            path.file_name().and_then(OsStr::to_str) != Some("codex-dev.db"),
            path.file_name().map(OsStr::to_os_string),
        )
    });
    let mut found = Vec::new();
    'paths: for path in candidates {
        let connection = match open_read_only(&path) {
            Ok(c) => c,
            Err(e) => {
                warnings.push(e);
                continue;
            }
        };
        let mut supported = false;
        for table in ["threads", "automation_runs", "inbox_items"] {
            match table_exists(&connection, table) {
                Ok(true) => supported = true,
                Ok(false) => {}
                Err(e) => {
                    warnings.push(format!(
                        "Failed to inspect database schema {}: {e}",
                        path.display()
                    ));
                    continue 'paths;
                }
            }
        }
        if supported {
            found.push(path);
        }
    }
    (found, warnings)
}

fn table_exists(connection: &Connection, table: &str) -> Result<bool, String> {
    match connection.query_row(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?1 LIMIT 1",
        [table],
        |_| Ok(()),
    ) {
        Ok(()) => Ok(true),
        Err(rusqlite::Error::QueryReturnedNoRows) => Ok(false),
        Err(e) => Err(format!("Failed to read SQLite table list: {e}")),
    }
}

fn table_columns(connection: &Connection, table: &str) -> Result<HashSet<String>, String> {
    if !table_exists(connection, table)? {
        return Ok(HashSet::new());
    }
    let mut statement = connection
        .prepare(&format!(
            "PRAGMA table_info(\"{}\")",
            table.replace('"', "\"\"")
        ))
        .map_err(|e| format!("Failed to read {table} table schema: {e}"))?;
    let rows = statement
        .query_map([], |row| row.get::<_, String>(1))
        .map_err(|e| format!("Failed to read {table} table schema: {e}"))?;
    rows.collect::<rusqlite::Result<HashSet<_>>>()
        .map_err(|e| format!("Failed to read {table} table schema: {e}"))
}

fn optional_column<'a>(columns: &HashSet<String>, column: &'a str, fallback: &'a str) -> &'a str {
    if columns.contains(column) {
        column
    } else {
        fallback
    }
}

/// Seconds or milliseconds in column, as milliseconds.
fn millis_expression(column: &str) -> String {
    format!(
        "CASE WHEN {column} IS NULL THEN NULL WHEN ABS({column}) < 100000000000 THEN CAST({column} * 1000 AS INTEGER) ELSE CAST({column} AS INTEGER) END"
    )
}

/// The `local:` prefix Codex puts on some thread IDs, removed.
pub fn normalize_id(value: &str) -> String {
    let v = value.trim();
    v.strip_prefix("local:").unwrap_or(v).trim().to_string()
}

fn identity_key(value: &str) -> String {
    let n = normalize_id(value);
    if n.is_empty() {
        value.trim().to_string()
    } else {
        n
    }
}

/// A page of the list: offset sessions in, at most limit (1 to 100), newest first.
pub fn list(codex_home: &Path, offset: usize, requested: usize) -> Page {
    let limit = requested.clamp(1, MAX_PAGE_SIZE);
    let fetch = offset.saturating_add(limit).saturating_add(1);
    let (databases, mut warnings) = discover_databases(codex_home);
    let mut sessions = Vec::new();
    let mut ids = HashSet::new();
    for path in &databases {
        match database_sessions(path, fetch) {
            Ok(mut items) => sessions.append(&mut items),
            Err(e) => warnings.push(e),
        }
        match database_ids(path) {
            Ok(found) => ids.extend(found),
            Err(e) => warnings.push(e),
        }
    }
    let (mut rollouts, rollout_warnings) = rollout_sessions(codex_home, &ids);
    sessions.append(&mut rollouts);
    warnings.extend(rollout_warnings);
    ids.extend(
        sessions
            .iter()
            .map(|s| identity_key(&s.id))
            .filter(|id| !id.is_empty()),
    );
    let mut sessions = merge(sessions);
    sessions.sort_by(|a, b| {
        b.updated_ms
            .cmp(&a.updated_ms)
            .then_with(|| b.id.cmp(&a.id))
            .then_with(|| a.database.cmp(&b.database))
    });
    let has_more = sessions.len() > offset.saturating_add(limit);
    Page {
        codex_home: codex_home.to_path_buf(),
        databases,
        sessions: sessions.into_iter().skip(offset).take(limit).collect(),
        total: ids.len(),
        offset,
        limit,
        has_more,
        warnings,
    }
}

/// Sessions with the same ID (without `local:`) made one: the newer wins, and the other fills its
/// empty fields.
fn merge(sessions: Vec<Summary>) -> Vec<Summary> {
    let mut merged = Vec::<Summary>::new();
    let mut at = HashMap::<String, usize>::new();
    for s in sessions {
        let key = identity_key(&s.id);
        let Some(&i) = at.get(&key) else {
            at.insert(key, merged.len());
            merged.push(s);
            continue;
        };
        let existing = &mut merged[i];
        if s.updated_ms > existing.updated_ms {
            let mut next = s;
            fill(&mut next, existing);
            *existing = next;
        } else {
            fill(existing, &s);
        }
    }
    merged
}

fn fill(target: &mut Summary, from: &Summary) {
    for (t, f) in [
        (&mut target.title, &from.title),
        (&mut target.cwd, &from.cwd),
        (&mut target.provider, &from.provider),
        (&mut target.database, &from.database),
    ] {
        if t.trim().is_empty() {
            t.clone_from(f);
        }
    }
    if target.updated_ms.is_none() {
        target.updated_ms = from.updated_ms;
    }
}

/// The rollout files' sessions whose ID no database has, named by their file names (never
/// read), with the index's title and time, else the file's modification time.
fn rollout_sessions(codex_home: &Path, known: &HashSet<String>) -> (Vec<Summary>, Vec<String>) {
    let mut warnings = Vec::new();
    let index = index_metadata(codex_home).unwrap_or_else(|e| {
        warnings.push(e);
        HashMap::new()
    });
    let paths = match rollout_files(codex_home) {
        Ok(p) => p,
        Err(e) => {
            warnings.push(e);
            return (Vec::new(), warnings);
        }
    };
    let mut sessions = Vec::new();
    for path in paths {
        let Some(id) = rollout_id(&path) else {
            continue;
        };
        if known.contains(&identity_key(&id)) {
            continue;
        }
        let meta = index.get(&id).or_else(|| index.get(&normalize_id(&id)));
        let updated_ms = meta.and_then(|m| m.1).or_else(|| {
            fs::metadata(&path)
                .and_then(|m| m.modified())
                .ok()
                .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
                .and_then(|d| i64::try_from(d.as_millis()).ok())
        });
        sessions.push(Summary {
            title: meta.map(|m| m.0.clone()).unwrap_or_default(),
            cwd: String::new(),
            provider: String::new(),
            archived: path.starts_with(codex_home.join("archived_sessions")),
            updated_ms,
            database: String::new(),
            id,
        });
    }
    (sessions, warnings)
}

/// `session_index.jsonl`: each ID's thread name and update time.
fn index_metadata(codex_home: &Path) -> Result<HashMap<String, (String, Option<i64>)>, String> {
    let path = codex_home.join("session_index.jsonl");
    if !path.is_file() {
        return Ok(HashMap::new());
    }
    let content =
        fs::read(&path).map_err(|e| format!("Failed to read session_index.jsonl: {e}"))?;
    let content = String::from_utf8_lossy(&content);
    let mut metadata = HashMap::new();
    for line in content.lines() {
        let Ok(record) = serde_json::from_str::<Value>(line) else {
            continue;
        };
        let Some(id) = record["id"]
            .as_str()
            .map(str::trim)
            .filter(|id| !id.is_empty())
        else {
            continue;
        };
        let item = (
            record["thread_name"]
                .as_str()
                .unwrap_or_default()
                .to_string(),
            record["updated_at"].as_str().and_then(parse_timestamp_ms),
        );
        metadata.insert(id.to_string(), item.clone());
        let n = normalize_id(id);
        if !n.is_empty() {
            metadata.entry(n).or_insert(item);
        }
    }
    Ok(metadata)
}

/// Seconds or milliseconds as digits, or an RFC 3339 time, as milliseconds since 1970.
pub fn parse_timestamp_ms(value: &str) -> Option<i64> {
    let value = value.trim();
    if value.is_empty() {
        return None;
    }
    if let Ok(v) = value.parse::<i64>() {
        return Some(if v.abs() < 100_000_000_000 {
            v.saturating_mul(1000)
        } else {
            v
        });
    }
    rfc3339_ms(value)
}

/// `YYYY-MM-DDTHH:MM:SS[.fraction](Z|±HH:MM)` as milliseconds since 1970.
fn rfc3339_ms(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    let num = |r: std::ops::Range<usize>| -> Option<i64> {
        let part = s.get(r)?;
        part.bytes()
            .all(|c| c.is_ascii_digit())
            .then(|| part.parse().ok())?
    };
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't' | b' ') {
        return None;
    }
    if b[13] != b':' || b[16] != b':' {
        return None;
    }
    let (year, month, day) = (num(0..4)?, num(5..7)?, num(8..10)?);
    let (hour, minute, second) = (num(11..13)?, num(14..16)?, num(17..19)?);
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 60
    {
        return None;
    }
    let mut i = 19;
    let mut millis = 0i64;
    if b.get(i) == Some(&b'.') {
        i += 1;
        let start = i;
        while b.get(i).is_some_and(u8::is_ascii_digit) {
            i += 1;
        }
        if i == start {
            return None;
        }
        let digits = &s[start..i.min(start + 3)];
        millis = digits.parse::<i64>().ok()? * 10i64.pow(3 - digits.len() as u32);
    }
    let offset = match b.get(i)? {
        b'Z' | b'z' if i + 1 == b.len() => 0,
        sign @ (b'+' | b'-') if i + 6 == b.len() && b[i + 3] == b':' => {
            let m = num(i + 1..i + 3)? * 60 + num(i + 4..i + 6)?;
            if *sign == b'+' {
                m
            } else {
                -m
            }
        }
        _ => return None,
    };
    // Days from 1970-01-01 to the civil date (Howard Hinnant's algorithm).
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    let days = era * 146_097 + doe - 719_468;
    let seconds = days * 86_400 + hour * 3600 + minute * 60 + second - offset * 60;
    Some(seconds * 1000 + millis)
}

fn database_sessions(path: &Path, limit: usize) -> Result<Vec<Summary>, String> {
    let connection = open_read_only(path)?;
    let mut sessions = Vec::new();
    if table_exists(&connection, "threads")? {
        sessions.extend(thread_rows(&connection, path, limit)?);
    }
    if table_exists(&connection, "automation_runs")? {
        sessions.extend(automation_rows(&connection, path, limit)?);
    }
    Ok(sessions)
}

fn database_ids(path: &Path) -> Result<HashSet<String>, String> {
    let connection = open_read_only(path)?;
    let mut ids = HashSet::new();
    for (table, column) in [("threads", "id"), ("automation_runs", "thread_id")] {
        if !table_columns(&connection, table)?.contains(column) {
            continue;
        }
        let failed =
            |e: rusqlite::Error| format!("Failed to count sessions {}: {e}", path.display());
        let mut statement = connection
            .prepare(&format!(
                "SELECT DISTINCT \"{column}\" FROM \"{table}\" WHERE COALESCE(\"{column}\", '') <> ''"
            ))
            .map_err(failed)?;
        let rows = statement
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(failed)?;
        for id in rows.collect::<rusqlite::Result<Vec<_>>>().map_err(failed)? {
            let id = identity_key(&id);
            if !id.is_empty() {
                ids.insert(id);
            }
        }
    }
    Ok(ids)
}

fn thread_rows(connection: &Connection, path: &Path, limit: usize) -> Result<Vec<Summary>, String> {
    let columns = table_columns(connection, "threads")?;
    if !columns.contains("id") {
        return Ok(Vec::new());
    }
    let title = if columns.contains("title") && columns.contains("name") {
        "COALESCE(NULLIF(title, ''), NULLIF(name, ''), '')"
    } else {
        optional_column(&columns, "title", optional_column(&columns, "name", "''"))
    };
    let cwd = optional_column(&columns, "cwd", "''");
    let provider = optional_column(&columns, "model_provider", "''");
    let archived = optional_column(&columns, "archived", "0");
    let updated = if columns.contains("updated_at_ms") {
        "updated_at_ms".to_string()
    } else if columns.contains("updated_at") {
        millis_expression("updated_at")
    } else if columns.contains("created_at_ms") {
        "created_at_ms".to_string()
    } else if columns.contains("created_at") {
        millis_expression("created_at")
    } else {
        "NULL".to_string()
    };
    let failed =
        |e: rusqlite::Error| format!("Failed to read session database {}: {e}", path.display());
    let mut statement = connection
        .prepare(&format!(
            "SELECT id, {title}, {cwd}, {provider}, {archived}, {updated} FROM threads ORDER BY COALESCE({updated}, 0) DESC, id DESC LIMIT ?1"
        ))
        .map_err(failed)?;
    let database = path.to_string_lossy().to_string();
    let rows = statement
        .query_map([i64::try_from(limit).unwrap_or(i64::MAX)], |row| {
            Ok(Summary {
                id: row.get(0)?,
                title: row.get::<_, Option<String>>(1)?.unwrap_or_default(),
                cwd: row.get::<_, Option<String>>(2)?.unwrap_or_default(),
                provider: row.get::<_, Option<String>>(3)?.unwrap_or_default(),
                archived: row.get::<_, Option<i64>>(4)?.unwrap_or_default() != 0,
                updated_ms: row.get(5)?,
                database: database.clone(),
            })
        })
        .map_err(failed)?;
    rows.collect::<rusqlite::Result<Vec<_>>>().map_err(failed)
}

fn automation_rows(
    connection: &Connection,
    path: &Path,
    limit: usize,
) -> Result<Vec<Summary>, String> {
    let columns = table_columns(connection, "automation_runs")?;
    if !columns.contains("thread_id") {
        return Ok(Vec::new());
    }
    let title = optional_column(&columns, "thread_title", "''");
    let cwd = optional_column(&columns, "source_cwd", "''");
    let status = optional_column(&columns, "status", "''");
    let updated = if columns.contains("updated_at") {
        millis_expression("updated_at")
    } else if columns.contains("created_at") {
        millis_expression("created_at")
    } else {
        "NULL".to_string()
    };
    let failed = |e: rusqlite::Error| {
        format!(
            "Failed to read automation session database {}: {e}",
            path.display()
        )
    };
    let mut statement = connection
        .prepare(&format!(
            "SELECT thread_id, {title}, {cwd}, {status}, {updated} FROM automation_runs WHERE COALESCE(thread_id, '') <> '' ORDER BY COALESCE({updated}, 0) DESC, thread_id DESC LIMIT ?1"
        ))
        .map_err(failed)?;
    let database = path.to_string_lossy().to_string();
    let rows = statement
        .query_map([i64::try_from(limit).unwrap_or(i64::MAX)], |row| {
            let status = row.get::<_, Option<String>>(3)?.unwrap_or_default();
            Ok(Summary {
                id: row.get(0)?,
                title: row.get::<_, Option<String>>(1)?.unwrap_or_default(),
                cwd: row.get::<_, Option<String>>(2)?.unwrap_or_default(),
                provider: String::new(),
                archived: status.eq_ignore_ascii_case("archived"),
                updated_ms: row.get(4)?,
                database: database.clone(),
            })
        })
        .map_err(failed)?;
    rows.collect::<rusqlite::Result<Vec<_>>>().map_err(failed)
}

/// The `.jsonl` files under sessions/ and archived_sessions/, sorted; links are skipped.
pub fn rollout_files(codex_home: &Path) -> Result<Vec<PathBuf>, String> {
    fn walk(root: &Path, files: &mut Vec<PathBuf>) -> Result<(), String> {
        if !root.exists() {
            return Ok(());
        }
        let entries = fs::read_dir(root)
            .map_err(|e| format!("Failed to scan session directory {}: {e}", root.display()))?;
        for entry in entries {
            let entry =
                entry.map_err(|e| format!("Failed to read session directory entry: {e}"))?;
            let kind = entry
                .file_type()
                .map_err(|e| format!("Failed to read session directory entry type: {e}"))?;
            let path = entry.path();
            if kind.is_symlink() {
                continue;
            }
            if kind.is_dir() {
                walk(&path, files)?;
            } else if kind.is_file() && path.extension().and_then(OsStr::to_str) == Some("jsonl") {
                files.push(path);
            }
        }
        Ok(())
    }
    let mut files = Vec::new();
    for d in SESSION_DIRS {
        walk(&codex_home.join(d), &mut files)?;
    }
    files.sort();
    Ok(files)
}

/// The session ID a rollout's file name ends with: `rollout-…<UUID>.jsonl`.
pub fn rollout_id(path: &Path) -> Option<String> {
    let name = path.file_name()?.to_str()?;
    let stem = name.strip_prefix("rollout-")?.strip_suffix(".jsonl")?;
    let candidate = stem.get(stem.len().checked_sub(36)?..)?;
    candidate
        .chars()
        .enumerate()
        .all(|(i, c)| match i {
            8 | 13 | 18 | 23 => c == '-',
            _ => c.is_ascii_hexdigit(),
        })
        .then(|| candidate.to_string())
}

/// A session ID as detail accepts it: `local:` removed, 1 to 128 letters, digits, `-` or `_`.
pub fn validate_id(id: &str) -> Result<String, String> {
    let id = normalize_id(id);
    if id.is_empty() {
        return Err("Session ID cannot be empty".into());
    }
    if id.len() > MAX_ID {
        return Err("Session ID exceeds maximum length".into());
    }
    if !id
        .chars()
        .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
    {
        return Err("Session ID contains disallowed characters".into());
    }
    Ok(id)
}

/// path (relative to the Codex home, or absolute), resolved, when it is a `.jsonl` file inside
/// the Codex home's sessions/ or archived_sessions/.
fn validated_rollout(codex_home: &Path, path: &Path) -> Option<PathBuf> {
    let candidate = if path.is_absolute() {
        path.to_path_buf()
    } else {
        codex_home.join(path)
    };
    if !candidate.is_file() {
        return None;
    }
    let home = fs::canonicalize(codex_home).ok()?;
    let real = fs::canonicalize(&candidate).ok()?;
    SESSION_DIRS.iter().find_map(|d| {
        let root = fs::canonicalize(codex_home.join(d)).ok()?;
        (root.starts_with(&home)
            && real.starts_with(&root)
            && real.extension().and_then(OsStr::to_str) == Some("jsonl"))
        .then(|| real.clone())
    })
}

/// The rollout whose file name ends with id, inside the Codex home.
pub fn find_rollout(codex_home: &Path, id: &str) -> Option<PathBuf> {
    rollout_files(codex_home)
        .ok()?
        .iter()
        .find(|p| rollout_id(p).as_deref() == Some(id))
        .and_then(|p| validated_rollout(codex_home, p))
}

/// One message of a session.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Message {
    pub line: usize,
    pub role: String,
    pub timestamp: Option<String>,
    pub content: String,
    pub truncated: bool,
    pub call_id: Option<String>,
}

impl Message {
    fn to_json(&self) -> Value {
        json!({
            "line": self.line, "role": self.role, "timestamp": self.timestamp,
            "content": self.content, "truncated": self.truncated, "call_id": self.call_id,
        })
    }
}

/// What detail found about a session's rollout.
#[derive(Default)]
struct Counts {
    lines: usize,
    user: usize,
    assistant: usize,
    tool: usize,
    reasoning: usize,
    size: u64,
}

/// A message's text cut to MAX_MESSAGE bytes at a character boundary, and whether it was cut.
fn capped(text: String) -> (String, bool) {
    if text.len() <= MAX_MESSAGE {
        return (text, false);
    }
    let mut end = MAX_MESSAGE;
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    (text[..end].to_string(), true)
}

fn content_text(content: &Value) -> String {
    match content {
        Value::String(t) => t.clone(),
        Value::Array(a) => a
            .iter()
            .filter_map(|i| i.get("text").and_then(Value::as_str).or_else(|| i.as_str()))
            .collect::<Vec<_>>()
            .join("\n"),
        Value::Object(o) => o
            .get("text")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string(),
        _ => String::new(),
    }
}

fn reasoning_text(payload: &Value) -> String {
    if let Some(t) = payload.get("thinking").and_then(Value::as_str) {
        return t.to_string();
    }
    for field in ["summary_text", "raw_content", "summary", "content"] {
        if let Some(v) = payload.get(field) {
            let t = content_text(v);
            if !t.is_empty() {
                return t;
            }
        }
    }
    if payload.get("encrypted_content").is_some() {
        return "[Encrypted reasoning block]".into();
    }
    String::new()
}

fn is_tool(kind: &str) -> bool {
    matches!(kind, "function_call" | "function_call_output") || kind.contains("tool")
}

/// Reads one line into buf, at most MAX_LINE bytes of it (the rest is skipped): the bytes the
/// line had, 0 at the end, and whether it was longer than MAX_LINE.
fn read_line(r: &mut impl BufRead, buf: &mut Vec<u8>) -> std::io::Result<(usize, bool)> {
    buf.clear();
    let (mut total, mut long) = (0, false);
    loop {
        let chunk = r.fill_buf()?;
        if chunk.is_empty() {
            return Ok((total, long));
        }
        let (used, done) = match chunk.iter().position(|&b| b == b'\n') {
            Some(i) => (i + 1, true),
            None => (chunk.len(), false),
        };
        if buf.len() + used <= MAX_LINE {
            buf.extend_from_slice(&chunk[..used]);
        } else {
            long = true;
        }
        total += used;
        r.consume(used);
        if done {
            return Ok((total, long));
        }
    }
}

/// One session's detail: its metadata and a page of its messages, from offset, at most limit
/// (1 to 100) and at most MAX_DETAIL bytes of them as JSON. cancelled is asked now and then.
pub fn detail(
    codex_home: &Path,
    raw_id: &str,
    offset: usize,
    limit: usize,
    cancelled: &dyn Fn() -> bool,
) -> Result<Value, DetailError> {
    let id = validate_id(raw_id).map_err(DetailError::Invalid)?;
    let limit = limit.clamp(1, MAX_PAGE_SIZE);
    let (databases, _) = discover_databases(codex_home);
    let (mut title, mut cwd, mut provider) = (String::new(), String::new(), String::new());
    let mut model = None;
    let mut archived = false;
    let mut updated_ms = None;
    let mut database = None;
    let mut rollout = None;
    for path in &databases {
        let Ok(c) = open_read_only(path) else {
            continue;
        };
        if !matches!(table_exists(&c, "threads"), Ok(true)) {
            continue;
        }
        let columns = table_columns(&c, "threads").unwrap_or_default();
        let title_col = optional_column(&columns, "title", optional_column(&columns, "name", "id"));
        let query = format!(
            "SELECT {title_col}, {}, {}, {}, {}, {}, {} FROM threads WHERE id = ?1 LIMIT 1",
            optional_column(&columns, "cwd", "''"),
            optional_column(&columns, "model_provider", "''"),
            optional_column(&columns, "model", "''"),
            optional_column(&columns, "archived", "0"),
            optional_column(&columns, "rollout_path", "''"),
            optional_column(
                &columns,
                "updated_at_ms",
                optional_column(&columns, "updated_at", "NULL")
            ),
        );
        let Ok(mut statement) = c.prepare(&query) else {
            continue;
        };
        let Ok(mut rows) = statement.query([&id]) else {
            continue;
        };
        let Ok(Some(row)) = rows.next() else { continue };
        database = Some(path.to_string_lossy().to_string());
        for (i, field) in [(0, &mut title), (1, &mut cwd), (2, &mut provider)] {
            if let Ok(Some(v)) = row.get::<_, Option<String>>(i) {
                if !v.trim().is_empty() {
                    *field = v;
                }
            }
        }
        if let Ok(Some(v)) = row.get::<_, Option<String>>(3) {
            if !v.trim().is_empty() {
                model = Some(v);
            }
        }
        if let Ok(Some(v)) = row.get::<_, Option<i64>>(4) {
            archived = v != 0;
        }
        if let Ok(Some(v)) = row.get::<_, Option<String>>(5) {
            if !v.trim().is_empty() {
                rollout = validated_rollout(codex_home, Path::new(&v));
            }
        }
        if let Ok(Some(v)) = row.get::<_, Option<i64>>(6) {
            updated_ms = Some(if v < 100_000_000_000 {
                v.saturating_mul(1000)
            } else {
                v
            });
        }
        break;
    }
    if rollout.is_none() {
        rollout = find_rollout(codex_home, &id);
    }
    if title.is_empty() {
        if let Some((t, _)) = index_metadata(codex_home)
            .ok()
            .and_then(|m| m.get(&id).cloned())
        {
            title = t;
        }
    }
    if let Some(path) = &rollout {
        let archived_root = fs::canonicalize(codex_home.join("archived_sessions")).ok();
        archived = archived || archived_root.is_some_and(|r| path.starts_with(r));
    }

    let mut counts = Counts::default();
    let mut messages = Vec::new();
    let mut total = 0usize;
    let mut used = 0usize;
    let mut full = false;
    let mut rollout_model = None;
    if let Some(path) = &rollout {
        let file = fs::File::open(path).map_err(|e| {
            DetailError::Unavailable(format!("Failed to read rollout {}: {e}", path.display()))
        })?;
        let mut reader = BufReader::new(file);
        let mut line = Vec::new();
        loop {
            if counts.lines % 1024 == 0 && cancelled() {
                return Err(DetailError::Cancelled);
            }
            let (read, long) = read_line(&mut reader, &mut line).map_err(|e| {
                DetailError::Unavailable(format!("Failed to read rollout {}: {e}", path.display()))
            })?;
            if read == 0 {
                break;
            }
            counts.size += read as u64;
            let index = counts.lines;
            counts.lines += 1;
            if long {
                continue;
            }
            let Ok(record) = serde_json::from_slice::<Value>(&line) else {
                continue;
            };
            let payload = &record["payload"];
            match record["type"].as_str().unwrap_or_default() {
                "session_meta" if payload.is_object() => {
                    let pick = |keys: &[&str]| {
                        keys.iter()
                            .find_map(|k| payload.get(*k).and_then(Value::as_str))
                            .unwrap_or_default()
                            .to_string()
                    };
                    if title.is_empty() {
                        title = pick(&["title", "name"]);
                    }
                    if cwd.is_empty() {
                        cwd = pick(&["cwd"]);
                    }
                    if provider.is_empty() {
                        provider = pick(&["model_provider"]);
                    }
                }
                "turn_context" if payload.is_object() => {
                    if let Some(m) = payload["model"].as_str() {
                        rollout_model = Some(m.to_string());
                    }
                    if cwd.is_empty() {
                        cwd = payload["cwd"].as_str().unwrap_or_default().to_string();
                    }
                }
                "response_item" if payload.is_object() => {
                    let kind = payload["type"].as_str().unwrap_or_default();
                    let (role, text) = match kind {
                        "message" => {
                            let role = payload["role"].as_str().unwrap_or("unknown");
                            match role {
                                "user" => counts.user += 1,
                                "assistant" => counts.assistant += 1,
                                _ => {}
                            }
                            (role.to_string(), content_text(&payload["content"]))
                        }
                        "reasoning" | "thinking" => {
                            counts.reasoning += 1;
                            ("reasoning".to_string(), reasoning_text(payload))
                        }
                        k if is_tool(k) => {
                            counts.tool += 1;
                            let text = if k.ends_with("_output") {
                                payload
                                    .get("output")
                                    .map(|o| {
                                        o.as_str()
                                            .map(str::to_string)
                                            .unwrap_or_else(|| o.to_string())
                                    })
                                    .unwrap_or_default()
                            } else {
                                let name = payload["name"].as_str().unwrap_or("unknown_tool");
                                let args = payload
                                    .get("arguments")
                                    .or_else(|| payload.get("input"))
                                    .and_then(Value::as_str)
                                    .unwrap_or_default();
                                format!("{name}({args})")
                            };
                            ("tool".to_string(), text)
                        }
                        _ => continue,
                    };
                    let n = total;
                    total += 1;
                    if n < offset || n >= offset + limit || full {
                        continue;
                    }
                    let (content, truncated) = capped(text);
                    let m = Message {
                        line: index,
                        call_id: (role == "tool")
                            .then(|| payload["call_id"].as_str().map(str::to_string))
                            .flatten(),
                        timestamp: record["timestamp"].as_str().map(str::to_string),
                        role,
                        content,
                        truncated,
                    };
                    let size = m.to_json().to_string().len();
                    if !messages.is_empty() && used + size > MAX_DETAIL {
                        full = true;
                        continue;
                    }
                    used += size;
                    messages.push(m);
                }
                _ => {}
            }
        }
    } else if database.is_none() {
        return Err(DetailError::NotFound(format!(
            "Session {id} not found in database or rollout files"
        )));
    }
    if model.is_none() {
        model = rollout_model;
    }
    let next = offset + messages.len();
    Ok(json!({
        "id": id,
        "title": title,
        "cwd": cwd,
        "provider": provider,
        "model": model,
        "archived": archived,
        "updated_ms": updated_ms,
        "rollout": rollout,
        "database": database,
        "stats": {
            "lines": counts.lines,
            "messages": counts.user + counts.assistant + counts.reasoning,
            "user": counts.user,
            "assistant": counts.assistant,
            "tool": counts.tool,
            "reasoning": counts.reasoning,
            "size": counts.size,
        },
        "messages": messages.iter().map(Message::to_json).collect::<Vec<_>>(),
        "offset": offset,
        "total": total,
        "next_offset": (next < total && !messages.is_empty()).then_some(next),
    }))
}

/// Why detail failed.
#[derive(Debug, PartialEq, Eq)]
pub enum DetailError {
    Invalid(String),
    NotFound(String),
    Unavailable(String),
    Cancelled,
}

#[cfg(test)]
#[path = "reader_tests.rs"]
mod tests;
