//! The app's connection in each mode, against a real core with fake extensions
//! (next/e2e/panelcore, which needs Go). No window: these drive the same connection code the
//! window's commands use. Every core runs in a temporary folder on a random loopback port.

#[path = "support/releases.rs"]
mod releases;

use pluxy_app::client::{certificates, Client};
use pluxy_app::conn::{Change, Connection, CoreChange, Dirs, Kept, Mode, Settings};
use pluxy_app::managed::OnExit;
use serde_json::Value;
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, ChildStdin, Command, Stdio};

fn next_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

/// A temporary folder, removed when dropped, also when a test fails.
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
        "pluxy-app-{name}-{}",
        pluxy_app::managed::random_key()
    ));
    std::fs::create_dir_all(&d).unwrap();
    Temp(d)
}

fn dirs(root: &Path) -> Dirs {
    Dirs {
        config: root.join("config"),
        core: root.join("core"),
        cores: root.join("cores"),
    }
}

/// A core run by panelcore until this is dropped.
struct Core {
    child: Child,
    stdin: Option<ChildStdin>,
    base: String,
    key: String,
    ca: Option<String>,
    /// Core's config file, which holds its current management key.
    config: PathBuf,
}

impl Core {
    fn start(tls: bool) -> Core {
        let mut cmd = Command::new("go");
        cmd.args(["run", "./e2e/panelcore"]);
        if tls {
            cmd.arg("-tls");
        }
        let mut child = cmd
            .current_dir(next_dir())
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .expect("go run ./e2e/panelcore");
        let mut line = String::new();
        BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut line)
            .unwrap();
        let v: Value =
            serde_json::from_str(&line).unwrap_or_else(|e| panic!("panelcore said {line:?}: {e}"));
        Core {
            stdin: child.stdin.take(),
            child,
            base: v["base"].as_str().unwrap().into(),
            key: v["key"].as_str().unwrap().into(),
            ca: v["ca"].as_str().map(String::from),
            config: v["config"].as_str().unwrap().into(),
        }
    }

    /// This core, reached directly, not through the app.
    fn client(&self) -> Client {
        let certs = self
            .ca
            .as_ref()
            .map(|f| certificates(&std::fs::read(f).unwrap()).unwrap());
        Client::new(&self.base, certs)
    }

    fn status(&self, key: &str) -> u16 {
        self.client().call(key, "GET", "/status", None).status
    }
}

/// Rotates c's management key, failing the test with core's or the app's reason.
fn rotate(c: &mut Connection) -> pluxy_app::conn::Rotation {
    c.rotate_key()
        .unwrap_or_else(|r| panic!("rotation failed: {r:?}"))
}

/// The management key in the core config at path: core saves a rotated key there first.
fn config_key(path: &Path) -> String {
    pluxy_app::managed::management_key(path).unwrap()
}

impl Drop for Core {
    fn drop(&mut self) {
        if let Some(mut s) = self.stdin.take() {
            let _ = s.flush();
        }
        let _ = self.child.wait();
    }
}

/// The extensions core lists pages for, through the app's connection.
fn page_extensions(c: &Connection) -> Vec<String> {
    let r = c.caller().call("GET", "/ui", None);
    assert_eq!(r.status, 200, "{r:?}");
    r.body.unwrap()["extensions"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|e| !e["pages"].as_array().unwrap().is_empty())
        .map(|e| e["id"].as_str().unwrap().to_string())
        .collect()
}

fn alive(pid: u32) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .map(|s| !s.rsplit(") ").next().unwrap_or("").starts_with('Z'))
        .unwrap_or(false)
}

#[test]
fn managed_runs_its_own_core_and_stops_it() {
    let root = temp("managed");
    let out = Command::new("go")
        .args(["run", "./e2e/panelcore", "-prepare"])
        .arg(root.join("program"))
        .current_dir(next_dir())
        .stderr(Stdio::inherit())
        .output()
        .unwrap();
    assert!(out.status.success());
    // The prepared core and its bundled test packages, as a signed release 1.0.0.
    let program = root.join("program");
    releases::build_core(&program, "1.0.0");
    let source = root.join("source");
    std::fs::create_dir_all(&source).unwrap();
    let index = source.join("index.json");
    let keys = releases::Keys::new(&index);
    let release = releases::release(&source, &keys, "1.0.0", &releases::pack(&program), true);
    releases::write_index(&index, &[release]);

    let mut c = Connection::open(dirs(&root), None);
    assert_eq!(c.view().mode, None);
    c.change(Change {
        mode: Some(Mode::Managed),
        core_source: Some(index.display().to_string()),
        ..Change::default()
    })
    .unwrap();
    let staged = c.prepare_install(&keys, "1.0.0").unwrap();
    c.apply(CoreChange::Install(staged)).unwrap();
    let v = c.view();
    assert!(v.signed_in, "{v:?}");
    let target = v.target.clone().unwrap();
    assert!(target.starts_with("http://127.0.0.1:"), "{target}");
    let pid = c.core_pid().unwrap();
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    assert!(page_extensions(&c).contains(&"test/alpha".to_string()));
    // The person cannot sign out of the app's own core, nor sign in with another key.
    c.sign_out().unwrap();
    assert!(c.view().signed_in);
    assert!(c.sign_in("wrong", false).is_err());

    // Quitting stops it; the next start runs it again on the same port, with the same config.
    c.stop_core();
    assert!(!alive(pid), "core {pid} still runs");
    drop(c);
    let mut c = Connection::open(dirs(&root), None);
    assert_eq!(c.view().target.as_deref(), Some(target.as_str()));
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    // Another mode stops the app's core too.
    let pid = c.core_pid().unwrap();
    c.change(Change {
        mode: Some(Mode::Local),
        url: Some("http://127.0.0.1:9".into()),
        ..Change::default()
    })
    .unwrap();
    assert!(
        !alive(pid),
        "core {pid} still runs after switching to local mode"
    );
    drop(c);
}

