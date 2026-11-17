//! The library against a host played over real pipes.

use super::*;
use std::io::{PipeReader, PipeWriter};
use std::sync::atomic::AtomicUsize;

/// A handler whose operations exercise each part of the contract.
#[derive(Default)]
struct Tool {
    reconciled: Mutex<Vec<(Vec<Value>, Vec<Value>)>>,
    running: AtomicUsize,
    /// The acks the test host has sent so far, set before it writes each one.
    acks_sent: AtomicU64,
    /// For each batch the producer sent, the acks the host had sent by then.
    acks_at_send: Mutex<Vec<u64>>,
}

impl Handler for Tool {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        self.running.fetch_add(1, Ordering::SeqCst);
        let r = match call.op.as_str() {
            "echo" => {
                Ok(json!({"input": call.input, "chain": call.chain, "secrets": call.secrets}))
            }
            "progress" => {
                call.progress(json!({"done": 1})).unwrap();
                Ok(json!({}))
            }
            "fail" => Err(Error::not_found("no such thing")),
            "wait" => loop {
                if call.cancelled() {
                    break Err(Error::cancelled());
                }
                std::thread::sleep(Duration::from_millis(10));
            },
            "ignore-cancel" => {
                while !call.cancelled() {
                    std::thread::sleep(Duration::from_millis(10));
                }
                Ok(json!({"finished": true}))
            }
            "relay" => call.call("pluxy/target", "summary", json!({"items": ["a"]})),
            "backup" => {
                let mut f = Map::new();
                f.insert("label".into(), json!("old"));
                f.insert("data".into(), json!("AA=="));
                call.vault("backup.put", f)
            }
            "huge" => Ok(json!({"text": "x".repeat(MAX_FRAME)})),
            "spawn" => call.spawn(),
            "runtime" => call.runtime("stop", Some("rt-1")),
            "start" => call.jobs("start", json!({"kind": "pluxy/hub/items"})),
            "scan" => (|| {
                for n in 1..=call.input["count"].as_u64().unwrap_or(0) {
                    let seq = call.batch(json!({"n": n}))?;
                    assert_eq!(seq, n);
                    let at = self.acks_sent.load(Ordering::SeqCst);
                    self.acks_at_send.lock().unwrap().push(at);
                }
                Ok(json!({"sent": call.input["count"]}))
            })(),
            "big" => call
                .batch(json!({"text": "x".repeat(MAX_BATCH)}))
                .map(|_| json!({})),
            "save" => call
                .save_file(
                    call.input["name"].as_str().unwrap_or_default(),
                    &save_bytes(call.input["len"].as_u64().unwrap_or(0) as usize),
                )
                .map(|o| match o {
                    SaveOutcome::Saved { size } => json!({"outcome": "saved", "size": size}),
                    SaveOutcome::Cancelled => json!({"outcome": "cancelled"}),
                }),
            op => Err(Error::unsupported(format!("no operation {op}"))),
        };
        self.running.fetch_sub(1, Ordering::SeqCst);
        r
    }

    fn reconcile(&self, _: &Call, unknown: &[Value], journals: &[Value]) -> Result<(), Error> {
        self.reconciled
            .lock()
            .unwrap()
            .push((unknown.to_vec(), journals.to_vec()));
        Ok(())
    }
}

/// The bytes the "save" operation saves: len of them, every value in turn (never all zero).
fn save_bytes(len: usize) -> Vec<u8> {
    (0..len).map(|i| (i % 251) as u8).collect()
}

struct Host {
    to: Option<PipeWriter>,
    from: PipeReader,
    served: Option<std::thread::JoinHandle<io::Result<()>>>,
    tool: Arc<Tool>,
}

