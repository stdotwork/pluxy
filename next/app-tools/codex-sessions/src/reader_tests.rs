//! The reader against Codex homes built here, after the old app's tests (in
//! router-for-me/EasyCLIProxyAPI, https://github.com/router-for-me/EasyCLIProxyAPI:
//! `src-tauri/src/codex_sessions.rs` and `src-tauri/src/codex_sessions/context.rs`).

use super::*;
use std::sync::atomic::{AtomicU64, Ordering};

/// A new empty folder, removed when dropped.
struct Root(PathBuf);

impl Root {
    fn new(name: &str) -> Root {
        static N: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "codex-sessions-{name}-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = fs::remove_dir_all(&p);
        fs::create_dir_all(&p).unwrap();
        Root(p)
    }
}

impl Drop for Root {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

impl std::ops::Deref for Root {
    type Target = Path;
    fn deref(&self) -> &Path {
        &self.0
    }
}

fn sql(path: &Path, batch: &str) {
    let c = Connection::open(path).unwrap();
    c.execute_batch(batch).unwrap();
}

fn never() -> bool {
    false
}

/// The `-wal` or `-shm` file beside a database.
fn sidecar(path: &Path, suffix: &str) -> PathBuf {
    let mut s = path.as_os_str().to_os_string();
    s.push(suffix);
    PathBuf::from(s)
}

fn write(path: &Path, text: &str) {
    fs::create_dir_all(path.parent().unwrap()).unwrap();
    fs::write(path, text).unwrap();
}

#[test]
fn list_merges_threads_and_automation_rows_in_update_order() {
    let root = Root::new("list");
    let rollout = root.join("sessions/rollout.jsonl");
    write(&rollout, "{}\n");
    sql(
        &root.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, title TEXT, cwd TEXT, model_provider TEXT, archived INTEGER, has_user_event INTEGER, updated_at_ms INTEGER);
             INSERT INTO threads VALUES ('thread-1', '{}', 'First', 'C:/old', 'openai', 0, 0, 200);",
            rollout.display()
        ),
    );
    fs::create_dir_all(root.join("sqlite")).unwrap();
    sql(
        &root.join("sqlite/codex-dev.db"),
        "CREATE TABLE automation_runs (thread_id TEXT PRIMARY KEY, status TEXT, thread_title TEXT, source_cwd TEXT, updated_at INTEGER);
         INSERT INTO automation_runs VALUES ('thread-1', 'running', 'First new', 'C:/newer', 4);
         INSERT INTO automation_runs VALUES ('thread-2', 'archived', 'Second', 'C:/new', 3);",
    );
    let page = list(&root, 0, 50);
    assert_eq!(page.sessions.len(), 2);
    assert_eq!(page.total, 2);
    assert_eq!(page.sessions[0].id, "thread-1");
    assert_eq!(page.sessions[0].title, "First new");
    assert_eq!(page.sessions[0].provider, "openai");
    assert_eq!(page.sessions[0].updated_ms, Some(4000));
    assert_eq!(page.sessions[1].id, "thread-2");
    assert!(page.sessions[1].archived);
    let second = list(&root, 1, 1);
    assert_eq!(second.sessions.len(), 1);
    assert_eq!(second.total, 2);
    assert_eq!(second.sessions[0].id, "thread-2");
    assert!(!second.has_more);
    let v = second.to_json();
    assert_eq!(
        (v["active"].clone(), v["archived"].clone()),
        (json!(0), json!(1))
    );
}

#[test]
fn list_includes_unindexed_sessions_without_rollout_metadata() {
    let root = Root::new("list-all-providers");
    let api = "019f1234-abcd-7000-8000-000000000001";
    let oauth = "019f1234-abcd-7000-8000-000000000002";
    write(
        &root.join(format!("sessions/rollout-2026-08-04T12-00-00-{api}.jsonl")),
        "not parsed by the session list\n",
    );
    write(
        &root.join(format!(
            "archived_sessions/rollout-2026-08-03T12-00-00-{oauth}.jsonl"
        )),
        "not parsed by the session list\n",
    );
    write(
        &root.join("session_index.jsonl"),
        &format!(
            "{}\n{}\n",
            json!({"id": api, "thread_name": "API session", "updated_at": "2026-08-04T12:00:00Z"}),
            json!({"id": oauth, "thread_name": "OAuth session", "updated_at": "2026-08-03T12:00:00Z"})
        ),
    );
    let page = list(&root, 0, 50);
    assert_eq!(page.sessions.len(), 2);
    assert_eq!(page.sessions[0].id, api);
    assert_eq!(page.sessions[0].title, "API session");
    assert!(!page.sessions[0].archived);
    assert_eq!(page.sessions[1].id, oauth);
    assert_eq!(page.sessions[1].title, "OAuth session");
    assert!(page.sessions[1].archived);
    assert!(page
        .sessions
        .iter()
        .all(|s| s.cwd.is_empty() && s.provider.is_empty()));
}

#[test]
fn listing_uses_filenames_and_index_without_parsing_rollouts() {
    let root = Root::new("list-no-rollout-reads");
    let mut index = String::new();
    for n in 0..6 {
        let id = format!("019f1234-abcd-7000-8000-{n:012}");
        let dir = if n % 2 == 0 {
            "sessions"
        } else {
            "archived_sessions"
        };
        let path = root
            .join(dir)
            .join(format!("rollout-2026-10-10T10-00-00-{id}.jsonl"));
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(
            path,
            b"not JSON; listing must not read or parse this file\xff",
        )
        .unwrap();
        index.push_str(&format!(
            "{}\n",
            json!({"id": id, "thread_name": format!("Session {n}"), "updated_at": (1000 + n).to_string()})
        ));
    }
    write(&root.join("session_index.jsonl"), &index);
    let page = list(&root, 2, 2);
    assert_eq!(page.total, 6);
    assert_eq!(page.sessions.len(), 2);
    assert!(page.has_more);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    assert_eq!(page.sessions[0].title, "Session 3");
    assert!(page.sessions[0].archived);
    assert_eq!(page.sessions[1].title, "Session 2");
    assert!(!page.sessions[1].archived);
}