#[test]
fn local_connects_to_a_running_core_and_leaves_it_running() {
    let core = Core::start(false);
    let root = temp("local");
    let mut c = Connection::open(dirs(&root), None);
    // Only this computer, and only a key core accepts.
    let refused = c.change(Change {
        mode: Some(Mode::Local),
        url: Some("http://192.0.2.1:8400".into()),
        ..Change::default()
    });
    assert!(refused.unwrap_err().contains("not on this computer"));
    c.change(Change {
        mode: Some(Mode::Local),
        url: Some(core.base.clone()),
        ..Change::default()
    })
    .unwrap();
    assert_eq!(
        c.sign_in(&pluxy_app::managed::random_key(), false)
            .unwrap_err(),
        "core refused this key"
    );
    assert!(!c.view().signed_in);
    c.sign_in(&core.key, false).unwrap();
    let v = c.view();
    assert_eq!(
        (v.mode, v.target.as_deref(), v.signed_in),
        (Some(Mode::Local), Some(core.base.as_str()), true)
    );
    assert!(!v.remembered);
    assert!(page_extensions(&c).contains(&"test/alpha".to_string()));
    assert_eq!(c.core_pid(), None);

    // Not remembered: a new start asks for the key; signing in with remember keeps it.
    drop(c);
    let mut c = Connection::open(dirs(&root), None);
    assert!(!c.view().signed_in);
    assert_eq!(c.caller().call("GET", "/status", None).status, 401);
    c.sign_in(&core.key, true).unwrap();
    drop(c);
    let mut c = Connection::open(dirs(&root), None);
    assert!(c.view().signed_in && c.view().remembered);
    c.sign_out().unwrap();
    assert!(!c.view().signed_in && !c.view().remembered);
    drop(c);

    // The app never stopped that core.
    let direct = pluxy_app::client::Client::new(&core.base, None);
    assert_eq!(direct.call(&core.key, "GET", "/status", None).status, 200);
}

#[test]
fn remote_needs_https_and_trusts_the_given_authority_only() {
    let core = Core::start(true);
    let ca = core.ca.clone().unwrap();
    assert!(core.base.starts_with("https://"));
    let root = temp("remote");
    let mut c = Connection::open(dirs(&root), None);

    let http = core.base.replacen("https://", "http://", 1);
    let refused = c.change(Change {
        mode: Some(Mode::Remote),
        url: Some(http),
        ..Change::default()
    });
    assert!(refused.unwrap_err().contains("https"));
    // Without its authority, TLS authentication fails without accepting the key.
    c.change(Change {
        mode: Some(Mode::Remote),
        url: Some(core.base.clone()),
        ..Change::default()
    })
    .unwrap();
    let error = c.sign_in(&core.key, false).unwrap_err();
    assert!(error.contains("TLS"), "{error}");
    assert!(!error.contains(&core.key));
    c.change(Change {
        mode: Some(Mode::Remote),
        url: Some(core.base.clone()),
        ca_file: Some(ca.clone()),
        ..Change::default()
    })
    .unwrap();
    c.sign_in(&core.key, true).unwrap();
    let v = c.view();
    assert_eq!(
        (v.mode, v.target.as_deref(), v.signed_in),
        (Some(Mode::Remote), Some(core.base.as_str()), true)
    );
    assert_eq!(v.ca_file.as_deref(), Some(ca.as_str()));
    assert!(page_extensions(&c).contains(&"test/alpha".to_string()));
    drop(c);
    // Kept: a new start connects with it.
    let c = Connection::open(dirs(&root), None);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    drop(c);
}