impl Host {
    fn start() -> Host {
        let (in_r, in_w) = io::pipe().unwrap();
        let (out_r, out_w) = io::pipe().unwrap();
        let tool = Arc::new(Tool::default());
        let handler: Arc<dyn Handler> = tool.clone();
        let description = Description::from_manifest(
            br#"{"app": {"operations": [{"id": "echo", "description": "Echo", "effect": "read"}],
                "state": {"version": 0, "readable_versions": [0]}}}"#,
        )
        .unwrap();
        let identity = Identity {
            id: "pluxy/test".into(),
            sha256: "ab".repeat(32),
        };
        let served =
            std::thread::spawn(move || serve(in_r, out_w, &identity, description, handler));
        Host {
            to: Some(in_w),
            from: out_r,
            served: Some(served),
            tool,
        }
    }

    fn send(&mut self, v: Value) {
        write_frame(self.to.as_mut().unwrap(), &v, MAX_INBOUND).unwrap();
    }

    /// Acknowledges batch seq of call id, noting it for the tool first.
    fn ack(&mut self, id: &str, seq: u64) {
        self.tool.acks_sent.store(seq, Ordering::SeqCst);
        self.send(json!({"type": "ack", "id": id, "seq": seq}));
    }

    fn next(&mut self) -> Value {
        read_frame(&mut self.from).unwrap().expect("a frame")
    }

    /// Ends the input; the run must return.
    fn end(mut self) -> io::Result<()> {
        drop(self.to.take());
        self.served.take().unwrap().join().unwrap()
    }
}

#[test]
fn frames_round_trip_and_end_cleanly() {
    let mut buf = Vec::new();
    write_frame(&mut buf, &json!({"a": 1}), MAX_FRAME).unwrap();
    assert_eq!(&buf[..4], &7u32.to_be_bytes());
    let mut r = &buf[..];
    assert_eq!(read_frame(&mut r).unwrap(), Some(json!({"a": 1})));
    assert_eq!(read_frame(&mut r).unwrap(), None);
    let mut cut = &buf[..2];
    assert!(read_frame(&mut cut).is_err());
    let mut big = Vec::new();
    let e = write_frame(&mut big, &json!("x".repeat(64)), 16).unwrap_err();
    assert_eq!(e.kind(), io::ErrorKind::InvalidInput);
    assert!(big.is_empty());
    let mut huge = &(MAX_INBOUND as u32 + 1).to_be_bytes()[..];
    assert!(read_frame(&mut huge).is_err());
}

#[test]
fn hello_first_then_describe_reconciles_and_answers_the_manifest() {
    let mut h = Host::start();
    assert_eq!(
        h.next(),
        json!({"type": "hello", "contract": "app", "versions": [0], "id": "pluxy/test", "sha256": "ab".repeat(32)})
    );
    let unknown = json!([{"id": "c-7", "op": "apply", "generation": 1700000000}]);
    let journals = json!([{"id": "j-1", "op": "apply"}]);
    h.send(json!({"type": "call", "id": "c-1", "op": "describe", "contract_version": 0, "unknown": unknown, "journals": journals}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-1",
            "operations": [{"id": "echo", "description": "Echo"}],
            "state": {"version": 0, "readable_versions": [0]}})
    );
    assert_eq!(
        h.tool.reconciled.lock().unwrap().clone(),
        vec![(
            unknown.as_array().unwrap().clone(),
            journals.as_array().unwrap().clone()
        )]
    );
    h.end().unwrap();
}

#[test]
fn calls_answer_with_results_errors_and_progress() {
    let mut h = Host::start();
    h.next();
    let chain = json!([{"kind": "main"}]);
    h.send(json!({"type": "call", "id": "c-2", "op": "echo", "input": {"x": 1}, "chain": chain, "secrets": {"r-1": "s"}}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"input": {"x": 1}, "chain": chain, "secrets": {"r-1": "s"}}})
    );
    h.send(json!({"type": "call", "id": "c-3", "op": "fail", "input": {}}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-3", "error": {"code": "not_found", "message": "no such thing"}})
    );
    h.send(json!({"type": "call", "id": "c-4", "op": "progress", "input": {}}));
    assert_eq!(
        h.next(),
        json!({"type": "progress", "id": "c-4", "value": {"done": 1}})
    );
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-4", "output": {}})
    );
    h.send(json!({"type": "call", "id": "c-5", "op": "nothing", "input": {}}));
    assert_eq!(h.next()["error"]["code"], "unsupported");
    // An answer too big for a frame fails the call instead of breaking the pipe.
    h.send(json!({"type": "call", "id": "c-6", "op": "huge", "input": {}}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-6", "error": {"code": "internal", "message": "the answer is larger than a frame may be (1 MiB)"}})
    );
    h.end().unwrap();
}

