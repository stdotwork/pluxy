//! The program side of pluxy's app program contract v0 (`next/app/app-host/README.md`, "App
//! program contract v0"): framing, hello, describe (with the calls of unknown outcome and the open
//! journals it names), calls with progress and cancellation, a program's own calls of other
//! packages' operations, its vault frames, and jobs (`next/app/app-host/README.md`, "Jobs"): a
//! consumer's `jobs` frames ([`Call::jobs`]) and a producer's batches within the job's window
//! ([`Call::batch`]), counted off by the host's `ack` frames.
//!
//! A program implements [`Handler`] and hands it to [`run`], which checks the host's environment,
//! says hello, answers describe from the package's own manifest and serves calls until its stdin
//! ends:
//!
//! ```no_run
//! use pluxy_program::{run, Call, Error, Handler};
//! use serde_json::{json, Value};
//!
//! struct Tool;
//! impl Handler for Tool {
//!     fn call(&self, call: &Call) -> Result<Value, Error> {
//!         match call.op.as_str() {
//!             "count" => Ok(json!({"count": call.input["items"].as_array().map_or(0, Vec::len)})),
//!             op => Err(Error::unsupported(format!("no operation {op}"))),
//!         }
//!     }
//! }
//!
//! fn main() -> std::io::Result<()> {
//!     run(Tool)
//! }
//! ```
//!
//! [`run`] reads frames on the thread that calls it. Each call, describe included, runs on a
//! thread of its own that ends with the call, so no thread of the program is left once its calls
//! end. The reading thread routes cancels, and the answers to a call's own calls and vault frames,
//! to the call they belong to, and a job's acks to its producer's call. When stdin ends it cancels every call, fails what they wait for,
//! waits for them and returns: the program then exits.

use serde_json::{json, Map, Value};
use std::collections::HashMap;
use std::io::{self, Read, Write};
use std::path::Path;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Condvar, Mutex};
use std::thread::JoinHandle;
use std::time::Duration;

/// The contract major version this library speaks.
pub const CONTRACT: i64 = 0;
/// The largest frame a program may send; the host breaks the pipe of one that sends more.
pub const MAX_FRAME: usize = 1 << 20;
/// The largest frame the framing allows, which the host's frames stay under.
pub const MAX_INBOUND: usize = 64 << 20;
/// The largest batch value a producer may send, as JSON; the host fails a job sent a larger one.
pub const MAX_BATCH: usize = 48 << 10;

fn invalid(message: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message.into())
}

/// Reads one frame: a four-byte big-endian length and that many bytes of UTF-8 JSON. None at the
/// end of the input between frames; an error for input that ends inside one or breaks the rules.
pub fn read_frame(r: &mut impl Read) -> io::Result<Option<Value>> {
    let mut len = [0u8; 4];
    let mut got = 0;
    while got < len.len() {
        match r.read(&mut len[got..]) {
            Ok(0) if got == 0 => return Ok(None),
            Ok(0) => return Err(invalid("the input ended inside a frame's length")),
            Ok(n) => got += n,
            Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
            Err(e) => return Err(e),
        }
    }
    let n = u32::from_be_bytes(len) as usize;
    if n > MAX_INBOUND {
        return Err(invalid(format!(
            "a frame of {n} bytes is larger than 64 MiB"
        )));
    }
    let mut buf = vec![0; n];
    r.read_exact(&mut buf)?;
    serde_json::from_slice(&buf)
        .map(Some)
        .map_err(|e| invalid(format!("a frame is not JSON: {e}")))
}

/// Writes v as one frame, refused (and nothing written) when it is larger than max bytes.
pub fn write_frame(w: &mut impl Write, v: &Value, max: usize) -> io::Result<()> {
    let bytes = serde_json::to_vec(v).map_err(|e| invalid(e.to_string()))?;
    if bytes.len() > max {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("a frame of {} bytes is larger than {max}", bytes.len()),
        ));
    }
    let mut frame = Vec::with_capacity(4 + bytes.len());
    frame.extend_from_slice(&(bytes.len() as u32).to_be_bytes());
    frame.extend_from_slice(&bytes);
    w.write_all(&frame)?;
    w.flush()
}

/// An error a call ends with, as the contract's error object: a code and a message safe to show.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Error {
    pub code: String,
    pub message: String,
}