#[test]
fn list_accepts_optional_thread_columns_and_warns_about_a_broken_database() {
    let root = Root::new("list-optional");
    fs::create_dir_all(root.join("sqlite")).unwrap();
    sql(
        &root.join("sqlite/optional.sqlite3"),
        "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT);
         CREATE TABLE inbox_items (id TEXT PRIMARY KEY);
         INSERT INTO threads VALUES ('optional-thread', 'Optional');",
    );
    fs::write(root.join("sqlite/broken.db"), b"not a sqlite database").unwrap();
    let page = list(&root, 0, 0);
    assert_eq!(page.limit, 1);
    assert_eq!(page.sessions.len(), 1);
    assert_eq!(page.sessions[0].id, "optional-thread");
    assert_eq!(page.sessions[0].cwd, "");
    assert_eq!(page.sessions[0].updated_ms, None);
    // One warning for the broken database, naming it.
    assert_eq!(page.warnings.len(), 1, "{:?}", page.warnings);
    assert!(page.warnings[0].contains("broken.db"));
    assert_eq!(list(&root, 0, usize::MAX).limit, MAX_PAGE_SIZE);
}

#[test]
fn local_prefixed_threads_and_their_rollouts_are_one_session() {
    let root = Root::new("list-local");
    let id = "019f1234-abcd-7000-8000-0000000000aa";
    sql(
        &root.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT, updated_at INTEGER);
             INSERT INTO threads VALUES ('local:{id}', 'Local', 1754000000);"
        ),
    );
    write(&root.join(format!("sessions/rollout-x-{id}.jsonl")), "");
    let page = list(&root, 0, 50);
    assert_eq!(page.total, 1);
    assert_eq!(page.sessions.len(), 1);
    assert_eq!(page.sessions[0].id, format!("local:{id}"));
    assert_eq!(page.sessions[0].updated_ms, Some(1_754_000_000_000));
}

#[test]
fn links_in_the_session_folders_are_skipped() {
    if !links_allowed() {
        return;
    }
    let root = Root::new("list-links");
    let outside = Root::new("list-links-outside");
    let id = "019f1234-abcd-7000-8000-0000000000bb";
    let target = outside.join(format!("rollout-{id}.jsonl"));
    write(&target, "");
    fs::create_dir_all(root.join("sessions")).unwrap();
    make_link(&target, root.join(format!("sessions/rollout-{id}.jsonl"))).unwrap();
    assert!(list(&root, 0, 50).sessions.is_empty());
    assert!(find_rollout(&root, id).is_none());
}

#[test]
fn timestamps_parse_as_the_old_app_did() {
    assert_eq!(
        parse_timestamp_ms("2025-08-05T09:00:00Z"),
        Some(1_754_384_400_000)
    );
    assert_eq!(
        parse_timestamp_ms("2025-08-05T11:00:00.250+02:00"),
        Some(1_754_384_400_250)
    );
    assert_eq!(parse_timestamp_ms("1970-01-01T00:00:00Z"), Some(0));
    assert_eq!(parse_timestamp_ms("1003"), Some(1_003_000));
    assert_eq!(parse_timestamp_ms("1754384400000"), Some(1_754_384_400_000));
    assert_eq!(parse_timestamp_ms(""), None);
    assert_eq!(parse_timestamp_ms("2025-08-05"), None);
    assert_eq!(parse_timestamp_ms("2025-13-05T09:00:00Z"), None);
}

#[test]
fn rollout_ids_come_from_the_end_of_the_file_name() {
    let p = |n: &str| rollout_id(Path::new(n));
    assert_eq!(
        p("rollout-2026-10-05T12-00-00-019f1234-abcd-7000-8000-000000000020_019f1234-abcd-7000-8000-000000000021.jsonl").as_deref(),
        Some("019f1234-abcd-7000-8000-000000000021")
    );
    assert_eq!(p("rollout-short.jsonl"), None);
    assert_eq!(p("other-019f1234-abcd-7000-8000-000000000021.jsonl"), None);
    assert_eq!(
        p("rollout-019f1234-abcd-7000-8000-00000000002g.jsonl"),
        None
    );
}

#[test]
fn session_ids_with_paths_or_too_long_are_refused() {
    for bad in [
        "../etc/passwd",
        "..\\windows\\system32",
        "test/session",
        "test:session",
        "",
        "   ",
        "a.b",
    ] {
        assert!(validate_id(bad).is_err(), "{bad}");
    }
    assert_eq!(
        validate_id("01a0ba19-e1ac-7d00-9136-563b25bbdb86").unwrap(),
        "01a0ba19-e1ac-7d00-9136-563b25bbdb86"
    );
    assert_eq!(validate_id("local:abc").unwrap(), "abc");
    assert!(validate_id(&"a".repeat(129)).is_err());
    assert!(validate_id(&"a".repeat(128)).is_ok());
}

#[test]
fn lookup_does_not_open_unrelated_rollouts() {
    let root = Root::new("detail-no-unrelated");
    let id = "019f1234-abcd-7000-8000-000000000030";
    let other = "019f1234-abcd-7000-8000-000000000031";
    write(
        &root.join(format!("sessions/rollout-{other}.jsonl")),
        &format!(
            "{}\n",
            json!({"type": "session_meta", "payload": {"id": id}})
        ),
    );
    assert!(find_rollout(&root, id).is_none());
    assert!(matches!(
        detail(&root, id, 0, 50, &never),
        Err(DetailError::NotFound(_))
    ));
}

