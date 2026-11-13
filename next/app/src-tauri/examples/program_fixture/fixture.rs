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
//! - `store` and `launch` with what they were given, doing nothing else: the routing check's
//!   write- and launch-class operations;
//! - `relay` by calling another package's operation through the host, as the input says, and
//!   answering with the host's answer to that;
//! - `slow` after `ms` milliseconds, with its version, process and when it answered;
//! - `crash` never: it exits at once with status 3.
//!
//! Each answer of summary, store, launch and relay also carries the `chain` the host's call named.
//!
//! `summary` also says its release's version, when it answered (milliseconds since the Unix
//! epoch) and the calls of unknown outcome the host reported in describe (`reported`).
//!
//! At start it starts one child, itself again, which sleeps in its process group. As the contract
//! asks, it ends that child before it exits at the end of its input and on SIGTERM; one that
//! crashes or ignores the end of its input leaves it to the host's group kill. A `fixture.json`
//! beside the manifest makes it misbehave for the unit tests (`Mode`).

use serde_json::{json, Value};
use std::io::{self, Read, Write};
#[cfg(unix)]
use std::sync::atomic::{AtomicI32, Ordering};
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
    /// Leaves its process group for the app's, once its child has started.
    leave_group: bool,
    /// Adds this many bytes to `summary`'s output.
    pad: Option<usize>,
    /// Sends a result before hello.
    before_hello: bool,
    /// Sends a frame of an unknown type before `summary`'s result.
    unknown_frame: bool,
    /// Says hello with these contract versions instead.
    hello_versions: Option<Value>,
    /// Describes this state version instead of the manifest's.
    state_version: Option<Value>,
    /// Answers `summary` with an error with this message.
    error_message: Option<String>,
    /// Writes this many lines to stderr at start, then waits a little over a second before the
    /// `stderr` lines.
    stderr_burst: usize,
    /// Exits with status 3 right after it has described itself.
    exit_after_describe: bool,
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
            leave_group: v["leave_group"] == true,
            pad: v["pad"].as_u64().map(|n| n as usize),
            before_hello: v["before_hello"] == true,
            unknown_frame: v["unknown_frame"] == true,
            hello_versions: v.get("hello_versions").cloned(),
            state_version: v.get("state_version").cloned(),
            error_message: s("error_message"),
            stderr_burst: v["stderr_burst"].as_u64().unwrap_or_default() as usize,
            exit_after_describe: v["exit_after_describe"] == true,
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

/// Calls `input.package`'s `input.op` with `input.input` through the host, for call f, with
/// `input.extra`'s fields added to the frame (and `input.for` instead of f's ID when given), and
/// waits for the host's answer: `{"output": ...}` or `{"error": ...}`. A cancel of f while it
/// waits is passed on as a cancel of this call.
fn relay(input: &mut impl Read, output: &mut impl Write, f: &Value, n: u64) -> Value {
    let rid = format!("o-{n}");
    let mut call = json!({
        "type": "call",
        "id": rid,
        "for": f["input"].get("for").cloned().unwrap_or_else(|| f["id"].clone()),
        "package": f["input"]["package"],
        "op": f["input"]["op"],
        "input": f["input"]["input"],
    });
    if let (Some(extra), Some(frame)) = (f["input"]["extra"].as_object(), call.as_object_mut()) {
        for (k, v) in extra {
            frame.insert(k.clone(), v.clone());
        }
    }
    write_frame(output, &call);
    while let Some(m) = read_frame(input) {
        match m["type"].as_str() {
            Some("result") if m["id"] == rid.as_str() => return json!({"output": m["output"]}),
            Some("error") if m["id"] == rid.as_str() => return json!({"error": m["error"]}),
            Some("cancel") if m["id"] == f["id"] => {
                write_frame(output, &json!({"type": "cancel", "id": rid}));
            }
            _ => {}
        }
    }
    json!({"error": {"code": "extension_unavailable", "message": "the host closed the pipe"}})
}

/// Its child's process ID, for the SIGTERM handler.
#[cfg(unix)]
static CHILD_PID: AtomicI32 = AtomicI32::new(0);

/// On SIGTERM (the parent-death signal) it ends its child, then exits.
#[cfg(unix)]
extern "C" fn on_term(_: libc::c_int) {
    let child = CHILD_PID.load(Ordering::SeqCst);
    unsafe {
        if child > 0 {
            libc::kill(child, libc::SIGKILL);
        }
        libc::_exit(143);
    }
}

/// Ends its child and exits: the end of its input.
fn exit(child: &mut Option<std::process::Child>) -> ! {
    if let Some(c) = child {
        let _ = c.kill();
        let _ = c.wait();
    }
    std::process::exit(0);
}

