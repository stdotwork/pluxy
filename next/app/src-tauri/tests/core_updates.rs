//! The app's own core versions in managed mode (`cores.rs`, through the connection the window's
//! commands use), with real cores built by Go and releases signed at run time. The native test
//! (`native/run.ts`) runs the same changes through the real confirmation dialog; these cover the
//! paths it does not, such as a crash while a candidate starts and hostile archives.

#[path = "support/releases.rs"]
mod releases;

use pluxy_app::conn::{Change, Connection, CoreChange, Dirs, Mode};
use pluxy_app::cores::{ConfigSnapshot, Store};
use releases::*;
use serde_json::Value;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::OnceLock;

struct Temp(PathBuf);
impl std::ops::Deref for Temp {
    type Target = Path;
    fn deref(&self) -> &Path {
        &self.0
    }
}
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn temp(name: &str) -> Temp {
    let d = std::env::temp_dir().join(format!(
        "pluxy-app-cores-{name}-{}",
        pluxy_app::managed::random_key()
    ));
    std::fs::create_dir_all(&d).unwrap();
    Temp(d)
}

/// Cores 1.0.0 and 2.0.0 and the bad candidate, built once for this test program.
fn built() -> &'static Path {
    static DIR: OnceLock<PathBuf> = OnceLock::new();
    DIR.get_or_init(|| {
        let d = Path::new(env!("CARGO_TARGET_TMPDIR"))
            .join(format!("core-updates-{}", pluxy_app::managed::random_key()));
        build_core(&d.join("1.0.0"), "1.0.0");
        build_core(&d.join("2.0.0"), "2.0.0");
        bad_core(&d.join("3.0.0"));
        // Exits after making its config folder (its working folder) unwritable, so putting the
        // config back fails.
        script_core(
            &d.join("3.1.0"),
            "chmod 500 .\necho 'pluxy: candidate locked its config folder' >&2\nexit 3",
        );
        // Exits after making the app's core store unwritable, so recording the failure fails.
        script_core(
            &d.join("3.2.0"),
            "chmod 500 ../cores\necho 'pluxy: candidate locked the store' >&2\nexit 3",
        );
        d
    })
}

/// A source with releases 1.0.0, 2.0.0, a bad-start 3.0.0 and a badly signed 2.1.0, and a
/// connection in managed mode reading it.
struct Fixture {
    root: Temp,
    index: PathBuf,
    keys: Keys,
    conn: Connection,
}

fn dirs(root: &Path) -> Dirs {
    Dirs {
        config: root.join("config"),
        core: root.join("core"),
        cores: root.join("cores"),
    }
}

fn fixture(name: &str) -> Fixture {
    let root = temp(name);
    let src = root.join("source");
    std::fs::create_dir_all(&src).unwrap();
    let index = src.join("index.json");
    let keys = Keys::new(&index);
    let b = built();
    let releases = vec![
        release(&src, &keys, "1.0.0", &pack(&b.join("1.0.0")), true),
        release(&src, &keys, "2.0.0", &pack(&b.join("2.0.0")), true),
        release(&src, &keys, "3.0.0", &pack(&b.join("3.0.0")), true),
        release(&src, &keys, "2.1.0", &pack(&b.join("2.0.0")), false),
        // Signed as 2.5.0, but the program in it is 2.0.0.
        release(&src, &keys, "2.5.0", &pack(&b.join("2.0.0")), true),
        release(&src, &keys, "3.1.0", &pack(&b.join("3.1.0")), true),
        release(&src, &keys, "3.2.0", &pack(&b.join("3.2.0")), true),
        // Release versions in the adopted programs' namespace, and one ending in .json.
        release(
            &src,
            &keys,
            "custom-0123456789ab",
            &pack(&b.join("1.0.0")),
            true,
        ),
        release(&src, &keys, "1.0.0.json", &pack(&b.join("1.0.0")), true),
    ];
    write_index(&index, &releases);
    let mut conn = Connection::open(dirs(&root), None);
    conn.change(Change {
        mode: Some(Mode::Managed),
        core_source: Some(index.display().to_string()),
        ..Change::default()
    })
    .unwrap();
    Fixture {
        root,
        index,
        keys,
        conn,
    }
}