#[test]
fn a_cancel_ends_a_call_that_honours_it_and_a_finished_call_keeps_its_result() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "wait", "input": {}}));
    h.send(json!({"type": "cancel", "id": "c-2"}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-2", "error": {"code": "cancelled", "message": "the call was cancelled"}})
    );
    // A handler that finishes anyway did its work (a write may have taken effect): its result
    // is the call's outcome, not `cancelled`.
    h.send(json!({"type": "call", "id": "c-3", "op": "ignore-cancel", "input": {}}));
    h.send(json!({"type": "cancel", "id": "c-3"}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-3", "output": {"finished": true}})
    );
    // The pipe stays: the next call is answered.
    h.send(json!({"type": "call", "id": "c-4", "op": "echo", "input": {}}));
    assert_eq!(h.next()["type"], "result");
    h.end().unwrap();
}

#[test]
fn a_calls_own_call_and_vault_frame_go_through_the_host() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "relay", "input": {}}));
    let out = h.next();
    assert_eq!(out["type"], "call");
    assert_eq!(out["for"], "c-2");
    assert_eq!(
        (&out["package"], &out["op"], &out["input"]),
        (
            &json!("pluxy/target"),
            &json!("summary"),
            &json!({"items": ["a"]})
        )
    );
    let o = out["id"].as_str().unwrap().to_string();
    assert!(o.starts_with("o-"));
    h.send(json!({"type": "result", "id": o, "output": {"count": 1}}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"count": 1}})
    );
    h.send(json!({"type": "call", "id": "c-3", "op": "backup", "input": {}}));
    let v = h.next();
    assert_eq!(
        (&v["type"], &v["for"], &v["op"], &v["label"]),
        (
            &json!("vault"),
            &json!("c-3"),
            &json!("backup.put"),
            &json!("old")
        )
    );
    h.send(json!({"type": "error", "id": v["id"], "error": {"code": "unavailable", "message": "full"}}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-3", "error": {"code": "unavailable", "message": "full"}})
    );
    h.end().unwrap();
}

#[test]
fn cancelling_a_call_cancels_its_own_call() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "relay", "input": {}}));
    let o = h.next()["id"].clone();
    h.send(json!({"type": "cancel", "id": "c-2"}));
    assert_eq!(h.next(), json!({"type": "cancel", "id": o}));
    h.send(
        json!({"type": "error", "id": o, "error": {"code": "cancelled", "message": "cancelled"}}),
    );
    assert_eq!(h.next()["error"]["code"], "cancelled");
    h.end().unwrap();
}

#[test]
fn the_end_of_the_input_cancels_waiting_calls_and_leaves_none_running() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "wait", "input": {}}));
    h.send(json!({"type": "call", "id": "c-3", "op": "relay", "input": {}}));
    // c-3's own call is out; neither is answered.
    assert_eq!(h.next()["type"], "call");
    let tool = Arc::clone(&h.tool);
    h.end().unwrap();
    assert_eq!(tool.running.load(Ordering::SeqCst), 0);
}

#[test]
fn identity_needs_the_hosts_environment() {
    // Not set in a test process.
    if std::env::var_os("PLUXY_EXTENSION_ID").is_none() {
        assert!(Identity::from_env().is_err());
    }
    assert!(Description::from_manifest(b"{}").is_err());
}

#[test]
fn spawn_and_runtime_requests_name_only_the_call_and_get_the_hosts_answer() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "spawn", "input": {}}));
    let s = h.next();
    let id = s["id"].as_str().unwrap().to_string();
    assert!(id.starts_with("s-"));
    assert_eq!(s, json!({"type": "spawn", "id": id, "for": "c-2"}));
    h.send(json!({"type": "result", "id": id, "output": {"lifetime": "detached"}}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"lifetime": "detached"}})
    );
    h.send(json!({"type": "call", "id": "c-3", "op": "runtime", "input": {}}));
    let u = h.next();
    let id = u["id"].as_str().unwrap().to_string();
    assert_eq!(
        u,
        json!({"type": "runtime", "id": id, "for": "c-3", "op": "stop", "runtime": "rt-1"})
    );
    h.send(
        json!({"type": "error", "id": id, "error": {"code": "not_found", "message": "no runtime"}}),
    );
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-3", "error": {"code": "not_found", "message": "no runtime"}})
    );
    h.end().unwrap();
}

