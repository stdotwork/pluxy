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
//! `stop` (when its package changes, is disabled or removed) and `stop_all` (when the app quits,
//! after which no call or swap starts a tool) do the same at once; `cancel_all` (when the window
//! closes) cancels every call, of every release, so the tools stop when idle. Calls waiting for a
//! tool to start or stop are bounded too, and calls waiting on a start that fails get its error.
//!
//! A tool that exits unexpectedly, or breaks its pipe, has crashed: its calls fail. A call after
//! a backoff starts it again (one second, doubling to a minute; a call during it is refused);
//! nothing restarts it on its own. Five crashes in a row quarantine it until its package changes;
//! a clean stop (idle, or one the host asked for) or a run of ten minutes starts a new count. A
//! call that reached the tool and ended without its answer has an unknown outcome: it may or may
//! not have run. It is never sent again; the host records it and reports it in the describe of
//! the package's next start, whichever release that is, on the package's turn, so the tool can
//! reconcile it before it takes work. A swap's candidate is told of none. A start that fails after
//! its describe is told again at the next one: delivery may repeat.
//!
//! A swap replaces a running release's tool by another's (`prepare`, then `switch`): the new one
//! is started and described first, with no call; only once it is ready do new calls go to it.
//! The old one drains: the calls that hold its leases finish on it (until the drain limit, ten
//! minutes, when it is stopped), then it stops. A candidate that fails leaves the old one as it
//! was. Calls to a package run one at a time, whichever release they reach: the new release's
//! first call waits for the call running on the old one, and calls still waiting their turn on
//! either release go in no set order. A swap is refused once its package was stopped (changed)
//! since the swap was staged (`stops`).
//!
//! Its stderr is logged, one bounded line at a time, at most `LOG_RATE` lines a second, with
//! control and format characters escaped and the values the tool must not show removed; the last
//! lines say why it stopped. What a tool says that reaches a caller or the status is escaped the
//! same way.
//!
//! The host authorizes nothing: the router (`route.rs`) decides each call and passes it with a
//! `Context`, whose chain each call frame names. A tool calls another package's operation by
//! sending a `call` frame on its stdout for the host call it serves (`for`); the host hands it to
//! the router (`Host::set_route`) and writes the answer to the tool's stdin. A frame naming its own
//! caller or chain is refused: the host sets them.
//!
//! A job's call (`Context::feed`, the router's `jobs.rs`) carries the job in its frame; the tool's
//! `progress` frames for it are the job's batches, handed to the feed, and the host acknowledges
//! each batch the job's consumer took with an `ack` frame. A tool asks the host's job service with
//! a `jobs` frame, for a host call it serves, which goes to the router as its calls do.
//!
//! The host-private services (`Host::set_private`, the app's vault) answer a tool's `vault` frames
//! for a host call it serves or the describe it is answering, name the package's open journals in
//! the describe of its next start (on the package's turn, with the calls of unknown outcome; a start
//! whose describe leaves one open fails), and say what must never be shown: every secret value and
//! reference is redacted from its log lines, the errors and status it causes and the outputs of its
//! calls. A call the router gives secrets to (`Context::secrets`) carries them in its frame only.

use serde::Serialize;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::{HashMap, VecDeque};
use std::io::{self, Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::process::CommandExt;
use std::path::PathBuf;
use std::process::{Child, ChildStderr, ChildStdin, ChildStdout, Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError, Sender};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, OnceLock};
use std::time::{Duration, Instant};

/// The app program contract major version this host speaks.
pub const CONTRACT: i64 = 0;
/// The largest frame a tool may send; a larger one breaks its pipe.
const MAX_FRAME: usize = 1 << 20;
/// The largest call input, as JSON: the call envelope limit.
const MAX_INPUT: usize = 64 << 10;
/// Calls per package at once, the one running and those waiting their turn.
const MAX_PENDING: usize = 8;
/// Frames waiting for a tool's stdin beyond which a refusal of its own call breaks the pipe: it
/// sends calls and does not read their answers.
const MAX_QUEUED: usize = 64;
/// The longest stderr line logged; a longer one is left out, with a note.
const MAX_LOG_LINE: usize = 8 << 10;
/// Stderr lines kept, to say why a tool stopped.
const TAIL: usize = 20;
const MAX_TICKET: usize = 64;
/// Stderr lines logged a second; the rest are left out, and counted.
const LOG_RATE: usize = 50;
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
    /// After the first crash in a row, before a call may start the tool again; it doubles with
    /// each crash in a row, up to `backoff_max`.
    pub backoff: Duration,
    pub backoff_max: Duration,
    /// Crashes in a row that quarantine the tool.
    pub quarantine_after: u32,
    /// A run this long before it crashed starts a new count of crashes in a row.
    pub stable: Duration,
    /// How long a replaced release's calls may take before it is stopped.
    pub drain: Duration,
}

impl Default for Limits {
    fn default() -> Self {
        Limits {
            idle: Duration::from_secs(30),
            startup: Duration::from_secs(10),
            stop: Duration::from_secs(5),
            cancel: Duration::from_secs(5),
            backoff: Duration::from_secs(1),
            backoff_max: Duration::from_secs(60),
            quarantine_after: 5,
            stable: Duration::from_secs(600),
            drain: Duration::from_secs(600),
        }
    }
}

/// A declared operation, as the host checks calls against it.
#[derive(Clone, Debug)]
pub struct Operation {
    id: String,
    input_schema: Value,
    output_schema: Value,
}

/// What the host starts a tool from: an admitted record's facts. Built only
/// `From<&device::Admitted>`, outside the unit tests.
#[derive(Clone, Debug)]
pub struct Spec {
    id: String,
    /// The release archive's SHA-256, which the tool echoes in hello.
    sha256: String,
    /// The release's install time: its generation.
    generation: u64,
    /// The program file, inside dir, and its expected SHA-256.
    program: PathBuf,
    program_sha256: String,
    /// The release's immutable folder: the tool's working folder.
    dir: PathBuf,
    operations: Vec<Operation>,
    /// The state version the release writes, which describe must report.
    state_version: i64,
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
        }
    }
}

/// Gives the admitted record for a package ID, checked now, or why there is none. Only the
/// device catalog makes an `Admitted`.
pub type Resolve = Box<dyn Fn(&str) -> Result<crate::device::Admitted, String> + Send + Sync>;

/// What the host starts from: `Resolve`'s record, or the unit tests' own.
type Records = Box<dyn Fn(&str) -> Result<Spec, String> + Send + Sync>;

/// Fields a tool's call frame may not have: the host names a call's caller and chain.
const IDENTITY_FIELDS: &[&str] = &["chain", "caller", "requester", "from", "origin", "context"];

/// One hop of a call's chain, as the host sets it: the original requester first (the window, or
/// a page of a package at its release), then each tool that called on, at its release and
/// generation. Call frames carry it as `chain`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(tag = "kind", rename_all = "lowercase")]
pub enum Hop {
    Main,
    Page {
        id: String,
        sha256: String,
        page: String,
    },
    Package {
        id: String,
        sha256: String,
        generation: u64,
    },
}

impl Hop {
    /// The package it names, if any.
    pub fn package(&self) -> Option<&str> {
        match self {
            Hop::Main => None,
            Hop::Page { id, .. } | Hop::Package { id, .. } => Some(id),
        }
    }
}

/// What a call carries besides its operation and input.
pub struct Context {
    /// The chain, as call frames name it.
    pub chain: Vec<Hop>,
    /// The release the call is for, as the router granted it: its archive digest and
    /// generation. A running tool of another release refuses the call.
    pub expect: Option<(String, u64)>,
    /// The router's own record of the call, opaque to the host.
    pub extra: Box<dyn std::any::Any + Send + Sync>,
    /// A job's call: where its batches go.
    pub feed: Option<Arc<dyn Feed>>,
    /// Its wait for the package's turn is bounded by the startup limit (a job's delivery): past
    /// it, the call fails, saying the package was busy.
    pub turn_bounded: bool,
    /// Secret values the call delivers to the tool, by the reference its input names them with:
    /// its frame's `secrets`, and nowhere else.
    pub secrets: Vec<(String, String)>,
}