impl Fixture {
    fn install(&mut self, version: &str) -> Result<(), String> {
        let staged = self.conn.prepare_install(&self.keys, version)?;
        self.conn.apply(CoreChange::Install(staged))
    }
    fn reported(&self) -> (u16, Option<String>) {
        let r = self.conn.caller().call("GET", "/status", None);
        let v = r
            .body
            .as_ref()
            .and_then(|b| b["version"].as_str())
            .map(String::from);
        (r.status, v)
    }
    fn config(&self) -> Vec<u8> {
        std::fs::read(self.root.join("core/config.json")).unwrap()
    }
    fn installed(&self) -> Vec<String> {
        self.conn
            .versions()
            .installed
            .into_iter()
            .map(|v| v.id)
            .collect()
    }
}

fn alive(pid: u32) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .map(|s| !s.rsplit(") ").next().unwrap_or("").starts_with('Z'))
        .unwrap_or(false)
}

fn exe(pid: u32) -> PathBuf {
    std::fs::read_link(format!("/proc/{pid}/exe")).unwrap_or_default()
}

/// The processes whose program is path.
fn running(path: &Path) -> usize {
    let want = std::fs::canonicalize(path).unwrap();
    std::fs::read_dir("/proc")
        .unwrap()
        .flatten()
        .filter(|e| std::fs::read_link(e.path().join("exe")).ok().as_ref() == Some(&want))
        .count()
}

#[test]
fn a_missing_core_is_installed_before_connecting_then_updated() {
    let mut f = fixture("install");
    let v = f.conn.view();
    let core = v.core.clone().unwrap();
    assert_eq!(core.state, "missing", "{v:?}");
    assert!(
        v.target.is_none() && !v.signed_in,
        "connected with no core: {v:?}"
    );

    // While the person confirms, the window keeps reading the connection and the versions; the
    // checked copy stays until it is applied.
    let staged = f.conn.prepare_install(&f.keys, "1.0.0").unwrap();
    let _ = (f.conn.view(), f.conn.versions());
    // The confirmation binds this file by its path.
    assert!(
        staged.path().exists(),
        "reading the versions removed the checked copy"
    );
    f.conn.apply(CoreChange::Install(staged)).unwrap();
    assert_eq!(f.reported(), (200, Some("1.0.0".into())));
    let v = f.conn.view();
    assert_eq!(v.core.as_ref().unwrap().version.as_deref(), Some("1.0.0"));
    let pid1 = f.conn.core_pid().unwrap();
    assert_eq!(exe(pid1), f.root.join("cores/versions/1.0.0/pluxy"));
    let config = f.config();

    f.install("2.0.0").unwrap();
    assert_eq!(f.reported(), (200, Some("2.0.0".into())));
    assert!(!alive(pid1), "core 1.0.0 still runs");
    assert_eq!(f.config(), config, "the update changed the config");
    assert_eq!(f.installed(), ["1.0.0", "2.0.0"]);

    // The selection lasts: the next start runs 2.0.0 again.
    f.conn.stop_core();
    let c = Connection::open(dirs(&f.root), None);
    let r = c.caller().call("GET", "/status", None);
    assert_eq!(r.body.unwrap()["version"], "2.0.0");
}

/// A core the app did not start, on this computer: the sentinel.
struct Sentinel {
    child: Child,
    dir: Temp,
}