#[test]
fn a_consumers_jobs_frame_goes_to_the_hosts_job_service() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "start", "input": {}}));
    let q = h.next();
    assert_eq!(
        (&q["type"], &q["for"], &q["op"], &q["input"]),
        (
            &json!("jobs"),
            &json!("c-2"),
            &json!("start"),
            &json!({"kind": "pluxy/hub/items"})
        )
    );
    assert!(q["id"].as_str().unwrap().starts_with("q-"));
    h.send(json!({"type": "result", "id": q["id"], "output": {"job": "j-7"}}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"job": "j-7"}})
    );
    // Refused by the host: the call gets the host's error.
    h.send(json!({"type": "call", "id": "c-3", "op": "start", "input": {}}));
    let q = h.next();
    h.send(
        json!({"type": "error", "id": q["id"], "error": {"code": "permission", "message": "no"}}),
    );
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-3", "error": {"code": "permission", "message": "no"}})
    );
    h.end().unwrap();
}

#[test]
fn a_producer_sends_batches_within_its_window_of_acks() {
    let mut h = Host::start();
    h.next();
    let job = json!({"id": "j-1", "kind": "pluxy/hub/items", "window": 2});
    h.send(json!({"type": "call", "id": "c-2", "op": "scan", "input": {"count": 5}, "job": job}));
    // Two batches go out before any ack; each later one only after the ack two behind it.
    for n in 1..=5u64 {
        assert_eq!(
            h.next(),
            json!({"type": "progress", "id": "c-2", "value": {"n": n}})
        );
        if n >= 2 {
            // Late, so a batch sent past the window would have been sent before it.
            std::thread::sleep(Duration::from_millis(50));
            h.ack("c-2", n - 1);
        }
    }
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"sent": 5}})
    );
    // Batch n was sent only once the host had acknowledged n - 2.
    assert_eq!(*h.tool.acks_at_send.lock().unwrap(), vec![0, 0, 1, 2, 3]);
    h.end().unwrap();
}

#[test]
fn a_batch_is_refused_too_big_outside_a_job_and_once_cancelled() {
    let mut h = Host::start();
    h.next();
    let job = json!({"id": "j-1", "kind": "pluxy/hub/items", "window": 4});
    // Too big: nothing is sent, the call fails.
    h.send(json!({"type": "call", "id": "c-2", "op": "big", "input": {}, "job": job}));
    let e = h.next();
    assert_eq!(
        (&e["type"], &e["error"]["code"]),
        (&json!("error"), &json!("invalid_request"))
    );
    // Not a job's call.
    h.send(json!({"type": "call", "id": "c-3", "op": "scan", "input": {"count": 1}}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-3", "error": {"code": "invalid_request", "message": "this call runs no job"}})
    );
    // Waiting for an ack, then cancelled: it ends cancelled, with no more batches.
    let job = json!({"id": "j-2", "kind": "pluxy/hub/items", "window": 1});
    h.send(json!({"type": "call", "id": "c-4", "op": "scan", "input": {"count": 3}, "job": job}));
    assert_eq!(h.next()["value"], json!({"n": 1}));
    h.send(json!({"type": "cancel", "id": "c-4"}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-4", "error": {"code": "cancelled", "message": "the call was cancelled"}})
    );
    h.end().unwrap();
}

#[test]
fn base64_is_the_standard_alphabet_with_padding() {
    // RFC 4648, section 10.
    for (plain, coded) in [
        ("", ""),
        ("f", "Zg=="),
        ("fo", "Zm8="),
        ("foo", "Zm9v"),
        ("foob", "Zm9vYg=="),
        ("fooba", "Zm9vYmE="),
        ("foobar", "Zm9vYmFy"),
    ] {
        assert_eq!(base64(plain.as_bytes()), coded);
    }
    assert_eq!(base64(&[0xfb, 0xff, 0xfe, 0x00, 0x80]), "+//+AIA=");
}

/// The next frame, which must be a `files` frame of call `call`: its ID, op and input.
fn files_frame(h: &mut Host, call: &str) -> (Value, String, Value) {
    let f = h.next();
    assert_eq!((&f["type"], &f["for"]), (&json!("files"), &json!(call)));
    assert!(f["id"].as_str().unwrap().starts_with("f-"));
    let op = f["op"].as_str().unwrap().to_string();
    (f["id"].clone(), op, f["input"].clone())
}

#[test]
fn a_call_saves_a_file_in_appends_then_one_save_and_hears_the_outcome() {
    let mut h = Host::start();
    h.next();
    // Two full appends and a short third.
    let len = 2 * APPEND_BYTES + 5;
    let bytes = save_bytes(len);
    h.send(json!({"type": "call", "id": "c-2", "op": "save", "input": {"name": "s.jsonl", "len": len}}));
    for (n, part) in bytes.chunks(APPEND_BYTES).enumerate() {
        let (id, op, input) = files_frame(&mut h, "c-2");
        assert_eq!(op, "append");
        assert_eq!(
            input,
            json!({"offset": n * APPEND_BYTES, "data": base64(part)})
        );
        h.send(
            json!({"type": "result", "id": id, "output": {"size": n * APPEND_BYTES + part.len()}}),
        );
    }
    let (id, op, input) = files_frame(&mut h, "c-2");
    assert_eq!((op.as_str(), input), ("save", json!({"name": "s.jsonl"})));
    h.send(json!({"type": "result", "id": id, "output": {"outcome": "saved", "size": len}}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"outcome": "saved", "size": len}})
    );
    h.end().unwrap();
}

