//! Manual backups: a writer's backup browser ([`Transactions::backup_create`],
//! [`Transactions::backup_list`], [`Transactions::backup_diff`], [`Transactions::backup_restore`],
//! [`Transactions::backup_delete`] and [`backup_call`], which serves them as the standard
//! operations every writer exports).
//!
//! A manual backup is one version of a set's files: for each pinned target, its bytes then (an
//! empty image when the file did not exist) as a vault backup labelled `bk <set>`, and a record in
//! the state file with the selection, the target, whether the file existed, its size and its
//! permission bits; never its bytes or a hash of them. The version's ID is its first file's
//! vault ID. A set keeps at most [`KEEP_BACKUPS`] versions: saving another deletes the oldest.
//! Retention never touches the library's own backups (`tx `), which `sweep` keeps while a set's
//! record, a kept original or an open journal names them.
//!
//! A diff compares a version with the files now, inside the tool: the writer reads both images
//! into settings ([`Leaves`]), and the diff gives only each changed setting's path and what a
//! restore would do to it, never a value. Restoring a version writes its files back through a
//! journal like any write (a file it did not have is removed), makes no automatic backup, leaves
//! the set's record as it is and keeps the originals kept for restore.

use crate::{b64, Pin, Selection, Target};
use crate::{check_set, fields, remember_all, Change, Transactions, MAX_TARGETS};
use pluxy_program::{Call, Error};
use serde_json::{json, Value};
use std::path::PathBuf;

/// The manual backups a set keeps.
pub const KEEP_BACKUPS: usize = 10;

/// Manual backups are labelled with this prefix in the vault.
pub(crate) const MANUAL: &str = "bk ";

/// The changed settings a diff lists for one file; the rest are counted in `more`.
const DIFF_MAX: usize = 200;

/// A writer's reading of one of its files for a diff: each setting's path and value, in the
/// file's order; None when the bytes are not a file it reads. Values never leave the tool.
pub type Leaves<'a> = dyn Fn(&[u8]) -> Option<Vec<(Vec<String>, Value)>> + 'a;

/// A JSON file's settings: each object's members by key, down to values that are not objects (an
/// array is one setting), and an empty object as one setting. An empty file has none.
pub fn json_leaves(bytes: &[u8]) -> Option<Vec<(Vec<String>, Value)>> {
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Some(Vec::new());
    }
    let v: Value = serde_json::from_slice(bytes).ok()?;
    let mut out = Vec::new();
    walk(&mut Vec::new(), &v, &mut out);
    Some(out)
}

fn walk(path: &mut Vec<String>, v: &Value, out: &mut Vec<(Vec<String>, Value)>) {
    match v {
        Value::Object(m) if !m.is_empty() => {
            for (k, x) in m {
                path.push(k.clone());
                walk(path, x, out);
                path.pop();
            }
        }
        _ => out.push((path.clone(), v.clone())),
    }
}

/// What a restore of before (the backup) over now does to each setting: adds what only the backup
/// has, changes what both have with other values, removes what only the file now has, in the
/// backup's order and then the file's. At most DIFF_MAX, and how many more.
fn changes(before: &[(Vec<String>, Value)], now: &[(Vec<String>, Value)]) -> (Vec<Value>, usize) {
    let mut all = Vec::new();
    for (p, v) in before {
        match now.iter().find(|(q, _)| q == p) {
            None => all.push(json!({"path": p, "restore": "adds"})),
            Some((_, w)) if w != v => all.push(json!({"path": p, "restore": "changes"})),
            Some(_) => {}
        }
    }
    for (q, _) in now {
        if !before.iter().any(|(p, _)| p == q) {
            all.push(json!({"path": q, "restore": "removes"}));
        }
    }
    let more = all.len().saturating_sub(DIFF_MAX);
    all.truncate(DIFF_MAX);
    (all, more)
}

/// The selection a record of a target names (`selected` and, in a root, `path`).
pub(crate) fn selection_of(t: &Value) -> Option<Selection> {
    let selected = PathBuf::from(t["selected"].as_str()?);
    Some(match t["path"].as_str() {
        Some(path) => Selection::Root {
            root: selected,
            path: path.into(),
        },
        None => Selection::File(selected),
    })
}

