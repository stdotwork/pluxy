//! Syncs, as the page drives them: each `sync` call starts what is due and says how far the sync
//! is; the host delivers the jobs' batches and ends to `ingest` between calls. A sync lists each
//! registered parser's files (a `files` job), then reads the files whose revision changed (`read`
//! jobs naming them, in chunks), and commits each file's rows when its last part arrives. Parts of
//! a file not finished when its job ends are dropped, and the file counts as failed. A job the
//! hub has heard nothing of for `STALE_SECS` (its end may never come: the host delivers an end
//! once, best-effort) is cancelled and counted as ended, so a run always ends. The run lives in
//! memory: if the hub stops, the next sync starts over and skips every file already committed.

use crate::store::{Rows, Store};
use pluxy_program::Error;
use pluxy_usage::{Row, KIND, MAX_FILES, MAX_PATH};
use serde_json::{json, Value};
use std::collections::{HashMap, HashSet, VecDeque};

/// Jobs the hub runs at once (the host allows a consumer four).
pub const MAX_JOBS: usize = 4;
/// File IDs a `read` job names at most.
const CHUNK_FILES: usize = 64;
/// A job input's JSON size at most: the host takes at most 64 KiB.
const INPUT_BUDGET: usize = 60 << 10;
/// The longest label kept.
const MAX_LABEL: usize = 64;
/// Seconds with no delivery after which a job is cancelled and counted as ended: above the
/// host's ten-second bound on a delivery's wait.
pub const STALE_SECS: i64 = 60;
/// Distinct (hour, model) rows one file may stage; a file past it fails.
pub const MAX_STAGED_ROWS: usize = 100_000;
/// The earliest hour a row may name (2000-01-01 UTC), and how far past now (a day).
pub const MIN_HOUR: i64 = 946_684_800;
const MAX_AHEAD: i64 = 86_400;

/// What the hub asks the host's job service.
pub trait Host {
    /// The registrations of the hub's kind: each enabled parser, as the host admitted it.
    fn registrations(&self) -> Result<Vec<Value>, Error>;
    /// Starts a job of registration with input: the job's ID.
    fn start(&self, registration: &Value, input: Value) -> Result<String, Error>;
    /// Cancels one of the hub's jobs.
    fn cancel(&self, job: &str) -> Result<(), Error>;
}

/// A model name as the hub keeps it: trimmed, lower case, without a path before its last `/`.
pub fn model_name(model: &str) -> String {
    let m = model.trim();
    m.rsplit('/').next().unwrap_or(m).trim().to_lowercase()
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Phase {
    /// Its files job is not started yet.
    Waiting,
    Listing,
    Reading,
    Done,
    Failed,
}

impl Phase {
    fn name(self) -> &'static str {
        match self {
            Phase::Waiting => "waiting",
            Phase::Listing => "listing",
            Phase::Reading => "reading",
            Phase::Done => "done",
            Phase::Failed => "failed",
        }
    }
}

/// One source's part of a run.
struct SourceRun {
    registration: Value,
    id: String,
    label: String,
    phase: Phase,
    roots: Option<Vec<String>>,
    inventory: Vec<(String, u64, i64)>,
    truncated: bool,
    todo: VecDeque<String>,
    changed: u64,
    read: u64,
    failed: u64,
    error: Option<String>,
}

/// A file's parts so far.
#[derive(Default)]
struct Staged {
    next: u64,
    rows: Rows,
    /// Refused (and counted failed) before its last part: its later parts are taken and dropped.
    refused: bool,
}

enum Kind {
    Files,
    Read {
        files: HashSet<String>,
        done: HashSet<String>,
        staged: HashMap<String, Staged>,
    },
}

struct JobRun {
    source: usize,
    kind: Kind,
    /// When it started, or its last delivery came.
    heard: i64,
}

struct Run {
    started: i64,
    sources: Vec<SourceRun>,
    jobs: HashMap<String, JobRun>,
}

