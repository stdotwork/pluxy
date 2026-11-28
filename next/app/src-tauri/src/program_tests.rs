//! The program host against real processes: the fixture tool (`examples/program_fixture`) runs
//! as this test program. When the host starts it, with the contract's environment, the hook
//! below runs the fixture before any test does; each test's package folder holds a link to this
//! program as its tool, its manifest, and a `fixture.json` that makes the fixture misbehave.
//!
//! The lifetime checks (`lifetime_*`, at the end) run on Linux, macOS and Windows: the tool host
//! and the spawn service (`spawn.rs`) together, with the fixture's heartbeat as the managed
//! runtime and the detached launch.

use super::*;
use crate::spawn::{Launch, Lifetime, Runtimes, Values};
use std::collections::{BTreeMap, BTreeSet};
use std::path::Path;
use std::sync::OnceLock;

#[path = "../examples/program_fixture/fixture.rs"]
mod fixture;

// Runs before main: ELF's `.init_array`, Mach-O's `__mod_init_func`, the MSVC runtime's `.CRT$XCU`.
#[used]
#[cfg_attr(target_os = "linux", link_section = ".init_array")]
#[cfg_attr(target_os = "macos", link_section = "__DATA,__mod_init_func")]
#[cfg_attr(windows, link_section = ".CRT$XCU")]
static FIXTURE: extern "C" fn() = {
    extern "C" fn run_fixture() {
        if let Some(role) = std::env::var_os(LIFETIME_HOST) {
            let dir = std::env::var_os(LIFETIME_DIR).unwrap_or_default();
            lifetime_host(&role.to_string_lossy(), Path::new(&dir));
        }
        // The spawn tests' agent (`spawn_tests.rs`) is started with `AGENT` as its first argument,
        // the lifetime checks' heartbeat with `HEARTBEAT`.
        let first = |prefix: &str| {
            #[cfg(target_os = "linux")]
            return std::fs::read("/proc/self/cmdline").is_ok_and(|c| {
                c.split(|b| *b == 0)
                    .nth(1)
                    .is_some_and(|a| a.starts_with(prefix.as_bytes()))
            });
            #[cfg(not(target_os = "linux"))]
            std::env::args_os()
                .nth(1)
                .is_some_and(|a| a.to_string_lossy().starts_with(prefix))
        };
        if std::env::var_os("PLUXY_EXTENSION_ID").is_some()
            || std::env::var_os(fixture::CHILD).is_some()
            || first(fixture::AGENT)
            || first(fixture::HEARTBEAT)
        {
            fixture::main();
        }
    }
    run_fixture
};

const ID: &str = "pluxy/test";
const SECRET: &str = "s3cret-canary";

fn fast() -> Limits {
    Limits {
        idle: Duration::from_millis(800),
        startup: Duration::from_secs(3),
        stop: Duration::from_millis(800),
        cancel: Duration::from_millis(800),
        backoff: Duration::from_millis(300),
        backoff_max: Duration::from_millis(1200),
        quarantine_after: 5,
        stable: Duration::from_secs(600),
        drain: Duration::from_secs(10),
        wsl_retry: Duration::from_millis(100),
    }
}

fn this_program_sha256() -> &'static str {
    static SHA: OnceLock<String> = OnceLock::new();
    SHA.get_or_init(|| {
        let bytes = std::fs::read(std::env::current_exe().unwrap()).unwrap();
        Sha256::digest(bytes)
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect()
    })
}

fn operations() -> Vec<Operation> {
    vec![
        Operation {
            id: "summary".into(),
            input_schema: json!({"type": "object",
                "properties": {"items": {"type": "array", "items": {"type": "string", "maxLength": 16}, "maxItems": 8}},
                "required": ["items"], "additionalProperties": false}),
            output_schema: json!({"type": "object", "required": ["count", "longest"],
                "properties": {"count": {"type": "integer"}, "longest": {"type": "string"}}}),
        },
        Operation {
            id: "wait".into(),
            input_schema: json!({"type": "object"}),
            output_schema: json!({"type": "object"}),
        },
        Operation {
            id: "relay".into(),
            input_schema: json!({"type": "object"}),
            output_schema: json!({"type": "object"}),
        },
        Operation {
            id: "slow".into(),
            input_schema: json!({"type": "object", "required": ["ms"],
                "properties": {"ms": {"type": "integer", "minimum": 0}}}),
            output_schema: json!({"type": "object"}),
        },
        Operation {
            id: "flood".into(),
            input_schema: json!({"type": "object"}),
            output_schema: json!({"type": "object"}),
        },
        Operation {
            id: "crash".into(),
            input_schema: json!({"type": "object"}),
            output_schema: json!({"type": "object"}),
        },
        Operation {
            id: "apply".into(),
            input_schema: json!({"type": "object"}),
            output_schema: json!({"type": "object"}),
        },
        Operation {
            id: "report".into(),
            input_schema: json!({"type": "object"}),
            output_schema: json!({"type": "object"}),
        },
    ]
}

/// The host-private services, as the unit tests see them: SECRET is redacted, journals are kept
/// in memory, and each vault frame is recorded with the call's operation and, in describe, the
/// journals the tool was told of.
#[derive(Default)]
struct Stub {
    journals: Mutex<Vec<Value>>,
    frames: Mutex<Vec<Frame>>,
    /// journal.end is refused: the journal stays open.
    keep_open: bool,
    /// The journals cannot be read.
    unreadable: bool,
}

/// A vault frame a Stub was sent: its op, the call's operation, the journals describe was told of.
type Frame = (String, String, Option<Vec<String>>);

impl Private for Stub {
    #[cfg(feature = "native-test")]
    fn test_carries(&self, _: &str) -> (bool, bool) {
        (false, false)
    }
    fn journals(&self, _: &Instance) -> Result<Vec<Value>, String> {
        if self.unreadable {
            return Err("its records are damaged".into());
        }
        Ok(self.journals.lock().unwrap().clone())
    }
    fn frame(
        &self,
        tool: &Hop,
        _: &Instance,
        op: &str,
        describing: Option<&[String]>,
        r: &Value,
    ) -> Result<Value, CallError> {
        assert_eq!(tool.package(), Some(ID));
        let kind = r["op"].as_str().unwrap_or_default().to_string();
        self.frames.lock().unwrap().push((
            kind.clone(),
            op.into(),
            describing.map(<[String]>::to_vec),
        ));
        match kind.as_str() {
            "backup.put" => Ok(json!({"backup": "b-1"})),
            "journal.begin" => Ok(json!({"journal": "j-1"})),
            "journal.commit" => Ok(json!({"journal": r["journal"], "outcome": "committed"})),
            "journal.end" if self.keep_open => Err(fail("unavailable", "kept open")),
            "backup.list" => Ok(json!({"backups": []})),
            "journal.end" => {
                self.journals
                    .lock()
                    .unwrap()
                    .retain(|j| j["id"] != r["journal"]);
                Ok(json!({"journal": r["journal"], "outcome": r["outcome"]}))
            }
            _ => Err(fail("unsupported", "no such operation")),
        }
    }
    fn redact(&self, text: &str) -> String {
        text.replace(SECRET, "[redacted]")
    }
}

/// host with a Stub as its host-private services.
fn private(h: &Host, stub: Stub) -> Arc<Stub> {
    let stub = Arc::new(stub);
    h.set_private(Arc::clone(&stub) as Arc<dyn Private>);
    stub
}

/// One copy of this test program in the temporary folder, for the package folders there to link
/// to: named by its digest, so each build of it is copied once, however often the tests run.
#[cfg(windows)]
fn this_program_in_temp() -> &'static Path {
    static COPY: OnceLock<PathBuf> = OnceLock::new();
    COPY.get_or_init(|| {
        let copy = std::env::temp_dir().join(format!(
            "pluxy-program-tests-{}.exe",
            &this_program_sha256()[..16]
        ));
        if !copy.exists() {
            let part = with(&copy, &crate::managed::random_key());
            std::fs::copy(std::env::current_exe().unwrap(), &part).unwrap();
            // Another test run may have put it there meanwhile: the same bytes.
            if std::fs::rename(&part, &copy).is_err() {
                let _ = std::fs::remove_file(&part);
            }
        }
        copy
    })
}

/// A package folder for the fixture, misbehaving as mode says (null: not at all).
fn package(mode: Value) -> Spec {
    release(mode, "1.0.0", 1)
}

/// `package`, for release version with generation.
fn release(mode: Value, version: &str, generation: u64) -> Spec {
    let mut rnd = [0u8; 8];
    getrandom::fill(&mut rnd).unwrap();
    let name: String = rnd.iter().map(|b| format!("{b:02x}")).collect();
    let dir = std::env::temp_dir().join(format!("pluxy-program-{name}"));
    std::fs::create_dir_all(dir.join("bin")).unwrap();
    let ops: Vec<Value> = operations()
        .iter()
        .map(|o| json!({"id": o.id, "description": "d", "input_schema": o.input_schema, "output_schema": o.output_schema}))
        .collect();
    std::fs::write(
        dir.join("pluxy-extension.json"),
        serde_json::to_vec(&json!({"version": version,
                "app": {"operations": ops, "state": {"version": 0, "readable_versions": [0]}}}))
        .unwrap(),
    )
    .unwrap();
    if !mode.is_null() {
        std::fs::write(dir.join("fixture.json"), serde_json::to_vec(&mode).unwrap()).unwrap();
    }
    #[cfg(unix)]
    let program = dir.join("bin").join("tool");
    #[cfg(unix)]
    std::os::unix::fs::symlink(std::env::current_exe().unwrap(), &program).unwrap();
    // Windows runs programs only (`.exe`): a hard link of this test program, made from one copy
    // of it in the temporary folder, which can be on another drive than the program.
    #[cfg(windows)]
    let program = dir.join("bin").join("tool.exe");
    #[cfg(windows)]
    std::fs::hard_link(this_program_in_temp(), &program).unwrap();
    Spec {
        id: ID.into(),
        sha256: "ab".repeat(32),
        generation,
        program,
        program_sha256: this_program_sha256().into(),
        dir,
        operations: operations(),
        state_version: 0,
        environment: Environment::Local,
        wsl: None,
    }
}

/// A package folder, removed with the host that holds it.
struct Folder(PathBuf);

impl Drop for Folder {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// A host whose record for ID is spec; each start first runs before, given the ID.
fn host_gated(spec: Spec, limits: Limits, before: impl Fn(&str) + Send + Sync + 'static) -> Host {
    let folder = Folder(spec.dir.clone());
    Host::for_tests(
        Box::new(move |instance: &Instance| {
            let _ = &folder;
            let id = instance.key();
            before(&id);
            if id == ID {
                Ok(spec.clone())
            } else {
                Err(format!("{id} is not installed on this device"))
            }
        }),
        limits,
    )
}

fn host_for(spec: Spec, limits: Limits) -> Host {
    host_gated(spec, limits, |_| {})
}

/// A host whose starts each wait for a message on the sender it gives.
fn gated(mode: Value) -> (Arc<Host>, mpsc::Sender<()>) {
    let (tx, rx) = mpsc::channel::<()>();
    let rx = Mutex::new(rx);
    let h = host_gated(package(mode), fast(), move |id| {
        if id == ID {
            let _ = rx.lock().unwrap().recv();
        }
    });
    (Arc::new(h), tx)
}

fn host(mode: Value) -> Host {
    host_for(package(mode), fast())
}

fn summary(h: &Host, items: &[&str]) -> Result<Value, CallError> {
    h.call(ID, "summary", json!({"items": items}), None)
}

/// Whether process pid runs (a zombie does not). A look, never a signal.
#[cfg(target_os = "linux")]
fn alive(pid: u64) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .ok()
        .and_then(|s| s.rsplit_once(") ").and_then(|(_, r)| r.chars().next()))
        .is_some_and(|c| c != 'Z')
}

#[cfg(target_os = "macos")]
fn alive(pid: u64) -> bool {
    let mut info: libc::proc_bsdinfo = unsafe { std::mem::zeroed() };
    let size = std::mem::size_of::<libc::proc_bsdinfo>() as i32;
    let n = unsafe {
        libc::proc_pidinfo(
            pid as i32,
            libc::PROC_PIDTBSDINFO,
            0,
            &mut info as *mut _ as *mut libc::c_void,
            size,
        )
    };
    n == size && info.pbi_status != libc::SZOMB
}

/// Process pid, opened with access only: rights to look at it (to wait for it, to query it),
/// none that could end it. None when there is no such process.
#[cfg(windows)]
fn look_at(pid: u64, access: u32) -> Option<std::os::windows::io::OwnedHandle> {
    use std::os::windows::io::{FromRawHandle, OwnedHandle};
    use windows_sys::Win32::System::Threading::OpenProcess;
    let h = unsafe { OpenProcess(access, 0, pid as u32) };
    (!h.is_null()).then(|| unsafe { OwnedHandle::from_raw_handle(h) })
}

#[cfg(windows)]
fn alive(pid: u64) -> bool {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::Foundation::WAIT_TIMEOUT;
    use windows_sys::Win32::System::Threading::{WaitForSingleObject, PROCESS_SYNCHRONIZE};
    look_at(pid, PROCESS_SYNCHRONIZE)
        .is_some_and(|h| unsafe { WaitForSingleObject(h.as_raw_handle(), 0) } == WAIT_TIMEOUT)
}

fn until(what: &str, secs: u64, f: impl Fn() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(secs);
    while !f() {
        assert!(Instant::now() < deadline, "timed out waiting for {what}");
        std::thread::sleep(Duration::from_millis(50));
    }
}

fn pids(v: &Value) -> (u64, u64) {
    (v["pid"].as_u64().unwrap(), v["child"].as_u64().unwrap())
}

/// Its result, once it ends within secs.
fn joined<T>(t: std::thread::JoinHandle<T>, secs: u64, what: &str) -> T {
    until(what, secs, || t.is_finished());
    t.join().unwrap()
}

fn spawn_summary(h: &Arc<Host>) -> std::thread::JoinHandle<Result<Value, CallError>> {
    let h = Arc::clone(h);
    std::thread::spawn(move || summary(&h, &[]))
}

/// The tool's current run, to see its turn.
fn run_of(h: &Host) -> Option<Arc<Run>> {
    h.0.slots().get(ID).and_then(|s| s.run.clone())
}

#[test]
fn concurrent_calls_start_one_tool_with_only_the_contract() {
    let h = Arc::new(host(Value::Null));
    let answers: Vec<Value> = (0..3)
        .map(|_| {
            let h = Arc::clone(&h);
            std::thread::spawn(move || summary(&h, &["one", "seventeen", "two"]).unwrap())
        })
        .collect::<Vec<_>>()
        .into_iter()
        .map(|t| t.join().unwrap())
        .collect();
    let (pid, child) = pids(&answers[0]);
    let calls: BTreeSet<u64> = answers
        .iter()
        .map(|a| a["calls"].as_u64().unwrap())
        .collect();
    assert_eq!(calls, BTreeSet::from([1, 2, 3]));
    for a in &answers {
        assert_eq!(a["pid"], pid);
        assert_eq!(
            (a["count"].clone(), a["longest"].clone()),
            (json!(3), json!("seventeen"))
        );
    }
    let a = &answers[0];
    assert_eq!(
        a["env"],
        json!([
            "PLUXY_APP_ARTIFACT_SHA256",
            "PLUXY_APP_CONTRACT_VERSIONS",
            "PLUXY_EXTENSION_ID"
        ])
    );
    #[cfg(unix)]
    {
        assert_eq!(a["fds"], json!([0, 1, 2]));
        assert_eq!(a["pgid"], pid);
    }
    let s = h.status(ID);
    assert_eq!((s.state, s.pid), ("ready", Some(pid as u32)));
    assert!(alive(child));
    h.stop_all();
    // Reaped; it ended its child as it exited.
    assert!(!alive(pid));
    until("its child to go", 5, || !alive(child));
    assert_eq!(h.status(ID).state, "stopped");
}

#[test]
fn the_tool_outlives_the_thread_that_started_it() {
    let h = Arc::new(host(Value::Null));
    let h2 = Arc::clone(&h);
    let first = std::thread::spawn(move || summary(&h2, &[]).unwrap())
        .join()
        .unwrap();
    std::thread::sleep(Duration::from_millis(400));
    let (pid, _) = pids(&first);
    assert!(
        alive(pid),
        "the tool stopped with the thread that started it"
    );
    assert_eq!(summary(&h, &[]).unwrap()["pid"], pid);
    h.stop_all();
}

#[test]
fn idle_stops_the_whole_group_and_demand_starts_it_again() {
    let h = host(Value::Null);
    let (pid, child) = pids(&summary(&h, &[]).unwrap());
    let t = Instant::now();
    until("the idle stop", 10, || h.status(ID).state == "idle");
    assert!(t.elapsed() + Duration::from_millis(50) >= fast().idle);
    until("the tool and its child to go", 5, || {
        !alive(pid) && !alive(child)
    });
    assert!(h.status(ID).last.unwrap().contains("idle"));
    let again = summary(&h, &[]).unwrap();
    assert_ne!(again["pid"], pid);
    assert_eq!(again["calls"], 1);
    h.stop_all();
}

