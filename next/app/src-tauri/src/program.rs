//! The program host: runs the native tools this device admitted, on demand, as app program
//! contract v0 says (`../app-host/README.md`). Linux only for now.
//!
//! A tool starts at the first call or lease, from the record `device::Catalog::admitted` gives
//! at that moment (`Host::new`'s resolver), never from anything a caller passes. The program
//! file is opened, its SHA-256 checked against the record, and that opened file is what runs: a
//! file replaced after the check never runs. It runs in its package folder with a cleared
//! environment holding only the contract's three values, only its three private pipes open,
//! in a process group of its own, with a parent-death signal. A dedicated supervisor thread
//! starts it and stays until it has exited and been reaped: on Linux the parent-death signal
//! follows the thread that started the process, and the threads commands run on end when idle.
//!
//! It says hello and describes itself; a mismatch with the record (ID, digest, contract,
//! operations, state) leaves it incompatible until its package changes. Calls are checked
//! against the declared operation and its schema (`schema`), serialized, at most `MAX_PENDING`
//! at a time per tool, and can be cancelled. Each call holds a lease; when the last lease ends
//! the tool stops after the idle delay (30 seconds): its stdin closes, and after the stop grace
//! its whole process group is killed, then it is reaped, and no process or thread of it is left.
//! `stop` (when its package changes, is disabled or removed) and `stop_all` (when the app quits)
//! do the same at once. A tool that exits unexpectedly is crashed: its calls fail, and the next
//! call starts it again. Crash backoff, quarantine and swaps belong to later work.
//!
//! Its stderr is logged, one bounded line at a time with control characters escaped and the
//! values the tool must not show removed; the last lines say why it stopped.

use serde::Serialize;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::{HashMap, VecDeque};
use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::process::CommandExt;
use std::path::PathBuf;
use std::process::{Child, ChildStderr, ChildStdin, ChildStdout, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError, Sender};
use std::sync::{Arc, Condvar, Mutex, MutexGuard};
use std::time::{Duration, Instant};

/// The app program contract major version this host speaks.
pub const CONTRACT: i64 = 0;
/// The largest frame a tool may send; a larger one breaks its pipe.
const MAX_FRAME: usize = 1 << 20;
/// The largest call input, as JSON: the call envelope limit.
const MAX_INPUT: usize = 64 << 10;
/// Calls per tool at once, the one running and those waiting their turn.
const MAX_PENDING: usize = 8;
/// The longest stderr line logged; a longer one is left out, with a note.
const MAX_LOG_LINE: usize = 8 << 10;
/// Stderr lines kept, to say why a tool stopped.
const TAIL: usize = 20;
const MAX_TICKET: usize = 64;
/// How often the supervisor looks at its tool, and the pipe readers at their stop flag.
const TICK: Duration = Duration::from_millis(100);

/// The host's time limits. `Default` is the contract's.
#[derive(Clone, Copy, Debug)]
pub struct Limits {
    /// After the last lease ends, before the tool is stopped.
    pub idle: Duration,
    /// For hello and describe.
    pub startup: Duration,
    /// After stdin closes, before the tool's process group is killed.
    pub stop: Duration,
    /// After a cancel, for the call's terminal reply, before the tool is stopped.
    pub cancel: Duration,
}

impl Default for Limits {
    fn default() -> Self {
        Limits {
            idle: Duration::from_secs(30),
            startup: Duration::from_secs(10),
            stop: Duration::from_secs(5),
            cancel: Duration::from_secs(5),
        }
    }
}

/// A declared operation, as the host checks calls against it.
#[derive(Clone, Debug)]
pub struct Operation {
    pub id: String,
    pub input_schema: Value,
    pub output_schema: Value,
}

/// What the host starts a tool from: an admitted record's facts (`From<&device::Admitted>`).
#[derive(Clone, Debug)]
pub struct Spec {
    pub id: String,
    /// The release archive's SHA-256, which the tool echoes in hello.
    pub sha256: String,
    /// The release's install time: its generation.
    pub generation: u64,
    /// The program file, inside dir, and its expected SHA-256.
    pub program: PathBuf,
    pub program_sha256: String,
    /// The release's immutable folder: the tool's working folder.
    pub dir: PathBuf,
    pub operations: Vec<Operation>,
    /// The state version the release writes, which describe must report.
    pub state_version: i64,
    /// Values the tool's log lines must never show. None yet: the host gives tools no secret.
    pub secrets: Vec<String>,
}

impl From<&crate::device::Admitted> for Spec {
    fn from(a: &crate::device::Admitted) -> Self {
        Spec {
            id: a.id().into(),
            sha256: a.sha256().into(),
            generation: a.installed_at(),
            program: a.program().into(),
            program_sha256: a.program_sha256().into(),
            dir: a.dir().into(),
            operations: a
                .operations()
                .iter()
                .map(|o| Operation {
                    id: o.id.clone(),
                    input_schema: o.input_schema.clone(),
                    output_schema: o.output_schema.clone(),
                })
                .collect(),
            state_version: a.state().version,
            secrets: Vec::new(),
        }
    }
}

/// Gives the admitted record for a package ID, checked now, or why there is none.
pub type Resolve = Box<dyn Fn(&str) -> Result<Spec, String> + Send + Sync>;

/// A call's failure: a protocol error code (`../../protocol/README.md#errors`) and a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CallError {
    pub code: String,
    pub message: String,
}

impl std::fmt::Display for CallError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.code, self.message)
    }
}

fn fail(code: &str, message: impl Into<String>) -> CallError {
    CallError {
        code: code.into(),
        message: message.into(),
    }
}

