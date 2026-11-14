//! Link-preserving, journalled config writes for pluxy's app tools (`pluxy-transactions`).
//!
//! A writer previews what the person selected ([`Transactions::preview`]): each selected config
//! file, or file inside a selected root folder, resolved through its link (stow and chezmoi make
//! them) and pinned: the link's text and identity, the target, its folder's identity and the
//! file's identity and SHA-256. The pins go back in the write's input, so the operation's native
//! confirmation shows the selected path and the file it resolves to; the write checks them again
//! and refuses a link pointed elsewhere, a replaced target or a file modified since the preview.
//!
//! [`Transactions::apply`], [`Transactions::clear`] and [`Transactions::restore`] then write each
//! file through a handle on its folder: a new file renamed over the target, so a link stays the
//! same link with the same text, and only the target changes. Each write is checked before and
//! after the rename; the whole operation is checked at its end. The old bytes are kept as vault
//! backups and the operation as a vault journal; a write that fails puts back the files already
//! written, except one another program changed meanwhile, which stays as that program left it.
//! The images apply keeps (the original, with the person's later edits promoted into it, and the
//! applied one) let clear remove only the writer's untouched values ([`Merge`]) and restore put
//! the original back.
//!
//! After a crash, the next start's describe names the open journal and [`Transactions::reconcile`]
//! looks at the disk: a write that finished is committed, anything else is rolled back from the
//! backups, never written again, and never over a competing edit.
//!
//! Other programs do not share these checks: a write between the last check and the rename, or
//! after the final one, is not seen. Several files are journalled, not written atomically.

mod b64;
mod merge;
mod target;

#[cfg(test)]
mod tests;

pub use merge::{merge_value, Json, Merge};
pub use target::{pin_schema, sha256, sha256_of, Failed, Id, Pin, Selection, Target, MAX_FILE};

use cap_std::ambient_authority;
use cap_std::fs::{Dir, OpenOptions};
use pluxy_program::{Call, Error};
use serde_json::{json, Map, Value};
use std::io::{ErrorKind, Read, Write};
use std::os::unix::fs::PermissionsExt;
use std::path::{Path, PathBuf};

/// The `kind` of the journals this library begins.
pub const KIND: &str = "pluxy-transactions";

/// The most targets one operation writes.
pub const MAX_TARGETS: usize = 8;

/// The state file in the tool's data folder.
const STATE: &str = "transactions.json";

/// The backups this library keeps are labelled with this prefix.
const LABEL: &str = "tx ";

/// The journal IDs whose writes committed (kept for reconciliation), and the recovery reports
/// `status` shows: at most this many of each.
const KEEP: usize = 32;

/// What a writer's apply gives a target, from its index, its pin and its bytes now (None: no
/// file): its new bytes, or None to remove it.
pub type Image<'a> = dyn Fn(usize, &Pin, Option<&[u8]>) -> Result<Option<Vec<u8>>, Error> + 'a;

/// The vault the host keeps for the tool: its `backup.*` and `journal.*` frames.
pub trait Vault {
    /// Sends one vault frame: its output, or its error.
    fn vault(&self, op: &str, fields: Map<String, Value>) -> Result<Value, Error>;
}

impl Vault for Call {
    fn vault(&self, op: &str, fields: Map<String, Value>) -> Result<Value, Error> {
        Call::vault(self, op, fields)
    }
}

