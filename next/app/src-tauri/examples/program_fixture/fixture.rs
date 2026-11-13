//! A disposable native tool for the program host's checks: the native test's summary tool
//! (`native/program.ts` packs it, `src/program_native.rs` drives it) and the host's own unit
//! tests. It speaks app program contract v0 (`app-host/README.md`) on stdin and stdout.
//!
//! It reads its manifest, `pluxy-extension.json`, from its working folder, which the host sets to
//! its package folder, and answers:
//!
//! - `describe` with the manifest's operations and state;
//! - `summary` with what it was given (`count`, `longest`) and what it sees of itself: its process
//!   and group, the child it started, the calls it has had, its environment's names, its open
//!   file descriptors and its working folder;
//! - `wait` only once it is cancelled;
//! - `slow` after `ms` milliseconds, with its version, process and when it answered;
//! - `crash` never: it exits at once with status 3.
//!
//! `summary` also says its release's version, when it answered (milliseconds since the Unix
//! epoch) and the calls of unknown outcome the host reported in describe (`reported`).
//!
//! At start it starts one child, itself again, which sleeps in its process group until killed, so
//! the checks can see the host stop the whole group. It exits at the end of its input. A
//! `fixture.json` beside the manifest makes it misbehave for the unit tests (`Mode`).

use serde_json::{json, Value};
use std::io::{self, Read, Write};
use std::time::Duration;

/// The variable the fixture's child is started with, and nothing else.
pub const CHILD: &str = "PLUXY_FIXTURE_CHILD";

/// How the unit tests make it misbehave, from `fixture.json`. Absent: it behaves.
#[derive(Default)]
struct Mode {
    /// Says hello with this digest instead of its own.
    hello_sha256: Option<String>,
    /// Says nothing at all.
    silent: bool,
    /// Describes this operation besides the manifest's.
    extra_operation: Option<String>,
    /// Exits with status 3 when this operation is called.
    crash_on: Option<String>,
    /// Keeps running after its input ends.
    ignore_eof: bool,
    /// Never answers a cancel.
    ignore_cancel: bool,
    /// Writes these lines to stderr at start.
    stderr: Vec<String>,
    /// Answers `summary` with this output instead.
    output: Option<Value>,
}

impl Mode {
    fn read() -> Mode {
        let Some(v) = std::fs::read("fixture.json")
            .ok()
            .and_then(|b| serde_json::from_slice::<Value>(&b).ok())
        else {
            return Mode::default();
        };
        let s = |k: &str| v[k].as_str().map(String::from);
        Mode {
            hello_sha256: s("hello_sha256"),
            silent: v["silent"] == true,
            extra_operation: s("extra_operation"),
            crash_on: s("crash_on"),
            ignore_eof: v["ignore_eof"] == true,
            ignore_cancel: v["ignore_cancel"] == true,
            stderr: v["stderr"]
                .as_array()
                .map(|a| {
                    a.iter()
                        .filter_map(|l| l.as_str().map(String::from))
                        .collect()
                })
                .unwrap_or_default(),
            output: v.get("output").cloned(),
        }
    }
}

fn read_frame(r: &mut impl Read) -> Option<Value> {
    let mut len = [0u8; 4];
    r.read_exact(&mut len).ok()?;
    let n = u32::from_be_bytes(len) as usize;
    if n > 64 << 20 {
        return None;
    }
    let mut body = vec![0u8; n];
    r.read_exact(&mut body).ok()?;
    serde_json::from_slice(&body).ok()
}

fn write_frame(w: &mut impl Write, v: &Value) {
    let body = serde_json::to_vec(v).expect("a JSON value");
    let _ = w.write_all(&(body.len() as u32).to_be_bytes());
    let _ = w.write_all(&body);
    let _ = w.flush();
}

fn error(id: &Value, code: &str, message: &str) -> Value {
    json!({"type": "error", "id": id, "error": {"code": code, "message": message}})
}

/// The file descriptors open in this process, but for the one listing them.
fn fds() -> Vec<u32> {
    let mut out: Vec<u32> = std::fs::read_dir("/proc/self/fd")
        .map(|d| {
            d.filter_map(|e| e.ok())
                .filter_map(|e| e.file_name().to_str()?.parse().ok())
                .collect()
        })
        .unwrap_or_default();
    // The listing's own descriptor no longer exists once it is closed.
    out.retain(|fd| std::fs::read_link(format!("/proc/self/fd/{fd}")).is_ok());
    out.sort();
    out
}

fn sleep_forever() -> ! {
    loop {
        std::thread::sleep(Duration::from_secs(3600));
    }
}

