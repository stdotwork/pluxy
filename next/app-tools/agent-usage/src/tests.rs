//! The store, syncs and reports, against a real SQLite file and a host played in memory. Every
//! expected number is counted by hand from the rows each test writes.

use super::*;
use pluxy_usage::Tokens;
use std::cell::RefCell;
use std::collections::BTreeMap;
use store::Rows;
use sync::Host;

/// A store in a new folder of its own.
fn store(name: &str) -> (Store, PathBuf) {
    let d = std::env::temp_dir().join(format!("pluxy-agent-usage-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    std::fs::create_dir_all(&d).unwrap();
    let path = d.join("usage.db");
    (Store::open(&path).unwrap(), d)
}

fn tok(input: u64, output: u64, total: u64) -> Tokens {
    Tokens {
        input,
        output,
        total,
        samples: 1,
        ..Tokens::default()
    }
}

fn rows(list: &[(i64, &str, Tokens)]) -> Rows {
    let mut r = BTreeMap::new();
    for (h, m, t) in list {
        r.insert((*h, m.to_string()), *t);
    }
    r
}

/// The stored rows per (source, hour, model): (input, total).
fn stored(s: &Store) -> Vec<(String, i64, String, u64, u64)> {
    let mut v: Vec<_> = s
        .rows(i64::MIN, i64::MAX)
        .unwrap()
        .into_iter()
        .map(|(src, h, m, t)| (src, h, m, t.input, t.total))
        .collect();
    v.sort();
    v
}

#[test]
fn a_files_rows_and_revision_are_replaced_together_or_not_at_all() {
    let (mut s, d) = store("replace");
    s.replace_file(
        "p/a",
        "f1",
        10,
        100,
        &rows(&[(0, "m", tok(1, 1, 2)), (3600, "m", tok(2, 0, 2))]),
    )
    .unwrap();
    s.replace_file("p/a", "f2", 5, 50, &rows(&[(0, "m", tok(7, 0, 7))]))
        .unwrap();
    // Replaced as a whole: the old hour 3600 row is gone.
    s.replace_file("p/a", "f1", 20, 200, &rows(&[(0, "m", tok(4, 0, 4))]))
        .unwrap();
    let want = vec![("p/a".to_string(), 0, "m".to_string(), 11, 11)];
    assert_eq!(stored(&s), want);
    // A replace that fails part way (a trigger refuses its second row) leaves the old rows and
    // the old revision.
    let side = rusqlite::Connection::open(d.join("usage.db")).unwrap();
    side.execute_batch(
        "CREATE TRIGGER boom BEFORE INSERT ON usage WHEN NEW.model = 'boom'
         BEGIN SELECT RAISE(ABORT, 'refused'); END;",
    )
    .unwrap();
    let r = s.replace_file(
        "p/a",
        "f1",
        30,
        300,
        &rows(&[(0, "a", tok(100, 0, 100)), (0, "boom", tok(1, 0, 1))]),
    );
    assert!(r.unwrap_err().contains("refused"));
    assert_eq!(stored(&s), want);
    assert_eq!(s.revisions("p/a").unwrap()["f1"], (20, 200));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn clear_deletes_history_and_revisions_and_keeps_settings() {
    let (mut s, d) = store("clear");
    s.replace_file("p/a", "f1", 10, 100, &rows(&[(0, "m", tok(1, 1, 2))]))
        .unwrap();
    s.set_roots("p/a", Some(&["/r".to_string()])).unwrap();
    s.set_label("p/a", "A").unwrap();
    s.set_interval(30).unwrap();
    s.set_last_sync(5).unwrap();
    let (bytes, n, files) = s.storage().unwrap();
    assert!(bytes > 0);
    assert_eq!((n, files), (1, 1));
    s.clear().unwrap();
    assert!(stored(&s).is_empty());
    assert!(s.revisions("p/a").unwrap().is_empty());
    assert_eq!(s.last_sync().unwrap(), None);
    assert_eq!(s.interval().unwrap(), 30);
    assert_eq!(s.roots("p/a").unwrap(), Some(vec!["/r".to_string()]));
    assert_eq!(s.storage().unwrap().1, 0);
    std::fs::remove_dir_all(d).unwrap();
}

/// The host's job service, played: the registrations given, and every job started.
struct FakeHost {
    registrations: Vec<Value>,
    started: RefCell<Vec<(String, Value)>>,
    cancelled: RefCell<Vec<String>>,
}

impl FakeHost {
    fn new(packages: &[&str]) -> FakeHost {
        FakeHost {
            registrations: packages
                .iter()
                .map(|p| json!({"package": p, "sha256": format!("sha-{p}"), "generation": 1, "schema": 1}))
                .collect(),
            started: RefCell::new(Vec::new()),
            cancelled: RefCell::new(Vec::new()),
        }
    }
    /// The last job started, and its input.
    fn last(&self) -> (String, Value) {
        self.started.borrow().last().cloned().unwrap()
    }
}

impl Host for FakeHost {
    fn registrations(&self) -> Result<Vec<Value>, Error> {
        Ok(self.registrations.clone())
    }
    fn start(&self, r: &Value, input: Value) -> Result<String, Error> {
        let mut s = self.started.borrow_mut();
        let id = format!("j-{}", s.len() + 1);
        assert!(r["package"].is_string());
        // The host refuses an input over 64 KiB.
        let size = serde_json::to_vec(&input).unwrap().len();
        assert!(size <= 64 << 10, "a job input of {size} bytes");
        s.push((id.clone(), input));
        Ok(id)
    }
    fn cancel(&self, job: &str) -> Result<(), Error> {
        self.cancelled.borrow_mut().push(job.to_string());
        Ok(())
    }
}

fn delivery(job: &str, package: &str, event: &str, value: Value) -> Value {
    let mut d = json!({"job": job, "kind": KIND, "seq": 1, "event": event,
        "source": {"package": package, "sha256": format!("sha-{package}")}});
    if event == "batch" {
        d["value"] = value;
    } else if event == "done" {
        d["output"] = value;
    } else {
        d["error"] = json!({"code": event, "message": format!("the job was {event}")});
    }
    d
}

fn part(file: &str, size: u64, part: u64, last: bool, rows: Value) -> Value {
    json!({"file": file, "size": size, "mtime_ns": 7, "part": part, "last": last, "rows": rows})
}

fn row(hour: i64, model: &str, input: u64) -> Value {
    json!({"hour": hour, "model": model, "input": input, "output": 0, "cache_read": 0,
        "cache_write": 0, "reasoning": 0, "total": input, "samples": 1})
}

/// Lists files (ID, size) for package's files job and ends it.
fn list(sync: &mut Sync, s: &mut Store, job: &str, package: &str, files: &[(&str, u64)]) {
    let items: Vec<Value> = files
        .iter()
        .map(|(f, size)| json!({"file": f, "size": size, "mtime_ns": 7}))
        .collect();
    sync.ingest(
        s,
        T0 + 1,
        &delivery(job, package, "batch", json!({"files": items})),
    )
    .unwrap();
    sync.ingest(
        s,
        T0 + 1,
        &delivery(
            job,
            package,
            "done",
            json!({"label": "Parser A", "truncated": false}),
        ),
    )
    .unwrap();
}

#[test]
fn a_sync_lists_reads_changed_files_and_commits_each_once() {
    let (mut s, d) = store("sync");
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    // Never synced: due on its own.
    let st = sync.step(&mut s, &host, false, T0).unwrap();
    assert_eq!(st["running"], true);
    let (job, input) = host.last();
    assert_eq!(input, json!({"mode": "files"}));
    list(&mut sync, &mut s, &job, "p/a", &[("f1", 10), ("f2", 20)]);
    sync.step(&mut s, &host, false, T0 + 1).unwrap();
    let (read, input) = host.last();
    assert_eq!(input, json!({"mode": "read", "files": ["f1", "f2"]}));
    // f1 in two parts; models named alike become one; f2 cannot be read.
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(
            &read,
            "p/a",
            "batch",
            part("f1", 10, 0, false, json!([row(NOV10 + H, "Vendor/M-1", 5)])),
        ),
    )
    .unwrap();
    let st = sync.step(&mut s, &host, false, T0 + 2).unwrap();
    assert_eq!(st["sources"][0]["staged"], 1);
    assert!(stored(&s).is_empty());
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(
            &read,
            "p/a",
            "batch",
            part("f1", 10, 1, true, json!([row(NOV10 + H, " m-1 ", 2)])),
        ),
    )
    .unwrap();
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(
            &read,
            "p/a",
            "batch",
            json!({"file": "f2", "error": "unreadable"}),
        ),
    )
    .unwrap();
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(&read, "p/a", "done", json!({"read": 1, "failed": 1})),
    )
    .unwrap();
    let st = sync.step(&mut s, &host, false, T0 + 3).unwrap();
    assert_eq!(st["running"], false);
    assert_eq!(
        (
            &st["sources"][0]["listed"],
            &st["sources"][0]["changed"],
            &st["sources"][0]["read"],
            &st["sources"][0]["failed"],
            &st["sources"][0]["label"]
        ),
        (
            &json!(2),
            &json!(2),
            &json!(1),
            &json!(1),
            &json!("Parser A")
        )
    );
    assert_eq!(st["last_sync"], T0 + 3);
    assert_eq!(
        stored(&s),
        vec![("p/a".into(), NOV10 + H, "m-1".into(), 7, 7)]
    );
    // Not due before the interval (5 minutes) passes; due after it. Then f1 is unchanged and f2
    // (never committed) is read again.
    assert_eq!(
        sync.step(&mut s, &host, false, T0 + 3 + 299).unwrap()["running"],
        false
    );
    assert_eq!(host.started.borrow().len(), 2);
    assert_eq!(
        sync.step(&mut s, &host, false, T0 + 3 + 300).unwrap()["running"],
        true
    );
    let (job, _) = host.last();
    list(&mut sync, &mut s, &job, "p/a", &[("f1", 10), ("f2", 20)]);
    sync.step(&mut s, &host, false, T0 + 400).unwrap();
    assert_eq!(host.last().1["files"], json!(["f2"]));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_job_ended_mid_file_leaves_no_rows_and_a_resumed_sync_reads_only_what_is_missing() {
    let (mut s, d) = store("resume");
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0).unwrap();
    let (job, _) = host.last();
    list(&mut sync, &mut s, &job, "p/a", &[("f1", 10), ("f2", 20)]);
    sync.step(&mut s, &host, false, T0 + 1).unwrap();
    let (read, _) = host.last();
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(
            &read,
            "p/a",
            "batch",
            part("f1", 10, 0, true, json!([row(NOV10, "m", 3)])),
        ),
    )
    .unwrap();
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(
            &read,
            "p/a",
            "batch",
            part("f2", 20, 0, false, json!([row(NOV10, "m", 100)])),
        ),
    )
    .unwrap();
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(&read, "p/a", "cancelled", Value::Null),
    )
    .unwrap();
    // Only f1 is stored: f2's part went with its job.
    assert_eq!(stored(&s), vec![("p/a".into(), NOV10, "m".into(), 3, 3)]);
    let st = sync.step(&mut s, &host, false, T0 + 2).unwrap();
    assert_eq!(
        (&st["running"], &st["sources"][0]["error"]),
        (&json!(false), &json!("the job was cancelled"))
    );
    // The hub stops and starts again: a new sync skips f1 and reads f2.
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0 + 1000).unwrap();
    let (job, _) = host.last();
    list(&mut sync, &mut s, &job, "p/a", &[("f1", 10), ("f2", 20)]);
    let st = sync.step(&mut s, &host, false, T0 + 1001).unwrap();
    assert_eq!(st["sources"][0]["changed"], 1);
    assert_eq!(host.last().1["files"], json!(["f2"]));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn deliveries_not_from_the_jobs_source_or_out_of_order_are_refused() {
    let (mut s, d) = store("forged");
    let host = FakeHost::new(&["p/a", "p/b"]);
    let mut sync = Sync::new();
    // No sync under way.
    let e = sync
        .ingest(
            &mut s,
            T0 + 1,
            &delivery("j-1", "p/a", "batch", json!({"files": []})),
        )
        .unwrap_err();
    assert_eq!(e.code, "invalid_request");
    sync.step(&mut s, &host, true, T0).unwrap();
    // j-1 is p/a's files job: a batch naming p/b, or p/a at another digest, is refused.
    let e = sync
        .ingest(
            &mut s,
            T0 + 1,
            &delivery("j-1", "p/b", "batch", json!({"files": []})),
        )
        .unwrap_err();
    assert_eq!(e.code, "permission");
    let mut other = delivery("j-1", "p/a", "batch", json!({"files": []}));
    other["source"]["sha256"] = json!("sha-else");
    assert_eq!(
        sync.ingest(&mut s, T0 + 1, &other).unwrap_err().code,
        "permission"
    );
    // A job this sync did not start.
    let e = sync
        .ingest(
            &mut s,
            T0 + 1,
            &delivery("j-99", "p/a", "batch", json!({"files": []})),
        )
        .unwrap_err();
    assert_eq!(e.code, "invalid_request");
    list(&mut sync, &mut s, "j-1", "p/a", &[("f1", 10)]);
    list(&mut sync, &mut s, "j-2", "p/b", &[]);
    sync.step(&mut s, &host, false, T0 + 1).unwrap();
    let (read, _) = host.last();
    // A file the job does not read, then a part out of order: refused, nothing stored.
    let e = sync
        .ingest(
            &mut s,
            T0 + 1,
            &delivery(&read, "p/a", "batch", part("f9", 1, 0, true, json!([]))),
        )
        .unwrap_err();
    assert_eq!(e.code, "invalid_request");
    let e = sync
        .ingest(
            &mut s,
            T0 + 1,
            &delivery(
                &read,
                "p/a",
                "batch",
                part("f1", 10, 1, true, json!([row(NOV10, "m", 1)])),
            ),
        )
        .unwrap_err();
    assert_eq!(e.code, "invalid_request");
    assert!(stored(&s).is_empty());
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_store_error_at_a_jobs_end_fails_its_source_and_the_run_ends() {
    let (mut s, d) = store("end-error");
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0).unwrap();
    let (job, _) = host.last();
    // The store breaks under the hub: its files table is gone.
    rusqlite::Connection::open(d.join("usage.db"))
        .unwrap()
        .execute_batch("ALTER TABLE files RENAME TO elsewhere")
        .unwrap();
    list(&mut sync, &mut s, &job, "p/a", &[("f1", 10)]);
    let st = sync.step(&mut s, &host, false, T0 + 2).unwrap();
    assert_eq!(st["running"], false, "{st}");
    assert_eq!(st["sources"][0]["phase"], "failed");
    assert!(
        st["sources"][0]["error"]
            .as_str()
            .unwrap()
            .contains("files"),
        "{st}"
    );
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_job_with_no_word_for_a_minute_is_cancelled_and_the_run_ends() {
    let (mut s, d) = store("stale");
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0).unwrap();
    let (job, _) = host.last();
    // A batch at T0 + 50 is word from it: still waited for at T0 + 109, not at T0 + 110.
    let items = json!({"files": [{"file": "f1", "size": 1, "mtime_ns": 7}]});
    sync.ingest(&mut s, T0 + 50, &delivery(&job, "p/a", "batch", items))
        .unwrap();
    assert_eq!(
        sync.step(&mut s, &host, false, T0 + 109).unwrap()["running"],
        true
    );
    assert!(host.cancelled.borrow().is_empty());
    let st = sync.step(&mut s, &host, false, T0 + 110).unwrap();
    assert_eq!(*host.cancelled.borrow(), vec![job.clone()]);
    assert_eq!(
        (&st["running"], &st["sources"][0]["phase"]),
        (&json!(false), &json!("failed"))
    );
    assert!(st["sources"][0]["error"]
        .as_str()
        .unwrap()
        .contains("no word"));
    // Its end, if it still comes, is not this sync's.
    let late = delivery(&job, "p/a", "done", json!({"label": "A"}));
    assert_eq!(
        sync.ingest(&mut s, T0 + 111, &late).unwrap_err().code,
        "invalid_request"
    );
    // A read job that goes quiet is cancelled the same way.
    sync.step(&mut s, &host, true, T0 + 200).unwrap();
    let (job, _) = host.last();
    list(&mut sync, &mut s, &job, "p/a", &[("f1", 10)]);
    sync.step(&mut s, &host, false, T0 + 200).unwrap();
    let (read, _) = host.last();
    let st = sync.step(&mut s, &host, false, T0 + 260).unwrap();
    assert_eq!(host.cancelled.borrow().last(), Some(&read));
    assert_eq!(st["running"], false);
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_file_with_rows_out_of_bounds_fails_and_its_job_goes_on() {
    let (mut s, d) = store("bounds");
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0).unwrap();
    let (job, _) = host.last();
    list(
        &mut sync,
        &mut s,
        &job,
        "p/a",
        &[("old", 1), ("future", 1), ("big", 1), ("ok", 1)],
    );
    sync.step(&mut s, &host, false, T0 + 1).unwrap();
    let (read, _) = host.last();
    let batch = |v: Value| delivery(&read, "p/a", "batch", v);
    // 1970, and two days after now: refused, the file failed.
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("old", 1, 0, true, json!([row(0, "m", 5)]))),
    )
    .unwrap();
    let ahead = (T0 + 2 * 86_400).div_euclid(H) * H;
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("future", 1, 0, true, json!([row(ahead, "m", 5)]))),
    )
    .unwrap();
    // One row more than a file may stage.
    let many: Vec<Value> = (0..=sync::MAX_STAGED_ROWS as i64)
        .map(|k| row(NOV10 - k * H, "m", 1))
        .collect();
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("big", 1, 0, false, json!(many))),
    )
    .unwrap();
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("ok", 1, 0, true, json!([row(NOV10, "m", 2)]))),
    )
    .unwrap();
    sync.ingest(&mut s, T0 + 1, &delivery(&read, "p/a", "done", json!({})))
        .unwrap();
    let st = sync.step(&mut s, &host, false, T0 + 2).unwrap();
    assert_eq!(
        (&st["sources"][0]["read"], &st["sources"][0]["failed"]),
        (&json!(1), &json!(3))
    );
    assert_eq!(stored(&s), vec![("p/a".into(), NOV10, "m".into(), 2, 2)]);
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_file_refused_before_its_last_part_fails_once_and_its_job_goes_on() {
    let (mut s, d) = store("refused-parts");
    // Old rows of "a" and "big" (an earlier revision): a refused read leaves them.
    s.replace_file("p/a", "a", 1, 1, &rows(&[(NOV10, "m", tok(9, 0, 9))]))
        .unwrap();
    s.replace_file("p/a", "big", 1, 1, &rows(&[(NOV10, "m", tok(8, 0, 8))]))
        .unwrap();
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0).unwrap();
    let (job, _) = host.last();
    list(
        &mut sync,
        &mut s,
        &job,
        "p/a",
        &[("a", 5), ("big", 5), ("e", 5), ("b", 5)],
    );
    sync.step(&mut s, &host, false, T0 + 1).unwrap();
    let (read, _) = host.last();
    let batch = |v: Value| delivery(&read, "p/a", "batch", v);
    // "a": part 0 has an hour in 1970; its part 1 must be taken and dropped (its row is fine).
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("a", 5, 0, false, json!([row(0, "m", 5)]))),
    )
    .unwrap();
    // "big": one row more than a file may stage in part 0, then its parts 1 and 2.
    let many: Vec<Value> = (0..=sync::MAX_STAGED_ROWS as i64)
        .map(|k| row(NOV10 - k * H, "m", 1))
        .collect();
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("big", 5, 0, false, json!(many))),
    )
    .unwrap();
    // "e": refused in part 0, then the parser reports it unreadable.
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("e", 5, 0, false, json!([row(0, "m", 5)]))),
    )
    .unwrap();
    // The refused files' later parts (interleaved across files) and "e"'s error are taken.
    for (f, n, last) in [("a", 1, true), ("big", 1, false), ("big", 2, true)] {
        sync.ingest(
            &mut s,
            T0 + 1,
            &batch(part(f, 5, n, last, json!([row(NOV10, "m", 100)]))),
        )
        .unwrap();
    }
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(json!({"file": "e", "error": "unreadable"})),
    )
    .unwrap();
    // "b", read after them in the same job, commits.
    sync.ingest(
        &mut s,
        T0 + 1,
        &batch(part("b", 5, 0, true, json!([row(NOV10, "m", 2)]))),
    )
    .unwrap();
    // A part of a file already finished is still refused.
    let e = sync
        .ingest(
            &mut s,
            T0 + 1,
            &batch(part("a", 5, 2, true, json!([row(NOV10, "m", 1)]))),
        )
        .unwrap_err();
    assert_eq!(e.code, "invalid_request");
    sync.ingest(&mut s, T0 + 1, &delivery(&read, "p/a", "done", json!({})))
        .unwrap();
    let st = sync.step(&mut s, &host, false, T0 + 2).unwrap();
    let src = &st["sources"][0];
    assert_eq!(
        (&src["read"], &src["failed"], &src["staged"]),
        (&json!(1), &json!(3), &json!(0))
    );
    assert_eq!(src["phase"], "done");
    assert!(
        src["error"].as_str().unwrap().contains("more than")
            || src["error"].as_str().unwrap().contains("before 2000"),
        "{st}"
    );
    // The old rows of "a" and "big" stay (9 and 8), and "b" is stored (2): 19 in all, one hour.
    assert_eq!(stored(&s), vec![("p/a".into(), NOV10, "m".into(), 19, 19)]);
    assert_eq!(s.revisions("p/a").unwrap()["a"], (1, 1));
    std::fs::remove_dir_all(d).unwrap();
}

