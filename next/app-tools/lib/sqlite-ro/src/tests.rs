//! The opener against real SQLite files in temporary folders: the database left byte for byte as it
//! was with no file made beside it, WAL databases with and without a `-shm`, links and FIFOs, and
//! the private copies (made 0700, removed when closed, pruned only when their process is gone).

use super::*;
use std::collections::BTreeMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc;

/// A new empty folder, removed when dropped.
struct Dir(PathBuf);

impl Dir {
    fn new(name: &str) -> Dir {
        static N: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "sqlite-ro-{name}-{}-{}",
            std::process::id(),
            N.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = fs::remove_dir_all(&p);
        fs::create_dir_all(&p).unwrap();
        Dir(p)
    }
}

impl Drop for Dir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

impl std::ops::Deref for Dir {
    type Target = Path;
    fn deref(&self) -> &Path {
        &self.0
    }
}

/// Every file in dir with its bytes.
fn files(dir: &Path) -> BTreeMap<String, Vec<u8>> {
    fs::read_dir(dir)
        .unwrap()
        .flatten()
        .map(|e| {
            (
                e.file_name().to_string_lossy().into_owned(),
                fs::read(e.path()).unwrap_or_default(),
            )
        })
        .collect()
}

/// A WAL-mode database at path with row 'a' in the database file; no `-wal` or `-shm` is left.
fn wal_db(path: &Path) {
    let c = Connection::open(path).unwrap();
    c.execute_batch(
        "PRAGMA journal_mode = WAL;
         CREATE TABLE t (id TEXT PRIMARY KEY);
         INSERT INTO t VALUES ('a');",
    )
    .unwrap();
}

/// A WAL database at dest with row 'a' in it and row 'b' only in its `-wal` (committed, not copied
/// into the database), and no `-shm`: as a writer that stopped without checkpointing leaves one.
fn wal_with_rows_in_the_wal(dest: &Path) {
    let scratch = Dir::new("wal-source");
    let db = scratch.join("live.db");
    let c = Connection::open(&db).unwrap();
    c.execute_batch(
        "PRAGMA journal_mode = WAL;
         CREATE TABLE t (id TEXT PRIMARY KEY);
         INSERT INTO t VALUES ('a');
         PRAGMA wal_checkpoint(TRUNCATE);
         PRAGMA wal_autocheckpoint = 0;
         INSERT INTO t VALUES ('b');",
    )
    .unwrap();
    fs::copy(&db, dest).unwrap();
    fs::copy(sidecar(&db, "-wal"), sidecar(dest, "-wal")).unwrap();
    drop(c);
}

fn ids(db: &Db) -> Vec<String> {
    let mut st = db.prepare("SELECT id FROM t ORDER BY id").unwrap();
    st.query_map([], |r| r.get::<_, String>(0))
        .unwrap()
        .map(Result::unwrap)
        .collect()
}

/// A copy root under dir that counts as the tool's own.
fn own_root(dir: &Path) -> CopyRoot {
    CopyRoot {
        dir: dir.join("tmp"),
        own: true,
    }
}

/// The names of the folders in root.
fn names(root: &Path) -> Vec<String> {
    let mut n: Vec<String> = fs::read_dir(root)
        .map(|d| {
            d.flatten()
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect()
        })
        .unwrap_or_default();
    n.sort();
    n
}

/// The id of a process that has stopped, on Linux, where a copy's process is told by its pid; on
/// macOS and Windows the lock beside a copy tells, so any number serves in a name.
fn dead_pid() -> u32 {
    #[cfg(not(any(windows, target_os = "macos")))]
    {
        let mut child = std::process::Command::new("true").spawn().unwrap();
        let pid = child.id();
        child.wait().unwrap();
        pid
    }
    #[cfg(any(windows, target_os = "macos"))]
    4_294_967_294
}

/// Makes the copy folder name in root as one left over by a stopped process: on Linux its name says
/// so; on macOS and Windows it has no lock beside it.
fn stale_copy(root: &Path, name: &str) {
    fs::create_dir_all(root.join(name)).unwrap();
}

/// What keeps a copy alive while it lives: on macOS and Windows its held lock.
#[cfg(any(windows, target_os = "macos"))]
type Held = pluxy_files::Lock;
#[cfg(not(any(windows, target_os = "macos")))]
struct Held;

