//! The spawn service against real processes. The agent is the fixture program
//! (`examples/program_fixture`), which this test program runs when its first argument says so
//! (the hook in `program_tests.rs`); the host-crash check runs this test program as a host too
//! (the hook below), which it then kills. Every process a test starts is ended by it, by process
//! ID and only while its command line still names the test's folder.

use super::*;
use serde_json::Value;
use std::path::Path;

const AGENT: &str = "pluxy-fixture-agent";
const HOST: &str = "PLUXY_SPAWN_TEST_HOST";
const CANARY: &str = "pluxy-spawn-test-canary-0f1e2d";

#[used]
#[link_section = ".init_array"]
static CRASH_HOST: extern "C" fn() = {
    extern "C" fn run_host() {
        if let Some(dir) = std::env::var_os(HOST) {
            crash_host(Path::new(&dir));
        }
    }
    run_host
};

fn folder(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "pluxy-spawn-{name}-{}",
        crate::managed::random_key()
    ));
    std::fs::create_dir_all(&d).unwrap();
    std::fs::canonicalize(&d).unwrap()
}

fn opened(dir: &Path) -> Box<dyn Opened> {
    Box::new(Plain {
        executable: File::open(std::env::current_exe().unwrap()).unwrap(),
        cwd: File::open(dir).unwrap(),
    })
}

fn agent(dir: &Path, record: &Path, extra: &[&str], lifetime: Lifetime) -> Launch {
    let mut argv = vec![AGENT.to_string(), record.to_string_lossy().into_owned()];
    argv.extend(extra.iter().map(|s| s.to_string()));
    Launch {
        opened: opened(dir),
        named: std::env::current_exe().unwrap(),
        argv,
        lifetime,
        request: Request::default(),
        secrets: vec![],
    }
}

fn record(path: &Path, not: u64) -> Value {
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        if let Some(r) = std::fs::read(path)
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
            .filter(|r| r["pid"].as_u64().is_some_and(|p| p != not))
        {
            return r;
        }
        assert!(Instant::now() < deadline, "no record at {}", path.display());
        std::thread::sleep(Duration::from_millis(50));
    }
}

fn owned(pid: u64, marker: &Path) -> bool {
    pid > 0
        && unsafe { libc::kill(pid as libc::pid_t, 0) } == 0
        && std::fs::read(format!("/proc/{pid}/cmdline"))
            .is_ok_and(|c| String::from_utf8_lossy(&c).contains(&*marker.to_string_lossy()))
}

fn ended(pid: u64, marker: &Path) -> bool {
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        if !owned(pid, marker) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    false
}

/// Kills each process in pids that still runs as one of the test folder dir's.
fn clean(pids: &[u64], dir: &Path) {
    for &p in pids {
        if owned(p, dir) {
            unsafe { libc::kill(p as libc::pid_t, libc::SIGKILL) };
        }
    }
}

fn pid(r: &Value, k: &str) -> u64 {
    r[k].as_u64().unwrap_or(0)
}

fn no_secret(_: &str) -> Option<String> {
    None
}

fn limit(v: libc::rlim_t) -> Value {
    if v == libc::RLIM_INFINITY {
        json!("unlimited")
    } else {
        json!(v)
    }
}

