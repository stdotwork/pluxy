//! Tests of the Pi and Oh My Pi session reading: the old desktop app's case with numbers counted by hand,
//! the corners of the rules in `rows.rs`, and where a session's file is.

use super::rows::{self, Out};
use super::{hash, listed, Sessions};
use pluxy_program::Error;
use pluxy_usage::{LineReader, Parser, Tokens};
use serde_json::{json, Value};
use std::collections::{BTreeMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};

/// 2026-10-29 12:00:00 UTC and one day later, in unix seconds (20,755 days after 1970-01-01).
const OCT29: i64 = 1_793_275_200;
const OCT30: i64 = OCT29 + 86_400;

/// Rows summed per hour and model, as the usage sink sums them, and events counted once by key, as the
/// hub counts them across files: one `Rows` can take several files.
#[derive(Default)]
struct Rows {
    rows: BTreeMap<(i64, String), Tokens>,
    /// The keys of the events counted, and every key sent (a key sent twice by one file shows here).
    counted: HashSet<String>,
    sent: Vec<String>,
    cancel: bool,
}

impl Out for Rows {
    fn row(&mut self, time: i64, model: &str, tokens: &Tokens) -> Result<(), Error> {
        let key = (time.div_euclid(3600) * 3600, model.to_string());
        self.rows.entry(key).or_default().add(tokens);
        Ok(())
    }

    fn event(&mut self, time: i64, model: &str, key: &str, tokens: &Tokens) -> Result<(), Error> {
        self.sent.push(key.to_string());
        if self.counted.insert(key.to_string()) {
            self.row(time, model, tokens)?;
        }
        Ok(())
    }

    fn cancelled(&self) -> bool {
        self.cancel
    }
}

/// A session file of lines, parsed.
fn parse(lines: &[Value]) -> Rows {
    let mut rows = Rows::default();
    parse_into(&mut rows, lines);
    rows
}

/// Another session file of lines, parsed into the same rows (events already counted stay counted once).
fn parse_into(rows: &mut Rows, lines: &[Value]) {
    let text: String = lines.iter().map(|l| format!("{l}\n")).collect();
    let before = rows.sent.len();
    rows::parse(&mut LineReader::new(text.as_bytes()), rows).unwrap();
    // A file sends each key once.
    let sent = &rows.sent[before..];
    assert_eq!(sent.iter().collect::<HashSet<_>>().len(), sent.len());
}

/// A fresh entry ID, as the tools make them (8 hex digits).
fn next_id() -> String {
    static N: AtomicU64 = AtomicU64::new(0x1000_0000);
    format!("{:08x}", N.fetch_add(1, Ordering::Relaxed))
}

/// [input, output, cache read, cache write, reasoning, total, samples].
fn t(v: [u64; 7]) -> Tokens {
    Tokens::from_values(v)
}

const NOON29: &str = "2026-10-29T12:10:00.000Z";
const NOON30: &str = "2026-10-30T12:10:00.000Z";

/// An entry of a session file with a message.
fn message(at: &str, message: Value) -> Value {
    json!({"type": "message", "id": next_id(), "parentId": null, "timestamp": at, "message": message})
}

/// An entry that carries usage of its own.
fn entry(kind: &str, at: &str, rest: Value) -> Value {
    let mut o = json!({"type": kind, "id": next_id(), "parentId": null, "timestamp": at});
    for (k, v) in rest.as_object().unwrap() {
        o[k] = v.clone();
    }
    o
}

#[test]
fn reasoning_is_part_of_output_and_never_added_again() {
    // The old app's case, for Pi (`reasoning`) and Oh My Pi (`reasoningTokens`): 10 + 20 + 30 + 40 is
    // the total when the source gives none; reasoning 5 stays inside the 20 of output.
    for key in ["reasoning", "reasoningTokens"] {
        let rows = parse(&[message(
            NOON29,
            json!({"role": "assistant", "model": "model", "usage": {
                "input": 10, "output": 20, "cacheRead": 30, "cacheWrite": 40, key: 5}}),
        )]);
        assert_eq!(
            rows.rows,
            BTreeMap::from([((OCT29, "model".to_string()), t([10, 20, 30, 40, 5, 100, 1]))]),
            "{key}"
        );
    }
}

