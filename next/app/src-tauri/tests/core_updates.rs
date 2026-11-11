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
    assert!(staged.path().exists(), "reading the versions removed the checked copy");
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
    let (path, sha, _) = f.conn.prepare_adopt().unwrap();
    std::fs::copy(built().join("1.0.0/pluxy"), &custom).unwrap();
    let err = f
        .conn
        .apply(CoreChange::Adopt { path, sha256: sha })
        .unwrap_err();
    assert!(err.contains("changed after you confirmed"), "{err}");
    assert!(f.installed().is_empty());

    let (path, sha, _) = f.conn.prepare_adopt().unwrap();
    f.conn
        .apply(CoreChange::Adopt {
            path,
            sha256: sha.clone(),
        })
        .unwrap();
    let id = format!("custom-{}", &sha[..12]);
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
}