#[test]
fn a_launch_request_is_checked_and_its_review_shows_no_value_it_must_not() {
    let secret_field = |f: &str| f == "key";
    let ok = json!({"executable": "/x", "argv": [], "cwd": "/", "lifetime": "managed",
        "environment": {"set": {"BASE_URL": "http://127.0.0.1:1"}, "inherit": ["CODEX_HOME"],
            "unset": ["LANG"], "secrets": {"API_KEY": "key"}}});
    let (lifetime, r) = parse(&ok, &secret_field).unwrap();
    assert_eq!(lifetime, Lifetime::Managed);
    assert_eq!(r.secrets.get("API_KEY").map(String::as_str), Some("key"));
    // Absent: a detached launch with no change to the session values.
    let (l, plain) = parse(&json!({}), &secret_field).unwrap();
    assert_eq!((l, plain), (Lifetime::Detached, Request::default()));
    for bad in [
        json!({"lifetime": "forever"}),
        json!({"environment": []}),
        json!({"environment": {"other": {}}}),
        json!({"environment": {"set": {"PLUXY_APP_TEST": "x"}}}),
        json!({"environment": {"inherit": ["LD_PRELOAD"]}}),
        json!({"environment": {"inherit": ["APPDIR"]}}),
        json!({"environment": {"set": {"1A": "x"}}}),
        json!({"environment": {"set": {"A": "x\u{0}y"}}}),
        json!({"environment": {"set": {"A": 1}}}),
        json!({"environment": {"set": {"A": "x"}, "inherit": ["A"]}}),
        json!({"environment": {"secrets": {"A": "note"}}}),
        json!({"environment": {"secrets": {"PLUXY_KEY": "key"}}}),
        // Credentials go through the vault's secrets, never inherited by name.
        json!({"environment": {"inherit": ["GITHUB_TOKEN"]}}),
        json!({"environment": {"inherit": ["anthropic_api_key"]}}),
        json!({"environment": {"inherit": ["TAURI_SIGNING_PRIVATE_KEY"]}}),
        json!({"environment": {"inherit": ["DB_PASSWORD"]}}),
        json!({"environment": {"inherit": ["CLIENT_SECRET"]}}),
        json!({"environment": {"inherit": ["AWS_SECRET_ACCESS_KEY"]}}),
        // Loader- and interpreter-startup injection, like LD_*.
        json!({"environment": {"set": {"NODE_OPTIONS": "--require /x"}}}),
        json!({"environment": {"set": {"GLIBC_TUNABLES": "x"}}}),
        json!({"environment": {"set": {"GCONV_PATH": "/x"}}}),
        json!({"environment": {"set": {"BASH_ENV": "/x"}}}),
        json!({"environment": {"set": {"ENV": "/x"}}}),
        json!({"environment": {"inherit": ["PYTHONSTARTUP"]}}),
        json!({"environment": {"inherit": ["PERL5OPT"]}}),
    ] {
        assert!(parse(&bad, &secret_field).is_err(), "{bad} was taken");
    }
    // A socket path agents need for git is inherited by name; so is a client's config folder.
    let (_, ok_names) = parse(
        &json!({"environment": {"inherit": ["SSH_AUTH_SOCK", "CODEX_HOME"]}}),
        &secret_field,
    )
    .unwrap();
    assert_eq!(ok_names.inherit, vec!["CODEX_HOME", "SSH_AUTH_SOCK"]);
    // Names, set values and the secret's label; never an inherited value or a secret's.
    let review = r.review(&|f| format!("label for {f}"));
    let text = format!("{review:?}");
    for want in [
        "BASE_URL",
        "http://127.0.0.1:1",
        "CODEX_HOME",
        "inherited",
        "LANG",
        "removed",
        "API_KEY",
        "label for key",
    ] {
        assert!(text.contains(want), "{want} not in {text}");
    }
    // The revision follows the request and the lifetime, nothing else.
    let again = parse(&ok, &secret_field).unwrap().1;
    assert_eq!(r.revision(lifetime), again.revision(lifetime));
    assert_ne!(r.revision(lifetime), r.revision(Lifetime::Detached));
    let mut other = r.clone();
    other
        .set
        .insert("BASE_URL".into(), "http://127.0.0.1:2".into());
    assert_ne!(r.revision(lifetime), other.revision(lifetime));
}

#[test]
fn the_launch_environment_holds_session_values_and_what_was_asked_only() {
    let snap = Snapshot::of(
        &[
            ("HOME", "/home/me"),
            ("PATH", "/tmp/.mount_x/usr/bin:/usr/bin:/tmp/.mount_x"),
            ("XDG_DATA_DIRS", "/tmp/.mount_x/usr/share:/usr/share"),
            ("LANG", "C.UTF-8"),
            ("LC_TIME", "C"),
            ("DISPLAY", ":1"),
            ("APPDIR", "/tmp/.mount_x"),
            ("APPIMAGE", "/x.AppImage"),
            ("LD_LIBRARY_PATH", "/tmp/.mount_x/usr/lib"),
            ("PLUXY_TEST_TRUST_NONCE", CANARY),
            ("OPENAI_API_KEY", CANARY),
            ("CODEX_HOME", "/home/me/.codex-alt"),
            ("WEBKIT_DISABLE_DMABUF_RENDERER", "1"),
        ],
        core_limit(),
    );
    let (_, r) = parse(
        &json!({"environment": {"set": {"BASE_URL": "u"}, "inherit": ["CODEX_HOME", "ABSENT"],
            "unset": ["LANG"], "secrets": {"API_KEY": "key"}}}),
        &|f| f == "key",
    )
    .unwrap();
    let env = environment(&snap, &r, &[("API_KEY".into(), "the-key".into())]);
    let got: Vec<(String, String)> = env
        .into_iter()
        .map(|(k, v)| (k, v.to_string_lossy().into_owned()))
        .collect();
    let want: Vec<(String, String)> = [
        ("API_KEY", "the-key"),
        ("BASE_URL", "u"),
        ("CODEX_HOME", "/home/me/.codex-alt"),
        ("DISPLAY", ":1"),
        ("HOME", "/home/me"),
        ("LC_TIME", "C"),
        ("PATH", "/usr/bin"),
        ("XDG_DATA_DIRS", "/usr/share"),
    ]
    .iter()
    .map(|(k, v)| (k.to_string(), v.to_string()))
    .collect();
    assert_eq!(got, want);
}