/// A tool's state, for the window.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Status {
    pub id: String,
    /// idle, starting, ready (running, no lease), active, draining, stopped, crashed or
    /// incompatible.
    pub state: &'static str,
    pub pid: Option<u32>,
    pub leases: usize,
    pub generation: Option<u64>,
    /// Why it last stopped or could not start.
    pub last: Option<String>,
    /// Its last stderr lines, as logged.
    pub log: Vec<String>,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
enum Phase {
    #[default]
    Idle,
    Starting,
    Ready,
    Draining,
    Stopped,
    Crashed,
    Incompatible,
}

#[derive(Default)]
struct Slot {
    phase: Phase,
    run: Option<Arc<Run>>,
    leases: usize,
    idle_since: Option<Instant>,
    last: Option<String>,
    log: Vec<String>,
    /// `stop` came while it was starting, before it ran: the start gives up.
    stop_asked: bool,
}

/// Why a running tool is being stopped.
#[derive(Clone, Debug)]
enum End {
    Idle,
    Stopped,
    Incompatible(String),
    Failed(String),
}

enum Reply {
    /// The whole result frame: describe's carries operations and state, a call's its output.
    Result(Value),
    Error(CallError),
}

/// One run of a tool: from spawn to reap.
struct Run {
    spec: Spec,
    pid: AtomicU64,
    /// To the writer thread; taken to close the tool's stdin.
    tx: Mutex<Option<Sender<Vec<u8>>>>,
    hello: Mutex<Option<Sender<Value>>>,
    pending: Mutex<HashMap<String, Sender<Reply>>>,
    /// The call whose turn it is, and how many wait for theirs.
    turn: Mutex<(bool, usize)>,
    turned: Condvar,
    end: Mutex<Option<End>>,
    /// Kill the group now, without the stop grace.
    kill: AtomicBool,
    /// Reaped: the pipe readers stop even if some other process holds the pipes open.
    closed: AtomicBool,
    tail: Mutex<VecDeque<String>>,
}

impl Run {
    fn pid(&self) -> Option<u32> {
        match self.pid.load(Ordering::SeqCst) {
            0 => None,
            p => Some(p as u32),
        }
    }

    fn send(&self, v: &Value) -> Result<(), CallError> {
        let body = serde_json::to_vec(v).expect("a JSON value");
        let mut frame = (body.len() as u32).to_be_bytes().to_vec();
        frame.extend(body);
        match self.tx.lock().unwrap().as_ref() {
            Some(tx) if tx.send(frame).is_ok() => Ok(()),
            _ => Err(fail(
                "extension_unavailable",
                format!("{} is stopping", self.spec.id),
            )),
        }
    }

    /// Asks the supervisor to stop it, for why, unless a stop is under way already.
    fn finish(&self, why: End, kill: bool) {
        let mut end = self.end.lock().unwrap();
        if end.is_none() {
            *end = Some(why);
        }
        if kill {
            self.kill.store(true, Ordering::SeqCst);
        }
    }

    fn ending(&self) -> bool {
        self.end.lock().unwrap().is_some()
    }

    /// Breaks the pipe: a frame against the contract.
    fn violation(&self, why: String) {
        eprintln!("program {}: {why}", self.spec.id);
        self.finish(End::Failed(why), true);
        self.pending.lock().unwrap().clear();
        self.hello.lock().unwrap().take();
    }

    fn log(&self, line: &str) {
        let mut line = line.to_string();
        for s in self.spec.secrets.iter().filter(|s| !s.is_empty()) {
            line = line.replace(s.as_str(), "[redacted]");
        }
        let line: String = line
            .chars()
            .flat_map(|c| {
                let keep = !c.is_control() || c == '\t';
                let esc: Vec<char> = if keep {
                    vec![c]
                } else {
                    c.escape_default().collect()
                };
                esc
            })
            .collect();
        eprintln!("program {}: {line}", self.spec.id);
        let mut t = self.tail.lock().unwrap();
        if t.len() == TAIL {
            t.pop_front();
        }
        t.push_back(line);
    }

    fn tail(&self) -> Vec<String> {
        self.tail.lock().unwrap().iter().cloned().collect()
    }
}

struct Inner {
    resolve: Resolve,
    limits: Limits,
    slots: Mutex<HashMap<String, Slot>>,
    changed: Condvar,
    tickets: Mutex<HashMap<String, Arc<AtomicBool>>>,
}

impl Inner {
    fn slots(&self) -> MutexGuard<'_, HashMap<String, Slot>> {
        self.slots.lock().unwrap()
    }
}

/// The program host. One per app.
pub struct Host(Arc<Inner>);

/// A lease on a running tool: while one is held, it is not stopped for idleness.
pub struct Lease {
    inner: Arc<Inner>,
    run: Arc<Run>,
}

impl Drop for Lease {
    fn drop(&mut self) {
        let mut slots = self.inner.slots();
        if let Some(slot) = slots.get_mut(&self.run.spec.id) {
            if slot.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &self.run)) {
                slot.leases -= 1;
                if slot.leases == 0 {
                    slot.idle_since = Some(Instant::now());
                }
                self.inner.changed.notify_all();
            }
        }
    }
}

static CALL_IDS: AtomicU64 = AtomicU64::new(1);

fn call_id() -> String {
    format!("c-{}", CALL_IDS.fetch_add(1, Ordering::SeqCst))
}

fn valid_ticket(t: &str) -> bool {
    !t.is_empty()
        && t.len() <= MAX_TICKET
        && t.bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_' || b == b'.')
}

impl Host {
    pub fn new(resolve: Resolve, limits: Limits) -> Host {
        Host(Arc::new(Inner {
            resolve,
            limits,
            slots: Mutex::new(HashMap::new()),
            changed: Condvar::new(),
            tickets: Mutex::new(HashMap::new()),
        }))
    }