#[test]
fn a_lease_keeps_it_running() {
    let h = host(Value::Null);
    let (pid, _) = pids(&summary(&h, &[]).unwrap());
    let lease = h.lease(ID).unwrap();
    std::thread::sleep(fast().idle * 3);
    assert!(alive(pid));
    assert_eq!(h.status(ID).state, "active");
    drop(lease);
    until("the idle stop", 10, || !alive(pid));
}

#[test]
fn a_cancelled_call_ends_cancelled_and_the_pipe_stays() {
    let h = Arc::new(host(Value::Null));
    let (pid, _) = pids(&summary(&h, &[]).unwrap());
    let h2 = Arc::clone(&h);
    let waiting = std::thread::spawn(move || h2.call(ID, "wait", json!({}), Some("w-1")));
    until("the wait call", 5, || h.status(ID).state == "active");
    assert!(h.cancel("nope").is_err());
    h.cancel("w-1").unwrap();
    let e = waiting.join().unwrap().unwrap_err();
    assert_eq!(e.code, "cancelled", "{e}");
    let after = summary(&h, &[]).unwrap();
    assert_eq!(
        (after["pid"].clone(), after["calls"].clone()),
        (json!(pid), json!(3))
    );
    h.stop_all();
}

#[test]
fn an_unanswered_cancel_stops_the_tool() {
    let h = Arc::new(host(json!({"ignore_cancel": true})));
    let (pid, child) = pids(&summary(&h, &[]).unwrap());
    let h2 = Arc::clone(&h);
    let waiting = std::thread::spawn(move || h2.call(ID, "wait", json!({}), Some("w-2")));
    until("the wait call", 5, || h.status(ID).state == "active");
    h.cancel("w-2").unwrap();
    let e = waiting.join().unwrap().unwrap_err();
    assert!(
        e.code == "cancelled" && e.message.contains("stopped"),
        "{e}"
    );
    until("the tool to go", 5, || !alive(pid) && !alive(child));
    // The host records the end once its supervisor has reaped the tool, which can come after
    // the processes are gone: wait for the state, not a single read.
    until("the crashed state", 5, || h.status(ID).state == "crashed");
}

#[test]
fn calls_are_checked_before_they_reach_the_tool() {
    let h = host(Value::Null);
    let bad = h
        .call(ID, "summary", json!({"items": "one"}), None)
        .unwrap_err();
    assert_eq!(bad.code, "invalid_request", "{bad}");
    let extra = h
        .call(ID, "summary", json!({"items": [], "more": 1}), None)
        .unwrap_err();
    assert_eq!(extra.code, "invalid_request", "{extra}");
    let big = "x".repeat(MAX_INPUT);
    assert_eq!(
        h.call(ID, "wait", json!({"big": big}), None)
            .unwrap_err()
            .code,
        "invalid_request"
    );
    for op in ["nope", "describe"] {
        assert_eq!(
            h.call(ID, op, json!({}), None).unwrap_err().code,
            "unsupported"
        );
    }
    assert_eq!(
        h.call(ID, "wait", json!({}), Some("bad ticket"))
            .unwrap_err()
            .code,
        "invalid_request"
    );
    // None of them started it, or reached it: its first call is this one.
    assert_eq!(h.status(ID).pid, None, "a refused call started the tool");
    assert_eq!(summary(&h, &[]).unwrap()["calls"], 1);
    h.stop_all();
}

#[test]
fn an_output_its_schema_refuses_fails_the_call() {
    let h = host(json!({"output": {"count": "three"}}));
    let e = summary(&h, &[]).unwrap_err();
    assert_eq!(e.code, "extension_protocol", "{e}");
    h.stop_all();
}

#[test]
fn calls_wait_their_turn_and_are_bounded() {
    let h = Arc::new(host(Value::Null));
    summary(&h, &[]).unwrap();
    let mut waits = Vec::new();
    for i in 0..MAX_PENDING {
        let h2 = Arc::clone(&h);
        waits.push(std::thread::spawn(move || {
            h2.call(ID, "wait", json!({}), Some(&format!("q-{i}")))
        }));
    }
    until("the calls to queue", 5, || {
        let t = h.0.slots().get(ID).and_then(|s| s.run.clone());
        t.is_some_and(|r| {
            let turn = r.gate.turn.lock().unwrap();
            usize::from(turn.0) + turn.1 == MAX_PENDING
        })
    });
    let h2 = Arc::clone(&h);
    let over = std::thread::spawn(move || h2.call(ID, "wait", json!({}), Some("q-over")));
    let over = joined(over, 5, "the call over the bound to be refused").unwrap_err();
    assert_eq!(over.code, "unavailable", "{over}");
    for i in 0..MAX_PENDING {
        h.cancel(&format!("q-{i}")).unwrap();
    }
    for w in waits {
        assert_eq!(w.join().unwrap().unwrap_err().code, "cancelled");
    }
    h.stop_all();
}

#[test]
fn a_mismatched_hello_or_describe_is_incompatible_until_stopped() {
    for mode in [
        json!({"hello_sha256": "cd".repeat(32)}),
        json!({"extra_operation": "more"}),
        json!({"hello_versions": [7]}),
        json!({"state_version": 3}),
    ] {
        let h = host(mode.clone());
        let e = summary(&h, &[]).unwrap_err();
        assert!(e.message.contains("incompatible"), "{mode}: {e}");
        let s = h.status(ID);
        assert_eq!((s.state, s.pid), ("incompatible", None), "{mode}");
        // No new start until its package changes.
        let again = summary(&h, &[]).unwrap_err();
        assert!(again.message.contains("incompatible"), "{again}");
        h.stop(ID);
        assert_eq!(h.status(ID).state, "idle");
        assert!(summary(&h, &[]).is_err());
    }
}

#[test]
fn a_silent_tool_times_out_and_is_reaped() {
    let h = host(json!({"silent": true}));
    let t = Instant::now();
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("did not say hello"), "{e}");
    assert!(t.elapsed() >= fast().startup);
    let s = h.status(ID);
    assert_eq!((s.state, s.pid), ("crashed", None));
}

#[test]
fn a_crash_fails_the_call_and_the_next_call_starts_it_again() {
    let spec = package(json!({"crash_on": "wait"}));
    let h = host_for(spec, fast());
    let (pid, child) = pids(&summary(&h, &[]).unwrap());
    let e = h.call(ID, "wait", json!({}), None).unwrap_err();
    assert_eq!(e.code, "extension_crashed", "{e}");
    assert!(e.unknown, "{e}");
    until("the crash", 5, || h.status(ID).state == "crashed");
    until("its child to go", 5, || !alive(child));
    assert!(h.status(ID).last.unwrap().contains("exited"));
    // After the backoff, a call starts it again.
    std::thread::sleep(fast().backoff);
    assert_ne!(summary(&h, &[]).unwrap()["pid"], pid);
    h.stop_all();
}

#[test]
fn a_tool_that_ignores_the_end_of_its_input_is_killed_after_the_grace() {
    let h = host(json!({"ignore_eof": true}));
    let (pid, child) = pids(&summary(&h, &[]).unwrap());
    let t = Instant::now();
    h.stop(ID);
    until("the kill", 10, || !alive(pid) && !alive(child));
    assert!(t.elapsed() >= fast().stop);
    until("the reap", 5, || h.status(ID).state == "stopped");
}

/// On macOS a native tool never runs below a folder the `admin` group may write
/// (`owned::Writers::Nobody`). The program is a copy of this test program (a link would resolve
/// to it outside the folder), which would otherwise start and answer.
#[cfg(target_os = "macos")]
#[test]
fn on_macos_a_tool_below_a_folder_the_admin_group_may_write_does_not_start() {
    use std::os::unix::fs::PermissionsExt;
    let spec = package(Value::Null);
    let bin = spec.dir.join("bin");
    std::fs::remove_file(&spec.program).unwrap();
    std::fs::copy(std::env::current_exe().unwrap(), &spec.program).unwrap();
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
    let h = host_for(spec, fast());
    let e = summary(&h, &["a"]).unwrap_err();
    assert!(e.message.contains("may be changed by other users"), "{e}");
}

#[test]
fn stopping_a_tool_that_does_not_run_leaves_it_as_it_was() {
    let h = host_for(package(Value::Null), fast());
    h.stop(ID);
    assert_eq!(h.status(ID).state, "idle");
    let e = h
        .call("pluxy/other", "summary", json!({}), None)
        .unwrap_err();
    assert!(e.message.contains("not installed"), "{e}");
    h.stop("pluxy/other");
    assert_eq!(h.status("pluxy/other").state, "idle");
    let (pid, _) = pids(&summary(&h, &[]).unwrap());
    h.stop(ID);
    until("the stop", 5, || {
        h.status(ID).state == "stopped" && !alive(pid)
    });
}

#[test]
fn a_changed_program_or_a_refused_record_starts_nothing() {
    let mut spec = package(Value::Null);
    spec.program_sha256 = "00".repeat(32);
    let h = host_for(spec, fast());
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("changed since it was installed"), "{e}");
    assert_eq!(h.status(ID).pid, None);
    let e = h
        .call("pluxy/other", "summary", json!({}), None)
        .unwrap_err();
    assert!(e.message.contains("not installed"), "{e}");

    let mut spec = package(Value::Null);
    spec.program = Path::new("/bin/sh").into();
    let e = summary(&host_for(spec, fast()), &[]).unwrap_err();
    assert!(e.message.contains("inside its package folder"), "{e}");

    for (op, schema) in [
        ("describe", json!({"type": "object"})),
        ("odd", json!({"type": "object", "pattern": "x"})),
    ] {
        let mut spec = package(Value::Null);
        spec.operations.push(Operation {
            id: op.into(),
            input_schema: schema,
            output_schema: json!({}),
        });
        let h = host_for(spec, fast());
        let e = summary(&h, &[]).unwrap_err();
        assert_eq!(h.status(ID).state, "incompatible", "{op}: {e}");
    }
}

#[test]
fn its_log_is_bounded_escaped_and_redacted() {
    let long = "x".repeat(MAX_LOG_LINE + 10);
    let h = host(json!({"stderr": [
        "plain",
        format!("holds {SECRET} inside"),
        "colour \u{1b}[31m",
        "bidi \u{202e}x",
        long,
        "after",
    ]}));
    private(&h, Stub::default());
    summary(&h, &[]).unwrap();
    until("the log", 5, || {
        h.status(ID).log.iter().any(|l| l == "after")
    });
    let log = h.status(ID).log;
    assert_eq!(
        log,
        vec![
            "plain".to_string(),
            "holds [redacted] inside".into(),
            "colour \\u001b[31m".into(),
            "bidi \\u202ex".into(),
            format!("(a line longer than {MAX_LOG_LINE} bytes, not logged)"),
            "after".into(),
        ]
    );
    assert!(!log.iter().any(|l| l.contains(SECRET)));
    h.stop_all();
}

#[test]
fn stop_all_reaps_every_tool_and_stop_makes_the_next_call_start_again() {
    let h = host(Value::Null);
    let (pid, child) = pids(&summary(&h, &[]).unwrap());
    h.stop(ID);
    until("the stop", 5, || h.status(ID).state == "stopped");
    assert!(!alive(pid) && !alive(child));
    let (pid2, child2) = pids(&summary(&h, &[]).unwrap());
    assert_ne!(pid2, pid);
    h.stop_all();
    assert!(!alive(pid2));
    until("its child to go", 5, || !alive(child2));
}

#[test]
fn the_schema_subset_checks_what_it_supports_and_refuses_the_rest() {
    use schema::{check, supported};
    let s = json!({"type": "object", "properties": {
        "n": {"type": "integer", "minimum": 1, "maximum": 3},
        "s": {"type": ["string", "null"], "minLength": 1, "maxLength": 3},
        "e": {"enum": ["a", "b"]},
        "c": {"const": true},
        "a": {"type": "array", "items": {"type": "number"}, "minItems": 1, "maxItems": 2}},
        "required": ["n"], "additionalProperties": {"type": "boolean"}});
    assert_eq!(supported(&s), Ok(()));
    for ok in [
        json!({"n": 2}),
        json!({"n": 1.0, "s": null, "e": "a", "c": true, "a": [1, 2.5], "other": false}),
    ] {
        assert_eq!(check(&s, &ok, "v"), Ok(()), "{ok}");
    }
    for bad in [
        json!([]),
        json!({}),
        json!({"n": 4}),
        json!({"n": 1.5}),
        json!({"n": 1, "s": ""}),
        json!({"n": 1, "s": "long"}),
        json!({"n": 1, "e": "z"}),
        json!({"n": 1, "c": false}),
        json!({"n": 1, "a": []}),
        json!({"n": 1, "a": ["x"]}),
        json!({"n": 1, "other": 1}),
    ] {
        assert!(check(&s, &bad, "v").is_err(), "{bad}");
    }
    for unsupported in [
        json!([]),
        json!({"pattern": "x"}),
        json!({"type": "date"}),
        json!({"properties": {"x": {"oneOf": []}}}),
        json!({"items": {"$ref": "#"}}),
        json!({"maxLength": -1}),
    ] {
        assert!(supported(&unsupported).is_err(), "{unsupported}");
    }
    let mut deep = json!({"type": "array"});
    for _ in 0..40 {
        deep = json!({"type": "array", "items": deep});
    }
    assert!(supported(&deep).is_err());
}

/// Starts a call of op on its own thread.
fn call_in_thread(
    h: &Arc<Host>,
    op: &'static str,
    input: Value,
) -> std::thread::JoinHandle<Result<Value, CallError>> {
    let h = Arc::clone(h);
    std::thread::spawn(move || h.call(ID, op, input, None))
}

fn retry_in(h: &Host) -> Duration {
    Duration::from_millis(h.status(ID).retry_in_ms.expect("a backoff"))
}

#[test]
fn crashes_back_off_then_quarantine_until_the_package_changes() {
    let h = host(json!({"crash_on": "crash"}));
    let mut waits = Vec::new();
    for n in 1..=5u32 {
        let pid = pids(&summary(&h, &[]).unwrap()).0;
        let e = h.call(ID, "crash", json!({}), None).unwrap_err();
        assert!(e.unknown && e.code == "extension_crashed", "{e}");
        until("the crash", 5, || h.status(ID).pid.is_none());
        let s = h.status(ID);
        assert_eq!(s.crashes, n, "{s:?}");
        if n == 5 {
            assert_eq!(s.state, "quarantined", "{s:?}");
            break;
        }
        assert_eq!(s.state, "crashed", "{s:?}");
        // A call during the backoff is refused and starts nothing.
        let wait = retry_in(&h);
        waits.push(wait);
        let e = summary(&h, &[]).unwrap_err();
        assert!(e.message.contains("restart"), "{e}");
        assert_eq!(h.status(ID).pid, None);
        assert!(!alive(pid));
        std::thread::sleep(wait + Duration::from_millis(50));
    }
    // 300 ms doubling, up to 1200 ms: what is left of it once the crash is seen, which takes
    // longer on a loaded runner (up to 160 ms seen on macOS), still tells each step apart.
    let expect = [300u64, 600, 1200, 1200];
    for (w, e) in waits.iter().zip(expect) {
        assert!(
            *w <= Duration::from_millis(e + 1)
                && *w + Duration::from_millis(250) >= Duration::from_millis(e),
            "{waits:?}"
        );
    }
    // Quarantined: no start, however long one waits.
    std::thread::sleep(fast().backoff_max);
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("quarantined after 5 crashes"), "{e}");
    assert_eq!(h.status(ID).pid, None);
    // Its package changed: a new count.
    h.stop(ID);
    assert_eq!((h.status(ID).state, h.status(ID).crashes), ("idle", 0));
    assert_eq!(summary(&h, &[]).unwrap()["calls"], 1);
    h.stop_all();
}

/// What a router stub saw of a tool's own call.
#[derive(Debug, Clone)]
struct Seen {
    caller: Hop,
    chain: Vec<Hop>,
    package: String,
    op: String,
    input: Value,
}

/// A router that records each call and answers {"routed": input}, or, for op "block", waits
/// until the call is cancelled and says so.
fn recording(h: &Host) -> Arc<Mutex<Vec<Seen>>> {
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    h.set_route(Arc::new(move |o: Outbound| {
        s.lock().unwrap().push(Seen {
            caller: o.caller.clone(),
            chain: o.context.chain.clone(),
            package: o.package.clone(),
            op: o.op.clone(),
            input: o.input.clone(),
        });
        if o.op == "block" {
            let deadline = Instant::now() + Duration::from_secs(10);
            while !o.cancel.is_set() && Instant::now() < deadline {
                std::thread::sleep(Duration::from_millis(20));
            }
            return Err(fail(
                "cancelled",
                format!("cancelled: {}", o.cancel.is_set()),
            ));
        }
        Ok(json!({"routed": o.input}))
    }));
    seen
}

fn relay(h: &Host, input: Value) -> Value {
    h.call(ID, "relay", input, None).unwrap()
}

