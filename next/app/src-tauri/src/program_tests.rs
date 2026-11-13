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
        if std::env::var_os("PLUXY_EXTENSION_ID").is_some()
            || std::env::var_os(fixture::CHILD).is_some()
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
    ]
}

/// A package folder for the fixture, misbehaving as mode says (null: not at all).
fn package(mode: Value) -> Spec {
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
        serde_json::to_vec(
            &json!({"app": {"operations": ops, "state": {"version": 0, "readable_versions": [0]}}}),
        )
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
        generation: 1,
        program,
        program_sha256: this_program_sha256().into(),
        dir,
        operations: operations(),
        state_version: 0,
        secrets: vec![SECRET.into()],
    }
}

fn host_for(spec: Spec, limits: Limits) -> Host {
    Host::new(
        Box::new(move |id| {
            if id == ID {
                Ok(spec.clone())
            } else {
                Err(format!("{id} is not installed on this device"))
            }
        }),
        limits,
    )
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
    // Reaped; the child got SIGKILL with its group and goes a moment later.
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
    // None of them reached it: its first call is this one.
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
            let turn = r.turn.lock().unwrap();
            usize::from(turn.0) + turn.1 == MAX_PENDING
        })
    });
    let over = h.call(ID, "wait", json!({}), None).unwrap_err();
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
    until("the crash", 5, || h.status(ID).state == "crashed");
    until("its child to go", 5, || !alive(child));
    assert!(h.status(ID).last.unwrap().contains("exited"));
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
        long,
        "after",
    ]}));
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
            "colour \\u{1b}[31m".into(),
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
fn a_call_allowed_for_another_release_is_refused() {
    let h = host(Value::Null);
    let ctx = |sha: &str, generation: u64| {
        Arc::new(Context {
            chain: vec![Hop::Main],
            expect: Some((sha.into(), generation)),
            extra: Box::new(()),
        })
    };
    let e = h
        .call_with(
            ID,
            "summary",
            json!({"items": []}),
            None,
            ctx(&"ab".repeat(32), 2),
            Cancel::default(),
        )
        .unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let e = h
        .call_with(
            ID,
            "summary",
            json!({"items": []}),
            None,
            ctx(&"cd".repeat(32), 1),
            Cancel::default(),
        )
        .unwrap_err();
    assert_eq!(e.code, "extension_unavailable", "{e}");
    let ok = h.call_with(
        ID,
        "summary",
        json!({"items": ["a"]}),
        None,
        ctx(&"ab".repeat(32), 1),
        Cancel::default(),
    );
    assert_eq!(ok.unwrap()["count"], 1);
    h.stop_all();
}