impl Drop for Sentinel {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn sentinel() -> Sentinel {
    let dir = temp("sentinel");
    std::fs::copy(built().join("1.0.0/pluxy"), dir.join("pluxy")).unwrap();
    std::fs::write(
        dir.join("config.json"),
        serde_json::json!({"listen": "127.0.0.1:0", "client_keys": ["c"], "management_key": "sentinel-key",
            "credentials_dir": dir.join("credentials"), "packages": {"dir": dir.join("packages")}})
        .to_string(),
    )
    .unwrap();
    let child = Command::new(dir.join("pluxy"))
        .arg("-config")
        .arg(dir.join("config.json"))
        .current_dir(&*dir)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    Sentinel { child, dir }
}

#[test]
fn a_bad_start_restores_the_config_and_runs_the_last_good_core() {
    let s = sentinel();
    let before = std::fs::read(s.dir.join("config.json")).unwrap();
    let mut f = fixture("bad-start");
    f.install("1.0.0").unwrap();
    let config = f.config();
    let pid1 = f.conn.core_pid().unwrap();

    let err = f.install("3.0.0").unwrap_err();
    assert!(err.contains("bad candidate broke its config"), "{err}");
    assert!(
        err.contains("put back") && err.contains("core 1.0.0 runs again"),
        "{err}"
    );
    assert_eq!(f.config(), config, "the config was not restored");
    assert_eq!(f.reported(), (200, Some("1.0.0".into())));
    assert!(
        !alive(pid1),
        "the old core was never stopped for the candidate"
    );
    let v = f.conn.versions();
    assert_eq!(v.failed.unwrap().version, "3.0.0");
    assert_eq!(v.selected.as_deref(), Some("1.0.0"));
    assert_eq!(f.installed(), ["1.0.0"], "the failed candidate was kept");

    // A core that answers, but is not the version it was signed as, is a bad start too.
    let err = f.install("2.5.0").unwrap_err();
    assert!(err.contains("reports version 2.0.0, not 2.5.0"), "{err}");
    assert_eq!(f.reported(), (200, Some("1.0.0".into())));
    assert_eq!(f.config(), config);

    // Through all of it, and after the app's core stops, the sentinel runs on, untouched.
    f.conn.stop_core();
    assert!(alive(s.child.id()), "the sentinel core was stopped");
    assert_eq!(std::fs::read(s.dir.join("config.json")).unwrap(), before);
}

#[test]
fn a_crash_while_a_candidate_starts_is_undone_at_the_next_start() {
    let mut f = fixture("crash");
    f.install("1.0.0").unwrap();
    let config = f.config();
    f.conn.stop_core();
    // What the app leaves when it dies after it marked 2.0.0 the candidate and 2.0.0 broke the
    // config: the kept config copy, the candidate in the state, a broken config.
    let store = Store::open(&f.root.join("cores")).unwrap();
    ConfigSnapshot::take(&store, &f.root.join("core/config.json")).unwrap();
    let mut st = store.state().unwrap();
    st.candidate = Some("2.0.0".into());
    store.save_state(&st).unwrap();
    std::fs::write(f.root.join("core/config.json"), b"broken").unwrap();

    let c = Connection::open(dirs(&f.root), None);
    assert_eq!(
        std::fs::read(f.root.join("core/config.json")).unwrap(),
        config
    );
    let r = c.caller().call("GET", "/status", None);
    assert_eq!(r.body.unwrap()["version"], "1.0.0");
    let v = c.versions();
    assert_eq!(v.failed.unwrap().version, "2.0.0");
    assert!(store.state().unwrap().candidate.is_none());
}

#[test]
fn refusals_change_nothing_and_the_same_routes_work() {
    let mut f = fixture("refuse");
    // No trust roots (a normal build's trust store): refused before the archive is read.
    let home = temp("home");
    let none = pluxy_app::trust::TrustStore::load(&home).unwrap();
    let err = f.conn.prepare_install(&none, "1.0.0").err().unwrap();
    assert!(err.contains("trust not configured"), "{err}");
    // A bad signature, another key, a changed source, a version not offered.
    let err = f.conn.prepare_install(&f.keys, "2.1.0").err().unwrap();
    assert!(err.contains("signature"), "{err}");
    let other = Keys::new(&f.index);
    let err = f.conn.prepare_install(&other, "1.0.0").err().unwrap();
    assert!(err.contains("unknown publisher key"), "{err}");
    let moved = f.root.join("source/moved.json");
    std::fs::copy(&f.index, &moved).unwrap();
    let other_root = temp("moved");
    let mut c2 = Connection::open(dirs(&other_root), None);
    c2.change(Change {
        mode: Some(Mode::Managed),
        core_source: Some(moved.display().to_string()),
        ..Change::default()
    })
    .unwrap();
    let err = c2.prepare_install(&f.keys, "1.0.0").err().unwrap();
    assert!(err.contains("changed source"), "{err}");
    assert!(f
        .conn
        .prepare_install(&f.keys, "9.9.9")
        .err()
        .unwrap()
        .contains("offers no core"));
    assert!(f.installed().is_empty());
    assert!(f.conn.view().target.is_none());

    // The same route, with the trusted key and source and a good signature.
    f.install("1.0.0").unwrap();
    f.install("2.0.0").unwrap();
    // An unknown rollback target is refused; a previously installed one runs.
    let err = f.conn.prepare_rollback("9.9.9").unwrap_err();
    assert!(err.contains("not installed"), "{err}");
    let err = f.conn.prepare_rollback("../x").unwrap_err();
    assert!(err.contains("not installed"), "{err}");
    assert!(f
        .conn
        .prepare_rollback("2.0.0")
        .unwrap_err()
        .contains("already runs"));
    f.conn.prepare_rollback("1.0.0").unwrap();
    f.conn.apply(CoreChange::Rollback("1.0.0".into())).unwrap();
    assert_eq!(f.reported(), (200, Some("1.0.0".into())));
    assert_eq!(f.conn.versions().previous.as_deref(), Some("2.0.0"));
}

#[test]
fn a_changed_installed_core_does_not_start() {
    use std::os::unix::fs::PermissionsExt;
    let mut f = fixture("tamper");
    f.install("1.0.0").unwrap();
    f.conn.stop_core();
    // The same start, unchanged, runs it.
    let c = Connection::open(dirs(&f.root), None);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    drop(c);
    let p = f.root.join("cores/versions/1.0.0/pluxy");
    std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o700)).unwrap();
    let mut bytes = std::fs::read(&p).unwrap();
    bytes.push(0);
    std::fs::write(&p, bytes).unwrap();
    std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o500)).unwrap();
    let mut c = Connection::open(dirs(&f.root), None);
    let v = c.view();
    assert!(
        v.error
            .as_deref()
            .unwrap_or("")
            .contains("changed since it was installed"),
        "{v:?}"
    );
    assert!(c.core_pid().is_none());
    // A9: the same release installs again over it, through the same checks, and runs.
    let staged = c.prepare_install(&f.keys, "1.0.0").unwrap();
    c.apply(CoreChange::Install(staged)).unwrap();
    assert_eq!(
        c.caller().call("GET", "/status", None).body.unwrap()["version"],
        "1.0.0"
    );
}