#[test]
fn a_database_rollout_outside_the_codex_home_is_not_read() {
    let root = Root::new("detail-outside");
    let outside = Root::new("detail-outside-file");
    let id = "019f1234-abcd-7000-8000-000000000010";
    let file = outside.join("outside.jsonl");
    write(
        &file,
        &format!(
            "{}\n",
            json!({"type": "response_item", "payload": {"type": "message", "role": "user", "content": "outside"}})
        ),
    );
    sql(
        &root.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, title TEXT);
             INSERT INTO threads VALUES ('{id}', '{}', 'Outside');",
            file.display()
        ),
    );
    let d = detail(&root, id, 0, 50, &never).unwrap();
    assert_eq!(d["rollout"], Value::Null);
    assert_eq!(d["title"], "Outside");
    assert_eq!(d["messages"], json!([]));
}

#[test]
fn the_child_id_in_a_forked_rollouts_name_is_the_one_found() {
    let root = Root::new("detail-parent-child");
    let parent = "019f1234-abcd-7000-8000-000000000020";
    let child = "019f1234-abcd-7000-8000-000000000021";
    let rollout = root.join(format!(
        "sessions/2026/10/05/rollout-2026-10-05T12-00-00-{parent}_{child}.jsonl"
    ));
    write(
        &rollout,
        &format!(
            "{}\n",
            json!({"type": "session_meta", "payload": {"id": child}})
        ),
    );
    assert_eq!(
        find_rollout(&root, child),
        Some(fs::canonicalize(&rollout).unwrap())
    );
    assert!(find_rollout(&root, parent).is_none());
}

#[test]
fn detail_reads_a_legacy_schema_without_optional_columns() {
    let root = Root::new("detail-legacy");
    let id = "019f1234-abcd-7000-8000-000000000002";
    let rollout = root.join(format!("sessions/rollout-{id}.jsonl"));
    write(
        &rollout,
        &format!(
            "{}\n",
            json!({"type": "session_meta", "payload": {"id": id, "title": "Legacy"}})
        ),
    );
    sql(
        &root.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, title TEXT, cwd TEXT, model_provider TEXT);
             INSERT INTO threads VALUES ('{id}', '{}', 'Legacy', 'C:\\Repo', 'openai');",
            rollout.display()
        ),
    );
    let d = detail(&root, id, 0, 50, &never).unwrap();
    assert_eq!(d["model"], Value::Null);
    assert_eq!(d["updated_ms"], Value::Null);
    assert_eq!(d["title"], "Legacy");
    assert_eq!(d["cwd"], "C:\\Repo");
}

#[test]
fn detail_takes_the_latest_model_and_pages_capped_messages() {
    let root = Root::new("detail-model");
    let id = "019f1234-abcd-7000-8000-000000000095";
    let lines = [
        json!({"type": "session_meta", "payload": {"id": id, "cwd": "/w", "model_provider": "openai"}}),
        json!({"type": "turn_context", "payload": {"model": "first-model"}}),
        json!({"type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "First turn"}]}}),
        json!({"type": "turn_context", "payload": {"model": "latest-model"}}),
        json!({"type": "response_item", "payload": {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "é".repeat(5000)}]}}),
        json!({"type": "response_item", "payload": {"type": "reasoning", "encrypted_content": "x"}}),
        json!({"type": "response_item", "payload": {"type": "custom_tool_call", "name": "apply", "input": "patch", "call_id": "c1"}}),
        json!({"type": "response_item", "payload": {"type": "function_call_output", "call_id": "c1", "output": {"ok": true}}}),
    ];
    let text: String = lines.iter().map(|l| format!("{l}\n")).collect();
    write(&root.join(format!("sessions/rollout-{id}.jsonl")), &text);
    let d = detail(&root, id, 0, 50, &never).unwrap();
    assert_eq!(d["model"], "latest-model");
    assert_eq!(
        (d["cwd"].clone(), d["provider"].clone()),
        (json!("/w"), json!("openai"))
    );
    assert_eq!(d["total"], 5);
    assert_eq!(d["next_offset"], Value::Null);
    assert_eq!(
        d["stats"],
        json!({"lines": 8, "messages": 3, "user": 1, "assistant": 1, "tool": 2, "reasoning": 1, "size": text.len()})
    );
    let m = d["messages"].as_array().unwrap();
    // 5,000 two-byte characters, cut at 8 KiB on a character boundary.
    assert_eq!(m[1]["truncated"], true);
    assert_eq!(m[1]["content"].as_str().unwrap().len(), MAX_MESSAGE);
    assert_eq!(m[2]["content"], "[Encrypted reasoning block]");
    assert_eq!(m[3]["content"], "apply(patch)");
    assert_eq!(m[3]["call_id"], "c1");
    assert_eq!(m[4]["content"], "{\"ok\":true}");
    let page = detail(&root, id, 1, 2, &never).unwrap();
    assert_eq!(page["messages"].as_array().unwrap().len(), 2);
    assert_eq!(page["messages"][0]["line"], 4);
    assert_eq!(page["next_offset"], 3);
    assert_eq!(
        detail(&root, id, 0, 50, &|| true),
        Err(DetailError::Cancelled)
    );
}

#[test]
fn a_detail_page_stays_within_its_budget() {
    let root = Root::new("detail-budget");
    let id = "019f1234-abcd-7000-8000-000000000096";
    let line = json!({"type": "response_item", "payload": {"type": "message", "role": "user", "content": "\u{1}".repeat(MAX_MESSAGE)}});
    let text: String = (0..100).map(|_| format!("{line}\n")).collect();
    write(&root.join(format!("sessions/rollout-{id}.jsonl")), &text);
    let d = detail(&root, id, 0, 100, &never).unwrap();
    let n = d["messages"].as_array().unwrap().len();
    assert!(n > 0 && n < 100, "{n}");
    assert!(d["messages"].to_string().len() <= MAX_DETAIL);
    assert_eq!(d["next_offset"], n);
    assert_eq!(d["total"], 100);
}