#[test]
fn a_detached_launch_leaves_the_app_with_only_what_it_was_given() {
    let dir = folder("detached");
    // A descriptor of the app's that is not close-on-exec: it must not reach the launch.
    let mut fds = [0; 2];
    assert_eq!(unsafe { libc::pipe(fds.as_mut_ptr()) }, 0);
    let rec = dir.join("agent");
    let mut l = agent(&dir, &rec, &[], Lifetime::Detached);
    l.request.set.insert("FIXTURE_SET".into(), "set".into());
    l.request.secrets.insert("FIXTURE_KEY".into(), "key".into());
    l.secrets = vec![("FIXTURE_KEY".into(), "s-1".into())];
    let value = |id: &str| (id == "s-1").then(|| CANARY.to_string());
    detached(&l, &value).unwrap();
    let r = record(&rec, 0);
    let (p, child) = (pid(&r, "pid"), pid(&r, "child"));
    unsafe {
        libc::close(fds[0]);
        libc::close(fds[1]);
    }
    let checks = || {
        assert_eq!(r["sid"].as_u64(), Some(p), "{r}");
        assert_eq!(r["pgid"].as_u64(), Some(p), "{r}");
        assert_ne!(
            r["ppid"].as_u64(),
            Some(u64::from(std::process::id())),
            "{r}"
        );
        assert_eq!(
            r["fds"],
            json!([{"fd": 0, "target": "/dev/null"}, {"fd": 1, "target": "/dev/null"},
                {"fd": 2, "target": "/dev/null"}]),
        );
        let mut argv = vec![std::env::current_exe()
            .unwrap()
            .to_string_lossy()
            .into_owned()];
        argv.extend(l.argv.iter().cloned());
        assert_eq!(r["argv"], json!(argv));
        assert_eq!(r["cwd"], json!(dir));
        assert_eq!(r["env"]["FIXTURE_KEY"], CANARY);
        assert_eq!(r["env"]["FIXTURE_SET"], "set");
        assert_eq!(
            r["core"],
            json!([1, 1]),
            "a key holder may write core files, or run the kernel's core dump handler"
        );
        for (k, _) in r["env"].as_object().unwrap() {
            assert!(
                BASE.contains(&k.as_str()) || k.starts_with("LC_") || k.starts_with("FIXTURE_"),
                "{k} was passed on"
            );
        }
        // A secret that is gone refuses the launch; nothing starts.
        let gone = dir.join("gone");
        let mut g = agent(&dir, &gone, &[], Lifetime::Detached);
        g.secrets = vec![("FIXTURE_KEY".into(), "s-2".into())];
        assert!(detached(&g, &value).unwrap_err().contains("removed"));
        std::thread::sleep(Duration::from_millis(300));
        assert!(!gone.exists());
        // Without a secret, the launch keeps the limits the app started with.
        let plain = dir.join("plain");
        detached(&agent(&dir, &plain, &[], Lifetime::Detached), &no_secret).unwrap();
        let q = record(&plain, 0);
        let start = snapshot().core;
        assert_eq!(
            q["core"],
            json!([limit(start.rlim_cur), limit(start.rlim_max)])
        );
        clean(&[pid(&q, "pid"), pid(&q, "child")], &dir);
    };
    let r2 = std::panic::catch_unwind(std::panic::AssertUnwindSafe(checks));
    clean(&[p, child], &dir);
    let _ = std::fs::remove_dir_all(&dir);
    if let Err(e) = r2 {
        std::panic::resume_unwind(e);
    }
}