#[test]
fn the_sources_own_total_wins_over_the_sum() {
    // Oh My Pi's total holds 20 tokens of provider-side work the other fields do not: 40 + 10 + 50 + 20.
    let rows = parse(&[message(
        NOON29,
        json!({"role": "assistant", "model": "m", "usage": {
            "input": 40, "output": 10, "cacheRead": 50, "cacheWrite": 0, "totalTokens": 120,
            "orchestration": {"input": 20}, "cost": {"total": 0.5}}}),
    )]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([((OCT29, "m".to_string()), t([40, 10, 50, 0, 0, 120, 1]))])
    );
}

#[test]
fn reads_usage_where_the_two_tools_write_it_and_nowhere_else() {
    let rows = parse(&[
        // A header, the title line, a user message and a message with no usage: none.
        json!({"type": "title", "title": "private title"}),
        json!({"type": "session", "version": 3, "id": "x", "timestamp": NOON29, "cwd": "/private"}),
        message(NOON29, json!({"role": "user", "content": "input 99999"})),
        // An assistant message: model "A", 1 + 2.
        message(
            NOON29,
            json!({"role": "assistant", "model": "A", "usage": {"input": 1, "output": 2}}),
        ),
        // A Pi usage entry (model "B", 10) and an Oh My Pi model_usage entry (model "C", 100).
        entry(
            "usage",
            NOON29,
            json!({"provider": "p", "model": "B", "usage": {"input": 10}}),
        ),
        entry(
            "model_usage",
            NOON29,
            json!({"provider": "p", "model": "C", "usage": {"output": 100}}),
        ),
        // A compaction, a branch summary and a tool result that did nested work: no model, 1,000 +
        // 2,000 + 4,000, three samples.
        entry("compaction", NOON29, json!({"usage": {"input": 1000}})),
        entry("branch_summary", NOON29, json!({"usage": {"input": 2000}})),
        message(
            NOON29,
            json!({"role": "toolResult", "toolName": "x", "usage": {"input": 4000}}),
        ),
        // Not read: another kind of entry that has a `usage`, and an Oh My Pi task result, whose child
        // usage is in `details` and in the child's own file.
        entry("custom", NOON29, json!({"usage": {"input": 99999}})),
        message(
            NOON29,
            json!({"role": "toolResult", "toolName": "task",
                "details": {"usage": {"input": 88888, "totalTokens": 88888}}}),
        ),
    ]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            ((OCT29, String::new()), t([7000, 0, 0, 0, 0, 7000, 3])),
            ((OCT29, "A".to_string()), t([1, 2, 0, 0, 0, 3, 1])),
            ((OCT29, "B".to_string()), t([10, 0, 0, 0, 0, 10, 1])),
            ((OCT29, "C".to_string()), t([0, 100, 0, 0, 0, 100, 1])),
        ])
    );
    assert!(!format!("{:?}", rows.rows).contains("private"));
}

#[test]
fn the_model_is_the_one_that_answered() {
    let usage = json!({"input": 1});
    let rows = parse(&[
        // Pi's `responseModel` is the concrete model that answered; the control character is dropped.
        message(
            NOON29,
            json!({"role": "assistant", "model": "virtual", "responseModel": "Real\u{7}Model",
                "usage": usage}),
        ),
        message(
            NOON29,
            json!({"role": "assistant", "model": "only", "usage": usage}),
        ),
        message(NOON29, json!({"role": "assistant", "usage": usage})),
        // A name is cut to 120 characters.
        message(
            NOON29,
            json!({"role": "assistant", "model": "x".repeat(130), "usage": usage}),
        ),
    ]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            ((OCT29, String::new()), t([1, 0, 0, 0, 0, 1, 1])),
            ((OCT29, "RealModel".to_string()), t([1, 0, 0, 0, 0, 1, 1])),
            ((OCT29, "only".to_string()), t([1, 0, 0, 0, 0, 1, 1])),
            ((OCT29, "x".repeat(120)), t([1, 0, 0, 0, 0, 1, 1])),
        ])
    );
}