/// An archive holding entry (a raw tar header) after a good program.
fn hostile(name: &[u8], kind: u8, data: &[u8]) -> Vec<u8> {
    let gz = flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::fast());
    let mut b = tar::Builder::new(gz);
    let prog = std::fs::read(built().join("1.0.0/pluxy")).unwrap();
    let mut h = tar::Header::new_ustar();
    h.set_path("pluxy").unwrap();
    h.set_size(prog.len() as u64);
    h.set_mode(0o755);
    h.set_cksum();
    b.append(&h, &prog[..]).unwrap();
    let mut h = tar::Header::new_ustar();
    h.as_old_mut().name[..name.len()].copy_from_slice(name);
    h.set_entry_type(tar::EntryType::new(kind));
    h.set_size(data.len() as u64);
    h.set_mode(0o644);
    if kind == b'2' {
        h.set_link_name("/etc/passwd").unwrap();
    }
    h.set_cksum();
    b.append(&h, data).unwrap();
    b.into_inner().unwrap().finish().unwrap()
}

#[test]
fn archives_with_links_or_escaping_names_are_refused() {
    let root = temp("hostile");
    let index = root.join("index.json");
    let keys = Keys::new(&index);
    let cases: [(&str, &[u8], u8); 4] = [
        ("1.0.1", b"../escape", b'0'),
        ("1.0.2", b"/abs", b'0'),
        ("1.0.3", b"link", b'2'),
        ("1.0.4", b"fifo", b'6'),
    ];
    let mut rel: Vec<Value> = cases
        .iter()
        .map(|(v, name, kind)| release(&root, &keys, v, &hostile(name, *kind, b"x"), true))
        .collect();
    // The positive control: the same archive with a plain extra file.
    rel.push(release(
        &root,
        &keys,
        "1.0.0",
        &hostile(b"bundled/x", b'0', b"x"),
        true,
    ));
    write_index(&index, &rel);
    let store = Store::open(&root.join("cores")).unwrap();
    let source = index.display().to_string();
    for (v, name, _) in cases {
        let staged = store.stage(&keys, &source, v).unwrap();
        let err = store.install(staged).unwrap_err();
        assert!(
            err.contains("leaves its folder") || err.contains("not a regular file"),
            "{}: {err}",
            String::from_utf8_lossy(name)
        );
    }
    assert!(!root.join("escape").exists());
    assert!(store.list().is_empty());
    let staged = store.stage(&keys, &source, "1.0.0").unwrap();
    assert_eq!(store.install(staged).unwrap(), "1.0.0");
    store.program("1.0.0").unwrap();
    assert!(std::fs::read_dir(root.join("cores/staging"))
        .unwrap()
        .next()
        .is_none());
}

