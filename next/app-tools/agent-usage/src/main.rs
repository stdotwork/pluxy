//! pluxy/agent-usage, the usage hub (`../README.md`): the one store of agents' token usage on
//! this computer and its Agent usage page. Parsers produce kind `pluxy/agent-usage/usage` at
//! schema 1 or 2; the hub starts their jobs when the page syncs, takes their batches through `ingest`
//! (which only the host may call) and answers the page's reports.

mod report;
mod store;
mod sync;

use pluxy_program::{run, Call, Error, Handler};
use pluxy_usage::{KIND, MAX_PATH, MAX_ROOTS};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use store::{Store, INTERVALS};
use sync::Sync;

/// The host's job service, for the call being served.
struct Jobs<'a>(&'a Call);

impl sync::Host for Jobs<'_> {
    fn registrations(&self) -> Result<Vec<Value>, Error> {
        let out = self.0.jobs("registrations", json!({"kind": KIND}))?;
        Ok(out["registrations"].as_array().cloned().unwrap_or_default())
    }

    fn start(&self, r: &Value, input: Value) -> Result<String, Error> {
        let out = self.0.jobs(
            "start",
            json!({"kind": KIND, "package": r["package"], "sha256": r["sha256"],
                "generation": r["generation"], "input": input}),
        )?;
        out["job"]
            .as_str()
            .map(String::from)
            .ok_or_else(|| Error::internal("the host's start gave no job ID"))
    }

    fn cancel(&self, job: &str) -> Result<(), Error> {
        self.0.jobs("cancel", json!({"job": job})).map(|_| ())
    }
}

/// The largest list of roots kept for a source, as JSON, so a job's input stays well within the
/// host's 64 KiB.
const MAX_ROOTS_JSON: usize = 16 << 10;

/// A `roots` call's folders: full paths, at most 16, at most `MAX_ROOTS_JSON` bytes as JSON.
fn checked_roots(input: &Value) -> Result<Vec<String>, Error> {
    let roots: Vec<String> = input["roots"]
        .as_array()
        .filter(|a| a.len() <= MAX_ROOTS)
        .ok_or_else(|| Error::invalid_request("roots is not a short list of folders"))?
        .iter()
        .map(|r| {
            r.as_str()
                .filter(|s| s.len() <= MAX_PATH && Path::new(s).is_absolute())
                .map(String::from)
                .ok_or_else(|| Error::invalid_request("a root is not a full path"))
        })
        .collect::<Result<_, _>>()?;
    if json!(roots).to_string().len() > MAX_ROOTS_JSON {
        return Err(Error::invalid_request(format!(
            "the folders are longer than {MAX_ROOTS_JSON} bytes in all"
        )));
    }
    Ok(roots)
}

/// Opens the store at path. One that cannot be opened is replaced by an empty one when the call
/// is `clear` (clearing it is what the person asked); any other call fails, saying so.
fn open_store(path: &Path, clearing: bool) -> Result<Store, Error> {
    match Store::open(path) {
        Ok(s) => Ok(s),
        Err(_) if clearing => Store::reset(path)
            .map_err(|e| Error::unavailable(format!("the usage store could not be replaced: {e}"))),
        Err(e) => Err(Error::unavailable(format!(
            "the usage store cannot be read ({e}); Clear Agent history replaces it"
        ))),
    }
}

struct State {
    store: Option<Store>,
    sync: Sync,
}

struct Hub {
    data: Option<PathBuf>,
    state: Mutex<State>,
}

fn now() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_secs() as i64)
}

/// The package IDs registered now.
fn registered(host: &dyn sync::Host) -> Result<Vec<String>, Error> {
    Ok(host
        .registrations()?
        .iter()
        .filter_map(|r| r["package"].as_str().map(String::from))
        .collect())
}