/// The hub's sync state over its store.
pub struct Sync {
    run: Option<Run>,
    /// The last run's per-source status, shown once it ended.
    last: Vec<Value>,
}

fn status_of(s: &SourceRun, jobs: &HashMap<String, JobRun>, index: usize) -> Value {
    let staged: u64 = jobs
        .values()
        .filter(|j| j.source == index)
        .map(|j| match &j.kind {
            Kind::Read { staged, .. } => staged.values().map(|f| f.next).sum(),
            Kind::Files => 0,
        })
        .sum();
    json!({"id": s.id, "label": s.label, "phase": s.phase.name(), "listed": s.inventory.len(),
        "changed": s.changed, "read": s.read, "failed": s.failed, "staged": staged,
        "truncated": s.truncated, "error": s.error})
}

impl Default for Sync {
    fn default() -> Self {
        Sync::new()
    }
}

impl Sync {
    pub fn new() -> Sync {
        Sync {
            run: None,
            last: Vec::new(),
        }
    }

    pub fn running(&self) -> bool {
        self.run.is_some()
    }

    /// Where the sync is: `{running, started, last_sync, interval_minutes, sources}`.
    pub fn status(&self, store: &Store) -> Result<Value, String> {
        let (started, sources) = match &self.run {
            Some(r) => (
                json!(r.started),
                r.sources
                    .iter()
                    .enumerate()
                    .map(|(i, s)| status_of(s, &r.jobs, i))
                    .collect(),
            ),
            None => (Value::Null, self.last.clone()),
        };
        Ok(json!({"running": self.run.is_some(), "started": started,
            "last_sync": store.last_sync()?, "interval_minutes": store.interval()?,
            "sources": sources}))
    }

    /// A `sync` call at time now: starts a run when asked (`start`) or due (never synced, or the
    /// interval passed), starts the jobs due, and ends the run once every source is done.
    pub fn step(
        &mut self,
        store: &mut Store,
        host: &dyn Host,
        start: bool,
        now: i64,
    ) -> Result<Value, Error> {
        let internal = |e: String| Error::internal(e);
        if self.run.is_none() {
            let interval = store.interval().map_err(internal)?;
            let due = match store.last_sync().map_err(internal)? {
                None => true,
                Some(last) => interval > 0 && now - last >= interval as i64 * 60,
            };
            if start || due {
                let mut sources = Vec::new();
                for r in host.registrations()? {
                    let Some(id) = r["package"].as_str().map(String::from) else {
                        continue;
                    };
                    store.ensure_source(&id).map_err(internal)?;
                    let known = store
                        .sources()
                        .map_err(internal)?
                        .into_iter()
                        .find(|s| s.id == id);
                    sources.push(SourceRun {
                        registration: r,
                        label: known
                            .as_ref()
                            .map(|s| s.label.clone())
                            .filter(|l| !l.is_empty())
                            .unwrap_or_else(|| id.clone()),
                        roots: store.roots(&id).map_err(internal)?,
                        id,
                        phase: Phase::Waiting,
                        inventory: Vec::new(),
                        truncated: false,
                        todo: VecDeque::new(),
                        changed: 0,
                        read: 0,
                        failed: 0,
                        error: None,
                    });
                }
                self.run = Some(Run {
                    started: now,
                    sources,
                    jobs: HashMap::new(),
                });
            }
        }
        if let Some(run) = &mut self.run {
            let stale: Vec<String> = run
                .jobs
                .iter()
                .filter(|(_, j)| now - j.heard >= STALE_SECS)
                .map(|(id, _)| id.clone())
                .collect();
            for id in stale {
                // Its end may still come; it is refused then, as no job of this sync.
                let _ = host.cancel(&id);
                if let Some(job) = run.jobs.remove(&id) {
                    let s = &mut run.sources[job.source];
                    s.error = Some(format!(
                        "no word from the parser for {STALE_SECS} seconds: its job was cancelled"
                    ));
                    match &job.kind {
                        Kind::Files => s.phase = Phase::Failed,
                        Kind::Read {
                            files,
                            done,
                            staged,
                        } => s.failed += unread(files, done, staged),
                    }
                }
            }
            for i in 0..run.sources.len() {
                advance(run, i, host, now);
            }
            let finished = run.jobs.is_empty()
                && run
                    .sources
                    .iter()
                    .all(|s| matches!(s.phase, Phase::Done | Phase::Failed));
            if finished {
                let run = self.run.take().unwrap_or_else(|| unreachable!());
                for s in &run.sources {
                    store.set_scanned(&s.id, now).map_err(internal)?;
                }
                store.set_last_sync(now).map_err(internal)?;
                self.last = run
                    .sources
                    .iter()
                    .enumerate()
                    .map(|(i, s)| status_of(s, &run.jobs, i))
                    .collect();
            }
        }
        self.status(store).map_err(internal)
    }