/// A run with files f1 (committed), f2 (a part staged, not last) and f3 (not reached) in one read
/// job; returns the job's ID. f2 has old rows of 8 tokens and f1 none.
fn read_job_midway(sync: &mut Sync, s: &mut Store, host: &FakeHost) -> String {
    s.replace_file("p/a", "f2", 1, 1, &rows(&[(NOV10, "m", tok(8, 0, 8))]))
        .unwrap();
    sync.step(s, host, true, T0).unwrap();
    let (job, _) = host.last();
    list(sync, s, &job, "p/a", &[("f1", 5), ("f2", 5), ("f3", 5)]);
    sync.step(s, host, false, T0 + 1).unwrap();
    let (read, input) = host.last();
    assert_eq!(input["files"], json!(["f1", "f2", "f3"]));
    let batch = |v: Value| delivery(&read, "p/a", "batch", v);
    sync.ingest(
        s,
        T0 + 1,
        &batch(part("f1", 5, 0, true, json!([row(NOV10, "m", 3)]))),
    )
    .unwrap();
    sync.ingest(
        s,
        T0 + 1,
        &batch(part("f2", 5, 0, false, json!([row(NOV10, "m", 100)]))),
    )
    .unwrap();
    read
}

#[test]
fn a_read_job_that_ends_without_all_its_files_counts_the_unread_ones_as_failed() {
    for end in ["failed", "cancelled", "done"] {
        let (mut s, d) = store(&format!("unread-{end}"));
        let host = FakeHost::new(&["p/a"]);
        let mut sync = Sync::new();
        let read = read_job_midway(&mut sync, &mut s, &host);
        sync.ingest(&mut s, T0 + 2, &delivery(&read, "p/a", end, Value::Null))
            .unwrap();
        let st = sync.step(&mut s, &host, false, T0 + 3).unwrap();
        let src = &st["sources"][0];
        // f1 committed; f2 (parts staged) and f3 (not reached) failed; the reason is shown.
        assert_eq!(
            (&src["read"], &src["failed"], &src["phase"], &src["error"]),
            (
                &json!(1),
                &json!(2),
                &json!("done"),
                &json!(if end == "done" {
                    "the parser ended its job without reading 2 of its files".to_string()
                } else {
                    format!("the job was {end}")
                })
            ),
            "{end}"
        );
        // f1 stored (3), f2's old rows stay (8), nothing of its staged part.
        assert_eq!(stored(&s), vec![("p/a".into(), NOV10, "m".into(), 11, 11)]);
        // The next sync reads the two unread files again, and not f1.
        sync.step(&mut s, &host, true, T0 + 100).unwrap();
        let (job, _) = host.last();
        list(
            &mut sync,
            &mut s,
            &job,
            "p/a",
            &[("f1", 5), ("f2", 5), ("f3", 5)],
        );
        // f2's old revision is (1, 1) and f1's is (5, 7): only f2 and f3 are changed.
        sync.step(&mut s, &host, false, T0 + 101).unwrap();
        assert_eq!(host.last().1["files"], json!(["f2", "f3"]), "{end}");
        std::fs::remove_dir_all(d).unwrap();
    }
}