#[test]
fn reading_a_wal_database_leaves_nothing_beside_it() {
    let root = Root::new("wal");
    fs::create_dir_all(root.join("sqlite")).unwrap();
    let db = root.join("sqlite/threads.db");
    {
        let c = Connection::open(&db).unwrap();
        c.execute_batch(
            "PRAGMA journal_mode = WAL;
             CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, updated_at_ms INTEGER);
             INSERT INTO threads VALUES ('w', 'WAL thread', 5);",
        )
        .unwrap();
    }
    // The database is in WAL mode (byte 18 of its header is 2).
    assert_eq!(fs::read(&db).unwrap()[18], 2);
    assert!(!sidecar(&db, "-wal").exists() && !sidecar(&db, "-shm").exists());
    let before = fs::read(&db).unwrap();
    let page = list(&root, 0, 50);
    assert_eq!(page.sessions[0].title, "WAL thread");
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    assert!(detail(&root, "w", 0, 50, &never).is_ok());
    assert!(!sidecar(&db, "-wal").exists(), "a -wal file appeared");
    assert!(!sidecar(&db, "-shm").exists(), "a -shm file appeared");
    assert_eq!(fs::read(&db).unwrap(), before);
}

#[test]
fn a_path_with_uri_characters_opens() {
    // `?` cannot be in a Windows file name.
    let root = Root::new(if cfg!(windows) {
        "uri #%&="
    } else {
        "uri ?#%&="
    });
    sql(
        &root.join("state_5.sqlite"),
        "CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT); INSERT INTO threads VALUES ('u', 'Odd path');",
    );
    let page = list(&root, 0, 50);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    assert_eq!(page.sessions[0].title, "Odd path");
}

#[test]
fn the_page_never_writes_html() {
    let dir = Path::new(env!("CARGO_MANIFEST_DIR")).join("pages");
    for name in ["app.js", "index.html"] {
        let text = fs::read_to_string(dir.join(name)).unwrap();
        for sink in [
            "innerHTML",
            "outerHTML",
            "insertAdjacentHTML",
            "document.write",
            "DOMParser",
            "createContextualFragment",
        ] {
            assert!(!text.contains(sink), "{name} uses {sink}");
        }
    }
}

/// A WAL database at dest whose row `b` is only in its -wal (committed, not copied into the
/// database), with no -shm beside it: as Codex leaves one that a crash stopped.
fn wal_with_uncheckpointed_rows(dest: &Path) {
    let scratch = Root::new("wal-source");
    let db = scratch.join("live.db");
    let c = Connection::open(&db).unwrap();
    c.execute_batch(
        "PRAGMA journal_mode = WAL;
         CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, updated_at_ms INTEGER);
         INSERT INTO threads VALUES ('a', 'Checkpointed', 1);
         PRAGMA wal_checkpoint(TRUNCATE);
         PRAGMA wal_autocheckpoint = 0;
         INSERT INTO threads VALUES ('b', 'Only in the WAL', 2);",
    )
    .unwrap();
    fs::create_dir_all(dest.parent().unwrap()).unwrap();
    fs::copy(&db, dest).unwrap();
    fs::copy(sidecar(&db, "-wal"), sidecar(dest, "-wal")).unwrap();
    drop(c);
}

#[test]
fn committed_rows_still_in_the_wal_are_read_and_nothing_is_written_beside_it() {
    let _lock = COPY_ROOT.lock().unwrap_or_else(|e| e.into_inner());
    let root = Root::new("wal-rows");
    let db = root.join("sqlite/live.db");
    wal_with_uncheckpointed_rows(&db);
    let wal = sidecar(&db, "-wal");
    assert!(fs::metadata(&wal).unwrap().len() > 0);
    assert!(!sidecar(&db, "-shm").exists());
    let before = (fs::read(&db).unwrap(), fs::read(&wal).unwrap());
    let page = list(&root, 0, 50);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    let ids: Vec<&str> = page.sessions.iter().map(|s| s.id.as_str()).collect();
    assert_eq!(ids, ["b", "a"]);
    assert_eq!(
        detail(&root, "b", 0, 50, &never).unwrap()["title"],
        "Only in the WAL"
    );
    assert!(!sidecar(&db, "-shm").exists(), "a -shm file appeared");
    assert_eq!((fs::read(&db).unwrap(), fs::read(&wal).unwrap()), before);
    // The private copies are gone.
    let prefix = format!("wal-{}-", std::process::id());
    assert!(!fs::read_dir(std::env::temp_dir())
        .unwrap()
        .flatten()
        .any(|e| e.file_name().to_string_lossy().starts_with(&prefix)));
}

/// Tests that set `PLUXY_TOOL_DATA_DIR`, or read a -wal that needs a private copy, take this lock:
/// the variable is the process's, and a copy's folder is under it while it is set.
static COPY_ROOT: std::sync::Mutex<()> = std::sync::Mutex::new(());

#[test]
fn a_database_that_cannot_be_opened_is_a_warning_that_names_it_once_and_not_sqlites_uri() {
    let root = Root::new("unreadable");
    let db = root.join("state_5.sqlite");
    sql(
        &db,
        "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT); INSERT INTO threads VALUES ('x', 'Readable');",
    );
    // The positive control: readable, it is listed with no warning.
    let page = list(&root, 0, 50);
    assert_eq!(page.sessions.len(), 1);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&db, fs::Permissions::from_mode(0o000)).unwrap();
    }
    #[cfg(windows)]
    deny_reading(&db, true);
    if fs::File::open(&db).is_ok() {
        return; // running as root: nothing is unreadable
    }
    let page = list(&root, 0, 50);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&db, fs::Permissions::from_mode(0o600)).unwrap();
    }
    #[cfg(windows)]
    deny_reading(&db, false);
    assert!(page.sessions.is_empty());
    let shown = db.to_string_lossy().into_owned();
    assert_eq!(page.warnings.len(), 1, "{:?}", page.warnings);
    let w = &page.warnings[0];
    assert!(w.starts_with("Failed to open database "), "{w}");
    // SQLite's own message repeats the file as a `file:` URI; only the page's name for it is shown.
    assert_eq!(w.matches(&shown).count(), 1, "{w}");
    assert!(!w.contains("file:"), "{w}");
}

