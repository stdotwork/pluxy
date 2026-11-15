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