/// A rotated key is kept as the old one was, also across a restart (a new Connection on the same
/// folders, as the app opens it at startup); the old key stops working; a failed save leaves the
/// app on the new key and saying what to do; a call in flight with the old key goes again.
#[test]
fn local_rotation_keeps_the_new_key_as_the_old_one_was_kept() {
    let core = Core::start(false);
    let root = temp("rotate-local");
    let mut c = Connection::open(dirs(&root), None);
    c.change(Change {
        mode: Some(Mode::Local),
        url: Some(core.base.clone()),
        ..Change::default()
    })
    .unwrap();

    // Kept on this device.
    c.sign_in(&core.key, true).unwrap();
    let in_flight = c.caller();
    let first = rotate(&mut c);
    assert_eq!(first.kept, Kept::Device);
    assert_eq!(first.key, config_key(&core.config));
    assert_ne!(first.key, core.key);
    assert_eq!(core.status(&core.key), 401);
    assert_eq!(core.status(&first.key), 200);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    let settings = dirs(&root).settings();
    assert_eq!(
        Settings::load(&settings).unwrap().key,
        Some(first.key.clone())
    );
    assert!(c.view().error.is_none());
    // A call that left with the old key is refused, and sent again with the new one.
    assert_eq!(in_flight.call("GET", "/status", None).status, 401);
    assert_eq!(
        in_flight
            .call_or_retry("GET", "/status", None, || c.caller())
            .status,
        200
    );
    drop(c);
    let mut c = Connection::open(dirs(&root), None);
    assert!(c.view().signed_in && c.view().remembered);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);

    // Not kept: it lasts until the app quits, and signs in again after a restart.
    c.sign_out().unwrap();
    c.sign_in(&first.key, false).unwrap();
    let second = rotate(&mut c);
    assert_eq!(second.kept, Kept::Session);
    assert_eq!(second.key, config_key(&core.config));
    assert_eq!(core.status(&first.key), 401);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    assert_eq!(Settings::load(&settings).unwrap().key, None);
    drop(c);
    let mut c = Connection::open(dirs(&root), None);
    assert!(!c.view().signed_in);
    assert_eq!(
        c.sign_in(&first.key, false).unwrap_err(),
        "core refused this key"
    );
    c.sign_in(&second.key, false).unwrap();

    // A failed save: the app works on the new key, the old kept key stays on disk, and the
    // connection says what to do; doing it recovers.
    c.sign_in(&second.key, true).unwrap();
    let config = dirs(&root).config;
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&config, std::fs::Permissions::from_mode(0o500)).unwrap();
        let third = c.rotate_key();
        std::fs::set_permissions(&config, std::fs::Permissions::from_mode(0o700)).unwrap();
        let third = third.unwrap_or_else(|r| panic!("rotation failed: {r:?}"));
        let Kept::NotSaved(why) = &third.kept else {
            panic!("a failed save was not reported: {:?}", third.kept)
        };
        assert!(why.contains("could not be kept on this device"), "{why}");
        assert!(!why.contains(&third.key));
        assert_eq!(c.view().error.as_deref(), Some(why.as_str()));
        assert_eq!(c.caller().call("GET", "/status", None).status, 200);
        assert_eq!(core.status(&second.key), 401);
        assert_eq!(
            Settings::load(&settings).unwrap().key,
            Some(second.key.clone())
        );
        drop(c);
        // After a restart the stale kept key is refused, as the connection said.
        let mut c = Connection::open(dirs(&root), None);
        assert_eq!(c.caller().call("GET", "/status", None).status, 401);
        c.sign_in(&third.key, true).unwrap();
        assert!(c.view().error.is_none());
        drop(c);
        let c = Connection::open(dirs(&root), None);
        assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    }
}

/// The app's own core saves the rotated key in its config file, where the app reads it when it
/// starts the core again.
#[test]
fn managed_rotation_survives_restarting_the_core() {
    let root = temp("rotate-managed");
    let out = Command::new("go")
        .args(["run", "./e2e/panelcore", "-prepare"])
        .arg(root.join("program"))
        .current_dir(next_dir())
        .stderr(Stdio::inherit())
        .output()
        .unwrap();
    assert!(out.status.success());
    let program: Value = serde_json::from_slice(&out.stdout).unwrap();
    let mut c = Connection::open(dirs(&root), None);
    adopt(&mut c, program["core"].as_str().unwrap());
    let config = dirs(&root).core.join("config.json");
    let old = config_key(&config);
    let base = c.view().target.unwrap();
    let rotated = rotate(&mut c);
    assert_eq!(rotated.kept, Kept::Core);
    assert_eq!(rotated.key, config_key(&config));
    assert_ne!(rotated.key, old);
    let direct = Client::new(&base, None);
    assert_eq!(direct.call(&old, "GET", "/status", None).status, 401);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    assert_eq!(Settings::load(&dirs(&root).settings()).unwrap().key, None);
    c.stop_core();
    drop(c);
    // The core starts again on the same port, with the key from its config file.
    let c = Connection::open(dirs(&root), None);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    assert_eq!(direct.call(&old, "GET", "/status", None).status, 401);
    drop(c);
}