    /// A lease on tool id, starting it if it is not running (once, however many ask at once).
    pub fn lease(&self, id: &str) -> Result<Lease, CallError> {
        let inner = &self.0;
        let mut slots = inner.slots();
        loop {
            let slot = slots.entry(id.to_string()).or_default();
            match slot.phase {
                // Being stopped already: wait for it to go, then start it again.
                Phase::Ready if slot.run.as_ref().is_some_and(|r| r.ending()) => {
                    slots = inner.changed.wait_timeout(slots, TICK).unwrap().0;
                }
                Phase::Ready => {
                    let run = slot.run.clone().expect("a ready tool runs");
                    slot.leases += 1;
                    slot.idle_since = None;
                    return Ok(Lease {
                        inner: Arc::clone(inner),
                        run,
                    });
                }
                Phase::Starting | Phase::Draining => {
                    slots = inner.changed.wait(slots).unwrap();
                }
                Phase::Incompatible => {
                    return Err(fail(
                        "extension_unavailable",
                        slot.last
                            .clone()
                            .unwrap_or_else(|| format!("{id} is incompatible")),
                    ))
                }
                Phase::Idle | Phase::Stopped | Phase::Crashed => {
                    slot.phase = Phase::Starting;
                    slot.stop_asked = false;
                    drop(slots);
                    let started = start(inner, id);
                    slots = inner.slots();
                    if let Err(e) = started {
                        let slot = slots.entry(id.to_string()).or_default();
                        if slot.phase == Phase::Starting {
                            slot.phase = Phase::Idle;
                        }
                        slot.last = Some(e.message.clone());
                        inner.changed.notify_all();
                        return Err(e);
                    }
                }
            }
        }
    }

    /// Calls op of tool id with input. With a ticket, `cancel` can cancel it.
    pub fn call(
        &self,
        id: &str,
        op: &str,
        input: Value,
        ticket: Option<&str>,
    ) -> Result<Value, CallError> {
        let cancel = Arc::new(AtomicBool::new(false));
        if let Some(t) = ticket {
            if !valid_ticket(t) {
                return Err(fail(
                    "invalid_request",
                    "a ticket is 1 to 64 letters, digits, '-', '_' or '.'",
                ));
            }
            let mut tickets = self.0.tickets.lock().unwrap();
            if tickets.contains_key(t) {
                return Err(fail("invalid_request", format!("ticket {t} is in use")));
            }
            tickets.insert(t.to_string(), Arc::clone(&cancel));
        }
        let r = self.call_inner(id, op, input, &cancel);
        if let Some(t) = ticket {
            self.0.tickets.lock().unwrap().remove(t);
        }
        r
    }

    /// Cancels the call with ticket.
    pub fn cancel(&self, ticket: &str) -> Result<(), String> {
        match self.0.tickets.lock().unwrap().get(ticket) {
            Some(c) => {
                c.store(true, Ordering::SeqCst);
                Ok(())
            }
            None => Err(format!("no call has ticket {ticket}")),
        }
    }

    fn call_inner(
        &self,
        id: &str,
        op: &str,
        input: Value,
        cancel: &AtomicBool,
    ) -> Result<Value, CallError> {
        let lease = self.lease(id)?;
        let run = &lease.run;
        let decl = run
            .spec
            .operations
            .iter()
            .find(|o| o.id == op)
            .ok_or_else(|| fail("unsupported", format!("{id} declares no operation {op}")))?;
        if serde_json::to_vec(&input).map_or(usize::MAX, |b| b.len()) > MAX_INPUT {
            return Err(fail(
                "invalid_request",
                format!("the input is larger than {MAX_INPUT} bytes"),
            ));
        }
        schema::check(&decl.input_schema, &input, "input")
            .map_err(|e| fail("invalid_request", e))?;
        // Its turn: one call at a time, at most MAX_PENDING at once.
        {
            let mut turn = run.turn.lock().unwrap();
            if usize::from(turn.0) + turn.1 >= MAX_PENDING {
                return Err(fail(
                    "unavailable",
                    format!("{id} has {MAX_PENDING} calls already"),
                ));
            }
            turn.1 += 1;
            while turn.0 {
                let (t, _) = run.turned.wait_timeout(turn, TICK).unwrap();
                turn = t;
                if cancel.load(Ordering::SeqCst) || run.ending() {
                    turn.1 -= 1;
                    return Err(if run.ending() {
                        fail("extension_unavailable", format!("{id} is stopping"))
                    } else {
                        fail("cancelled", "the call was cancelled")
                    });
                }
            }
            turn.1 -= 1;
            turn.0 = true;
        }
        let _turn = Turn(run);
        if cancel.load(Ordering::SeqCst) {
            return Err(fail("cancelled", "the call was cancelled"));
        }
        let cid = call_id();
        let (tx, rx) = mpsc::channel();
        run.pending.lock().unwrap().insert(cid.clone(), tx);
        let result = (|| {
            run.send(&json!({"type": "call", "id": cid, "op": op, "input": input}))?;
            let mut cancelled: Option<Instant> = None;
            loop {
                match rx.recv_timeout(TICK) {
                    Ok(Reply::Result(frame)) => {
                        let out = frame["output"].clone();
                        return schema::check(&decl.output_schema, &out, "output")
                            .map(|_| out)
                            .map_err(|e| {
                                fail("extension_protocol", format!("{id} answered {op}: {e}"))
                            });
                    }
                    Ok(Reply::Error(e)) => return Err(e),
                    Err(RecvTimeoutError::Disconnected) => {
                        return Err(fail(
                            "extension_crashed",
                            format!("{id} stopped before it answered {op}"),
                        ))
                    }
                    Err(RecvTimeoutError::Timeout) => match cancelled {
                        None if cancel.load(Ordering::SeqCst) => {
                            let _ = run.send(&json!({"type": "cancel", "id": cid}));
                            cancelled = Some(Instant::now());
                        }
                        Some(at) if at.elapsed() >= self.0.limits.cancel => {
                            // It did not stop the work: stop the tool.
                            run.finish(End::Failed(format!("{id} did not answer a cancel")), true);
                            return Err(fail(
                                "cancelled",
                                format!("{id} did not answer the cancel, so it was stopped"),
                            ));
                        }
                        _ => {}
                    },
                }
            }
        })();
        run.pending.lock().unwrap().remove(&cid);
        result
    }