/// The parent-death signal it was given, if any.
fn pdeathsig() -> i64 {
    #[cfg(target_os = "linux")]
    {
        let mut sig: libc::c_int = 0;
        unsafe { libc::prctl(libc::PR_GET_PDEATHSIG, &mut sig as *mut libc::c_int) };
        i64::from(sig)
    }
    #[cfg(not(target_os = "linux"))]
    {
        0
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
    let mut child_process = std::env::current_exe()
        .and_then(|exe| {
            std::process::Command::new(exe)
                .env_clear()
                .env(CHILD, "1")
                .stdin(std::process::Stdio::null())
                .stdout(std::process::Stdio::null())
                .stderr(std::process::Stdio::null())
                .spawn()
        })
        .ok();
    let child = child_process.as_ref().map(|c| c.id());
    #[cfg(unix)]
    unsafe {
        CHILD_PID.store(child.unwrap_or_default() as i32, Ordering::SeqCst);
        libc::signal(libc::SIGTERM, on_term as *const () as libc::sighandler_t);
        if mode.leave_group {
            libc::setpgid(0, libc::getpgid(libc::getppid()));
        }
    }
    {
        let mut err = io::stderr().lock();
        for i in 0..mode.stderr_burst {
            let _ = writeln!(err, "burst {i}");
        }
        if mode.stderr_burst > 0 {
            std::thread::sleep(Duration::from_millis(1100));
        }
        for line in &mode.stderr {
            let _ = writeln!(err, "{line}");
        }
    }
    let mut input = io::stdin().lock();
    let mut output = io::stdout().lock();
    if mode.silent {
        while read_frame(&mut input).is_some() {}
        exit(&mut child_process);
    }
    if mode.before_hello {
        write_frame(
            &mut output,
            &json!({"type": "result", "id": "c-0", "output": {}}),
        );
    }
    let env = |k: &str| std::env::var(k).unwrap_or_default();
    let versions = mode.hello_versions.clone().unwrap_or_else(|| {
        let v: Vec<i64> = env("PLUXY_APP_CONTRACT_VERSIONS")
            .split(',')
            .filter_map(|v| v.trim().parse().ok())
            .filter(|v| *v == 0)
            .collect();
        json!(v)
    });
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
    let mut relayed = 0u64;
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
        let chain = f["chain"].clone();
        match (f["type"].as_str(), f["op"].as_str()) {
            (Some("call"), Some("describe")) => {
                reported = f["unknown"].clone();
                let mut ops = manifest["app"]["operations"].clone();
                if let (Some(extra), Some(list)) = (&mode.extra_operation, ops.as_array_mut()) {
                    list.push(json!({"id": extra, "description": "Not in the manifest",
                        "input_schema": {"type": "object"}, "output_schema": {"type": "object"}}));
                }
                let mut state = manifest["app"]["state"].clone();
                if let Some(v) = &mode.state_version {
                    state["version"] = v.clone();
                }
                write_frame(
                    &mut output,
                    &json!({"type": "result", "id": id, "operations": ops, "state": state}),
                );
                if mode.exit_after_describe {
                    std::process::exit(3);
                }
            }
            (Some("call"), Some(op)) => {
                calls += 1;
                if mode.crash_on.as_deref() == Some(op) {
                    std::process::exit(3);
                }
                match op {
                    "summary" if mode.error_message.is_some() => write_frame(
                        &mut output,
                        &error(&id, "internal", mode.error_message.as_deref().unwrap_or("")),
                    ),
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
                        let mut out = mode.output.clone().unwrap_or_else(|| {
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
                                "chain": chain,
                                "version": version,
                                "at": at(),
                                "reported": reported,
                                "pdeathsig": pdeathsig(),
                                "arg0": std::env::args_os().next()
                                    .map(|a| a.to_string_lossy().into_owned()),
                            })
                        });
                        if let Some(n) = mode.pad {
                            out["pad"] = json!("x".repeat(n));
                        }
                        if mode.unknown_frame {
                            write_frame(&mut output, &json!({"type": "bogus", "id": id}));
                        }
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
                    "store" => write_frame(
                        &mut output,
                        &json!({"type": "result", "id": id, "output":
                            {"stored": f["input"]["value"], "calls": calls, "chain": chain}}),
                    ),
                    "launch" => write_frame(
                        &mut output,
                        &json!({"type": "result", "id": id, "output":
                            {"launched": f["input"]["argv"], "calls": calls, "chain": chain}}),
                    ),
                    "relay" => {
                        relayed += 1;
                        let answer = relay(&mut input, &mut output, &f, relayed);
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output":
                                {"answer": answer, "calls": calls, "chain": chain}}),
                        );
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
    exit(&mut child_process);
}