#[test]
fn a_read_job_cancelled_as_stale_counts_its_unread_files_once_each() {
    let (mut s, d) = store("unread-stale");
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    let read = read_job_midway(&mut sync, &mut s, &host);
    // f3, refused in its first part, is counted when refused and not again when the job goes.
    sync.ingest(
        &mut s,
        T0 + 1,
        &delivery(
            &read,
            "p/a",
            "batch",
            part("f3", 5, 0, false, json!([row(0, "m", 1)])),
        ),
    )
    .unwrap();
    // Quiet from T0 + 1: still waited for at T0 + 60, cancelled at T0 + 61.
    let st = sync.step(&mut s, &host, false, T0 + 60).unwrap();
    assert_eq!(st["sources"][0]["failed"], 1);
    let st = sync.step(&mut s, &host, false, T0 + 61).unwrap();
    assert_eq!(host.cancelled.borrow().last(), Some(&read));
    let src = &st["sources"][0];
    assert_eq!(st["running"], false);
    // f1 read; f3 refused (1) and f2 unread (1).
    assert_eq!(
        (&src["read"], &src["failed"], &src["phase"]),
        (&json!(1), &json!(2), &json!("done"))
    );
    assert!(src["error"].as_str().unwrap().contains("no word"));
    assert_eq!(stored(&s), vec![("p/a".into(), NOV10, "m".into(), 11, 11)]);
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn read_jobs_fit_the_hosts_input_limit_with_long_roots_and_escaped_ids() {
    let (mut s, d) = store("chunks");
    // 16 KiB of roots, and file IDs whose JSON is six times their length (control characters).
    let roots: Vec<String> = (0..4)
        .map(|i| format!("/{i}{}", "r".repeat(4000)))
        .collect();
    s.set_roots("p/a", Some(&roots)).unwrap();
    let ids: Vec<String> = (0..20)
        .map(|i| format!("/{i}{}", "\u{1}".repeat(3000)))
        .collect();
    let host = FakeHost::new(&["p/a"]);
    let mut sync = Sync::new();
    sync.step(&mut s, &host, true, T0).unwrap();
    let (job, _) = host.last();
    let listed: Vec<(&str, u64)> = ids.iter().map(|i| (i.as_str(), 1)).collect();
    list(&mut sync, &mut s, &job, "p/a", &listed);
    // Four jobs at once, each within 64 KiB (FakeHost checks); the rest at the next calls.
    sync.step(&mut s, &host, false, T0 + 1).unwrap();
    let named: usize = host.started.borrow()[1..]
        .iter()
        .map(|(_, i)| i["files"].as_array().unwrap().len())
        .sum();
    assert!((1..20).contains(&named), "{named} files in the first jobs");
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn roots_longer_than_16_kib_are_refused() {
    // Each root is well under the 4,096-byte path limit; together they pass 16 KiB.
    let long: Vec<String> = (0..16)
        .map(|i| format!("/{i}{}", "r".repeat(1100)))
        .collect();
    let e = checked_roots(&json!({"roots": long})).unwrap_err();
    assert_eq!(e.code, "invalid_request");
    assert_eq!(
        checked_roots(&json!({"roots": ["/a", "/b"]})).unwrap(),
        vec!["/a", "/b"]
    );
    assert!(checked_roots(&json!({"roots": ["relative"]})).is_err());
}

#[test]
fn the_store_is_private_and_clear_replaces_one_that_will_not_open() {
    use std::os::unix::fs::PermissionsExt;
    let (_, d) = store("repair");
    let path = d.join("usage.db");
    let mode = |p: &Path| std::fs::metadata(p).unwrap().permissions().mode() & 0o777;
    assert_eq!(mode(&path), 0o600);
    // A store an earlier version left readable by others is made private when opened.
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
    open_store(&path, false).unwrap();
    assert_eq!(mode(&path), 0o600);
    std::fs::write(&path, b"not a database at all, not even close to one").unwrap();
    let e = open_store(&path, false).err().unwrap();
    assert!(
        e.message.contains("Clear Agent history replaces it"),
        "{}",
        e.message
    );
    let s = open_store(&path, true).unwrap();
    assert_eq!(s.storage().unwrap().1, 0);
    assert_eq!(mode(&path), 0o600);
    assert!(open_store(&path, false).is_ok());
    std::fs::remove_dir_all(d).unwrap();
}

/// Noon on 2026-11-10 UTC: the syncs' time.
const T0: i64 = NOV10 + 43_200;
/// 2026-11-10 00:00 UTC, a Tuesday.
const NOV10: i64 = 1_794_268_800;
const H: i64 = 3600;

fn ask(s: &Store, input: Value, registered: &[&str]) -> Value {
    let r: Vec<String> = registered.iter().map(|x| x.to_string()).collect();
    report::report(s, &input, &r, json!(null)).unwrap()
}

#[test]
fn reports_bucket_hours_in_the_viewers_zone() {
    let (mut s, d) = store("zone");
    // The zone is +01:00 until 2026-11-08 00:00 UTC, then +02:00.
    let offsets = json!([[0, 60], [NOV10 - 2 * 86_400, 120]]);
    // UTC hours: 11-06 23:00 (+1: 11-07 00:00), 11-07 22:00 (+1: 23:00 on 11-07),
    // 11-08 22:00 (+2: 11-09 00:00), 11-10 10:00 (+2: 12:00), and 2026-09-01 10:00.
    s.replace_file(
        "p/a",
        "f1",
        1,
        1,
        &rows(&[
            (NOV10 - 3 * 86_400 - H, "m1", tok(1, 0, 1)),
            (NOV10 - 2 * 86_400 - 2 * H, "m1", tok(10, 0, 10)),
            (NOV10 - 86_400 - 2 * H, "m2", tok(100, 0, 100)),
            (NOV10 + 10 * H, "m1", tok(1000, 0, 1000)),
        ]),
    )
    .unwrap();
    s.replace_file(
        "p/b",
        "g1",
        1,
        1,
        &rows(&[(1_788_256_800, "m3", tok(5, 0, 5))]),
    )
    .unwrap();
    let now = NOV10 + 13 * H; // 15:00 local
    let r = ask(
        &s,
        json!({"range": "7d", "offsets": offsets, "now": now}),
        &["p/a"],
    );
    assert_eq!(r["granularity"], "day");
    assert_eq!(
        r["buckets"],
        json!([
            "2026-11-04",
            "2026-11-05",
            "2026-11-06",
            "2026-11-07",
            "2026-11-08",
            "2026-11-09",
            "2026-11-10"
        ])
    );
    let totals: Vec<u64> = r["series"]
        .as_array()
        .unwrap()
        .iter()
        .map(|t| t["total"].as_u64().unwrap())
        .collect();
    assert_eq!(totals, vec![0, 0, 0, 11, 0, 100, 1000]);
    assert_eq!(
        (&r["totals"]["total"], &r["active_days"]),
        (&json!(1111), &json!(3))
    );
    assert_eq!(r["previous"]["total"], 0);
    assert_eq!(
        r["models"],
        json!([{"model": "m1", "total": 1011}, {"model": "m2", "total": 100}])
    );
    // p/b is not registered: archived, with nothing in these 7 days.
    assert_eq!(
        r["sources"],
        json!([
        {"id": "p/a", "label": "p/a", "status": "active", "total": 1111, "active_days": 3},
        {"id": "p/b", "label": "p/b", "status": "archived", "total": 0, "active_days": 0}])
    );
    // Hours: Saturday 11-07 00:00 and 23:00, Monday 11-09 00:00, Tuesday 11-10 12:00.
    let h = &r["hours"];
    assert_eq!(
        (&h[5][0], &h[5][23], &h[0][0], &h[1][12]),
        (&json!(1), &json!(10), &json!(100), &json!(1000))
    );
    let sum: u64 = h
        .as_array()
        .unwrap()
        .iter()
        .flat_map(|d| d.as_array().unwrap())
        .map(|v| v.as_u64().unwrap())
        .sum();
    assert_eq!(sum, 1111);
    // The calendar starts on the Monday on or before 2025-11-11 and ends today.
    assert_eq!(r["calendar"]["start"], "2025-11-10");
    let cal = r["calendar"]["totals"].as_array().unwrap();
    assert_eq!(cal.len(), 366);
    assert_eq!(
        (&cal[365], &cal[364], &cal[362], &cal[295]),
        (&json!(1000), &json!(100), &json!(11), &json!(5))
    );
    // Weeks from Monday: 11-02 to 11-08 holds 11-07; 11-09 to 11-15 the rest. The filter keeps
    // the series to p/a; previous: the 7 days before, 10-28 to 11-03, nothing.
    let r = ask(
        &s,
        json!({"range": "7d", "granularity": "week", "sources": ["p/a"], "offsets": offsets, "now": now}),
        &["p/a"],
    );
    assert_eq!(r["buckets"], json!(["2026-11-02", "2026-11-09"]));
    assert_eq!(
        r["by_source"],
        json!({"keys": ["p/a"], "values": [[11], [1100]]})
    );
    // Today in hours: 00:00 to 15:00 local; yesterday's same hours are the previous period.
    let r = ask(
        &s,
        json!({"range": "today", "offsets": offsets, "now": now}),
        &["p/a"],
    );
    assert_eq!(r["granularity"], "hour");
    assert_eq!(r["buckets"].as_array().unwrap().len(), 16);
    assert_eq!(
        (&r["buckets"][12], &r["series"][12]["total"]),
        (&json!("2026-11-10T12"), &json!(1000))
    );
    assert_eq!(r["previous"]["total"], 100);
    // All: from 2026-09-01's month; an hour asked for falls back to days, and with no
    // granularity, more than 92 days are weeks. No previous period.
    let r = ask(
        &s,
        json!({"range": "all", "granularity": "month", "offsets": offsets, "now": now}),
        &["p/a"],
    );
    assert_eq!(r["buckets"], json!(["2026-09", "2026-10", "2026-11"]));
    let totals: Vec<u64> = r["series"]
        .as_array()
        .unwrap()
        .iter()
        .map(|t| t["total"].as_u64().unwrap())
        .collect();
    assert_eq!((totals, &r["previous"]), (vec![5, 0, 1111], &Value::Null));
    let r = ask(
        &s,
        json!({"range": "all", "granularity": "hour", "offsets": offsets, "now": now}),
        &["p/a"],
    );
    assert_eq!(
        (&r["granularity"], r["buckets"].as_array().unwrap().len()),
        (&json!("day"), 71)
    );
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_trend_stacks_the_top_six_and_the_rest_as_other() {
    let (mut s, d) = store("top");
    // Eight models in one hour: m1 = 8 tokens, ..., m8 = 1.
    let list: Vec<(i64, String, Tokens)> = (1..=8)
        .map(|i| (NOV10, format!("m{i}"), tok(9 - i, 0, 9 - i)))
        .collect();
    let r: Rows = list.into_iter().map(|(h, m, t)| ((h, m), t)).collect();
    s.replace_file("p/a", "f1", 1, 1, &r).unwrap();
    let out = ask(
        &s,
        json!({"range": "today", "offsets": [[0, 0]], "now": NOV10 + 600}),
        &["p/a"],
    );
    assert_eq!(
        out["by_model"]["keys"],
        json!(["m1", "m2", "m3", "m4", "m5", "m6", "__other__"])
    );
    assert_eq!(out["by_model"]["values"][0], json!([8, 7, 6, 5, 4, 3, 3]));
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_report_refuses_bad_input() {
    let (s, d) = store("bad");
    for input in [
        json!({"range": "week", "offsets": [[0, 0]], "now": 0}),
        json!({"range": "7d", "offsets": [], "now": 0}),
        json!({"range": "7d", "offsets": [[10, 0], [5, 60]], "now": 0}),
        json!({"range": "7d", "offsets": [[0, 2000]], "now": 0}),
        json!({"range": "7d", "offsets": [[0, 0]], "now": -1}),
        json!({"range": "7d", "granularity": "year", "offsets": [[0, 0]], "now": 0}),
    ] {
        let e = report::report(&s, &input, &[], json!(null)).unwrap_err();
        assert_eq!(e.code, "invalid_request", "{input}");
    }
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn the_last_24_hours_are_24_utc_hours_across_a_clock_change() {
    let (mut s, d) = store("24h");
    // +01:00, then +02:00 from 2026-11-10 00:00 UTC; now 13:00 UTC.
    let offsets = json!([[0, 60], [NOV10, 120]]);
    let now = NOV10 + 13 * H;
    // One token in each of the 30 UTC hours to 13:00.
    let list: Vec<(i64, &str, Tokens)> =
        (0..30).map(|k| (now - k * H, "m", tok(1, 0, 1))).collect();
    s.replace_file("p/a", "f1", 1, 1, &rows(&list)).unwrap();
    let r = ask(
        &s,
        json!({"range": "24h", "offsets": offsets, "now": now}),
        &["p/a"],
    );
    let keys = r["buckets"].as_array().unwrap();
    // From 2026-11-09 14:00 UTC (15:00 local) to 13:00 UTC (15:00 local): 24 hours, 24 tokens.
    assert_eq!(
        (keys.len(), &keys[0], &keys[23]),
        (24, &json!("2026-11-09T15"), &json!("2026-11-10T15"))
    );
    assert_eq!(
        (&r["totals"]["total"], &r["previous"]["total"]),
        (&json!(24), &json!(6))
    );
    std::fs::remove_dir_all(d).unwrap();
}

#[test]
fn a_report_always_fits_a_frame() {
    let (mut s, d) = store("fits");
    // The review's probe: a row at hour 0 and one now; All by day.
    s.replace_file(
        "p/a",
        "f1",
        1,
        1,
        &rows(&[(0, "m", tok(1, 0, 1)), (NOV10, "m", tok(1, 0, 1))]),
    )
    .unwrap();
    let r = ask(
        &s,
        json!({"range": "all", "granularity": "day", "offsets": [[0, 0]], "now": NOV10}),
        &["p/a"],
    );
    // From 2000: 323 months, after 9,811 days and 1,402 weeks were too many.
    assert_eq!(
        (
            &r["granularity"],
            r["buckets"].as_array().unwrap().len(),
            &r["buckets"][0]
        ),
        (&json!("month"), 323, &json!("2000-01"))
    );
    assert!(serde_json::to_vec(&r).unwrap().len() < 1 << 20);
    // About 11.5 years of daily rows from three sources and seven models each.
    let (mut s2, d2) = store("fits-big");
    for src in ["p/a", "p/b", "p/c"] {
        let list: Vec<(i64, String, Tokens)> = (0..4200)
            .flat_map(|k| {
                (0..7).map(move |m| {
                    (
                        NOV10 - k * 86_400,
                        format!("model-{src}-{m}"),
                        tok(123_456_789, 0, 987_654_321),
                    )
                })
            })
            .collect();
        let r: Rows = list.into_iter().map(|(h, m, t)| ((h, m), t)).collect();
        s2.replace_file(src, "f", 1, 1, &r).unwrap();
    }
    let r = ask(
        &s2,
        json!({"range": "all", "granularity": "day", "offsets": [[0, 0]], "now": NOV10}),
        &["p/a"],
    );
    assert_eq!(r["granularity"], "week");
    let size = serde_json::to_vec(&r).unwrap().len();
    assert!(size < 1 << 20, "{size} bytes");
    // Models past 200 are summed as Other.
    let (mut s3, d3) = store("fits-models");
    let many: Rows = (0..205)
        .map(|m| ((NOV10, format!("m{m:03}")), tok(1, 0, 1)))
        .collect();
    s3.replace_file("p/a", "f", 1, 1, &many).unwrap();
    let r = ask(
        &s3,
        json!({"range": "today", "offsets": [[0, 0]], "now": NOV10 + 60}),
        &["p/a"],
    );
    let models = r["models"].as_array().unwrap();
    assert_eq!(
        (models.len(), &models[200]),
        (201, &json!({"model": "__other__", "total": 5}))
    );
    for dir in [d, d2, d3] {
        std::fs::remove_dir_all(dir).unwrap();
    }
}