/// A copy folder in root of a process still running, and what keeps it so while it lives: on Linux
/// pid 1's; on macOS and Windows one whose lock this test holds.
fn running_copy(root: &Path) -> (String, Held) {
    let name = "wal-1-0".to_string();
    fs::create_dir_all(root.join(&name)).unwrap();
    #[cfg(any(windows, target_os = "macos"))]
    let held = pluxy_files::create_locked(&lock_path(root, &name)).unwrap();
    #[cfg(not(any(windows, target_os = "macos")))]
    let held = Held;
    (name, held)
}

/// A FIFO at path (Linux and macOS: Windows has none in a folder).
#[cfg(unix)]
fn make_fifo(path: &Path) {
    let made = std::process::Command::new("mkfifo")
        .arg(path)
        .status()
        .unwrap();
    assert!(made.success(), "mkfifo");
}

/// Removes the link at link (on Windows a folder link is removed as a folder).
fn remove_link(link: &Path) -> std::io::Result<()> {
    #[cfg(windows)]
    {
        use std::os::windows::fs::FileTypeExt;
        if fs::symlink_metadata(link)?.file_type().is_symlink_dir() {
            return fs::remove_dir(link);
        }
    }
    fs::remove_file(link)
}

#[test]
fn a_database_without_a_wal_is_read_and_left_as_it_was() {
    let d = Dir::new("plain");
    let db = d.join("a.db");
    wal_db(&db);
    assert!(is_wal(&db));
    assert_eq!(names(&d), ["a.db"]);
    let before = files(&d);
    let o = open_read_only(&db).unwrap();
    assert_eq!(ids(&o), ["a"]);
    drop(o);
    assert_eq!(files(&d), before, "the database changed or a file was made");
    // A database that is not in WAL mode (a rollback journal) is read in place too.
    let plain = d.join("plain.db");
    Connection::open(&plain)
        .unwrap()
        .execute_batch("CREATE TABLE t (id TEXT); INSERT INTO t VALUES ('p');")
        .unwrap();
    assert!(!is_wal(&plain));
    let before = files(&d);
    let o = open_read_only(&plain).unwrap();
    assert_eq!(
        o.query_row("SELECT id FROM t", [], |r| r.get::<_, String>(0))
            .unwrap(),
        "p"
    );
    drop(o);
    assert_eq!(files(&d), before);
}

#[test]
fn an_empty_wal_with_no_shm_is_opened_immutable_and_nothing_is_made() {
    let d = Dir::new("empty-wal");
    let db = d.join("a.db");
    wal_db(&db);
    fs::write(sidecar(&db, "-wal"), b"").unwrap();
    let tool = Dir::new("empty-wal-tool");
    let before = files(&d);
    let o = open(&db, &own_root(&tool)).unwrap();
    assert_eq!(ids(&o), ["a"]);
    assert!(
        !tool.join("tmp").exists(),
        "an empty -wal was copied, not opened in place"
    );
    drop(o);
    assert_eq!(files(&d), before, "a -shm appeared or a file changed");
    assert!(!sidecar(&db, "-shm").exists());
}

#[test]
fn a_wal_with_changes_and_no_shm_is_read_from_a_private_copy_removed_when_closed() {
    let d = Dir::new("wal-copy");
    let tool = Dir::new("wal-copy-tool");
    let db = d.join("live.db");
    wal_with_rows_in_the_wal(&db);
    assert!(fs::metadata(sidecar(&db, "-wal")).unwrap().len() > 0);
    assert!(!sidecar(&db, "-shm").exists());
    let before = files(&d);
    let root = own_root(&tool);
    let o = open(&db, &root).unwrap();
    // Row 'b' is only in the -wal: the copy has it.
    assert_eq!(ids(&o), ["a", "b"]);
    // While it is open the copy is there, in a folder only this user can use; on macOS and
    // Windows with its lock beside it, which no other can take.
    let copies: Vec<String> = names(&root.dir)
        .into_iter()
        .filter(|n| !n.ends_with(".lock"))
        .collect();
    assert!(
        copies.len() == 1 && copies[0].starts_with(&format!("wal-{}-", std::process::id())),
        "{copies:?}"
    );
    #[cfg(any(windows, target_os = "macos"))]
    {
        let lock = lock_path(&root.dir, &copies[0]);
        assert_eq!(names(&root.dir).len(), 2, "{:?}", names(&root.dir));
        assert!(pluxy_files::take_lock(&lock).unwrap().is_none());
        prune(&root.dir);
        assert!(
            root.dir.join(&copies[0]).is_dir(),
            "an open copy was pruned"
        );
    }
    for dir in [&root.dir, &root.dir.join(&copies[0])] {
        assert!(pluxy_files::is_private(dir).unwrap(), "{dir:?}");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(dir).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o700, "{dir:?}");
        }
    }
    assert!(o.execute_batch("INSERT INTO t VALUES ('z')").is_err());
    drop(o);
    assert!(names(&root.dir).is_empty(), "the private copy stayed");
    assert_eq!(files(&d), before, "the database or its -wal changed");
    assert!(!sidecar(&db, "-shm").exists(), "a -shm appeared");
}