    /// Stops tool id, if it runs, without waiting: its package changed, or was disabled or
    /// removed. Its next call starts it again from the record admitted then.
    pub fn stop(&self, id: &str) {
        let mut slots = self.0.slots();
        if let Some(slot) = slots.get_mut(id) {
            if let Some(run) = &slot.run {
                run.finish(End::Stopped, false);
                if slot.phase == Phase::Ready {
                    slot.phase = Phase::Draining;
                }
            } else if slot.phase == Phase::Starting {
                slot.stop_asked = true;
            } else {
                slot.phase = Phase::Stopped;
            }
            self.0.changed.notify_all();
        }
    }

    /// Stops every tool and waits until each has been reaped, for at most the stop grace and a
    /// little more: the app is quitting.
    pub fn stop_all(&self) {
        let ids: Vec<String> = self.0.slots().keys().cloned().collect();
        for id in &ids {
            self.stop(id);
        }
        let deadline = Instant::now() + self.0.limits.stop + Duration::from_secs(2);
        let mut slots = self.0.slots();
        while slots.values().any(|s| s.run.is_some()) && Instant::now() < deadline {
            slots = self.0.changed.wait_timeout(slots, TICK).unwrap().0;
        }
    }

    pub fn status(&self, id: &str) -> Status {
        let slots = self.0.slots();
        let slot = slots.get(id);
        let phase = slot.map_or(Phase::Idle, |s| s.phase);
        let leases = slot.map_or(0, |s| s.leases);
        let run = slot.and_then(|s| s.run.as_ref());
        Status {
            id: id.into(),
            state: match phase {
                Phase::Idle => "idle",
                Phase::Starting => "starting",
                Phase::Ready if leases > 0 => "active",
                Phase::Ready => "ready",
                Phase::Draining => "draining",
                Phase::Stopped => "stopped",
                Phase::Crashed => "crashed",
                Phase::Incompatible => "incompatible",
            },
            pid: run.and_then(|r| r.pid()),
            leases,
            generation: run.map(|r| r.spec.generation),
            last: slot.and_then(|s| s.last.clone()),
            log: run.map_or_else(
                || slot.map(|s| s.log.clone()).unwrap_or_default(),
                |r| r.tail(),
            ),
        }
    }
}

/// The turn of the call holding it, given up when it ends.
struct Turn<'a>(&'a Run);

impl Drop for Turn<'_> {
    fn drop(&mut self) {
        self.0.turn.lock().unwrap().0 = false;
        self.0.turned.notify_all();
    }
}

/// Opens the program file and checks it has the expected SHA-256. The opened file is what runs.
fn open_program(spec: &Spec) -> Result<std::fs::File, String> {
    if !spec.dir.is_absolute()
        || !spec.program.is_absolute()
        || !spec.program.starts_with(&spec.dir)
    {
        return Err(format!(
            "{}: its program must be a full path inside its package folder",
            spec.id
        ));
    }
    let mut f = std::fs::File::open(&spec.program)
        .map_err(|e| format!("{}: {e}", spec.program.display()))?;
    let mut h = Sha256::new();
    let mut buf = vec![0u8; 64 << 10];
    loop {
        let n = f.read(&mut buf).map_err(|e| e.to_string())?;
        if n == 0 {
            break;
        }
        h.update(&buf[..n]);
    }
    let got: String = h.finalize().iter().map(|b| format!("{b:02x}")).collect();
    if got != spec.program_sha256 {
        return Err(format!(
            "{}: its program {} changed since it was installed",
            spec.id,
            spec.program.display()
        ));
    }
    Ok(f)
}

/// The command that runs the opened program file f, as the contract says.
fn command(spec: &Spec, f: &std::fs::File) -> Command {
    // The kernel opens /proc/self/fd/<n>, the checked file, as it executes; the descriptor is
    // close-on-exec, so the tool does not keep it.
    let mut cmd = Command::new(format!("/proc/self/fd/{}", f.as_raw_fd()));
    cmd.env_clear()
        .env("PLUXY_APP_CONTRACT_VERSIONS", CONTRACT.to_string())
        .env("PLUXY_EXTENSION_ID", &spec.id)
        .env("PLUXY_APP_ARTIFACT_SHA256", &spec.sha256)
        .current_dir(&spec.dir)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        // Its own process group, which the host stops as a whole.
        .process_group(0);
    let parent = unsafe { libc::getpid() };
    unsafe {
        cmd.pre_exec(move || {
            // If the app dies without stopping it, it gets SIGTERM rather than living on.
            if libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGTERM) != 0 {
                return Err(io::Error::last_os_error());
            }
            if libc::getppid() != parent {
                return Err(io::Error::other("the app exited while starting it"));
            }
            // Only its three pipes reach it: every other descriptor closes as it executes.
            if libc::syscall(
                libc::SYS_close_range,
                3u32,
                u32::MAX,
                libc::CLOSE_RANGE_CLOEXEC,
            ) != 0
            {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        });
    }
    cmd
}