#[test]
fn counts_each_entry_on_its_own_day() {
    let at = |stamp: &str, input: u64| {
        message(
            stamp,
            json!({"role": "assistant", "model": "m", "usage": {"input": input, "output": 1}}),
        )
    };
    let rows = parse(&[
        at(NOON29, 100),
        // The same hour, summed: two samples.
        at("2026-10-29T12:59:59.999Z", 20),
        // A day later; 14:30 in the zone +02:00 is 12:30 UTC, the same hour as 12:10 UTC: 3 + 4.
        at("2026-10-30T14:30:00+02:00", 3),
        at(NOON30, 4),
    ]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            ((OCT29, "m".to_string()), t([120, 2, 0, 0, 0, 122, 2])),
            ((OCT30, "m".to_string()), t([7, 2, 0, 0, 0, 9, 2])),
        ])
    );
}

#[test]
fn reads_the_times_the_two_tools_write() {
    let one = |entry_time: Option<Value>, message_time: Option<Value>| {
        let mut m = json!({"role": "assistant", "usage": {"input": 1}});
        if let Some(v) = message_time {
            m["timestamp"] = v;
        }
        let mut line = json!({"type": "message", "message": m});
        if let Some(v) = entry_time {
            line["timestamp"] = v;
        }
        parse(&[line]).rows.keys().next().map(|k| k.0)
    };
    // The entry's own time, as an ISO string (in any zone) or as seconds or milliseconds.
    assert_eq!(
        one(Some(json!("2026-10-29T12:34:56.789Z")), None),
        Some(OCT29)
    );
    assert_eq!(
        one(Some(json!("2026-10-29T09:04:56-03:30")), None),
        Some(OCT29)
    );
    assert_eq!(one(Some(json!(OCT29 + 30)), None), Some(OCT29));
    // Else the message's, in milliseconds (also when the entry's is not a time).
    let ms = json!((OCT29 + 30) * 1000);
    assert_eq!(one(None, Some(ms.clone())), Some(OCT29));
    assert_eq!(one(Some(json!("yesterday")), Some(ms.clone())), Some(OCT29));
    // The entry's wins when both are there.
    assert_eq!(one(Some(json!(NOON30)), Some(ms)), Some(OCT30));
    // No readable time: skipped.
    assert_eq!(one(None, None), None);
    assert_eq!(one(Some(json!("2026-13-29T12:00:00Z")), None), None);
    assert_eq!(one(Some(json!("2026-10-29T12:00:00")), None), None);
}

#[test]
fn skips_what_has_no_usable_count() {
    let m = |usage: Value| {
        message(
            NOON29,
            json!({"role": "assistant", "model": "m", "usage": usage}),
        )
    };
    let mut text = String::new();
    for line in [
        // No known field; not an object; counts that are not whole numbers from 0 up (so no count at
        // all); and one whole `output` of 2 beside an input that is not whole: one sample, 0 + 2.
        m(json!({"cost": {"total": 1}})),
        m(json!("lots")),
        m(json!({"input": "7", "output": -1})),
        m(json!({"input": 1.5, "output": 2.0})),
    ] {
        text.push_str(&format!("{line}\n"));
        text.push_str("not json\n\n{\"type\": \"message\"\n");
    }
    let mut rows = Rows::default();
    rows::parse(&mut LineReader::new(text.as_bytes()), &mut rows).unwrap();
    assert_eq!(
        rows.rows,
        BTreeMap::from([((OCT29, "m".to_string()), t([0, 2, 0, 0, 0, 2, 1]))])
    );
}

#[test]
fn stops_when_the_job_is_cancelled() {
    let mut rows = Rows {
        cancel: true,
        ..Rows::default()
    };
    let err = rows::parse(&mut LineReader::new(&b"{}\n"[..]), &mut rows).unwrap_err();
    assert_eq!(err.code, "cancelled");
}

// ---- events: an entry a fork copies counts once

/// An assistant entry with a usage of `input` and 5 output tokens.
fn turn(at: &str, input: u64) -> Value {
    message(
        at,
        json!({"role": "assistant", "model": "m", "usage": {"input": input, "output": 5}}),
    )
}