#[test]
fn a_custom_program_runs_only_once_adopted() {
    let mut f = fixture("adopt");
    let custom = f.root.join("custom/pluxy");
    std::fs::create_dir_all(custom.parent().unwrap()).unwrap();
    std::fs::copy(built().join("2.0.0/pluxy"), &custom).unwrap();
    f.conn
        .change(Change {
            mode: Some(Mode::Managed),
            core_path: Some(custom.display().to_string()),
            core_source: Some(f.index.display().to_string()),
            ..Change::default()
        })
        .unwrap();
    let v = f.conn.view();
    let core = v.core.unwrap();
    assert_eq!(core.adopt.as_deref(), Some(custom.to_str().unwrap()));
    assert_eq!(core.state, "missing");
    assert_eq!(
        running(&custom),
        0,
        "the custom program runs without adoption"
    );

    // Changed after the person confirmed it: refused, nothing installed.
    let (path, sha, size) = f.conn.prepare_adopt().unwrap();
    std::fs::copy(built().join("1.0.0/pluxy"), &custom).unwrap();
    let err = f
        .conn
        .apply(CoreChange::Adopt {
            path,
            sha256: sha,
            size,
        })
        .unwrap_err();
    assert!(err.contains("changed after you confirmed"), "{err}");
    assert!(f.installed().is_empty());

    let (path, sha, size) = f.conn.prepare_adopt().unwrap();
    f.conn
        .apply(CoreChange::Adopt {
            path,
            sha256: sha.clone(),
            size,
        })
        .unwrap();
    let id = format!("custom-{sha}");
    let v = f.conn.view();
    assert_eq!(
        v.core.as_ref().unwrap().version.as_deref(),
        Some(id.as_str())
    );
    assert!(v.core.unwrap().adopt.is_none());
    let pid = f.conn.core_pid().unwrap();
    assert_eq!(exe(pid), f.root.join(format!("cores/versions/{id}/pluxy")));
    assert_eq!(running(&custom), 0);
    assert_eq!(f.reported(), (200, Some("1.0.0".into())));

    // N3: a damaged adopted copy does not start, and adopting the program again repairs it.
    use std::os::unix::fs::PermissionsExt;
    f.conn.stop_core();
    let copy = f.root.join(format!("cores/versions/{id}/pluxy"));
    std::fs::set_permissions(&copy, std::fs::Permissions::from_mode(0o700)).unwrap();
    let mut bytes = std::fs::read(&copy).unwrap();
    bytes.push(0);
    std::fs::write(&copy, bytes).unwrap();
    std::fs::set_permissions(&copy, std::fs::Permissions::from_mode(0o500)).unwrap();
    let mut c = Connection::open(dirs(&f.root), None);
    let err = c.view().error.unwrap_or_default();
    assert!(err.contains("adopt it again"), "{err}");
    assert!(c.core_pid().is_none());
    // Choosing the program again saves it, and still runs no damaged copy.
    let err = c
        .change(Change {
            mode: Some(Mode::Managed),
            core_path: Some(custom.display().to_string()),
            core_source: Some(f.index.display().to_string()),
            ..Change::default()
        })
        .unwrap_err();
    assert!(err.contains("adopt it again"), "{err}");
    let (path, sha, size) = c.prepare_adopt().unwrap();
    c.apply(CoreChange::Adopt {
        path,
        sha256: sha,
        size,
    })
    .unwrap();
    assert_eq!(
        c.caller().call("GET", "/status", None).body.unwrap()["version"],
        "1.0.0"
    );
}

