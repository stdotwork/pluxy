//! The library against a host played over real pipes.

use super::*;
use std::io::{PipeReader, PipeWriter};
use std::sync::atomic::AtomicUsize;

/// A handler whose operations exercise each part of the contract.
#[derive(Default)]
struct Tool {
    reconciled: Mutex<Vec<(Vec<Value>, Vec<Value>)>>,
    running: AtomicUsize,
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
fn a_cancel_ends_the_call_cancelled_even_if_it_finishes() {
    let mut h = Host::start();
    h.next();
    h.send(json!({"type": "call", "id": "c-2", "op": "wait", "input": {}}));
    h.send(json!({"type": "cancel", "id": "c-2"}));
    assert_eq!(
        h.next(),
        json!({"type": "error", "id": "c-2", "error": {"code": "cancelled", "message": "the call was cancelled"}})
    );
    h.send(json!({"type": "call", "id": "c-3", "op": "ignore-cancel", "input": {}}));
    h.send(json!({"type": "cancel", "id": "c-3"}));
    assert_eq!(h.next()["error"]["code"], "cancelled");
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