#[test]
fn a_forks_copied_entries_count_once() {
    // A session of two turns, and its fork: the fork's file holds copies of both, unchanged (as the
    // tools write them), and a turn of its own.
    let a = turn(NOON29, 10);
    let b = turn(NOON30, 20);
    let c = turn("2026-10-30T12:40:00.000Z", 40);
    let mut rows = parse(&[a.clone(), b.clone()]);
    parse_into(&mut rows, &[a.clone(), b.clone(), c.clone()]);
    // Each of the three turns once: 10 + 5 on the 29th; 20 + 40 input, 5 + 5 output on the 30th.
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            ((OCT29, "m".to_string()), t([10, 5, 0, 0, 0, 15, 1])),
            ((OCT30, "m".to_string()), t([60, 10, 0, 0, 0, 70, 2])),
        ])
    );
    // Five events were sent (two files), three counted. A key is a hash: it holds no entry ID.
    assert_eq!((rows.sent.len(), rows.counted.len()), (5, 3));
    for key in &rows.sent {
        assert!(
            key.len() == 32 && key.bytes().all(|b| b.is_ascii_hexdigit()),
            "{key}"
        );
        for turn in [&a, &b, &c] {
            assert!(!key.contains(turn["id"].as_str().unwrap()));
        }
    }
    // The fork alone, read without its parent, is its three turns.
    let alone = parse(&[a, b, c]);
    assert_eq!(alone.rows.values().map(|v| v.samples).sum::<u64>(), 3);
}

#[test]
fn an_entry_repeated_in_one_file_is_sent_once() {
    let a = turn(NOON29, 10);
    let rows = parse(&[a.clone(), a]);
    assert_eq!(
        rows.rows,
        BTreeMap::from([((OCT29, "m".to_string()), t([10, 5, 0, 0, 0, 15, 1]))])
    );
    assert_eq!(rows.sent.len(), 1);
}

#[test]
fn the_same_id_at_another_time_is_another_entry() {
    // An entry ID is eight hex digits, unique within one session only: two sessions' entries can share
    // one by chance, and are told apart by their time.
    let mut a = turn(NOON29, 10);
    let mut b = turn(NOON30, 20);
    a["id"] = json!("deadbeef");
    b["id"] = json!("deadbeef");
    let mut rows = parse(&[a]);
    parse_into(&mut rows, &[b]);
    assert_eq!(rows.counted.len(), 2);
    assert_eq!(
        rows.rows,
        BTreeMap::from([
            ((OCT29, "m".to_string()), t([10, 5, 0, 0, 0, 15, 1])),
            ((OCT30, "m".to_string()), t([20, 5, 0, 0, 0, 25, 1])),
        ])
    );
}

#[test]
fn an_entry_without_an_id_or_a_time_is_a_plain_row() {
    let mut no_id = turn(NOON29, 10);
    no_id.as_object_mut().unwrap().remove("id");
    // No `timestamp` of its own: the message's time (milliseconds) places it, and it has no key.
    let mut no_time = turn(NOON29, 1);
    no_time.as_object_mut().unwrap().remove("timestamp");
    no_time["message"]["timestamp"] = json!((OCT29 + 30) * 1000);
    // Each is counted every time it appears, as at schema 1.
    let rows = parse(&[no_id.clone(), no_id, no_time.clone(), no_time]);
    assert!(rows.sent.is_empty());
    assert_eq!(
        rows.rows,
        BTreeMap::from([((OCT29, "m".to_string()), t([22, 20, 0, 0, 0, 42, 4]))])
    );
}

// ---- where a session's file is

