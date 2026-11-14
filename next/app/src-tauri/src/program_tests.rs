//! The program host against real processes: the fixture tool (`examples/program_fixture`) runs
//! as this test program. When the host starts it, with the contract's environment, the hook
//! below runs the fixture before any test does; each test's package folder holds a link to this
//! program as its tool, its manifest, and a `fixture.json` that makes the fixture misbehave.

use super::*;
use std::collections::BTreeSet;
use std::path::Path;
use std::sync::OnceLock;

#[path = "../examples/program_fixture/fixture.rs"]
mod fixture;

#[used]
#[link_section = ".init_array"]
static FIXTURE: extern "C" fn() = {
    extern "C" fn run_fixture() {
        // The spawn tests' agent (`spawn_tests.rs`) is started with `AGENT` as its first argument.
        let agent = std::fs::read("/proc/self/cmdline").is_ok_and(|c| {
            c.split(|b| *b == 0)
                .nth(1)
                .is_some_and(|a| a.starts_with(fixture::AGENT.as_bytes()))
        });
        if std::env::var_os("PLUXY_EXTENSION_ID").is_some()
            || std::env::var_os(fixture::CHILD).is_some()
            || agent
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
    fn journals(&self, _: &str) -> Result<Vec<Value>, String> {
        if self.unreadable {
            return Err("its records are damaged".into());
        }
        Ok(self.journals.lock().unwrap().clone())
    }
    fn frame(
        &self,
        tool: &Hop,
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
    let program = dir.join("bin").join("tool");
    std::os::unix::fs::symlink(std::env::current_exe().unwrap(), &program).unwrap();
    Spec {
        id: ID.into(),
        sha256: "ab".repeat(32),
        generation,
        program,
        program_sha256: this_program_sha256().into(),
        dir,
        operations: operations(),
        state_version: 0,
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
        Box::new(move |id| {
            let _ = &folder;
            before(id);
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

fn alive(pid: u64) -> bool {
    std::fs::read_to_string(format!("/proc/{pid}/stat"))
        .ok()
        .and_then(|s| s.rsplit_once(") ").and_then(|(_, r)| r.chars().next()))
        .is_some_and(|c| c != 'Z')
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
    assert_eq!(a["fds"], json!([0, 1, 2]));
    assert_eq!(a["pgid"], pid);
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
    assert_eq!(h.status(ID).state, "crashed");
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
    // 300 ms doubling, up to 1200 ms.
    let expect = [300u64, 600, 1200, 1200];
    for (w, e) in waits.iter().zip(expect) {
        assert!(
            *w <= Duration::from_millis(e + 1)
                && *w + Duration::from_millis(100) >= Duration::from_millis(e),
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
            ID,
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
            ID,
            "summary",
            json!({"items": []}),
            ctx(&"cd".repeat(32), 1),
            Cancel::default(),
        )
        .unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let ok = h.run_call_with(
        h.begin_call(None).unwrap(),
        ID,
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
    let h = Arc::new(host_for(v1, fast()));
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
    assert!(alive(new));
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
    let h = Arc::new(host(Value::Null));
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
            ID,
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
            ID,
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
