//! The spawn service against real processes. The agent is the fixture program
//! (`examples/program_fixture`), which this test program runs when its first argument says so
//! (the hook in `program_tests.rs`); the host-crash check runs this test program as a host too
//! (the hook below), which it then kills. Every process a test starts is ended by it: on Linux by
//! process ID and only while its command line still names the test's folder; elsewhere, where a
//! process ID alone is all a test could name, through a file named `stop` in the test's folder,
//! which the agents wait for (the fixture's `until_stop`). The lifetime checks on every platform
//! are in `program_tests.rs` (`lifetime_*`).

use super::*;
use serde_json::Value;
use std::path::Path;

const AGENT: &str = "pluxy-fixture-agent";
#[cfg(target_os = "linux")]
const HOST: &str = "PLUXY_SPAWN_TEST_HOST";
const CANARY: &str = "pluxy-spawn-test-canary-0f1e2d";

// Linux only, with the check it serves: the parent-death signal and the orphans it leaves are
// Linux's (`lifetime_a_host_crash_*` in `program_tests.rs` checks a host crash everywhere).
#[cfg(target_os = "linux")]
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

/// Folder dir, opened as a launch's cwd is (on Windows a folder opens only with backup
/// semantics).
fn open_dir(dir: &Path) -> File {
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        std::fs::OpenOptions::new()
            .read(true)
            .custom_flags(windows_sys::Win32::Storage::FileSystem::FILE_FLAG_BACKUP_SEMANTICS)
            .open(dir)
            .unwrap()
    }
    #[cfg(not(windows))]
    File::open(dir).unwrap()
}