#[test]
fn a_database_of_no_bytes_with_a_wal_that_holds_bytes_leaves_the_wal_alone() {
    // SQLite opening such a pair read-only deletes a -wal that holds bytes; a candidate is opened to see its tables.
    let root = Root::new("empty-db-wal");
    let dir = root.join("sqlite");
    fs::create_dir_all(&dir).unwrap();
    let db = dir.join("empty.db");
    fs::write(&db, b"").unwrap();
    fs::write(sidecar(&db, "-wal"), [7u8; 64]).unwrap();
    let page = list(&root, 0, 50);
    assert!(
        page.databases.is_empty() && page.warnings.is_empty(),
        "{:?}",
        page.warnings
    );
    assert_eq!(
        fs::read(sidecar(&db, "-wal")).ok(),
        Some(vec![7u8; 64]),
        "the -wal was deleted or changed"
    );
    assert!(!sidecar(&db, "-shm").exists(), "a -shm appeared");
}

#[test]
fn the_opener_refuses_a_database_that_is_not_a_regular_file() {
    if !links_allowed() {
        return;
    }
    let root = Root::new("opener-link");
    let db = root.join("real.db");
    sql(
        &db,
        "CREATE TABLE threads (id TEXT PRIMARY KEY); INSERT INTO threads VALUES ('x');",
    );
    let link = root.join("link.db");
    make_link(&db, &link).unwrap();
    // The positive control: the file itself opens.
    assert!(open_read_only(&db).is_ok());
    let e = open_read_only(&link)
        .err()
        .expect("a linked database opened");
    assert!(
        e.starts_with("Skipped database ") && e.contains(&*link.to_string_lossy()),
        "{e}"
    );
}

#[test]
fn only_private_copies_of_processes_no_longer_running_are_removed() {
    let _lock = COPY_ROOT.lock().unwrap_or_else(|e| e.into_inner());
    let root = Root::new("prune-root");
    let data = Root::new("prune-data");
    let db = root.join("sqlite/live.db");
    wal_with_uncheckpointed_rows(&db);
    let tmp = data.join("tmp");
    // A process that is running and one that is not, each with a copy left in the folder. On Linux
    // a copy's process is told by the pid in its name (pid 1 runs); on macOS and Windows by the
    // lock beside it, which a running process holds (pluxy-sqlite-ro).
    #[cfg(not(any(windows, target_os = "macos")))]
    let dead = {
        let mut child = std::process::Command::new("true").spawn().unwrap();
        let dead = child.id();
        child.wait().unwrap();
        dead
    };
    #[cfg(any(windows, target_os = "macos"))]
    let dead = 4_294_967_294u32;
    for left in ["wal-1-0".to_string(), format!("wal-{dead}-0")] {
        fs::create_dir_all(tmp.join(left)).unwrap();
    }
    #[cfg(any(windows, target_os = "macos"))]
    let _held = pluxy_files::create_locked(&tmp.join("wal-1-0.lock")).unwrap();
    std::env::set_var("PLUXY_TOOL_DATA_DIR", &*data);
    let page = list(&root, 0, 50);
    std::env::remove_var("PLUXY_TOOL_DATA_DIR");
    let ids: Vec<&str> = page.sessions.iter().map(|s| s.id.as_str()).collect();
    assert_eq!(ids, ["b", "a"], "{:?}", page.warnings);
    assert!(
        tmp.join("wal-1-0").exists(),
        "a running process's copy was removed"
    );
    assert!(
        !tmp.join(format!("wal-{dead}-0")).exists(),
        "a stopped process's copy stayed"
    );
}

#[test]
fn every_string_from_a_database_or_rollout_is_capped_and_a_page_fits_a_frame() {
    let root = Root::new("caps");
    let c = Connection::open(root.join("state_5.sqlite")).unwrap();
    c.execute_batch("CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, cwd TEXT, model_provider TEXT, model TEXT, rollout_path TEXT, updated_at_ms INTEGER);")
        .unwrap();
    let big = "t".repeat(64 << 10);
    for n in 0..100 {
        c.execute(
            "INSERT INTO threads VALUES (?1, ?2, ?2, ?2, ?2, '', ?3)",
            rusqlite::params![format!("{n:03}{}", "i".repeat(300)), big, n],
        )
        .unwrap();
    }
    drop(c);
    let v = list(&root, 0, 100).to_json();
    let sessions = v["sessions"].as_array().unwrap();
    assert!(
        !sessions.is_empty() && sessions.len() < 100,
        "{}",
        sessions.len()
    );
    assert_eq!(v["has_more"], true);
    assert!(v.to_string().len() < MAX_LIST + (64 << 10));
    for s in sessions {
        assert!(s["id"].as_str().unwrap().len() <= MAX_ID + 3);
        for k in ["title", "cwd", "provider"] {
            let t = s[k].as_str().unwrap();
            assert!(t.len() <= MAX_TEXT + 3 && t.ends_with('…'), "{k}");
        }
    }
    // A detail with a 2 MiB timestamp, role and call ID, and long metadata.
    let id = "019f1234-abcd-7000-8000-0000000000cc";
    let huge = "9".repeat(2 << 20);
    let line = json!({"timestamp": huge, "type": "response_item", "payload": {"type": "function_call_output", "call_id": huge, "output": "x"}});
    let meta = json!({"type": "session_meta", "payload": {"id": id, "title": big, "cwd": big, "model_provider": big}});
    let msg = json!({"type": "response_item", "payload": {"type": "message", "role": huge, "content": "y"}});
    write(
        &root.join(format!("sessions/rollout-{id}.jsonl")),
        &format!("{meta}\n{line}\n{msg}\n"),
    );
    let d = detail(&root, id, 0, 50, &never).unwrap();
    assert!(d.to_string().len() <= MAX_DETAIL);
    for k in ["title", "cwd", "provider"] {
        assert!(d[k].as_str().unwrap().len() <= MAX_TEXT + 3, "{k}");
    }
    let m = &d["messages"];
    assert!(m[0]["timestamp"].as_str().unwrap().len() <= MAX_SHORT + 3);
    assert!(m[0]["call_id"].as_str().unwrap().len() <= MAX_SHORT + 3);
    assert!(m[1]["role"].as_str().unwrap().len() <= MAX_SHORT + 3);
}