/// Where a job's call's batches go (the router's `jobs.rs`).
pub trait Feed: Send + Sync {
    /// The job, as its call's frame names it.
    fn job(&self) -> Value;
    /// A `progress` frame the tool sent for the call: a batch.
    fn batch(&self, frame: &Value);
    /// How to acknowledge a batch to the tool, while the call runs; None once it has ended.
    fn attach(&self, ack: Option<Box<dyn Fn(u64) + Send + Sync>>);
}

impl Context {
    /// A call from the window with nothing else: what `Host::call` uses.
    #[cfg(test)]
    pub fn main() -> Context {
        Context {
            chain: vec![Hop::Main],
            expect: None,
            extra: Box::new(()),
            feed: None,
            turn_bounded: false,
            secrets: Vec::new(),
        }
    }
}

/// Flags any of which cancels a call: its ticket's, those of the calls it serves, and whether
/// those have ended.
#[derive(Clone, Default)]
pub struct Cancel(Vec<Arc<AtomicBool>>);

impl Cancel {
    /// This set and flag.
    pub fn with(&self, flag: &Arc<AtomicBool>) -> Cancel {
        let mut c = self.clone();
        c.0.push(Arc::clone(flag));
        c
    }
    pub fn is_set(&self) -> bool {
        self.0.iter().any(|f| f.load(Ordering::SeqCst))
    }
}

/// A call a tool made on its pipe, for the router: the tool's run, the context of the host call
/// it made it for, and what it asks for.
pub struct Outbound {
    /// The calling tool, at the release and generation it runs.
    pub caller: Hop,
    pub context: Arc<Context>,
    pub package: String,
    pub op: String,
    pub input: Value,
    /// Cancelled when the tool cancels it, or the call it serves is cancelled or ends.
    pub cancel: Cancel,
    /// A request to the host's job service (a `jobs` frame: op and input), not a call of package.
    pub jobs: bool,
}

/// Decides and makes a tool's call (`Host::set_route`).
pub type Route = Arc<dyn Fn(Outbound) -> Result<Value, CallError> + Send + Sync>;

/// The host-private services a tool reaches through the host (`Host::set_private`): the app's
/// vault.
pub trait Private: Send + Sync {
    /// Package id's open journals, as describe names them; an error fails the start.
    fn journals(&self, id: &str) -> Result<Vec<Value>, String>;
    /// The output of tool's `vault` frame request, sent for a host call of op it serves, or, with
    /// describing, for the describe it is answering on its package's turn, naming the IDs of the
    /// open journals it was told of.
    fn frame(
        &self,
        tool: &Hop,
        op: &str,
        describing: Option<&[String]>,
        request: &Value,
    ) -> Result<Value, CallError>;
    /// text with every value the host must never show replaced.
    fn redact(&self, text: &str) -> String;
}

/// v with p's redaction applied to each of its strings, keys included.
fn redact_value(p: &dyn Private, v: Value) -> Value {
    match v {
        Value::String(s) => Value::String(p.redact(&s)),
        Value::Array(a) => Value::Array(a.into_iter().map(|x| redact_value(p, x)).collect()),
        Value::Object(m) => Value::Object(
            m.into_iter()
                .map(|(k, x)| (p.redact(&k), redact_value(p, x)))
                .collect(),
        ),
        other => other,
    }
}

/// A call's failure: a protocol error code (`../../protocol/README.md#errors`) and a message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CallError {
    pub code: String,
    pub message: String,
    /// The call reached the tool and ended without its answer: it may or may not have run. The
    /// host does not send it again (`Unknown`).
    pub unknown: bool,
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
        unknown: false,
    }
}

/// A call whose outcome is unknown, until the tool's next start reports it in describe.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Unknown {
    /// Its call ID on the tool's pipe.
    pub call: String,
    pub op: String,
    /// The release it reached.
    pub generation: u64,
    pub why: String,
}

/// A replaced release's tool, draining.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Draining {
    pub generation: u64,
    pub pid: Option<u32>,
    pub leases: usize,
}

/// Text with control and format characters escaped (such as U+202E, which reorders what follows
/// it), as the native confirmation shows text.
fn escape(text: &str) -> String {
    crate::confirmation::escape_format_characters(text)
}

/// text as it leaves the host: redacted, then escaped.
fn clean(private: Option<&Arc<dyn Private>>, text: &str) -> String {
    match private {
        Some(p) => escape(&p.redact(text)),
        None => escape(text),
    }
}

/// A tool's state, for the window.
#[derive(Clone, Debug, Serialize, PartialEq)]
pub struct Status {
    pub id: String,
    /// idle, starting, ready (running, no lease), active, draining, stopped, crashed,
    /// quarantined or incompatible.
    pub state: &'static str,
    pub pid: Option<u32>,
    pub leases: usize,
    pub generation: Option<u64>,
    /// Why it last stopped or could not start.
    pub last: Option<String>,
    /// Its last stderr lines, as logged.
    pub log: Vec<String>,
    /// Crashes in a row.
    pub crashes: u32,
    /// Crashed: how long until a call may start it again.
    pub retry_in_ms: Option<u64>,
    /// Calls of unknown outcome its next start reports.
    pub unknown: Vec<Unknown>,
    /// Replaced releases still draining.
    pub draining: Vec<Draining>,
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
    Quarantined,
    Incompatible,
}

/// The turn of a package's calls, shared by its releases' tools: one call at a time, the one
/// running and those waiting their turn.
#[derive(Default)]
struct Gate {
    turn: Mutex<(bool, usize)>,
    turned: Condvar,
}

/// A replaced release's tool, while it drains.
struct Old {
    run: Arc<Run>,
    leases: usize,
    deadline: Instant,
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
    gate: Arc<Gate>,
    /// The release being swapped in (`prepare`), until `switch`.
    candidate: Option<Arc<Run>>,
    /// Replaced releases, draining.
    old: Vec<Old>,
    crashes: u32,
    /// Crashed: no start before then.
    retry_at: Option<Instant>,
    unknown: Vec<Unknown>,
    /// `stop` calls so far (`Host::stops`).
    stops: u64,
    /// A call is starting it: other calls wait until that start has ended, however the tool's
    /// phase changes meanwhile.
    starting: bool,
    /// Starts so far, and the last that failed with its error, for the calls that waited on it.
    starts: u64,
    failed: Option<(u64, CallError)>,
    /// Calls waiting for it to start or stop: at most `MAX_PENDING`.
    waiting: usize,
}

/// Why a start did not make the tool ready.
enum StartError {
    /// The call is not one its record allows: nothing started.
    Refused(CallError),
    /// The start failed: the calls waiting on it get this too.
    Failed(CallError),
}

/// Why a running tool is being stopped.
#[derive(Clone, Debug)]
enum End {
    Idle,
    Stopped,
    /// Another release took over; it drained, or the drain limit was reached.
    Replaced(String),
    Incompatible(String),
    Failed(String),
}

enum Reply {
    /// The whole result frame: describe's carries operations and state, a call's its output.
    Result(Value),
    Error(CallError),
}

/// A host call a tool is serving: its context, its cancel flags (with an ended flag) and its
/// operation.
type Serving = (Arc<Context>, Cancel, String);

