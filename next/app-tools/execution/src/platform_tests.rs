//! The program served as the app runs it, over pipes: on Linux it answers; on any other system it
//! refuses every operation as unsupported.

use super::Execution;
use pluxy_program::{read_frame, serve, write_frame, Description, Identity, MAX_INBOUND};
use serde_json::json;
use std::sync::Arc;

#[test]
fn every_operation_is_refused_off_linux_and_answered_on_linux() {
    let (in_r, mut in_w) = std::io::pipe().unwrap();
    let (mut out_r, out_w) = std::io::pipe().unwrap();
    let description =
        Description::from_manifest(include_bytes!("../pluxy-extension.json")).unwrap();
    let identity = Identity {
        id: "pluxy/execution".into(),
        sha256: "ab".repeat(32),
    };
    let served =
        std::thread::spawn(move || serve(in_r, out_w, &identity, description, Arc::new(Execution)));
    assert_eq!(read_frame(&mut out_r).unwrap().unwrap()["type"], "hello");
    let call = json!({"type": "call", "id": "c-1", "op": "terminals",
        "input": {"search_path": []}});
    write_frame(&mut in_w, &call, MAX_INBOUND).unwrap();
    let reply = read_frame(&mut out_r).unwrap().unwrap();
    drop(in_w);
    served.join().unwrap().unwrap();
    if cfg!(target_os = "linux") {
        assert_eq!(reply["type"], "result", "{reply}");
    } else {
        assert_eq!(
            reply,
            json!({"type": "error", "id": "c-1", "error": {"code": "unsupported",
                "message": "pluxy/execution runs on Linux only for now"}})
        );
    }
}