#[test]
fn a_session_index_over_16_mib_is_a_warning_and_not_read() {
    let root = Root::new("big-index");
    let id = "019f1234-abcd-7000-8000-0000000000dd";
    write(&root.join(format!("sessions/rollout-{id}.jsonl")), "");
    let mut index = format!("{}\n", json!({"id": id, "thread_name": "Named"}));
    index.push_str(&" ".repeat(16 << 20));
    write(&root.join("session_index.jsonl"), &index);
    let page = list(&root, 0, 50);
    assert_eq!(page.sessions[0].title, "");
    assert!(
        page.warnings
            .iter()
            .any(|w| w.contains("larger than 16 MiB")),
        "{:?}",
        page.warnings
    );
}

#[test]
fn linked_databases_and_index_are_skipped() {
    if !links_allowed() {
        return;
    }
    let root = Root::new("linked-db");
    let outside = Root::new("linked-db-outside");
    let db = outside.join("real.db");
    sql(&db, "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT); INSERT INTO threads VALUES ('x', 'Linked');");
    fs::create_dir_all(root.join("sqlite")).unwrap();
    make_link(&db, root.join("sqlite/linked.db")).unwrap();
    make_link(&db, root.join("state_5.sqlite")).unwrap();
    let id = "019f1234-abcd-7000-8000-0000000000ee";
    write(&root.join(format!("sessions/rollout-{id}.jsonl")), "");
    let index = outside.join("index.jsonl");
    write(
        &index,
        &format!("{}\n", json!({"id": id, "thread_name": "Through a link"})),
    );
    make_link(&index, root.join("session_index.jsonl")).unwrap();
    let page = list(&root, 0, 50);
    assert!(page.databases.is_empty(), "{:?}", page.databases);
    assert_eq!(page.sessions.len(), 1);
    assert_eq!(page.sessions[0].title, "");
}

/// A database with one thread, `x`, at path (its folder made).
fn one_thread_db(path: &Path) {
    fs::create_dir_all(path.parent().unwrap()).unwrap();
    sql(
        path,
        "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT); INSERT INTO threads VALUES ('x', 'Seen');",
    );
}

#[test]
fn a_linked_sqlite_folder_is_skipped_with_a_warning_and_a_real_one_is_read() {
    if !links_allowed() {
        return;
    }
    let outside = Root::new("linked-sqlite-outside");
    one_thread_db(&outside.join("other.db"));
    let root = Root::new("linked-sqlite");
    make_link(&*outside, root.join("sqlite")).unwrap();
    let page = list(&root, 0, 50);
    assert!(page.databases.is_empty(), "{:?}", page.databases);
    assert!(page.sessions.is_empty());
    assert_eq!(page.warnings.len(), 1, "{:?}", page.warnings);
    assert!(page.warnings[0].contains("sqlite"), "{:?}", page.warnings);
    // The same database in a real folder is read.
    let real = Root::new("real-sqlite");
    one_thread_db(&real.join("sqlite/other.db"));
    let page = list(&real, 0, 50);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    assert_eq!(page.sessions.len(), 1);
    assert_eq!(page.sessions[0].title, "Seen");
}

#[test]
fn linked_session_folders_are_skipped_with_a_warning_each_and_real_ones_are_read() {
    if !links_allowed() {
        return;
    }
    let outside = Root::new("linked-sessions-outside");
    let live = "019f1234-abcd-7000-8000-0000000000c1";
    let old = "019f1234-abcd-7000-8000-0000000000c2";
    write(&outside.join(format!("a/rollout-{live}.jsonl")), "");
    write(&outside.join(format!("b/rollout-{old}.jsonl")), "");
    let root = Root::new("linked-sessions");
    make_link(outside.join("a"), root.join("sessions")).unwrap();
    make_link(outside.join("b"), root.join("archived_sessions")).unwrap();
    let page = list(&root, 0, 50);
    assert!(page.sessions.is_empty(), "{:?}", page.sessions);
    assert_eq!(page.warnings.len(), 2, "{:?}", page.warnings);
    assert!(page
        .warnings
        .iter()
        .any(|w| w.contains("archived_sessions")));
    assert!(page
        .warnings
        .iter()
        .any(|w| w.contains("sessions") && !w.contains("archived_sessions")));
    assert!(find_rollout(&root, live).is_none());
    // The same files in real folders are listed, and found.
    let real = Root::new("real-sessions");
    write(&real.join(format!("sessions/rollout-{live}.jsonl")), "");
    write(
        &real.join(format!("archived_sessions/rollout-{old}.jsonl")),
        "",
    );
    let page = list(&real, 0, 50);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    assert_eq!(page.sessions.len(), 2);
    assert!(find_rollout(&real, live).is_some());
}