#[test]
fn a_managed_runtime_is_the_registrys_and_only_its_package_controls_it() {
    let dir = folder("managed");
    let file = dir.join("runtimes.json");
    let rt = Runtimes::open(Some(file.clone()), Box::new(no_secret));
    let (ra, rb) = (dir.join("a"), dir.join("b"));
    let a = rt
        .start("pluxy/one", agent(&dir, &ra, &[], Lifetime::Managed))
        .unwrap();
    let b = rt
        .start("pluxy/one", agent(&dir, &rb, &[], Lifetime::Managed))
        .unwrap();
    let (ida, idb) = (
        a["runtime"].as_str().unwrap().to_string(),
        b["runtime"].as_str().unwrap().to_string(),
    );
    let (x, y) = (record(&ra, 0), record(&rb, 0));
    let mut all = vec![
        pid(&x, "pid"),
        pid(&x, "child"),
        pid(&y, "pid"),
        pid(&y, "child"),
    ];
    let checks = || {
        let p = pid(&x, "pid");
        assert_eq!(a["pid"].as_u64(), Some(p));
        assert_eq!(x["pgid"].as_u64(), Some(p), "{x}");
        assert_eq!(
            x["ppid"].as_u64(),
            Some(u64::from(std::process::id())),
            "{x}"
        );
        assert!(std::fs::read_to_string(&file).unwrap().contains(&ida));
        assert_eq!(rt.status("pluxy/one", &ida).unwrap()["state"], "running");
        assert_eq!(rt.list("pluxy/one").len(), 2);
        // Another package sees and controls none of them.
        assert!(rt.list("pluxy/two").is_empty());
        assert!(rt.status("pluxy/two", &ida).is_err());
        assert!(rt.stop("pluxy/two", &ida).is_err());
        assert!(rt.restart("pluxy/two", &ida).is_err());
        assert!(owned(p, &ra));
        // Restart replaces its whole group, and only its group.
        let r = rt.restart("pluxy/one", &ida).unwrap();
        let fresh = record(&ra, p);
        all.extend([pid(&fresh, "pid"), pid(&fresh, "child")]);
        assert_eq!(r["pid"], fresh["pid"]);
        assert_eq!(r["restarts"], 1);
        assert!(ended(p, &ra) && ended(pid(&x, "child"), &ra));
        assert!(owned(pid(&y, "pid"), &rb) && owned(pid(&y, "child"), &rb));
        // Stop ends it (leader and member), reaped, and nothing else.
        let s = rt.stop("pluxy/one", &ida).unwrap();
        assert_eq!(s["state"], "exited");
        assert!(ended(pid(&fresh, "pid"), &ra) && ended(pid(&fresh, "child"), &ra));
        assert!(owned(pid(&y, "pid"), &rb) && owned(pid(&y, "child"), &rb));
        assert!(!std::fs::read_to_string(&file).unwrap().contains(&ida));
        // The app's quit stops the rest; nothing starts after.
        rt.stop_all();
        assert!(ended(pid(&y, "pid"), &rb) && ended(pid(&y, "child"), &rb));
        assert_eq!(rt.status("pluxy/one", &idb).unwrap()["state"], "exited");
        assert!(rt
            .start(
                "pluxy/one",
                agent(&dir, &dir.join("c"), &[], Lifetime::Managed)
            )
            .is_err());
        assert_eq!(std::fs::read_to_string(&file).unwrap(), "[]");
    };
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(checks));
    rt.stop_all();
    clean(&all, &dir);
    let _ = std::fs::remove_dir_all(&dir);
    if let Err(e) = r {
        std::panic::resume_unwind(e);
    }
}

/// The host-crash check's host: starts a managed runtime whose leader leaves an orphan, and a
/// detached agent, says so, and waits to be killed.
fn crash_host(dir: &Path) -> ! {
    let rt = Runtimes::open(Some(dir.join("runtimes.json")), Box::new(no_secret));
    let leader = dir.join("leader");
    rt.start(
        "pluxy/one",
        agent(dir, &leader, &["orphan"], Lifetime::Managed),
    )
    .expect("the runtime");
    detached(
        &agent(dir, &dir.join("detached"), &[], Lifetime::Detached),
        &no_secret,
    )
    .expect("the agent");
    let _ = std::fs::write(dir.join("ready"), "");
    loop {
        std::thread::sleep(Duration::from_secs(3600));
    }
}