    /// A delivery from the host: a batch or the end of one of this run's jobs.
    pub fn ingest(&mut self, store: &mut Store, now: i64, d: &Value) -> Result<Value, Error> {
        if d["kind"] != KIND {
            return Err(Error::invalid_request("not a delivery of the hub's kind"));
        }
        let run = self
            .run
            .as_mut()
            .ok_or_else(|| Error::invalid_request("no sync is under way"))?;
        let job_id = d["job"].as_str().unwrap_or_default().to_string();
        let index = run
            .jobs
            .get(&job_id)
            .map(|j| j.source)
            .ok_or_else(|| Error::invalid_request("not a job of this sync"))?;
        let source = &mut run.sources[index];
        // The host names the source; it must be the registration the job was started for.
        if d["source"]["package"] != source.registration["package"]
            || d["source"]["sha256"] != source.registration["sha256"]
        {
            return Err(Error::new(
                "permission",
                "the delivery's source is not the job's",
            ));
        }
        let value = &d["value"];
        match d["event"].as_str() {
            Some("batch") => {
                let job = run.jobs.get_mut(&job_id).unwrap_or_else(|| unreachable!());
                job.heard = now;
                match &mut job.kind {
                    Kind::Files => files_batch(source, value),
                    Kind::Read {
                        files,
                        done,
                        staged,
                    } => read_batch(store, source, files, done, staged, value, now),
                }
            }
            Some(end @ ("done" | "failed" | "cancelled")) => {
                let job = run.jobs.remove(&job_id).unwrap_or_else(|| unreachable!());
                let why = || {
                    d["error"]["message"]
                        .as_str()
                        .map(|m| m.chars().take(512).collect::<String>())
                        .unwrap_or_else(|| end.to_string())
                };
                match job.kind {
                    // A store that fails here fails the source, so the run can still end.
                    Kind::Files if end == "done" => {
                        if let Err(e) = listed(store, source, &d["output"]) {
                            source.phase = Phase::Failed;
                            source.error = Some(e.message);
                        }
                    }
                    Kind::Files => {
                        source.phase = Phase::Failed;
                        source.error = Some(why());
                    }
                    // Files of the job it did not finish keep their old rows and revision, so the
                    // next sync reads them again; their staged parts go with the job, and they
                    // count as failed.
                    Kind::Read {
                        files,
                        done,
                        staged,
                    } if end != "done" => {
                        source.failed += unread(&files, &done, &staged);
                        source.error = Some(why());
                    }
                    Kind::Read { .. } => {}
                }
                Ok(json!({}))
            }
            _ => Err(Error::invalid_request("not a known delivery event")),
        }
    }
}