impl Hub {
    fn serve(&self, call: &Call, state: &mut State) -> Result<Value, Error> {
        let internal = |e: String| Error::internal(e);
        if state.store.is_none() {
            let dir = self
                .data
                .as_deref()
                .ok_or_else(|| Error::unavailable("PLUXY_TOOL_DATA_DIR is not set"))?;
            state.store = Some(open_store(&dir.join("usage.db"), call.op == "clear")?);
        }
        let State { store, sync } = state;
        let store = store.as_mut().unwrap_or_else(|| unreachable!());
        let host = Jobs(call);
        let input = &call.input;
        match call.op.as_str() {
            "ingest" => sync.ingest(store, now(), input),
            "sync" => sync.step(store, &host, input["now"] == true, now()),
            "report" => {
                let status = sync.status(store).map_err(internal)?;
                report::report(store, input, &registered(&host)?, status)
            }
            "sources" => {
                let registered = registered(&host)?;
                let known = store.sources().map_err(internal)?;
                let mut ids: Vec<String> = known
                    .iter()
                    .filter(|s| s.rows > 0 || registered.contains(&s.id))
                    .map(|s| s.id.clone())
                    .collect();
                ids.extend(registered.iter().cloned());
                ids.sort();
                ids.dedup();
                let sources: Vec<Value> = ids
                    .iter()
                    .map(|id| {
                        let k = known.iter().find(|s| &s.id == id);
                        let label = k
                            .map(|s| s.label.clone())
                            .filter(|l| !l.is_empty())
                            .unwrap_or_else(|| id.clone());
                        json!({"id": id, "label": label,
                            "status": if registered.contains(id) { "active" } else { "archived" },
                            "roots": k.and_then(|s| s.roots.clone()),
                            "files": k.map_or(0, |s| s.files), "rows": k.map_or(0, |s| s.rows),
                            "last_scan": k.and_then(|s| s.last_scan)})
                    })
                    .collect();
                Ok(
                    json!({"sources": sources, "interval_minutes": store.interval().map_err(internal)?,
                    "last_sync": store.last_sync().map_err(internal)?}),
                )
            }
            "storage" => {
                let (bytes, rows, files) = store.storage().map_err(internal)?;
                Ok(json!({"bytes": bytes, "rows": rows, "files": files,
                    "last_sync": store.last_sync().map_err(internal)?}))
            }
            "settings" => {
                let minutes = input["interval_minutes"]
                    .as_u64()
                    .filter(|m| INTERVALS.contains(m))
                    .ok_or_else(|| {
                        Error::invalid_request("interval_minutes is not 0, 5, 15, 30 or 60")
                    })?;
                store.set_interval(minutes).map_err(internal)?;
                Ok(json!({"interval_minutes": minutes}))
            }
            "roots" => {
                let source = input["source"]
                    .as_str()
                    .ok_or_else(|| Error::invalid_request("source is not a source ID"))?;
                let known = store
                    .sources()
                    .map_err(internal)?
                    .iter()
                    .any(|s| s.id == source);
                if !known && !registered(&host)?.iter().any(|r| r == source) {
                    return Err(Error::not_found(format!("no source {source}")));
                }
                let roots = checked_roots(input)?;
                let set = (!roots.is_empty()).then_some(roots.as_slice());
                store.set_roots(source, set).map_err(internal)?;
                Ok(json!({"source": source, "roots": set}))
            }
            "clear" => {
                if sync.running() {
                    return Err(Error::unavailable(
                        "a sync is under way; clear the history once it ends",
                    ));
                }
                store.clear().map_err(internal)?;
                Ok(json!({"cleared": true}))
            }
            op => Err(Error::unsupported(format!("no operation {op}"))),
        }
    }
}

impl Handler for Hub {
    fn call(&self, call: &Call) -> Result<Value, Error> {
        // The host runs one call of a package at a time; the lock keeps that true here too.
        let mut state = self.state.lock().unwrap_or_else(|p| p.into_inner());
        self.serve(call, &mut state)
    }
}

fn main() -> std::io::Result<()> {
    run(Hub {
        data: std::env::var_os("PLUXY_TOOL_DATA_DIR").map(PathBuf::from),
        state: Mutex::new(State {
            store: None,
            sync: Sync::new(),
        }),
    })
}

#[cfg(test)]
mod tests;