/// Starts tool id from its admitted record and waits until it is ready, or has failed and been
/// reaped. The slot is Starting.
fn start(inner: &Arc<Inner>, id: &str) -> Result<(), CallError> {
    let spec = (inner.resolve)(id).map_err(|e| fail("extension_unavailable", e))?;
    if spec.id != id {
        return Err(fail(
            "extension_unavailable",
            format!("the record for {id} names {}", spec.id),
        ));
    }
    if let Err(e) = compatible(&spec) {
        set_incompatible(inner, id, &e);
        return Err(fail("extension_unavailable", e));
    }
    let program = open_program(&spec).map_err(|e| fail("extension_unavailable", e))?;
    let (hello_tx, hello_rx) = mpsc::channel();
    let run = Arc::new(Run {
        spec,
        pid: AtomicU64::new(0),
        tx: Mutex::new(None),
        hello: Mutex::new(Some(hello_tx)),
        pending: Mutex::new(HashMap::new()),
        turn: Mutex::new((false, 0)),
        turned: Condvar::new(),
        end: Mutex::new(None),
        kill: AtomicBool::new(false),
        closed: AtomicBool::new(false),
        tail: Mutex::new(VecDeque::new()),
    });
    let cmd = command(&run.spec, &program);
    {
        let mut slots = inner.slots();
        let slot = slots.entry(id.to_string()).or_default();
        // Its package changed since its record was read: that record is not run.
        if slot.stop_asked {
            slot.stop_asked = false;
            slot.phase = Phase::Stopped;
            return Err(fail(
                "extension_unavailable",
                format!("{id} was stopped while it started"),
            ));
        }
        slot.run = Some(Arc::clone(&run));
        slot.leases = 0;
    }
    let (spawned_tx, spawned_rx) = mpsc::channel();
    let (i, r) = (Arc::clone(inner), Arc::clone(&run));
    let supervisor = std::thread::Builder::new()
        .name("pluxy-prog-sup".into())
        .spawn(move || supervise(i, r, cmd, program, spawned_tx));
    let spawned = match supervisor {
        Ok(_) => spawned_rx
            .recv()
            .unwrap_or_else(|_| Err("its supervisor stopped".into())),
        Err(e) => Err(e.to_string()),
    };
    if let Err(e) = spawned {
        let mut slots = inner.slots();
        if let Some(slot) = slots.get_mut(id) {
            slot.run = None;
        }
        return Err(fail("extension_unavailable", format!("starting {id}: {e}")));
    }
    match handshake(inner, &run, hello_rx) {
        Ok(()) => {
            let mut slots = inner.slots();
            let slot = slots.entry(id.to_string()).or_default();
            if slot.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &run)) && !run.ending() {
                slot.phase = Phase::Ready;
                slot.idle_since = Some(Instant::now());
                slot.last = None;
                inner.changed.notify_all();
                return Ok(());
            }
            drop(slots);
            run.finish(End::Stopped, true);
            wait_reaped(inner, id, &run);
            Err(fail(
                "extension_unavailable",
                format!("{id} was stopped while it started"),
            ))
        }
        Err(e) => {
            let incompatible = e.code == "incompatible";
            run.finish(
                if incompatible {
                    End::Incompatible(e.message.clone())
                } else {
                    End::Failed(e.message.clone())
                },
                true,
            );
            wait_reaped(inner, id, &run);
            Err(fail("extension_unavailable", e.message))
        }
    }
}

fn set_incompatible(inner: &Inner, id: &str, why: &str) {
    let mut slots = inner.slots();
    let slot = slots.entry(id.to_string()).or_default();
    slot.phase = Phase::Incompatible;
    slot.last = Some(why.to_string());
    inner.changed.notify_all();
}

/// Waits until run has been reaped.
fn wait_reaped(inner: &Inner, id: &str, run: &Arc<Run>) {
    let mut slots = inner.slots();
    while slots
        .get(id)
        .and_then(|s| s.run.as_ref())
        .is_some_and(|r| Arc::ptr_eq(r, run))
    {
        slots = inner.changed.wait_timeout(slots, TICK).unwrap().0;
    }
}

/// What the record declares that this host cannot run.
fn compatible(spec: &Spec) -> Result<(), String> {
    for o in &spec.operations {
        if o.id == "describe" {
            return Err(format!(
                "{}: describe is the host's call, not an operation it may declare",
                spec.id
            ));
        }
        for (what, s) in [("input", &o.input_schema), ("output", &o.output_schema)] {
            schema::supported(s).map_err(|e| {
                format!(
                    "{}: operation {}'s {what} schema {e}, which app host contract {CONTRACT} does not support",
                    spec.id, o.id
                )
            })?;
        }
    }
    Ok(())
}