/// Runs the fixture: as its child when started with `CHILD`, else as the tool.
pub fn main() -> ! {
    if std::env::var_os(CHILD).is_some() {
        sleep_forever();
    }
    let mode = Mode::read();
    let manifest: Value = std::fs::read("pluxy-extension.json")
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
        .unwrap_or(Value::Null);
    let child = std::env::current_exe()
        .and_then(|exe| {
            std::process::Command::new(exe)
                .env_clear()
                .env(CHILD, "1")
                .stdin(std::process::Stdio::null())
                .stdout(std::process::Stdio::null())
                .stderr(std::process::Stdio::null())
                .spawn()
        })
        .map(|c| c.id())
        .ok();
    {
        let mut err = io::stderr().lock();
        for line in &mode.stderr {
            let _ = writeln!(err, "{line}");
        }
    }
    let mut input = io::stdin().lock();
    let mut output = io::stdout().lock();
    if mode.silent {
        while read_frame(&mut input).is_some() {}
        std::process::exit(0);
    }
    let env = |k: &str| std::env::var(k).unwrap_or_default();
    let versions: Vec<i64> = env("PLUXY_APP_CONTRACT_VERSIONS")
        .split(',')
        .filter_map(|v| v.trim().parse().ok())
        .filter(|v| *v == 0)
        .collect();
    write_frame(
        &mut output,
        &json!({
            "type": "hello",
            "contract": "app",
            "versions": versions,
            "id": env("PLUXY_EXTENSION_ID"),
            "sha256": mode.hello_sha256.clone().unwrap_or_else(|| env("PLUXY_APP_ARTIFACT_SHA256")),
        }),
    );
    let mut calls = 0u64;
    let mut waiting: Option<Value> = None;
    let mut reported = Value::Null;
    let version = manifest["version"].clone();
    let at = || {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_millis() as u64)
            .unwrap_or(0)
    };
    while let Some(f) = read_frame(&mut input) {
        let id = f["id"].clone();
        match (f["type"].as_str(), f["op"].as_str()) {
            (Some("call"), Some("describe")) => {
                reported = f["unknown"].clone();
                let mut ops = manifest["app"]["operations"].clone();
                if let (Some(extra), Some(list)) = (&mode.extra_operation, ops.as_array_mut()) {
                    list.push(json!({"id": extra, "description": "Not in the manifest",
                        "input_schema": {"type": "object"}, "output_schema": {"type": "object"}}));
                }
                write_frame(
                    &mut output,
                    &json!({"type": "result", "id": id, "operations": ops,
                        "state": manifest["app"]["state"]}),
                );
            }
            (Some("call"), Some(op)) => {
                calls += 1;
                if mode.crash_on.as_deref() == Some(op) {
                    std::process::exit(3);
                }
                match op {
                    "summary" => {
                        let items: Vec<&str> = f["input"]["items"]
                            .as_array()
                            .map(|a| a.iter().filter_map(Value::as_str).collect())
                            .unwrap_or_default();
                        let longest =
                            items
                                .iter()
                                .copied()
                                .fold("", |a, b| if b.len() > a.len() { b } else { a });
                        let mut names: Vec<String> = std::env::vars_os()
                            .map(|(k, _)| k.to_string_lossy().into_owned())
                            .collect();
                        names.sort();
                        #[cfg(unix)]
                        let pgid = unsafe { libc::getpgrp() };
                        #[cfg(not(unix))]
                        let pgid = 0;
                        let out = mode.output.clone().unwrap_or_else(|| {
                            json!({
                                "count": items.len(),
                                "longest": longest,
                                "pid": std::process::id(),
                                "pgid": pgid,
                                "child": child,
                                "calls": calls,
                                "env": names,
                                "fds": fds(),
                                "cwd": std::env::current_dir().ok(),
                                "version": version,
                                "at": at(),
                                "reported": reported,
                            })
                        });
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output": out}),
                        );
                    }
                    "wait" => {
                        write_frame(
                            &mut output,
                            &json!({"type": "progress", "id": id, "value": {"waiting": true}}),
                        );
                        waiting = Some(id);
                    }
                    "slow" => {
                        let ms = f["input"]["ms"].as_u64().unwrap_or(0);
                        std::thread::sleep(Duration::from_millis(ms));
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output": {
                                "version": version, "pid": std::process::id(), "at": at()}}),
                        );
                    }
                    "crash" => std::process::exit(3),
                    _ => write_frame(
                        &mut output,
                        &error(&id, "unsupported", &format!("no operation {op}")),
                    ),
                }
            }
            (Some("cancel"), _) if !mode.ignore_cancel && waiting.as_ref() == Some(&id) => {
                waiting = None;
                write_frame(
                    &mut output,
                    &error(&id, "cancelled", "the call was cancelled"),
                );
            }
            _ => {}
        }
    }
    if mode.ignore_eof {
        sleep_forever();
    }
    std::process::exit(0);
}