fn opened(dir: &Path) -> Box<dyn Opened> {
    Box::new(Plain {
        executable: File::open(std::env::current_exe().unwrap()).unwrap(),
        cwd: open_dir(dir),
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
        stdin: None,
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

#[cfg(target_os = "linux")]
fn owned(pid: u64, marker: &Path) -> bool {
    pid > 0
        && unsafe { libc::kill(pid as libc::pid_t, 0) } == 0
        && std::fs::read(format!("/proc/{pid}/cmdline"))
            .is_ok_and(|c| String::from_utf8_lossy(&c).contains(&*marker.to_string_lossy()))
}

/// The counts agent pid writes while it waits, under the record named marker (the fixture's
/// `until_stop`).
#[cfg(not(target_os = "linux"))]
fn beats(pid: u64, marker: &Path) -> PathBuf {
    PathBuf::from(format!("{}.{pid}.beat", marker.display()))
}

/// The count in beat file f.
#[cfg(not(target_os = "linux"))]
fn count(f: &Path) -> Option<u64> {
    std::fs::read_to_string(f).ok()?.trim().parse().ok()
}

/// Whether agent pid, of the record named marker, runs: its count advances (one every 50 ms).
/// There is no command line to read here, and nothing is signalled.
#[cfg(not(target_os = "linux"))]
fn owned(pid: u64, marker: &Path) -> bool {
    let f = beats(pid, marker);
    let deadline = Instant::now() + Duration::from_secs(2);
    let mut first = count(&f);
    while pid > 0 && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
        match (first, count(&f)) {
            (Some(a), Some(b)) if b > a => return true,
            (None, now) => first = now,
            _ => {}
        }
    }
    false
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
#[cfg(target_os = "linux")]
fn clean(pids: &[u64], dir: &Path) {
    for &p in pids {
        if owned(p, dir) {
            unsafe { libc::kill(p as libc::pid_t, libc::SIGKILL) };
        }
    }
}

/// Ends the agents of the test folder dir, which each exit once a file named `stop` is there,
/// and waits until no count there advances. Nothing is signalled.
#[cfg(not(target_os = "linux"))]
fn clean(_pids: &[u64], dir: &Path) {
    let _ = std::fs::write(dir.join("stop"), "");
    let counts = || -> Vec<Option<u64>> {
        let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
            .map(|d| d.filter_map(|e| e.ok()).map(|e| e.path()).collect())
            .unwrap_or_default();
        files.retain(|f| f.extension().is_some_and(|x| x == "beat"));
        files.sort();
        files.iter().map(|f| count(f)).collect()
    };
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut before = counts();
    while Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(300));
        let now = counts();
        if now == before {
            return;
        }
        before = now;
    }
}

/// Starts detached launch l for package: true once it runs. On Windows a detached launch runs
/// only where the job the tests run in lets it leave (`owned::windows::breakaway`; GitHub's hosted
/// runners do not), and is refused before it runs otherwise (`NO_BREAKAWAY`): false.
fn let_go(l: &Launch, package: &str, values: &dyn Values) -> bool {
    let r = detached(l, package, values);
    #[cfg(windows)]
    {
        use crate::owned::windows::{breakaway, NO_BREAKAWAY};
        if r.as_ref().is_err_and(|e| e.contains(NO_BREAKAWAY)) {
            return false;
        }
        assert!(
            breakaway().is_ok(),
            "a detached launch was not refused in a job it may not leave: {r:?}"
        );
    }
    r.unwrap();
    true
}

fn pid(r: &Value, k: &str) -> u64 {
    r[k].as_u64().unwrap_or(0)
}

/// A test's `Values`: each secret's value from its function (package, secret ID), with no
/// revision, and never refused right before the spawn.
struct By<F>(F);

impl<F: Fn(&str, &str) -> Result<String, String> + Send + Sync> Values for By<F> {
    fn value(&self, package: &str, id: &str) -> Result<(String, Option<u64>), String> {
        (self.0)(package, id).map(|v| (v, None))
    }
    fn still(&self, _: &[Given]) -> Result<(), String> {
        Ok(())
    }
}

/// Knows no secret.
fn none(_: &str, _: &str) -> Result<String, String> {
    Err("no secret".into())
}

#[cfg(target_os = "linux")]
fn limit(v: libc::rlim_t) -> Value {
    if v == libc::RLIM_INFINITY {
        json!("unlimited")
    } else {
        json!(v)
    }
}

#[test]
fn a_managed_terminal_launch_is_refused_and_a_managed_direct_launch_is_not() {
    let none = |_: &str| false;
    let direct =
        json!({"executable": "/bin/agent", "argv": ["agent"], "cwd": "/", "lifetime": "managed"});
    assert_eq!(parse(&direct, &none).unwrap().0, Lifetime::Managed);
    // A launch naming the program a terminal runs is a terminal launch.
    let terminal = json!({"executable": "/usr/bin/xterm", "argv": ["-e", "/bin/agent"], "cwd": "/",
        "program": "/bin/agent", "lifetime": "managed"});
    let refused = parse(&terminal, &none).unwrap_err();
    assert!(
        refused.contains("terminal launch is detached only"),
        "{refused}"
    );
    // Detached, stated or by default, is what a terminal launch may be.
    for lifetime in [json!("detached"), Value::Null] {
        let mut ok = terminal.clone();
        match lifetime {
            Value::Null => {
                ok.as_object_mut().unwrap().remove("lifetime");
            }
            l => ok["lifetime"] = l,
        }
        assert_eq!(parse(&ok, &none).unwrap().0, Lifetime::Detached);
    }
}

/// A name is the same name whatever its case where the environment ignores case (Windows): named
/// twice, in one list or across set, inherit and secrets, the launch is refused, so its review
/// shows each variable it gets once. Elsewhere `Path` and `PATH` are two variables.
#[test]
fn a_name_given_twice_as_the_environment_compares_names_is_refused() {
    let secret_field = |f: &str| f == "key";
    for twice in [
        json!({"environment": {"set": {"Path": "a", "PATH": "b"}}}),
        json!({"environment": {"set": {"Path": "a"}, "inherit": ["PATH"]}}),
        json!({"environment": {"set": {"Path": "a"}, "secrets": {"PATH": "key"}}}),
        json!({"environment": {"inherit": ["Path", "PATH"]}}),
        json!({"environment": {"unset": ["Path", "PATH"]}}),
    ] {
        let r = parse(&twice, &secret_field);
        if cfg!(windows) {
            let e = r.unwrap_err();
            assert!(e.contains("more than once"), "{twice}: {e}");
        } else {
            assert!(r.is_ok(), "{twice}: {r:?}");
        }
    }
    // The same name twice is refused everywhere.
    let e = parse(
        &json!({"environment": {"set": {"PATH": "a"}, "inherit": ["PATH"]}}),
        &secret_field,
    )
    .unwrap_err();
    assert!(e.contains("more than once"), "{e}");
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
    // Each name below is tried on its own. A failed assert would stop at the first name a launch
    // takes, so every name a launch takes is collected and reported together.
    let mut taken = Vec::new();
    for bad in [
        // .NET's startup hook and profiler variables: code loaded into a .NET program at start.
        json!({"environment": {"set": {"DOTNET_STARTUP_HOOKS": "/x.dll"}}}),
        json!({"environment": {"set": {"COR_PROFILER": "{x}"}}}),
        json!({"environment": {"inherit": ["CORECLR_PROFILER_PATH_64"]}}),
        json!({"environment": {"set": {"COR_PROFILER_PATH_64": "/x.so"}}}),
        json!({"environment": {"set": {"DOTNET_PROFILER": "{x}"}}}),
        json!({"environment": {"inherit": ["DOTNET_PROFILER_PATH_64"]}}),
        json!({"environment": {"set": {"DOTNET_ADDITIONAL_DEPS": "/x"}}}),
        // The module search paths of Python, Node.js and Ruby.
        json!({"environment": {"inherit": ["PYTHONPATH"]}}),
        json!({"environment": {"set": {"pythonpath": "/x"}}}),
        json!({"environment": {"set": {"PYTHONHOME": "/x"}}}),
        json!({"environment": {"set": {"NODE_PATH": "/x"}}}),
        json!({"environment": {"set": {"RUBYLIB": "/x"}}}),
        // The legacy COMPlus_ spelling of the runtime's settings, in any case.
        json!({"environment": {"set": {"COMPlus_ENABLE_PROFILING": "1"}}}),
        json!({"environment": {"inherit": ["compLUS_PROFILER"]}}),
    ] {
        if parse(&bad, &secret_field).is_ok() {
            taken.push(bad.to_string());
        }
    }
    assert!(taken.is_empty(), "taken: {}", taken.join(", "));
    // The prefixes are not wider than their families: ordinary names of these runtimes pass.
    let (_, names) = parse(
        &json!({"environment": {"inherit": ["DOTNET_ROOT", "NODE_ENV", "PYTHON_VERSION"]}}),
        &secret_field,
    )
    .unwrap();
    assert_eq!(
        names.inherit,
        vec!["DOTNET_ROOT", "NODE_ENV", "PYTHON_VERSION"]
    );
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

// Linux only: its session values (`BASE`) and the AppImage's folders are Linux's.
#[cfg(target_os = "linux")]
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

// Linux only: sessions, descriptors read from /proc and core limits (the lifetime checks in
// `program_tests.rs` check what a launch gets everywhere).
#[cfg(target_os = "linux")]
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
    let value = By(|_: &str, id: &str| {
        (id == "s-1")
            .then(|| CANARY.to_string())
            .ok_or_else(|| "it was removed from the vault".to_string())
    });
    detached(&l, "p", &value).unwrap();
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
        assert!(detached(&g, "p", &value).unwrap_err().contains("removed"));
        std::thread::sleep(Duration::from_millis(300));
        assert!(!gone.exists());
        // Without a secret, the launch keeps the limits the app started with.
        let plain = dir.join("plain");
        detached(
            &agent(&dir, &plain, &[], Lifetime::Detached),
            "p",
            &By(none),
        )
        .unwrap();
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
    let rt = Runtimes::open(Some(file.clone()), Box::new(By(none)));
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
        // A group of its own, and the app's own child (on Windows its job: see the lifetime checks).
        #[cfg(unix)]
        {
            assert_eq!(x["pgid"].as_u64(), Some(p), "{x}");
            assert_eq!(
                x["ppid"].as_u64(),
                Some(u64::from(std::process::id())),
                "{x}"
            );
        }
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

#[test]
fn a_restart_resolves_its_secrets_again_for_its_package_and_a_refusal_refuses_it() {
    let dir = folder("revoked");
    let revoked = Arc::new(AtomicBool::new(false));
    let asked = Arc::new(Mutex::new(Vec::new()));
    let rt = {
        let (revoked, asked) = (Arc::clone(&revoked), Arc::clone(&asked));
        Runtimes::open(
            None,
            Box::new(By(move |package: &str, id: &str| {
                asked
                    .lock()
                    .unwrap()
                    .push((package.to_string(), id.to_string()));
                if revoked.load(Ordering::SeqCst) {
                    return Err("the client key was deleted on core".into());
                }
                Ok(CANARY.to_string())
            })),
        )
    };
    let rec = dir.join("a");
    let mut l = agent(&dir, &rec, &[], Lifetime::Managed);
    l.request.secrets.insert("FIXTURE_KEY".into(), "key".into());
    l.secrets = vec![("FIXTURE_KEY".into(), "s-1".into())];
    let a = rt.start("pluxy/one", l).unwrap();
    let id = a["runtime"].as_str().unwrap().to_string();
    let x = record(&rec, 0);
    let mut all = vec![pid(&x, "pid"), pid(&x, "child")];
    let checks = || {
        assert_eq!(x["env"]["FIXTURE_KEY"], CANARY);
        // A restart with the secret still given: resolved again, for the runtime's package.
        rt.restart("pluxy/one", &id).unwrap();
        let y = record(&rec, pid(&x, "pid"));
        all.extend([pid(&y, "pid"), pid(&y, "child")]);
        assert_eq!(y["env"]["FIXTURE_KEY"], CANARY);
        // Refused now: the restart is refused, and nothing earlier stands for the secret.
        revoked.store(true, Ordering::SeqCst);
        let e = rt.restart("pluxy/one", &id).unwrap_err();
        assert!(
            e.contains("FIXTURE_KEY") && e.contains("deleted on core"),
            "{e}"
        );
        assert!(ended(pid(&y, "pid"), &rec) && ended(pid(&y, "child"), &rec));
        std::thread::sleep(Duration::from_millis(300));
        let now: Value = serde_json::from_slice(&std::fs::read(&rec).unwrap()).unwrap();
        assert_eq!(now["pid"], y["pid"], "the refused restart started it again");
        assert_eq!(rt.status("pluxy/one", &id).unwrap()["state"], "exited");
        let want = ("pluxy/one".to_string(), "s-1".to_string());
        assert_eq!(
            *asked.lock().unwrap(),
            vec![want.clone(), want.clone(), want]
        );
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
#[cfg(target_os = "linux")]
fn crash_host(dir: &Path) -> ! {
    let rt = Runtimes::open(Some(dir.join("runtimes.json")), Box::new(By(none)));
    let leader = dir.join("leader");
    rt.start(
        "pluxy/one",
        agent(dir, &leader, &["orphan"], Lifetime::Managed),
    )
    .expect("the runtime");
    detached(
        &agent(dir, &dir.join("detached"), &[], Lifetime::Detached),
        "pluxy/one",
        &By(none),
    )
    .expect("the agent");
    let _ = std::fs::write(dir.join("ready"), "");
    loop {
        std::thread::sleep(Duration::from_secs(3600));
    }
}

// Linux only: the parent-death signal and the orphans it leaves are Linux's
// (`lifetime_a_host_crash_*` in `program_tests.rs` checks a host crash on every platform).
#[cfg(target_os = "linux")]
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
        let rt = Runtimes::open(Some(dir.join("runtimes.json")), Box::new(By(none)));
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
    let rt = Arc::new(Runtimes::open(None, Box::new(By(none))));
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
            cwd: open_dir(&dir),
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
    let rt = Runtimes::open(Some(file), Box::new(By(none)));
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

#[test]
fn the_apps_quit_leaves_no_start_under_way_to_spawn_after_it() {
    let dir = folder("quit");
    // A resolver held until the test lets it go: a start past its first checks, not spawned yet.
    let (release, gate) = std::sync::mpsc::channel::<()>();
    let (held, holding) = std::sync::mpsc::channel::<()>();
    let gate = Mutex::new(gate);
    let rt = Arc::new(Runtimes::open(
        None,
        Box::new(By(move |_: &str, _: &str| {
            let _ = held.send(());
            let _ = gate.lock().unwrap().recv_timeout(Duration::from_secs(10));
            Ok(CANARY.to_string())
        })),
    ));
    let rec = dir.join("a");
    let mut l = agent(&dir, &rec, &[], Lifetime::Managed);
    l.request.secrets.insert("FIXTURE_KEY".into(), "key".into());
    l.secrets = vec![("FIXTURE_KEY".into(), "s-1".into())];
    let starting = {
        let rt = Arc::clone(&rt);
        std::thread::spawn(move || rt.start("pluxy/one", l))
    };
    holding.recv().unwrap();
    rt.stop_all();
    let _ = release.send(());
    let r = starting.join().unwrap();
    // Nothing of it may run once the quit's stop has returned: it never spawned.
    let left = rt.list("pluxy/one");
    std::thread::sleep(Duration::from_millis(500));
    let spawned = std::fs::read(&rec)
        .ok()
        .and_then(|b| serde_json::from_slice::<Value>(&b).ok());
    if let Some(x) = &spawned {
        clean(&[pid(x, "pid"), pid(x, "child")], &dir);
    }
    let _ = std::fs::remove_dir_all(&dir);
    assert!(r.is_err(), "a start after the quit's stop: {r:?}");
    assert!(
        left.is_empty(),
        "a start spawned after the quit's stop: {left:?}"
    );
    assert!(spawned.is_none(), "a start spawned after the quit's stop");
}

#[test]
fn a_restart_racing_a_removal_leaves_nothing_and_never_starts_the_dropped_launch() {
    let dir = folder("raced");
    let rt = Arc::new(Runtimes::open(None, Box::new(By(none))));
    let mut all = vec![];
    let checks = |package: &str, reinstall: bool, all: &mut Vec<u64>| {
        let rec = dir.join(package.replace('/', "-"));
        // A restart held between its stop and its new start.
        let (release, gate) = std::sync::mpsc::channel();
        let (held, holding) = std::sync::mpsc::channel();
        let mut gated = agent(&dir, &rec, &[], Lifetime::Managed);
        gated.opened = Box::new(Gated {
            plain: Plain {
                executable: File::open(std::env::current_exe().unwrap()).unwrap(),
                cwd: open_dir(&dir),
            },
            gate: Mutex::new(gate),
            held,
        });
        let _ = release.send(());
        let a = rt.start(package, gated).unwrap();
        let id = a["runtime"].as_str().unwrap().to_string();
        holding.recv().unwrap();
        let x = record(&rec, 0);
        all.extend([pid(&x, "pid"), pid(&x, "child")]);
        let racer = {
            let (rt, package, id) = (Arc::clone(&rt), package.to_string(), id.clone());
            std::thread::spawn(move || rt.restart(&package, &id))
        };
        holding.recv().unwrap();
        // Removed while it restarts (and, with reinstall, installed and enabled again at once).
        rt.close(package, true);
        if reinstall {
            rt.reopen(package);
        }
        let _ = release.send(());
        let r = racer.join().unwrap();
        std::thread::sleep(Duration::from_millis(300));
        let again: Value = std::fs::read(&rec)
            .ok()
            .and_then(|b| serde_json::from_slice(&b).ok())
            .unwrap_or(Value::Null);
        all.extend([pid(&again, "pid"), pid(&again, "child")]);
        assert!(
            r.is_err(),
            "{package}: the raced restart started again: {r:?}"
        );
        assert_eq!(
            again["pid"], x["pid"],
            "{package}: the dropped launch ran again"
        );
        assert!(ended(pid(&x, "pid"), &rec) && ended(pid(&x, "child"), &rec));
        assert!(
            rt.list(package).is_empty(),
            "{package}: the removed package keeps {:?}",
            rt.list(package)
        );
    };
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        checks("pluxy/one", false, &mut all);
        checks("pluxy/two", true, &mut all);
    }));
    rt.stop_all();
    clean(&all, &dir);
    let _ = std::fs::remove_dir_all(&dir);
    if let Err(e) = r {
        std::panic::resume_unwind(e);
    }
}

#[test]
fn a_launchs_stdin_secret_reaches_it_on_its_stdin_only() {
    // Refused unless it names a secret field the launch fills; shown and part of the revision.
    let input = json!({"executable": "/x", "argv": [], "cwd": "/", "stdin": "key"});
    assert!(parse(&input, &|_| false).is_err());
    let (lifetime, request) = parse(&input, &|f| f == "key").unwrap();
    assert_eq!(request.stdin.as_deref(), Some("key"));
    let review = request.review(&|f| format!("label ({f})"));
    assert!(review
        .iter()
        .any(|(n, v)| n == "(stdin)" && v.as_deref().is_some_and(|v| v.contains("label (key)"))));
    assert_ne!(
        request.revision(lifetime),
        Request::default().revision(lifetime)
    );
    for lifetime in [Lifetime::Detached, Lifetime::Managed] {
        let dir = folder("stdin");
        let rec = dir.join("agent");
        let mut l = agent(&dir, &rec, &[], lifetime);
        l.request
            .set
            .insert("FIXTURE_READ_STDIN".into(), "1".into());
        l.request.stdin = Some("key".into());
        l.stdin = Some("s-1".into());
        let value = |_: &str, id: &str| {
            (id == "s-1")
                .then(|| CANARY.to_string())
                .ok_or_else(|| "it was removed from the vault".to_string())
        };
        let rt = Runtimes::open(None, Box::new(By(value)));
        if lifetime == Lifetime::Detached {
            if !let_go(&l, "p", &By(value)) {
                // Refused before it ran (Windows, in a job it may not leave): nothing started.
                std::thread::sleep(Duration::from_millis(300));
                let started = rec.exists();
                let _ = std::fs::remove_dir_all(&dir);
                assert!(!started, "a refused detached launch started");
                continue;
            }
        } else {
            rt.start("p", l).unwrap();
        }
        let r = record(&rec, 0);
        let all = [pid(&r, "pid"), pid(&r, "child")];
        let checks = || {
            assert_eq!(r["stdin"], CANARY, "{r}");
            assert!(!r["env"].to_string().contains(CANARY), "{r}");
            #[cfg(unix)]
            assert_eq!(r["core"], json!([1, 1]), "{r}");
        };
        let ok = std::panic::catch_unwind(checks);
        rt.stop_all();
        clean(&all, &dir);
        ok.unwrap();
    }
}

#[test]
fn a_refused_stdin_secret_refuses_the_launch_and_nothing_starts() {
    let refused =
        |_: &str, _: &str| Err::<String, String>("the client key was deleted on core".into());
    for lifetime in [Lifetime::Detached, Lifetime::Managed] {
        let dir = folder("stdin-refused");
        let rec = dir.join("agent");
        let mut l = agent(&dir, &rec, &[], lifetime);
        l.request.stdin = Some("key".into());
        l.stdin = Some("s-1".into());
        let rt = Runtimes::open(None, Box::new(By(refused)));
        let e = if lifetime == Lifetime::Detached {
            detached(&l, "p", &By(refused)).unwrap_err()
        } else {
            rt.start("p", l).unwrap_err()
        };
        std::thread::sleep(Duration::from_millis(300));
        let started = rec.exists();
        let _ = std::fs::remove_dir_all(&dir);
        assert!(e.contains("stdin") && e.contains("deleted on core"), "{e}");
        assert!(!started, "a launch whose stdin secret was refused started");
        assert!(rt.list("p").is_empty());
    }
}

/// A test's `Values`: every secret given, its check held on revision 7, and what it is asked
/// recorded; right before the spawn it refuses while refuse is set.
#[derive(Default)]
struct Checked {
    refuse: AtomicBool,
    asked: Mutex<Vec<String>>,
}

impl Values for Arc<Checked> {
    fn value(&self, package: &str, id: &str) -> Result<(String, Option<u64>), String> {
        self.asked
            .lock()
            .unwrap()
            .push(format!("value {package} {id}"));
        Ok((CANARY.to_string(), Some(7)))
    }
    fn still(&self, given: &[Given]) -> Result<(), String> {
        self.asked.lock().unwrap().push(format!("still {given:?}"));
        if self.refuse.load(Ordering::SeqCst) {
            return Err(
                "the app's connection changed after the launch's client keys were checked".into(),
            );
        }
        Ok(())
    }
}

#[test]
fn a_launch_is_checked_once_more_right_before_its_spawn_and_nothing_starts_when_refused() {
    let checked = Arc::new(Checked::default());
    let rt = Runtimes::open(None, Box::new(Arc::clone(&checked)));
    // A secret in its environment and one on its stdin.
    let launch = |dir: &Path, rec: &Path, lifetime| {
        let mut l = agent(dir, rec, &[], lifetime);
        l.request.secrets.insert("FIXTURE_KEY".into(), "key".into());
        l.request
            .set
            .insert("FIXTURE_READ_STDIN".into(), "1".into());
        l.request.stdin = Some("bridge".into());
        l.secrets = vec![("FIXTURE_KEY".into(), "s-1".into())];
        l.stdin = Some("s-2".into());
        l
    };
    let given = [
        ("s-1".to_string(), Some(7u64)),
        ("s-2".to_string(), Some(7)),
    ];
    let want = vec![
        "value p s-1".to_string(),
        "value p s-2".to_string(),
        format!("still {given:?}"),
    ];
    for lifetime in [Lifetime::Detached, Lifetime::Managed] {
        let dir = folder("still");
        let rec = dir.join("agent");
        checked.refuse.store(true, Ordering::SeqCst);
        checked.asked.lock().unwrap().clear();
        let l = launch(&dir, &rec, lifetime);
        let e = if lifetime == Lifetime::Detached {
            detached(&l, "p", &checked).unwrap_err()
        } else {
            rt.start("p", l).unwrap_err()
        };
        std::thread::sleep(Duration::from_millis(300));
        let started = rec.exists();
        let _ = std::fs::remove_dir_all(&dir);
        assert!(e.contains("connection changed"), "{lifetime:?}: {e}");
        assert!(
            !started,
            "{lifetime:?}: a launch refused before its spawn started"
        );
        assert!(rt.list("p").is_empty());
        // Checked once all of its secrets were resolved, each with its check's revision.
        assert_eq!(*checked.asked.lock().unwrap(), want, "{lifetime:?}");
    }
    // A runtime's restart is checked so too: refused, it does not start again.
    let dir = folder("still-restart");
    let rec = dir.join("agent");
    checked.refuse.store(false, Ordering::SeqCst);
    let a = rt
        .start("p", launch(&dir, &rec, Lifetime::Managed))
        .unwrap();
    let id = a["runtime"].as_str().unwrap().to_string();
    let x = record(&rec, 0);
    let checks = || {
        assert_eq!(x["stdin"], CANARY, "{x}");
        assert_eq!(x["env"]["FIXTURE_KEY"], CANARY, "{x}");
        checked.refuse.store(true, Ordering::SeqCst);
        let e = rt.restart("p", &id).unwrap_err();
        assert!(e.contains("connection changed"), "{e}");
        assert!(ended(pid(&x, "pid"), &rec) && ended(pid(&x, "child"), &rec));
        std::thread::sleep(Duration::from_millis(300));
        let now: Value = serde_json::from_slice(&std::fs::read(&rec).unwrap()).unwrap();
        assert_eq!(now["pid"], x["pid"], "the refused restart started it again");
        assert_eq!(rt.status("p", &id).unwrap()["state"], "exited");
    };
    let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(checks));
    rt.stop_all();
    clean(&[pid(&x, "pid"), pid(&x, "child")], &dir);
    let _ = std::fs::remove_dir_all(&dir);
    if let Err(e) = r {
        std::panic::resume_unwind(e);
    }
}