#[test]
fn a_database_with_a_linked_wal_or_shm_is_skipped_with_a_warning() {
    if !links_allowed() {
        return;
    }
    let _lock = COPY_ROOT.lock().unwrap_or_else(|e| e.into_inner());
    let outside = Root::new("linked-wal-outside");
    // A real -wal beside the database is read (the control).
    let real = Root::new("real-wal");
    wal_with_uncheckpointed_rows(&real.join("sqlite/live.db"));
    let page = list(&real, 0, 50);
    assert!(page.warnings.is_empty(), "{:?}", page.warnings);
    assert_eq!(page.sessions.len(), 2);
    // The same database whose -wal is a link to a copy elsewhere.
    let root = Root::new("linked-wal");
    let db = root.join("sqlite/live.db");
    wal_with_uncheckpointed_rows(&db);
    let moved = outside.join("live.db-wal");
    fs::rename(sidecar(&db, "-wal"), &moved).unwrap();
    make_link(&moved, sidecar(&db, "-wal")).unwrap();
    let page = list(&root, 0, 50);
    assert!(page.sessions.is_empty(), "{:?}", page.sessions);
    assert_eq!(page.warnings.len(), 1, "{:?}", page.warnings);
    assert!(page.warnings[0].contains("live.db") && page.warnings[0].contains("-wal"));
    // A linked -shm, with a real -wal.
    let root = Root::new("linked-shm");
    let db = root.join("sqlite/live.db");
    wal_with_uncheckpointed_rows(&db);
    let shm = outside.join("live.db-shm");
    fs::write(&shm, b"").unwrap();
    make_link(&shm, sidecar(&db, "-shm")).unwrap();
    let page = list(&root, 0, 50);
    assert!(page.sessions.is_empty(), "{:?}", page.sessions);
    assert_eq!(page.warnings.len(), 1, "{:?}", page.warnings);
    assert!(page.warnings[0].contains("-shm"), "{:?}", page.warnings);
    // detail reads nothing through them either.
    assert!(matches!(
        detail(&root, "b", 0, 50, &never),
        Err(DetailError::NotFound(_))
    ));
}

#[test]
fn a_thread_stored_as_local_uuid_is_found_by_either_form_of_its_id() {
    let root = Root::new("detail-local");
    let id = "019f1234-abcd-7000-8000-0000000000d1";
    // The rollout's name does not carry the ID: only the row leads to its metadata.
    sql(
        &root.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, cwd TEXT);
             INSERT INTO threads VALUES ('local:{id}', 'Local row', '/work');"
        ),
    );
    for asked in [format!("local:{id}"), id.to_string()] {
        let d = detail(&root, &asked, 0, 50, &never).unwrap();
        assert_eq!(d["title"], "Local row", "{asked}");
        assert_eq!(d["cwd"], "/work", "{asked}");
        assert!(d["database"].as_str().unwrap().ends_with("state_5.sqlite"));
    }
    // A row stored without the prefix is still found by both.
    let plain = Root::new("detail-plain");
    sql(
        &plain.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, title TEXT, cwd TEXT);
             INSERT INTO threads VALUES ('{id}', 'Plain row', '/work');"
        ),
    );
    for asked in [format!("local:{id}"), id.to_string()] {
        assert_eq!(
            detail(&plain, &asked, 0, 50, &never).unwrap()["title"],
            "Plain row"
        );
    }
}

/// A symbolic link at link to target: on Windows a folder link when target is a folder.
fn make_link(target: impl AsRef<Path>, link: impl AsRef<Path>) -> std::io::Result<()> {
    #[cfg(unix)]
    return std::os::unix::fs::symlink(target, link);
    #[cfg(windows)]
    return if target.as_ref().is_dir() {
        std::os::windows::fs::symlink_dir(target, link)
    } else {
        std::os::windows::fs::symlink_file(target, link)
    };
}

/// Windows: an entry in path's access list denying everyone (this user too) its bytes (`deny`), or
/// that entry taken out again.
#[cfg(windows)]
fn deny_reading(path: &Path, deny: bool) {
    let args: &[&str] = if deny {
        &["/deny", "*S-1-1-0:(RD)"]
    } else {
        &["/remove:d", "*S-1-1-0"]
    };
    let out = std::process::Command::new("icacls")
        .arg(path)
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
}

/// Whether this account may make symbolic links (on Windows that takes a privilege or Developer
/// Mode). When it may not, says so on stderr: a test that needs links is then skipped, and the CI
/// summary counts the skip.
fn links_allowed() -> bool {
    #[cfg(unix)]
    return true;
    #[cfg(windows)]
    {
        static ALLOWED: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
        let allowed = *ALLOWED.get_or_init(|| {
            let probe = std::env::temp_dir().join(format!("links-allowed-{}", std::process::id()));
            let _ = std::fs::remove_file(&probe);
            let made = std::os::windows::fs::symlink_file("target", &probe).is_ok();
            let _ = std::fs::remove_file(&probe);
            made
        });
        if !allowed {
            eprintln!("skipped: this account cannot make a symbolic link");
        }
        allowed
    }
}

const EXPORT_ID: &str = "019f1234-abcd-7000-8000-0000000000e1";

/// A rollout for EXPORT_ID in the usual place, holding bytes; its path.
fn exported_rollout(root: &Path, bytes: &[u8]) -> PathBuf {
    let path = root.join(format!(
        "sessions/2026/08/04/rollout-2026-08-04T10-00-00-{EXPORT_ID}.jsonl"
    ));
    fs::create_dir_all(path.parent().unwrap()).unwrap();
    fs::write(&path, bytes).unwrap();
    path
}

#[test]
fn export_name_is_the_old_apps_with_the_utc_date() {
    assert_eq!(export_name("abc", 0), "session-abc-1970-01-01.jsonl");
    assert_eq!(export_name("abc", 86_399), "session-abc-1970-01-01.jsonl");
    assert_eq!(export_name("abc", 86_400), "session-abc-1970-01-02.jsonl");
    // 2000-02-29 and 2024-02-29 (leap days), 2023-11-14 22:13:20, the end of 1999 and 2100-03-01.
    assert_eq!(export_name("x", 951_782_400), "session-x-2000-02-29.jsonl");
    assert_eq!(
        export_name("x", 1_709_164_800),
        "session-x-2024-02-29.jsonl"
    );
    assert_eq!(
        export_name("x", 1_700_000_000),
        "session-x-2023-11-14.jsonl"
    );
    assert_eq!(export_name("x", 946_684_799), "session-x-1999-12-31.jsonl");
    assert_eq!(
        export_name("x", 4_107_542_400),
        "session-x-2100-03-01.jsonl"
    );
}