/// Sets managed mode with program as the core program, and adopts it as the window's Adopt
/// would: the app runs only a core in its own store.
fn adopt(c: &mut Connection, program: &str) {
    c.change(Change {
        mode: Some(Mode::Managed),
        core_path: Some(program.into()),
        ..Change::default()
    })
    .unwrap();
    let (path, sha256, size) = c.prepare_adopt().unwrap();
    c.apply(CoreChange::Adopt { path, sha256, size }).unwrap();
}

/// panelcore's prepared core program, in root.
fn prepared_core(root: &Path) -> String {
    let out = Command::new("go")
        .args(["run", "./e2e/panelcore", "-prepare"])
        .arg(root.join("program"))
        .current_dir(next_dir())
        .stderr(Stdio::inherit())
        .output()
        .unwrap();
    assert!(out.status.success());
    let program: Value = serde_json::from_slice(&out.stdout).unwrap();
    program["core"].as_str().unwrap().to_string()
}

/// Start leaves a running core alone and Restart replaces it. The window's Start and the
/// tray's use Start, so a click that races a recovery keeps the recovered core.
#[test]
fn start_leaves_a_live_core_alone_and_restart_replaces_it() {
    let root = temp("start");
    let program = prepared_core(&root);
    let mut c = Connection::open(dirs(&root), None);
    adopt(&mut c, &program);
    let first = c.core_pid().expect("the adopted core runs");
    c.resume_core().unwrap();
    assert_eq!(c.core_pid(), Some(first), "Start replaced a running core");
    // The same measure sees a replaced core.
    c.restart_core().unwrap();
    let second = c.core_pid().expect("Restart runs a core");
    assert_ne!(second, first, "Restart kept the old core");
    c.stop_core();
    assert_eq!(c.core_pid(), None);
}

/// A core can exit while the app is still starting, before anything can act on its exit
/// callback. Asking the connection afterwards, as the app's setup does, schedules recovery.
#[test]
fn a_core_that_exits_before_anyone_watches_is_recovered() {
    use std::time::{Duration, Instant};
    let root = temp("early");
    let program = prepared_core(&root);
    // Like the app's callback before its state exists: it reaches no connection.
    let (exited, exit) = std::sync::mpsc::channel();
    let on_exit: OnExit = std::sync::Arc::new(move || {
        let _ = exited.send(());
    });
    let mut c = Connection::open(dirs(&root), Some(on_exit));
    adopt(&mut c, &program);
    assert_eq!(c.view().error, None, "the adopted core started");
    // It answered the app (a core counts as started only then), and now it is gone.
    let pid = c.core_pid().expect("the adopted core runs");
    assert!(Command::new("kill")
        .args(["-KILL", &pid.to_string()])
        .status()
        .unwrap()
        .success());
    exit.recv_timeout(Duration::from_secs(10))
        .expect("the core exited");

    let asked = Instant::now();
    let (_, due) = c.core_exited().expect("the early exit is recovered");
    let core = c.view().core.expect("a managed core view");
    assert_eq!(core.state, "recovering");
    assert!(
        core.detail
            .as_deref()
            .is_some_and(|d| d.contains("signal: 9")),
        "{:?}",
        core.detail
    );
    // The first of the 2, 5 and 15 second delays.
    let delay = due.duration_since(asked);
    assert!(
        delay >= Duration::from_millis(1900) && delay <= Duration::from_secs(3),
        "{delay:?}"
    );
    c.stop_core();
}

/// Over https with the given authority, a kept rotated key works after a restart.
#[test]
fn remote_rotation_keeps_the_key_across_a_restart() {
    let core = Core::start(true);
    let root = temp("rotate-remote");
    let mut c = Connection::open(dirs(&root), None);
    c.change(Change {
        mode: Some(Mode::Remote),
        url: Some(core.base.clone()),
        ca_file: core.ca.clone(),
        ..Change::default()
    })
    .unwrap();
    c.sign_in(&core.key, true).unwrap();
    let rotated = rotate(&mut c);
    assert_eq!(rotated.kept, Kept::Device);
    assert_eq!(rotated.key, config_key(&core.config));
    assert_eq!(core.status(&core.key), 401);
    drop(c);
    let mut c = Connection::open(dirs(&root), None);
    assert!(c.view().signed_in && c.view().remembered);
    assert_eq!(c.caller().call("GET", "/status", None).status, 200);
    assert_eq!(core.status(&rotated.key), 200);
}