#[test]
fn the_copy_goes_to_the_tools_data_folder_when_there_is_one_and_else_to_the_temporary_folder() {
    // The only test that touches the variable; the others call `open` with their own root.
    let tool = Dir::new("copy-root");
    std::env::set_var("PLUXY_TOOL_DATA_DIR", &*tool);
    let set = copy_root();
    std::env::set_var("PLUXY_TOOL_DATA_DIR", "");
    let empty = copy_root();
    std::env::remove_var("PLUXY_TOOL_DATA_DIR");
    let unset = copy_root();
    assert_eq!((set.dir, set.own), (tool.join("tmp"), true));
    assert_eq!((empty.dir, empty.own), (std::env::temp_dir(), false));
    assert_eq!((unset.dir, unset.own), (std::env::temp_dir(), false));
}

#[test]
fn a_connection_cannot_write_in_any_mode() {
    let d = Dir::new("readonly");
    let tool = Dir::new("readonly-tool");
    let in_place = d.join("a.db");
    wal_db(&in_place);
    let immutable = d.join("i.db");
    wal_db(&immutable);
    fs::write(sidecar(&immutable, "-wal"), b"").unwrap();
    let copied = d.join("c.db");
    wal_with_rows_in_the_wal(&copied);
    let before = files(&d);
    for db in [&in_place, &immutable, &copied] {
        let o = open(db, &own_root(&tool)).unwrap();
        let e = o.execute_batch("INSERT INTO t VALUES ('z')").unwrap_err();
        assert!(e.to_string().contains("readonly"), "{db:?}: {e}");
    }
    assert_eq!(files(&d), before);
}

#[test]
fn a_database_a_writer_has_open_is_read_in_place_and_nothing_is_made() {
    let d = Dir::new("live-writer");
    let tool = Dir::new("live-writer-tool");
    let db = d.join("a.db");
    let writer = Connection::open(&db).unwrap();
    writer
        .execute_batch(
            "PRAGMA journal_mode = WAL;
             CREATE TABLE t (id TEXT PRIMARY KEY);
             INSERT INTO t VALUES ('a');",
        )
        .unwrap();
    // The writer's -wal and -shm are there: the reader shares them and copies nothing.
    assert!(sidecar(&db, "-shm").exists());
    let before = names(&d);
    let o = open(&db, &own_root(&tool)).unwrap();
    writer.execute_batch("INSERT INTO t VALUES ('b')").unwrap();
    assert_eq!(ids(&o), ["a", "b"]);
    assert!(o.execute_batch("INSERT INTO t VALUES ('z')").is_err());
    assert_eq!(names(&d), before);
    assert!(!tool.join("tmp").exists(), "a copy was made");
}

#[test]
fn a_link_fifo_folder_or_missing_file_is_not_a_database() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("not-regular");
    let db = d.join("a.db");
    wal_db(&db);
    // The positive control: the file itself opens.
    assert!(open_read_only(&db).is_ok());
    make_link(&db, d.join("link.db")).unwrap();
    #[cfg(unix)]
    make_fifo(&d.join("fifo.db"));
    fs::create_dir(d.join("folder.db")).unwrap();
    let names: &[&str] = if cfg!(unix) {
        &["link.db", "fifo.db", "folder.db", "missing.db"]
    } else {
        &["link.db", "folder.db", "missing.db"]
    };
    for &name in names {
        // Opening a FIFO to look at it would block for ever: run it where a hang is a failure.
        let path = d.join(name);
        let (tx, rx) = mpsc::channel();
        std::thread::spawn(move || {
            let _ = tx.send(open_read_only(&path).err().map(|e| e.to_string()));
        });
        let e = rx
            .recv_timeout(std::time::Duration::from_secs(10))
            .unwrap_or_else(|_| panic!("{name}: opening it hung"));
        assert_eq!(
            e.as_deref(),
            Some("the database is not a regular file"),
            "{name}"
        );
    }
}

