//! The OpenCode parser's tests: the old desktop app's case with numbers counted by hand, a message
//! stored in the JSON store and in both database tables counted once, forks, the database left
//! untouched (also in WAL mode, with and without its `-shm`), and file IDs and jobs that name no
//! session, message or folder.

use super::db::{open_read_only, Cache};
use super::message::{self, Out};
use super::{hash, OpenCode};
use pluxy_program::{
    read_frame, serve, write_frame, Call, Description, Error, Handler, Identity, MAX_INBOUND,
};
use pluxy_usage::{produce, Parser, Tokens, KIND};
use rusqlite::Connection;
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::sync::Arc;

/// 2026-10-29 12:00:00 UTC in unix seconds (20,755 days after 1970-01-01), and in milliseconds.
const OCT29: i64 = 1_793_275_200;
const NOON: i64 = OCT29 * 1000;
const S1: &str = "ses_CANARY-SESSION-one";
const S2: &str = "ses_CANARY-SESSION-two";
const FORK: &str = "ses_CANARY-SESSION-fork";

/// A new empty folder of its own under the system's temporary folder.
fn folder(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!("pluxy-opencode-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    d
}

/// [input, output, cache read, cache write, reasoning, total, samples].
fn t(v: [u64; 7]) -> Tokens {
    Tokens::from_values(v)
}

/// What a read gave out: its revision and (hour, model, key, tokens) in order; key None for a row.
#[derive(Default)]
struct Got {
    revision: Option<(u64, i64)>,
    out: Vec<(i64, String, Option<String>, Tokens)>,
}

impl Out for Got {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        self.out
            .push((time.div_euclid(3600) * 3600, model.into(), None, *tokens));
        Ok(())
    }
    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error> {
        self.out.push((
            time.div_euclid(3600) * 3600,
            model.into(),
            Some(key.into()),
            *tokens,
        ));
        Ok(())
    }
    fn revise(&mut self, size: u64, mtime_ns: i64) -> Result<(), Error> {
        assert!(self.out.is_empty(), "the revision comes before any row");
        self.revision = Some((size, mtime_ns));
        Ok(())
    }
    fn cancelled(&self) -> bool {
        false
    }
}

/// OpenCode's tables, as its migrations leave them (the columns read here).
fn schema(c: &Connection) {
    c.execute_batch(
        "CREATE TABLE session_v2 (id text PRIMARY KEY, fork_session_id text, title text);
         CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL,
           time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL);
         CREATE TABLE session_message (id text PRIMARY KEY, session_id text NOT NULL,
           type text NOT NULL, seq integer NOT NULL, time_created integer NOT NULL,
           time_updated integer NOT NULL, data text NOT NULL);
         CREATE UNIQUE INDEX session_message_session_seq_idx ON session_message (session_id, seq);",
    )
    .unwrap();
}

fn session(c: &Connection, id: &str, fork: Option<&str>) {
    c.execute(
        "INSERT INTO session_v2 VALUES (?1, ?2, 'CANARY-TITLE')",
        rusqlite::params![id, fork],
    )
    .unwrap();
}

fn old(c: &Connection, id: &str, session: &str, at: i64, data: &Value) {
    c.execute(
        "INSERT INTO message VALUES (?1, ?2, ?3, ?3, ?4)",
        rusqlite::params![id, session, at, data.to_string()],
    )
    .unwrap();
}

fn current(c: &Connection, id: &str, session: &str, kind: &str, seq: i64, at: i64, data: &Value) {
    c.execute(
        "INSERT INTO session_message VALUES (?1, ?2, ?3, ?4, ?5, ?5, ?6)",
        rusqlite::params![id, session, kind, seq, at, data.to_string()],
    )
    .unwrap();
}

/// OpenCode's tokens object.
fn tokens(i: u64, o: u64, r: u64, read: u64, write: u64) -> Value {
    json!({"input": i, "output": o, "reasoning": r, "cache": {"read": read, "write": write}})
}