/// Starts source i's next jobs, while the run has room.
fn advance(run: &mut Run, i: usize, host: &dyn Host, now: i64) {
    let room = |run: &Run| run.jobs.len() < MAX_JOBS;
    match run.sources[i].phase {
        Phase::Waiting if room(run) => {
            let s = &mut run.sources[i];
            let mut input = json!({"mode": "files"});
            if let Some(r) = &s.roots {
                input["roots"] = json!(r);
            }
            match host.start(&s.registration, input) {
                Ok(job) => {
                    s.phase = Phase::Listing;
                    run.jobs.insert(
                        job,
                        JobRun {
                            source: i,
                            kind: Kind::Files,
                            heard: now,
                        },
                    );
                }
                // The host's limit: tried again at the next call.
                Err(e) if e.code == "unavailable" => {}
                Err(e) => {
                    s.phase = Phase::Failed;
                    s.error = Some(e.message);
                }
            }
        }
        Phase::Reading => {
            while room(run) && !run.sources[i].todo.is_empty() {
                let s = &mut run.sources[i];
                // The input's JSON stays within the budget: its roots, then file IDs as JSON.
                let mut bytes = json!({"mode": "read", "roots": s.roots, "files": []})
                    .to_string()
                    .len();
                let mut chunk = Vec::new();
                while let Some(f) = s.todo.front() {
                    let n = json!(f).to_string().len() + 1;
                    if chunk.len() == CHUNK_FILES || (bytes + n > INPUT_BUDGET && !chunk.is_empty())
                    {
                        break;
                    }
                    bytes += n;
                    chunk.push(s.todo.pop_front().unwrap_or_default());
                }
                let mut input = json!({"mode": "read", "files": chunk});
                if let Some(r) = &s.roots {
                    input["roots"] = json!(r);
                }
                match host.start(&s.registration, input) {
                    Ok(job) => {
                        run.jobs.insert(
                            job,
                            JobRun {
                                source: i,
                                kind: Kind::Read {
                                    files: chunk.into_iter().collect(),
                                    done: HashSet::new(),
                                    staged: HashMap::new(),
                                },
                                heard: now,
                            },
                        );
                    }
                    Err(e) if e.code == "unavailable" => {
                        for f in chunk.into_iter().rev() {
                            s.todo.push_front(f);
                        }
                        break;
                    }
                    Err(e) => {
                        s.failed += chunk.len() as u64;
                        s.error = Some(e.message);
                    }
                }
            }
            let s = &mut run.sources[i];
            if s.todo.is_empty() && !run.jobs.values().any(|j| j.source == i) {
                s.phase = Phase::Done;
            }
        }
        _ => {}
    }
}

/// The files of a `read` job that ended before they were done, and not yet counted failed: those
/// refused part way were counted when they were.
fn unread(
    files: &HashSet<String>,
    done: &HashSet<String>,
    staged: &HashMap<String, Staged>,
) -> u64 {
    files
        .iter()
        .filter(|f| !done.contains(*f) && !staged.get(*f).is_some_and(|s| s.refused))
        .count() as u64
}

/// A `files` batch: `{files: [{file, size, mtime_ns}]}`.
fn files_batch(source: &mut SourceRun, value: &Value) -> Result<Value, Error> {
    let files = value["files"]
        .as_array()
        .ok_or_else(|| Error::invalid_request("a files batch has no files"))?;
    for f in files {
        let (Some(file), Some(size), Some(mtime)) = (
            f["file"]
                .as_str()
                .filter(|s| !s.is_empty() && s.len() <= MAX_PATH),
            f["size"].as_u64(),
            f["mtime_ns"].as_i64(),
        ) else {
            return Err(Error::invalid_request(
                "a listed file is not {file, size, mtime_ns}",
            ));
        };
        if source.inventory.len() < MAX_FILES {
            source.inventory.push((file.to_string(), size, mtime));
        } else {
            source.truncated = true;
        }
    }
    Ok(json!({}))
}