/// Makes something at path that is not a regular file: a link to target, a folder or a FIFO.
fn make_irregular(kind: &str, path: &Path, target: &Path) {
    match kind {
        "link" => make_link(target, path).unwrap(),
        "folder" => fs::create_dir(path).unwrap(),
        #[cfg(unix)]
        "fifo" => make_fifo(path),
        _ => unreachable!("{kind}"),
    }
}

#[test]
fn a_wal_shm_or_journal_that_is_not_a_regular_file_is_not_read() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("sidecars");
    let db = d.join("a.db");
    wal_db(&db);
    let elsewhere = d.join("elsewhere");
    fs::write(&elsewhere, b"").unwrap();
    for name in ["-wal", "-shm", "-journal"] {
        let path = sidecar(&db, name);
        let kinds: &[&str] = if cfg!(unix) {
            &["link", "folder", "fifo"]
        } else {
            &["link", "folder"]
        };
        for &kind in kinds {
            make_irregular(kind, &path, &elsewhere);
            let e = open_read_only(&db)
                .err()
                .unwrap_or_else(|| panic!("{name} {kind}"));
            assert_eq!(
                e.to_string(),
                format!("the database's {name} is not a regular file"),
                "{name} {kind}"
            );
            assert!(matches!(e, Error::SidecarNotRegular(n) if n == name));
            if fs::symlink_metadata(&path).unwrap().is_dir() {
                fs::remove_dir(&path).unwrap();
            } else {
                fs::remove_file(&path).unwrap();
            }
        }
        // The positive control: without it, the database is read.
        assert!(open_read_only(&db).is_ok(), "{name}");
    }
}

#[test]
fn only_copies_left_by_processes_no_longer_running_are_pruned() {
    let d = Dir::new("prune");
    let dead = dead_pid();
    let own = format!("wal-{}-9", std::process::id());
    let stale = format!("wal-{dead}-0");
    stale_copy(&d, &stale);
    // One with this process's own pid, as a process that had it before left it: on Linux that
    // pid is running, so it stays; elsewhere its lock decides, and no process holds it.
    stale_copy(&d, &own);
    // A running process's, and names that are not a copy's.
    let (running, _held) = running_copy(&d);
    let left = [
        own.clone(),
        stale,
        running.clone(),
        "wal-x-0".to_string(),
        "wal-".to_string(),
        "other".to_string(),
    ];
    for n in &left {
        fs::create_dir_all(d.join(n)).unwrap();
    }
    prune(&d);
    let mut kept: Vec<String> = left
        .iter()
        .filter(|n| d.join(n).exists())
        .cloned()
        .collect();
    kept.sort();
    let mut want = vec![running, "wal-x-0".into(), "wal-".into(), "other".into()];
    if cfg!(target_os = "linux") {
        want.push(own);
    }
    want.sort();
    assert_eq!(kept, want);
}

#[test]
fn a_copy_is_made_past_folders_left_with_this_processs_pid() {
    let d = Dir::new("pid-reuse");
    let tool = Dir::new("pid-reuse-tool");
    let db = d.join("live.db");
    wal_with_rows_in_the_wal(&db);
    let root = own_root(&tool);
    fs::create_dir_all(&root.dir).unwrap();
    for n in 0..256 {
        stale_copy(&root.dir, &format!("wal-{}-{n}", std::process::id()));
    }
    let o = open(&db, &root).unwrap();
    assert_eq!(ids(&o), ["a", "b"]);
    drop(o);
    // Elsewhere than Linux no process holds their locks, so they are pruned.
    if !cfg!(target_os = "linux") {
        assert!(names(&root.dir).is_empty(), "{:?}", names(&root.dir));
    }
}

/// Windows: a path SQLite would read against another folder is refused; a path longer than
/// Windows' old limit, or one already written in its long form (`\\?\`), opens.
#[cfg(windows)]
#[test]
fn on_windows_a_relative_path_is_refused_and_a_long_or_verbatim_one_opens() {
    let e = connect(Path::new("relative.db"), true).err().unwrap();
    assert!(
        matches!(&e, Error::Open(m) if m.contains("full path")),
        "{e:?}"
    );
    let e = connect(Path::new(r"\relative.db"), true).err().unwrap();
    assert!(
        matches!(&e, Error::Open(m) if m.contains("full path")),
        "{e:?}"
    );
    let d = Dir::new("long");
    let short = d.join("a.db");
    wal_db(&short);
    let mut long = d.to_path_buf();
    while long.as_os_str().len() < 600 {
        long.push("a-folder-with-a-name-forty-letters-long-");
    }
    fs::create_dir_all(&long).unwrap();
    let long = long.join("a.db");
    fs::copy(&short, &long).unwrap();
    assert_eq!(ids(&open_read_only(&long).unwrap()), ["a"]);
    let verbatim = PathBuf::from(format!(r"\\?\{}", short.display()));
    assert_eq!(ids(&open_read_only(&verbatim).unwrap()), ["a"]);
}