// Linux only: systemd's scopes.
#[cfg(target_os = "linux")]
#[test]
fn a_detached_launch_runs_in_a_scope_of_its_own_under_a_user_manager() {
    let dir = folder("scope");
    let rec = dir.join("agent");
    let l = agent(&dir, &rec, &[], Lifetime::Detached);
    detached(&l, "p", &By(none)).unwrap();
    let r = record(&rec, 0);
    let all = [pid(&r, "pid"), pid(&r, "child")];
    let cgroup = |p: u64| std::fs::read_to_string(format!("/proc/{p}/cgroup")).unwrap_or_default();
    let own = std::fs::read_to_string("/proc/self/cgroup").unwrap_or_default();
    // Whether a systemd user manager answers on this session bus at all.
    let manager = scope::available();
    let checks = || {
        if manager {
            for p in all {
                let c = cgroup(p);
                assert!(c.contains("/app-pluxy-launch-"), "pid {p} cgroup {c}");
            }
        } else {
            eprintln!("no systemd user manager on this session bus: the launch stays in {own}");
            assert_eq!(cgroup(all[0]), own);
        }
    };
    let ok = std::panic::catch_unwind(checks);
    clean(&all, &dir);
    ok.unwrap();
}

// Linux only: the session bus the systemd scopes are asked for on.
#[cfg(target_os = "linux")]
#[test]
fn a_bus_that_never_answers_holds_a_launch_no_longer_than_the_wait() {
    use gtk::gio;
    // A socket that accepts connections (its backlog) and never says a word.
    let dir = std::env::temp_dir().join(format!("pluxy-silent-bus-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("bus");
    let _ = std::fs::remove_file(&path);
    let _listener = std::os::unix::net::UnixListener::bind(&path).unwrap();
    let address = format!("unix:path={}", path.display());
    let began = std::time::Instant::now();
    let r = scope::within(Duration::from_millis(500), |c| {
        gio::DBusConnection::for_address_sync(
            &address,
            gio::DBusConnectionFlags::AUTHENTICATION_CLIENT
                | gio::DBusConnectionFlags::MESSAGE_BUS_CONNECTION,
            None,
            Some(c),
        )
    });
    assert!(r.is_err(), "a silent bus answered");
    assert!(
        began.elapsed() < Duration::from_secs(3),
        "the silent bus held the call {:?}",
        began.elapsed()
    );
    let _ = std::fs::remove_dir_all(&dir);
}

/// On macOS a launch's program may sit below a folder the `admin` group may write, as in
/// `/Applications` or Homebrew's folders (`owned::Writers::Admins`): such a launch starts.
#[cfg(target_os = "macos")]
#[test]
fn on_macos_a_launch_from_a_folder_the_admin_group_may_write_starts() {
    use std::os::unix::fs::PermissionsExt;
    let dir = folder("admin");
    let bin = dir.join("bin");
    std::fs::create_dir(&bin).unwrap();
    let out = std::process::Command::new("/usr/bin/chgrp")
        .arg("admin")
        .arg(&bin)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "chgrp admin (the test's user must be in admin): {}",
        String::from_utf8_lossy(&out.stderr)
    );
    std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o775)).unwrap();
    let program = bin.join("agent");
    std::fs::write(&program, "#!/bin/sh\necho started > \"$1\"\n").unwrap();
    std::fs::set_permissions(&program, std::fs::Permissions::from_mode(0o700)).unwrap();
    let started = dir.join("started");
    let l = Launch {
        opened: Box::new(Plain {
            executable: File::open(&program).unwrap(),
            cwd: open_dir(&dir),
        }),
        named: program.clone(),
        // Its arguments after its name (`named`, given as argv[0]).
        argv: vec![started.to_string_lossy().into_owned()],
        lifetime: Lifetime::Detached,
        request: Request::default(),
        secrets: vec![],
        stdin: None,
    };
    let r = detached(&l, "p", &By(none));
    let deadline = Instant::now() + Duration::from_secs(20);
    while r.is_ok() && !started.exists() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    let ran = started.exists();
    let _ = std::fs::remove_dir_all(&dir);
    r.unwrap();
    assert!(ran, "the launch did not run");
}