fn fields(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// The tool's data folder, `PLUXY_TOOL_DATA_DIR`, which the host gives it.
pub fn data_dir() -> Result<PathBuf, Error> {
    std::env::var_os("PLUXY_TOOL_DATA_DIR")
        .filter(|d| !d.is_empty())
        .map(PathBuf::from)
        .ok_or_else(|| Error::unavailable("the app gave no data folder (PLUXY_TOOL_DATA_DIR)"))
}

/// A set's name: 1 to 32 lower-case letters, digits and hyphens.
fn check_set(set: &str) -> Result<(), Error> {
    let ok = (1..=32).contains(&set.len())
        && set
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-');
    if ok {
        Ok(())
    } else {
        Err(Error::invalid_request(
            "a set is 1 to 32 lower-case letters, digits and hyphens",
        ))
    }
}

/// One file of an operation: its target, its bytes and permission bits now, the bytes it gets,
/// and the permission bits a file it creates gets (else 0600).
struct Change {
    target: Target,
    before: Option<Vec<u8>>,
    mode: Option<u32>,
    after: Option<Vec<u8>>,
    after_mode: Option<u32>,
}

/// Where a pin is, as records name it: the selection and the target.
fn place(pin: &Pin) -> Value {
    let (selected, path) = match &pin.selection {
        Selection::File(f) => (f.clone(), None),
        Selection::Root { root, path } => (root.clone(), Some(path.clone())),
    };
    json!({"selected": selected, "path": path, "target": pin.target})
}

/// What a set's record keeps for one target: where it is, the backups of its original and
/// applied images (null: no file) and the original's permission bits.
fn record(
    pin: &Pin,
    original: Option<String>,
    original_mode: Option<u32>,
    applied: Option<String>,
    applied_sha: Option<String>,
) -> Value {
    let mut r = place(pin);
    r["original"] = json!(original);
    r["original_mode"] = json!(original_mode);
    r["applied"] = json!(applied);
    r["applied_sha256"] = json!(applied_sha);
    r
}

/// Whether a set's record is for exactly these pins' targets, in order. The selection may differ:
/// a pin of the target file itself reaches a record made through a link that now points
/// elsewhere.
fn matches(rec: &Value, pins: &[Pin]) -> bool {
    let targets = rec["targets"]
        .as_array()
        .map(Vec::as_slice)
        .unwrap_or_default();
    targets.len() == pins.len()
        && targets
            .iter()
            .zip(pins)
            .all(|(t, p)| t["target"] == json!(p.target))
}

/// Whether none of a record's selections resolves to its recorded target any more (a link
/// pointed elsewhere, a root or file gone).
fn stale(rec: &Value) -> bool {
    rec["targets"].as_array().into_iter().flatten().all(|t| {
        let file = t["selected"].as_str().map(PathBuf::from);
        let selection = match (file, t["path"].as_str()) {
            (Some(root), Some(path)) => Selection::Root {
                root,
                path: path.into(),
            },
            (Some(f), None) => Selection::File(f),
            _ => return true,
        };
        Target::resolve(&selection).map_or(true, |now| json!(now.pin.target) != t["target"])
    })
}

/// A kept image: the original of a target an operation could not finish cleanly (another program
/// changed it meanwhile: `conflict`) or whose record a new apply replaced (`retargeted`), kept so
/// restore can still put it back.
fn kept(
    set: &str,
    at: &Value,
    original: &Value,
    mode: &Value,
    written: &Value,
    why: &str,
) -> Value {
    json!({"set": set, "selected": at["selected"], "path": at["path"], "target": at["target"],
        "original": original, "original_mode": mode, "written": written, "why": why})
}

/// The library's operations for one call, with the vault of that call and the tool's data folder.
pub struct Transactions<'a> {
    vault: &'a dyn Vault,
    data: PathBuf,
}