impl Error {
    pub fn new(code: &str, message: impl Into<String>) -> Error {
        Error {
            code: code.into(),
            message: message.into(),
        }
    }
    /// The input is wrong.
    pub fn invalid_request(message: impl Into<String>) -> Error {
        Error::new("invalid_request", message)
    }
    /// What was asked is not supported.
    pub fn unsupported(message: impl Into<String>) -> Error {
        Error::new("unsupported", message)
    }
    /// What was asked for does not exist.
    pub fn not_found(message: impl Into<String>) -> Error {
        Error::new("not_found", message)
    }
    /// It cannot be reached or read for now.
    pub fn unavailable(message: impl Into<String>) -> Error {
        Error::new("unavailable", message)
    }
    /// A bug or an unexpected state in the program.
    pub fn internal(message: impl Into<String>) -> Error {
        Error::new("internal", message)
    }
    /// The call was cancelled.
    pub fn cancelled() -> Error {
        Error::new("cancelled", "the call was cancelled")
    }

    fn to_json(&self) -> Value {
        json!({"code": self.code, "message": self.message})
    }

    fn from_json(v: &Value) -> Error {
        Error::new(
            v["code"].as_str().unwrap_or("internal"),
            v["message"].as_str().unwrap_or_default(),
        )
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.code, self.message)
    }
}

impl std::error::Error for Error {}

/// Who the host started: the package ID and the release's archive digest, which hello echoes.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Identity {
    pub id: String,
    pub sha256: String,
}

impl Identity {
    /// From the host's environment: `PLUXY_APP_CONTRACT_VERSIONS` must list this contract,
    /// `PLUXY_EXTENSION_ID` and `PLUXY_APP_ARTIFACT_SHA256` name the release.
    pub fn from_env() -> Result<Identity, String> {
        let var = |name: &str| {
            std::env::var(name)
                .ok()
                .filter(|v| !v.is_empty())
                .ok_or_else(|| format!("{name} is not set: start this program from pluxy's app"))
        };
        let versions = var("PLUXY_APP_CONTRACT_VERSIONS")?;
        if !versions
            .split(',')
            .any(|v| v.trim().parse::<i64>() == Ok(CONTRACT))
        {
            return Err(format!(
                "the app speaks contract versions {versions}, this program {CONTRACT}"
            ));
        }
        Ok(Identity {
            id: var("PLUXY_EXTENSION_ID")?,
            sha256: var("PLUXY_APP_ARTIFACT_SHA256")?,
        })
    }
}

/// What describe answers: each declared operation (its `id` and `description`) and the state
/// versions, as the package's manifest declares them.
#[derive(Clone, Debug, PartialEq)]
pub struct Description {
    pub operations: Vec<Value>,
    pub state: Value,
}

impl Description {
    /// From a manifest's bytes (`pluxy-extension.json`): its `app.operations` and `app.state`.
    pub fn from_manifest(bytes: &[u8]) -> Result<Description, String> {
        let m: Value =
            serde_json::from_slice(bytes).map_err(|e| format!("the manifest is not JSON: {e}"))?;
        let operations = m["app"]["operations"]
            .as_array()
            .ok_or("the manifest declares no app operations")?
            .iter()
            .map(|o| json!({"id": o["id"], "description": o["description"]}))
            .collect();
        let state = m["app"]["state"].clone();
        if !state.is_object() {
            return Err("the manifest declares no app state".into());
        }
        Ok(Description { operations, state })
    }

    /// From the package's own manifest: the host runs a program in its package folder.
    pub fn from_package() -> Result<Description, String> {
        let path = Path::new("pluxy-extension.json");
        let bytes = std::fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
        Description::from_manifest(&bytes)
    }
}

/// A program's operations.
pub trait Handler: Send + Sync + 'static {
    /// Answers one call of a declared operation: its output, or the error it ends with. A long
    /// call looks at [`Call::cancelled`] now and then and ends with [`Error::cancelled`] once set.
    fn call(&self, call: &Call) -> Result<Value, Error>;

    /// Before describe is answered at a start: reconciles the calls of unknown outcome the host
    /// names (`{"id", "op", "generation"}`) and the package's open journals (ending each with
    /// [`Call::vault`]). Delivery may repeat, so this must be idempotent. By default, nothing.
    fn reconcile(
        &self,
        _call: &Call,
        _unknown: &[Value],
        _journals: &[Value],
    ) -> Result<(), Error> {
        Ok(())
    }
}