/// A terminal launch as confirmed: the terminal, and the program it runs, opened and as the
/// launch names it.
#[cfg(unix)]
struct InTerminal {
    plain: Plain,
    program: File,
    named: PathBuf,
}

#[cfg(unix)]
impl Opened for InTerminal {
    fn executable(&self) -> &File {
        &self.plain.executable
    }
    fn cwd(&self) -> &File {
        &self.plain.cwd
    }
    fn program(&self) -> Option<(&File, &Path)> {
        Some((&self.program, &self.named))
    }
    fn recheck(&self) -> Result<(), String> {
        Ok(())
    }
}

/// A shell script at p running body, which only its owner may change or run.
#[cfg(unix)]
fn script(p: &Path, body: &str) {
    use std::os::unix::fs::PermissionsExt;
    std::fs::write(p, format!("#!/bin/sh\n{body}\n")).unwrap();
    std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o700)).unwrap();
}

/// A folder at p with mode.
#[cfg(unix)]
fn mode_dir(p: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    std::fs::create_dir(p).unwrap();
    std::fs::set_permissions(p, std::fs::Permissions::from_mode(mode)).unwrap();
}

/// A detached launch in dir of terminal, a stand-in terminal script running body (its arguments:
/// the file `go` in dir, then args), with program named as the program it runs.
#[cfg(unix)]
fn in_terminal(dir: &Path, body: &str, program: &Path, args: &[&Path]) -> Launch {
    let terminal = dir.join("terminal");
    script(&terminal, body);
    let mut argv = vec![dir.join("go").to_string_lossy().into_owned()];
    argv.extend(args.iter().map(|a| a.to_string_lossy().into_owned()));
    Launch {
        opened: Box::new(InTerminal {
            plain: Plain {
                executable: File::open(&terminal).unwrap(),
                cwd: open_dir(dir),
            },
            program: File::open(program).unwrap(),
            named: program.to_path_buf(),
        }),
        named: terminal,
        argv,
        lifetime: Lifetime::Detached,
        request: Request::default(),
        secrets: vec![],
        stdin: None,
    }
}