#[test]
fn a_save_the_person_cancels_is_the_outcome_cancelled_and_an_empty_file_has_no_append() {
    let mut h = Host::start();
    h.next();
    h.send(
        json!({"type": "call", "id": "c-2", "op": "save", "input": {"name": "e.jsonl", "len": 0}}),
    );
    let (id, op, input) = files_frame(&mut h, "c-2");
    assert_eq!((op.as_str(), input), ("save", json!({"name": "e.jsonl"})));
    h.send(json!({"type": "result", "id": id, "output": {"outcome": "cancelled"}}));
    assert_eq!(
        h.next(),
        json!({"type": "result", "id": "c-2", "output": {"outcome": "cancelled"}})
    );
    h.end().unwrap();
}

#[test]
fn the_hosts_error_ends_the_save_at_once_and_its_message_is_the_calls() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "save", "input": {"name": "s", "len": 2 * APPEND_BYTES}}));
    let (id, _, _) = files_frame(&mut h, "c-2");
    h.send(json!({"type": "result", "id": id, "output": {"size": APPEND_BYTES}}));
    let (id, _, _) = files_frame(&mut h, "c-2");
    h.send(json!({"type": "error", "id": id, "error": {"code": "invalid_request", "message": "too much"}}));
    // No further append and no save: the next frame is the call's own error.
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-2", "error": {"code": "invalid_request", "message": "too much"}})
    );
    // A refused save is the call's error too.
    h.send(json!({"type": "call", "id": "c-3", "op": "save", "input": {"name": "a/b", "len": 0}}));
    let (id, _, _) = files_frame(&mut h, "c-3");
    h.send(json!({"type": "error", "id": id, "error": {"code": "permission", "message": "no"}}));
    assert_eq!(h.next()["error"]["code"], "permission");
    h.end().unwrap();
}

#[test]
fn an_answer_that_is_not_what_was_sent_fails_the_save_and_names_no_bytes() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "save", "input": {"name": "s", "len": 10}}));
    let (id, _, _) = files_frame(&mut h, "c-2");
    h.send(json!({"type": "result", "id": id, "output": {"size": 9}}));
    let e = h.next();
    assert_eq!(
        (&e["type"], &e["error"]["code"]),
        (&json!("error"), &json!("internal"))
    );
    h.send(json!({"type": "call", "id": "c-3", "op": "save", "input": {"name": "s", "len": 0}}));
    let (id, _, _) = files_frame(&mut h, "c-3");
    h.send(json!({"type": "result", "id": id, "output": {"outcome": "maybe"}}));
    assert_eq!(h.next()["error"]["code"], "internal");
    h.end().unwrap();
}