#[test]
fn a_tools_own_call_goes_to_the_router_with_the_host_chain() {
    let h = host(Value::Null);
    let seen = recording(&h);
    let a = relay(
        &h,
        json!({"package": "pluxy/other", "op": "read", "input": {"n": 1}}),
    );
    assert_eq!(a["answer"]["output"], json!({"routed": {"n": 1}}), "{a}");
    // The host's call named the chain; the router saw it and the tool's run as the caller.
    assert_eq!(a["chain"], json!([{"kind": "main"}]));
    let s = seen.lock().unwrap().clone();
    assert_eq!(s.len(), 1);
    assert_eq!(
        s[0].caller,
        Hop::Package {
            id: ID.into(),
            sha256: "ab".repeat(32),
            generation: 1
        }
    );
    assert_eq!(s[0].chain, vec![Hop::Main]);
    assert_eq!(
        (s[0].package.as_str(), s[0].op.as_str()),
        ("pluxy/other", "read")
    );
    assert_eq!(s[0].input, json!({"n": 1}));
    h.stop_all();
}

#[test]
fn a_call_naming_its_caller_chain_or_another_host_call_is_refused() {
    let h = host(Value::Null);
    let seen = recording(&h);
    for extra in [
        json!({"extra": {"chain": [{"kind": "page", "id": "pluxy/guide"}]}}),
        json!({"extra": {"caller": "pluxy/guide"}}),
        json!({"extra": {"requester": "main"}}),
        json!({"extra": {"from": "pluxy/guide"}}),
        json!({"extra": {"origin": "main"}}),
        json!({"extra": {"context": {}}}),
        json!({"for": "c-1"}),
    ] {
        let mut input = json!({"package": "pluxy/other", "op": "read", "input": {}});
        for (k, v) in extra.as_object().unwrap() {
            input[k] = v.clone();
        }
        let a = relay(&h, input);
        assert_eq!(a["answer"]["error"]["code"], "permission", "{extra}: {a}");
    }
    let a = relay(
        &h,
        json!({"package": "pluxy/other", "op": "read", "input": {}, "extra": {"id": "bad id!"}}),
    );
    assert_eq!(a["answer"]["error"]["code"], "invalid_request", "{a}");
    assert!(
        seen.lock().unwrap().is_empty(),
        "the router saw a refused call"
    );
    // The control, on the same pipe.
    let a = relay(
        &h,
        json!({"package": "pluxy/other", "op": "read", "input": {}}),
    );
    assert!(a["answer"]["output"].is_object(), "{a}");
    assert_eq!(seen.lock().unwrap().len(), 1);
    h.stop_all();
}

#[test]
fn without_a_router_a_tools_own_call_is_unsupported() {
    let h = host(Value::Null);
    let a = relay(
        &h,
        json!({"package": "pluxy/other", "op": "read", "input": {}}),
    );
    assert_eq!(a["answer"]["error"]["code"], "unsupported", "{a}");
    h.stop_all();
}

#[test]
fn a_run_past_the_stable_time_starts_a_new_count() {
    let mut limits = fast();
    limits.stable = Duration::from_millis(600);
    let h = host_for(package(json!({"crash_on": "crash"})), limits);
    summary(&h, &[]).unwrap();
    h.call(ID, "crash", json!({}), None).unwrap_err();
    until("the crash", 5, || h.status(ID).crashes == 1);
    std::thread::sleep(retry_in(&h));
    summary(&h, &[]).unwrap();
    std::thread::sleep(limits.stable);
    h.call(ID, "crash", json!({}), None).unwrap_err();
    until("the crash", 5, || h.status(ID).pid.is_none());
    assert_eq!(h.status(ID).crashes, 1);
    h.stop_all();
}

#[test]
fn cancelling_a_call_cancels_the_tools_own_call_for_it() {
    let h = Arc::new(host(Value::Null));
    let seen = recording(&h);
    let h2 = Arc::clone(&h);
    let t = std::thread::spawn(move || {
        h2.call(
            ID,
            "relay",
            json!({"package": "pluxy/other", "op": "block", "input": {}}),
            Some("relay-1"),
        )
    });
    until("the router to get the call", 5, || {
        !seen.lock().unwrap().is_empty()
    });
    h.cancel("relay-1").unwrap();
    let r = t.join().unwrap().unwrap();
    // The router saw its call cancelled; the tool answered with what the host told it.
    assert_eq!(r["answer"]["error"]["message"], "cancelled: true", "{r}");
    h.stop_all();
}

#[test]
fn a_tool_that_floods_refused_calls_and_reads_no_answer_is_stopped() {
    let h = Arc::new(host(Value::Null));
    let pid = pids(&summary(&h, &[]).unwrap()).0;
    let t = call_in_thread(&h, "flood", json!({}));
    let deadline = Instant::now() + Duration::from_secs(10);
    while !t.is_finished() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    assert!(
        t.is_finished(),
        "the flooding call still runs: the refusals queue without end"
    );
    let e = t.join().unwrap().unwrap_err();
    assert!(e.unknown, "{e}");
    until("the tool to go", 5, || !alive(pid));
    // The host records why it stopped once its supervisor has reaped the tool, which can come
    // after the process is gone: wait for the record, not a single read.
    until("the stop to be recorded", 5, || h.status(ID).last.is_some());
    assert!(
        h.status(ID).last.unwrap_or_default().contains("faster"),
        "{:?}",
        h.status(ID)
    );
    h.stop_all();
}

#[test]
fn a_call_allowed_for_another_release_is_refused() {
    let h = host(Value::Null);
    let ctx = |sha: &str, generation: u64| {
        Arc::new(Context {
            chain: vec![Hop::Main],
            expect: Some((sha.into(), generation)),
            extra: Box::new(()),
            feed: None,
            turn_bounded: false,
            secrets: Vec::new(),
        })
    };
    let e = h
        .run_call_with(
            h.begin_call(None).unwrap(),
            &Instance::local(ID),
            "summary",
            json!({"items": []}),
            ctx(&"ab".repeat(32), 2),
            Cancel::default(),
        )
        .unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let e = h
        .run_call_with(
            h.begin_call(None).unwrap(),
            &Instance::local(ID),
            "summary",
            json!({"items": []}),
            ctx(&"cd".repeat(32), 1),
            Cancel::default(),
        )
        .unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let ok = h.run_call_with(
        h.begin_call(None).unwrap(),
        &Instance::local(ID),
        "summary",
        json!({"items": ["a"]}),
        ctx(&"ab".repeat(32), 1),
        Cancel::default(),
    );
    assert_eq!(ok.unwrap()["count"], 1);
    h.stop_all();
}

#[test]
fn an_unknown_outcome_is_reported_at_the_next_start_and_never_sent_again() {
    let h = Arc::new(host(json!({"crash_on": "crash"})));
    // Known outcomes are not unknown: a refusal before sending, the tool's error, a cancel it
    // answered, a result.
    let refused = h
        .call(ID, "summary", json!({"items": 1}), None)
        .unwrap_err();
    let errored = h.call(ID, "summary", json!({}), None).unwrap_err();
    assert!(!refused.unknown && !errored.unknown, "{refused} {errored}");
    let w = {
        let h = Arc::clone(&h);
        std::thread::spawn(move || h.call(ID, "wait", json!({}), Some("u-1")))
    };
    until("the wait call", 5, || h.status(ID).state == "active");
    h.cancel("u-1").unwrap();
    let cancelled = w.join().unwrap().unwrap_err();
    assert!(!cancelled.unknown, "{cancelled}");
    let first = summary(&h, &[]).unwrap();
    assert!(
        first["reported"].as_array().is_some_and(|a| a.is_empty()),
        "{first}"
    );
    assert!(h.status(ID).unknown.is_empty());

    let e = h.call(ID, "crash", json!({}), None).unwrap_err();
    assert!(e.unknown && e.message.contains("outcome is unknown"), "{e}");
    // What the window shows for it (the native swap check matches these words): no protocol code.
    let shown = e.user_message();
    assert!(
        shown.contains("stopped before it answered crash")
            && shown.contains("outcome is unknown")
            && !shown.contains("extension_crashed"),
        "{shown}"
    );
    until("the crash", 5, || h.status(ID).pid.is_none());
    let unknown = h.status(ID).unknown;
    assert_eq!(unknown.len(), 1, "{unknown:?}");
    assert_eq!(
        (unknown[0].op.as_str(), unknown[0].generation),
        ("crash", 1)
    );
    std::thread::sleep(retry_in(&h));
    // The next start's describe reports it; the crash call itself is not sent again.
    let again = summary(&h, &[]).unwrap();
    assert_eq!(again["calls"], 1);
    assert_eq!(
        again["reported"],
        json!([{"id": unknown[0].call, "op": "crash", "generation": 1}])
    );
    assert!(h.status(ID).unknown.is_empty());
    // Reported once: the start after it reports nothing.
    h.stop(ID);
    until("the stop", 5, || h.status(ID).pid.is_none());
    let third = summary(&h, &[]).unwrap();
    assert_eq!(third["reported"], json!([]));
    h.stop_all();
}

#[test]
fn a_swap_drains_the_old_release_and_serializes_calls_across_releases() {
    let v1 = package(Value::Null);
    let v2 = release(Value::Null, "1.1.0", 2);
    // Not stopped for idleness while the old release drains and is reaped, which a loaded machine
    // can take longer than fast()'s idle delay to do: the swap starts the new one's idle time.
    let h = Arc::new(host_for(
        v1,
        Limits {
            idle: Duration::from_secs(60),
            ..fast()
        },
    ));
    let first = summary(&h, &[]).unwrap();
    let (old, old_child) = pids(&first);
    // Held through the swap (hashing this test program can take longer than the idle delay).
    let held = h.lease(ID).unwrap();

    // Prepared: started and described, but no call reaches it and the old one serves.
    let p = h.prepare_spec(v2, h.stops(ID)).unwrap();
    let new = u64::from(p.run.pid().unwrap());
    assert!(alive(new));
    let slow = call_in_thread(&h, "slow", json!({"ms": 1500}));
    until("the slow call", 5, || {
        h.0.slots()[ID].gate.turn.lock().unwrap().0
    });
    let s = h.status(ID);
    assert_eq!((s.pid, s.generation), (Some(old as u32), Some(1)));
    h.switch(p).unwrap();
    drop(held);
    let s = h.status(ID);
    assert_eq!((s.pid, s.generation), (Some(new as u32), Some(2)));
    assert_eq!(
        s.draining,
        vec![Draining {
            generation: 1,
            pid: Some(old as u32),
            leases: 1
        }]
    );
    // The next call goes to the new release, after the old release's call.
    let next = summary(&h, &[]).unwrap();
    let slow = slow.join().unwrap().unwrap();
    assert_eq!(
        (slow["version"].clone(), slow["pid"].clone()),
        (json!("1.0.0"), json!(old))
    );
    assert_eq!(
        (next["version"].clone(), next["pid"].clone()),
        (json!("1.1.0"), json!(new))
    );
    assert_eq!(next["calls"], 1);
    assert!(next["at"].as_u64().unwrap() >= slow["at"].as_u64().unwrap());
    // Its last call over, the old release stops and is reaped.
    until("the old release to go", 5, || {
        !alive(old) && !alive(old_child) && h.status(ID).draining.is_empty()
    });
    assert!(alive(new), "{:?}", h.status(ID));
    assert_eq!(h.status(ID).state, "ready");
    h.stop_all();
    assert!(!alive(new));
}

#[test]
fn a_failed_or_abandoned_candidate_leaves_the_old_release_serving() {
    let h = host(Value::Null);
    let (old, _) = pids(&summary(&h, &[]).unwrap());
    // Held (hashing this test program can take longer than the idle delay).
    let held = h.lease(ID).unwrap();
    // Incompatible: refused, reaped, and the old release serves on.
    let mut bad = release(json!({"extra_operation": "more"}), "1.2.0", 3);
    bad.sha256 = "ab".repeat(32);
    let e = h.prepare_spec(bad, h.stops(ID)).err().expect("refused");
    assert!(e.message.contains("incompatible"), "{e}");
    let s = h.status(ID);
    assert_eq!(
        (s.state, s.pid, s.generation),
        ("active", Some(old as u32), Some(1))
    );
    // Silent until its startup limit: refused the same way.
    let t = Instant::now();
    let e = h
        .prepare_spec(release(json!({"silent": true}), "1.2.0", 3), h.stops(ID))
        .err()
        .expect("refused");
    assert!(
        e.message.contains("did not say hello") && t.elapsed() >= fast().startup,
        "{e}"
    );
    // Abandoned (dropped without a switch): stopped.
    let p = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), h.stops(ID))
        .unwrap();
    let cand = u64::from(p.run.pid().unwrap());
    drop(p);
    until("the abandoned candidate to go", 5, || !alive(cand));
    // Stopped by a package change before its switch: the switch is refused.
    let p = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), h.stops(ID))
        .unwrap();
    let cand = u64::from(p.run.pid().unwrap());
    h.stop(ID);
    let e = h.switch(p).unwrap_err();
    assert!(e.message.contains("stopped before it could serve"), "{e}");
    drop(held);
    until("the stopped candidate to go", 5, || {
        !alive(cand) && !alive(old)
    });
    // None of them got a call: the old release's next start answers its first.
    let again = summary(&h, &[]).unwrap();
    assert_eq!(
        (again["version"].clone(), again["calls"].clone()),
        (json!("1.0.0"), json!(1))
    );
    h.stop_all();
}

#[test]
fn a_draining_release_is_stopped_at_the_drain_limit() {
    let mut limits = fast();
    limits.drain = Duration::from_millis(500);
    // The new release is not stopped for idleness while the old one drains: the swap starts its
    // idle time, and the drain limit and the reap take most of fast()'s idle delay.
    limits.idle = Duration::from_secs(60);
    let h = Arc::new(host_for(package(Value::Null), limits));
    let (old, _) = pids(&summary(&h, &[]).unwrap());
    let waiting = call_in_thread(&h, "wait", json!({}));
    until("the wait call", 5, || h.status(ID).state == "active");
    let p = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), h.stops(ID))
        .unwrap();
    let t = Instant::now();
    h.switch(p).unwrap();
    let e = waiting.join().unwrap().unwrap_err();
    assert!(e.unknown, "{e}");
    assert!(t.elapsed() >= limits.drain, "{:?}", t.elapsed());
    until("the old release to go", 5, || {
        !alive(old) && h.status(ID).draining.is_empty()
    });
    let u = h.status(ID).unknown;
    assert_eq!((u.len(), u[0].op.as_str(), u[0].generation), (1, "wait", 1));
    // The release serving now is not crashed by it.
    assert_eq!(h.status(ID).state, "ready");
    assert_eq!(summary(&h, &[]).unwrap()["version"], "1.1.0");
    h.stop_all();
}

#[test]
fn after_stop_all_no_call_starts_a_tool() {
    let h = Arc::new(host(json!({"ignore_eof": true})));
    let (pid, child) = pids(&summary(&h, &[]).unwrap());
    // A call waiting for the tool to stop, to start it again.
    h.stop(ID);
    assert_eq!(h.status(ID).state, "draining");
    let waiting = spawn_summary(&h);
    until("the call to wait", 5, || {
        h.0.slots().get(ID).is_some_and(|s| s.waiting == 1)
    });
    h.stop_all();
    let e = joined(waiting, 5, "the waiting call").unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("quitting"), "{e}");
    assert_eq!(h.status(ID).pid, None);
    assert!(!alive(pid));
    until("its child to go", 5, || !alive(child));
}

#[test]
fn cancel_all_ends_every_call_and_the_tool_stops_when_idle() {
    let h = Arc::new(host(Value::Null));
    let (pid, _) = pids(&summary(&h, &[]).unwrap());
    let calls: Vec<_> = [None, Some("all-1")]
        .into_iter()
        .map(|t| {
            let h = Arc::clone(&h);
            std::thread::spawn(move || h.call(ID, "wait", json!({}), t))
        })
        .collect();
    until("the wait calls", 5, || {
        run_of(&h).is_some_and(|r| {
            let turn = r.gate.turn.lock().unwrap();
            turn.0 && turn.1 == 1
        })
    });
    h.cancel_all();
    for c in calls {
        let e = joined(c, 5, "the cancelled call").unwrap_err();
        assert_eq!(e.code, "cancelled", "{e}");
    }
    until("the idle stop", 10, || h.status(ID).state == "idle");
    assert!(!alive(pid));
}

// Unix only: process groups, which a process may leave (on Windows a tool's job holds it and all
// it starts, and none of them can leave it).
#[cfg(unix)]
#[test]
fn a_tool_that_leaves_its_group_and_ignores_the_end_of_its_input_is_killed() {
    let h = host(json!({"ignore_eof": true, "leave_group": true}));
    let out = summary(&h, &[]).unwrap();
    let (pid, child) = pids(&out);
    assert_ne!(out["pgid"], pid, "it did not leave its group");
    h.stop(ID);
    until("the kill", 10, || !alive(pid) && !alive(child));
    until("the reap", 5, || h.status(ID).state == "stopped");
}