fn now_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
}

fn no_backup(set: &str, id: &str) -> Error {
    Error::not_found(format!("{id} is no manual backup of {set}"))
}

impl Transactions<'_> {
    /// The version id of set, from state.
    fn version(&self, state: &Value, set: &str, id: &str) -> Result<Value, Error> {
        state["backups"]
            .as_array()
            .into_iter()
            .flatten()
            .find(|b| b["set"] == set && b["id"] == id)
            .cloned()
            .ok_or_else(|| no_backup(set, id))
    }

    /// Keeps the pinned targets' files as they are now as a new manual backup of set, then
    /// deletes the oldest versions over [`KEEP_BACKUPS`]: `{"backup", "deleted": [<id>]}`.
    pub fn backup_create(&self, set: &str, pins: &[Pin]) -> Result<Value, Error> {
        check_set(set)?;
        if pins.is_empty() || pins.len() > MAX_TARGETS {
            return Err(Error::invalid_request(format!(
                "a backup has 1 to {MAX_TARGETS} files"
            )));
        }
        crate::distinct(pins)?;
        let mut state = self.load()?;
        let mut files = Vec::new();
        let mut put = Vec::new();
        let label = format!("{MANUAL}{set}");
        let result = (|| -> Result<(), Error> {
            for p in pins {
                let t = Target::open(p)?;
                let (_, bytes) = t.read_now()?;
                let mode = t.mode_now()?;
                let data = bytes.as_deref().unwrap_or_default();
                let v = self.vault.vault(
                    "backup.put",
                    fields(json!({"label": label, "data": b64::encode(data)})),
                )?;
                let id = v["backup"]
                    .as_str()
                    .ok_or_else(|| Error::internal("the vault gave no backup ID"))?
                    .to_string();
                put.push(id.clone());
                let mut f = crate::place(p);
                f["backup"] = json!(id);
                f["exists"] = json!(bytes.is_some());
                f["size"] = json!(data.len());
                f["mode"] = json!(mode);
                files.push(f);
            }
            Ok(())
        })();
        if let Err(e) = result {
            for id in put {
                let _ = self
                    .vault
                    .vault("backup.delete", fields(json!({"backup": id})));
            }
            return Err(e);
        }
        let id = files[0]["backup"].clone();
        remember_all(
            &mut state,
            "backups",
            json!({"id": id, "set": set, "created": now_ms(), "files": files}),
        );
        // The oldest versions of set over the limit, by the order they were saved in.
        let mine: Vec<Value> = state["backups"]
            .as_array()
            .into_iter()
            .flatten()
            .filter(|b| b["set"] == set)
            .cloned()
            .collect();
        let over: Vec<Value> = mine[..mine.len().saturating_sub(KEEP_BACKUPS)].to_vec();
        if let Some(a) = state["backups"].as_array_mut() {
            a.retain(|b| !over.iter().any(|o| o["id"] == b["id"] && b["set"] == set));
        }
        self.save(&state)?;
        // A vault backup whose removal fails here is unnamed now: the next sweep removes it.
        for o in &over {
            for f in o["files"].as_array().into_iter().flatten() {
                let _ = self
                    .vault
                    .vault("backup.delete", fields(json!({"backup": f["backup"]})));
            }
        }
        let deleted: Vec<Value> = over.iter().map(|o| o["id"].clone()).collect();
        Ok(json!({"backup": id, "deleted": deleted}))
    }

    /// Set's manual backups, newest first: each version's ID, when it was saved and, for each of
    /// its files, the selection, the target, whether it existed and its size. Never their bytes.
    pub fn backup_list(&self, set: &str) -> Result<Value, Error> {
        check_set(set)?;
        let state = self.load()?;
        let list: Vec<Value> = state["backups"]
            .as_array()
            .into_iter()
            .flatten()
            .rev()
            .filter(|b| b["set"] == set)
            .map(|b| {
                let files: Vec<Value> = b["files"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .map(|f| {
                        json!({"selected": f["selected"], "path": f["path"], "target": f["target"],
                            "exists": f["exists"], "size": f["size"]})
                    })
                    .collect();
                json!({"id": b["id"], "created": b["created"], "files": files})
            })
            .collect();
        Ok(json!({"backups": list, "keep": KEEP_BACKUPS}))
    }

    /// What restoring set's backup id would change in each of its files, as they are now at its
    /// selection: whether the file existed then and exists now, whether it is the same, and, when
    /// leaves reads both, each changed setting's path and what the restore does to it. Never a
    /// value. A selection that resolves to another file now is refused.
    pub fn backup_diff(&self, set: &str, id: &str, leaves: &Leaves<'_>) -> Result<Value, Error> {
        check_set(set)?;
        let state = self.load()?;
        let b = self.version(&state, set, id)?;
        let mut out = Vec::new();
        let mut all_same = true;
        for f in b["files"].as_array().into_iter().flatten() {
            let sel = selection_of(f).ok_or_else(|| Error::internal("a backup names no file"))?;
            let t = Target::resolve(&sel)?;
            if json!(t.pin.target) != f["target"] {
                return Err(Error::invalid_request(format!(
                    "{} resolves to {} now, not to {}, the file this backup is of",
                    f["selected"].as_str().unwrap_or_default(),
                    t.pin.target.display(),
                    f["target"].as_str().unwrap_or_default()
                )));
            }
            let (_, now) = t.read_now()?;
            let then = if f["exists"] == true {
                self.get(&f["backup"])?
            } else {
                None
            };
            let same = then == now;
            all_same &= same;
            let read = |image: &Option<Vec<u8>>| match image {
                None => Some(Vec::new()),
                Some(bytes) => leaves(bytes),
            };
            let (a, z) = (read(&then), read(&now));
            let readable = a.is_some() && z.is_some();
            let (list, more) = match (&a, &z) {
                (Some(a), Some(z)) => changes(a, z),
                _ => (Vec::new(), 0),
            };
            out.push(json!({"target": f["target"], "exists": then.is_some(),
                "now_exists": now.is_some(), "same": same, "readable": readable,
                "changes": list, "more": more}));
        }
        Ok(json!({"backup": id, "same": all_same, "files": out}))
    }

    /// Puts set's backup id back in its files, pinned by pins (one for each of its files, in its
    /// order): each gets the bytes it had then, and one that did not exist is removed. Journalled
    /// like every write; no automatic backup is made; set's record and the originals kept for
    /// restore stay as they are.
    pub fn backup_restore(
        &self,
        set: &str,
        pins: &[Pin],
        id: &str,
        written: &mut dyn FnMut(usize),
    ) -> Result<Value, Error> {
        check_set(set)?;
        let state = self.load()?;
        let b = self.version(&state, set, id)?;
        let files = b["files"].as_array().cloned().unwrap_or_default();
        let targets: Vec<Value> = files.iter().map(|f| f["target"].clone()).collect();
        let pinned: Vec<Value> = pins.iter().map(|p| json!(p.target)).collect();
        if targets != pinned {
            return Err(Error::invalid_request(format!(
                "{id} is a backup of {}, not of {}",
                Value::Array(targets),
                Value::Array(pinned)
            )));
        }
        let mut changes: Vec<Change> = self.open(set, pins)?;
        for (c, f) in changes.iter_mut().zip(&files) {
            c.after = if f["exists"] == true {
                Some(self.get(&f["backup"])?.unwrap_or_default())
            } else {
                None
            };
            c.after_mode = f["mode"].as_u64().map(|m| m as u32);
        }
        self.run(crate::RESTORE_BACKUP, set, changes, None, written)
            .map(|changed| json!({"outcome": "restored", "changed": changed}))
    }

    /// Deletes set's backup id, from the state file and the vault: `{"deleted": <id>}`.
    pub fn backup_delete(&self, set: &str, id: &str) -> Result<Value, Error> {
        check_set(set)?;
        let mut state = self.load()?;
        let b = self.version(&state, set, id)?;
        if let Some(a) = state["backups"].as_array_mut() {
            a.retain(|x| !(x["set"] == set && x["id"] == id));
        }
        self.save(&state)?;
        // A vault backup whose removal fails here is unnamed now: the next sweep removes it.
        for f in b["files"].as_array().into_iter().flatten() {
            let _ = self
                .vault
                .vault("backup.delete", fields(json!({"backup": f["backup"]})));
        }
        Ok(json!({"deleted": id}))
    }
}

