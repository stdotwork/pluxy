//! The program served as the app runs it, over pipes: it answers on Linux, Windows and macOS, with
//! each system's terminals, and on Windows and macOS refuses a terminal launch with standard input
//! before it asks the host.

use super::Execution;
use pluxy_program::{read_frame, serve, write_frame, Description, Identity, MAX_INBOUND};
use serde_json::{json, Value};
use std::sync::Arc;

/// The replies to calls, served as the app runs the program.
fn served(calls: &[Value]) -> Vec<Value> {
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
    let replies = calls
        .iter()
        .map(|c| {
            write_frame(&mut in_w, c, MAX_INBOUND).unwrap();
            read_frame(&mut out_r).unwrap().unwrap()
        })
        .collect();
    drop(in_w);
    served.join().unwrap().unwrap();
    replies
}

#[test]
fn the_terminals_are_this_systems() {
    let call = json!({"type": "call", "id": "c-1", "op": "terminals",
        "input": {"search_path": []}});
    let reply = &served(&[call])[0];
    assert_eq!(reply["type"], "result", "{reply}");
    let ids: Vec<&str> = reply["output"]["terminals"]
        .as_array()
        .unwrap()
        .iter()
        .map(|t| t["id"].as_str().unwrap())
        .collect();
    let want: &[&str] = if cfg!(windows) {
        &["automatic", "powershell", "cmd", "console"]
    } else if cfg!(target_os = "macos") {
        &["automatic", "terminal"]
    } else {
        // Linux's are those on the search path: none here.
        &["automatic"]
    };
    assert_eq!(ids, want);
}

#[cfg(not(target_os = "linux"))]
#[test]
fn a_terminal_launch_with_standard_input_is_refused_before_the_host_is_asked() {
    let me = super::recipe::os::this_program().unwrap();
    let call = json!({"type": "call", "id": "c-1", "op": "launch", "input": {
        "executable": me, "argv": ["terminal", "--with", "automatic", "--", me],
        "cwd": std::env::temp_dir(), "stdin": "key"}});
    let reply = &served(&[call])[0];
    assert_eq!(reply["type"], "error", "{reply}");
    assert_eq!(reply["error"]["code"], "invalid_request", "{reply}");
    assert!(
        reply["error"]["message"]
            .as_str()
            .unwrap()
            .contains("no standard input"),
        "{reply}"
    );
}