/// The end of a source's `files` job: its label, and the files to read, those whose revision is
/// not the one stored; a file no longer listed is marked gone (its rows stay), unless the listing
/// was cut short.
fn listed(store: &mut Store, source: &mut SourceRun, output: &Value) -> Result<(), Error> {
    let internal = |e: String| Error::internal(e);
    if let Some(label) = output["label"].as_str() {
        let label: String = label.trim().chars().take(MAX_LABEL).collect();
        if !label.is_empty() {
            store.set_label(&source.id, &label).map_err(internal)?;
            source.label = label;
        }
    }
    source.truncated |= output["truncated"] == true;
    let stored = store.revisions(&source.id).map_err(internal)?;
    let mut seen = HashSet::new();
    for (file, size, mtime) in &source.inventory {
        if seen.insert(file.clone()) && stored.get(file) != Some(&(*size, *mtime)) {
            source.todo.push_back(file.clone());
        }
    }
    source.changed = source.todo.len() as u64;
    if !source.truncated {
        store.mark_present(&source.id, &seen).map_err(internal)?;
    }
    source.phase = Phase::Reading;
    Ok(())
}

/// A `read` batch: a part of a file's rows, committed with the file's last part, or the file's
/// error (its old rows stay). A file with a row before 2000 or more than a day after now, or with
/// more than `MAX_STAGED_ROWS` rows, fails (its old rows stay) and the job goes on: its later
/// parts are dropped.
fn read_batch(
    store: &mut Store,
    source: &mut SourceRun,
    files: &HashSet<String>,
    done: &mut HashSet<String>,
    staged: &mut HashMap<String, Staged>,
    value: &Value,
    now: i64,
) -> Result<Value, Error> {
    let file = value["file"]
        .as_str()
        .filter(|f| files.contains(*f) && !done.contains(*f))
        .ok_or_else(|| Error::invalid_request("a batch names a file the job does not read"))?
        .to_string();
    if value.get("error").is_some() {
        // A file already refused was counted when it was.
        let counted = staged.remove(&file).is_some_and(|s| s.refused);
        done.insert(file);
        if !counted {
            source.failed += 1;
        }
        return Ok(json!({}));
    }
    if staged.get(&file).is_some_and(|s| s.refused) {
        if value["last"] == true {
            staged.remove(&file);
            done.insert(file);
        }
        return Ok(json!({}));
    }
    let bad = |what: &str| Error::invalid_request(format!("a read batch's {what} is wrong"));
    let part = value["part"].as_u64().ok_or_else(|| bad("part"))?;
    let entry = staged.entry(file.clone()).or_default();
    if part != entry.next {
        staged.remove(&file);
        done.insert(file);
        source.failed += 1;
        return Err(bad("part number"));
    }
    let mut refused = None;
    for r in value["rows"].as_array().ok_or_else(|| bad("rows"))? {
        let row = Row::from_json(r).map_err(|e| bad(&e))?;
        if row.hour < MIN_HOUR || row.hour > now + MAX_AHEAD {
            refused = Some(format!(
                "{file}: a row's hour is before 2000 or more than a day from now"
            ));
            break;
        }
        entry
            .rows
            .entry((row.hour, model_name(&row.model)))
            .or_default()
            .add(&row.tokens);
        if entry.rows.len() > MAX_STAGED_ROWS {
            refused = Some(format!("{file}: more than {MAX_STAGED_ROWS} rows"));
            break;
        }
    }
    if let Some(why) = refused {
        source.failed += 1;
        source.error = Some(why);
        if value["last"] == true {
            staged.remove(&file);
            done.insert(file);
        } else {
            // Its later parts are taken and dropped until the last: refusing them would end the
            // whole job.
            *entry = Staged {
                refused: true,
                ..Staged::default()
            };
        }
        return Ok(json!({}));
    }
    entry.next += 1;
    if value["last"] == true {
        let size = value["size"].as_u64().ok_or_else(|| bad("size"))?;
        let mtime = value["mtime_ns"].as_i64().ok_or_else(|| bad("mtime_ns"))?;
        let rows = staged.remove(&file).map(|s| s.rows).unwrap_or_default();
        store
            .replace_file(&source.id, &file, size, mtime, &rows)
            .map_err(Error::internal)?;
        done.insert(file);
        source.read += 1;
    }
    Ok(json!({}))
}