#[test]
fn export_gives_the_rollouts_bytes_exactly_as_they_are_on_disk() {
    let root = Root::new("export-bytes");
    // No final newline, a CRLF, a NUL, multi-byte text and a line that is not JSON: nothing is
    // parsed or normalised.
    let bytes = "{\"a\":\"é€😀\"}\r\n\0not json\n{\"b\":1}"
        .as_bytes()
        .to_vec();
    exported_rollout(&root, &bytes);
    assert_eq!(
        export_bytes(&root, EXPORT_ID),
        Ok((EXPORT_ID.to_string(), bytes.clone()))
    );
    // `local:` is how a database may name it; the ID that comes back is without it.
    assert_eq!(
        export_bytes(&root, &format!("local:{EXPORT_ID}"))
            .unwrap()
            .0,
        EXPORT_ID
    );
    // An empty rollout is an empty export.
    exported_rollout(&root, b"");
    assert_eq!(
        export_bytes(&root, EXPORT_ID),
        Ok((EXPORT_ID.into(), vec![]))
    );
}

#[test]
fn export_takes_a_rollout_of_exactly_the_limit_and_refuses_one_byte_more_or_not_utf8() {
    let root = Root::new("export-limit");
    let path = exported_rollout(&root, &vec![b'x'; MAX_EXPORT as usize]);
    assert_eq!(
        export_bytes(&root, EXPORT_ID).unwrap().1.len() as u64,
        MAX_EXPORT
    );
    fs::write(&path, vec![b'x'; MAX_EXPORT as usize + 1]).unwrap();
    let Err(DetailError::Unavailable(m)) = export_bytes(&root, EXPORT_ID) else {
        panic!("a rollout over the limit is unavailable");
    };
    assert!(m.contains("larger than 10 MiB"), "{m}");
    assert!(!m.contains(&*root.to_string_lossy()), "{m}");
    // Not UTF-8: the old app had no raw JSONL for it either.
    fs::write(&path, b"{\"a\":1}\n\xff\xfe\n").unwrap();
    let Err(DetailError::Unavailable(m)) = export_bytes(&root, EXPORT_ID) else {
        panic!("a rollout that is not UTF-8 is unavailable");
    };
    assert!(m.contains("not UTF-8"), "{m}");
    assert!(!m.contains(&*root.to_string_lossy()), "{m}");
}

#[test]
fn export_finds_the_rollout_as_detail_does_and_refuses_what_detail_would_not_read() {
    let root = Root::new("export-resolve");
    let outside = Root::new("export-resolve-outside");
    let canary = "canary-outside-file-text";
    // A database names a rollout whose file name holds no session ID.
    let named = root.join("sessions/chosen.jsonl");
    write(&named, "{\"chosen\":true}\n");
    sql(
        &root.join("state_5.sqlite"),
        &format!(
            "CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, title TEXT);
             INSERT INTO threads VALUES ('by-database', '{}', 'Named');
             INSERT INTO threads VALUES ('local:{EXPORT_ID}', '{}', 'Outside');
             INSERT INTO threads VALUES ('linked', '{}', 'Linked');",
            named.display(),
            outside.join("outside.jsonl").display(),
            root.join("sessions/link.jsonl").display(),
        ),
    );
    let d = detail(&root, "by-database", 0, 50, &never).unwrap();
    assert_eq!(
        d["rollout"],
        json!(fs::canonicalize(&named).unwrap().to_string_lossy())
    );
    assert_eq!(
        export_bytes(&root, "by-database"),
        Ok(("by-database".to_string(), b"{\"chosen\":true}\n".to_vec()))
    );

    // The database's path leaves the Codex home: not read, as detail does not read it; the
    // rollout that is in the home by its name is, as for detail.
    write(&outside.join("outside.jsonl"), canary);
    assert_eq!(
        detail(&root, EXPORT_ID, 0, 50, &never).unwrap()["rollout"],
        Value::Null
    );
    assert!(matches!(
        export_bytes(&root, EXPORT_ID),
        Err(DetailError::NotFound(_))
    ));
    exported_rollout(&root, b"{\"inside\":true}\n");
    assert_eq!(
        export_bytes(&root, EXPORT_ID).unwrap().1,
        b"{\"inside\":true}\n"
    );

    // A link in the home that points outside is not read, by a database's path or by name.
    if !links_allowed() {
        return;
    }
    make_link(
        outside.join("outside.jsonl"),
        root.join("sessions/link.jsonl"),
    )
    .unwrap();
    assert_eq!(
        detail(&root, "linked", 0, 50, &never).unwrap()["rollout"],
        Value::Null
    );
    let Err(DetailError::NotFound(m)) = export_bytes(&root, "linked") else {
        panic!("a link out of the Codex home is not exported");
    };
    assert!(
        !m.contains(canary) && !m.contains(&*outside.to_string_lossy()),
        "{m}"
    );
    let other = "019f1234-abcd-7000-8000-0000000000e2";
    make_link(
        outside.join("outside.jsonl"),
        root.join(format!("sessions/rollout-{other}.jsonl")),
    )
    .unwrap();
    assert!(matches!(
        export_bytes(&root, other),
        Err(DetailError::NotFound(_))
    ));
}

#[test]
fn export_skips_session_folders_that_are_links_and_refuses_bad_ids() {
    let root = Root::new("export-folders");
    let outside = Root::new("export-folders-outside");
    let file = outside.join(format!("a/rollout-{EXPORT_ID}.jsonl"));
    write(&file, "outside\n");
    if links_allowed() {
        make_link(outside.join("a"), root.join("sessions")).unwrap();
        assert!(matches!(
            export_bytes(&root, EXPORT_ID),
            Err(DetailError::NotFound(_))
        ));
    }
    for bad in ["../etc/passwd", "a/b", "", "a.b", &"a".repeat(129)] {
        assert!(
            matches!(export_bytes(&root, bad), Err(DetailError::Invalid(_))),
            "{bad}"
        );
    }
}
