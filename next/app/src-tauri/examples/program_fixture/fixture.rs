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
//! - `crash` never: it exits at once with status 3;
//! - `flood` never: it sends 2000 call frames the host refuses, and reads nothing more;
//! - `apply`, the vault check's writer (`src/vault_native.rs`): takes the secret the host delivered
//!   for its `key` reference, keeps the file's old bytes as a vault backup, begins a vault journal
//!   naming the new bytes' SHA-256, writes the file (and counts its writes in `<file>.writes`),
//!   logs the key on stderr and, unless `crash` is set, commits the journal (`crash_before` exits
//!   before it writes the file; `leave_open` marks the journal's intent so that describe leaves it
//!   open, as a tool that never reconciles would); it then writes its
//!   reference to `leak` and forwards a reference (its own, or `forward.key_from`'s, or the raw
//!   value with `forward.raw`) to `forward.package`'s `forward.op`, and answers with the key in
//!   `echo`, as a leaky tool would;
//! - `report` with the journals it reconciled at its last describe and the last key it took (and
//!   what `take` kept, below);
//! - `run`, the vault check's execution program: appends the frame it got to `record`, then calls
//!   `consumer` with its own reference (or `steal`'s) and `out`, and appends the answer it got
//!   back to `record` too;
//! - `deliver`, the vault check's final consumer: writes the secret delivered for `key` to `out`;
//! - `start`, the spawn check's launcher (`src/spawn_native.rs`): appends the frame it got to
//!   `record`, then asks the host to spawn the launch the call confirmed (a `spawn` frame naming
//!   nothing but the call) and answers with the host's answer (with `again`, it asks a second
//!   time for the same call, and answers that answer as `again` too);
//! - `runtime` by sending a `runtime` frame with the input's `op` and `runtime` for the call (or,
//!   for op `spawn`, a spawn request for that call), and answering with the host's answer;
//! - `serve`, the client-key check's agent (`src/client_key_native.rs`): sends `GET /v1/models` to
//!   the core at `base` (plain HTTP) with the secret delivered for `key` as its bearer token, writes
//!   the status core answered to `out` and answers with it and its process ID.
//!
//! Started with `AGENT` as its first argument it is the spawn check's agent instead (`agent`).
//!
//! In describe, it reconciles each open vault journal the host names, never redoing it: it ends it
//! as `committed` when the file holds the bytes the journal names, `rolled_back` otherwise, and
//! leaves open one whose intent says `leave_open`. With `describe_vault` in `fixture.json`, it also
//! sends that vault request in describe; `report` gives the host's answer with the journals.
//!
//! As a job's consumer and producer (`app-host/README.md`, "Jobs"; `src/jobs_native.rs`):
//!
//! - `jobs` sends one `jobs` frame to the host, `{op, input}` from its input, for this call, and
//!   answers with the host's answer: `{"answer": {"output": ...}}` or `{"answer": {"error": ...}}`;
//! - `take`, a deliver operation, keeps what the host delivered (its input and the call's chain),
//!   waits `value.hold` milliseconds when the batch names it, and answers `{"ok": true}`;
//! - `report` also answers with everything `take` kept, in order (`deliveries`);
//! - `scan`, a job's operation, sends `count` batches `{"n", "item": "item-<n>", "hold"}` as
//!   progress, never more than the job's window ahead of the host's acks, waits until each is
//!   acknowledged and answers `{"sent", "acked", "max_unacked", "window"}`. `mode` makes it
//!   misbehave: `flood` sends twice the window and one more without waiting, `forge` sends one
//!   batch naming a `source`, `big` one batch padded past the host's limit, and `wait` one batch;
//!   each then waits for its cancel.
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
/// The first argument that makes it the spawn check's agent (`agent`); its group member and its
/// orphan get this with `-child` and `-orphan` added.
pub const AGENT: &str = "pluxy-fixture-agent";

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
    /// A vault request it sends in describe, for the describe.
    describe_vault: Option<Value>,
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
            describe_vault: v.get("describe_vault").cloned(),
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
    let own = format!("o-{n}");
    let mut call = json!({
        "type": "call",
        "id": own,
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
    // The ID sent, which extra may have replaced: the host answers that one.
    let rid = call["id"].clone();
    write_frame(output, &call);
    while let Some(m) = read_frame(input) {
        match m["type"].as_str() {
            Some("result") if m["id"] == rid => return json!({"output": m["output"]}),
            Some("error") if m["id"] == rid => return json!({"error": m["error"]}),
            Some("cancel") if m["id"] == f["id"] => {
                write_frame(output, &json!({"type": "cancel", "id": rid}));
            }
            _ => {}
        }
    }
    json!({"error": {"code": "extension_unavailable", "message": "the host closed the pipe"}})
}

/// Sends frame (a vault request or a call through the host) and waits for the host's answer
/// to it: `{"output": ...}` or `{"error": ...}`.
fn ask(input: &mut impl Read, output: &mut impl Write, frame: Value) -> Value {
    let rid = frame["id"].clone();
    write_frame(output, &frame);
    while let Some(m) = read_frame(input) {
        match m["type"].as_str() {
            Some("result") if m["id"] == rid => return json!({"output": m["output"]}),
            Some("error") if m["id"] == rid => return json!({"error": m["error"]}),
            _ => {}
        }
    }
    json!({"error": {"code": "extension_unavailable", "message": "the host closed the pipe"}})
}

/// Reads frames for job call id until its cancel, which it answers, or the end of its input,
/// counting acks in acked.
fn until_cancelled(input: &mut impl Read, output: &mut impl Write, id: &Value, acked: &mut u64) {
    while let Some(m) = read_frame(input) {
        match m["type"].as_str() {
            Some("ack") if m["id"] == *id => *acked = m["seq"].as_u64().unwrap_or(*acked),
            Some("cancel") if m["id"] == *id => {
                write_frame(output, &error(id, "cancelled", "the job was cancelled"));
                return;
            }
            _ => {}
        }
    }
}

/// Runs job call f of `scan` (see the module's description).
fn scan(input: &mut impl Read, output: &mut impl Write, f: &Value) {
    let id = f["id"].clone();
    let window = f["job"]["window"].as_u64().unwrap_or(0);
    let count = f["input"]["count"].as_u64().unwrap_or(0);
    let hold = f["input"]["hold"].clone();
    let batch = |n: u64| json!({"type": "progress", "id": id, "value": {"n": n, "item": format!("item-{n}"), "hold": hold}});
    let (mut sent, mut acked, mut max_unacked) = (0u64, 0u64, 0u64);
    if window == 0 {
        return write_frame(
            output,
            &error(&id, "invalid_request", "scan runs only as a job"),
        );
    }
    match f["input"]["mode"].as_str().unwrap_or_default() {
        "flood" => {
            for n in 1..=window * 2 + 1 {
                write_frame(output, &batch(n));
            }
            return until_cancelled(input, output, &id, &mut acked);
        }
        "forge" => {
            let mut b = batch(1);
            b["source"] = json!({"package": "pluxy/jobs-other"});
            write_frame(output, &b);
            return until_cancelled(input, output, &id, &mut acked);
        }
        "big" => {
            let mut b = batch(1);
            b["value"]["pad"] = json!("x".repeat(60 << 10));
            write_frame(output, &b);
            return until_cancelled(input, output, &id, &mut acked);
        }
        "wait" => {
            write_frame(output, &batch(1));
            return until_cancelled(input, output, &id, &mut acked);
        }
        _ => {}
    }
    /// Reads one frame for job call id: an ack, or its cancel (answered: false, the job ends).
    fn next(input: &mut impl Read, output: &mut impl Write, id: &Value, acked: &mut u64) -> bool {
        match read_frame(input) {
            Some(m) if m["type"] == "ack" && m["id"] == *id => {
                *acked = m["seq"].as_u64().unwrap_or(*acked);
                true
            }
            Some(m) if m["type"] == "cancel" && m["id"] == *id => {
                write_frame(output, &error(id, "cancelled", "the job was cancelled"));
                false
            }
            Some(_) => true,
            None => std::process::exit(0),
        }
    }
    for n in 1..=count {
        while sent - acked >= window {
            if !next(input, output, &id, &mut acked) {
                return;
            }
        }
        write_frame(output, &batch(n));
        sent += 1;
        max_unacked = max_unacked.max(sent - acked);
    }
    while acked < sent {
        if !next(input, output, &id, &mut acked) {
            return;
        }
    }
    write_frame(
        output,
        &json!({"type": "result", "id": id, "output": {"sent": sent, "acked": acked,
            "max_unacked": max_unacked, "window": window}}),
    );
}

fn sha256_hex(b: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(b)
        .iter()
        .map(|x| format!("{x:02x}"))
        .collect()
}

/// The secret the host delivered with call frame f for its input field, if any.
fn delivered(f: &Value, field: &str) -> Option<String> {
    let reference = f["input"][field].as_str()?;
    f["secrets"][reference].as_str().map(String::from)
}

/// The client-key check's agent: the status of `GET <base>/v1/models` with key as its bearer
/// token, over plain HTTP to a loopback core; 0 when core could not be asked.
fn serve(base: &str, key: &str) -> u16 {
    use std::io::BufRead;
    let Some(addr) = base.strip_prefix("http://") else {
        return 0;
    };
    let Ok(mut s) = std::net::TcpStream::connect(addr) else {
        return 0;
    };
    let _ = s.set_read_timeout(Some(Duration::from_secs(10)));
    let req = format!(
        "GET /v1/models HTTP/1.1\r\nHost: {addr}\r\nAuthorization: Bearer {key}\r\nConnection: close\r\n\r\n"
    );
    if s.write_all(req.as_bytes()).is_err() {
        return 0;
    }
    let mut line = String::new();
    if io::BufReader::new(s).read_line(&mut line).is_err() {
        return 0;
    }
    line.split(' ')
        .nth(1)
        .and_then(|c| c.parse().ok())
        .unwrap_or(0)
}

/// The vault check's writer: see the module's `apply`.
fn apply(
    input: &mut impl Read,
    output: &mut impl Write,
    f: &Value,
    n: &mut u64,
    last_key: &mut Value,
) -> Value {
    let mut next = || {
        *n += 1;
        format!("v-{n}")
    };
    let i = &f["input"];
    let Some(key) = delivered(f, "key") else {
        return error(
            &f["id"],
            "invalid_request",
            "no secret was delivered for key",
        );
    };
    *last_key = json!(key);
    let file = std::path::PathBuf::from(i["file"].as_str().unwrap_or_default());
    let old = std::fs::read(&file).unwrap_or_default();
    use base64::Engine;
    let backup = ask(
        input,
        output,
        json!({"type": "vault", "id": next(), "for": f["id"], "op": "backup.put",
            "label": "original", "data": base64::engine::general_purpose::STANDARD.encode(&old)}),
    );
    let content = format!("key={key}\n");
    let journal = ask(
        input,
        output,
        json!({"type": "vault", "id": next(), "for": f["id"], "op": "journal.begin",
            "intent": {"file": file, "sha256": sha256_hex(content.as_bytes()),
                "leave_open": i["leave_open"] == true}}),
    );
    let Some(j) = journal["output"]["journal"].as_str().map(String::from) else {
        return error(&f["id"], "internal", &format!("journal.begin: {journal}"));
    };
    if i["crash_before"] == true {
        std::process::exit(3);
    }
    let tmp = file.with_extension("tmp");
    let writes = file.with_extension("writes");
    let count: u64 = std::fs::read_to_string(&writes)
        .ok()
        .and_then(|s| s.trim().parse().ok())
        .unwrap_or(0);
    if std::fs::write(&tmp, &content).is_err()
        || std::fs::rename(&tmp, &file).is_err()
        || std::fs::write(&writes, format!("{}", count + 1)).is_err()
    {
        return error(&f["id"], "internal", "could not write the file");
    }
    eprintln!("writer: applied key {key} to {}", file.display());
    if i["crash"] == true {
        std::process::exit(3);
    }
    let commit = ask(
        input,
        output,
        json!({"type": "vault", "id": next(), "for": f["id"], "op": "journal.commit", "journal": j}),
    );
    if let Some(path) = i["leak"].as_str() {
        let _ = std::fs::write(path, i["key"].as_str().unwrap_or_default());
    }
    let mut answer = Value::Null;
    if let Some(fw) = i.get("forward").filter(|v| v.is_object()) {
        let reference = if fw["raw"] == true {
            json!(key)
        } else if let Some(p) = fw["key_from"].as_str() {
            json!(std::fs::read_to_string(p).unwrap_or_default())
        } else {
            i["key"].clone()
        };
        let mut call_input = fw["input"].clone();
        if !call_input.is_object() {
            call_input = json!({});
        }
        call_input["key"] = reference;
        answer = ask(
            input,
            output,
            json!({"type": "call", "id": next(), "for": f["id"], "package": fw["package"],
                "op": fw["op"], "input": call_input}),
        );
    }
    json!({"type": "result", "id": f["id"], "output": {"written": true, "echo": key,
        "backup": backup, "journal": journal, "commit": commit, "answer": answer,
        "chain": f["chain"]}})
}

/// Reconciles the open vault journals describe frame d names (see the module's description).
fn reconcile(input: &mut impl Read, output: &mut impl Write, d: &Value, n: &mut u64) -> Value {
    let mut done = Vec::new();
    for j in d["journals"].as_array().into_iter().flatten() {
        if j["intent"]["leave_open"] == true {
            continue;
        }
        let file = j["intent"]["file"].as_str().unwrap_or_default();
        let now = std::fs::read(file).unwrap_or_default();
        let outcome = if sha256_hex(&now) == j["intent"]["sha256"].as_str().unwrap_or_default() {
            "committed"
        } else {
            "rolled_back"
        };
        *n += 1;
        let reply = ask(
            input,
            output,
            json!({"type": "vault", "id": format!("v-{n}"), "for": d["id"], "op": "journal.end",
                "journal": j["id"], "outcome": outcome}),
        );
        done.push(json!({"journal": j["id"], "outcome": outcome, "reply": reply}));
    }
    json!(done)
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

/// The spawn check's agent, started as `<program> AGENT <record> [orphan]`: it starts its group
/// member (itself again, with the record's path, which sleeps in its process group) and, with `orphan`, a descendant that
/// starts a session of its own and ignores SIGTERM (as an agent's helper may), which writes its
/// process ID to `<record>.orphan`. Then it writes what it sees of itself to record (written
/// whole, then renamed into place): its process, parent, group and session IDs, its arguments,
/// working folder, environment, open descriptors and where each leads, its core file size limits
/// and its group member's process ID. Then it sleeps until it is killed. SIGTERM ends it, as the
/// default action, leaving its group member to whoever kills its group.
#[cfg(target_os = "linux")]
fn agent(args: &[String]) -> ! {
    let exe = std::env::current_exe().unwrap_or_default();
    let quiet = |c: &mut std::process::Command| {
        c.stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null());
    };
    match args.get(1).map(String::as_str) {
        Some(a) if a == format!("{AGENT}-child") => sleep_forever(),
        Some(a) if a == format!("{AGENT}-orphan") => {
            unsafe {
                libc::setsid();
                libc::signal(libc::SIGTERM, libc::SIG_IGN);
            }
            if let Some(p) = args.get(2) {
                let _ = std::fs::write(p, std::process::id().to_string());
            }
            sleep_forever();
        }
        _ => {}
    }
    let record = args.get(2).cloned().unwrap_or_default();
    let mut c = std::process::Command::new(&exe);
    // The record's path makes its command line its own, for the harness that ends it.
    c.arg(format!("{AGENT}-child")).arg(&record);
    quiet(&mut c);
    let child = c.spawn().ok().map(|c| c.id());
    if args.get(3).map(String::as_str) == Some("orphan") {
        let mut o = std::process::Command::new(&exe);
        o.arg(format!("{AGENT}-orphan"))
            .arg(format!("{record}.orphan"));
        quiet(&mut o);
        let _ = o.spawn();
    }
    let fds: Vec<Value> = fds()
        .into_iter()
        .map(|fd| {
            let target = std::fs::read_link(format!("/proc/self/fd/{fd}"))
                .map(|t| t.to_string_lossy().into_owned())
                .unwrap_or_default();
            json!({"fd": fd, "target": target})
        })
        .collect();
    let env: serde_json::Map<String, Value> = std::env::vars_os()
        .map(|(k, v)| {
            (
                k.to_string_lossy().into_owned(),
                json!(v.to_string_lossy().into_owned()),
            )
        })
        .collect();
    let mut core = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    unsafe { libc::getrlimit(libc::RLIMIT_CORE, &mut core) };
    let limit = |v: libc::rlim_t| {
        if v == libc::RLIM_INFINITY {
            json!("unlimited")
        } else {
            json!(v)
        }
    };
    let seen = json!({
        "pid": std::process::id(),
        "ppid": unsafe { libc::getppid() },
        "pgid": unsafe { libc::getpgrp() },
        "sid": unsafe { libc::getsid(0) },
        "argv": std::env::args_os().map(|a| a.to_string_lossy().into_owned()).collect::<Vec<_>>(),
        "cwd": std::env::current_dir().ok(),
        "env": env,
        "fds": fds,
        "core": [limit(core.rlim_cur), limit(core.rlim_max)],
        "child": child,
    });
    let tmp = format!("{record}.tmp");
    if std::fs::write(&tmp, seen.to_string()).is_ok() {
        let _ = std::fs::rename(&tmp, &record);
    }
    sleep_forever();
}

/// Runs the fixture: as its child when started with `CHILD`, as the spawn check's agent when
/// started with `AGENT`, else as the tool.
pub fn main() -> ! {
    if std::env::var_os(CHILD).is_some() {
        sleep_forever();
    }
    #[cfg(target_os = "linux")]
    {
        let args: Vec<String> = std::env::args().collect();
        if args.get(1).is_some_and(|a| a.starts_with(AGENT)) {
            agent(&args);
        }
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
    let mut deliveries: Vec<Value> = Vec::new();
    let mut reported = Value::Null;
    // The vault check's: journals reconciled at the last describe, frames asked, the last key.
    let mut reconciled = json!([]);
    let mut asked = 0u64;
    let mut last_key = Value::Null;
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
                reconciled = reconcile(&mut input, &mut output, &f, &mut asked);
                if let Some(mut req) = mode.describe_vault.clone() {
                    asked += 1;
                    req["type"] = json!("vault");
                    req["id"] = json!(format!("v-{asked}"));
                    req["for"] = id.clone();
                    let reply = ask(&mut input, &mut output, req);
                    if let Some(list) = reconciled.as_array_mut() {
                        list.push(json!({"describe_vault": reply}));
                    }
                }
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
                                "home": std::env::var_os("HOME")
                                    .map(|v| v.to_string_lossy().into_owned()),
                                "data_dir": std::env::var_os("PLUXY_TOOL_DATA_DIR")
                                    .map(|v| v.to_string_lossy().into_owned()),
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
                    "jobs" => {
                        asked += 1;
                        let answer = ask(
                            &mut input,
                            &mut output,
                            json!({"type": "jobs", "id": format!("q-{asked}"), "for": id,
                                "op": f["input"]["op"], "input": f["input"]["input"]}),
                        );
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output": {"answer": answer}}),
                        );
                    }
                    "take" => {
                        deliveries.push(json!({"input": f["input"], "chain": chain}));
                        let hold = f["input"]["value"]["hold"].as_u64().unwrap_or(0);
                        std::thread::sleep(Duration::from_millis(hold));
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output": {"ok": true}}),
                        );
                    }
                    "scan" => scan(&mut input, &mut output, &f),
                    "apply" => {
                        let r = apply(&mut input, &mut output, &f, &mut asked, &mut last_key);
                        write_frame(&mut output, &r);
                    }
                    "report" => write_frame(
                        &mut output,
                        &json!({"type": "result", "id": id, "output":
                            {"reconciled": reconciled, "echo": last_key, "calls": calls,
                             "version": version, "deliveries": deliveries}}),
                    ),
                    "run" => {
                        let i = &f["input"];
                        if let Some(record) = i["record"].as_str() {
                            if let Ok(mut file) = std::fs::OpenOptions::new()
                                .create(true)
                                .append(true)
                                .open(record)
                            {
                                let _ = writeln!(file, "{f}");
                            }
                        }
                        let reference = match i["steal"].as_str() {
                            Some(p) => json!(std::fs::read_to_string(p).unwrap_or_default()),
                            None => i["key"].clone(),
                        };
                        asked += 1;
                        let answer = ask(
                            &mut input,
                            &mut output,
                            json!({"type": "call", "id": format!("v-{asked}"), "for": id,
                                "package": i["consumer"]["package"], "op": i["consumer"]["op"],
                                "input": {"key": reference, "out": i["out"]}}),
                        );
                        if let Some(record) = i["record"].as_str() {
                            if let Ok(mut file) =
                                std::fs::OpenOptions::new().append(true).open(record)
                            {
                                let _ = writeln!(file, "{answer}");
                            }
                        }
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output":
                                {"answer": answer, "chain": chain}}),
                        );
                    }
                    "deliver" => match delivered(&f, "key") {
                        Some(key)
                            if f["input"]["out"]
                                .as_str()
                                .is_some_and(|out| std::fs::write(out, &key).is_ok()) =>
                        {
                            write_frame(
                                &mut output,
                                &json!({"type": "result", "id": id, "output":
                                    {"received": true, "chain": chain}}),
                            )
                        }
                        _ => write_frame(
                            &mut output,
                            &error(&id, "invalid_request", "no secret was delivered for key"),
                        ),
                    },
                    "start" => {
                        if let Some(record) = f["input"]["record"].as_str() {
                            if let Ok(mut file) = std::fs::OpenOptions::new()
                                .create(true)
                                .append(true)
                                .open(record)
                            {
                                let _ = writeln!(file, "{f}");
                            }
                        }
                        asked += 1;
                        let answer = ask(
                            &mut input,
                            &mut output,
                            json!({"type": "spawn", "id": format!("v-{asked}"), "for": id}),
                        );
                        // With `again`, a second spawn for the same call: refused.
                        let mut again = Value::Null;
                        if f["input"]["again"] == true {
                            asked += 1;
                            again = ask(
                                &mut input,
                                &mut output,
                                json!({"type": "spawn", "id": format!("v-{asked}"), "for": id}),
                            );
                        }
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output":
                                {"answer": answer, "again": again, "pid": std::process::id(),
                                 "chain": chain}}),
                        );
                    }
                    "runtime" => {
                        asked += 1;
                        // `spawn`: a spawn request for this call, which confirmed no launch.
                        let frame = if f["input"]["op"] == "spawn" {
                            json!({"type": "spawn", "id": format!("v-{asked}"), "for": id})
                        } else {
                            json!({"type": "runtime", "id": format!("v-{asked}"), "for": id,
                                "op": f["input"]["op"], "runtime": f["input"]["runtime"]})
                        };
                        let answer = ask(&mut input, &mut output, frame);
                        write_frame(
                            &mut output,
                            &json!({"type": "result", "id": id, "output":
                                {"answer": answer, "pid": std::process::id()}}),
                        );
                    }
                    "serve" => match delivered(&f, "key") {
                        Some(key) => {
                            let status = serve(f["input"]["base"].as_str().unwrap_or(""), &key);
                            if let Some(out) = f["input"]["out"].as_str() {
                                let _ = std::fs::write(out, status.to_string());
                            }
                            write_frame(
                                &mut output,
                                &json!({"type": "result", "id": id, "output":
                                    {"status": status, "pid": std::process::id(), "chain": chain}}),
                            )
                        }
                        None => write_frame(
                            &mut output,
                            &error(&id, "invalid_request", "no secret was delivered for key"),
                        ),
                    },
                    "crash" => std::process::exit(3),
                    "flood" => {
                        for i in 0..2000 {
                            write_frame(
                                &mut output,
                                &json!({"type": "call", "id": format!("f-{i}"), "for": id,
                                    "package": "pluxy/other", "op": "read", "input": {},
                                    "chain": []}),
                            );
                        }
                        sleep_forever();
                    }
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