/// What every call of a run shares: stdout, and the calls' own calls waiting for their answers.
struct Shared {
    out: Mutex<Box<dyn Write + Send>>,
    waiting: Mutex<HashMap<String, mpsc::Sender<Result<Value, Error>>>>,
    next: AtomicU64,
}

impl Shared {
    fn send(&self, v: &Value) -> io::Result<()> {
        write_frame(&mut *self.out.lock().unwrap(), v, MAX_FRAME)
    }
}

/// The job a producer's call runs, as the host names it in the call frame.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Job {
    /// The host's ID for the job, `j-<n>`.
    pub id: String,
    /// The kind of data it produces, `<consumer ID>/<stream>`.
    pub kind: String,
    /// How many batches may wait unacknowledged.
    pub window: u64,
}

impl Job {
    fn from_frame(v: &Value) -> Option<Job> {
        Some(Job {
            id: v["id"].as_str()?.to_string(),
            kind: v["kind"].as_str()?.to_string(),
            window: v["window"].as_u64()?,
        })
    }
}

/// The batches of a job's call the host has acknowledged, the highest number so far.
#[derive(Default)]
struct Acks {
    acked: Mutex<u64>,
    changed: Condvar,
}

impl Acks {
    fn ack(&self, seq: u64) {
        let mut a = self.acked.lock().unwrap();
        if seq > *a {
            *a = seq;
        }
        self.changed.notify_all();
    }
}

/// One call the host made: its operation, input, chain and secrets, and what it may do while it
/// runs.
pub struct Call {
    /// The host's ID for the call.
    pub id: String,
    /// The operation, `describe` for the host's describe.
    pub op: String,
    pub input: Value,
    /// Who asked, as the host names it: the requester, then each tool that called on.
    pub chain: Vec<Value>,
    /// The secrets the host gave this call, by the reference its secret fields hold.
    pub secrets: Map<String, Value>,
    /// The job, when the host called a producer's operation to run one.
    pub job: Option<Job>,
    cancelled: Arc<AtomicBool>,
    shared: Arc<Shared>,
    acks: Arc<Acks>,
    sent: AtomicU64,
}

impl Call {
    /// Whether the host cancelled the call (or its input ended).
    pub fn cancelled(&self) -> bool {
        self.cancelled.load(Ordering::SeqCst)
    }

    /// [`Error::cancelled`] once the call was cancelled.
    pub fn check(&self) -> Result<(), Error> {
        if self.cancelled() {
            Err(Error::cancelled())
        } else {
            Ok(())
        }
    }

    /// Reports progress (a bounded JSON value) for the call.
    pub fn progress(&self, value: Value) -> io::Result<()> {
        self.shared
            .send(&json!({"type": "progress", "id": self.id, "value": value}))
    }

    /// Calls op of package with input, for this call, through the host (the package must import
    /// it): the operation's output, or its error. Once this call is cancelled, that call is
    /// cancelled too, and its answer still waited for.
    pub fn call(&self, package: &str, op: &str, input: Value) -> Result<Value, Error> {
        self.check()?;
        let id = format!("o-{}", self.shared.next.fetch_add(1, Ordering::SeqCst));
        let frame = json!({"type": "call", "id": id, "for": self.id, "package": package, "op": op, "input": input});
        self.ask(id, frame, true)
    }

    /// Asks the host's job service for this call (`registrations`, `start` or `cancel`, with its
    /// input): the answer's output, or its error. `start` is answered at once with the job's ID;
    /// its batches arrive later as host calls of the consumer's deliver operation, which wait
    /// for this call to end, so a call never waits for its own job.
    pub fn jobs(&self, op: &str, input: Value) -> Result<Value, Error> {
        self.check()?;
        let id = format!("q-{}", self.shared.next.fetch_add(1, Ordering::SeqCst));
        let frame = json!({"type": "jobs", "id": id, "for": self.id, "op": op, "input": input});
        self.ask(id, frame, false)
    }