/// One run of a tool: from spawn to reap.
struct Run {
    spec: Spec,
    pid: AtomicU64,
    /// To the writer thread; taken to close the tool's stdin.
    tx: Mutex<Option<Sender<Vec<u8>>>>,
    /// Frames sent to the writer thread and not yet written.
    queued: Arc<AtomicUsize>,
    hello: Mutex<Option<Sender<Value>>>,
    pending: Mutex<HashMap<String, Sender<Reply>>>,
    /// Its package's turn.
    gate: Arc<Gate>,
    started: Instant,
    end: Mutex<Option<End>>,
    /// Kill the group now, without the stop grace.
    kill: AtomicBool,
    /// Reaped: the pipe readers stop even if some other process holds the pipes open.
    closed: AtomicBool,
    /// Reaped, and its slot updated.
    reaped: AtomicBool,
    tail: Mutex<VecDeque<String>>,
    /// The host calls it is serving: their context and cancel flags (with an ended flag).
    serving: Mutex<HashMap<String, Serving>>,
    /// Its own calls through the host, by its ID, with their cancel flags.
    outbound: Mutex<HashMap<String, Arc<AtomicBool>>>,
    route: Option<Route>,
    private: Option<Arc<dyn Private>>,
    /// The describe call it is answering, while it does: its `vault` frames may name it.
    describing: Mutex<Option<(String, Vec<String>)>>,
    /// Its last stderr line, as logged: why it stopped, if it says.
    last_line: Mutex<Option<String>>,
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
        // Counted before the writer can take it: counted after, the writer could write it and
        // count it down first, wrapping the count past zero so a tool's next call reads as a
        // flood of unread answers.
        self.queued.fetch_add(1, Ordering::SeqCst);
        match self.tx.lock().unwrap().as_ref() {
            Some(tx) if tx.send(frame).is_ok() => Ok(()),
            _ => {
                self.queued.fetch_sub(1, Ordering::SeqCst);
                Err(fail(
                    "extension_unavailable",
                    format!("{} is stopping", self.spec.id),
                ))
            }
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
        eprintln!(
            "program {}: {}",
            self.spec.id,
            clean(self.private.as_ref(), &why)
        );
        self.finish(End::Failed(why), true);
        self.pending.lock().unwrap().clear();
        self.hello.lock().unwrap().take();
    }

    /// Keeps line in the tail and, if shown, logs it; gives it as kept.
    fn log(&self, line: &str, shown: bool) -> String {
        let line = clean(self.private.as_ref(), line);
        if shown {
            eprintln!("program {}: {line}", self.spec.id);
        }
        let mut t = self.tail.lock().unwrap();
        if t.len() == TAIL {
            t.pop_front();
        }
        t.push_back(line.clone());
        line
    }

    fn tail(&self) -> Vec<String> {
        self.tail.lock().unwrap().iter().cloned().collect()
    }
}

struct Inner {
    resolve: Records,
    limits: Limits,
    slots: Mutex<HashMap<String, Slot>>,
    changed: Condvar,
    tickets: Mutex<HashMap<String, Arc<AtomicBool>>>,
    route: OnceLock<Route>,
    private: OnceLock<Arc<dyn Private>>,
    /// Every call under way, by number, to cancel them all.
    calls: Mutex<HashMap<u64, Arc<AtomicBool>>>,
    /// The app is quitting: no tool starts and no lease is given.
    closing: AtomicBool,
}

impl Inner {
    fn slots(&self) -> MutexGuard<'_, HashMap<String, Slot>> {
        self.slots.lock().unwrap()
    }

    /// e as it leaves the host: what a tool put in it redacted and escaped.
    fn escaped(&self, e: CallError) -> CallError {
        let p = self.private.get();
        CallError {
            code: clean(p, &e.code),
            message: clean(p, &e.message),
            unknown: e.unknown,
        }
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
            } else if let Some(o) = slot.old.iter_mut().find(|o| Arc::ptr_eq(&o.run, &self.run)) {
                // A replaced release's call: its tool stops once its last one ends.
                o.leases -= 1;
            }
            self.inner.changed.notify_all();
        }
    }
}

/// A release's tool started to replace the running one (`Host::prepare`): described, with no
/// call and no lease. Dropped without `Host::switch`, it is stopped.
pub struct Prepared {
    inner: Arc<Inner>,
    run: Arc<Run>,
}

impl Drop for Prepared {
    fn drop(&mut self) {
        let mut slots = self.inner.slots();
        if let Some(slot) = slots.get_mut(&self.run.spec.id) {
            if slot
                .candidate
                .as_ref()
                .is_some_and(|r| Arc::ptr_eq(r, &self.run))
            {
                self.run.finish(End::Stopped, true);
                self.inner.changed.notify_all();
            }
        }
    }
}