/// The management key in the managed core's config.
fn management_key(root: &Path) -> String {
    let cfg: Value =
        serde_json::from_slice(&std::fs::read(root.join("core/config.json")).unwrap()).unwrap();
    cfg["management_key"].as_str().unwrap().to_string()
}

/// A listener on this computer that is not the app's core: it answers every request 200 with a
/// version, and keeps what it was sent.
fn impostor(listener: std::net::TcpListener) -> std::sync::Arc<std::sync::Mutex<Vec<u8>>> {
    use std::io::{Read, Write};
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let keep = seen.clone();
    std::thread::spawn(move || {
        for conn in listener.incoming() {
            let Ok(mut c) = conn else { return };
            let _ = c.set_read_timeout(Some(std::time::Duration::from_millis(500)));
            let mut buf = [0u8; 4096];
            if let Ok(n) = c.read(&mut buf) {
                keep.lock().unwrap().extend_from_slice(&buf[..n]);
            }
            let body = r#"{"version":"1.0.0"}"#;
            let _ = write!(
                c,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            );
        }
    });
    seen
}

/// B1-1: a program that says it listens where something else does is not ready, and the key
/// never goes there.
#[test]
fn readiness_never_sends_the_key_to_a_listener_the_core_does_not_own() {
    let mut f = fixture("impostor");
    f.install("1.0.0").unwrap();
    let key = management_key(&f.root);
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let seen = impostor(listener);
    let custom = script_core(
        &f.root.join("liar"),
        &format!("echo 'pluxy: listening on http://127.0.0.1:{port}' >&2\nexec sleep 30"),
    );
    f.conn
        .change(Change {
            mode: Some(Mode::Managed),
            core_path: Some(custom.display().to_string()),
            core_source: Some(f.index.display().to_string()),
            ..Change::default()
        })
        .unwrap();
    let (path, sha, size) = f.conn.prepare_adopt().unwrap();
    let err = f
        .conn
        .apply(CoreChange::Adopt {
            path,
            sha256: sha,
            size,
        })
        .unwrap_err();
    assert!(err.contains("not listening"), "{err}");
    let sent = String::from_utf8_lossy(&seen.lock().unwrap()).into_owned();
    assert!(
        !sent.contains(&key),
        "the management key went to another listener: {sent}"
    );
    assert_eq!(f.reported(), (200, Some("1.0.0".into())));
}

/// B1-1: once the app's core has exited, calls do not go to whatever listens on its port next.
#[test]
fn calls_stop_when_the_core_is_gone() {
    let mut f = fixture("gone");
    f.install("1.0.0").unwrap();
    assert_eq!(f.conn.caller().call("GET", "/status", None).status, 200);
    let pid = f.conn.core_pid().unwrap();
    let cfg: Value =
        serde_json::from_slice(&std::fs::read(f.root.join("core/config.json")).unwrap()).unwrap();
    let listen = cfg["listen"].as_str().unwrap().to_string();
    unsafe { libc::kill(pid as i32, libc::SIGKILL) };
    for _ in 0..100 {
        if f.conn.view().core.unwrap().state == "stopped" {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(50));
    }
    assert_eq!(f.conn.view().core.unwrap().state, "stopped");
    let seen = impostor(std::net::TcpListener::bind(&listen).unwrap());
    let r = f.conn.caller().call("GET", "/status", None);
    assert_ne!(r.status, 200, "a call reached the new listener");
    std::thread::sleep(std::time::Duration::from_millis(200));
    assert!(
        seen.lock().unwrap().is_empty(),
        "a call reached the new listener"
    );
}