    /// Sends value as the next batch of this call's job, once fewer than the job's window of
    /// batches wait unacknowledged: its number (from 1). Fails with `invalid_request` when the
    /// call runs no job or the value is larger than [`MAX_BATCH`] (nothing is sent), with
    /// `cancelled` once the call is cancelled while it waits, and with `unavailable` when the
    /// host cannot be written to.
    pub fn batch(&self, value: Value) -> Result<u64, Error> {
        let job = self
            .job
            .as_ref()
            .ok_or_else(|| Error::invalid_request("this call runs no job"))?;
        let size = serde_json::to_vec(&value).map_or(usize::MAX, |b| b.len());
        if size > MAX_BATCH {
            return Err(Error::invalid_request(format!(
                "a batch of {size} bytes is larger than {MAX_BATCH}"
            )));
        }
        let seq = self.sent.load(Ordering::SeqCst) + 1;
        let mut acked = self.acks.acked.lock().unwrap();
        // The batches sent and not acknowledged must leave room for this one.
        while (seq - 1).saturating_sub(*acked) >= job.window {
            self.check()?;
            acked = self
                .acks
                .changed
                .wait_timeout(acked, Duration::from_millis(100))
                .unwrap()
                .0;
        }
        drop(acked);
        self.check()?;
        self.shared
            .send(&json!({"type": "progress", "id": self.id, "value": value}))
            .map_err(|e| Error::unavailable(format!("could not send a batch: {e}")))?;
        self.sent.store(seq, Ordering::SeqCst);
        Ok(seq)
    }

    /// How many batches of this call's job the host has acknowledged.
    pub fn acked(&self) -> u64 {
        *self.acks.acked.lock().unwrap()
    }

    /// Sends a vault frame (`backup.put`, `journal.begin`, ...) with fields for this call: its
    /// output, or its error.
    pub fn vault(&self, op: &str, fields: Map<String, Value>) -> Result<Value, Error> {
        let id = format!("v-{}", self.shared.next.fetch_add(1, Ordering::SeqCst));
        let mut frame = fields;
        frame.insert("type".into(), json!("vault"));
        frame.insert("id".into(), json!(id));
        frame.insert("for".into(), json!(self.id));
        frame.insert("op".into(), json!(op));
        self.ask(id, Value::Object(frame), false)
    }

    fn ask(&self, id: String, frame: Value, cancellable: bool) -> Result<Value, Error> {
        let (tx, rx) = mpsc::channel();
        self.shared.waiting.lock().unwrap().insert(id.clone(), tx);
        if let Err(e) = self.shared.send(&frame) {
            self.shared.waiting.lock().unwrap().remove(&id);
            return Err(Error::unavailable(format!("could not ask the host: {e}")));
        }
        let mut told = false;
        loop {
            match rx.recv_timeout(Duration::from_millis(100)) {
                Ok(answer) => return answer,
                Err(mpsc::RecvTimeoutError::Disconnected) => {
                    return Err(Error::unavailable("the host went away"))
                }
                Err(mpsc::RecvTimeoutError::Timeout) => {
                    if cancellable && !told && self.cancelled() {
                        told = true;
                        let _ = self.shared.send(&json!({"type": "cancel", "id": id}));
                    }
                }
            }
        }
    }
}

/// Runs handler as the program the host started: checks the environment, says hello, answers
/// describe from `pluxy-extension.json` in the working folder and serves calls on stdin and
/// stdout until stdin ends.
pub fn run(handler: impl Handler) -> io::Result<()> {
    let identity = Identity::from_env().map_err(invalid)?;
    let description = Description::from_package().map_err(invalid)?;
    serve(
        io::stdin().lock(),
        io::stdout(),
        &identity,
        description,
        Arc::new(handler),
    )
}