#[test]
fn a_copy_prunes_stale_copies_only_in_the_tools_own_folder() {
    let d = Dir::new("prune-on-copy");
    let tool = Dir::new("prune-on-copy-tool");
    let db = d.join("live.db");
    wal_with_rows_in_the_wal(&db);
    let dead = dead_pid();
    // In the tool's own folder, a stopped process's copy goes and a running one's stays.
    let own = own_root(&tool);
    stale_copy(&own.dir, &format!("wal-{dead}-0"));
    let (running, _held) = running_copy(&own.dir);
    let o = open(&db, &own).unwrap();
    assert_eq!(ids(&o), ["a", "b"]);
    assert!(own.dir.join(running).exists());
    assert!(!own.dir.join(format!("wal-{dead}-0")).exists());
    drop(o);
    // In the shared temporary folder, other programs' folders are not ours to remove.
    let shared = CopyRoot {
        dir: tool.join("shared"),
        own: false,
    };
    stale_copy(&shared.dir, &format!("wal-{dead}-0"));
    drop(open(&db, &shared).unwrap());
    assert!(shared.dir.join(format!("wal-{dead}-0")).exists());
}

#[test]
fn errors_name_no_path() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("CANARY-PATH");
    let db = d.join("a.db");
    wal_db(&db);
    let mut errors = vec![];
    errors.push(open_read_only(&d.join("missing.db")).err().unwrap());
    make_link(&db, d.join("link.db")).unwrap();
    errors.push(open_read_only(&d.join("link.db")).err().unwrap());
    let with_link = d.join("b.db");
    wal_db(&with_link);
    make_link(&db, sidecar(&with_link, "-wal")).unwrap();
    errors.push(open_read_only(&with_link).err().unwrap());
    // A copy that cannot be made: the copy folder's parent is a file.
    let live = d.join("live.db");
    wal_with_rows_in_the_wal(&live);
    let blocked = d.join("blocked");
    fs::write(&blocked, b"").unwrap();
    let root = CopyRoot {
        dir: blocked.join("tmp"),
        own: true,
    };
    errors.push(open(&live, &root).err().unwrap());
    // A database SQLite cannot open: no one may read it (skipped where the test runs as root).
    let locked = d.join("locked.db");
    wal_db(&locked);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&locked, fs::Permissions::from_mode(0o000)).unwrap();
    }
    #[cfg(windows)]
    deny_reading(&locked);
    if fs::File::open(&locked).is_err() {
        let e = open_read_only(&locked).err();
        errors.extend(e);
    }
    assert!(errors.len() >= 4);
    for e in &errors {
        let text = format!("{e} {e:?}");
        assert!(!text.contains("CANARY-PATH"), "{text}");
    }
    assert!(matches!(errors[3], Error::Copy(_)), "{:?}", errors[3]);
    // SQLite's own message names the file; what is kept is its error code.
    if let Some(e) = errors.get(4) {
        assert!(matches!(e, Error::Open(m) if m.contains("open")), "{e:?}");
    }
}

#[test]
fn a_path_with_uri_characters_opens() {
    // `?` cannot be in a Windows file name.
    let odd = if cfg!(windows) { " #%&=" } else { " ?#%&=" };
    let d = Dir::new(&format!("uri{odd}"));
    let db = d.join(format!("odd{odd}.db"));
    wal_db(&db);
    let o = open_read_only(&db).unwrap();
    assert_eq!(ids(&o), ["a"]);
}

/// Runs f where a hang is a failure: its result, or a panic after ten seconds.
#[cfg(unix)]
fn within_ten_seconds<T: Send + 'static>(what: &str, f: impl FnOnce() -> T + Send + 'static) -> T {
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let _ = tx.send(f());
    });
    rx.recv_timeout(std::time::Duration::from_secs(10))
        .unwrap_or_else(|_| panic!("{what}: it hung"))
}