/// The standard backup operations of a writer for set, which every writer exports with the
/// schemas of [`backup_operations`]: `backup-list`, `backup-diff`, `backup-create`,
/// `backup-restore` and `backup-delete`. Some(answer) for one of them, None for any other op.
pub fn backup_call(
    call: &Call,
    t: &Transactions<'_>,
    set: &str,
    leaves: &Leaves<'_>,
) -> Option<Result<Value, Error>> {
    let input = &call.input;
    let id = || {
        input["backup"]
            .as_str()
            .ok_or_else(|| Error::invalid_request("backup names a backup ID"))
    };
    let pins = || -> Result<Vec<Pin>, Error> {
        input["targets"]
            .as_array()
            .ok_or_else(|| Error::invalid_request("targets lists the pinned files"))?
            .iter()
            .map(Pin::from_json)
            .collect()
    };
    Some(match call.op.as_str() {
        "backup-list" => t.backup_list(set),
        "backup-diff" => id().and_then(|id| t.backup_diff(set, id, leaves)),
        "backup-create" => pins().and_then(|p| t.backup_create(set, &p)),
        "backup-restore" => {
            id().and_then(|id| pins().and_then(|p| t.backup_restore(set, &p, id, &mut |_| {})))
        }
        "backup-delete" => id().and_then(|id| t.backup_delete(set, id)),
        _ => return None,
    })
}