/// A terminal as Linux's run a launch's program: once the file go exists, by its path, as
/// `<program> <out>` after the flag `-e`.
#[cfg(unix)]
const BY_PATH: &str = r#"go=$1; shift 2; while [ ! -e "$go" ]; do sleep 0.05; done; exec "$@""#;

/// Another file renamed over p, as another user who may write its folder could: it writes
/// `swapped` to its first argument.
#[cfg(unix)]
fn swap(p: &Path) {
    let new = p.with_extension("new");
    script(&new, r#"echo swapped > "$1""#);
    std::fs::rename(&new, p).unwrap();
}

/// What file p holds once it exists, within 20 seconds.
#[cfg(unix)]
fn wait_for(p: &Path) -> Option<String> {
    let deadline = Instant::now() + Duration::from_secs(20);
    while !p.exists() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    std::fs::read_to_string(p).ok()
}

/// A terminal runs a launch's program by its path, while it starts, after every check of the
/// app's: a program another user could swap in that time (here, in a folder anyone may write) is
/// refused, and nothing runs.
#[cfg(unix)]
#[test]
fn a_terminal_launchs_program_another_user_could_swap_is_refused() {
    let dir = folder("swap");
    let open = dir.join("open");
    mode_dir(&open, 0o777);
    let program = open.join("agent");
    script(&program, r#"echo checked > "$1""#);
    let out = dir.join("out");
    let l = in_terminal(&dir, BY_PATH, &program, &[Path::new("-e"), &program, &out]);
    let r = detached(&l, "p", &By(none));
    swap(&program);
    std::fs::write(dir.join("go"), "").unwrap();
    let ran = r.is_ok().then(|| wait_for(&out));
    let _ = std::fs::remove_dir_all(&dir);
    let e = r.expect_err(&format!("the launch started, and its terminal ran {ran:?}"));
    assert!(
        e.starts_with(&format!("{} may be changed by other users", open.display())),
        "{e}"
    );
}

/// On Linux the terminal resolves the program's path itself: each folder and link on the way must
/// be one only root or the person can change. A program reached through the person's own link (as
/// `~/.local/bin/claude`) starts; through a link in, or to, a folder anyone may write, it is
/// refused, naming that folder.
#[cfg(target_os = "linux")]
#[test]
fn on_linux_a_terminal_launchs_program_is_checked_along_its_links() {
    use std::os::unix::fs::symlink;
    let dir = folder("links");
    let (versions, bin, open) = (dir.join("versions"), dir.join("bin"), dir.join("open"));
    mode_dir(&versions, 0o755);
    mode_dir(&bin, 0o755);
    mode_dir(&open, 0o777);
    script(&versions.join("agent"), r#"echo checked > "$1""#);
    symlink("../versions/agent", bin.join("agent")).unwrap();
    symlink("../versions/agent", open.join("agent")).unwrap();
    script(&open.join("real"), r#"echo checked > "$1""#);
    symlink("../open/real", bin.join("other")).unwrap();
    let out = dir.join("out");
    let run = |program: &Path| {
        let l = in_terminal(&dir, BY_PATH, program, &[Path::new("-e"), program, &out]);
        detached(&l, "p", &By(none))
    };
    let own = run(&bin.join("agent"));
    std::fs::write(dir.join("go"), "").unwrap();
    let ran = wait_for(&out);
    let in_open = run(&open.join("agent"));
    let to_open = run(&bin.join("other"));
    let _ = std::fs::remove_dir_all(&dir);
    own.unwrap();
    assert_eq!(ran.as_deref(), Some("checked\n"));
    for r in [in_open, to_open] {
        let e = r.expect_err("a program another user could redirect started");
        assert!(
            e.starts_with(&format!("{} may be changed by other users", open.display())),
            "{e}"
        );
    }
}

/// On macOS the terminal is the execution program, which runs the program in the window from the
/// descriptor the app gives it (3): the file the app checked, even once another file has been
/// renamed over its path. A program below a folder the `admin` group may write starts.
#[cfg(target_os = "macos")]
#[test]
fn on_macos_a_terminal_launch_gets_the_checked_program_as_descriptor_3() {
    let dir = folder("fd");
    let bin = dir.join("bin");
    mode_dir(&bin, 0o775);
    let out = std::process::Command::new("/usr/bin/chgrp")
        .arg("admin")
        .arg(&bin)
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "chgrp admin (the test's user must be in admin): {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let program = bin.join("agent");
    script(&program, r#"echo checked > "$1""#);
    let copy = dir.join("copy");
    let l = in_terminal(
        &dir,
        r#"while [ ! -e "$1" ]; do sleep 0.05; done; cat <&3 > "$2.part" && mv "$2.part" "$2""#,
        &program,
        &[&copy],
    );
    let r = detached(&l, "p", &By(none));
    swap(&program);
    std::fs::write(dir.join("go"), "").unwrap();
    let got = r.is_ok().then(|| wait_for(&copy));
    let _ = std::fs::remove_dir_all(&dir);
    r.unwrap();
    assert_eq!(
        got.flatten().as_deref(),
        Some("#!/bin/sh\necho checked > \"$1\"\n")
    );
}

#[test]
fn an_open_launch_gets_only_the_absolute_path_entries() {
    // Windows separates PATH's entries with `;`, and an absolute one names its drive.
    #[cfg(unix)]
    let (all, absolute) = (".:bin:/usr/bin::/bin", "/usr/bin:/bin");
    #[cfg(windows)]
    let (all, absolute) = (
        r".;bin;C:\Windows;;D:\tools;\rooted",
        r"C:\Windows;D:\tools",
    );
    #[cfg(unix)]
    let snap = Snapshot::of(&[("HOME", "/home/me"), ("PATH", all)], core_limit());
    #[cfg(windows)]
    let snap = Snapshot::of(&[("USERPROFILE", r"C:\Users\me"), ("PATH", all)]);
    let path = |r: &Request| {
        environment(&snap, r, &[])
            .into_iter()
            .find(|(k, _)| k == "PATH")
            .map(|(_, v)| v.to_string_lossy().into_owned())
    };
    let mut r = Request::default();
    assert_eq!(path(&r).as_deref(), Some(all));
    r.absolute_path = true;
    assert_eq!(path(&r).as_deref(), Some(absolute));
}

/// A launch's files as the confirmation opened them, which records at each check whether its
/// executable could be renamed then (what a pin forbids: an open or running program can be
/// renamed otherwise) and refuses from its second check on.
#[cfg(windows)]
struct Probed {
    plain: Plain,
    path: PathBuf,
    pinned: Mutex<Vec<bool>>,
}

#[cfg(windows)]
impl Opened for Probed {
    fn executable(&self) -> &File {
        &self.plain.executable
    }
    fn cwd(&self) -> &File {
        &self.plain.cwd
    }
    fn recheck(&self) -> Result<(), String> {
        let moved = self.path.with_extension("moved");
        let renamed = std::fs::rename(&self.path, &moved).is_ok();
        if renamed {
            std::fs::rename(&moved, &self.path).unwrap();
        }
        let mut pinned = self.pinned.lock().unwrap();
        pinned.push(!renamed);
        if pinned.len() > 1 {
            return Err("changed since it was confirmed".into());
        }
        Ok(())
    }
}

/// On Windows a launch is checked once more after its executable and cwd are pinned, when
/// nothing can change them any more: a change made after the first check is seen then, and
/// nothing starts.
#[cfg(windows)]
#[test]
fn on_windows_a_launch_is_checked_again_once_its_files_are_pinned() {
    let rt = Runtimes::open(None, Box::new(By(none)));
    for lifetime in [Lifetime::Detached, Lifetime::Managed] {
        let dir = folder("pinned");
        let exe = dir.join("agent.exe");
        std::fs::copy(std::env::current_exe().unwrap(), &exe).unwrap();
        let rec = dir.join("agent");
        let mut l = agent(&dir, &rec, &[], lifetime);
        l.named = exe.clone();
        let probed = Arc::new(Probed {
            plain: Plain {
                executable: File::open(&exe).unwrap(),
                cwd: open_dir(&dir),
            },
            path: exe.clone(),
            pinned: Mutex::default(),
        });
        struct Shared(Arc<Probed>);
        impl Opened for Shared {
            fn executable(&self) -> &File {
                self.0.executable()
            }
            fn cwd(&self) -> &File {
                self.0.cwd()
            }
            fn recheck(&self) -> Result<(), String> {
                self.0.recheck()
            }
        }
        l.opened = Box::new(Shared(Arc::clone(&probed)));
        let e = if lifetime == Lifetime::Detached {
            detached(&l, "p", &By(none)).unwrap_err()
        } else {
            rt.start("p", l).unwrap_err()
        };
        std::thread::sleep(Duration::from_millis(300));
        let started = rec.exists();
        assert!(
            e.contains("changed since it was confirmed"),
            "{lifetime:?}: {e}"
        );
        assert!(
            !started,
            "{lifetime:?}: a launch refused at its second check started"
        );
        assert_eq!(
            *probed.pinned.lock().unwrap(),
            vec![false, true],
            "{lifetime:?}: checked before and after its executable was pinned"
        );
        assert!(rt.list("p").is_empty());
        let _ = std::fs::remove_dir_all(&dir);
    }
}

/// Resolves no secret: the runtimes file test starts nothing.
struct NoValues;

impl Values for NoValues {
    fn value(&self, _: &str, id: &str) -> Result<(String, Option<u64>), String> {
        Err(format!("{id}: no secrets here"))
    }
    fn still(&self, _: &[Given]) -> Result<(), String> {
        Ok(())
    }
}

/// The runtimes file is written private, replacing the old one in one step: on Linux and macOS
/// 0600, also when a temporary file of an earlier run is left beside it with wider bits or a link
/// is left there; on Windows with a protected access list allowing only this user and SYSTEM.
#[test]
fn the_runtimes_file_is_private() {
    let dir = folder("runtimes-file");
    let file = dir.join("runtimes.json");
    // Opening the registry writes the file once.
    let rt = Runtimes::open(Some(file.clone()), Box::new(NoValues));
    // What an earlier run could leave beside it: its temporary file, readable by others.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::write(dir.join("runtimes.tmp"), "[]").unwrap();
        std::fs::set_permissions(
            dir.join("runtimes.tmp"),
            std::fs::Permissions::from_mode(0o644),
        )
        .unwrap();
    }
    rt.0.save(&[]);
    let m = std::fs::symlink_metadata(&file).unwrap();
    assert!(m.is_file(), "{m:?}");
    assert_eq!(std::fs::read_to_string(&file).unwrap(), "[]");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(m.permissions().mode() & 0o777, 0o600);
    }
    #[cfg(windows)]
    assert!(crate::private_windows::is_private(&std::fs::File::open(&file).unwrap()).unwrap());
    // A link left where a temporary file could go is not written through.
    #[cfg(unix)]
    {
        let elsewhere = dir.join("elsewhere");
        std::fs::write(&elsewhere, "kept").unwrap();
        let _ = std::fs::remove_file(dir.join("runtimes.tmp"));
        std::os::unix::fs::symlink(&elsewhere, dir.join("runtimes.tmp")).unwrap();
        rt.0.save(&[]);
        assert_eq!(std::fs::read_to_string(&elsewhere).unwrap(), "kept");
        assert!(std::fs::symlink_metadata(&file).unwrap().is_file());
    }
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn a_launch_through_wsl_exe_passes_on_the_variables_it_sets_and_nothing_else() {
    let root = Path::new(r"C:\Windows");
    for yes in [
        r"C:\Windows\System32\wsl.exe",
        r"c:\windows\system32\WSL.EXE",
        r"\\?\C:\Windows\System32\wsl.exe",
    ] {
        assert!(is_wsl_exe(Path::new(yes), Some(root)), "{yes}");
    }
    for no in [
        r"C:\Windows\SysWOW64\wsl.exe",
        r"C:\tools\wsl.exe",
        r"C:\Windows\System32\wslg.exe",
        "wsl.exe",
    ] {
        assert!(!is_wsl_exe(Path::new(no), Some(root)), "{no}");
    }
    assert!(!is_wsl_exe(Path::new(r"C:\Windows\System32\wsl.exe"), None));
    assert_eq!(wslenv(&Request::default()), None);
    let (_, r) = parse(
        &serde_json::json!({"environment": {"set": {"MODE": "x"}, "inherit": ["LANG"],
            "secrets": {"API_KEY": "key"}}, "key": "s"}),
        &|f| f == "key",
    )
    .unwrap();
    // Set and secret names cross; inherited Windows values do not.
    assert_eq!(wslenv(&r).as_deref(), Some("MODE/u:API_KEY/u"));
}

/// A managed and a detached launch named as Windows' own `wsl.exe`, with Windows' folder given
/// (`TEST_SYSTEM_ROOT`; the program run is still this test's agent), each start with `WSLENV`
/// naming what they set: both launch paths take their environment from `launch_env`. On Windows
/// the detached one runs only outside the runner's job (`let_go`), as CI's step "Detached launch
/// outside the runner's job" runs this test; inside it, it is refused before it runs.
#[test]
fn managed_and_detached_launches_of_wsl_exe_start_with_wslenv() {
    *TEST_SYSTEM_ROOT.lock().unwrap() = Some(PathBuf::from(r"C:\Windows"));
    let dir = folder("wslenv-launch");
    let as_wsl = |rec: &Path, lifetime| {
        let mut l = agent(&dir, rec, &[], lifetime);
        l.named = PathBuf::from(r"C:\Windows\System32\wsl.exe");
        l.request.set.insert("FIXTURE_SET".into(), "set".into());
        l
    };
    let (rd, rm) = (dir.join("detached"), dir.join("managed"));
    let ran = let_go(&as_wsl(&rd, Lifetime::Detached), "p", &By(none));
    let rt = Runtimes::open(None, Box::new(By(none)));
    let m = rt.start("p", as_wsl(&rm, Lifetime::Managed)).unwrap();
    let mr = record(&rm, 0);
    let d = ran.then(|| record(&rd, 0));
    *TEST_SYSTEM_ROOT.lock().unwrap() = None;
    let _ = rt.stop("p", m["runtime"].as_str().unwrap());
    let mut all = vec![pid(&mr, "pid"), pid(&mr, "child")];
    if let Some(d) = &d {
        all.extend([pid(d, "pid"), pid(d, "child")]);
    } else {
        // Refused before it ran (Windows, in a job it may not leave): nothing started.
        std::thread::sleep(Duration::from_millis(300));
        assert!(!rd.exists(), "a refused detached launch started");
    }
    clean(&all, &dir);
    for r in d.iter().chain([&mr]) {
        assert_eq!(r["env"]["WSLENV"], "FIXTURE_SET/u", "{r}");
        assert_eq!(r["env"]["FIXTURE_SET"], "set", "{r}");
    }
    let _ = std::fs::remove_dir_all(&dir);
}

/// Every launch, managed or detached, gets its environment from `launch_env`: one of Windows'
/// own `wsl.exe` gets `WSLENV` naming what it sets (replacing any it inherited), and no other
/// launch does.
#[test]
fn a_launch_of_wsl_exe_gets_wslenv_in_its_environment_and_no_other_launch_does() {
    let dir = folder("wslenv");
    let (lifetime, request) = parse(
        &serde_json::json!({"environment": {"set": {"MODE": "x"}, "secrets": {"API_KEY": "key"}},
            "key": "s"}),
        &|f| f == "key",
    )
    .unwrap();
    let launch = |named: &str| Launch {
        opened: opened(&dir),
        named: PathBuf::from(named),
        argv: vec![],
        lifetime,
        request: request.clone(),
        secrets: vec![("API_KEY".into(), "s-1".into())],
        stdin: None,
    };
    let secrets = [("API_KEY".to_string(), "v".to_string())];
    let root = Some(Path::new(r"C:\Windows"));
    let wslenv_of = |env: Vec<(String, OsString)>| -> Vec<OsString> {
        env.into_iter()
            .filter(|(k, _)| k.eq_ignore_ascii_case("WSLENV"))
            .map(|(_, v)| v)
            .collect()
    };
    let wsl = launch(r"C:\Windows\System32\wsl.exe");
    assert_eq!(
        wslenv_of(launch_env(&wsl, &secrets, root)),
        [OsString::from("MODE/u:API_KEY/u")]
    );
    // Not wsl.exe, or no Windows folder to find it in: nothing added.
    assert!(wslenv_of(launch_env(&launch(r"C:\tools\wsl.exe"), &secrets, root)).is_empty());
    assert!(wslenv_of(launch_env(&wsl, &secrets, None)).is_empty());
    let _ = std::fs::remove_dir_all(&dir);
}