/// The wait after crashes crashes in a row: limits.backoff, doubling, up to limits.backoff_max.
fn backoff(limits: &Limits, crashes: u32) -> Duration {
    let mut b = limits.backoff;
    for _ in 1..crashes {
        if b >= limits.backoff_max {
            break;
        }
        b *= 2;
    }
    b.min(limits.backoff_max)
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
    /// A host that starts each tool from the record resolve gives for it at that moment.
    pub fn new(resolve: Resolve, limits: Limits) -> Host {
        Host::with(
            Box::new(move |id| resolve(id).map(|a| Spec::from(&a))),
            limits,
        )
    }

    /// For the unit tests: records that are no device's.
    #[cfg(test)]
    fn for_tests(records: Records, limits: Limits) -> Host {
        Host::with(records, limits)
    }

    fn with(resolve: Records, limits: Limits) -> Host {
        Host(Arc::new(Inner {
            resolve,
            limits,
            slots: Mutex::new(HashMap::new()),
            changed: Condvar::new(),
            tickets: Mutex::new(HashMap::new()),
            route: OnceLock::new(),
            private: OnceLock::new(),
            calls: Mutex::new(HashMap::new()),
            closing: AtomicBool::new(false),
        }))
    }

    /// Sets the router tools' own calls go to. Without one, they are refused.
    pub fn set_route(&self, route: Route) {
        let _ = self.0.route.set(route);
    }

    /// Sets the host-private services tools' `vault` frames, describe's journals and redaction
    /// come from. Without them, `vault` frames are refused and describe names no journal.
    pub fn set_private(&self, private: Arc<dyn Private>) {
        let _ = self.0.private.set(private);
    }

    /// A lease on tool id, starting it if it is not running (once, however many ask at once).
    pub fn lease(&self, id: &str) -> Result<Lease, CallError> {
        self.lease_for(id, &|_| Ok(()), &Cancel::default())
            .map_err(|e| self.0.escaped(e))
    }

    /// `lease`, for a call check allows: check sees the record the lease would be on before any
    /// lease is taken or tool started. A cancel is seen before each start and each wait.
    fn lease_for(
        &self,
        id: &str,
        check: &dyn Fn(&Spec) -> Result<(), CallError>,
        cancel: &Cancel,
    ) -> Result<Lease, CallError> {
        let inner = &self.0;
        let mut slots = inner.slots();
        // The start this call waited on, if it waited on one.
        let mut waited_on: Option<u64> = None;
        loop {
            if inner.closing.load(Ordering::SeqCst) {
                return Err(fail("extension_unavailable", "the app is quitting"));
            }
            if cancel.is_set() {
                return Err(fail("cancelled", "the call was cancelled"));
            }
            let slot = slots.entry(id.to_string()).or_default();
            if let (Some(n), Some((m, e))) = (waited_on, &slot.failed) {
                if n == *m {
                    return Err(e.clone());
                }
            }
            if slot.starting {
                waited_on = Some(slot.starts);
            } else {
                match slot.phase {
                    // Being stopped already: wait for it to go, then start it again.
                    Phase::Ready if slot.run.as_ref().is_some_and(|r| r.ending()) => {}
                    Phase::Ready => {
                        let run = slot.run.clone().expect("a ready tool runs");
                        check(&run.spec)?;
                        slot.leases += 1;
                        slot.idle_since = None;
                        return Ok(Lease {
                            inner: Arc::clone(inner),
                            run,
                        });
                    }
                    Phase::Starting | Phase::Draining => {}
                    Phase::Crashed
                        if slot.retry_at.is_some_and(|t| t > Instant::now()) =>
                    {
                        let left = slot.retry_at.unwrap() - Instant::now();
                        return Err(fail(
                            "extension_unavailable",
                            format!(
                                "{id} crashed ({}); a call may restart it in {} ms",
                                slot.last.clone().unwrap_or_default(),
                                left.as_millis() + 1
                            ),
                        ));
                    }
                    Phase::Quarantined => {
                        return Err(fail(
                            "extension_unavailable",
                            format!(
                                "{id} is quarantined after {} crashes in a row (last: {}); disable and enable it again, or update it",
                                slot.crashes,
                                slot.last.clone().unwrap_or_default()
                            ),
                        ))
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
                        slot.starting = true;
                        slot.stop_asked = false;
                        slot.starts += 1;
                        let n = slot.starts;
                        drop(slots);
                        let started = start(inner, id, check);
                        slots = inner.slots();
                        let slot = slots.entry(id.to_string()).or_default();
                        slot.starting = false;
                        inner.changed.notify_all();
                        if let Err(e) = started {
                            // A swap made another release's tool ready meanwhile: that one
                            // serves.
                            if slot.phase == Phase::Ready
                                && slot.run.as_ref().is_some_and(|r| !r.ending())
                            {
                                continue;
                            }
                            if slot.phase == Phase::Starting {
                                slot.phase = Phase::Idle;
                            }
                            let e = match e {
                                StartError::Refused(e) => e,
                                StartError::Failed(e) => {
                                    slot.last = Some(e.message.clone());
                                    slot.failed = Some((n, e.clone()));
                                    e
                                }
                            };
                            inner.changed.notify_all();
                            return Err(e);
                        }
                        continue;
                    }
                }
            }
            // Starting or stopping: wait, with at most MAX_PENDING others.
            if slot.waiting >= MAX_PENDING {
                return Err(fail(
                    "unavailable",
                    format!("{MAX_PENDING} calls wait for {id} already"),
                ));
            }
            slot.waiting += 1;
            slots = inner.changed.wait_timeout(slots, TICK).unwrap().0;
            // Slots are never removed.
            slots.get_mut(id).expect("its slot").waiting -= 1;
        }
    }

    /// Calls op of tool id with input, from the window with nothing else. With a ticket,
    /// `cancel` can cancel it. The unit tests' own: the app's calls go through the router
    /// (`route.rs`), which decides each one and calls `run_call_with`.
    #[cfg(test)]
    pub fn call(
        &self,
        id: &str,
        op: &str,
        input: Value,
        ticket: Option<&str>,
    ) -> Result<Value, CallError> {
        let call = self.begin_call(ticket)?;
        self.run_call(call, id, op, input)
    }

    /// A call, registered: from now on `cancel` (by its ticket, if it has one) and `cancel_all`
    /// reach it. Begin it as the request arrives, before it can wait; then `run_call` it.
    pub fn begin_call(&self, ticket: Option<&str>) -> Result<Call, CallError> {
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
        let n = CALL_IDS.fetch_add(1, Ordering::SeqCst);
        self.0.calls.lock().unwrap().insert(n, Arc::clone(&cancel));
        Ok(Call {
            inner: Arc::clone(&self.0),
            n,
            ticket: ticket.map(String::from),
            cancel,
        })
    }

    /// Calls op of tool id with input, as call, from the window with nothing else. Test-only,
    /// as `call`.
    #[cfg(test)]
    pub fn run_call(
        &self,
        call: Call,
        id: &str,
        op: &str,
        input: Value,
    ) -> Result<Value, CallError> {
        self.run_call_with(
            call,
            id,
            op,
            input,
            Arc::new(Context::main()),
            Cancel::default(),
        )
    }

    /// Calls op of tool id with input, as call, in context, the router's decision; any of also
    /// cancels it too.
    pub fn run_call_with(
        &self,
        call: Call,
        id: &str,
        op: &str,
        input: Value,
        context: Arc<Context>,
        also: Cancel,
    ) -> Result<Value, CallError> {
        self.call_inner(id, op, input, &also.with(&call.cancel), context)
            .map_err(|e| self.0.escaped(e))
    }

    /// Cancels every call under way: the window that made them closed.
    pub fn cancel_all(&self) {
        for c in self.0.calls.lock().unwrap().values() {
            c.store(true, Ordering::SeqCst);
        }
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
        cancel: &Cancel,
        context: Arc<Context>,
    ) -> Result<Value, CallError> {
        if serde_json::to_vec(&input).map_or(usize::MAX, |b| b.len()) > MAX_INPUT {
            return Err(fail(
                "invalid_request",
                format!("the input is larger than {MAX_INPUT} bytes"),
            ));
        }
        // Checked against the record before the tool is leased, or started: the release the
        // router allowed the call for, if it named one, the operation and the input.
        let lease = self.lease_for(
            id,
            &|spec| {
                if let Some((sha256, generation)) = &context.expect {
                    if spec.sha256 != *sha256 || spec.generation != *generation {
                        return Err(fail(
                            "extension_unavailable",
                            format!(
                                "{id} runs another release than the one this call was allowed for"
                            ),
                        ));
                    }
                }
                schema::check(&declared(spec, op)?.input_schema, &input, "input")
                    .map_err(|e| fail("invalid_request", e))
            },
            cancel,
        )?;
        let run = &lease.run;
        let decl = declared(&run.spec, op)?;
        // Its turn: one call of the package at a time, across its releases, at most MAX_PENDING
        // at once.
        {
            let gate = &run.gate;
            let mut turn = gate.turn.lock().unwrap();
            if usize::from(turn.0) + turn.1 >= MAX_PENDING {
                return Err(fail(
                    "unavailable",
                    format!("{id} has {MAX_PENDING} calls already"),
                ));
            }
            turn.1 += 1;
            let waiting = Instant::now();
            while turn.0 {
                let (t, _) = gate.turned.wait_timeout(turn, TICK).unwrap();
                turn = t;
                if context.turn_bounded && waiting.elapsed() >= self.0.limits.startup {
                    turn.1 -= 1;
                    return Err(fail(
                        "unavailable",
                        format!(
                            "{id} stayed busy with another call for {} s",
                            self.0.limits.startup.as_secs()
                        ),
                    ));
                }
                if cancel.is_set() || run.ending() {
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
        if cancel.is_set() {
            return Err(fail("cancelled", "the call was cancelled"));
        }
        let cid = call_id();
        let (tx, rx) = mpsc::channel();
        run.pending.lock().unwrap().insert(cid.clone(), tx);
        // The calls the tool makes for this one are cancelled with it, and once it has ended.
        let ended = Arc::new(AtomicBool::new(false));
        run.serving.lock().unwrap().insert(
            cid.clone(),
            (Arc::clone(&context), cancel.with(&ended), op.to_string()),
        );
        // Once sent, a call that ends without the tool's answer has an unknown outcome.
        let unknown = |why: String, code: &str| CallError {
            code: code.into(),
            message: format!("{why}; its outcome is unknown, and it is not sent again"),
            unknown: true,
        };
        // A job's batches are acknowledged on the tool's stdin while the call runs.
        if let Some(f) = &context.feed {
            let (r, c) = (Arc::downgrade(run), cid.clone());
            f.attach(Some(Box::new(move |seq| {
                if let Some(r) = r.upgrade() {
                    let _ = r.send(&json!({"type": "ack", "id": c, "seq": seq}));
                }
            })));
        }
        let result = (|| {
            let mut frame = json!({"type": "call", "id": cid, "op": op, "input": input,
                "chain": context.chain});
            if let Some(f) = &context.feed {
                frame["job"] = f.job();
            }
            if !context.secrets.is_empty() {
                frame["secrets"] = context
                    .secrets
                    .iter()
                    .map(|(r, v)| (r.clone(), Value::String(v.clone())))
                    .collect::<serde_json::Map<_, _>>()
                    .into();
            }
            run.send(&frame)?;
            let mut cancelled: Option<Instant> = None;
            loop {
                match rx.recv_timeout(TICK) {
                    Ok(Reply::Result(frame)) => {
                        let out = frame["output"].clone();
                        // Checked as the tool sent it, then redacted: no secret goes back.
                        return schema::check(&decl.output_schema, &out, "output")
                            .map(|_| match self.0.private.get() {
                                Some(p) => redact_value(p.as_ref(), out),
                                None => out,
                            })
                            .map_err(|e| {
                                fail("extension_protocol", format!("{id} answered {op}: {e}"))
                            });
                    }
                    Ok(Reply::Error(e)) => return Err(e),
                    Err(RecvTimeoutError::Disconnected) => {
                        return Err(unknown(
                            format!("{id} stopped before it answered {op}"),
                            "extension_crashed",
                        ))
                    }
                    Err(RecvTimeoutError::Timeout) => match cancelled {
                        None if cancel.is_set() => {
                            let _ = run.send(&json!({"type": "cancel", "id": cid}));
                            cancelled = Some(Instant::now());
                        }
                        Some(at) if at.elapsed() >= self.0.limits.cancel => {
                            // It did not stop the work: stop the tool.
                            run.finish(End::Failed(format!("{id} did not answer a cancel")), true);
                            return Err(unknown(
                                format!("{id} did not answer the cancel, so it was stopped"),
                                "cancelled",
                            ));
                        }
                        _ => {}
                    },
                }
            }
        })();
        run.pending.lock().unwrap().remove(&cid);
        run.serving.lock().unwrap().remove(&cid);
        ended.store(true, Ordering::SeqCst);
        if let Some(f) = &context.feed {
            f.attach(None);
        }
        if let Err(e) = &result {
            if e.unknown {
                let mut slots = self.0.slots();
                slots
                    .entry(id.to_string())
                    .or_default()
                    .unknown
                    .push(Unknown {
                        call: cid,
                        op: op.into(),
                        generation: run.spec.generation,
                        why: e.message.clone(),
                    });
            }
        }
        result
    }

    /// Stops tool id, if it runs, without waiting: its package changed, or was disabled or
    /// removed. Its next call starts it again from the record admitted then. One that does not
    /// run stays as it is, but one incompatible or quarantined is checked again at its next call,
    /// with no backoff: a new count of crashes starts. A release being swapped in, and replaced
    /// ones draining, are stopped too. Calls of unknown outcome are still reported at its next
    /// start.
    pub fn stop(&self, id: &str) {
        let mut slots = self.0.slots();
        // Counted even for a tool that never ran: a swap staged before it is refused.
        slots.entry(id.to_string()).or_default().stops += 1;
        if let Some(slot) = slots.get_mut(id) {
            for r in slot.candidate.iter().chain(slot.old.iter().map(|o| &o.run)) {
                r.finish(End::Stopped, false);
            }
            slot.crashes = 0;
            slot.retry_at = None;
            if let Some(run) = &slot.run {
                run.finish(End::Stopped, false);
                if slot.phase == Phase::Ready {
                    slot.phase = Phase::Draining;
                }
            } else if slot.phase == Phase::Starting {
                slot.stop_asked = true;
            } else if matches!(slot.phase, Phase::Incompatible | Phase::Quarantined) {
                // Not running: its next call checks the package as it is now.
                slot.phase = Phase::Idle;
            }
            self.0.changed.notify_all();
        }
    }

    /// Stops every tool and waits until each has been reaped, for at most the stop grace and a
    /// little more: the app is quitting.
    pub fn stop_all(&self) {
        let ids: Vec<String> = {
            let slots = self.0.slots();
            self.0.closing.store(true, Ordering::SeqCst);
            self.0.changed.notify_all();
            slots.keys().cloned().collect()
        };
        for id in &ids {
            self.stop(id);
        }
        let deadline = Instant::now() + self.0.limits.stop + Duration::from_secs(2);
        let mut slots = self.0.slots();
        while slots
            .values()
            .any(|s| s.run.is_some() || s.candidate.is_some() || !s.old.is_empty())
            && Instant::now() < deadline
        {
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
                Phase::Quarantined => "quarantined",
                Phase::Incompatible => "incompatible",
            },
            pid: run.and_then(|r| r.pid()),
            leases,
            generation: run.map(|r| r.spec.generation),
            last: slot.and_then(|s| s.last.as_deref().map(|l| clean(self.0.private.get(), l))),
            log: run.map_or_else(
                || slot.map(|s| s.log.clone()).unwrap_or_default(),
                |r| r.tail(),
            ),
            crashes: slot.map_or(0, |s| s.crashes),
            retry_in_ms: slot
                .filter(|s| s.phase == Phase::Crashed)
                .and_then(|s| s.retry_at)
                .and_then(|t| t.checked_duration_since(Instant::now()))
                .map(|d| d.as_millis() as u64 + 1),
            unknown: slot
                .map(|s| {
                    s.unknown
                        .iter()
                        .map(|u| Unknown {
                            op: clean(self.0.private.get(), &u.op),
                            why: clean(self.0.private.get(), &u.why),
                            ..u.clone()
                        })
                        .collect()
                })
                .unwrap_or_default(),
            draining: slot
                .map(|s| {
                    s.old
                        .iter()
                        .map(|o| Draining {
                            generation: o.run.spec.generation,
                            pid: o.run.pid(),
                            leases: o.leases,
                        })
                        .collect()
                })
                .unwrap_or_default(),
        }
    }

    /// Starts the tool of admitted, a release the device catalog admitted, to replace the one of
    /// its package (a swap): hello and describe, with no call and no lease. The running one
    /// serves on meanwhile; a candidate that fails leaves it as it was. `switch` makes the
    /// candidate the one that serves. Once the app is quitting, no candidate starts.
    pub fn prepare(
        &self,
        admitted: &crate::device::Admitted,
        stops: u64,
    ) -> Result<Prepared, CallError> {
        self.prepare_spec(Spec::from(admitted), stops)
    }

    /// `prepare`, from a record: the unit tests' own, or an admitted one.
    fn prepare_spec(&self, spec: Spec, stops: u64) -> Result<Prepared, CallError> {
        self.prepare_inner(spec, stops)
            .map_err(|e| self.0.escaped(e))
    }

    /// How many times tool id has been stopped (`stop`): a swap staged at one count is refused
    /// by `prepare` once its package has changed since.
    pub fn stops(&self, id: &str) -> u64 {
        self.0.slots().get(id).map_or(0, |s| s.stops)
    }

    fn prepare_inner(&self, spec: Spec, stops: u64) -> Result<Prepared, CallError> {
        let inner = &self.0;
        let id = spec.id.clone();
        let unavailable = |e: String| fail("extension_unavailable", e);
        compatible(&spec).map_err(unavailable)?;
        let program = open_program(&spec).map_err(unavailable)?;
        let (run, hello_rx, cmd) = new_run(inner, spec, &program);
        {
            let mut slots = inner.slots();
            if inner.closing.load(Ordering::SeqCst) {
                return Err(unavailable("the app is quitting".into()));
            }
            let slot = slots.entry(id.clone()).or_default();
            if slot.stops != stops {
                return Err(unavailable(format!(
                    "{id} changed since the swap was staged"
                )));
            }
            if slot.candidate.is_some() {
                return Err(unavailable(format!("a swap of {id} is under way already")));
            }
            slot.candidate = Some(Arc::clone(&run));
        }
        if let Err(e) = spawn_supervisor(inner, &run, cmd, program) {
            let mut slots = inner.slots();
            if let Some(slot) = slots.get_mut(&id) {
                slot.candidate = None;
            }
            return Err(unavailable(format!("starting {id}: {e}")));
        }
        // A candidate may never serve: its describe carries no call of unknown outcome.
        match handshake(inner, &run, hello_rx, false) {
            Ok(_) if !run.ending() => Ok(Prepared {
                inner: Arc::clone(inner),
                run: Arc::clone(&run),
            }),
            Ok(_) => {
                wait_reaped(inner, &run);
                Err(unavailable(format!("{id} was stopped while it started")))
            }
            Err(e) => {
                run.finish(
                    if e.code == "incompatible" {
                        End::Incompatible(e.message.clone())
                    } else {
                        End::Failed(e.message.clone())
                    },
                    true,
                );
                wait_reaped(inner, &run);
                Err(unavailable(e.message))
            }
        }
    }

    /// Makes the prepared tool the one that serves its package: new calls and leases go to it.
    /// The one it replaces drains: the calls holding its leases finish on it, then it stops; after
    /// the drain limit it is stopped. Refused when the candidate stopped meanwhile.
    pub fn switch(&self, p: Prepared) -> Result<(), CallError> {
        let id = p.run.spec.id.clone();
        let mut slots = self.0.slots();
        let slot = slots.entry(id.clone()).or_default();
        let current = slot
            .candidate
            .as_ref()
            .is_some_and(|r| Arc::ptr_eq(r, &p.run));
        if !current || p.run.ending() {
            return Err(fail(
                "extension_unavailable",
                format!(
                    "{id} {} stopped before it could serve",
                    p.run.spec.generation
                ),
            ));
        }
        let now = Instant::now();
        if let Some(old) = slot.run.take() {
            slot.old.push(Old {
                run: old,
                leases: slot.leases,
                deadline: now + self.0.limits.drain,
            });
        }
        slot.candidate = None;
        slot.run = Some(Arc::clone(&p.run));
        slot.phase = Phase::Ready;
        slot.leases = 0;
        slot.idle_since = Some(now);
        slot.stop_asked = false;
        slot.crashes = 0;
        slot.retry_at = None;
        slot.last = None;
        self.0.changed.notify_all();
        Ok(())
    }
}

/// The operation op as spec declares it.
fn declared<'a>(spec: &'a Spec, op: &str) -> Result<&'a Operation, CallError> {
    spec.operations.iter().find(|o| o.id == op).ok_or_else(|| {
        fail(
            "unsupported",
            format!("{} declares no operation {op}", spec.id),
        )
    })
}