impl<'a> Transactions<'a> {
    pub fn new(vault: &'a dyn Vault, data: impl Into<PathBuf>) -> Transactions<'a> {
        Transactions {
            vault,
            data: data.into(),
        }
    }

    // ---- state file

    fn data_folder(&self) -> Result<Dir, Error> {
        Dir::open_ambient_dir(&self.data, ambient_authority())
            .map_err(|e| Error::unavailable(format!("the data folder: {e}")))
    }

    fn load(&self) -> Result<Value, Error> {
        let d = self.data_folder()?;
        let mut bytes = Vec::new();
        match d.open(STATE) {
            Ok(f) => f
                .into_std()
                .read_to_end(&mut bytes)
                .map_err(|e| Error::unavailable(format!("the state file: {e}")))?,
            Err(e) if e.kind() == ErrorKind::NotFound => {
                return Ok(json!({"version": 1, "sets": {}, "committed": [], "recovered": []}))
            }
            Err(e) => return Err(Error::unavailable(format!("the state file: {e}"))),
        };
        let v: Value = serde_json::from_slice(&bytes)
            .map_err(|e| Error::unavailable(format!("the state file is broken: {e}")))?;
        if v["version"] != 1 || !v["sets"].is_object() {
            return Err(Error::unavailable(
                "the state file is not one this tool writes",
            ));
        }
        Ok(v)
    }

    fn save(&self, state: &Value) -> Result<(), Error> {
        let d = self.data_folder()?;
        let tmp = format!(".{STATE}.{}.tmp", target::random_hex()?);
        let bytes = serde_json::to_vec(state).map_err(|e| Error::internal(e.to_string()))?;
        let r = (|| -> std::io::Result<()> {
            let mut f = d
                .open_with(&tmp, OpenOptions::new().write(true).create_new(true))?
                .into_std();
            f.set_permissions(std::fs::Permissions::from_mode(0o600))?;
            f.write_all(&bytes)?;
            f.sync_all()?;
            d.rename(&tmp, &d, STATE)?;
            d.open(".")?.sync_all()
        })();
        r.map_err(|e| {
            let _ = d.remove_file(&tmp);
            Error::unavailable(format!("saving the state file: {e}"))
        })
    }

    // ---- vault

    fn put(&self, label: &str, bytes: &[u8]) -> Result<String, Error> {
        let v = self.vault.vault(
            "backup.put",
            fields(json!({"label": format!("{LABEL}{label}"), "data": b64::encode(bytes)})),
        )?;
        v["backup"]
            .as_str()
            .map(String::from)
            .ok_or_else(|| Error::internal("the vault gave no backup ID"))
    }

    fn put_image(&self, label: &str, image: Option<&[u8]>) -> Result<Option<String>, Error> {
        image.map(|b| self.put(label, b)).transpose()
    }

    /// The bytes of a backup (null: no file). A vault answer without them is an error, never an
    /// empty file.
    fn get(&self, backup: &Value) -> Result<Option<Vec<u8>>, Error> {
        let Some(id) = backup.as_str() else {
            return Ok(None);
        };
        let v = self
            .vault
            .vault("backup.get", fields(json!({"backup": id})))?;
        let data = v["data"]
            .as_str()
            .ok_or_else(|| Error::internal("the vault gave a backup without its bytes"))?;
        b64::decode(data)
            .map(Some)
            .ok_or_else(|| Error::internal("the vault gave a backup that is not base64"))
    }

    /// Deletes this library's backups that nothing names any more: no set's record, no kept
    /// image and no journal left open after a failed put-back.
    fn sweep(&self, state: &Value) {
        let mut keep = Vec::new();
        for rec in state["sets"].as_object().into_iter().flat_map(Map::values) {
            for t in rec["targets"].as_array().into_iter().flatten() {
                keep.extend([t["original"].clone(), t["applied"].clone()]);
            }
        }
        for k in state["kept"].as_array().into_iter().flatten() {
            keep.extend([k["original"].clone(), k["written"].clone()]);
        }
        for j in state["open"].as_array().into_iter().flatten() {
            keep.extend(j["backups"].as_array().into_iter().flatten().cloned());
        }
        let Ok(list) = self.vault.vault("backup.list", Map::new()) else {
            return;
        };
        for b in list["backups"].as_array().into_iter().flatten() {
            let ours = b["label"].as_str().is_some_and(|l| l.starts_with(LABEL));
            if ours && !keep.contains(&b["backup"]) {
                let _ = self
                    .vault
                    .vault("backup.delete", fields(json!({"backup": b["backup"]})));
            }
        }
    }

    // ---- operations

    /// Resolves and pins each selection of set: `{"set", "targets": [<pin>], "applied", "edited"}`,
    /// where `applied` says whether set was applied to exactly these targets and `edited`, for
    /// each, whether its file changed since.
    pub fn preview(&self, set: &str, selections: &[Selection]) -> Result<Value, Error> {
        check_set(set)?;
        if selections.is_empty() || selections.len() > MAX_TARGETS {
            return Err(Error::invalid_request(format!(
                "an operation has 1 to {MAX_TARGETS} targets"
            )));
        }
        let pins = selections
            .iter()
            .map(|s| Target::resolve(s).map(|t| t.pin))
            .collect::<Result<Vec<_>, _>>()?;
        distinct(&pins)?;
        let state = self.load()?;
        let rec = &state["sets"][set];
        let applied = !rec.is_null() && matches(rec, &pins);
        let edited: Vec<bool> = pins
            .iter()
            .enumerate()
            .map(|(i, p)| applied && json!(p.sha256) != rec["targets"][i]["applied_sha256"])
            .collect();
        Ok(json!({
            "set": set,
            "targets": pins.iter().map(Pin::to_json).collect::<Vec<_>>(),
            "applied": applied,
            "edited": edited,
        }))
    }

    /// Writes each pinned target of set with what image gives for it (from its bytes now), and
    /// records set as applied to them. Pins come from a preview; a target that resolves otherwise
    /// now, or changed since, is refused. Applying set again keeps its original, with the
    /// person's edits since the last apply promoted into it (merge). A record none of whose
    /// selections still resolves to its target (a link pointed elsewhere) is replaced, its
    /// originals kept for restore. written is told each file's index once it is written.
    pub fn apply(
        &self,
        set: &str,
        pins: &[Pin],
        image: &Image<'_>,
        merge: &dyn Merge,
        written: &mut dyn FnMut(usize),
    ) -> Result<Value, Error> {
        let mut state = self.load()?;
        let mut rec = state["sets"][set].clone();
        let changes = self.open(set, pins)?;
        if !rec.is_null() && !matches(&rec, pins) {
            if !stale(&rec) {
                return Err(Error::invalid_request(format!(
                    "{set} is applied to other targets; clear or restore it first"
                )));
            }
            for t in rec["targets"].as_array().into_iter().flatten() {
                let k = kept(
                    set,
                    t,
                    &t["original"],
                    &t["original_mode"],
                    &t["applied"],
                    "retargeted",
                );
                remember_all(&mut state, "kept", k);
            }
            set_record(&mut state, set, None);
            self.save(&state)?;
            rec = Value::Null;
        }
        let mut out = Vec::new();
        let mut targets = Vec::new();
        for (i, c) in changes.into_iter().enumerate() {
            let after = image(i, &c.target.pin, c.before.as_deref())?;
            let (original, original_mode) = if rec.is_null() {
                (c.before.clone(), c.mode)
            } else {
                let t = &rec["targets"][i];
                let o = merge.merge(
                    self.get(&t["original"])?.as_deref(),
                    self.get(&t["applied"])?.as_deref(),
                    c.before.as_deref(),
                )?;
                (o, t["original_mode"].as_u64().map(|m| m as u32))
            };
            out.push((original, original_mode, Change { after, ..c }));
        }
        if out.iter().all(|(_, _, c)| c.after == c.before) {
            return Ok(json!({"outcome": "unchanged", "changed": []}));
        }
        for (original, mode, c) in &out {
            let o = self.put_image("original", original.as_deref())?;
            let a = self.put_image("applied", c.after.as_deref())?;
            targets.push(record(
                &c.target.pin,
                o,
                *mode,
                a,
                sha256_of(c.after.as_deref()),
            ));
        }
        let changes = out.into_iter().map(|(_, _, c)| c).collect();
        self.run(
            "apply",
            set,
            changes,
            Some(Some(json!({"targets": targets}))),
            written,
        )
        .map(|changed| json!({"outcome": "applied", "changed": changed}))
    }

    /// Takes set's values out of its pinned targets: each file becomes what merge makes of its
    /// original, its applied image and its bytes now, so the person's edits stay; set is no
    /// longer applied. A pin of the recorded target file itself reaches a record made through a
    /// link that now points elsewhere.
    pub fn clear(
        &self,
        set: &str,
        pins: &[Pin],
        merge: &dyn Merge,
        written: &mut dyn FnMut(usize),
    ) -> Result<Value, Error> {
        self.undo("clear", set, pins, Some(merge), written)
            .map(|changed| json!({"outcome": "cleared", "changed": changed}))
    }

    /// Puts set's original images back in its pinned targets (removing a file that did not
    /// exist); set is no longer applied. Without a record for these targets, it puts back the
    /// originals kept for them (after a conflict or a replaced record).
    pub fn restore(
        &self,
        set: &str,
        pins: &[Pin],
        written: &mut dyn FnMut(usize),
    ) -> Result<Value, Error> {
        self.undo("restore", set, pins, None, written)
            .map(|changed| json!({"outcome": "restored", "changed": changed}))
    }

    fn undo(
        &self,
        op: &str,
        set: &str,
        pins: &[Pin],
        merge: Option<&dyn Merge>,
        written: &mut dyn FnMut(usize),
    ) -> Result<Vec<Value>, Error> {
        let state = self.load()?;
        let rec = state["sets"][set].clone();
        let mut changes = self.open(set, pins)?;
        if !rec.is_null() && matches(&rec, pins) {
            for (i, c) in changes.iter_mut().enumerate() {
                let t = &rec["targets"][i];
                let original = self.get(&t["original"])?;
                c.after_mode = t["original_mode"].as_u64().map(|m| m as u32);
                c.after = match merge {
                    Some(m) => m.merge(
                        original.as_deref(),
                        self.get(&t["applied"])?.as_deref(),
                        c.before.as_deref(),
                    )?,
                    None => original,
                };
            }
            return self.run(op, set, changes, Some(None), written);
        }
        // Restore of kept originals: the oldest kept for each target in set.
        let all = state["kept"].as_array().cloned().unwrap_or_default();
        let found: Option<Vec<&Value>> = (merge.is_none())
            .then(|| {
                pins.iter()
                    .map(|p| {
                        all.iter()
                            .find(|k| k["set"] == set && k["target"] == json!(p.target))
                    })
                    .collect()
            })
            .flatten();
        let Some(found) = found else {
            return Err(Error::invalid_request(format!(
                "{set} is not applied to these targets"
            )));
        };
        for (c, k) in changes.iter_mut().zip(found) {
            c.after = self.get(&k["original"])?;
            c.after_mode = k["original_mode"].as_u64().map(|m| m as u32);
        }
        // The set's own record, for other targets, stays as it is.
        self.run(op, set, changes, None, written)
    }

    /// The pinned targets of set, opened and checked against their pins, with their bytes now.
    fn open(&self, set: &str, pins: &[Pin]) -> Result<Vec<Change>, Error> {
        check_set(set)?;
        if pins.is_empty() || pins.len() > MAX_TARGETS {
            return Err(Error::invalid_request(format!(
                "an operation has 1 to {MAX_TARGETS} targets"
            )));
        }
        distinct(pins)?;
        pins.iter()
            .map(|p| {
                let target = Target::open(p)?;
                let (_, before) = target.read_now()?;
                let mode = target.mode_now()?;
                Ok(Change {
                    target,
                    before: before.clone(),
                    mode,
                    after: before,
                    after_mode: None,
                })
            })
            .collect()
    }

    /// Writes changes under a journal, then records set as rec (Some(None): removed; None: its
    /// record left as it is) and drops the images kept for the targets written: the targets
    /// written. On a failure, puts back what it
    /// wrote, except what another program changed meanwhile, whose original is kept.
    fn run(
        &self,
        op: &str,
        set: &str,
        changes: Vec<Change>,
        rec: Option<Option<Value>>,
        written: &mut dyn FnMut(usize),
    ) -> Result<Vec<Value>, Error> {
        let changes: Vec<Change> = changes
            .into_iter()
            .filter(|c| c.after != c.before)
            .collect();
        let changed: Vec<Value> = changes.iter().map(|c| json!(c.target.pin.target)).collect();
        let mut state = self.load()?;
        if changes.is_empty() {
            if let Some(rec) = rec {
                set_record(&mut state, set, rec);
            }
            self.save(&state)?;
            self.sweep(&state);
            return Ok(changed);
        }
        let mut files = Vec::new();
        for c in &changes {
            files.push(json!({
                "pin": c.target.pin.to_json(),
                "before": sha256_of(c.before.as_deref()),
                "after": sha256_of(c.after.as_deref()),
                "mode": c.mode,
                "backup": self.put_image("before", c.before.as_deref())?,
                "intended": self.put_image("intended", c.after.as_deref())?,
            }));
        }
        let mut intent = json!({"kind": KIND, "op": op, "set": set, "files": files});
        if let Some(rec) = rec {
            intent["record"] = json!(rec);
        }
        if intent.to_string().len() > 16 << 10 {
            self.sweep(&state);
            return Err(Error::invalid_request(
                "the operation names too much for one journal",
            ));
        }
        let journal = self
            .vault
            .vault("journal.begin", fields(json!({"intent": intent})))?["journal"]
            .clone();
        let mut done: Vec<(usize, Option<Id>)> = Vec::new();
        let mut failure = None;
        for (i, c) in changes.iter().enumerate() {
            let from = sha256_of(c.before.as_deref());
            match c.target.replace(
                Some(c.target.pin.file_id),
                &from,
                c.after.as_deref(),
                c.after_mode,
            ) {
                Ok(id) => {
                    done.push((i, id));
                    written(i);
                }
                Err(f) => {
                    if let Some(id) = f.written {
                        done.push((i, id));
                    }
                    failure = Some((c.target.pin.target.clone(), f.error));
                    break;
                }
            }
        }
        // The whole operation, checked once more at its end.
        if failure.is_none() {
            for (i, id) in &done {
                let c = &changes[*i];
                let ok = c.target.check_place().is_ok()
                    && c.target.read_now().is_ok_and(|(now, b)| {
                        now == *id && sha256_of(b.as_deref()) == sha256_of(c.after.as_deref())
                    });
                if !ok {
                    failure = Some((
                        c.target.pin.target.clone(),
                        Error::invalid_request(
                            "another program changed it before the operation ended",
                        ),
                    ));
                    break;
                }
            }
        }
        if failure.is_none() {
            finish(&mut state, &intent, &journal);
            if let Err(e) = self.save(&state) {
                failure = Some((self.data.join(STATE), e));
            }
        }
        if let Some((at, e)) = failure {
            return Err(self.roll_back(&journal, &intent, &changes, &done, &at, e));
        }
        // The write took effect even if the vault cannot record it now: the state file names
        // the journal, so the next start commits it.
        let _ = self
            .vault
            .vault("journal.commit", fields(json!({"journal": journal})));
        self.sweep(&state);
        Ok(changed)
    }

    /// Puts back the files done wrote, newest first, except those another program changed since,
    /// whose originals are kept; ends the journal as rolled back unless a file could not be put
    /// back (then the journal stays open, its backups kept, for the next start). The error to end
    /// the call with.
    fn roll_back(
        &self,
        journal: &Value,
        intent: &Value,
        changes: &[Change],
        done: &[(usize, Option<Id>)],
        at: &Path,
        e: Error,
    ) -> Error {
        let (mut kept_at, mut failed) = (Vec::new(), Vec::new());
        let set = intent["set"].as_str().unwrap_or_default();
        let mut state = self.load().ok();
        for (i, id) in done.iter().rev() {
            let c = &changes[*i];
            let f = &intent["files"][*i];
            let after = sha256_of(c.after.as_deref());
            let ours = c.target.check_place().is_ok()
                && c.target
                    .read_now()
                    .is_ok_and(|(now, b)| now == *id && sha256_of(b.as_deref()) == after);
            if !ours {
                kept_at.push(c.target.pin.target.display().to_string());
                if let Some(state) = state.as_mut() {
                    let k = kept(
                        set,
                        &place(&c.target.pin),
                        &f["backup"],
                        &f["mode"],
                        &f["intended"],
                        "conflict",
                    );
                    remember_all(state, "kept", k);
                }
            } else if let Err(f) = c
                .target
                .replace(Some(*id), &after, c.before.as_deref(), c.mode)
            {
                failed.push(format!(
                    "{}: {}",
                    c.target.pin.target.display(),
                    f.error.message
                ));
            }
        }
        if !failed.is_empty() {
            if let Some(state) = state.as_mut() {
                let backups: Vec<Value> = intent["files"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .flat_map(|f| [f["backup"].clone(), f["intended"].clone()])
                    .collect();
                remember_all(
                    state,
                    "open",
                    json!({"journal": journal, "backups": backups}),
                );
            }
        }
        if let Some(state) = &state {
            let _ = self.save(state);
        }
        let mut msg = format!("writing {} failed ({}); ", at.display(), e.message);
        if failed.is_empty() {
            let _ = self.vault.vault(
                "journal.end",
                fields(json!({"journal": journal, "outcome": "rolled_back"})),
            );
            msg.push_str("this change was rolled back");
            if !kept_at.is_empty() {
                msg.push_str(&format!(
                    ", except {}, which another program changed meanwhile and is left as it is (restore still has its original)",
                    kept_at.join(", ")
                ));
            }
        } else {
            msg.push_str(&format!(
                "putting back what was written failed too ({}); the tool's next start reconciles it",
                failed.join("; ")
            ));
        }
        if let Some(state) = &state {
            self.sweep(state);
        }
        Error::new(&e.code, msg)
    }

    /// Reconciles the open journals the host named in describe, from what is on disk at each
    /// journalled target (whatever its selection resolves to now): a write that finished is
    /// committed (its record saved); anything else is rolled back from its backups, except files
    /// another program changed since (or whose backup is gone), which are left as they are, their
    /// originals kept for restore. Temporary files a write left beside a target are removed.
    /// Never writes the intended bytes again. Idempotent: delivery may repeat. The reports, which
    /// `status` shows too.
    pub fn reconcile(&self, journals: &[Value]) -> Result<Vec<Value>, Error> {
        let mut reports = Vec::new();
        for j in journals {
            let id = j["id"].clone();
            let intent = &j["intent"];
            if intent["kind"] != KIND {
                return Err(Error::internal(format!(
                    "journal {id} is not one this tool began"
                )));
            }
            let set = intent["set"].as_str().unwrap_or_default();
            let mut state = self.load()?;
            let (mut restored, mut conflicts) = (Vec::new(), Vec::new());
            let committed = state["committed"]
                .as_array()
                .is_some_and(|a| a.contains(&id));
            let files = intent["files"]
                .as_array()
                .map(Vec::as_slice)
                .unwrap_or_default();
            let mut now = Vec::new();
            for f in files {
                let pin = Pin::from_json(&f["pin"])?;
                let t = Target::pinned(&pin).ok();
                if let Some(t) = &t {
                    let _ = t.remove_temps();
                }
                now.push((pin, t));
            }
            let finished = committed
                || now.iter().zip(files).all(|((_, t), f)| {
                    t.as_ref()
                        .is_some_and(|t| json!(t.pin.sha256) == f["after"])
                });
            let outcome = if finished {
                if !committed {
                    finish(&mut state, intent, &id);
                }
                "committed"
            } else {
                for ((pin, t), f) in now.iter().zip(files) {
                    let at = json!(pin.target);
                    let mut keep = || {
                        let k = kept(
                            set,
                            &place(pin),
                            &f["backup"],
                            &f["mode"],
                            &f["intended"],
                            "conflict",
                        );
                        remember_all(&mut state, "kept", k);
                        conflicts.push(at.clone());
                    };
                    let Some(t) = t else {
                        keep();
                        continue;
                    };
                    let sha = json!(t.pin.sha256);
                    if sha == f["before"] {
                        continue;
                    }
                    if sha != f["after"] {
                        keep();
                        continue;
                    }
                    let before = match self.get(&f["backup"]) {
                        Ok(b) => b,
                        Err(e) if e.code == "not_found" => {
                            keep();
                            continue;
                        }
                        Err(e) => return Err(e),
                    };
                    let mode = f["mode"].as_u64().map(|m| m as u32);
                    t.replace(None, &t.pin.sha256, before.as_deref(), mode)
                        .map_err(|f| f.error)?;
                    restored.push(at);
                }
                "rolled_back"
            };
            let report = json!({"journal": id, "op": intent["op"], "set": set,
                "outcome": outcome, "restored": restored, "conflicts": conflicts});
            for key in ["recovered", "open"] {
                if let Some(a) = state[key].as_array_mut() {
                    a.retain(|r| r["journal"] != id);
                }
            }
            remember(&mut state, "recovered", report.clone());
            self.save(&state)?;
            self.vault.vault(
                "journal.end",
                fields(json!({"journal": id, "outcome": outcome})),
            )?;
            reports.push(report);
        }
        Ok(reports)
    }

    /// The sets applied now (each with its targets), the originals kept for restore and the
    /// latest recovery reports.
    pub fn status(&self) -> Result<Value, Error> {
        let state = self.load()?;
        let mut sets = Map::new();
        for (name, rec) in state["sets"].as_object().into_iter().flatten() {
            let targets: Vec<Value> = rec["targets"]
                .as_array()
                .into_iter()
                .flatten()
                .map(|t| json!({"selected": t["selected"], "path": t["path"], "target": t["target"]}))
                .collect();
            sets.insert(name.clone(), json!({"targets": targets}));
        }
        let kept: Vec<Value> = state["kept"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|k| json!({"set": k["set"], "target": k["target"], "why": k["why"]}))
            .collect();
        Ok(json!({"sets": sets, "kept": kept, "recovered": state["recovered"]}))
    }
}

/// Refuses two pins of one target.
fn distinct(pins: &[Pin]) -> Result<(), Error> {
    for (i, p) in pins.iter().enumerate() {
        if pins[..i].iter().any(|q| q.target == p.target) {
            return Err(Error::invalid_request(format!(
                "{} is named twice",
                p.target.display()
            )));
        }
    }
    Ok(())
}

/// A finished operation in state: its set's record saved, removed (`record` null) or left as it
/// is (no `record`), the images kept for the targets it wrote in that set dropped, and its
/// journal remembered as committed.
fn finish(state: &mut Value, intent: &Value, journal: &Value) {
    let set = intent["set"].as_str().unwrap_or_default();
    if let Some(rec) = intent.get("record") {
        set_record(state, set, (!rec.is_null()).then(|| rec.clone()));
    }
    let written: Vec<Value> = intent["files"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|f| f["pin"]["target"].clone())
        .collect();
    if let Some(a) = state["kept"].as_array_mut() {
        a.retain(|k| !(k["set"] == set && written.contains(&k["target"])));
    }
    remember(state, "committed", journal.clone());
}

fn set_record(state: &mut Value, set: &str, rec: Option<Value>) {
    let sets = state["sets"].as_object_mut();
    if let Some(sets) = sets {
        match rec {
            Some(r) => {
                sets.insert(set.to_string(), r);
            }
            None => {
                sets.remove(set);
            }
        }
    }
}

/// Appends v to the list key of state, keeping the last KEEP.
fn remember(state: &mut Value, key: &str, v: Value) {
    remember_all(state, key, v);
    let a = state[key].as_array_mut().expect("an array");
    let over = a.len().saturating_sub(KEEP);
    a.drain(..over);
}

/// Appends v to the list key of state, keeping all (what restore or reconciliation needs).
fn remember_all(state: &mut Value, key: &str, v: Value) {
    if !state[key].is_array() {
        state[key] = json!([]);
    }
    state[key].as_array_mut().expect("an array").push(v);
}