#[test]
fn a_crashed_host_leaves_the_documented_orphans_and_its_next_start_does_not_signal_them() {
    let dir = folder("crash");
    let mut host = Command::new(std::env::current_exe().unwrap())
        .env(HOST, &dir)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let leader = record(&dir.join("leader"), 0);
    let agent = record(&dir.join("detached"), 0);
    let orphan_file = dir.join("leader.orphan");
    let deadline = Instant::now() + Duration::from_secs(20);
    while !(dir.join("ready").exists() && orphan_file.exists()) {
        assert!(Instant::now() < deadline, "the host never got ready");
        std::thread::sleep(Duration::from_millis(50));
    }
    std::thread::sleep(Duration::from_millis(100));
    let orphan: u64 = std::fs::read_to_string(&orphan_file)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    let all = [
        pid(&leader, "pid"),
        pid(&leader, "child"),
        orphan,
        pid(&agent, "pid"),
        pid(&agent, "child"),
    ];
    let checks = || {
        // The host dies without stopping anything.
        host.kill().unwrap();
        host.wait().unwrap();
        // The runtime's leader got its parent-death signal; its group's member, and the
        // descendant in a session of its own that ignores SIGTERM, live on: the documented limit.
        assert!(
            ended(pid(&leader, "pid"), &dir),
            "the leader outlived the host"
        );
        assert!(owned(orphan, &dir), "the orphan did not outlive the host");
        assert!(owned(pid(&leader, "child"), &dir));
        assert!(
            owned(pid(&agent, "pid"), &dir),
            "the detached agent did not outlive the host"
        );
        // The next start reports the runtime as unknown and never signals it.
        let rt = Runtimes::open(Some(dir.join("runtimes.json")), Box::new(no_secret));
        let list = rt.list("pluxy/one");
        assert_eq!(list.len(), 1, "{list:?}");
        assert_eq!(list[0]["state"], "unknown");
        assert_eq!(list[0]["pid"], leader["pid"]);
        let id = list[0]["runtime"].as_str().unwrap().to_string();
        assert!(rt
            .stop("pluxy/one", &id)
            .unwrap_err()
            .contains("no longer controls"));
        assert!(rt
            .restart("pluxy/one", &id)
            .unwrap_err()
            .contains("no longer controls"));
        rt.stop_all();
        std::thread::sleep(Duration::from_millis(300));
        assert!(owned(orphan, &dir) && owned(pid(&leader, "child"), &dir));
        // Its record now names nothing running.
        assert_eq!(
            std::fs::read_to_string(dir.join("runtimes.json")).unwrap(),
            "[]"
        );
    };
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(checks));
    let _ = host.kill();
    let _ = host.wait();
    // The harness ends the processes it knows of.
    clean(&all, &dir);
    for p in all {
        assert!(ended(p, &dir), "process {p} was not cleaned up");
    }
    let _ = std::fs::remove_dir_all(&dir);
    if let Err(e) = r {
        std::panic::resume_unwind(e);
    }
}

/// An opened launch whose recheck waits until the test lets it go: a restart held between its
/// stop and its new start.
struct Gated {
    plain: Plain,
    gate: Mutex<std::sync::mpsc::Receiver<()>>,
    held: std::sync::mpsc::Sender<()>,
}

impl Opened for Gated {
    fn executable(&self) -> &File {
        &self.plain.executable
    }
    fn cwd(&self) -> &File {
        &self.plain.cwd
    }
    fn recheck(&self) -> Result<(), String> {
        let _ = self.held.send(());
        let _ = self
            .gate
            .lock()
            .unwrap()
            .recv_timeout(Duration::from_secs(10));
        Ok(())
    }
}