/// A call `Host::begin_call` registered; it leaves the host's lists when dropped.
pub struct Call {
    inner: Arc<Inner>,
    n: u64,
    ticket: Option<String>,
    cancel: Arc<AtomicBool>,
}

impl Call {
    /// also with this call's own flag: what cancels it, for work done for it before it runs
    /// (such as a wait for its native confirmation).
    pub fn cancelled_by(&self, also: &Cancel) -> Cancel {
        also.with(&self.cancel)
    }
}

impl Drop for Call {
    fn drop(&mut self) {
        self.inner.calls.lock().unwrap().remove(&self.n);
        if let Some(t) = &self.ticket {
            self.inner.tickets.lock().unwrap().remove(t);
        }
    }
}

/// The turn of the call holding it, given up when it ends.
struct Turn<'a>(&'a Run);

/// Run's package's turn, waited for at most within, or while run is not ending.
fn take_turn(run: &Run, within: Duration) -> Option<Turn<'_>> {
    let until = Instant::now() + within;
    let mut turn = run.gate.turn.lock().unwrap();
    while turn.0 {
        let left = until.saturating_duration_since(Instant::now());
        if left.is_zero() || run.ending() {
            return None;
        }
        turn = run
            .gate
            .turned
            .wait_timeout(turn, left.min(TICK))
            .unwrap()
            .0;
    }
    turn.0 = true;
    Some(Turn(run))
}