/// A rollback-journal database (no WAL header) at path with row 'p'.
fn rollback_db(path: &Path) {
    Connection::open(path)
        .unwrap()
        .execute_batch("CREATE TABLE t (id TEXT); INSERT INTO t VALUES ('p');")
        .unwrap();
    assert!(!is_wal(path));
}

#[test]
fn a_wal_beside_a_database_is_never_opened_in_place_whatever_the_databases_header() {
    // SQLite opening such a pair read-only deletes the -wal (a database of no pages) or creates a
    // -shm (a header that is not WAL's): both are writes beside the database.
    let tool = Dir::new("header-tool");
    // A database of 0 bytes with a -wal, empty or not: SQLite deletes one that holds bytes.
    for (kind, wal_bytes) in [("empty", vec![]), ("with bytes", vec![7u8; 64])] {
        let d = Dir::new("header-empty");
        let db = d.join("a.db");
        fs::write(&db, b"").unwrap();
        fs::write(sidecar(&db, "-wal"), &wal_bytes).unwrap();
        let before = files(&d);
        let o = open(&db, &own_root(&tool)).unwrap();
        let n: i64 = o
            .query_row("SELECT count(*) FROM sqlite_master", [], |r| r.get(0))
            .unwrap();
        assert_eq!(n, 0);
        drop(o);
        assert_eq!(
            files(&d),
            before,
            "{kind}: the -wal was deleted or a file was made"
        );
    }
    // A rollback-journal database with an empty -wal.
    let d = Dir::new("header-rollback");
    let db = d.join("a.db");
    rollback_db(&db);
    fs::write(sidecar(&db, "-wal"), b"").unwrap();
    let before = files(&d);
    let o = open(&db, &own_root(&tool)).unwrap();
    assert_eq!(
        o.query_row("SELECT id FROM t", [], |r| r.get::<_, String>(0))
            .unwrap(),
        "p"
    );
    drop(o);
    assert_eq!(files(&d), before, "a -shm appeared or a file changed");
    // The same with a -wal that holds bytes: read from a private copy.
    let d = Dir::new("header-rollback-wal");
    let db = d.join("a.db");
    rollback_db(&db);
    fs::write(sidecar(&db, "-wal"), [7u8; 64]).unwrap();
    let before = files(&d);
    let root = own_root(&tool);
    let o = open(&db, &root).unwrap();
    assert_eq!(
        o.query_row("SELECT id FROM t", [], |r| r.get::<_, String>(0))
            .unwrap(),
        "p"
    );
    drop(o);
    assert_eq!(files(&d), before, "a -shm appeared or a file changed");
    assert!(names(&root.dir).is_empty(), "the private copy stayed");
    // The positive control: with no -wal, a rollback-journal database is read in place.
    fs::remove_file(sidecar(&db, "-wal")).unwrap();
    assert!(open(&db, &root).is_ok());
}

#[test]
fn a_tmp_folder_that_is_a_link_is_refused_and_nothing_is_made_or_pruned_through_it() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("tmp-link");
    let tool = Dir::new("tmp-link-tool");
    let elsewhere = Dir::new("tmp-link-elsewhere");
    let db = d.join("live.db");
    wal_with_rows_in_the_wal(&db);
    let dead = dead_pid();
    stale_copy(&elsewhere, &format!("wal-{dead}-0"));
    let before = names(&elsewhere);
    make_link(&*elsewhere, tool.join("tmp")).unwrap();
    let e = open(&db, &own_root(&tool))
        .err()
        .expect("a linked tmp was used");
    assert!(matches!(e, Error::Copy(_)), "{e:?}");
    assert_eq!(names(&elsewhere), before, "the link's target was written");
    // The positive control: with a real folder, the copy is made.
    remove_link(&tool.join("tmp")).unwrap();
    assert_eq!(ids(&open(&db, &own_root(&tool)).unwrap()), ["a", "b"]);
}

#[test]
fn a_tmp_folder_open_to_others_is_made_private() {
    let d = Dir::new("tmp-mode");
    let tool = Dir::new("tmp-mode-tool");
    let db = d.join("live.db");
    wal_with_rows_in_the_wal(&db);
    let tmp = tool.join("tmp");
    fs::create_dir(&tmp).unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&tmp, fs::Permissions::from_mode(0o755)).unwrap();
    }
    #[cfg(windows)]
    let_everyone_read(&tmp);
    assert!(!pluxy_files::is_private(&tmp).unwrap());
    let o = open(&db, &own_root(&tool)).unwrap();
    assert_eq!(ids(&o), ["a", "b"]);
    assert!(pluxy_files::is_private(&tmp).unwrap());
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = fs::metadata(&tmp).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o700);
    }
}