#[test]
fn calls_waiting_on_a_failed_start_get_its_error() {
    let starts = Arc::new(AtomicU64::new(0));
    let counted = Arc::clone(&starts);
    let h = Arc::new(host_gated(
        package(json!({"silent": true})),
        fast(),
        move |_| {
            counted.fetch_add(1, Ordering::SeqCst);
        },
    ));
    let first = spawn_summary(&h);
    until("the start", 5, || h.status(ID).state == "starting");
    let calls: Vec<_> = (0..2).map(|_| spawn_summary(&h)).collect();
    until("the calls to wait", 5, || {
        h.0.slots().get(ID).is_some_and(|s| s.waiting == 2)
    });
    for c in std::iter::once(first).chain(calls) {
        let e = c.join().unwrap().unwrap_err();
        assert!(e.message.contains("did not say hello"), "{e}");
    }
    // One start, not one after another.
    assert_eq!(starts.load(Ordering::SeqCst), 1);
}

#[test]
fn calls_waiting_for_a_start_are_bounded() {
    let (h, go) = gated(Value::Null);
    let first = spawn_summary(&h);
    until("the start", 5, || h.status(ID).state == "starting");
    // Leases: calls would also meet the bound on calls waiting their turn.
    let lease = |h: &Arc<Host>| {
        let h = Arc::clone(h);
        std::thread::spawn(move || h.lease(ID).map(|l| l.run.pid()))
    };
    let waiting: Vec<_> = (0..MAX_PENDING).map(|_| lease(&h)).collect();
    until("the calls to wait", 5, || {
        h.0.slots()
            .get(ID)
            .is_some_and(|s| s.waiting == MAX_PENDING)
    });
    let over = joined(lease(&h), 5, "the call over the bound").unwrap_err();
    assert_eq!(over.code, "unavailable", "{over}");
    go.send(()).unwrap();
    let (pid, _) = pids(&joined(first, 5, "the first call").unwrap());
    for w in waiting {
        assert_eq!(joined(w, 5, "a waiting call").unwrap(), Some(pid as u32));
    }
    h.stop_all();
}

#[test]
fn a_stop_while_its_record_is_read_starts_nothing() {
    let (h, go) = gated(Value::Null);
    let call = spawn_summary(&h);
    until("the start", 5, || h.status(ID).state == "starting");
    h.stop(ID);
    go.send(()).unwrap();
    let e = joined(call, 5, "the call").unwrap_err();
    assert!(e.message.contains("stopped while it started"), "{e}");
    let s = h.status(ID);
    assert_eq!((s.state, s.pid), ("stopped", None));
    go.send(()).unwrap();
    summary(&h, &[]).unwrap();
    h.stop_all();
}

// Linux only: the parent-death signal and descriptors read from /proc (the lifetime checks probe
// for an inherited handle on every platform).
#[cfg(target_os = "linux")]
#[test]
fn the_tool_gets_its_name_the_parent_death_signal_and_no_inherited_descriptor() {
    // A descriptor the app leaves inheritable, as a library might: dup clears close-on-exec.
    let fd = unsafe { libc::dup(2) };
    assert!(fd > 2);
    let spec = package(Value::Null);
    let program = spec.program.clone();
    let h = host_for(spec, fast());
    let out = summary(&h, &[]);
    unsafe { libc::close(fd) };
    let out = out.unwrap();
    assert_eq!(out["fds"], json!([0, 1, 2]));
    assert_eq!(out["pdeathsig"], libc::SIGTERM);
    assert_eq!(out["arg0"], json!(program));
    h.stop_all();
}

#[test]
fn a_frame_against_the_contract_breaks_the_pipe() {
    // Just under the frame limit is a frame.
    let h = host(json!({"pad": MAX_FRAME - 1024}));
    summary(&h, &[]).unwrap();
    h.stop_all();
    for (mode, why) in [
        (json!({"pad": MAX_FRAME}), "the limit is"),
        (json!({"unknown_frame": true}), "unknown type"),
    ] {
        let h = host(mode.clone());
        let e = summary(&h, &[]).unwrap_err();
        assert_eq!(e.code, "extension_crashed", "{mode}: {e}");
        until("the stop", 5, || h.status(ID).state == "crashed");
        let last = h.status(ID).last.unwrap();
        assert!(last.contains(why), "{mode}: {last}");
    }
    // A reply before hello is not taken for one: the tool is crashed, not incompatible.
    let h = host(json!({"before_hello": true}));
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("before hello"), "{e}");
    assert_eq!(h.status(ID).state, "crashed", "{e}");
}

#[test]
fn a_call_waiting_its_turn_ends_when_the_tool_stops() {
    let h = Arc::new(host(json!({"ignore_eof": true})));
    summary(&h, &[]).unwrap();
    let h2 = Arc::clone(&h);
    let first = std::thread::spawn(move || h2.call(ID, "wait", json!({}), None));
    until("the wait call", 5, || {
        run_of(&h).is_some_and(|r| r.gate.turn.lock().unwrap().0)
    });
    let second = spawn_summary(&h);
    until("the second call to wait its turn", 5, || {
        run_of(&h).is_some_and(|r| r.gate.turn.lock().unwrap().1 == 1)
    });
    h.stop(ID);
    let e = joined(second, 5, "the waiting call").unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    // It did not wait for the call ahead of it, which ends only with the kill.
    assert!(!first.is_finished());
    assert_eq!(
        joined(first, 5, "the first call").unwrap_err().code,
        "extension_crashed"
    );
}

#[test]
fn a_call_after_an_unanswered_cancel_waits_for_a_new_tool() {
    let h = Arc::new(host(json!({"ignore_cancel": true})));
    let (pid, _) = pids(&summary(&h, &[]).unwrap());
    let h2 = Arc::clone(&h);
    let waiting = std::thread::spawn(move || h2.call(ID, "wait", json!({}), Some("w-3")));
    until("the wait call", 5, || h.status(ID).state == "active");
    h.cancel("w-3").unwrap();
    waiting.join().unwrap().unwrap_err();
    // At once, while the old one is still being stopped: it gets no lease on that one, but waits
    // for it to go. An unanswered cancel is a crash, so that call meets the backoff.
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("may restart it in"), "{e}");
    until("the backoff", 5, || h.status(ID).retry_in_ms.is_none());
    let again = summary(&h, &[]).unwrap();
    assert_ne!(again["pid"], pid);
    h.stop_all();
}

#[test]
fn what_a_tool_says_reaches_the_caller_escaped() {
    let h = host(json!({"error_message": "a\u{202e}b\u{1b}c"}));
    let e = summary(&h, &[]).unwrap_err();
    assert_eq!(
        (e.code.as_str(), e.message.as_str()),
        ("internal", "a\\u202eb\\u001bc")
    );
    h.stop_all();
    let h = host(json!({"hello_sha256": "\u{202e}"}));
    let e = summary(&h, &[]).unwrap_err();
    let last = h.status(ID).last.unwrap();
    for m in [&e.message, &last] {
        assert!(m.contains("\\u202e") && !m.contains('\u{202e}'), "{m}");
    }
}

#[test]
fn its_log_rate_is_bounded() {
    let h = host(json!({"stderr_burst": 500, "stderr": ["after"]}));
    summary(&h, &[]).unwrap();
    until("the log", 5, || {
        h.status(ID).log.iter().any(|l| l == "after")
    });
    let log = h.status(ID).log;
    let n = log.len();
    // The tail keeps every line; only the log leaves lines out.
    assert_eq!(
        log[n - 3..],
        [
            "burst 499".to_string(),
            format!(
                "({} lines left out: more than {LOG_RATE} a second)",
                500 - LOG_RATE
            ),
            "after".into(),
        ]
    );
    h.stop_all();
}

#[test]
fn a_crash_after_a_burst_keeps_its_last_words_and_the_count() {
    let mut lines: Vec<String> = (0..500).map(|i| format!("burst {i}")).collect();
    lines.push("last words".into());
    let h = host(json!({"stderr": lines, "crash_on": "summary"}));
    summary(&h, &[]).unwrap_err();
    until("the crash", 5, || h.status(ID).state == "crashed");
    let s = h.status(ID);
    let n = s.log.len();
    assert_eq!(
        s.log[n - 2..],
        [
            "last words".to_string(),
            format!(
                "({} lines left out: more than {LOG_RATE} a second)",
                501 - LOG_RATE
            ),
        ],
        "{s:?}"
    );
    assert!(
        s.last.as_deref().unwrap().ends_with(": last words"),
        "{s:?}"
    );
}

#[test]
fn a_call_waiting_for_a_start_is_cancelled_at_once() {
    let (h, go) = gated(Value::Null);
    let first = spawn_summary(&h);
    until("the start", 5, || h.status(ID).state == "starting");
    let h2 = Arc::clone(&h);
    let ticketed =
        std::thread::spawn(move || h2.call(ID, "summary", json!({"items": []}), Some("s-1")));
    let plain = spawn_summary(&h);
    until("the calls to wait", 5, || {
        h.0.slots().get(ID).is_some_and(|s| s.waiting == 2)
    });
    h.cancel("s-1").unwrap();
    let e = joined(ticketed, 5, "the cancelled call").unwrap_err();
    assert_eq!(e.code, "cancelled", "{e}");
    h.cancel_all();
    let e = joined(plain, 5, "the call cancel_all reached").unwrap_err();
    assert_eq!(e.code, "cancelled", "{e}");
    // The start goes on for the call that made it, which cancel_all reached too.
    go.send(()).unwrap();
    let e = joined(first, 5, "the first call").unwrap_err();
    assert_eq!(e.code, "cancelled", "{e}");
    h.stop_all();
}

#[test]
fn a_call_begun_before_cancel_all_never_starts_its_tool() {
    let h = host(Value::Null);
    let call = h.begin_call(None).unwrap();
    h.cancel_all();
    let e = h
        .run_call(call, ID, "summary", json!({"items": []}))
        .unwrap_err();
    assert_eq!(e.code, "cancelled", "{e}");
    assert_eq!(h.status(ID).pid, None);
    assert!(h.0.calls.lock().unwrap().is_empty());
}

#[test]
fn a_tool_that_exits_after_describe_backs_off_and_is_quarantined() {
    // Each start describes, then the tool exits: without a backoff, every call restarted it.
    let h = host(json!({"exit_after_describe": true}));
    for n in 1..=5u32 {
        summary(&h, &[]).unwrap_err();
        until("the exit", 5, || {
            let s = h.status(ID);
            s.pid.is_none() && s.crashes == n
        });
        if n == 5 {
            break;
        }
        // During the backoff a call is refused and starts nothing.
        let wait = retry_in(&h);
        let e = summary(&h, &[]).unwrap_err();
        assert!(e.message.contains("may restart it in"), "{e}");
        assert_eq!((h.status(ID).pid, h.status(ID).crashes), (None, n));
        std::thread::sleep(wait + Duration::from_millis(50));
    }
    let e = summary(&h, &[]).unwrap_err();
    assert!(e.message.contains("quarantined after 5 crashes"), "{e}");
    assert_eq!(h.status(ID).pid, None);
    h.stop_all();
}

#[test]
fn after_stop_all_no_candidate_starts_and_a_prepared_one_is_reaped() {
    let h = host(Value::Null);
    summary(&h, &[]).unwrap();
    let p = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), h.stops(ID))
        .unwrap();
    let candidate = u64::from(p.run.pid().unwrap());
    h.stop_all();
    assert!(!alive(candidate));
    let e = h.switch(p).unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let e = h
        .prepare_spec(release(Value::Null, "1.1.0", 3), h.stops(ID))
        .err()
        .expect("refused");
    assert!(e.message.contains("quitting"), "{e}");
    let slots = h.0.slots();
    let slot = &slots[ID];
    assert!(slot.run.is_none() && slot.candidate.is_none() && slot.old.is_empty());
}

#[test]
fn cancel_all_reaches_a_call_on_a_draining_release() {
    // The new release is not stopped for idleness: the swap starts its idle time, and fast()'s
    // idle delay is shorter than a loaded machine can take to reap the old release. Stopped idle,
    // the package would start again from the record, which names the first release.
    let h = Arc::new(host_for(
        package(Value::Null),
        Limits {
            idle: Duration::from_secs(60),
            ..fast()
        },
    ));
    let (old, _) = pids(&summary(&h, &[]).unwrap());
    let waiting = call_in_thread(&h, "wait", json!({}));
    until("the wait call", 5, || h.status(ID).state == "active");
    let p = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), h.stops(ID))
        .unwrap();
    h.switch(p).unwrap();
    assert_eq!(h.status(ID).draining.len(), 1);
    h.cancel_all();
    let e = joined(waiting, 5, "the wait call").unwrap_err();
    assert!(e.code == "cancelled" && !e.unknown, "{e}");
    until("the old release to go", 5, || {
        !alive(old) && h.status(ID).draining.is_empty()
    });
    assert_eq!(summary(&h, &[]).unwrap()["version"], "1.1.0");
    h.stop_all();
}

#[test]
fn what_a_candidate_says_reaches_the_caller_escaped() {
    let h = host(Value::Null);
    summary(&h, &[]).unwrap();
    let e = h
        .prepare_spec(
            release(json!({"hello_sha256": "\u{202e}"}), "1.1.0", 2),
            h.stops(ID),
        )
        .err()
        .expect("refused");
    assert!(
        e.message.contains("\\u202e") && !e.message.contains('\u{202e}'),
        "{e}"
    );
    h.stop_all();
}

#[test]
fn a_stop_after_a_swap_was_staged_refuses_its_candidate() {
    let h = host(Value::Null);
    let (old, _) = pids(&summary(&h, &[]).unwrap());
    // Staged at this count; then the package changed (disabled, removed) and it was stopped.
    let stops = h.stops(ID);
    h.stop(ID);
    until("the stop", 5, || !alive(old));
    let e = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), stops)
        .err()
        .expect("refused");
    assert!(e.message.contains("changed"), "{e}");
    assert!(h.0.slots()[ID].candidate.is_none());
    // A stop of a package that never ran counts too.
    let before = h.stops("pluxy/other");
    h.stop("pluxy/other");
    assert_ne!(h.stops("pluxy/other"), before);
    h.stop_all();
}

#[test]
fn unknown_outcomes_go_to_no_candidate_and_wait_for_the_package_turn() {
    let h = Arc::new(host(json!({"crash_on": "crash"})));
    summary(&h, &[]).unwrap();
    let e = h.call(ID, "crash", json!({}), None).unwrap_err();
    assert!(e.unknown, "{e}");
    until("the crash", 5, || h.status(ID).pid.is_none());
    let call = h.status(ID).unknown[0].call.clone();
    // A swap's candidate may never serve: its describe carries none, and they stay.
    let p = h
        .prepare_spec(
            release(json!({"crash_on": "crash"}), "1.1.0", 2),
            h.stops(ID),
        )
        .unwrap();
    h.switch(p).unwrap();
    assert_eq!(summary(&h, &[]).unwrap()["reported"], json!([]));
    assert_eq!(h.status(ID).unknown.len(), 1);
    // The next start's describe carries them only on the package's turn.
    h.stop(ID);
    until("the stop", 5, || h.status(ID).pid.is_none());
    h.0.slots()[ID].gate.turn.lock().unwrap().0 = true;
    let waiting = spawn_summary(&h);
    std::thread::sleep(Duration::from_millis(800));
    assert_eq!(h.status(ID).state, "starting");
    {
        let slots = h.0.slots();
        let gate = &slots[ID].gate;
        gate.turn.lock().unwrap().0 = false;
        gate.turned.notify_all();
    }
    let r = joined(waiting, 5, "the call").unwrap();
    assert_eq!(r["reported"][0]["id"], json!(call));
    assert!(h.status(ID).unknown.is_empty());
    h.stop_all();
}

#[test]
fn a_clean_stop_ends_the_crashes_in_a_row() {
    let h = host(json!({"crash_on": "crash"}));
    summary(&h, &[]).unwrap();
    h.call(ID, "crash", json!({}), None).unwrap_err();
    until("the crash", 5, || h.status(ID).pid.is_none());
    assert_eq!(h.status(ID).crashes, 1);
    std::thread::sleep(retry_in(&h) + Duration::from_millis(50));
    summary(&h, &[]).unwrap();
    // Stopped when idle, with no crash: the next crash is the first in a row.
    until("the idle stop", 5, || h.status(ID).state == "idle");
    assert_eq!(h.status(ID).crashes, 0);
    summary(&h, &[]).unwrap();
    h.call(ID, "crash", json!({}), None).unwrap_err();
    until("the crash", 5, || h.status(ID).pid.is_none());
    assert_eq!(h.status(ID).crashes, 1);
    h.stop_all();
}