#[test]
fn cancelling_a_call_cancels_its_save_request() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "save", "input": {"name": "s", "len": 0}}));
    let (id, _, _) = files_frame(&mut h, "c-2");
    h.send(json!({"type": "cancel", "id": "c-2"}));
    assert_eq!(h.next(), json!({"type": "cancel", "id": id}));
    h.send(
        json!({"type": "error", "id": id, "error": {"code": "cancelled", "message": "cancelled"}}),
    );
    assert_eq!(h.next()["error"]["code"], "cancelled");
    h.end().unwrap();
}

#[test]
fn an_environment_names_a_wsl_distribution_and_user() {
    assert_eq!(
        environment::parse("wsl:Ubuntu-24.04:bob"),
        Ok(("Ubuntu-24.04", "bob"))
    );
    for bad in [
        "",
        "wsl",
        "wsl:Ubuntu",
        "wsl::bob",
        "wsl:Ubuntu:",
        "lxc:a:b",
        "wsl:a:b:c",
    ] {
        assert!(environment::parse(bad).is_err(), "{bad}");
    }
    let here = |distro: Option<&str>, user: &str, uid: u32, home: &str| environment::Here {
        distro: distro.map(String::from),
        user: user.into(),
        uid,
        home: home.into(),
    };
    let bob = "1001:/home/bob";
    assert_eq!(
        environment::compare(
            "wsl:Ubuntu:bob",
            bob,
            &here(Some("Ubuntu"), "bob", 1001, "/home/bob")
        ),
        Ok(())
    );
    assert_eq!(
        environment::compare(
            "wsl:Ubuntu:bob",
            bob,
            &here(Some("Other"), "bob", 1001, "/home/bob")
        ),
        Err("pluxy expected WSL Ubuntu/bob, this is Other/bob".into())
    );
    assert_eq!(
        environment::compare(
            "wsl:Ubuntu:bob",
            bob,
            &here(Some("Ubuntu"), "alice", 1000, "/home/alice")
        ),
        Err("pluxy expected WSL Ubuntu/bob, this is Ubuntu/alice".into())
    );
    assert_eq!(
        environment::compare("wsl:Ubuntu:bob", bob, &here(None, "bob", 1001, "/home/bob")),
        Err("pluxy expected WSL Ubuntu/bob, this is (not WSL)/bob".into())
    );
    // bob removed and made again (another number), or bob's home moved.
    assert_eq!(
        environment::compare(
            "wsl:Ubuntu:bob",
            bob,
            &here(Some("Ubuntu"), "bob", 1002, "/home/bob")
        ),
        Err(
            "pluxy expected bob with user ID 1001 and home /home/bob, this is user ID 1002 with \
             home /home/bob"
                .into()
        )
    );
    assert_eq!(
        environment::compare(
            "wsl:Ubuntu:bob",
            bob,
            &here(Some("Ubuntu"), "bob", 1001, "/srv/bob")
        ),
        Err(
            "pluxy expected bob with user ID 1001 and home /home/bob, this is user ID 1001 with \
             home /srv/bob"
                .into()
        )
    );
    assert_eq!(environment::parse_account("0:/root"), Ok((0, "/root")));
    assert_eq!(
        environment::parse_account("1001:/home/b:c"),
        Ok((1001, "/home/b:c"))
    );
    for bad in [
        "",
        "1001",
        "1001:",
        "1001:home",
        "x:/h",
        "-1:/h",
        "+1:/h",
        "99999999999:/h",
    ] {
        assert!(environment::parse_account(bad).is_err(), "{bad}");
    }
}

#[test]
fn the_user_is_the_effective_user_ids_name() {
    let status = "Name:\tx\nUid:\t1000\t1001\t1001\t1001\nGid:\t5\t5\t5\t5\n";
    assert_eq!(environment::effective_uid(status), Some(1001));
    assert_eq!(environment::effective_uid("Name:\tx\n"), None);
    let passwd = "root:x:0:0::/root:/bin/sh\nalice:x:1000:1000::/home/alice:/bin/sh\n\
                  bob:x:1001:1001::/home/bob:/bin/sh\nbob2:x:1001:1001::/home/b:/bin/sh\n";
    assert_eq!(
        environment::account_of(passwd, 1001),
        Some(("bob", "/home/bob"))
    );
    assert_eq!(environment::account_of(passwd, 0), Some(("root", "/root")));
    assert_eq!(environment::account_of(passwd, 7), None);
    assert_eq!(environment::account_of("short:x:7\n", 7), None);
}