/// Windows: adds an entry to path's access list letting everyone read it.
#[cfg(windows)]
fn let_everyone_read(path: &Path) {
    icacls(path, &["/grant", "*S-1-1-0:(R)"]);
}

/// Windows: adds an entry to path's access list denying everyone (this user too) its bytes.
#[cfg(windows)]
fn deny_reading(path: &Path) {
    icacls(path, &["/deny", "*S-1-1-0:(RD)"]);
}

#[cfg(windows)]
fn icacls(path: &Path, args: &[&str]) {
    let out = std::process::Command::new("icacls")
        .arg(path)
        .args(args)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
}

#[test]
fn a_link_in_place_of_the_database_or_its_wal_is_not_read_or_copied() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("swapped-link");
    let tool = Dir::new("swapped-link-tool");
    let db = d.join("a.db");
    wal_db(&db);
    make_link(&db, d.join("link.db")).unwrap();
    assert!(is_wal(&db), "the positive control");
    assert!(!is_wal(&d.join("link.db")), "a link was followed");
    let live = d.join("live.db");
    wal_with_rows_in_the_wal(&live);
    let wal_link = d.join("wal-link");
    make_link(sidecar(&live, "-wal"), &wal_link).unwrap();
    let root = own_root(&tool);
    assert!(private_copy(&d.join("link.db"), &sidecar(&live, "-wal"), &root).is_err());
    assert!(private_copy(&live, &wal_link, &root).is_err());
    assert!(names(&root.dir).is_empty(), "a partial copy stayed");
    // The positive control: the files themselves are copied.
    assert!(private_copy(&live, &sidecar(&live, "-wal"), &root).is_ok());
}

#[cfg(unix)]
#[test]
fn a_fifo_or_link_in_place_of_the_database_is_caught_when_it_is_opened_and_never_waited_on() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("swapped");
    let tool = Dir::new("swapped-tool");
    let db = d.join("a.db");
    wal_db(&db);
    let fifo = d.join("fifo.db");
    make_fifo(&fifo);
    make_link(&db, d.join("link.db")).unwrap();
    // The header is read through a handle that does not follow links or wait for a writer.
    assert!(is_wal(&db), "the positive control");
    let (f, l) = (fifo.clone(), d.join("link.db"));
    assert!(!within_ten_seconds("reading a FIFO's header", move || {
        is_wal(&f)
    }));
    assert!(!is_wal(&l), "a link was followed");
    // So is the copy, which reads the database and its -wal from handles opened that way.
    let live = d.join("live.db");
    wal_with_rows_in_the_wal(&live);
    let root = own_root(&tool);
    let (f, w, r) = (fifo.clone(), sidecar(&live, "-wal"), own_root(&tool));
    let copied = within_ten_seconds("copying a FIFO", move || private_copy(&f, &w, &r).is_err());
    assert!(copied, "a FIFO was copied");
    let (f, r) = (fifo, own_root(&tool));
    let live2 = live.clone();
    let copied = within_ten_seconds("copying a FIFO as the -wal", move || {
        private_copy(&live2, &f, &r).is_err()
    });
    assert!(copied, "a FIFO was copied as the -wal");
    assert!(names(&root.dir).is_empty(), "a partial copy stayed");
}

#[test]
fn prune_removes_only_directories_named_wal_pid_counter_of_a_stopped_process() {
    if !links_allowed() {
        return;
    }
    let d = Dir::new("prune-names");
    let dead = dead_pid();
    let gone = format!("wal-{dead}-0");
    stale_copy(&d, &gone);
    // Names that are not a copy's, though a pid is in them; and non-directories.
    let kept = [
        format!("wal-{dead}"),
        format!("wal-+{dead}-0"),
        format!("wal-{dead}-x"),
        format!("wal-{dead}-"),
        format!("wal-{dead}-1-2"),
        "wal-0-0".to_string(),
        "wal--0".to_string(),
    ];
    for n in &kept {
        fs::create_dir(d.join(n)).unwrap();
    }
    fs::write(d.join(format!("wal-{dead}-2")), b"").unwrap();
    make_link(&*d, d.join(format!("wal-{dead}-3"))).unwrap();
    prune(&d);
    assert!(!d.join(&gone).exists(), "the stopped process's copy stayed");
    for n in &kept {
        assert!(d.join(n).exists(), "{n} was removed");
    }
    assert!(
        d.join(format!("wal-{dead}-2")).is_file(),
        "a file was removed"
    );
    assert!(
        fs::symlink_metadata(d.join(format!("wal-{dead}-3"))).is_ok(),
        "a link was removed"
    );
}