/// The manifest entries of the standard backup operations of a writer whose writes take
/// files targets (1 to [`MAX_TARGETS`]), each pinned by [`crate::pin_schema`]. Writers list
/// exactly these in their manifest (a test of theirs compares).
pub fn backup_operations(files: usize) -> Vec<Value> {
    let files = files.clamp(1, MAX_TARGETS);
    let targets = json!({"type": "array", "minItems": files, "maxItems": files,
        "items": crate::pin_schema()});
    let id = json!({"type": "string", "minLength": 34, "maxLength": 34});
    let same = "Saving replaces the file; an edit made in the same instant by another program may be lost.";
    let op = |id_: &str, description: String, effect: &str, props: Value, required: &[&str]| {
        json!({"id": id_, "description": description, "effect": effect,
            "input_schema": {"type": "object", "properties": props, "required": required,
                "additionalProperties": false},
            "output_schema": {"type": "object"}})
    };
    vec![
        op(
            "backup-list",
            "List the manual backups: each one's ID, when it was saved, and its files' paths, presence and sizes".into(),
            "read",
            json!({}),
            &[],
        ),
        op(
            "backup-diff",
            "Show which settings restoring a manual backup would add, change or remove, by name only, never a value".into(),
            "read",
            json!({"backup": id}),
            &["backup"],
        ),
        op(
            "backup-create",
            format!("Manual Backup: keep the previewed files as they are on disk now (or that they do not exist) in the app's vault. The newest {KEEP_BACKUPS} manual backups are kept; saving another deletes the oldest."),
            "write",
            json!({"targets": targets}),
            &["targets"],
        ),
        op(
            "backup-restore",
            format!("Restore this version: put the manual backup's contents back in the previewed files, replacing what they hold now; files absent in the backup are removed. No automatic backup is created. {same}"),
            "write",
            json!({"backup": id, "targets": targets}),
            &["backup", "targets"],
        ),
        op(
            "backup-delete",
            "Delete this version: delete only this manual backup. The current configuration and other backups are unchanged.".into(),
            "write",
            json!({"backup": id}),
            &["backup"],
        ),
    ]
}