#[test]
fn a_disabled_or_removed_packages_runtimes_are_retired_and_never_start_again() {
    let dir = folder("closed");
    let rt = Arc::new(Runtimes::open(None, Box::new(no_secret)));
    let (ra, rb) = (dir.join("a"), dir.join("b"));
    let a = rt
        .start("pluxy/one", agent(&dir, &ra, &[], Lifetime::Managed))
        .unwrap();
    let ida = a["runtime"].as_str().unwrap().to_string();
    let x = record(&ra, 0);
    // A restart held between its stop and its new start, while the package is disabled.
    let (release, gate) = std::sync::mpsc::channel();
    let (held, holding) = std::sync::mpsc::channel();
    let mut gated = agent(&dir, &rb, &[], Lifetime::Managed);
    gated.opened = Box::new(Gated {
        plain: Plain {
            executable: File::open(std::env::current_exe().unwrap()).unwrap(),
            cwd: File::open(&dir).unwrap(),
        },
        gate: Mutex::new(gate),
        held,
    });
    let _ = release.send(());
    let b = rt.start("pluxy/one", gated).unwrap();
    let idb = b["runtime"].as_str().unwrap().to_string();
    holding.recv().unwrap();
    let y = record(&rb, 0);
    let mut all = vec![
        pid(&x, "pid"),
        pid(&x, "child"),
        pid(&y, "pid"),
        pid(&y, "child"),
    ];
    let checks = || {
        let racer = {
            let (rt, idb) = (Arc::clone(&rt), idb.clone());
            std::thread::spawn(move || rt.restart("pluxy/one", &idb))
        };
        holding.recv().unwrap();
        rt.close("pluxy/one", false);
        let _ = release.send(());
        let r = racer.join().unwrap();
        assert!(
            r.is_err(),
            "a restart racing the disable started again: {r:?}"
        );
        std::thread::sleep(Duration::from_millis(300));
        if let Ok(again) = std::fs::read(&rb) {
            let again: Value = serde_json::from_slice(&again).unwrap_or(Value::Null);
            all.extend([pid(&again, "pid"), pid(&again, "child")]);
            assert!(
                pid(&again, "pid") == pid(&y, "pid") || ended(pid(&again, "pid"), &rb),
                "the raced restart left a runtime running"
            );
        }
        // Disabled: both end, and neither starts again, nor does a new one.
        assert!(ended(pid(&x, "pid"), &ra) && ended(pid(&x, "child"), &ra));
        assert!(ended(pid(&y, "pid"), &rb) && ended(pid(&y, "child"), &rb));
        assert!(rt.restart("pluxy/one", &ida).is_err());
        assert!(rt
            .start(
                "pluxy/one",
                agent(&dir, &dir.join("c"), &[], Lifetime::Managed)
            )
            .is_err());
        // Enabled again: the old launches stay retired (a new launch is confirmed afresh).
        rt.reopen("pluxy/one");
        assert!(
            rt.restart("pluxy/one", &ida).is_err(),
            "an old launch restarted after re-enable"
        );
        assert_eq!(rt.status("pluxy/one", &ida).unwrap()["state"], "exited");
        // Removed: its entries go.
        rt.close("pluxy/one", true);
        assert!(rt.list("pluxy/one").is_empty());
    };
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(checks));
    let _ = release.send(());
    rt.stop_all();
    clean(&all, &dir);
    let _ = std::fs::remove_dir_all(&dir);
    if let Err(e) = r {
        std::panic::resume_unwind(e);
    }
}

#[test]
fn unknown_runtimes_do_not_fill_a_packages_room() {
    let dir = folder("unknown");
    let file = dir.join("runtimes.json");
    let left: Vec<Value> = (0..MAX_RUNTIMES)
        .map(|i| {
            json!({"runtime": format!("rt-{i}"), "package": "pluxy/one", "pid": 0,
            "executable": "/x", "argv": [], "started": 0})
        })
        .collect();
    std::fs::write(&file, json!(left).to_string()).unwrap();
    let rt = Runtimes::open(Some(file), Box::new(no_secret));
    assert_eq!(rt.list("pluxy/one").len(), MAX_RUNTIMES);
    let rec = dir.join("a");
    let r = rt.start("pluxy/one", agent(&dir, &rec, &[], Lifetime::Managed));
    let mut pids = vec![];
    if r.is_ok() {
        let x = record(&rec, 0);
        pids = vec![pid(&x, "pid"), pid(&x, "child")];
    }
    rt.stop_all();
    clean(&pids, &dir);
    let _ = std::fs::remove_dir_all(&dir);
    assert!(r.is_ok(), "{r:?}");
}