/// This test binary run again as a child, with env, running only `identity_child`: what it said.
#[cfg(target_os = "linux")]
fn identity_in_child(env: &[(&str, &str)]) -> String {
    let env: Vec<(&str, &std::ffi::OsStr)> = env.iter().map(|(k, v)| (*k, v.as_ref())).collect();
    identity_in_child_os(&env)
}

/// As `identity_in_child`, with values that need not be UTF-8.
#[cfg(target_os = "linux")]
fn identity_in_child_os(env: &[(&str, &std::ffi::OsStr)]) -> String {
    let mut c = std::process::Command::new(std::env::current_exe().unwrap());
    c.args([
        "--exact",
        "tests::identity_child",
        "--nocapture",
        "--test-threads=1",
    ])
    .env_remove("PLUXY_ENVIRONMENT")
    .env_remove("PLUXY_ENVIRONMENT_ACCOUNT")
    .env_remove("WSL_DISTRO_NAME")
    .env("PLUXY_PROGRAM_TEST_CHILD", "1")
    .env("PLUXY_APP_CONTRACT_VERSIONS", "0")
    .env("PLUXY_EXTENSION_ID", "example/tool")
    .env("PLUXY_APP_ARTIFACT_SHA256", "ab".repeat(32));
    for (k, v) in env {
        c.env(k, v);
    }
    let out = c.output().unwrap();
    let text = String::from_utf8_lossy(&out.stdout);
    text.lines()
        .find_map(|l| l.split_once("identity: ").map(|(_, r)| r))
        .unwrap_or_else(|| panic!("the child said nothing: {text}"))
        .to_string()
}

/// Run by `identity_in_child` only: prints what `Identity::from_env` gives.
#[test]
fn identity_child() {
    if std::env::var_os("PLUXY_PROGRAM_TEST_CHILD").is_none() {
        return;
    }
    match Identity::from_env() {
        Ok(i) => println!("identity: ok {}", i.id),
        Err(e) => println!("identity: err {e}"),
    }
}

#[cfg(target_os = "linux")]
#[test]
fn a_program_in_another_distribution_or_as_another_user_refuses_to_start() {
    let me = std::process::Command::new("id")
        .arg("-un")
        .output()
        .unwrap();
    let me = String::from_utf8(me.stdout).unwrap().trim().to_string();
    // This user's number and home, from the system's own tools.
    let uid = std::process::Command::new("id").arg("-u").output().unwrap();
    let uid = String::from_utf8(uid.stdout).unwrap().trim().to_string();
    let entry = std::process::Command::new("getent")
        .args(["passwd", &uid])
        .output()
        .unwrap();
    let entry = String::from_utf8(entry.stdout).unwrap();
    let home = entry.trim().split(':').nth(5).unwrap().to_string();
    let account = format!("{uid}:{home}");
    // No environment: this computer, as before.
    assert_eq!(identity_in_child(&[]), "ok example/tool");
    // The distribution, user, user number and home named: it starts.
    let here = format!("wsl:Fake:{me}");
    let named = |env: &str, distro: Option<&str>, account: Option<&str>| {
        let mut v = vec![("PLUXY_ENVIRONMENT", env.to_string())];
        v.extend(distro.map(|d| ("WSL_DISTRO_NAME", d.to_string())));
        v.extend(account.map(|a| ("PLUXY_ENVIRONMENT_ACCOUNT", a.to_string())));
        let v: Vec<(&str, &str)> = v.iter().map(|(k, v)| (*k, v.as_str())).collect();
        identity_in_child(&v)
    };
    assert_eq!(
        named(&here, Some("Fake"), Some(&account)),
        "ok example/tool"
    );
    // Another distribution, another user, or not WSL at all: it stops before hello.
    assert_eq!(
        named(&here, Some("Other"), Some(&account)),
        format!("err pluxy expected WSL Fake/{me}, this is Other/{me}")
    );
    assert_eq!(
        named("wsl:Fake:nosuchuser", Some("Fake"), Some(&account)),
        format!("err pluxy expected WSL Fake/nosuchuser, this is Fake/{me}")
    );
    assert_eq!(
        named(&here, None, Some(&account)),
        format!("err pluxy expected WSL Fake/{me}, this is (not WSL)/{me}")
    );
    // The user's name, but another account than the record's: a user removed and made again
    // under the name (another number), or with another home.
    let other_uid = format!("{}:{home}", uid.parse::<u64>().unwrap() + 4242);
    assert_eq!(
        named(&here, Some("Fake"), Some(&other_uid)),
        format!(
            "err pluxy expected {me} with user ID {} and home {home}, this is user ID {uid} with \
             home {home}",
            uid.parse::<u64>().unwrap() + 4242
        )
    );
    assert_eq!(
        named(
            &here,
            Some("Fake"),
            Some(&format!("{uid}:/nonexistent-home"))
        ),
        format!(
            "err pluxy expected {me} with user ID {uid} and home /nonexistent-home, this is user \
             ID {uid} with home {home}"
        )
    );
    // No account, or one that is not `<user ID>:<home>`: refused, never taken for any.
    assert_eq!(
        named(&here, Some("Fake"), None),
        "err PLUXY_ENVIRONMENT_ACCOUNT is not set: start this program from pluxy's app"
    );
    for bad in ["", "x:/home/x", "1000", "1000:", "1000:home", "-1:/h"] {
        assert!(
            named(&here, Some("Fake"), Some(bad)).starts_with("err "),
            "{bad}"
        );
    }
    assert!(identity_in_child(&[("PLUXY_ENVIRONMENT", "docker:x")]).starts_with("err "));
}