/// [`run`] on any input and output, for tests and for programs that check their environment
/// themselves.
pub fn serve(
    mut input: impl Read,
    output: impl Write + Send + 'static,
    identity: &Identity,
    description: Description,
    handler: Arc<dyn Handler>,
) -> io::Result<()> {
    let shared = Arc::new(Shared {
        out: Mutex::new(Box::new(output)),
        waiting: Mutex::new(HashMap::new()),
        next: AtomicU64::new(1),
    });
    shared.send(&json!({
        "type": "hello", "contract": "app", "versions": [CONTRACT],
        "id": identity.id, "sha256": identity.sha256,
    }))?;
    let description = Arc::new(description);
    // Each call running: its cancel flag, its job's acks and its thread.
    type Running = (Arc<AtomicBool>, Arc<Acks>, JoinHandle<()>);
    let mut calls: HashMap<String, Running> = HashMap::new();
    let result = loop {
        let done: Vec<String> = calls
            .iter()
            .filter(|(_, (_, _, h))| h.is_finished())
            .map(|(id, _)| id.clone())
            .collect();
        for id in done {
            if let Some((_, _, h)) = calls.remove(&id) {
                let _ = h.join();
            }
        }
        let frame = match read_frame(&mut input) {
            Ok(Some(f)) => f,
            Ok(None) => break Ok(()),
            Err(e) => break Err(e),
        };
        let id = frame["id"].as_str().unwrap_or_default().to_string();
        match frame["type"].as_str() {
            Some("call") if !id.is_empty() && !calls.contains_key(&id) => {
                let flag = Arc::new(AtomicBool::new(false));
                let acks = Arc::new(Acks::default());
                let call = Call {
                    id: id.clone(),
                    op: frame["op"].as_str().unwrap_or_default().to_string(),
                    input: frame["input"].clone(),
                    chain: frame["chain"].as_array().cloned().unwrap_or_default(),
                    secrets: frame["secrets"].as_object().cloned().unwrap_or_default(),
                    job: Job::from_frame(&frame["job"]),
                    cancelled: Arc::clone(&flag),
                    shared: Arc::clone(&shared),
                    acks: Arc::clone(&acks),
                    sent: AtomicU64::new(0),
                };
                let (handler, description) = (Arc::clone(&handler), Arc::clone(&description));
                let spawned = std::thread::Builder::new()
                    .name(format!("call {id}"))
                    .spawn(move || answer(&*handler, &description, call, &frame));
                match spawned {
                    Ok(h) => calls.insert(id, (flag, acks, h)),
                    Err(e) => break Err(e),
                };
            }
            Some("cancel") => {
                if let Some((flag, acks, _)) = calls.get(&id) {
                    flag.store(true, Ordering::SeqCst);
                    acks.changed.notify_all();
                }
            }
            // A job's batches the host acknowledged, up to seq.
            Some("ack") => {
                if let (Some((_, acks, _)), Some(seq)) = (calls.get(&id), frame["seq"].as_u64()) {
                    acks.ack(seq);
                }
            }
            Some("result") | Some("error") => {
                if let Some(tx) = shared.waiting.lock().unwrap().remove(&id) {
                    let _ = tx.send(if frame["type"] == "result" {
                        Ok(frame["output"].clone())
                    } else {
                        Err(Error::from_json(&frame["error"]))
                    });
                }
            }
            // Unknown frames, and calls with no ID or one in use, are left alone.
            _ => {}
        }
    };
    // The end of the input cancels every call and fails what they wait for.
    for (flag, acks, _) in calls.values() {
        flag.store(true, Ordering::SeqCst);
        acks.changed.notify_all();
    }
    shared.waiting.lock().unwrap().clear();
    for (_, (_, _, h)) in calls {
        let _ = h.join();
    }
    result
}

/// Runs one call (describe included) and sends its one terminal reply.
fn answer(handler: &dyn Handler, description: &Description, call: Call, frame: &Value) {
    let reply = if call.op == "describe" {
        let none = Vec::new();
        let unknown = frame["unknown"].as_array().unwrap_or(&none);
        let journals = frame["journals"].as_array().unwrap_or(&none);
        handler
            .reconcile(&call, unknown, journals)
            .map(|_| json!({"type": "result", "id": call.id, "operations": description.operations, "state": description.state}))
    } else {
        handler
            .call(&call)
            .map(|output| json!({"type": "result", "id": call.id, "output": output}))
    };
    let reply = match reply {
        Ok(_) if call.cancelled() && call.op != "describe" => Err(Error::cancelled()),
        r => r,
    }
    .unwrap_or_else(|e| json!({"type": "error", "id": call.id, "error": e.to_json()}));
    if let Err(e) = call.shared.send(&reply) {
        if e.kind() == io::ErrorKind::InvalidInput {
            let e = Error::internal("the answer is larger than a frame may be (1 MiB)");
            let _ = call
                .shared
                .send(&json!({"type": "error", "id": call.id, "error": e.to_json()}));
        }
    }
}

#[cfg(test)]
mod tests;