/// Hello, then describe, within the startup limit. An "incompatible" error means the tool
/// answered but does not match its record.
fn handshake(inner: &Inner, run: &Run, hello: mpsc::Receiver<Value>) -> Result<(), CallError> {
    let spec = &run.spec;
    let id = &spec.id;
    let deadline = Instant::now() + inner.limits.startup;
    let left = || deadline.saturating_duration_since(Instant::now());
    let timeout = || {
        fail(
            "extension_unavailable",
            format!(
                "{id} did not say hello and describe itself within {}s",
                inner.limits.startup.as_secs()
            ),
        )
    };
    let exited = || fail("extension_crashed", format!("{id} exited as it started"));
    let incompatible = |why: String| fail("incompatible", format!("{id} is incompatible: {why}"));
    let h = match hello.recv_timeout(left()) {
        Ok(h) => h,
        Err(RecvTimeoutError::Timeout) => return Err(timeout()),
        Err(RecvTimeoutError::Disconnected) => return Err(exited()),
    };
    if h["contract"] != "app" {
        return Err(incompatible(format!(
            "its hello names contract {}",
            h["contract"]
        )));
    }
    if !h["versions"]
        .as_array()
        .is_some_and(|v| v.iter().any(|x| *x == CONTRACT))
    {
        return Err(incompatible(format!(
            "it speaks app contract {}; this host speaks {CONTRACT}",
            h["versions"]
        )));
    }
    if h["id"] != spec.id.as_str() || h["sha256"] != spec.sha256.as_str() {
        return Err(incompatible(format!(
            "its hello names {} {}, not {} {}",
            h["id"], h["sha256"], spec.id, spec.sha256
        )));
    }
    let cid = call_id();
    let (tx, rx) = mpsc::channel();
    run.pending.lock().unwrap().insert(cid.clone(), tx);
    run.send(&json!({"type": "call", "id": cid, "op": "describe", "contract_version": CONTRACT}))?;
    let d = match rx.recv_timeout(left()) {
        Ok(Reply::Result(d)) => d,
        Ok(Reply::Error(e)) => return Err(incompatible(format!("describe failed: {e}"))),
        Err(RecvTimeoutError::Timeout) => return Err(timeout()),
        Err(RecvTimeoutError::Disconnected) => return Err(exited()),
    };
    run.pending.lock().unwrap().remove(&cid);
    let mut described: Vec<&str> = d["operations"]
        .as_array()
        .map(|a| a.iter().filter_map(|o| o["id"].as_str()).collect())
        .unwrap_or_default();
    described.sort_unstable();
    let mut declared: Vec<&str> = spec.operations.iter().map(|o| o.id.as_str()).collect();
    declared.sort_unstable();
    if described != declared {
        return Err(incompatible(format!(
            "it describes operations {described:?}; its manifest declares {declared:?}"
        )));
    }
    if d["state"]["version"] != spec.state_version {
        return Err(incompatible(format!(
            "it describes state version {}; its manifest declares {}",
            d["state"]["version"], spec.state_version
        )));
    }
    Ok(())
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Leader {
    Running,
    /// Exited, not reaped yet: until it is reaped its process group's number cannot be reused,
    /// so the group can still be killed safely.
    Exited,
    /// Not this host's child to wait for any more: reaped by someone else.
    Gone,
}

/// The tool's state, without reaping it.
fn leader(pid: u32) -> Leader {
    let mut info: libc::siginfo_t = unsafe { std::mem::zeroed() };
    let r = unsafe {
        libc::waitid(
            libc::P_PID,
            pid as libc::id_t,
            &mut info,
            libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
        )
    };
    match (r, unsafe { info.si_pid() }) {
        (0, 0) => Leader::Running,
        (0, _) => Leader::Exited,
        _ => Leader::Gone,
    }
}

/// The supervisor thread: starts the tool and stays until it has exited and been reaped.
fn supervise(
    inner: Arc<Inner>,
    run: Arc<Run>,
    mut cmd: Command,
    program: std::fs::File,
    spawned: Sender<Result<(), String>>,
) {
    let child = cmd.spawn();
    drop(program);
    let mut child: Child = match child {
        Ok(c) => c,
        Err(e) => {
            let _ = spawned.send(Err(e.to_string()));
            return;
        }
    };
    let pid = child.id();
    run.pid.store(u64::from(pid), Ordering::SeqCst);
    let (stdin, stdout, stderr) = (
        child.stdin.take().expect("piped stdin"),
        child.stdout.take().expect("piped stdout"),
        child.stderr.take().expect("piped stderr"),
    );
    let (tx, rx) = mpsc::channel::<Vec<u8>>();
    *run.tx.lock().unwrap() = Some(tx);
    let threads = [
        spawn_named("pluxy-prog-in", move || write_frames(stdin, rx)),
        spawn_named("pluxy-prog-out", {
            let r = Arc::clone(&run);
            move || read_frames(&r, stdout)
        }),
        spawn_named("pluxy-prog-err", {
            let r = Arc::clone(&run);
            move || read_log(&r, stderr)
        }),
    ];
    if let Some(e) = threads.iter().find_map(|t| t.as_ref().err()) {
        run.finish(End::Failed(format!("its pipe threads: {e}")), true);
    }
    let _ = spawned.send(Ok(()));
    let mut stopping: Option<Instant> = None;
    loop {
        if leader(pid) != Leader::Running {
            break;
        }
        // Idle: no lease for the idle delay.
        {
            let mut slots = inner.slots();
            if let Some(slot) = slots
                .get_mut(&run.spec.id)
                .filter(|s| s.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &run)))
            {
                if slot.phase == Phase::Ready
                    && slot.leases == 0
                    && slot
                        .idle_since
                        .is_some_and(|t| t.elapsed() >= inner.limits.idle)
                {
                    run.finish(End::Idle, false);
                }
                if run.ending() && slot.phase == Phase::Ready {
                    slot.phase = Phase::Draining;
                    inner.changed.notify_all();
                }
            }
        }
        if run.ending() && stopping.is_none() {
            // Its stdin closes: it stops taking work and exits.
            run.tx.lock().unwrap().take();
            stopping = Some(Instant::now());
        }
        let grace_over = stopping.is_some_and(|t| t.elapsed() >= inner.limits.stop);
        if run.kill.load(Ordering::SeqCst) || grace_over {
            break;
        }
        std::thread::sleep(TICK);
    }
    // The whole group: what it left, or all of it once the grace is over. Never once it has been
    // reaped, when its group's number could be another's.
    if leader(pid) != Leader::Gone {
        unsafe {
            libc::killpg(pid as libc::pid_t, libc::SIGKILL);
        }
    }
    let status = child.wait();
    run.tx.lock().unwrap().take();
    run.closed.store(true, Ordering::SeqCst);
    run.pending.lock().unwrap().clear();
    run.hello.lock().unwrap().take();
    let end = run.end.lock().unwrap().clone();
    let status = match status {
        Ok(s) => s.to_string(),
        Err(e) => e.to_string(),
    };
    let last_line = run.tail().last().cloned();
    let mut slots = inner.slots();
    if let Some(slot) = slots.get_mut(&run.spec.id) {
        if slot.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &run)) {
            slot.run = None;
            slot.leases = 0;
            slot.idle_since = None;
            slot.log = run.tail();
            let (phase, why) = match end {
                Some(End::Idle) => (Phase::Idle, format!("stopped when idle ({status})")),
                Some(End::Stopped) => (Phase::Stopped, format!("stopped ({status})")),
                Some(End::Incompatible(w)) => (Phase::Incompatible, w),
                Some(End::Failed(w)) => (Phase::Crashed, format!("{w} ({status})")),
                None => (
                    Phase::Crashed,
                    format!(
                        "exited ({status}){}",
                        last_line.map(|l| format!(": {l}")).unwrap_or_default()
                    ),
                ),
            };
            // A stop asked for while it was starting wins over the start's own outcome.
            slot.phase = if slot.phase == Phase::Stopped {
                Phase::Stopped
            } else {
                phase
            };
            slot.last = Some(why);
            inner.changed.notify_all();
        }
    }
}