#[test]
fn a_call_with_a_bounded_turn_gives_up_waiting_for_a_busy_package() {
    let h = Arc::new(host(Value::Null));
    // A call holds the package's turn longer than the startup limit (3 s here).
    let slow = call_in_thread(&h, "slow", json!({"ms": 4500}));
    until("the slow call", 5, || {
        h.0.slots()
            .get(ID)
            .is_some_and(|s| s.gate.turn.lock().unwrap().0)
    });
    let started = Instant::now();
    let e = h
        .run_call_with(
            h.begin_call(None).unwrap(),
            &Instance::local(ID),
            "summary",
            json!({"items": []}),
            Arc::new(Context {
                chain: vec![Hop::Main],
                expect: None,
                extra: Box::new(()),
                feed: None,
                turn_bounded: true,
                secrets: Vec::new(),
            }),
            Cancel::default(),
        )
        .unwrap_err();
    assert!(
        e.message.contains("busy") && started.elapsed() < Duration::from_millis(4200),
        "{e} after {:?}",
        started.elapsed()
    );
    // The control: an ordinary call waits for the turn and is answered.
    assert!(summary(&h, &["a"]).is_ok());
    slow.join().unwrap().unwrap();
    h.stop_all();
}

fn sha256_hex(b: &[u8]) -> String {
    Sha256::digest(b)
        .iter()
        .map(|x| format!("{x:02x}"))
        .collect()
}

#[test]
fn a_call_delivers_its_secrets_in_its_frame_and_the_tool_reaches_the_vault_only_for_it() {
    let h = host(Value::Null);
    let stub = private(&h, Stub::default());
    let dir = std::env::temp_dir().join(format!("pluxy-vault-{}", call_id()));
    std::fs::create_dir_all(&dir).unwrap();
    let file = dir.join("config");
    let context = Context {
        chain: vec![Hop::Main],
        expect: None,
        extra: Box::new(()),
        feed: None,
        turn_bounded: false,
        secrets: vec![("r-1".into(), SECRET.into())],
    };
    let call = h.begin_call(None).unwrap();
    let out = h
        .run_call_with(
            call,
            &Instance::local(ID),
            "apply",
            json!({"key": "r-1", "file": file}),
            Arc::new(context),
            Cancel::default(),
        )
        .unwrap();
    // The tool had the secret (it wrote it), and its answer and log have it redacted.
    assert_eq!(
        std::fs::read_to_string(&file).unwrap(),
        format!("key={SECRET}\n")
    );
    assert_eq!(out["echo"], "[redacted]");
    assert!(!out.to_string().contains(SECRET));
    until("its log line", 5, || {
        h.status(ID).log.iter().any(|l| l.contains("applied key"))
    });
    assert!(!h.status(ID).log.iter().any(|l| l.contains(SECRET)));
    assert_eq!(
        *stub.frames.lock().unwrap(),
        vec![
            ("backup.put".to_string(), "apply".to_string(), None),
            ("journal.begin".into(), "apply".into(), None),
            ("journal.commit".into(), "apply".into(), None),
        ]
    );
    // A call with no secrets carries none: the tool says so.
    let e = h
        .call(ID, "apply", json!({"key": "r-1", "file": file}), None)
        .unwrap_err();
    assert!(e.message.contains("no secret was delivered"), "{e}");
    h.stop_all();
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn open_journals_go_in_the_next_describe_and_must_be_ended_before_it_serves() {
    let dir = std::env::temp_dir().join(format!("pluxy-vault-{}", call_id()));
    std::fs::create_dir_all(&dir).unwrap();
    let file = dir.join("config");
    std::fs::write(&file, "new").unwrap();
    let journal = json!({"id": "j-7", "op": "apply", "generation": 1, "began": 1,
        "intent": {"file": file, "sha256": sha256_hex(b"new")}});
    // Reconciled: the file holds what the journal names, so it is ended as committed, in
    // describe, before the call runs.
    let h = host(Value::Null);
    let stub = private(&h, Stub::default());
    stub.journals.lock().unwrap().push(journal.clone());
    let out = h.call(ID, "report", json!({}), None).unwrap();
    assert_eq!(out["reconciled"][0]["journal"], "j-7");
    assert_eq!(out["reconciled"][0]["outcome"], "committed");
    assert_eq!(
        *stub.frames.lock().unwrap(),
        vec![(
            "journal.end".to_string(),
            String::new(),
            Some(vec!["j-7".to_string()])
        )]
    );
    assert!(stub.journals.lock().unwrap().is_empty());
    // The next start names none.
    h.stop(ID);
    until("the stop", 5, || h.status(ID).state == "stopped");
    let out = h.call(ID, "report", json!({}), None).unwrap();
    assert_eq!(out["reconciled"], json!([]));
    h.stop_all();
    // Left open: the start fails, and the call never runs.
    let h = host(Value::Null);
    let stub = private(
        &h,
        Stub {
            keep_open: true,
            ..Default::default()
        },
    );
    stub.journals.lock().unwrap().push(journal);
    let e = h.call(ID, "report", json!({}), None).unwrap_err();
    assert!(e.message.contains("still open"), "{e}");
    h.stop_all();
    let _ = std::fs::remove_dir_all(dir);
}

#[test]
fn describe_reaches_the_vault_only_on_its_packages_turn_for_the_journals_it_was_told_of() {
    let mode = json!({"describe_vault": {"op": "backup.list"}});
    // No journal open and no unknown outcome: it does not hold the turn, so it is refused.
    let h = host(mode.clone());
    let stub = private(&h, Stub::default());
    let out = h.call(ID, "report", json!({}), None).unwrap();
    let refused = &out["reconciled"][0]["describe_vault"]["error"];
    assert_eq!(refused["code"], "permission", "{out}");
    assert!(stub.frames.lock().unwrap().is_empty());
    h.stop_all();
    // A journal open: on the turn, told of it; its other frame names the same journals.
    let h = host(mode);
    let stub = private(&h, Stub::default());
    stub.journals
        .lock()
        .unwrap()
        .push(json!({"id": "j-7", "op": "apply",
        "generation": 1, "began": 1, "intent": {"file": "/nonexistent"}}));
    let out = h.call(ID, "report", json!({}), None).unwrap();
    assert_eq!(out["reconciled"][0]["outcome"], "rolled_back");
    assert_eq!(
        out["reconciled"][1]["describe_vault"]["output"],
        json!({"backups": []})
    );
    let told = Some(vec!["j-7".to_string()]);
    assert_eq!(
        *stub.frames.lock().unwrap(),
        vec![
            ("journal.end".to_string(), String::new(), told.clone()),
            ("backup.list".to_string(), String::new(), told),
        ]
    );
    h.stop_all();
}

#[test]
fn a_start_whose_journals_cannot_be_read_fails() {
    let h = host(Value::Null);
    let stub = private(
        &h,
        Stub {
            unreadable: true,
            ..Default::default()
        },
    );
    let e = h.call(ID, "report", json!({}), None).unwrap_err();
    assert!(e.message.contains("journals"), "{e}");
    assert!(e.message.contains("damaged"), "{e}");
    assert!(stub.frames.lock().unwrap().is_empty());
    h.stop_all();
}

#[test]
fn a_spawn_request_reaches_the_spawner_with_the_served_calls_cancel_and_not_after_its_end() {
    let h = Arc::new(host(Value::Null));
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    h.set_spawner(Arc::new(
        move |_tool: &Hop, _c: &Arc<Context>, frame: &Value, cancel: &Cancel| {
            let deadline = Instant::now() + Duration::from_secs(10);
            while frame["block"] == true && !cancel.is_set() && Instant::now() < deadline {
                std::thread::sleep(Duration::from_millis(20));
            }
            s.lock()
                .unwrap()
                .push((frame["type"].clone(), cancel.is_set()));
            Err(fail("cancelled", "cancelled"))
        },
    ));
    // The control: a request for the call it serves reaches the spawner, not cancelled.
    let a = relay(
        &h,
        json!({"package": "x", "op": "y", "input": {}, "extra": {"type": "spawn"}}),
    );
    assert_eq!(a["answer"]["error"]["code"], "cancelled", "{a}");
    assert_eq!(seen.lock().unwrap().clone(), vec![(json!("spawn"), false)]);
    // A cancel of the call it serves reaches the request under way.
    let h2 = Arc::clone(&h);
    let t = std::thread::spawn(move || {
        h2.call(
            ID,
            "relay",
            json!({"package": "x", "op": "y", "input": {},
                "extra": {"type": "spawn", "block": true}}),
            Some("t-spawn"),
        )
    });
    until("the blocked spawn request", 10, || {
        h.status(ID).leases > 0 && seen.lock().unwrap().len() == 1
    });
    std::thread::sleep(Duration::from_millis(300));
    h.cancel("t-spawn").unwrap();
    let _ = t.join();
    let s = seen.lock().unwrap().clone();
    assert_eq!(s.get(1), Some(&(json!("spawn"), true)), "{s:?}");
    // A request for a call it no longer serves never reaches the spawner.
    let a = relay(
        &h,
        json!({"package": "x", "op": "y", "input": {}, "for": "c-gone",
            "extra": {"type": "runtime"}}),
    );
    assert_eq!(a["answer"]["error"]["code"], "permission", "{a}");
    assert_eq!(seen.lock().unwrap().len(), 2);
}

#[test]
fn a_files_request_reaches_the_saver_only_and_is_refused_without_one() {
    let h = Arc::new(host(Value::Null));
    // No saver yet: refused, and the spawner never sees it.
    let spawned = Arc::new(Mutex::new(0));
    let sp = Arc::clone(&spawned);
    h.set_spawner(Arc::new(
        move |_: &Hop, _: &Arc<Context>, _: &Value, _: &Cancel| {
            *sp.lock().unwrap() += 1;
            Ok(json!({}))
        },
    ));
    let a = relay(
        &h,
        json!({"package": "x", "op": "y", "input": {}, "extra": {"type": "files"}}),
    );
    assert_eq!(a["answer"]["error"]["code"], "unsupported", "{a}");
    assert_eq!(*spawned.lock().unwrap(), 0);
    h.stop_all();
    // With a saver: the request reaches it, with the frame as the tool sent it.
    let h = Arc::new(host(Value::Null));
    let seen = Arc::new(Mutex::new(Vec::new()));
    let s = Arc::clone(&seen);
    h.set_saver(Arc::new(
        move |_tool: &Hop, _c: &Arc<Context>, frame: &Value, cancel: &Cancel| {
            s.lock()
                .unwrap()
                .push((frame["type"].clone(), cancel.is_set()));
            Ok(json!({"outcome": "cancelled"}))
        },
    ));
    let a = relay(
        &h,
        json!({"package": "x", "op": "y", "input": {}, "extra": {"type": "files"}}),
    );
    assert_eq!(a["answer"]["output"]["outcome"], "cancelled", "{a}");
    assert_eq!(seen.lock().unwrap().clone(), vec![(json!("files"), false)]);
    // A request for a call it no longer serves never reaches it.
    let a = relay(
        &h,
        json!({"package": "x", "op": "y", "input": {}, "for": "c-gone",
            "extra": {"type": "files"}}),
    );
    assert_eq!(a["answer"]["error"]["code"], "permission", "{a}");
    assert_eq!(seen.lock().unwrap().len(), 1);
    h.stop_all();
}

#[test]
fn a_start_with_open_journals_that_cannot_take_its_turn_fails_instead_of_describing_none() {
    let h = Arc::new(host(Value::Null));
    let stub = private(&h, Stub::default());
    stub.journals
        .lock()
        .unwrap()
        .push(json!({"id": "j-7", "op": "apply",
        "generation": 1, "began": 1, "intent": {"file": "/nonexistent"}}));
    // An older release still holds the package's turn, past the startup limit (3 s here).
    let held = || h.0.slots().entry(ID.to_string()).or_default().gate.clone();
    let gate = held();
    gate.turn.lock().unwrap().0 = true;
    let started = Instant::now();
    let caller = {
        let h = Arc::clone(&h);
        std::thread::spawn(move || h.call(ID, "report", json!({}), None))
    };
    // Released once the start has failed. A start that went on without the turn is served and
    // its call waits for the turn, so it is released at a deadline well past the startup limit,
    // and the call is answered.
    let deadline = Instant::now() + fast().startup * 3;
    while !caller.is_finished() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
    gate.turn.lock().unwrap().0 = false;
    gate.turned.notify_all();
    let e = caller.join().unwrap().unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    assert!(
        e.message
            .contains("could not take its turn to reconcile 1 journal"),
        "{e}"
    );
    assert!(started.elapsed() >= fast().startup);
    // The journal was never described away: it is still open and no vault frame was taken.
    assert_eq!(stub.journals.lock().unwrap().len(), 1);
    assert!(stub.frames.lock().unwrap().is_empty());
    // On a later start, with the turn free, it is told of and reconciled.
    std::thread::sleep(fast().backoff + Duration::from_millis(100));
    let out = h.call(ID, "report", json!({}), None).unwrap();
    assert_eq!(out["reconciled"][0]["journal"], "j-7");
    assert!(stub.journals.lock().unwrap().is_empty());
    h.stop_all();
}

#[test]
fn a_call_unanswered_when_its_tool_exits_is_known_to_the_slot_before_its_thread_wakes() {
    let h = Arc::new(host(Value::Null));
    let slow = call_in_thread(&h, "slow", json!({"ms": 8000}));
    let run = || h.0.slots().get(ID).and_then(|s| s.run.clone());
    until("the call is sent", 5, || {
        run().is_some_and(|r| !r.pending.lock().unwrap().is_empty())
    });
    std::thread::sleep(Duration::from_millis(200));
    // Hold up the call's thread where it ends: it wakes only once the tool has been reaped, as
    // it can when it is slow to get a processor.
    let run = run().unwrap();
    let stall = run.serving.lock().unwrap();
    h.stop(ID);
    until("the stop", 5, || h.status(ID).state == "stopped");
    // The next start's describe looks now: the call is already of unknown outcome.
    let known = h.status(ID).unknown;
    assert_eq!(known.len(), 1, "the slot does not know the unanswered call");
    assert_eq!(known[0].op, "slow");
    drop(stall);
    let e = slow.join().unwrap().unwrap_err();
    assert!(e.unknown, "{e}");
    // Recorded once, not again by its thread.
    assert_eq!(h.status(ID).unknown.len(), 1);
    // The next start reports it, and the tool serves.
    summary(&h, &[]).unwrap();
    assert!(h.status(ID).unknown.is_empty());
    h.stop_all();
}

#[test]
fn stopping_a_crashed_tool_that_is_not_running_reads_as_idle() {
    let h = host(json!({"crash_on": "crash"}));
    summary(&h, &[]).unwrap();
    h.call(ID, "crash", json!({}), None).unwrap_err();
    until("the crash", 5, || h.status(ID).state == "crashed");
    assert_eq!(h.status(ID).crashes, 1);
    h.stop(ID);
    let s = h.status(ID);
    assert_eq!(s.state, "idle");
    assert_eq!(s.crashes, 0);
    assert_eq!(s.retry_in_ms, None);
    // Its next call starts it at once, with no backoff.
    summary(&h, &[]).unwrap();
    h.stop_all();
}

#[test]
fn a_tool_gets_its_home_and_its_own_data_folder_and_a_planted_link_refuses_the_start() {
    let base = Folder(package(Value::Null).dir);
    let home = base.0.join("home");
    let tools = base.0.join("data").join("tools");
    let system = base.0.join("system");
    std::fs::create_dir_all(&home).unwrap();
    let h = host(Value::Null);
    h.set_tool_env(ToolEnv {
        home: home.clone(),
        tools: tools.clone(),
        system: vec![("SystemRoot", system)],
    });
    let a = summary(&h, &["one"]).unwrap();
    assert_eq!(
        a["env"],
        json!([
            "HOME",
            "PLUXY_APP_ARTIFACT_SHA256",
            "PLUXY_APP_CONTRACT_VERSIONS",
            "PLUXY_EXTENSION_ID",
            "PLUXY_TOOL_DATA_DIR",
            "SystemRoot"
        ])
    );
    assert_eq!(
        (a["home"].clone(), a["data_dir"].clone()),
        (json!(home), json!(tools.join("pluxy").join("test")))
    );
    for dir in [
        tools.clone(),
        tools.join("pluxy"),
        tools.join("pluxy").join("test"),
    ] {
        let m = std::fs::symlink_metadata(&dir).unwrap();
        assert!(m.is_dir(), "{}", dir.display());
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(m.permissions().mode() & 0o777, 0o700, "{}", dir.display());
        }
        #[cfg(windows)]
        assert!(
            crate::private_windows::tests::private(&dir),
            "{}",
            dir.display()
        );
    }
    h.stop_all();

    // A link planted where the publisher's folder goes (a junction on Windows): the start is
    // refused, and nothing is made where it points.
    let elsewhere = base.0.join("elsewhere");
    std::fs::create_dir_all(&elsewhere).unwrap();
    let tools = base.0.join("data2").join("tools");
    std::fs::create_dir_all(&tools).unwrap();
    #[cfg(unix)]
    std::os::unix::fs::symlink(&elsewhere, tools.join("pluxy")).unwrap();
    #[cfg(windows)]
    crate::private_windows::tests::junction(&elsewhere, &tools.join("pluxy"));
    let h = host(Value::Null);
    h.set_tool_env(ToolEnv {
        home,
        tools,
        system: Vec::new(),
    });
    let e = summary(&h, &["one"]).unwrap_err();
    assert!(e.message.contains("data folder"), "{e:?}");
    assert!(std::fs::read_dir(&elsewhere).unwrap().next().is_none());
    assert_eq!(h.status(ID).pid, None);
}