fn folder(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("pi-sessions-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn write(path: &Path, text: &str) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, text).unwrap();
}

const FOLDER: &str = "-work-alpha";
const FILE: &str = "2026-10-29T12-00-00-000Z_019c0000-0000-7000-8000-00000000000a.jsonl";

/// The session's path below a root.
fn session() -> String {
    format!("{FOLDER}/{FILE}")
}

/// The session's file ID: its folder replaced by a hash, so the ID names no folder.
fn id() -> String {
    format!("{}/{FILE}", hash(&[FOLDER]))
}

fn omp() -> Sessions {
    Sessions::new("Oh My Pi", ".omp")
}

/// A file's ID as a job's listing gives it: below the first root that holds it.
fn id_of(p: &Sessions, roots: &[PathBuf], path: &Path) -> Option<String> {
    let root = roots.iter().find(|r| path.starts_with(r))?;
    p.file_id_under(root, path)
}

#[test]
fn reads_both_default_roots_and_only_jsonl_files() {
    let home = Path::new("/home/someone");
    let p = omp();
    assert_eq!(
        p.default_roots(home),
        [home.join(".omp/agent/sessions"), home.join(".omp/sessions")]
    );
    let pi = Sessions::new("Pi", ".pi");
    assert_eq!(
        pi.default_roots(home),
        [home.join(".pi/agent/sessions"), home.join(".pi/sessions")]
    );
    assert!(p.wanted(Path::new("/a/b/x.jsonl")) && p.wanted(Path::new("X.JSONL")));
    assert!(!p.wanted(Path::new("/a/b/x.json")) && !p.wanted(Path::new("/a/jsonl")));
}

#[test]
fn a_file_is_named_by_its_path_below_its_root_with_the_folder_hashed() {
    let dir = folder("id");
    let roots = vec![dir.join("agent/sessions"), dir.join("sessions")];
    let p = omp();
    // The same session under either root has the same ID, and it names no folder.
    let one = roots[0].join(session());
    let two = roots[1].join(session());
    assert_eq!(id_of(&p, &roots, &one), Some(id()));
    assert_eq!(id_of(&p, &roots, &two), Some(id()));
    assert!(!id().contains("work") && id().ends_with(FILE));
    // A file directly in a root is its name; a file under no root has none.
    assert_eq!(
        id_of(&p, &roots, &roots[0].join("x.jsonl")).as_deref(),
        Some("x.jsonl")
    );
    assert_eq!(id_of(&p, &roots, &dir.join("elsewhere/x.jsonl")), None);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn a_session_in_both_roots_is_one_file_and_the_newer_copy_is_read() {
    let dir = folder("both");
    let roots = vec![dir.join("agent/sessions"), dir.join("sessions")];
    let p = omp();
    let older = roots[0].join(session());
    let newer = roots[1].join(session());
    let alone = roots[0].join("-work-beta/other.jsonl");
    write(&older, "{}\n");
    std::thread::sleep(std::time::Duration::from_millis(20));
    write(&newer, "{}\n{}\n");
    write(&alone, "{}\n");
    write(&roots[1].join("-work-beta/notes.txt"), "not a session");
    // The listing has both copies, newest first, under one ID; the hub keeps the first's revision.
    let files = listed(&roots, &p);
    assert_eq!(files.len(), 3);
    assert_eq!(files[0], alone);
    assert_eq!(
        files
            .iter()
            .filter(|f| id_of(&p, &roots, f) == Some(id()))
            .collect::<Vec<_>>(),
        [&newer, &older]
    );
    assert_eq!(p.locate(&roots, &id()), Some(newer));
    let other = id_of(&p, &roots, &alone).unwrap();
    assert_eq!(p.locate(&roots, &other), Some(alone));
    let missing = format!("{}/missing.jsonl", hash(&["-work-beta"]));
    assert_eq!(p.locate(&roots, &missing), None);
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn never_locates_a_path_out_of_the_roots_or_a_link() {
    let dir = folder("locate");
    let roots = vec![dir.join("agent/sessions"), dir.join("sessions")];
    let p = omp();
    let real = roots[0].join(session());
    write(&real, "{}\n");
    std::fs::create_dir_all(&roots[1]).unwrap();
    write(&dir.join("secret.jsonl"), "{}\n");
    let hashed = hash(&[FOLDER]);
    for bad in [
        "../secret.jsonl".to_string(),
        format!("{hashed}/../../secret.jsonl"),
        "/etc/passwd".to_string(),
        dir.join("secret.jsonl").to_string_lossy().into_owned(),
        "./x.jsonl".to_string(),
        "..".to_string(),
        String::new(),
        // The folder's own name is no ID: only its hash is.
        session(),
    ] {
        assert_eq!(p.locate(&roots, &bad), None, "{bad:?}");
    }
    // A link is never followed, whether the file or a folder on the way is one.
    std::os::unix::fs::symlink(&real, roots[1].join("link.jsonl")).unwrap();
    assert_eq!(p.locate(&roots, "link.jsonl"), None);
    std::os::unix::fs::symlink(dir.join("agent"), roots[1].join("folder")).unwrap();
    assert_eq!(
        p.locate(
            &roots,
            &format!("{}/sessions/{}", hash(&["folder"]), session())
        ),
        None
    );
    assert_eq!(p.locate(&roots, &id()), Some(real));
    let _ = std::fs::remove_dir_all(dir);
}