/// N1: once the app's core is stopped (as at quit), no call goes to its port: not one taken
/// before, nor one taken after.
#[test]
fn calls_after_a_stop_go_nowhere() {
    let mut f = fixture("stopped");
    f.install("1.0.0").unwrap();
    let before = f.conn.caller();
    let cfg: Value =
        serde_json::from_slice(&std::fs::read(f.root.join("core/config.json")).unwrap()).unwrap();
    let listen = cfg["listen"].as_str().unwrap().to_string();
    f.conn.stop_core();
    let seen = impostor(std::net::TcpListener::bind(&listen).unwrap());
    for c in [before, f.conn.caller()] {
        assert_ne!(c.call("GET", "/status", None).status, 200);
    }
    std::thread::sleep(std::time::Duration::from_millis(200));
    assert!(
        seen.lock().unwrap().is_empty(),
        "a call reached the new listener"
    );
}

/// B1-3: when the config cannot be put back, no core runs and the next start tries again.
#[test]
fn a_failed_config_restore_keeps_the_core_stopped_and_retries_at_start() {
    use std::os::unix::fs::PermissionsExt;
    let mut f = fixture("restore-fails");
    f.install("1.0.0").unwrap();
    let config = f.config();
    let err = f.install("3.1.0").unwrap_err();
    let core = f.root.join("core");
    std::fs::set_permissions(&core, std::fs::Permissions::from_mode(0o700)).unwrap();
    assert!(err.contains("could not put the config back"), "{err}");
    assert!(
        f.conn.core_pid().is_none(),
        "a core runs on a config the app could not restore"
    );
    let store = Store::open(&f.root.join("cores")).unwrap();
    assert_eq!(store.state().unwrap().candidate.as_deref(), Some("3.1.0"));
    std::fs::write(core.join("config.json"), b"half written").unwrap();
    // The next start puts the config back and runs 1.0.0.
    let c = Connection::open(dirs(&f.root), None);
    assert_eq!(std::fs::read(core.join("config.json")).unwrap(), config);
    assert_eq!(
        c.caller().call("GET", "/status", None).body.unwrap()["version"],
        "1.0.0"
    );
}

/// B1-4: when the failure cannot be recorded, the last good core still runs again.
#[test]
fn an_unrecordable_failure_still_runs_the_last_good_core() {
    use std::os::unix::fs::PermissionsExt;
    let mut f = fixture("unrecordable");
    f.install("1.0.0").unwrap();
    let err = f.install("3.2.0").unwrap_err();
    std::fs::set_permissions(f.root.join("cores"), std::fs::Permissions::from_mode(0o700)).unwrap();
    assert!(err.contains("candidate locked the store"), "{err}");
    assert_eq!(f.reported(), (200, Some("1.0.0".into())), "{err}");
    // N2: the candidate is still recorded, but its config was put back already: the next start
    // keeps the person's edits since.
    let store = Store::open(&f.root.join("cores")).unwrap();
    assert_eq!(store.state().unwrap().candidate.as_deref(), Some("3.2.0"));
    let cfg: Value = serde_json::from_slice(&f.config()).unwrap();
    let edited = serde_json::to_vec(&cfg).unwrap();
    assert_ne!(edited, f.config());
    std::fs::write(f.root.join("core/config.json"), &edited).unwrap();
    f.conn.stop_core();
    let c = Connection::open(dirs(&f.root), None);
    assert_eq!(f.config(), edited, "the next start put the old config back");
    assert_eq!(
        c.caller().call("GET", "/status", None).body.unwrap()["version"],
        "1.0.0"
    );
    let st = store.state().unwrap();
    assert_eq!(st.candidate, None);
    assert_eq!(st.failed.unwrap().version, "3.2.0");
}