// Lifetimes, on every platform ----------------------------------------------------------------
//
// What each kind of process does when its tool idles or crashes, when the app quits or crashes,
// and what a stop does, as `app-host/README.md` states it for each platform. The managed runtime
// and the detached launch are the fixture's heartbeat (`fixture::heartbeat`): a process lives
// while its heartbeat file advances. Each process a check starts ends through its stop file (a
// `Lab`) or through the check's own handle of it (`Held`), also when an assertion fails; never by
// a process ID.

/// The variable that makes this test program a lifetime check's host (`lifetime_host`): its role.
const LIFETIME_HOST: &str = "PLUXY_LIFETIME_TEST_HOST";
/// And the host's folder.
const LIFETIME_DIR: &str = "PLUXY_LIFETIME_TEST_DIR";
/// How long a stop waits, once it has asked a runtime to stop, before it ends it: five seconds
/// (`app-host/README.md`).
const GRACE: Duration = Duration::from_secs(5);

/// path with `.ext` added to its name.
fn with(path: &Path, ext: &str) -> PathBuf {
    let mut s = path.as_os_str().to_owned();
    s.push(".");
    s.push(ext);
    s.into()
}

/// The heartbeat file of heartbeat f's child (`child`).
fn child_of(f: &Path) -> PathBuf {
    with(f, "child")
}

/// p without the `\\?\` that `canonicalize` puts before a Windows path: the form a process sees
/// its working folder in.
fn plain(p: PathBuf) -> PathBuf {
    #[cfg(windows)]
    if let Some(s) = p.to_str().and_then(|s| s.strip_prefix(r"\\?\")) {
        if !s.starts_with(r"UNC\") {
            return PathBuf::from(s);
        }
    }
    p
}

/// Folder dir, opened as a launch's cwd is (on Windows a folder opens only with backup
/// semantics).
fn open_dir(dir: &Path) -> std::fs::File {
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
    std::fs::File::open(dir).unwrap()
}

/// Starts cmd as std starts a process. On Windows under the app's spawn lock, as the app's own
/// such spawns are (`owned::windows::std_spawn`), so it is not passed a handle another check's
/// spawn has made inheritable for a moment.
fn spawn_std(cmd: &mut std::process::Command) -> std::process::Child {
    #[cfg(windows)]
    {
        crate::owned::windows::std_spawn(cmd).unwrap()
    }
    #[cfg(not(windows))]
    {
        cmd.spawn().unwrap()
    }
}

/// A process the check started itself and holds: ended through its handle when dropped.
struct Held(std::process::Child);

impl Drop for Held {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

/// Runs its function when dropped, also when an assertion fails.
struct OnDrop<F: FnMut()>(F);

impl<F: FnMut()> Drop for OnDrop<F> {
    fn drop(&mut self) {
        (self.0)()
    }
}

/// A lifetime check's folder and the heartbeats named in it. Dropped, it asks each heartbeat (and
/// its child) to stop through its stop file, waits until each has exited and removes the folder;
/// should one still run, the folder stays, with its stop file, and it is reported.
struct Lab {
    dir: PathBuf,
    beats: Mutex<Vec<PathBuf>>,
}

impl Lab {
    fn new(name: &str) -> Lab {
        let mut rnd = [0u8; 6];
        getrandom::fill(&mut rnd).unwrap();
        let tag: String = rnd.iter().map(|b| format!("{b:02x}")).collect();
        let dir = std::env::temp_dir().join(format!("pluxy-lifetime-{name}-{tag}"));
        std::fs::create_dir_all(&dir).unwrap();
        Lab {
            dir: plain(std::fs::canonicalize(&dir).unwrap()),
            beats: Mutex::default(),
        }
    }

    /// Heartbeat file name in the folder, to be stopped with it (its child too).
    fn beat(&self, name: &str) -> PathBuf {
        let f = self.dir.join(name);
        let mut beats = self.beats.lock().unwrap();
        beats.push(f.clone());
        beats.push(child_of(&f));
        f
    }
}

impl Drop for Lab {
    fn drop(&mut self) {
        let beats = std::mem::take(&mut *self.beats.lock().unwrap_or_else(|e| e.into_inner()));
        for f in &beats {
            let _ = std::fs::write(with(f, "stop"), "");
        }
        let running = || -> Vec<u64> {
            beats
                .iter()
                .filter_map(|f| wrote(f))
                .filter(runs)
                .filter_map(|v| v["pid"].as_u64())
                .collect()
        };
        let deadline = Instant::now() + Duration::from_secs(10);
        let mut left = running();
        while !left.is_empty() && Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(50));
            left = running();
        }
        if left.is_empty() {
            let _ = std::fs::remove_dir_all(&self.dir);
        } else {
            eprintln!(
                "processes {left:?} still run; {} keeps their stop files",
                self.dir.display()
            );
        }
    }
}

/// What heartbeat f wrote of itself as it started (`<f>.json`), once it has.
fn wrote(f: &Path) -> Option<Value> {
    std::fs::read(with(f, "json"))
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
}

/// The process ID heartbeat f wrote, once it has.
fn pid_of(f: &Path) -> Option<u64> {
    wrote(f).and_then(|v| v["pid"].as_u64())
}

/// What heartbeat f wrote of itself as it started (`<f>.json`), waiting for it.
fn seen(f: &Path) -> Value {
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        if let Some(v) = wrote(f) {
            return v;
        }
        assert!(Instant::now() < deadline, "{} never started", f.display());
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// Whether heartbeat f's process runs: its count advances (it writes one every 50 ms).
fn beating(f: &Path) -> bool {
    let count = || -> Option<u64> { std::fs::read_to_string(f).ok()?.trim().parse().ok() };
    let deadline = Instant::now() + Duration::from_secs(5);
    let mut first = count();
    while Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
        match (first, count()) {
            (Some(a), Some(b)) if b > a => return true,
            (None, now) => first = now,
            _ => {}
        }
    }
    false
}

/// Whether the heartbeat that wrote seen (its `<f>.json`) runs. On Windows the process at its ID
/// must also have been created when the heartbeat was: Windows may give the ID of an ended process
/// to the next one it starts, here another check's process, as soon as the last handle to the
/// ended one has closed.
fn runs(seen: &Value) -> bool {
    let pid = seen["pid"].as_u64().unwrap();
    #[cfg(windows)]
    {
        use std::os::windows::io::AsRawHandle;
        use windows_sys::Win32::Foundation::WAIT_TIMEOUT;
        use windows_sys::Win32::System::Threading::{
            WaitForSingleObject, PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE,
        };
        let created = seen["created"].as_u64();
        assert!(created.is_some(), "the heartbeat wrote no creation time");
        look_at(pid, PROCESS_SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION).is_some_and(|h| {
            fixture::created(h.as_raw_handle()) == created
                && unsafe { WaitForSingleObject(h.as_raw_handle(), 0) } == WAIT_TIMEOUT
        })
    }
    #[cfg(not(windows))]
    alive(pid)
}