impl Drop for Turn<'_> {
    fn drop(&mut self) {
        self.0.gate.turn.lock().unwrap().0 = false;
        self.0.gate.turned.notify_all();
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
    // Its command line; its short name (`comm`) stays the descriptor's number.
    cmd.arg0(&spec.program)
        .env_clear()
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
            // The app exited while starting it. Nothing between fork and exec may allocate.
            if libc::getppid() != parent {
                return Err(io::Error::from_raw_os_error(libc::ESRCH));
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

/// A new run of spec's tool, not started yet: its hello's receiver and its command, which runs
/// the opened program file.
fn new_run(
    inner: &Inner,
    spec: Spec,
    program: &std::fs::File,
) -> (Arc<Run>, mpsc::Receiver<Value>, Command) {
    let (hello_tx, hello_rx) = mpsc::channel();
    let gate = Arc::clone(&inner.slots().entry(spec.id.clone()).or_default().gate);
    let run = Arc::new(Run {
        spec,
        pid: AtomicU64::new(0),
        tx: Mutex::new(None),
        queued: Arc::new(AtomicUsize::new(0)),
        hello: Mutex::new(Some(hello_tx)),
        pending: Mutex::new(HashMap::new()),
        gate,
        started: Instant::now(),
        end: Mutex::new(None),
        kill: AtomicBool::new(false),
        closed: AtomicBool::new(false),
        reaped: AtomicBool::new(false),
        tail: Mutex::new(VecDeque::new()),
        serving: Mutex::new(HashMap::new()),
        outbound: Mutex::new(HashMap::new()),
        route: inner.route.get().cloned(),
        private: inner.private.get().cloned(),
        describing: Mutex::new(None),
        last_line: Mutex::new(None),
    });
    let cmd = command(&run.spec, program);
    (run, hello_rx, cmd)
}

/// Starts run's supervisor thread, which spawns the tool; returns once it has been spawned.
fn spawn_supervisor(
    inner: &Arc<Inner>,
    run: &Arc<Run>,
    cmd: Command,
    program: std::fs::File,
) -> Result<(), String> {
    let (spawned_tx, spawned_rx) = mpsc::channel();
    let (i, r) = (Arc::clone(inner), Arc::clone(run));
    match std::thread::Builder::new()
        .name("pluxy-prog-sup".into())
        .spawn(move || supervise(i, r, cmd, program, spawned_tx))
    {
        Ok(_) => spawned_rx
            .recv()
            .unwrap_or_else(|_| Err("its supervisor stopped".into())),
        Err(e) => Err(e.to_string()),
    }
}

/// Starts tool id from its admitted record, if check allows the call on it, and waits until it is
/// ready, or has failed and been reaped. The slot is Starting.
fn start(
    inner: &Arc<Inner>,
    id: &str,
    check: &dyn Fn(&Spec) -> Result<(), CallError>,
) -> Result<(), StartError> {
    let failed = |e: String| StartError::Failed(fail("extension_unavailable", e));
    let spec = (inner.resolve)(id).map_err(failed)?;
    if spec.id != id {
        return Err(failed(format!("the record for {id} names {}", spec.id)));
    }
    if let Err(e) = compatible(&spec) {
        set_incompatible(inner, id, &e);
        return Err(failed(e));
    }
    check(&spec).map_err(StartError::Refused)?;
    let program = open_program(&spec).map_err(failed)?;
    let (run, hello_rx, cmd) = new_run(inner, spec, &program);
    {
        let mut slots = inner.slots();
        let slot = slots.entry(id.to_string()).or_default();
        // Its package changed since its record was read, or the app is quitting: that record
        // is not run.
        if slot.stop_asked || inner.closing.load(Ordering::SeqCst) {
            slot.stop_asked = false;
            slot.phase = Phase::Stopped;
            return Err(failed(format!("{id} was stopped while it started")));
        }
        // A swap made another release's tool the one that serves meanwhile.
        if slot.phase != Phase::Starting || slot.run.is_some() {
            return Err(failed(format!("{id} was replaced while it started")));
        }
        slot.run = Some(Arc::clone(&run));
        slot.leases = 0;
    }
    if let Err(e) = spawn_supervisor(inner, &run, cmd, program) {
        let mut slots = inner.slots();
        if let Some(slot) = slots.get_mut(id) {
            if slot.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &run)) {
                slot.run = None;
            }
        }
        return Err(failed(format!("starting {id}: {e}")));
    }
    match handshake(inner, &run, hello_rx, true) {
        Ok(reported) => {
            let mut slots = inner.slots();
            let slot = slots.entry(id.to_string()).or_default();
            if slot.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &run)) && !run.ending() {
                slot.phase = Phase::Ready;
                slot.idle_since = Some(Instant::now());
                slot.last = None;
                slot.unknown.retain(|u| !reported.contains(&u.call));
                inner.changed.notify_all();
                return Ok(());
            }
            drop(slots);
            run.finish(End::Stopped, true);
            wait_reaped(inner, &run);
            Err(failed(format!("{id} was stopped while it started")))
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
            wait_reaped(inner, &run);
            Err(failed(e.message))
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
fn wait_reaped(inner: &Inner, run: &Run) {
    let mut slots = inner.slots();
    while !run.reaped.load(Ordering::SeqCst) {
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

/// Hello, then describe, within the startup limit. With unknown, describe reports the package's
/// calls of unknown outcome, on the package's turn (no call of it runs meanwhile); if that turn
/// does not come within the startup limit, it reports none, and they wait for a later start. The
/// IDs reported are returned, for the caller to clear once this tool serves.
/// An "incompatible" error means the tool answered but does not match its record.
fn handshake(
    inner: &Inner,
    run: &Run,
    hello: mpsc::Receiver<Value>,
    unknown: bool,
) -> Result<Vec<String>, CallError> {
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
    // The tool exited, or broke its pipe, which says why.
    let exited = || match run.end.lock().unwrap().clone() {
        Some(End::Failed(why)) => fail("extension_crashed", format!("{id} {why}")),
        _ => fail("extension_crashed", format!("{id} exited as it started")),
    };
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
    let pending = |inner: &Inner| -> Vec<Unknown> {
        inner
            .slots()
            .get(id)
            .map(|s| s.unknown.clone())
            .unwrap_or_default()
    };
    // The package's open journals, which only a start that may serve is told of. Records that
    // cannot be read fail the start: it must not take work with a journal it was not told of.
    let journals = || match (&run.private, unknown) {
        (Some(p), true) => p
            .journals(id)
            .map_err(|e| fail("unavailable", format!("reading {id}'s vault journals: {e}"))),
        _ => Ok(Vec::new()),
    };
    // The describe's own time, besides any wait for the turn.
    let describe = left();
    let turn = (unknown && (!pending(inner).is_empty() || !journals()?.is_empty()))
        .then(|| take_turn(run, inner.limits.startup))
        .flatten();
    let (unknown, journals) = if turn.is_some() {
        (pending(inner), journals()?)
    } else {
        (Vec::new(), Vec::new())
    };
    let cid = call_id();
    let (tx, rx) = mpsc::channel();
    run.pending.lock().unwrap().insert(cid.clone(), tx);
    // Its vault frames in describe are taken only on its package's turn, for the journals it
    // was told of.
    if turn.is_some() {
        let told = journals
            .iter()
            .filter_map(|j| j["id"].as_str().map(String::from))
            .collect();
        *run.describing.lock().unwrap() = Some((cid.clone(), told));
    }
    let sent = run.send(&json!({"type": "call", "id": cid, "op": "describe", "contract_version": CONTRACT,
        "unknown": unknown.iter().map(|u| json!({"id": u.call, "op": u.op, "generation": u.generation})).collect::<Vec<_>>(),
        "journals": journals}));
    let reply = sent.map(|_| rx.recv_timeout(describe));
    run.describing.lock().unwrap().take();
    let d = match reply? {
        Ok(Reply::Result(d)) => d,
        Ok(Reply::Error(e)) => return Err(incompatible(format!("describe failed: {e}"))),
        Err(RecvTimeoutError::Timeout) => return Err(timeout()),
        Err(RecvTimeoutError::Disconnected) => return Err(exited()),
    };
    run.pending.lock().unwrap().remove(&cid);
    // Each open journal it was told of must be ended before it takes work.
    if let (Some(p), false) = (&run.private, journals.is_empty()) {
        let open = p
            .journals(id)
            .map_err(|e| fail("unavailable", format!("reading {id}'s vault journals: {e}")))?;
        if let Some(j) = journals
            .iter()
            .find(|j| open.iter().any(|o| o["id"] == j["id"]))
        {
            return Err(fail(
                "extension_protocol",
                format!(
                    "{id} answered describe with journal {} still open: it must reconcile it first",
                    j["id"].as_str().unwrap_or_default()
                ),
            ));
        }
    }
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
    Ok(unknown.into_iter().map(|u| u.call).collect())
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
        spawn_named("pluxy-prog-in", {
            let queued = Arc::clone(&run.queued);
            move || write_frames(stdin, rx, &queued)
        }),
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
    let [_, _, log_reader] = threads;
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
            } else if let Some(o) = slots
                .get(&run.spec.id)
                .and_then(|s| s.old.iter().find(|o| Arc::ptr_eq(&o.run, &run)))
            {
                // Replaced: it stops once its last call ends, or at the drain limit.
                if o.leases == 0 {
                    run.finish(End::Replaced("replaced; its calls ended".into()), false);
                } else if Instant::now() >= o.deadline {
                    run.finish(
                        End::Replaced("replaced; the drain limit was reached".into()),
                        true,
                    );
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
        // And itself, should it have left its group.
        let _ = child.kill();
    }
    let status = child.wait();
    run.tx.lock().unwrap().take();
    run.closed.store(true, Ordering::SeqCst);
    // Its last lines, and the count of those left out of the log, reach the status. The reader
    // stops within a tick once it is reaped.
    if let Ok(t) = log_reader {
        let _ = t.join();
    }
    run.pending.lock().unwrap().clear();
    run.hello.lock().unwrap().take();
    let end = run.end.lock().unwrap().clone();
    let status = match status {
        Ok(s) => s.to_string(),
        Err(e) => e.to_string(),
    };
    let last_line = run.last_line.lock().unwrap().clone();
    let mut slots = inner.slots();
    if let Some(slot) = slots.get_mut(&run.spec.id) {
        // A replaced or a candidate release's tool leaves the one that serves as it is.
        slot.old.retain(|o| !Arc::ptr_eq(&o.run, &run));
        if slot
            .candidate
            .as_ref()
            .is_some_and(|r| Arc::ptr_eq(r, &run))
        {
            slot.candidate = None;
        }
        if slot.run.as_ref().is_some_and(|r| Arc::ptr_eq(r, &run)) {
            slot.run = None;
            slot.leases = 0;
            slot.idle_since = None;
            slot.log = run.tail();
            let (phase, why) = match end {
                Some(End::Idle) => (Phase::Idle, format!("stopped when idle ({status})")),
                Some(End::Stopped) => (Phase::Stopped, format!("stopped ({status})")),
                Some(End::Replaced(w)) => (Phase::Stopped, format!("{w} ({status})")),
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
            // A crash: backoff before the next start, or quarantine after too many in a row. A
            // clean stop (idle, or one the host asked for) ends the crashes in a row.
            slot.phase = if phase == Phase::Crashed {
                if run.started.elapsed() >= inner.limits.stable {
                    slot.crashes = 0;
                }
                slot.crashes += 1;
                if slot.crashes >= inner.limits.quarantine_after.max(1) {
                    slot.retry_at = None;
                    Phase::Quarantined
                } else {
                    slot.retry_at = Some(Instant::now() + backoff(&inner.limits, slot.crashes));
                    Phase::Crashed
                }
            } else {
                if matches!(phase, Phase::Idle | Phase::Stopped) {
                    slot.crashes = 0;
                    slot.retry_at = None;
                }
                phase
            };
            slot.last = Some(why);
        }
    }
    run.reaped.store(true, Ordering::SeqCst);
    inner.changed.notify_all();
}

fn spawn_named(
    name: &str,
    f: impl FnOnce() + Send + 'static,
) -> io::Result<std::thread::JoinHandle<()>> {
    std::thread::Builder::new().name(name.into()).spawn(f)
}

fn write_frames(mut stdin: ChildStdin, rx: mpsc::Receiver<Vec<u8>>, queued: &AtomicUsize) {
    for frame in rx {
        let written = stdin.write_all(&frame).and_then(|_| stdin.flush());
        queued.fetch_sub(1, Ordering::SeqCst);
        if written.is_err() {
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

fn read_frames(run: &Arc<Run>, mut stdout: ChildStdout) {
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

fn deliver(run: &Arc<Run>, frame: &[u8], said_hello: &mut bool) -> Result<(), String> {
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
        // A job's call's progress is its batches; other progress goes nowhere yet, and reading it
        // keeps the pipe flowing.
        "progress" => {
            let feed = run
                .serving
                .lock()
                .unwrap()
                .get(id)
                .and_then(|s| s.0.feed.clone());
            if let Some(f) = feed {
                f.batch(&v);
            }
            return Ok(());
        }
        "call" | "jobs" => {
            outbound(run, id, &v);
            return Ok(());
        }
        "vault" => {
            vault(run, id, &v);
            return Ok(());
        }
        "cancel" => {
            if let Some(f) = run.outbound.lock().unwrap().get(id) {
                f.store(true, Ordering::SeqCst);
            }
            return Ok(());
        }
        "result" => Reply::Result(v.clone()),
        "error" => Reply::Error(fail(
            v["error"]["code"].as_str().unwrap_or("internal"),
            v["error"]["message"].as_str().unwrap_or_default(),
        )),
        _ => return Err(format!("sent a frame of unknown type {kind:?}")),
    };
    // A reply to a call that is no longer waiting, such as one cancelled, is dropped.
    if let Some(tx) = run.pending.lock().unwrap().remove(id) {
        let _ = tx.send(reply);
    }
    Ok(())
}

/// A tool's own call, frame v with ID id: refused at once when it names its caller or chain, a
/// host call it is not serving, or an ID in use; otherwise the router decides and makes it, on a
/// thread of its own, and its answer goes back on the tool's stdin. A `jobs` frame, a request to
/// the host's job service, names no package and goes the same way.
fn outbound(run: &Arc<Run>, id: &str, v: &Value) {
    let refuse = |code: &str, message: String| {
        if run.ending() {
            return;
        }
        // A tool that keeps calling and reads no answer would queue refusals without end.
        if run.queued.load(Ordering::SeqCst) >= MAX_QUEUED {
            return run.violation(format!(
                "sent calls faster than it read the answers ({MAX_QUEUED} frames waited for its stdin)"
            ));
        }
        let _ = run.send(&json!({"type": "error", "id": id,
            "error": {"code": code, "message": message}}));
    };
    if !valid_ticket(id) {
        return refuse(
            "invalid_request",
            "a call ID is 1 to 64 letters, digits, '-', '_' or '.'".into(),
        );
    }
    if let Some(f) = IDENTITY_FIELDS.iter().find(|f| v.get(**f).is_some()) {
        return refuse(
            "permission",
            format!("a call may not name its {f}: the host sets a call's caller and chain"),
        );
    }
    let jobs = v["type"] == "jobs";
    let (Some(package), Some(op), Some(serves)) = (
        v["package"].as_str().or(jobs.then_some("")),
        v["op"].as_str(),
        v["for"].as_str(),
    ) else {
        return refuse(
            "invalid_request",
            "a call names the package, op and the host call it is for".into(),
        );
    };
    let Some((context, cancel, _)) = run.serving.lock().unwrap().get(serves).cloned() else {
        return refuse(
            "permission",
            format!("{serves} is not a call {} is serving", run.spec.id),
        );
    };
    let Some(route) = run.route.clone() else {
        return refuse(
            "unsupported",
            "this host routes no calls between packages".into(),
        );
    };
    let flag = Arc::new(AtomicBool::new(false));
    {
        let mut out = run.outbound.lock().unwrap();
        if out.contains_key(id) {
            return refuse("invalid_request", format!("call ID {id} is in use"));
        }
        if out.len() >= MAX_PENDING {
            return refuse(
                "unavailable",
                format!("{} has {MAX_PENDING} calls of its own already", run.spec.id),
            );
        }
        out.insert(id.to_string(), Arc::clone(&flag));
    }
    let o = Outbound {
        caller: Hop::Package {
            id: run.spec.id.clone(),
            sha256: run.spec.sha256.clone(),
            generation: run.spec.generation,
        },
        context,
        package: package.into(),
        op: op.into(),
        input: v["input"].clone(),
        cancel: cancel.with(&flag),
        jobs,
    };
    let (r, rid) = (Arc::clone(run), id.to_string());
    let spawned = spawn_named("pluxy-prog-route", move || {
        let answer = match route(o) {
            Ok(output) => json!({"type": "result", "id": rid, "output": output}),
            Err(e) => json!({"type": "error", "id": rid,
                "error": {"code": e.code, "message": e.message}}),
        };
        r.outbound.lock().unwrap().remove(&rid);
        let _ = r.send(&answer);
    });
    if let Err(e) = spawned {
        run.outbound.lock().unwrap().remove(id);
        refuse("unavailable", format!("starting the call: {e}"));
    }
}

/// A tool's `vault` frame v with ID id, answered on its stdin from the host-private services: for
/// a host call it serves (`for`), or the describe it is answering; refused otherwise.
fn vault(run: &Arc<Run>, id: &str, v: &Value) {
    let answer = (|| {
        if !valid_ticket(id) {
            return Err(fail(
                "invalid_request",
                "a request ID is 1 to 64 letters, digits, '-', '_' or '.'",
            ));
        }
        let private = run
            .private
            .as_ref()
            .ok_or_else(|| fail("unsupported", "this host keeps no vault"))?;
        let serves = v["for"].as_str().ok_or_else(|| {
            fail(
                "invalid_request",
                "a vault request names the host call it is for",
            )
        })?;
        let told = run
            .describing
            .lock()
            .unwrap()
            .as_ref()
            .filter(|(c, _)| c == serves)
            .map(|(_, told)| told.clone());
        let op = if told.is_some() {
            String::new()
        } else {
            run.serving
                .lock()
                .unwrap()
                .get(serves)
                .map(|(_, _, op)| op.clone())
                .ok_or_else(|| {
                    fail(
                        "permission",
                        format!("{serves} is not a call {} is serving", run.spec.id),
                    )
                })?
        };
        let tool = Hop::Package {
            id: run.spec.id.clone(),
            sha256: run.spec.sha256.clone(),
            generation: run.spec.generation,
        };
        private.frame(&tool, &op, told.as_deref(), v)
    })();
    if run.ending() {
        return;
    }
    // Answered on the reader's thread: a tool that sends requests and reads no answer would
    // queue them without end.
    if run.queued.load(Ordering::SeqCst) >= MAX_QUEUED {
        return run.violation(format!(
            "sent vault requests faster than it read the answers ({MAX_QUEUED} frames waited for its stdin)"
        ));
    }
    let _ = run.send(&match answer {
        Ok(output) => json!({"type": "result", "id": id, "output": output}),
        Err(e) => json!({"type": "error", "id": id,
            "error": {"code": e.code, "message": e.message}}),
    });
}

/// Keeps a tool's lines in its tail and logs them, at most `LOG_RATE` a second; the rest are
/// left out of the log, and counted.
struct Rate<'a> {
    run: &'a Run,
    since: Instant,
    lines: usize,
    left_out: usize,
}

impl Rate<'_> {
    fn log(&mut self, line: &str) {
        if self.since.elapsed() >= Duration::from_secs(1) {
            self.flush();
            self.since = Instant::now();
            self.lines = 0;
        }
        let shown = self.lines < LOG_RATE;
        if shown {
            self.lines += 1;
        } else {
            self.left_out += 1;
        }
        let kept = self.run.log(line, shown);
        *self.run.last_line.lock().unwrap() = Some(kept);
    }

    fn flush(&mut self) {
        if self.left_out > 0 {
            self.run.log(
                &format!(
                    "({} lines left out: more than {LOG_RATE} a second)",
                    self.left_out
                ),
                true,
            );
            self.left_out = 0;
        }
    }
}

fn read_log(run: &Run, mut stderr: ChildStderr) {
    let fd = stderr.as_raw_fd();
    let mut line: Vec<u8> = Vec::new();
    let mut skipping = false;
    let mut chunk = vec![0u8; 8 << 10];
    let mut rate = Rate {
        run,
        since: Instant::now(),
        lines: 0,
        left_out: 0,
    };
    while let Some(n) = read_some(run, &mut stderr, fd, &mut chunk) {
        for &b in &chunk[..n] {
            if b == b'\n' {
                if !skipping {
                    rate.log(&String::from_utf8_lossy(&line));
                }
                line.clear();
                skipping = false;
            } else if !skipping {
                line.push(b);
                if line.len() > MAX_LOG_LINE {
                    rate.log(&format!(
                        "(a line longer than {MAX_LOG_LINE} bytes, not logged)"
                    ));
                    line.clear();
                    skipping = true;
                }
            }
        }
    }
    if !line.is_empty() && !skipping {
        rate.log(&String::from_utf8_lossy(&line));
    }
    rate.flush();
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