/// A8: an unreadable kept config is an error, not a reason to delete the config.
#[test]
fn an_unreadable_kept_config_never_deletes_the_config() {
    use std::os::unix::fs::PermissionsExt;
    let mut f = fixture("unreadable-copy");
    f.install("1.0.0").unwrap();
    let config = f.config();
    f.conn.stop_core();
    let store = Store::open(&f.root.join("cores")).unwrap();
    ConfigSnapshot::take(&store, &f.root.join("core/config.json")).unwrap();
    let mut st = store.state().unwrap();
    st.candidate = Some("2.0.0".into());
    store.save_state(&st).unwrap();
    let kept = f.root.join("cores/config-last-good.json");
    std::fs::set_permissions(&kept, std::fs::Permissions::from_mode(0o000)).unwrap();
    let mut c = Connection::open(dirs(&f.root), None);
    std::fs::set_permissions(&kept, std::fs::Permissions::from_mode(0o600)).unwrap();
    assert_eq!(f.config(), config, "the config was removed");
    assert!(c.core_pid().is_none());
    assert!(c.view().error.is_some());
}

/// A6: release versions cannot take an adopted program's name; adopted names use the whole
/// SHA-256.
#[test]
fn adopted_and_released_names_stay_apart() {
    let mut f = fixture("names");
    let err = f
        .conn
        .prepare_install(&f.keys, "custom-0123456789ab")
        .err()
        .unwrap();
    assert!(err.contains("reserved"), "{err}");
    let custom = f.root.join("mine/pluxy");
    std::fs::create_dir_all(custom.parent().unwrap()).unwrap();
    std::fs::copy(built().join("1.0.0/pluxy"), &custom).unwrap();
    f.conn
        .change(Change {
            mode: Some(Mode::Managed),
            core_path: Some(custom.display().to_string()),
            core_source: Some(f.index.display().to_string()),
            ..Change::default()
        })
        .unwrap();
    let (path, sha, size) = f.conn.prepare_adopt().unwrap();
    f.conn
        .apply(CoreChange::Adopt {
            path,
            sha256: sha.clone(),
            size,
        })
        .unwrap();
    assert_eq!(f.installed(), [format!("custom-{sha}")]);
}

/// A7: a FIFO named as the core program is refused without blocking.
#[test]
fn a_fifo_is_not_a_core_program() {
    let root = temp("fifo");
    let fifo = root.join("pluxy");
    let c = std::ffi::CString::new(fifo.to_str().unwrap()).unwrap();
    assert_eq!(unsafe { libc::mkfifo(c.as_ptr(), 0o755) }, 0);
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let _ = tx.send(Store::inspect(&fifo));
    });
    let r = rx
        .recv_timeout(std::time::Duration::from_secs(5))
        .expect("inspecting a FIFO blocked");
    assert!(r.unwrap_err().contains("not a regular"));
}

/// A10: a version named like another version's record is its own version.
#[test]
fn a_version_named_like_a_record_installs_beside_it() {
    let f = fixture("json-name");
    let store = Store::open(&f.root.join("cores")).unwrap();
    let source = f.index.display().to_string();
    for v in ["1.0.0", "1.0.0.json"] {
        let staged = store.stage(&f.keys, &source, v).unwrap();
        assert_eq!(store.install(staged).unwrap(), v);
    }
    store.program("1.0.0").unwrap();
    store.program("1.0.0.json").unwrap();
    assert_eq!(store.list().len(), 2);
}

/// A4: an installed version's recorded file paths must be plain paths beneath its folder.
#[test]
fn recorded_paths_that_leave_the_plain_layout_are_refused() {
    let root = temp("record-paths");
    let index = root.join("index.json");
    let keys = Keys::new(&index);
    let rel = release(
        &root,
        &keys,
        "1.0.0",
        &hostile(b"bundled/x", b'0', b"x"),
        true,
    );
    write_index(&index, &[rel]);
    let store = Store::open(&root.join("cores")).unwrap();
    let staged = store
        .stage(&keys, &index.display().to_string(), "1.0.0")
        .unwrap();
    store.install(staged).unwrap();
    store.program("1.0.0").unwrap();
    let record = root.join("cores/records/1.0.0.json");
    let text = std::fs::read_to_string(&record).unwrap();
    // The same file, reached another way.
    std::fs::write(
        &record,
        text.replace("\"bundled/x\"", "\"bundled/../bundled/x\""),
    )
    .unwrap();
    let err = store.program("1.0.0").unwrap_err();
    assert!(err.contains("not installed properly"), "{err}");
}
