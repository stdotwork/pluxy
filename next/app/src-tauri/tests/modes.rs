//! The app's connection in each mode, against a real core with fake extensions
//! (next/e2e/panelcore, which needs Go). No window: these drive the same connection code the
//! window's commands use. Every core runs in a temporary folder on a random loopback port.

use pluxy_app::conn::{Change, Connection, Dirs, Mode};
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
    }
}

/// A core run by panelcore until this is dropped.
struct Core {
    child: Child,
    stdin: Option<ChildStdin>,
    base: String,
    key: String,
    ca: Option<String>,
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
        }
    }
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
    let program: Value = serde_json::from_slice(&out.stdout).unwrap();
    let program = program["core"].as_str().unwrap().to_string();

    let mut c = Connection::open(dirs(&root), None);
    assert_eq!(c.view().mode, None);
    c.change(Change {
        mode: Some(Mode::Managed),
        core_path: Some(program.clone()),
        ..Change::default()
    })
    .unwrap();
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