/// Whether heartbeat f's process has exited, within ten seconds.
fn ends(f: &Path) -> bool {
    let seen = seen(f);
    let deadline = Instant::now() + Duration::from_secs(10);
    while runs(&seen) {
        if Instant::now() >= deadline {
            eprintln!(
                "heartbeat {} (process {}) still runs after 10 s; its count {}",
                f.display(),
                seen["pid"],
                if beating(f) {
                    "advances"
                } else {
                    "stands still"
                }
            );
            return false;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    true
}

/// A launch of the heartbeat f, in f's folder, named as this program's path, with args after f.
fn heartbeat(f: &Path, args: &[&str], lifetime: Lifetime) -> Launch {
    let mut argv = vec![
        fixture::HEARTBEAT.to_string(),
        f.to_string_lossy().into_owned(),
    ];
    argv.extend(args.iter().map(|a| a.to_string()));
    Launch {
        opened: Box::new(crate::spawn::Plain {
            executable: std::fs::File::open(std::env::current_exe().unwrap()).unwrap(),
            cwd: open_dir(f.parent().unwrap()),
        }),
        named: std::env::current_exe().unwrap(),
        argv,
        lifetime,
        request: Default::default(),
        secrets: vec![],
        stdin: None,
    }
}

/// The lifetime checks' secrets: each its ID with SECRET before it.
struct Canary;

impl Values for Canary {
    fn value(&self, _: &str, id: &str) -> Result<(String, Option<u64>), String> {
        Ok((format!("{SECRET}-{id}"), None))
    }
    fn still(&self, _: &[crate::spawn::Given]) -> Result<(), String> {
        Ok(())
    }
}

/// Whether detached launch f started, from what its start answered (r). Once started it beats.
/// On Windows a detached launch runs outside every job of the app's or not at all: where the job
/// the check runs in may not be left (`owned::windows::breakaway` says so; GitHub's hosted runners
/// run every step in such a job) it must be refused (`NO_BREAKAWAY`) before any of it ran; where
/// it runs, it is in no job.
fn let_go(f: &Path, r: Result<(), String>) -> bool {
    #[cfg(windows)]
    {
        use crate::owned::windows::{breakaway, NO_BREAKAWAY};
        let refused = r.as_ref().is_err_and(|e| e.contains(NO_BREAKAWAY));
        if let Err(why) = breakaway() {
            assert_eq!(why, NO_BREAKAWAY);
            assert!(
                refused,
                "a detached launch was not refused in a job it may not leave: {r:?}"
            );
        }
        // Refused although breakaway() allowed it: a job further out does not (also refused).
        if refused {
            std::thread::sleep(Duration::from_millis(500));
            assert!(
                !with(f, "json").exists() && !f.exists(),
                "a refused detached launch ran"
            );
            return false;
        }
    }
    r.unwrap();
    assert!(beating(f), "{} does not run", f.display());
    #[cfg(windows)]
    assert!(
        !in_a_job(seen(f)["pid"].as_u64().unwrap()),
        "a detached launch runs in a job"
    );
    true
}

/// Whether process pid is in a job (a look at it; nothing is asked of it).
#[cfg(windows)]
fn in_a_job(pid: u64) -> bool {
    use std::os::windows::io::AsRawHandle;
    use windows_sys::Win32::System::JobObjects::IsProcessInJob;
    use windows_sys::Win32::System::Threading::PROCESS_QUERY_LIMITED_INFORMATION;
    let h = look_at(pid, PROCESS_QUERY_LIMITED_INFORMATION)
        .unwrap_or_else(|| panic!("process {pid}: {}", std::io::Error::last_os_error()));
    let mut b = 0;
    let ok = unsafe { IsProcessInJob(h.as_raw_handle(), std::ptr::null_mut(), &mut b) };
    assert_ne!(ok, 0, "{}", std::io::Error::last_os_error());
    b != 0
}

/// The host's spawn service for the lifetime checks (`Host::set_spawner`): a tool's `spawn` frame
/// starts the heartbeat it names (`file`, in dir, with `args`) for the tool's package, managed in
/// rt or detached as its `lifetime` says; a `runtime` frame's op (status, stop, restart) goes to
/// rt, for the tool's package.
fn spawner(rt: &Arc<Runtimes>, dir: &Path) -> Spawner {
    let (rt, dir) = (Arc::clone(rt), dir.to_path_buf());
    Arc::new(
        move |tool: &Hop, _: &Arc<Context>, frame: &Value, _: &Cancel| {
            let package = tool.package().unwrap_or_default();
            let r = if frame["type"] == "spawn" {
                let f = dir.join(frame["file"].as_str().unwrap_or_default());
                let args: Vec<&str> = frame["args"]
                    .as_array()
                    .map(|a| a.iter().filter_map(Value::as_str).collect())
                    .unwrap_or_default();
                if frame["lifetime"] == "managed" {
                    rt.start(package, heartbeat(&f, &args, Lifetime::Managed))
                } else {
                    crate::spawn::detached(
                        &heartbeat(&f, &args, Lifetime::Detached),
                        package,
                        &Canary,
                    )
                    .map(|()| json!({"lifetime": "detached"}))
                }
            } else {
                let id = frame["runtime"].as_str().unwrap_or_default();
                match frame["op"].as_str() {
                    Some("status") => rt.status(package, id),
                    Some("stop") => rt.stop(package, id),
                    Some("restart") => rt.restart(package, id),
                    _ => Err("no such runtime op".into()),
                }
            };
            r.map_err(|e| fail("refused", e))
        },
    )
}

/// The tool's request to the host's spawn service (a frame with fields extra, for the call it
/// serves) and its answer: the output, or the error's message.
fn request(h: &Host, extra: Value) -> Result<Value, String> {
    let a = relay(
        h,
        json!({"package": "-", "op": "-", "input": {}, "extra": extra}),
    );
    match a["answer"].get("output") {
        Some(o) => Ok(o.clone()),
        None => Err(a["answer"]["error"]["message"]
            .as_str()
            .unwrap_or_default()
            .to_string()),
    }
}

/// Limits that keep a tool running between a check's calls.
fn steady() -> Limits {
    Limits {
        idle: Duration::from_secs(120),
        ..fast()
    }
}

/// This test program started again as a lifetime check's host (`lifetime_host`) in role, in
/// folder dir (its stderr in `host.log` there), held by the check. On Windows with no console,
/// as the app has none (it is a windowed program): only a process without one may ask on a
/// runtime's console.
fn start_host(role: &str, dir: &Path) -> Held {
    let log = std::fs::File::create(dir.join("host.log")).unwrap();
    let mut cmd = std::process::Command::new(std::env::current_exe().unwrap());
    cmd.env(LIFETIME_HOST, role)
        .env(LIFETIME_DIR, dir)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(log);
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        cmd.creation_flags(windows_sys::Win32::System::Threading::DETACHED_PROCESS);
    }
    Held(spawn_std(&mut cmd))
}

/// What the host wrote to its log.
fn host_log(dir: &Path) -> String {
    std::fs::read_to_string(dir.join("host.log")).unwrap_or_default()
}

/// Waits, at most secs, until the host has written file in dir (or exited, when file is None).
fn host_wrote(host: &mut Held, dir: &Path, file: Option<&str>, secs: u64) {
    let deadline = Instant::now() + Duration::from_secs(secs);
    loop {
        let exited = host.0.try_wait().unwrap();
        match (file, exited) {
            (Some(f), _) if dir.join(f).exists() => return,
            (Some(f), Some(s)) => panic!("the host exited ({s}) before {f}: {}", host_log(dir)),
            (None, Some(s)) if s.success() => return,
            (None, Some(s)) => panic!("the host failed ({s}): {}", host_log(dir)),
            _ => {}
        }
        assert!(
            Instant::now() < deadline,
            "the host took too long: {}",
            host_log(dir)
        );
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// A lifetime check's host (`start_host`), which never returns:
/// - `crash`: starts a tool (its process, its child and its package folder in `tool.json`), a
///   managed runtime with a child (heartbeat `runtime`) recorded in `runtimes.json`, and a
///   detached launch (heartbeat `agent`, or `agent.refused` saying why it was refused), writes
///   `ready` and waits to be killed;
/// - `stop`: starts two managed runtimes with a child each, `handles` and `ignores` (which goes on
///   when asked to stop), stops both at once, writes how long each stop took and what it
///   answered to `<name>.stopped`, and exits.
fn lifetime_host(role: &str, dir: &Path) -> ! {
    match role {
        "crash" => {
            let rt = Runtimes::open(Some(dir.join("runtimes.json")), Box::new(Canary));
            rt.start(
                ID,
                heartbeat(&dir.join("runtime"), &["child"], Lifetime::Managed),
            )
            .expect("the runtime");
            let agent = dir.join("agent");
            if let Err(e) =
                crate::spawn::detached(&heartbeat(&agent, &[], Lifetime::Detached), ID, &Canary)
            {
                let _ = std::fs::write(with(&agent, "refused"), e);
            }
            let spec = package(Value::Null);
            let folder = spec.dir.clone();
            let h = host_for(spec, steady());
            let out = summary(&h, &[]).expect("the tool");
            let _ = std::fs::write(
                dir.join("tool.json"),
                json!({"pid": out["pid"], "child": out["child"], "package": folder}).to_string(),
            );
            let _ = std::fs::write(dir.join("ready"), "");
            loop {
                std::thread::sleep(Duration::from_secs(3600));
            }
        }
        "stop" => {
            let rt = Arc::new(Runtimes::open(None, Box::new(Canary)));
            let ids: Vec<(&str, String)> = [
                ("handles", &["child"][..]),
                ("ignores", &["child", "ignore"][..]),
            ]
            .into_iter()
            .map(|(name, args)| {
                let f = dir.join(name);
                let v = rt
                    .start(ID, heartbeat(&f, args, Lifetime::Managed))
                    .expect("the runtime");
                // Asked once each has set what it does when asked to stop, child and all.
                let _ = seen(&f);
                let _ = seen(&child_of(&f));
                (name, v["runtime"].as_str().unwrap_or_default().to_string())
            })
            .collect();
            let stops: Vec<_> = ids
                .into_iter()
                .map(|(name, id)| {
                    let (rt, dir) = (Arc::clone(&rt), dir.to_path_buf());
                    std::thread::spawn(move || {
                        let t = Instant::now();
                        let r = rt.stop(ID, &id);
                        let ms = t.elapsed().as_millis() as u64;
                        let answer = r.unwrap_or_else(|e| json!({"error": e}));
                        let _ = std::fs::write(
                            dir.join(format!("{name}.stopped")),
                            json!({"ms": ms, "answer": answer}).to_string(),
                        );
                    })
                })
                .collect();
            for s in stops {
                let _ = s.join();
            }
            std::process::exit(0);
        }
        _ => std::process::exit(2),
    }
}

#[test]
fn lifetime_a_tools_idle_stop_leaves_its_detached_launch_and_managed_runtime_running() {
    let lab = Lab::new("idle");
    let (runtime, agent) = (lab.beat("runtime"), lab.beat("agent"));
    let rt = Arc::new(Runtimes::open(None, Box::new(Canary)));
    let h = host(Value::Null);
    let _stop = OnDrop(|| h.stop_all());
    h.set_spawner(spawner(&rt, &lab.dir));
    let id = request(
        &h,
        json!({"type": "spawn", "file": "runtime", "lifetime": "managed", "args": ["child"]}),
    )
    .unwrap()["runtime"]
        .as_str()
        .unwrap()
        .to_string();
    let detached = let_go(
        &agent,
        request(
            &h,
            json!({"type": "spawn", "file": "agent", "lifetime": "detached"}),
        )
        .map(drop),
    );
    let (tool, tool_child) = pids(&summary(&h, &[]).unwrap());
    let pid = seen(&runtime)["pid"].clone();
    // The tool stops for idleness, child and all.
    until("the idle stop", 10, || h.status(ID).state == "idle");
    until("the tool and its child to end", 10, || {
        !alive(tool) && !alive(tool_child)
    });
    assert!(
        beating(&runtime) && beating(&child_of(&runtime)),
        "the tool's idle stop stopped its package's managed runtime"
    );
    assert!(
        !detached || beating(&agent),
        "the tool's idle stop stopped its detached launch"
    );
    // The tool's next run sees the same runtime, running.
    let s = request(
        &h,
        json!({"type": "runtime", "op": "status", "runtime": id}),
    )
    .unwrap();
    assert_eq!((&s["state"], &s["pid"]), (&json!("running"), &pid), "{s}");
    assert_ne!(h.status(ID).pid, Some(tool as u32));
}

#[test]
fn lifetime_a_tools_crash_leaves_its_launches_running_and_its_next_run_restarts_and_stops_its_runtime(
) {
    let lab = Lab::new("tool-crash");
    let (runtime, agent) = (lab.beat("runtime"), lab.beat("agent"));
    let rt = Arc::new(Runtimes::open(None, Box::new(Canary)));
    let h = host_for(package(json!({"crash_on": "crash"})), steady());
    let _stop = OnDrop(|| h.stop_all());
    h.set_spawner(spawner(&rt, &lab.dir));
    let id = request(
        &h,
        json!({"type": "spawn", "file": "runtime", "lifetime": "managed", "args": ["child"]}),
    )
    .unwrap()["runtime"]
        .as_str()
        .unwrap()
        .to_string();
    let detached = let_go(
        &agent,
        request(
            &h,
            json!({"type": "spawn", "file": "agent", "lifetime": "detached"}),
        )
        .map(drop),
    );
    let (old, old_child) = (
        seen(&runtime)["pid"].as_u64().unwrap(),
        seen(&child_of(&runtime))["pid"].as_u64().unwrap(),
    );
    let (tool, tool_child) = pids(&summary(&h, &[]).unwrap());
    // The tool crashes; its own child goes with it (its group; on Windows its job).
    let e = h.call(ID, "crash", json!({}), None).unwrap_err();
    assert_eq!(e.code, "extension_crashed", "{e}");
    until("the crash", 5, || h.status(ID).state == "crashed");
    until("the tool and its child to end", 10, || {
        !alive(tool) && !alive(tool_child)
    });
    assert!(
        beating(&runtime) && beating(&child_of(&runtime)),
        "the tool's crash stopped its package's managed runtime"
    );
    assert!(
        !detached || beating(&agent),
        "the tool's crash stopped its detached launch"
    );
    // After the backoff the tool runs again, a new process, and controls its package's runtime.
    std::thread::sleep(fast().backoff + Duration::from_millis(100));
    let s = request(
        &h,
        json!({"type": "runtime", "op": "status", "runtime": id}),
    )
    .unwrap();
    assert_eq!(
        (&s["state"], &s["pid"]),
        (&json!("running"), &json!(old)),
        "{s}"
    );
    assert!(h.status(ID).pid.is_some_and(|p| u64::from(p) != tool));
    // A restart replaces the runtime, child and all.
    let r = request(
        &h,
        json!({"type": "runtime", "op": "restart", "runtime": id}),
    )
    .unwrap();
    let new = r["pid"].as_u64().unwrap();
    assert_ne!(new, old);
    assert_eq!(
        (&r["state"], &r["restarts"]),
        (&json!("running"), &json!(1)),
        "{r}"
    );
    until("the old runtime and its child to end", 10, || {
        !alive(old) && !alive(old_child)
    });
    until("the new runtime to start", 20, || {
        pid_of(&runtime) == Some(new)
    });
    until("the new runtime's child to start", 20, || {
        pid_of(&child_of(&runtime)).is_some_and(|p| p != old_child)
    });
    let new_child = pid_of(&child_of(&runtime)).unwrap();
    assert!(beating(&runtime) && beating(&child_of(&runtime)));
    // A stop ends it, child and all, and the tool's package controls nothing more of it.
    let s = request(&h, json!({"type": "runtime", "op": "stop", "runtime": id})).unwrap();
    assert_eq!(s["state"], "exited", "{s}");
    until("the runtime and its child to end", 10, || {
        !alive(new) && !alive(new_child)
    });
    assert!(
        !detached || beating(&agent),
        "a runtime's stop stopped a detached launch"
    );
}

#[test]
fn lifetime_the_apps_quit_stops_its_tools_and_managed_runtimes_and_leaves_detached_launches() {
    let lab = Lab::new("quit");
    let (runtime, agent) = (lab.beat("runtime"), lab.beat("agent"));
    let file = lab.dir.join("runtimes.json");
    let rt = Arc::new(Runtimes::open(Some(file.clone()), Box::new(Canary)));
    let h = host_for(package(Value::Null), steady());
    let _stop = OnDrop(|| h.stop_all());
    h.set_spawner(spawner(&rt, &lab.dir));
    let id = request(
        &h,
        json!({"type": "spawn", "file": "runtime", "lifetime": "managed", "args": ["child"]}),
    )
    .unwrap()["runtime"]
        .as_str()
        .unwrap()
        .to_string();
    let detached = let_go(
        &agent,
        request(
            &h,
            json!({"type": "spawn", "file": "agent", "lifetime": "detached"}),
        )
        .map(drop),
    );
    let (tool, tool_child) = pids(&summary(&h, &[]).unwrap());
    let (pid, child) = (
        seen(&runtime)["pid"].as_u64().unwrap(),
        seen(&child_of(&runtime))["pid"].as_u64().unwrap(),
    );
    assert!(std::fs::read_to_string(&file).unwrap().contains(&id));
    // The app's quit, in its order (`lib.rs`): its tools, then its runtimes. Each returns once
    // what it stopped has been reaped.
    h.stop_all();
    rt.stop_all();
    assert!(!alive(tool) && !alive(pid));
    until("the tool's and the runtime's children to end", 10, || {
        !alive(tool_child) && !alive(child)
    });
    assert_eq!(rt.status(ID, &id).unwrap()["state"], "exited");
    assert_eq!(std::fs::read_to_string(&file).unwrap(), "[]");
    // Nothing starts after it.
    let late = lab.beat("late");
    let e = rt
        .start(ID, heartbeat(&late, &[], Lifetime::Managed))
        .unwrap_err();
    assert!(e.contains("quitting"), "{e}");
    // A detached launch is not the app's: it runs on.
    assert!(
        !detached || beating(&agent),
        "the app's quit stopped a detached launch"
    );
}

#[test]
fn lifetime_a_stop_asks_a_runtime_first_and_ends_one_that_ignores_it_at_the_grace() {
    let lab = Lab::new("stop");
    let (handles, ignores) = (lab.beat("handles"), lab.beat("ignores"));
    // The stops run in a host of their own, with no console on Windows, as the app's.
    let mut host = start_host("stop", &lab.dir);
    host_wrote(&mut host, &lab.dir, None, 60);
    let stopped = |name: &str| -> Value {
        serde_json::from_slice(&std::fs::read(lab.dir.join(format!("{name}.stopped"))).unwrap())
            .unwrap()
    };
    let (a, b) = (stopped("handles"), stopped("ignores"));
    for s in [&a, &b] {
        assert_eq!(s["answer"]["state"], "exited", "{s}");
    }
    // Asked (SIGTERM to its group; on Windows CTRL_BREAK to its group, on its own console), the
    // one that handles it exits at once, child and all, well inside the grace.
    assert!(
        with(&handles, "term").exists() && with(&child_of(&handles), "term").exists(),
        "the runtime was not asked to stop, or not its child: {}",
        host_log(&lab.dir)
    );
    assert!(
        a["ms"].as_u64().unwrap() < (GRACE - Duration::from_secs(1)).as_millis() as u64,
        "{a}"
    );
    assert!(ends(&handles) && ends(&child_of(&handles)));
    // The one that ignores it was asked too, and was ended, child and all, once the grace was
    // over.
    assert!(
        with(&ignores, "term").exists(),
        "the runtime was not asked to stop"
    );
    assert!(
        b["ms"].as_u64().unwrap() >= GRACE.as_millis() as u64,
        "ended before the grace: {b}"
    );
    assert!(ends(&ignores) && ends(&child_of(&ignores)));
}

#[test]
fn lifetime_a_host_crash_ends_its_tool_leaves_its_detached_launch_and_its_runtime_as_documented() {
    let lab = Lab::new("host-crash");
    let (runtime, agent) = (lab.beat("runtime"), lab.beat("agent"));
    let mut host = start_host("crash", &lab.dir);
    host_wrote(&mut host, &lab.dir, Some("ready"), 60);
    let tool: Value =
        serde_json::from_slice(&std::fs::read(lab.dir.join("tool.json")).unwrap()).unwrap();
    // The tool's package folder, which the crashed host cannot remove, goes with the check.
    let _package = Folder(PathBuf::from(tool["package"].as_str().unwrap()));
    let (tool, tool_child) = pids(&tool);
    let refused = std::fs::read_to_string(with(&agent, "refused"));
    let detached = let_go(&agent, refused.map_or(Ok(()), Err));
    assert!(beating(&runtime) && beating(&child_of(&runtime)));
    let pid = seen(&runtime)["pid"].as_u64().unwrap();
    // The host dies without stopping anything: ended through the check's own handle of it.
    host.0.kill().unwrap();
    host.0.wait().unwrap();
    // Its tool ends, child and all: on Linux by its parent-death signal, on macOS at the end of
    // its input, on Windows with its job.
    until("the tool and its child to end", 10, || {
        !alive(tool) && !alive(tool_child)
    });
    // Its runtime: Linux's parent-death signal ends the leader alone, its child lives on; on
    // Windows its job ends, child and all; macOS has neither, both live on.
    #[cfg(target_os = "linux")]
    let left = {
        assert!(ends(&runtime), "the runtime outlived the host");
        vec![child_of(&runtime)]
    };
    #[cfg(windows)]
    let left: Vec<PathBuf> = {
        assert!(
            ends(&runtime) && ends(&child_of(&runtime)),
            "the runtime's job outlived the host"
        );
        vec![]
    };
    #[cfg(target_os = "macos")]
    let left = vec![runtime.clone(), child_of(&runtime)];
    let mut left = left;
    if detached {
        left.push(agent.clone());
    }
    for f in &left {
        assert!(beating(f), "{} did not outlive the host", f.display());
    }
    // The next start reports the runtime unknown, and neither stops nor restarts it.
    let rt = Runtimes::open(Some(lab.dir.join("runtimes.json")), Box::new(Canary));
    let list = rt.list(ID);
    assert_eq!(list.len(), 1, "{list:?}");
    assert_eq!(
        (&list[0]["state"], &list[0]["pid"]),
        (&json!("unknown"), &json!(pid))
    );
    let id = list[0]["runtime"].as_str().unwrap().to_string();
    for e in [rt.stop(ID, &id), rt.restart(ID, &id)] {
        assert!(e.unwrap_err().contains("no longer controls"));
    }
    rt.stop_all();
    // What lived on was never asked to stop, and still runs.
    for f in &left {
        assert!(beating(f), "{} was stopped", f.display());
        assert!(!with(f, "term").exists(), "{} was signalled", f.display());
    }
    assert_eq!(
        std::fs::read_to_string(lab.dir.join("runtimes.json")).unwrap(),
        "[]"
    );
}

#[test]
fn lifetime_a_runtime_record_naming_a_live_process_never_gets_it_signalled() {
    let lab = Lab::new("decoy");
    let (decoy, own) = (lab.beat("decoy"), lab.beat("own"));
    // A process the check started itself, outside the registry, which it holds.
    let mut cmd = std::process::Command::new(std::env::current_exe().unwrap());
    cmd.arg(fixture::HEARTBEAT)
        .arg(&decoy)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null());
    let mut held = Held(spawn_std(&mut cmd));
    assert!(beating(&decoy));
    let pid = held.0.id();
    // A record that names it, as one an earlier run of the app left would.
    let file = lab.dir.join("runtimes.json");
    std::fs::write(
        &file,
        json!([{"runtime": "rt-decoy", "package": ID, "pid": pid, "started": 1,
            "executable": std::env::current_exe().unwrap(), "argv": [fixture::HEARTBEAT, decoy]}])
        .to_string(),
    )
    .unwrap();
    let rt = Runtimes::open(Some(file), Box::new(Canary));
    let list = rt.list(ID);
    assert_eq!(list.len(), 1, "{list:?}");
    assert_eq!(
        (&list[0]["state"], &list[0]["pid"]),
        (&json!("unknown"), &json!(pid))
    );
    for e in [rt.stop(ID, "rt-decoy"), rt.restart(ID, "rt-decoy")] {
        assert!(e.unwrap_err().contains("no longer controls"));
    }
    // A runtime of the registry's own beside it: a close and the quit do end that one.
    rt.start(ID, heartbeat(&own, &[], Lifetime::Managed))
        .unwrap();
    assert!(beating(&own));
    rt.close(ID, false);
    assert!(ends(&own), "a close did not stop the package's runtime");
    rt.reopen(ID);
    rt.stop_all();
    rt.close(ID, true);
    assert!(beating(&decoy), "the process the record names was stopped");
    assert!(
        !with(&decoy, "term").exists(),
        "the process the record names was asked to stop"
    );
    assert!(held.0.try_wait().unwrap().is_none());
}

/// A pipe whose read end this process leaves inheritable, as a library might, with a mark
/// waiting in it: n is that end's descriptor or handle number, and holder what the fixture's probe
/// of n reports in a process that holds it (unix: its device and inode; Windows: the mark).
struct Inheritable {
    n: u64,
    holder: Value,
    #[cfg(unix)]
    _ends: (std::os::fd::OwnedFd, std::os::fd::OwnedFd),
    #[cfg(windows)]
    _ends: (std::fs::File, std::fs::File),
}

impl Inheritable {
    #[cfg(unix)]
    fn new() -> Inheritable {
        use std::os::fd::FromRawFd;
        let mut fds = [0; 2];
        // Neither end is close-on-exec.
        assert_eq!(unsafe { libc::pipe(fds.as_mut_ptr()) }, 0);
        let mut st: libc::stat = unsafe { std::mem::zeroed() };
        assert_eq!(unsafe { libc::fstat(fds[0], &mut st) }, 0);
        Inheritable {
            n: fds[0] as u64,
            holder: json!(format!("{}:{}", st.st_dev, st.st_ino)),
            _ends: unsafe {
                (
                    std::os::fd::OwnedFd::from_raw_fd(fds[0]),
                    std::os::fd::OwnedFd::from_raw_fd(fds[1]),
                )
            },
        }
    }

    #[cfg(windows)]
    fn new() -> Inheritable {
        use std::io::Write as _;
        use std::os::windows::io::{AsRawHandle, FromRawHandle};
        use windows_sys::Win32::Foundation::{SetHandleInformation, HANDLE_FLAG_INHERIT};
        let (mut r, mut w) = (std::ptr::null_mut(), std::ptr::null_mut());
        assert_ne!(
            unsafe {
                windows_sys::Win32::System::Pipes::CreatePipe(&mut r, &mut w, std::ptr::null(), 0)
            },
            0
        );
        assert_ne!(
            unsafe { SetHandleInformation(r, HANDLE_FLAG_INHERIT, HANDLE_FLAG_INHERIT) },
            0
        );
        let mark = format!("pluxy-probe-{}", std::process::id());
        let (r, mut w) = unsafe {
            (
                std::fs::File::from_raw_handle(r),
                std::fs::File::from_raw_handle(w),
            )
        };
        w.write_all(mark.as_bytes()).unwrap();
        Inheritable {
            n: r.as_raw_handle() as usize as u64,
            holder: json!(mark),
            _ends: (r, w),
        }
    }
}

#[test]
fn lifetime_a_tool_a_runtime_and_a_detached_launch_get_no_handle_they_were_not_given() {
    let lab = Lab::new("probe");
    let (control, runtime, agent) = (lab.beat("control"), lab.beat("runtime"), lab.beat("agent"));
    let pipe = Inheritable::new();
    let probe = format!("probe={}", pipe.n);
    // The control: a process started the way std starts one gets it, and the probe sees it.
    let mut cmd = std::process::Command::new(std::env::current_exe().unwrap());
    cmd.arg(fixture::HEARTBEAT)
        .arg(&control)
        .arg(&probe)
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null());
    let _control = Held(spawn_std(&mut cmd));
    assert_eq!(
        seen(&control)["probe"],
        pipe.holder,
        "the probe does not see what a process holds"
    );
    // A managed runtime, a detached launch and a tool do not get it. (What one holds at that
    // number, if anything, is its own: a number is used again.)
    let rt = Runtimes::open(None, Box::new(Canary));
    let _quit = OnDrop(|| rt.stop_all());
    rt.start(ID, heartbeat(&runtime, &[&probe], Lifetime::Managed))
        .unwrap();
    assert_ne!(seen(&runtime)["probe"], pipe.holder, "the runtime got it");
    let l = heartbeat(&agent, &[&probe], Lifetime::Detached);
    if let_go(&agent, crate::spawn::detached(&l, ID, &Canary)) {
        assert_ne!(
            seen(&agent)["probe"],
            pipe.holder,
            "the detached launch got it"
        );
    }
    let h = host(json!({"probe": pipe.n}));
    let _stop = OnDrop(|| h.stop_all());
    let out = summary(&h, &[]).unwrap();
    assert_ne!(out["probe"], pipe.holder, "the tool got it");
    assert!(out.get("probe").is_some(), "the tool was not asked: {out}");
}

#[test]
fn lifetime_a_launch_gets_exactly_its_arguments_working_folder_and_environment() {
    let lab = Lab::new("exact");
    let (runtime, agent) = (lab.beat("runtime"), lab.beat("agent"));
    // Arguments a command line must quote and escape to pass on whole (Windows).
    let odd = [
        "two words",
        "",
        r#"a "quoted" word"#,
        r"back\slash\",
        r"\",
        "tab\tand ünïcödé",
        "--flag=x y",
    ];
    let launch = |f: &Path, lifetime| {
        let mut l = heartbeat(f, &odd, lifetime);
        l.request
            .set
            .insert("LIFETIME_SET".into(), "a value = with spaces".into());
        l.request.unset.push("PATH".into());
        l.request
            .secrets
            .insert("LIFETIME_KEY".into(), "key".into());
        l.secrets = vec![("LIFETIME_KEY".into(), "s-1".into())];
        l
    };
    // Exactly: its session values as this program has them (`spawn::BASE`, and `LC_*`) but
    // PATH, which it removes, and the variables it sets and is given.
    let mut want: BTreeMap<String, String> = std::env::vars_os()
        .filter_map(|(k, v)| {
            let k = k.into_string().ok()?;
            let k = if cfg!(windows) {
                k.to_ascii_uppercase()
            } else {
                k
            };
            let session = crate::spawn::BASE.contains(&k.as_str()) || k.starts_with("LC_");
            (session && k != "PATH").then(|| (k, v.to_string_lossy().into_owned()))
        })
        .collect();
    want.insert("LIFETIME_SET".into(), "a value = with spaces".into());
    want.insert("LIFETIME_KEY".into(), format!("{SECRET}-s-1"));
    let check = |f: &Path| {
        let v = seen(f);
        let mut argv = vec![
            std::env::current_exe()
                .unwrap()
                .to_string_lossy()
                .into_owned(),
            fixture::HEARTBEAT.to_string(),
            f.to_string_lossy().into_owned(),
        ];
        argv.extend(odd.iter().map(|a| a.to_string()));
        assert_eq!(v["argv"], json!(argv));
        assert_eq!(v["cwd"], json!(lab.dir));
        let got: BTreeMap<String, String> = v["env"]
            .as_object()
            .unwrap()
            .iter()
            .map(|(k, v)| (k.clone(), v.as_str().unwrap_or_default().to_string()))
            .collect();
        assert_eq!(got, want);
    };
    let rt = Runtimes::open(None, Box::new(Canary));
    let _quit = OnDrop(|| rt.stop_all());
    rt.start(ID, launch(&runtime, Lifetime::Managed)).unwrap();
    check(&runtime);
    if let_go(
        &agent,
        crate::spawn::detached(&launch(&agent, Lifetime::Detached), ID, &Canary),
    ) {
        check(&agent);
    }
}

/// The record of a WSL environment, as `pluxy/wsl`'s check gives it.
fn wsl_record(user: &str) -> crate::environments::Record {
    crate::environments::Record::from_check(
        "Ubuntu",
        user,
        &json!({"distro": "Ubuntu", "user": user, "uid": 1001, "home": format!("/home/{user}"),
            "arch": "amd64", "automount": "/mnt/",
            "drives": [{"letter": "c", "path": "/mnt/c", "exec": true}]}),
        0,
    )
    .unwrap()
}

fn call_in(h: &Host, instance: &Instance) -> Result<Value, CallError> {
    h.run_call_with(
        h.begin_call(None).unwrap(),
        instance,
        "summary",
        json!({"items": []}),
        Arc::new(Context::main()),
        Cancel::default(),
    )
}

#[test]
fn an_instance_in_wsl_has_its_own_slot_and_starts_only_from_its_own_record() {
    let local = package(Value::Null);
    let bob = wsl_record("bob");
    let in_wsl = Spec {
        environment: bob.environment(),
        wsl: Some(bob.clone()),
        ..local.clone()
    };
    let folder = Folder(local.dir.clone());
    let asked = Arc::new(Mutex::new(Vec::new()));
    let (l, w, seen) = (local.clone(), in_wsl.clone(), Arc::clone(&asked));
    // The record for alice names bob's instance: a routing mistake.
    let h = Host::for_tests(
        Box::new(move |i: &Instance| {
            let _ = &folder;
            seen.lock().unwrap().push(i.key());
            if i.environment.is_local() {
                Ok(l.clone())
            } else {
                Ok(w.clone())
            }
        }),
        fast(),
    );
    let alice_i = Instance {
        package: ID.into(),
        environment: wsl_record("alice").environment(),
    };
    let (pid, _) = pids(&summary(&h, &[]).unwrap());
    let _held = h.lease(ID).unwrap();
    // Off Windows nothing runs in WSL; the local tool runs on, its own slot untouched.
    #[cfg(unix)]
    {
        let bob_i = Instance {
            package: ID.into(),
            environment: bob.environment(),
        };
        let e = call_in(&h, &bob_i).unwrap_err();
        assert!(e.message.contains("WSL runs on Windows only"), "{e:?}");
        assert!(h.status_in(&bob_i).last.unwrap().contains("Windows only"));
    }
    let e = call_in(&h, &alice_i).unwrap_err();
    assert!(e.message.contains("names"), "{e:?}");
    assert_eq!(h.status(ID).state, "active");
    assert_eq!(h.status(ID).pid, Some(pid as u32));
    assert!(h.status(ID).last.is_none());
    let asked = asked.lock().unwrap().clone();
    assert!(asked.contains(&ID.to_string()) && asked.contains(&alice_i.key()));
}

/// A host whose WSL instance for bob runs the fixture here, as this computer's program with that
/// instance's environment (no `wsl.exe` off Windows), in mode; and that instance.
fn wsl_here(mode: Value) -> (Host, Instance) {
    let bob = wsl_record("bob");
    let spec = Spec {
        environment: bob.environment(),
        ..package(mode)
    };
    let instance = Instance {
        package: ID.into(),
        environment: bob.environment(),
    };
    let (folder, want) = (Folder(spec.dir.clone()), instance.clone());
    let h = Host::for_tests(
        Box::new(move |i: &Instance| {
            let _ = &folder;
            if *i == want {
                Ok(spec.clone())
            } else {
                Err(format!("{} is not installed on this device", i.name()))
            }
        }),
        fast(),
    );
    (h, instance)
}

/// A host running tool ID on this computer (from spec) and as bob's instance in WSL (the fixture
/// here, with that instance's environment); and that instance.
fn here_and_in_wsl(spec: Spec) -> (Host, Instance) {
    let bob = wsl_record("bob");
    let instance = Instance {
        package: ID.into(),
        environment: bob.environment(),
    };
    let in_wsl = Spec {
        environment: bob.environment(),
        ..spec.clone()
    };
    let (folder, want) = (Folder(spec.dir.clone()), instance.clone());
    let h = Host::for_tests(
        Box::new(move |i: &Instance| {
            let _ = &folder;
            if *i == want {
                Ok(in_wsl.clone())
            } else if *i == Instance::local(ID) {
                Ok(spec.clone())
            } else {
                Err(format!("{} is not installed on this device", i.name()))
            }
        }),
        Limits {
            idle: Duration::from_secs(60),
            ..fast()
        },
    );
    (h, instance)
}

/// Both of tool ID's instances run: their process IDs (here, then in WSL).
fn both_run(h: &Host, bob: &Instance) -> (u32, u32) {
    summary(h, &[]).unwrap();
    call_in(h, bob).unwrap();
    let (here, wsl) = (h.status(ID).pid.unwrap(), h.status_in(bob).pid.unwrap());
    assert_ne!(here, wsl);
    (here, wsl)
}

/// A change of the package stops its instances in every environment; a removed environment's
/// record stops only the instances there, and so does a newly installed Linux build (`stop_wsl`).
#[test]
fn a_package_change_stops_its_instances_in_wsl_and_an_environment_removed_only_those() {
    let (h, bob) = here_and_in_wsl(package(Value::Null));
    let (here, wsl) = both_run(&h, &bob);
    h.stop(ID);
    until("both stopped", 10, || {
        !alive(u64::from(here)) && !alive(u64::from(wsl))
    });
    let (here, wsl) = both_run(&h, &bob);
    h.stop_in(&bob.environment);
    until("the instance in WSL stopped", 10, || !alive(u64::from(wsl)));
    assert_eq!(h.status(ID).pid, Some(here));
    assert!(alive(u64::from(here)));
    // This computer is no environment to remove: nothing stops.
    h.stop_in(&Environment::Local);
    std::thread::sleep(Duration::from_millis(300));
    assert_eq!(h.status(ID).pid, Some(here));
    call_in(&h, &bob).unwrap();
    let wsl = h.status_in(&bob).pid.unwrap();
    h.stop_wsl(ID);
    until("the instance in WSL stopped", 10, || !alive(u64::from(wsl)));
    assert_eq!(h.status(ID).pid, Some(here));
    assert!(alive(u64::from(here)));
    h.stop_all();
}

/// A swap to a new release stops the instances in WSL, which ran the old release's Linux build:
/// their next call starts the one installed for the release now active.
#[test]
fn a_swap_stops_the_instances_in_wsl_of_the_release_it_replaces() {
    let (h, bob) = here_and_in_wsl(package(Value::Null));
    let (_, wsl) = both_run(&h, &bob);
    let p = h
        .prepare_spec(release(Value::Null, "1.1.0", 2), h.stops(ID))
        .unwrap();
    h.switch(p).unwrap();
    assert_eq!(h.status(ID).generation, Some(2));
    until("the instance in WSL stopped", 10, || !alive(u64::from(wsl)));
    h.stop_all();
}

// WSL 1 can refuse a start (`0xd0000225`, wsl.exe's text on stderr or in place of a frame): such a
// start is tried again, twice, 2 seconds apart, and none of those tries counts as a crash; only
// when the last one is refused too does one crash count, with its backoff.
#[test]
fn a_start_wsl_refused_is_tried_again_and_counts_as_one_crash_only_when_it_stays_refused() {
    let mut modes = vec![
        json!({"wsl_refuse": 2}),
        json!({"wsl_refuse": 2, "wsl_refuse_stdout": true}),
    ];
    // The text arriving on stdout only after the start was reaped (wsl.exe's pipe outlives it):
    // still read, and still a refusal.
    if cfg!(unix) {
        modes.push(json!({"wsl_refuse": 2, "wsl_refuse_late": true}));
    }
    for mode in modes {
        let (h, bob) = wsl_here(mode.clone());
        let v = call_in(&h, &bob).unwrap_or_else(|e| panic!("{mode}: {e:?}"));
        assert_eq!(v["count"], 0, "{mode}");
        let st = h.status_in(&bob);
        assert_eq!((st.state, st.crashes), ("ready", 0), "{mode}: {st:?}");
        h.stop_all();
    }
    let (h, bob) = wsl_here(json!({"wsl_refuse": 10}));
    let e = call_in(&h, &bob).unwrap_err();
    assert!(
        e.message.contains("wsl.exe said") && e.message.contains("0xd0000225"),
        "{e:?}"
    );
    let st = h.status_in(&bob);
    assert_eq!((st.state, st.crashes), ("crashed", 1), "{st:?}");
    // Its backoff holds the next call, which starts nothing.
    let e = call_in(&h, &bob).unwrap_err();
    assert!(e.message.contains("may restart it in"), "{e:?}");
    h.stop_all();
}

#[test]
fn call_error_user_message_omits_protocol_code_and_falls_back_for_empty() {
    // A real Host::call error carries its protocol code in `.code` and `Display`, while
    // `user_message()` gives only the message that reaches a page or dialog.
    let h = host(Value::Null);
    let real = h.call(ID, "summary", json!({}), None).unwrap_err();
    assert_eq!(real.code, "invalid_request", "{real}");
    assert!(
        real.to_string().starts_with("invalid_request: "),
        "Display keeps the code for logs: {real}"
    );
    let shown = real.user_message();
    assert_eq!(shown, real.message);
    assert!(
        !shown.contains("invalid_request"),
        "window string must not show protocol code: {shown}"
    );
    h.stop_all();

    // Realistic add-on error (missing settings folder): message only, no `invalid_request:`.
    let missing = fail(
        "invalid_request",
        "/home/me/.claude does not exist yet: run Claude Code once",
    );
    assert_eq!(
        missing.to_string(),
        "invalid_request: /home/me/.claude does not exist yet: run Claude Code once"
    );
    assert_eq!(
        missing.user_message(),
        "/home/me/.claude does not exist yet: run Claude Code once"
    );

    // A message that repeats a protocol code prefix still loses it.
    let repeated = fail("not_found", "not_found: settings.json is missing");
    assert_eq!(repeated.user_message(), "settings.json is missing");

    // A cancelled error keeps the `cancelled:` prefix for any message so pages detect cancels by
    // code rather than by message text.
    assert_eq!(
        fail("cancelled", "the native confirmation was cancelled").user_message(),
        "cancelled: the native confirmation was cancelled"
    );
    assert_eq!(
        fail("cancelled", "confirmation window closed by desktop").user_message(),
        "cancelled: confirmation window closed by desktop"
    );
    assert_eq!(
        fail("cancelled", "cancelled: already prefixed").user_message(),
        "cancelled: already prefixed"
    );

    // Empty, whitespace, bare-code, or punctuation-only messages fall back to a plain sentence
    // that never leaks the raw protocol code (except `cancelled`, which keeps its `cancelled:`
    // prefix for window cancel detection).
    for code in [
        "invalid_request",
        "auth",
        "permission",
        "account_closed",
        "not_found",
        "rate_limit",
        "quota",
        "unsupported",
        "upstream",
        "unavailable",
        "internal",
        "extension_crashed",
        "extension_protocol",
        "extension_unavailable",
        "extension_timeout",
        "incompatible",
        "something_new",
    ] {
        for empty in ["", "   ", code, &format!("{code}: "), "..."] {
            let e = fail(code, empty);
            let msg = e.user_message();
            assert!(!msg.is_empty(), "{code} / {empty:?}");
            assert!(
                !msg.contains('_'),
                "fallback for {code} / {empty:?} must not leak snake_case code: {msg}"
            );
            assert!(
                !msg.to_ascii_lowercase().starts_with(&format!("{code}:")),
                "fallback for {code} / {empty:?} must not start with code: {msg}"
            );
        }
    }
    assert_eq!(
        fail("not_found", "").user_message(),
        "What the add-on looked for could not be found."
    );
    for empty in ["", "   ", "cancelled", "cancelled: ", "..."] {
        assert_eq!(
            fail("cancelled", empty).user_message(),
            "cancelled: The action was cancelled.",
            "cancelled / {empty:?}"
        );
    }
}

// The program host and the spawn service against a real WSL distribution (`wsl_real_*`, ignored
// but in the WSL jobs of next-app-tools-platforms.yml).
#[cfg(windows)]
#[path = "wsl_real_tests.rs"]
mod wsl_real;