#[cfg(target_os = "linux")]
#[test]
fn an_environment_that_is_not_utf8_refuses_to_start() {
    use std::os::unix::ffi::OsStrExt;
    let bad = std::ffi::OsStr::from_bytes(b"wsl:Fake:b\xffob");
    let said = identity_in_child_os(&[
        ("PLUXY_ENVIRONMENT", bad),
        ("WSL_DISTRO_NAME", "Fake".as_ref()),
    ]);
    assert_eq!(
        said,
        "err PLUXY_ENVIRONMENT is not UTF-8: start this program from pluxy's app"
    );
}

/// In a real WSL distribution (the platforms workflow's WSL job runs this inside it, as the user
/// it names): `PLUXY_WSL_TEST_EXPECT` is `<distribution>:<user>` this runs as, and
/// `PLUXY_WSL_TEST_OTHER` another distribution's name.
#[cfg(target_os = "linux")]
#[test]
#[ignore = "runs inside a real WSL distribution (next-app-tools-platforms.yml, WSL job)"]
fn wsl_real_identity_is_the_distribution_and_user_wsl_runs_this_as() {
    let expect = std::env::var("PLUXY_WSL_TEST_EXPECT").expect("PLUXY_WSL_TEST_EXPECT");
    let other = std::env::var("PLUXY_WSL_TEST_OTHER").expect("PLUXY_WSL_TEST_OTHER");
    let (distro, user) = expect.split_once(':').unwrap();
    assert_eq!(
        std::env::var("WSL_DISTRO_NAME").as_deref(),
        Ok(distro),
        "WSL sets the distribution's name"
    );
    // The user's number and home as the distribution's own tools give them (WSL sets HOME from
    // the user's entry).
    let uid = std::process::Command::new("id").arg("-u").output().unwrap();
    let uid: u32 = String::from_utf8(uid.stdout)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    let home = std::env::var("HOME").expect("HOME");
    let account = format!("{uid}:{home}");
    assert_eq!(
        environment::check(&format!("wsl:{distro}:{user}"), &account),
        Ok(())
    );
    assert_eq!(
        environment::check(&format!("wsl:{other}:{user}"), &account),
        Err(format!(
            "pluxy expected WSL {other}/{user}, this is {distro}/{user}"
        ))
    );
    assert_eq!(
        environment::check(&format!("wsl:{distro}:root"), &account),
        Err(format!(
            "pluxy expected WSL {distro}/root, this is {distro}/{user}"
        ))
    );
    assert_eq!(
        environment::check(
            &format!("wsl:{distro}:{user}"),
            &format!("{}:{home}", uid + 1)
        ),
        Err(format!(
            "pluxy expected {user} with user ID {} and home {home}, this is user ID {uid} with \
             home {home}",
            uid + 1
        ))
    );
}
