//! The manifest, the input checks every operation makes before `wsl.exe` runs, and that no test
//! here stands in for `wsl.exe` (the real-WSL tests are `tests/real_wsl.rs`).

use super::Wsl;
use pluxy_program::{read_frame, serve, write_frame, Description, Identity, MAX_INBOUND};
use serde_json::{json, Value};
use std::sync::Arc;

fn manifest() -> Value {
    serde_json::from_str(include_str!("../pluxy-extension.json")).unwrap()
}

/// The replies to calls, served as the app runs the program.
fn served(calls: &[Value]) -> Vec<Value> {
    let (in_r, mut in_w) = std::io::pipe().unwrap();
    let (mut out_r, out_w) = std::io::pipe().unwrap();
    let description =
        Description::from_manifest(include_bytes!("../pluxy-extension.json")).unwrap();
    let identity = Identity {
        id: "pluxy/wsl".into(),
        sha256: "ab".repeat(32),
    };
    let served =
        std::thread::spawn(move || serve(in_r, out_w, &identity, description, Arc::new(Wsl)));
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
fn every_operation_is_a_read_and_the_program_is_windows_only() {
    let m = manifest();
    let ops = m["app"]["operations"].as_array().unwrap();
    let ids: Vec<&str> = ops.iter().map(|o| o["id"].as_str().unwrap()).collect();
    assert_eq!(ids, ["distros", "users", "check", "prepare"]);
    assert!(ops.iter().all(|o| o["effect"] == "read"));
    assert!(m["app"].get("exports").is_none());
    assert_eq!(
        m["app"]["programs"],
        json!({"windows/amd64": "bin/pluxy-wsl.exe", "windows/arm64": "bin/pluxy-wsl.exe"})
    );
    for o in ops.iter().skip(1) {
        let p = &o["input_schema"]["properties"];
        // The design's limits: 64 characters for a distribution, 32 bytes for a user.
        assert_eq!(p["distro"]["maxLength"], 64);
        assert_eq!(crate::list::MAX_DISTRO, 64);
        if o["id"] != "users" {
            assert_eq!(p["user"]["maxLength"], 32);
            assert_eq!(crate::list::MAX_USER, 32);
        }
    }
}

#[test]
fn names_pluxy_does_not_accept_are_refused_before_wsl_runs() {
    let call = |id: &str, op: &str, input: Value| json!({"type": "call", "id": id, "op": op, "input": input});
    let replies = served(&[
        call("1", "users", json!({"distro": "-u"})),
        call("2", "users", json!({"distro": "docker-desktop"})),
        call("3", "check", json!({"distro": "Ubuntu", "user": "Bob"})),
        call("4", "check", json!({"distro": "a/b", "user": "bob"})),
        call(
            "5",
            "prepare",
            json!({"distro": "Ubuntu", "user": "bob", "kind": "open", "program": "x"}),
        ),
        call(
            "6",
            "prepare",
            json!({"distro": "Ubuntu", "user": "-x", "kind": "direct", "program": "x"}),
        ),
        call("7", "nope", json!({})),
    ]);
    for r in &replies[..6] {
        assert_eq!(r["type"], "error", "{r}");
        assert_eq!(r["error"]["code"], "invalid_request", "{r}");
    }
    assert_eq!(replies[6]["error"]["code"], "unsupported");
}

#[cfg(not(windows))]
#[test]
fn other_systems_refuse_every_operation() {
    let replies = served(&[
        json!({"type": "call", "id": "1", "op": "distros", "input": {}}),
        json!({"type": "call", "id": "2", "op": "check", "input": {"distro": "Ubuntu", "user": "bob"}}),
    ]);
    for r in replies {
        assert_eq!(r["error"]["code"], "unsupported", "{r}");
        assert_eq!(r["error"]["message"], "WSL is on Windows only");
    }
}

#[test]
fn no_test_stands_in_for_wsl_exe() {
    // A Linux script or Windows folder dressed up as WSL would prove nothing (the real-WSL tests
    // run Windows' own wsl.exe and a real distribution): no file of this package is named like
    // wsl.exe, no source writes one, and the program finds wsl.exe only in System32.
    let writes = "write(";
    let exe = "wsl.exe";
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
    let mut stack = vec![root.to_path_buf()];
    while let Some(d) = stack.pop() {
        for e in std::fs::read_dir(&d).unwrap().flatten() {
            let p = e.path();
            let name = e.file_name().to_string_lossy().to_lowercase();
            if p.is_dir() {
                stack.push(p);
                continue;
            }
            assert!(
                !["wsl", "wsl.exe", "wsl.bat", "wsl.cmd", "wsl.sh", "wsl.ps1"]
                    .contains(&name.as_str()),
                "{} stands in for wsl.exe",
                p.display()
            );
            if name.ends_with(".rs") {
                let text = std::fs::read_to_string(&p).unwrap();
                assert!(
                    !text.lines().any(|l| l.contains(writes) && l.contains(exe)),
                    "{} writes a wsl.exe",
                    p.display()
                );
            }
        }
    }
    let exec = include_str!("exec.rs");
    assert!(exec.contains("GetSystemDirectoryW"));
    assert!(
        !exec.contains("env::var"),
        "exec.rs takes no path from the environment"
    );
}