/// On macOS and Windows a copy's lock is held before its folder is made: a folder whose lock is
/// held is never pruned, so a copy being made is not taken for a leftover. Once the lock is free,
/// the folder and the lock go; so does a lock left with no folder.
#[cfg(any(windows, target_os = "macos"))]
#[test]
fn a_copy_whose_lock_is_held_is_never_pruned_and_one_whose_lock_is_free_is() {
    let d = Dir::new("prune-locks");
    let name = format!("wal-{}-0", dead_pid());
    let lock = lock_path(&d, &name);
    let held = pluxy_files::create_locked(&lock).unwrap();
    fs::create_dir(d.join(&name)).unwrap();
    prune(&d);
    assert!(
        d.join(&name).is_dir(),
        "a copy whose lock is held was pruned"
    );
    drop(held);
    prune(&d);
    assert!(names(&d).is_empty(), "{:?}", names(&d));
    // A lock left behind (as a crash leaves one on macOS), free, with no folder: removed; a file
    // named like a lock but not a copy's stays.
    fs::write(lock_path(&d, "wal-7-1"), b"").unwrap();
    fs::write(d.join("other.lock"), b"").unwrap();
    prune(&d);
    assert_eq!(names(&d), ["other.lock"]);
}

/// Windows: a junction (a folder link that needs no privilege) in place of the tool's `tmp` is
/// refused like a symbolic link, and nothing is made or pruned through it.
#[cfg(windows)]
#[test]
fn a_tmp_folder_that_is_a_junction_is_refused() {
    let d = Dir::new("tmp-junction");
    let tool = Dir::new("tmp-junction-tool");
    let elsewhere = Dir::new("tmp-junction-elsewhere");
    let db = d.join("live.db");
    wal_with_rows_in_the_wal(&db);
    stale_copy(&elsewhere, &format!("wal-{}-0", dead_pid()));
    let before = names(&elsewhere);
    let out = std::process::Command::new("cmd")
        .args(["/C", "mklink", "/J"])
        .arg(tool.join("tmp"))
        .arg(&*elsewhere)
        .output()
        .unwrap();
    assert!(out.status.success(), "{out:?}");
    let e = open(&db, &own_root(&tool))
        .err()
        .expect("a junction was used");
    assert!(matches!(e, Error::Copy(_)), "{e:?}");
    assert_eq!(
        names(&elsewhere),
        before,
        "the junction's target was written"
    );
    fs::remove_dir(tool.join("tmp")).unwrap();
}

#[cfg(windows)]
#[test]
fn on_windows_the_uri_path_is_the_long_form_of_a_full_path() {
    let uri = |p: &str| uri_path(Path::new(p)).map(|b| String::from_utf8(b).unwrap());
    assert_eq!(uri(r"C:\a\b.db").unwrap(), r"\\?\C:\a\b.db");
    assert_eq!(uri(r"C:/a/./c/../b.db").unwrap(), r"\\?\C:\a\b.db");
    assert_eq!(
        uri(r"\\server\share\b.db").unwrap(),
        r"\\?\UNC\server\share\b.db"
    );
    assert_eq!(uri(r"\\?\C:\a\b.db").unwrap(), r"\\?\C:\a\b.db");
    assert_eq!(
        uri(r"\\?\UNC\server\share\b.db").unwrap(),
        r"\\?\UNC\server\share\b.db"
    );
    for not_full in ["b.db", r"a\b.db", r"\a\b.db", "C:b.db", r"\\.\pipe\b"] {
        assert!(
            matches!(uri(not_full), Err(Error::Open(ref m)) if m.contains("full path")),
            "{not_full}"
        );
    }
}

#[cfg(not(windows))]
#[test]
fn elsewhere_the_uri_path_is_the_path_itself() {
    for p in ["/a/b.db", "a/b.db", "/a/odd ?#%.db"] {
        assert_eq!(uri_path(Path::new(p)).unwrap(), p.as_bytes());
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