/// A v1 assistant message (the JSON store and the `message` table).
fn v1(model: &str, at: i64, tk: Value) -> Value {
    json!({"role": "assistant", "modelID": model, "providerID": "p", "time": {"created": at},
        "path": {"cwd": "/home/CANARY-FOLDER"}, "tokens": tk, "cost": 0.25})
}

/// A current assistant row's data.
fn v2(model: &str, at: i64, tk: Value) -> Value {
    json!({"agent": "build", "model": {"id": model, "providerID": "p"}, "content": [],
        "tokens": tk, "time": {"created": at}})
}

/// The files of folder, by name, with their bytes.
fn files(dir: &Path) -> Vec<(String, Vec<u8>)> {
    let mut f: Vec<_> = std::fs::read_dir(dir)
        .unwrap()
        .flatten()
        .map(|e| {
            (
                e.file_name().to_string_lossy().into_owned(),
                std::fs::read(e.path()).unwrap_or_default(),
            )
        })
        .collect();
    f.sort();
    f
}

fn read(path: &Path, session: &str) -> Got {
    let mut got = Got::default();
    assert!(Cache::default()
        .read_session(path, &hash(&[session]), &mut got)
        .unwrap());
    got
}

#[test]
fn the_old_apps_case_counts_reasoning_once_inside_output_and_leaves_the_database_as_it_was() {
    let d = folder("old-case");
    let db = d.join("opencode.db");
    let c = Connection::open(&db).unwrap();
    schema(&c);
    old(
        &c,
        "msg_1",
        S1,
        NOON,
        &json!({"role": "assistant", "modelID": "model", "time": {"created": NOON},
            "tokens": {"input": 10, "output": 20, "reasoning": 5, "cache": {"read": 30, "write": 40}},
            "cost": 0.25}),
    );
    drop(c);
    let before = files(&d);
    let got = read(&db, S1);
    // Input 10, output 20 + reasoning 5, cache read 30, cache write 40: total 10 + 25 + 30 + 40 = 105
    // (the old app gave 100, dropping reasoning).
    assert_eq!(
        got.out,
        vec![(
            OCT29,
            "model".into(),
            Some(message::key("msg_1", S1)),
            t([10, 25, 30, 40, 5, 105, 1])
        )]
    );
    assert_eq!(
        files(&d),
        before,
        "the database changed, or a file was made beside it"
    );
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_message_in_the_json_store_and_both_tables_is_one_event() {
    let d = folder("same");
    let db = d.join("opencode.db");
    let c = Connection::open(&db).unwrap();
    schema(&c);
    session(&c, S1, None);
    // msg_a in both tables (the migration copies it, dropping the provider total), msg_b only in
    // the current table, a user message and a running compaction with no tokens.
    let a = v1(
        "m-a",
        NOON,
        json!({"input": 100, "output": 40, "reasoning": 10,
        "cache": {"read": 500, "write": 50}, "total": 650}),
    );
    old(&c, "msg_a", S1, NOON, &a);
    old(
        &c,
        "msg_u",
        S1,
        NOON - 1000,
        &json!({"role": "user", "summary": {"title": "CANARY-PROMPT"}}),
    );
    current(
        &c,
        "msg_u",
        S1,
        "user",
        1,
        NOON - 1000,
        &json!({"text": "CANARY-PROMPT"}),
    );
    current(
        &c,
        "msg_a",
        S1,
        "assistant",
        2,
        NOON,
        &v2("m-a", NOON, tokens(100, 40, 10, 500, 50)),
    );
    current(
        &c,
        "msg_r",
        S1,
        "compaction",
        3,
        NOON + 1000,
        &json!({"status": "running", "summary": "CANARY-REPLY"}),
    );
    // A completed compaction: its request's own usage, an hour later.
    current(
        &c,
        "msg_c",
        S1,
        "compaction",
        4,
        NOON + 3_600_000,
        &json!({"status": "completed", "model": {"id": "m-c"}, "summary": "CANARY-REPLY",
            "tokens": tokens(300, 60, 0, 0, 0), "time": {"created": NOON + 3_600_000}}),
    );
    drop(c);
    let got = read(&db, S1);
    let ka = message::key("msg_a", S1);
    // msg_a: 100, 40 + 10, 500, 50 → total 700 (its provider total 650 not used); msg_c: 360.
    assert_eq!(
        got.out,
        vec![
            (
                OCT29,
                "m-a".into(),
                Some(ka.clone()),
                t([100, 50, 500, 50, 10, 700, 1])
            ),
            (
                OCT29 + 3600,
                "m-c".into(),
                Some(message::key("msg_c", S1)),
                t([300, 60, 0, 0, 0, 360, 1])
            ),
        ]
    );
    // The JSON store's file of msg_a has the same key and counts: the hub stores the event once.
    let mut json_got = Got::default();
    let file = json!({"id": "msg_a", "sessionID": S1})
        .as_object()
        .unwrap()
        .clone()
        .into_iter()
        .chain(a.as_object().unwrap().clone())
        .collect::<serde_json::Map<_, _>>();
    message::json_file(&Value::Object(file), &mut json_got).unwrap();
    assert_eq!(json_got.out, vec![got.out[0].clone()]);
    // A file without its IDs counts in its file only; a user message not at all.
    let mut plain = Got::default();
    message::json_file(&a, &mut plain).unwrap();
    message::json_file(
        &json!({"role": "user", "tokens": tokens(1, 1, 0, 0, 0), "time": {"created": NOON}}),
        &mut plain,
    )
    .unwrap();
    assert_eq!(
        plain.out,
        vec![(OCT29, "m-a".into(), None, t([100, 50, 500, 50, 10, 700, 1]))]
    );
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_forks_copied_history_counts_once_and_its_own_steps_count() {
    let d = folder("fork");
    let db = d.join("opencode.db");
    let c = Connection::open(&db).unwrap();
    schema(&c);
    session(&c, S2, None);
    session(&c, FORK, Some(S2));
    session(&c, "ses_fork_of_fork", Some(FORK));
    session(&c, "ses_orphan", Some("ses_gone"));
    let a1 = v2("m", NOON, tokens(200, 30, 20, 1000, 0));
    current(&c, "msg_a1", S2, "assistant", 2, NOON, &a1);
    // The fork copies seq 2 as msg_ev1_2; its own step is msg_f1.
    current(&c, "msg_ev1_2", FORK, "assistant", 2, NOON, &a1);
    current(
        &c,
        "msg_f1",
        FORK,
        "assistant",
        3,
        NOON + 60_000,
        &v2("m", NOON + 60_000, tokens(10, 5, 0, 0, 0)),
    );
    // A fork of the fork copies the copy; a fork whose parent is gone keeps its copy.
    current(
        &c,
        "msg_ev2_2",
        "ses_fork_of_fork",
        "assistant",
        2,
        NOON,
        &a1,
    );
    current(&c, "msg_ev3_2", "ses_orphan", "assistant", 2, NOON, &a1);
    drop(c);
    let k1 = message::key("msg_a1", S2);
    let a = t([200, 50, 1000, 0, 20, 1250, 1]);
    assert_eq!(
        read(&db, S2).out,
        vec![(OCT29, "m".into(), Some(k1.clone()), a)]
    );
    assert_eq!(
        read(&db, FORK).out,
        vec![
            (OCT29, "m".into(), Some(k1.clone()), a),
            (
                OCT29,
                "m".into(),
                Some(message::key("msg_f1", FORK)),
                t([10, 5, 0, 0, 0, 15, 1])
            ),
        ]
    );
    assert_eq!(
        read(&db, "ses_fork_of_fork").out,
        vec![(OCT29, "m".into(), Some(k1), a)]
    );
    assert_eq!(
        read(&db, "ses_orphan").out,
        vec![(
            OCT29,
            "m".into(),
            Some(hash(&["opencode-fork", "ses_gone", "2"])),
            a
        )]
    );
    std::fs::remove_dir_all(d).unwrap();
}

/// A WAL database with S1's msg_1 in it and its writer, which keeps the -wal and -shm while open.
fn wal_db(d: &Path) -> (PathBuf, Connection) {
    let db = d.join("opencode.db");
    let c = Connection::open(&db).unwrap();
    c.pragma_update(None, "journal_mode", "WAL").unwrap();
    c.pragma_update(None, "wal_autocheckpoint", 0).unwrap();
    schema(&c);
    current(
        &c,
        "msg_1",
        S1,
        "assistant",
        1,
        NOON,
        &v2("m", NOON, tokens(1, 2, 0, 0, 0)),
    );
    (db, c)
}

#[test]
fn a_change_only_in_the_wal_changes_the_sessions_revision_read_beside_a_live_writer() {
    let d = folder("wal-live");
    let (db, c) = wal_db(&d);
    let listed = Cache::default().sessions(&db).unwrap();
    assert_eq!(listed.len(), 1);
    let bytes = std::fs::read(&db).unwrap();
    current(
        &c,
        "msg_2",
        S1,
        "assistant",
        2,
        NOON + 5000,
        &v2("m", NOON + 5000, tokens(3, 4, 0, 0, 0)),
    );
    assert_eq!(
        std::fs::read(&db).unwrap(),
        bytes,
        "the write should be in the -wal only"
    );
    let again = Cache::default().sessions(&db).unwrap();
    assert_ne!(again[0], listed[0]);
    // The read gives the revision listed, and both messages.
    let got = read(&db, S1);
    assert_eq!(got.revision, Some((again[0].size, again[0].mtime_ns)));
    assert_eq!(got.out.len(), 2);
    assert_eq!(std::fs::read(&db).unwrap(), bytes);
    drop(c);
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_wal_database_with_no_shm_is_read_without_making_one() {
    let d = folder("wal-cold");
    let (db, c) = wal_db(&d);
    // The database and its -wal as a writer that stopped without checkpointing leaves them.
    let cold = folder("wal-cold-copy");
    std::fs::copy(&db, cold.join("opencode.db")).unwrap();
    std::fs::copy(d.join("opencode.db-wal"), cold.join("opencode.db-wal")).unwrap();
    drop(c);
    let db = cold.join("opencode.db");
    assert!(
        std::fs::metadata(cold.join("opencode.db-wal"))
            .unwrap()
            .len()
            > 0
    );
    let before = files(&cold);
    let mine = |dir: &Path| {
        std::fs::read_dir(dir)
            .unwrap()
            .flatten()
            .filter(|e| {
                e.file_name()
                    .to_string_lossy()
                    .starts_with(&format!("wal-{}-", std::process::id()))
            })
            .count()
    };
    // The message is only in the -wal: read from a private copy, removed after.
    assert_eq!(read(&db, S1).out.len(), 1);
    assert_eq!(
        files(&cold),
        before,
        "the database changed, or a file was made beside it"
    );
    assert_eq!(
        mine(&std::env::temp_dir()),
        0,
        "the private copy was not removed"
    );
    // With an empty -wal, opened immutable.
    std::fs::write(cold.join("opencode.db-wal"), b"").unwrap();
    let before = files(&cold);
    let db_open = open_read_only(&db).unwrap();
    drop(db_open);
    assert_eq!(
        Cache::default().sessions(&db).unwrap().len(),
        0,
        "the -wal's message is gone with it"
    );
    assert_eq!(files(&cold), before);
    // With no -wal at all, too.
    std::fs::remove_file(cold.join("opencode.db-wal")).unwrap();
    let before = files(&cold);
    assert!(Cache::default().sessions(&db).unwrap().is_empty());
    assert_eq!(files(&cold), before);
    std::fs::remove_dir_all(d).unwrap();
    std::fs::remove_dir_all(cold).unwrap();
}

#[test]
fn a_database_with_a_linked_wal_or_shm_is_not_read() {
    let d = folder("links");
    let (db, c) = wal_db(&d);
    drop(c);
    let elsewhere = d.join("elsewhere");
    std::fs::write(&elsewhere, b"").unwrap();
    for side in ["opencode.db-wal", "opencode.db-shm"] {
        let _ = std::fs::remove_file(d.join(side));
        std::os::unix::fs::symlink(&elsewhere, d.join(side)).unwrap();
        assert!(open_read_only(&db).is_err(), "{side} is a link");
        std::fs::remove_file(d.join(side)).unwrap();
    }
    // The positive control: with neither, it is read.
    assert_eq!(Cache::default().sessions(&db).unwrap().len(), 1);
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn file_ids_hash_sessions_and_messages_and_find_their_files_again() {
    let p = OpenCode {
        db: Cache::default(),
    };
    let root = Path::new("/home/u/.local/share/opencode");
    let file = root.join("storage/message/ses_CANARY/msg_CANARY.json");
    assert!(p.wanted(&file));
    assert!(p.wanted(&root.join("opencode.db")) && p.wanted(&root.join("opencode-local.db")));
    for not in [
        "opencode.db-wal",
        "opencode-.db",
        "other.db",
        "storage/part/msg_x/prt_y.json",
        "storage/session/x.json",
    ] {
        assert!(!p.wanted(&root.join(not)), "{not}");
    }
    assert_eq!(
        p.file_id_under(root, &file).unwrap(),
        format!(
            "storage/message/{}/{}.json",
            hash(&["ses_CANARY"]),
            hash(&["msg_CANARY"])
        )
    );
    assert_eq!(
        p.file_id_under(root, &root.join("opencode.db")).unwrap(),
        "opencode.db"
    );
    // On disk: the ID names its file again, under the first root that has it.
    let d = folder("ids");
    let root = d.join("data");
    let file = root.join("storage/message/ses_CANARY/msg_CANARY.json");
    std::fs::create_dir_all(file.parent().unwrap()).unwrap();
    std::fs::write(&file, "{}").unwrap();
    let id = p.file_id_under(&root, &file).unwrap();
    assert_eq!(p.locate(&[d.join("none"), root.clone()], &id), Some(file));
    assert_eq!(
        p.locate(std::slice::from_ref(&root), "storage/message/x/y.json"),
        None
    );
    assert_eq!(
        p.locate(std::slice::from_ref(&root), "opencode.db"),
        None,
        "a database is read by its sessions"
    );
    std::fs::remove_dir_all(d).unwrap();
}

struct Producer;
impl Handler for Producer {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        produce(
            call,
            &OpenCode {
                db: Cache::default(),
            },
        )
    }
}

/// A host for Producer: sends one job call with input and acknowledges every batch; gives the
/// batches and the terminal frame.
fn job(input: Value) -> (Vec<Value>, Value) {
    let (in_r, mut in_w) = std::io::pipe().unwrap();
    let (mut out_r, out_w) = std::io::pipe().unwrap();
    let description =
        Description::from_manifest(include_bytes!("../pluxy-extension.json")).unwrap();
    let identity = Identity {
        id: "pluxy/usage-opencode".into(),
        sha256: "ab".repeat(32),
    };
    let served =
        std::thread::spawn(move || serve(in_r, out_w, &identity, description, Arc::new(Producer)));
    assert_eq!(read_frame(&mut out_r).unwrap().unwrap()["type"], "hello");
    let call = json!({"type": "call", "id": "c-1", "op": "scan", "input": input,
        "job": {"id": "j-1", "kind": KIND, "window": 4}});
    write_frame(&mut in_w, &call, MAX_INBOUND).unwrap();
    let mut batches = Vec::new();
    let end = loop {
        let f = read_frame(&mut out_r).unwrap().unwrap();
        if f["type"] != "progress" {
            break f;
        }
        batches.push(f["value"].clone());
        let ack = json!({"type": "ack", "id": "c-1", "seq": batches.len()});
        write_frame(&mut in_w, &ack, MAX_INBOUND).unwrap();
    };
    drop(in_w);
    served.join().unwrap().unwrap();
    (batches, end)
}

#[test]
fn a_job_lists_each_session_and_message_file_and_reads_them_naming_no_raw_id() {
    let d = folder("job");
    let root = d.join("opencode");
    std::fs::create_dir_all(&root).unwrap();
    let db = root.join("opencode.db");
    let c = Connection::open(&db).unwrap();
    schema(&c);
    session(&c, S1, None);
    old(
        &c,
        "msg_a",
        S1,
        NOON,
        &v1("m-a", NOON, tokens(100, 40, 10, 500, 50)),
    );
    current(
        &c,
        "msg_a",
        S1,
        "assistant",
        1,
        NOON,
        &v2("m-a", NOON, tokens(100, 40, 10, 500, 50)),
    );
    current(
        &c,
        "msg_b",
        S2,
        "assistant",
        1,
        NOON + 7_200_000,
        &v2("m-b", NOON + 7_200_000, tokens(1, 2, 0, 0, 0)),
    );
    drop(c);
    let json_dir = root.join("storage/message").join(S1);
    std::fs::create_dir_all(&json_dir).unwrap();
    let mut a = v1("m-a", NOON, tokens(100, 40, 10, 500, 50));
    a["id"] = json!("msg_a");
    a["sessionID"] = json!(S1);
    std::fs::write(json_dir.join("msg_a.json"), a.to_string()).unwrap();
    let before = files(&root);
    let (batches, end) = job(json!({"mode": "files", "roots": [root]}));
    let listed: Vec<Value> = batches
        .iter()
        .flat_map(|b| b["files"].as_array().unwrap().clone())
        .collect();
    let ids: Vec<&str> = listed.iter().map(|f| f["file"].as_str().unwrap()).collect();
    assert_eq!(end["output"]["count"], 3);
    let s1 = format!("opencode.db#{}", hash(&[S1]));
    let s2 = format!("opencode.db#{}", hash(&[S2]));
    let jf = format!("storage/message/{}/{}.json", hash(&[S1]), hash(&["msg_a"]));
    for id in [&s1, &s2, &jf] {
        assert!(ids.contains(&id.as_str()), "{id} in {ids:?}");
    }
    // Newest first: S2's last update is newer than S1's.
    let at = |id: &str| ids.iter().position(|i| *i == id).unwrap();
    assert!(at(&s2) < at(&s1));
    let text = format!("{batches:?}{end}");
    for raw in [S1, S2, "msg_a", "msg_b", "CANARY"] {
        assert!(!text.contains(raw), "{raw} in the listing");
    }
    // Each read once; msg_a has one key in its session and in its JSON file.
    let (batches, end) =
        job(json!({"mode": "read", "roots": [root], "files": [s1, jf, s2, "opencode.db#nothing"]}));
    assert_eq!(end["output"], json!({"read": 3, "failed": 1}));
    let rows = |file: &str| -> Vec<Value> {
        batches
            .iter()
            .filter(|b| b["file"] == file)
            .flat_map(|b| b["rows"].as_array().cloned().unwrap_or_default())
            .collect()
    };
    let ka = message::key("msg_a", S1);
    let row_a = json!({"hour": OCT29, "model": "m-a", "input": 100, "output": 50, "cache_read": 500,
        "cache_write": 50, "reasoning": 10, "total": 700, "samples": 1, "key": ka});
    assert_eq!(rows(&s1), vec![row_a.clone()]);
    assert_eq!(rows(&jf), vec![row_a]);
    assert_eq!(rows(&s2).len(), 1);
    // The revision a session is read at is the one it was listed at.
    let size = |file: &str, from: &[Value]| {
        from.iter()
            .find(|f| f["file"] == file)
            .map(|f| (f["size"].clone(), f["mtime_ns"].clone()))
    };
    assert_eq!(size(&s1, &batches), size(&s1, &listed));
    let text = format!("{batches:?}{end}");
    for raw in [S1, S2, "msg_a", "msg_b", "CANARY"] {
        assert!(!text.contains(raw), "{raw} in a read");
    }
    assert_eq!(
        files(&root),
        before,
        "the store changed, or a file was made beside it"
    );
    std::fs::remove_dir_all(d).unwrap();
}