fn spawn_named(
    name: &str,
    f: impl FnOnce() + Send + 'static,
) -> io::Result<std::thread::JoinHandle<()>> {
    std::thread::Builder::new().name(name.into()).spawn(f)
}

fn write_frames(mut stdin: ChildStdin, rx: mpsc::Receiver<Vec<u8>>) {
    for frame in rx {
        if stdin.write_all(&frame).and_then(|_| stdin.flush()).is_err() {
            break;
        }
    }
}

/// Reads from fd until it has data, the end, or run is reaped: then None.
fn read_some(run: &Run, r: &mut impl Read, fd: i32, buf: &mut [u8]) -> Option<usize> {
    loop {
        if run.closed.load(Ordering::SeqCst) {
            return None;
        }
        let mut p = libc::pollfd {
            fd,
            events: libc::POLLIN,
            revents: 0,
        };
        let n = unsafe { libc::poll(&mut p, 1, TICK.as_millis() as i32) };
        if n < 0 && io::Error::last_os_error().kind() != io::ErrorKind::Interrupted {
            return None;
        }
        if n > 0 {
            return match r.read(buf) {
                Ok(0) | Err(_) => None,
                Ok(n) => Some(n),
            };
        }
    }
}

fn read_frames(run: &Run, mut stdout: ChildStdout) {
    let fd = stdout.as_raw_fd();
    let mut buf = Vec::new();
    let mut chunk = vec![0u8; 64 << 10];
    let mut said_hello = false;
    'read: while let Some(n) = read_some(run, &mut stdout, fd, &mut chunk) {
        buf.extend_from_slice(&chunk[..n]);
        while buf.len() >= 4 {
            let len = u32::from_be_bytes([buf[0], buf[1], buf[2], buf[3]]) as usize;
            if len > MAX_FRAME {
                run.violation(format!(
                    "sent a frame of {len} bytes; the limit is {MAX_FRAME}"
                ));
                break 'read;
            }
            if buf.len() < 4 + len {
                break;
            }
            let frame: Vec<u8> = buf.drain(..4 + len).skip(4).collect();
            if let Err(why) = deliver(run, &frame, &mut said_hello) {
                run.violation(why);
                break 'read;
            }
        }
    }
    // Its stdout ended: every call still waiting fails.
    run.pending.lock().unwrap().clear();
    run.hello.lock().unwrap().take();
}

fn deliver(run: &Run, frame: &[u8], said_hello: &mut bool) -> Result<(), String> {
    let v: Value = serde_json::from_slice(frame).map_err(|e| format!("sent a bad frame: {e}"))?;
    let kind = v["type"].as_str().unwrap_or_default().to_string();
    if !*said_hello {
        if kind != "hello" {
            return Err(format!("sent {kind:?} before hello"));
        }
        *said_hello = true;
        if let Some(h) = run.hello.lock().unwrap().take() {
            let _ = h.send(v);
        }
        return Ok(());
    }
    let id = v["id"]
        .as_str()
        .ok_or_else(|| format!("sent a {kind:?} frame with no call ID"))?;
    let reply = match kind.as_str() {
        // Progress goes nowhere yet; reading it keeps the pipe flowing.
        "progress" => return Ok(()),
        "result" => Reply::Result(v.clone()),
        "error" => Reply::Error(CallError {
            code: v["error"]["code"].as_str().unwrap_or("internal").into(),
            message: v["error"]["message"].as_str().unwrap_or_default().into(),
        }),
        _ => return Err(format!("sent a frame of unknown type {kind:?}")),
    };
    // A reply to a call that is no longer waiting, such as one cancelled, is dropped.
    if let Some(tx) = run.pending.lock().unwrap().remove(id) {
        let _ = tx.send(reply);
    }
    Ok(())
}

fn read_log(run: &Run, mut stderr: ChildStderr) {
    let fd = stderr.as_raw_fd();
    let mut line: Vec<u8> = Vec::new();
    let mut skipping = false;
    let mut chunk = vec![0u8; 8 << 10];
    while let Some(n) = read_some(run, &mut stderr, fd, &mut chunk) {
        for &b in &chunk[..n] {
            if b == b'\n' {
                if !skipping {
                    run.log(&String::from_utf8_lossy(&line));
                }
                line.clear();
                skipping = false;
            } else if !skipping {
                line.push(b);
                if line.len() > MAX_LOG_LINE {
                    run.log(&format!(
                        "(a line longer than {MAX_LOG_LINE} bytes, not logged)"
                    ));
                    line.clear();
                    skipping = true;
                }
            }
        }
    }
    if !line.is_empty() && !skipping {
        run.log(&String::from_utf8_lossy(&line));
    }
}

/// The part of JSON Schema the host checks calls and replies against. A schema using any other
/// keyword makes its tool incompatible rather than half checked.
pub mod schema {
    use serde_json::Value;

    const KEYWORDS: &[&str] = &[
        "type",
        "properties",
        "required",
        "additionalProperties",
        "items",
        "enum",
        "const",
        "minimum",
        "maximum",
        "minLength",
        "maxLength",
        "minItems",
        "maxItems",
        "title",
        "description",
        "default",
        "examples",
        "$comment",
    ];
    const TYPES: &[&str] = &[
        "object", "array", "string", "integer", "number", "boolean", "null",
    ];
    const MAX_DEPTH: usize = 32;

    /// Whether s uses only the supported keywords; if not, which it uses.
    pub fn supported(s: &Value) -> Result<(), String> {
        supported_at(s, 0)
    }

    fn supported_at(s: &Value, depth: usize) -> Result<(), String> {
        if depth > MAX_DEPTH {
            return Err(format!("nests deeper than {MAX_DEPTH}"));
        }
        let o = s.as_object().ok_or("is not a JSON object")?;
        for (k, v) in o {
            if !KEYWORDS.contains(&k.as_str()) {
                return Err(format!("uses {k}"));
            }
            match k.as_str() {
                "type" => {
                    let names: Vec<&Value> = match v {
                        Value::Array(a) => a.iter().collect(),
                        v => vec![v],
                    };
                    if names
                        .iter()
                        .any(|n| !n.as_str().is_some_and(|n| TYPES.contains(&n)))
                    {
                        return Err(format!("has type {v}"));
                    }
                }
                "properties" => {
                    for p in v
                        .as_object()
                        .ok_or("has properties that are not an object")?
                        .values()
                    {
                        supported_at(p, depth + 1)?;
                    }
                }
                "items" => supported_at(v, depth + 1)?,
                "additionalProperties" if !v.is_boolean() => supported_at(v, depth + 1)?,
                "required" if !v.as_array().is_some_and(|a| a.iter().all(Value::is_string)) => {
                    return Err("has required that is not a list of names".into())
                }
                "enum" if !v.is_array() => return Err("has enum that is not a list".into()),
                "minimum" | "maximum" if !v.is_number() => {
                    return Err(format!("has {k} that is not a number"))
                }
                "minLength" | "maxLength" | "minItems" | "maxItems" if !v.is_u64() => {
                    return Err(format!("has {k} that is not a count"))
                }
                _ => {}
            }
        }
        Ok(())
    }

    fn is_type(v: &Value, t: &str) -> bool {
        match t {
            "object" => v.is_object(),
            "array" => v.is_array(),
            "string" => v.is_string(),
            "integer" => v.is_i64() || v.is_u64() || v.as_f64().is_some_and(|f| f.fract() == 0.0),
            "number" => v.is_number(),
            "boolean" => v.is_boolean(),
            "null" => v.is_null(),
            _ => false,
        }
    }

    /// Checks v against s, a supported schema; at says where v is, for the message.
    pub fn check(s: &Value, v: &Value, at: &str) -> Result<(), String> {
        supported(s)?;
        check_at(s, v, at)
    }

    fn check_at(s: &Value, v: &Value, at: &str) -> Result<(), String> {
        let bad = |why: String| Err(format!("{at} {why}"));
        if let Some(t) = s.get("type") {
            let ok = match t {
                Value::Array(a) => a.iter().any(|t| is_type(v, t.as_str().unwrap_or(""))),
                t => is_type(v, t.as_str().unwrap_or("")),
            };
            if !ok {
                return bad(format!("is not of type {t}"));
            }
        }
        if let Some(e) = s.get("enum").and_then(Value::as_array) {
            if !e.contains(v) {
                return bad("is not one of the allowed values".into());
            }
        }
        if let Some(c) = s.get("const") {
            if c != v {
                return bad(format!("is not {c}"));
            }
        }
        let count = |k: &str| s.get(k).and_then(Value::as_u64);
        if let Some(n) = v.as_f64() {
            if s.get("minimum")
                .and_then(Value::as_f64)
                .is_some_and(|m| n < m)
            {
                return bad(format!("is below {}", s["minimum"]));
            }
            if s.get("maximum")
                .and_then(Value::as_f64)
                .is_some_and(|m| n > m)
            {
                return bad(format!("is above {}", s["maximum"]));
            }
        }
        if let Some(text) = v.as_str() {
            let n = text.chars().count() as u64;
            if count("minLength").is_some_and(|m| n < m)
                || count("maxLength").is_some_and(|m| n > m)
            {
                return bad("has a length out of bounds".into());
            }
        }
        if let Some(a) = v.as_array() {
            let n = a.len() as u64;
            if count("minItems").is_some_and(|m| n < m) || count("maxItems").is_some_and(|m| n > m)
            {
                return bad("has a number of items out of bounds".into());
            }
            if let Some(items) = s.get("items") {
                for (i, x) in a.iter().enumerate() {
                    check_at(items, x, &format!("{at}[{i}]"))?;
                }
            }
        }
        if let Some(o) = v.as_object() {
            for r in s
                .get("required")
                .and_then(Value::as_array)
                .into_iter()
                .flatten()
            {
                if let Some(r) = r.as_str() {
                    if !o.contains_key(r) {
                        return bad(format!("has no {r}"));
                    }
                }
            }
            let props = s.get("properties").and_then(Value::as_object);
            for (k, x) in o {
                let at = format!("{at}.{k}");
                match (props.and_then(|p| p.get(k)), s.get("additionalProperties")) {
                    (Some(p), _) => check_at(p, x, &at)?,
                    (None, Some(Value::Bool(false))) => return Err(format!("{at} is not allowed")),
                    (None, Some(extra)) if extra.is_object() => check_at(extra, x, &at)?,
                    _ => {}
                }
            }
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "program_tests.rs"]
mod tests;
