//! The `backups` step of the Claude Code native check (`claude_code_native.rs`): the backup browser
//! of pluxy/claude-code, through the window's own `program_call`, its real native confirmations
//! and the host's vault, on the files `native/claude-code.ts` made in this run's HOME. It runs after
//! the `root` step, so the settings file holds the cleared bytes, the root's file is absent and
//! the set is applied nowhere. Each refusal has a positive control on the same route:
//!
//! - list: no manual backup yet, answered without asking;
//! - create: a cancelled Manual Backup keeps nothing; a confirmed one keeps one, listed with its
//!   ID, file, presence and size only;
//! - diff: unchanged, then after the person's edits, the changed settings by name, never a value;
//! - restore: a cancelled restore writes nothing; a confirmed one, naming the backup and saying no
//!   automatic backup is made, puts the backed-up bytes back over the person's edits;
//! - foreign: an ID that is no manual backup of this tool is refused by diff, restore and delete,
//!   among them a backup the transaction library keeps for an applied set (read from the vault's
//!   records on disk), which stays in the vault; a backup restored onto another file is refused;
//! - absence: a backup of a file that does not exist lists as absent; restoring it, after an
//!   apply created the file, removes the file again;
//! - retention: with ten manual backups kept, Manual Backup deletes the oldest manual one, from
//!   the list and the vault, and none the applied set's record names;
//! - delete: a cancelled delete keeps the backup; a confirmed one removes it from the list and
//!   the vault;
//! - own data: no answer of the step carries a value of the files, the key or the login canary,
//!   and the tool's own data folder holds none of them.

use super::admission::{press, require, settle, start};
use super::claude_code::{confirmed, edit, mode, read_op, read_refused, written, Seen};
use super::transactions::{link, read};
use super::vault::dialog_with;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use tauri::{AppHandle, Manager, Webview};

const PACKAGE: &str = "pluxy/claude-code";
/// How many manual backups the library keeps of a set.
const KEEP: usize = 10;
const NO_AUTOMATIC: &str = "No automatic backup is created";

/// What the step needs from the Claude Code check.
pub(super) struct Ctx<'a> {
    pub app: &'a AppHandle,
    pub main: &'a Webview,
    pub seen: &'a Seen,
    /// The key and the login canary.
    pub hidden: &'a [&'a str],
    /// expect.json.
    pub x: &'a Value,
    pub key: &'a str,
    /// The key's vault secret ID.
    pub secret: &'a str,
}

/// The vault's records of the package, read from disk: each backup's ID and label.
fn vault_backups(app: &AppHandle) -> Result<Vec<(String, String)>, String> {
    let name: String = Sha256::digest(PACKAGE.as_bytes())
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    let index = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("vault")
        .join("packages")
        .join(name)
        .join("index.json");
    let v: Value = serde_json::from_slice(&std::fs::read(&index).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())?;
    Ok(v["backups"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|b| {
            (
                b["id"].as_str().unwrap_or_default().to_string(),
                b["label"].as_str().unwrap_or_default().to_string(),
            )
        })
        .collect())
}

fn in_vault(app: &AppHandle, id: &str) -> Result<bool, String> {
    Ok(vault_backups(app)?.iter().any(|(b, _)| b == id))
}

/// Every file under dir, with its bytes.
fn files(dir: &Path) -> Vec<(PathBuf, Vec<u8>)> {
    let mut out = Vec::new();
    let mut todo = vec![dir.to_path_buf()];
    while let Some(d) = todo.pop() {
        for e in std::fs::read_dir(&d).into_iter().flatten().flatten() {
            let p = e.path();
            match e.file_type() {
                Ok(t) if t.is_dir() => todo.push(p),
                Ok(t) if t.is_file() => {
                    out.push((p.clone(), std::fs::read(&p).unwrap_or_default()))
                }
                _ => {}
            }
        }
    }
    out
}

pub(super) fn run(c: &Ctx) -> Result<(), String> {
    let (app, main, seen, x) = (c.app, c.main, c.seen, c.x);
    let s = |v: &Value| v.as_str().unwrap_or_default().to_string();
    let p = |v: &Value| PathBuf::from(s(v));
    let txt = |q: &Path| q.display().to_string();
    let bytes = |k: &str| s(&x["bytes"][k]).replace("@KEY@", c.key);
    let settings = p(&x["settings"]);
    let (root_link, root_text, root_file) = (
        p(&x["root"]["link"]),
        s(&x["root"]["text"]),
        p(&x["root"]["file"]),
    );
    let (root_inode, _) = link(&root_link)?;
    let b = &x["backups"];
    // Values of the files no answer may carry: the person's own API key and the edits' canaries.
    let values: Vec<String> = b["values"]
        .as_array()
        .into_iter()
        .flatten()
        .map(s)
        .chain(c.hidden.iter().map(|h| h.to_string()))
        .collect();
    let mut answers: Vec<String> = Vec::new();
    let mut note = |v: &Value| answers.push(v.to_string());
    let file = |q: &Path| json!({"targets": [{"file": q}]});
    let root_sel = json!({"targets": [{"root": root_link, "path": "settings.json"}]});
    let preview = |sel: &Value| read_op(main, seen, "preview", sel.clone());
    let list = |note: &mut dyn FnMut(&Value)| -> Result<Vec<Value>, String> {
        let v = read_op(main, seen, "backup-list", json!({}))?;
        note(&v);
        require(v["keep"] == KEEP, format!("the list's keep: {v}"))?;
        Ok(v["backups"].as_array().cloned().unwrap_or_default())
    };
    let ids = |l: &[Value]| l.iter().map(|e| s(&e["id"])).collect::<Vec<_>>();
    let diff = |id: &str, note: &mut dyn FnMut(&Value)| -> Result<Value, String> {
        let v = read_op(main, seen, "backup-diff", json!({"backup": id}))?;
        note(&v);
        Ok(v)
    };
    // A confirmed Manual Backup of the file sel previews: its output.
    let create = |sel: &Value, at: &Path, note: &mut dyn FnMut(&Value)| -> Result<Value, String> {
        let v = preview(sel)?;
        let o = written(
            app,
            main,
            seen,
            "backup-create",
            json!({"targets": v["targets"]}),
            &[&txt(at), "newest 10"],
            c.hidden,
        )?;
        note(&o);
        require(
            o["backup"].as_str().is_some_and(|i| i.starts_with("b-")),
            format!("the new backup: {o}"),
        )?;
        Ok(o)
    };
    // A native confirmation of a write, cancelled: the window's answer.
    let cancelled = |op: &str, input: Value, needles: &[&str]| -> Result<Value, String> {
        let slot = start(
            main,
            "program_call",
            json!({"package": PACKAGE, "op": op, "input": input}),
        )?;
        let what = format!("{PACKAGE} {op}");
        let mut all = vec![what.as_str()];
        all.extend_from_slice(needles);
        let text = dialog_with(app, &all)?;
        seen.add(text);
        press("alt+c")?;
        let r = settle(main, &slot)?;
        seen.add(r.to_string());
        require(
            r["ok"] == false,
            format!("a cancelled {op} went ahead: {r}"),
        )?;
        Ok(r)
    };

    // ---- list: nothing yet, answered without asking
    require(
        list(&mut note)?.is_empty(),
        "a manual backup exists already",
    )?;
    require(
        read(&settings) == Some(bytes("cleared")),
        "the settings do not hold the cleared bytes",
    )?;

    // ---- create: cancelled keeps nothing; confirmed keeps one, listed by ID only
    let v = preview(&file(&settings))?;
    cancelled(
        "backup-create",
        json!({"targets": v["targets"]}),
        &[&txt(&settings)],
    )?;
    require(
        list(&mut note)?.is_empty(),
        "a cancelled Manual Backup kept one",
    )?;
    let first = s(&create(&file(&settings), &settings, &mut note)?["backup"]);
    let l = list(&mut note)?;
    require(
        l.len() == 1
            && l[0]["id"] == json!(first)
            && l[0]["files"]
                == json!([{"selected": txt(&settings), "path": null, "target": txt(&settings),
                    "exists": true, "size": bytes("cleared").len()}]),
        format!("the list after Manual Backup: {l:?}"),
    )?;
    require(in_vault(app, &first)?, "the backup is not in the vault")?;

    // ---- diff: unchanged, then the person's edits by setting name, never a value
    let d = diff(&first, &mut note)?;
    require(
        d["same"] == true && d["files"][0]["changes"] == json!([]),
        format!("the diff of an unchanged file: {d}"),
    )?;
    for e in b["edits"].as_array().into_iter().flatten() {
        edit(&settings, &s(&e[0]), &s(&e[1]))?;
    }
    let edited = read(&settings).unwrap_or_default();
    let d = diff(&first, &mut note)?;
    require(
        d["same"] == false
            && d["files"].as_array().map(Vec::len) == Some(1)
            && d["files"][0]["target"] == json!(txt(&settings))
            && d["files"][0]["exists"] == true
            && d["files"][0]["now_exists"] == true
            && d["files"][0]["readable"] == true
            && d["files"][0]["changes"] == b["diff"]
            && d["files"][0]["more"] == 0,
        format!("the diff after the person's edits: {d}"),
    )?;

    // ---- restore: cancelled writes nothing; confirmed puts the backup back over the edits
    let v = preview(&file(&settings))?;
    let input = json!({"backup": first, "targets": v["targets"]});
    cancelled(
        "backup-restore",
        input.clone(),
        &[&txt(&settings), &first, NO_AUTOMATIC],
    )?;
    require(
        read(&settings) == Some(edited.clone()),
        "a cancelled restore changed the settings",
    )?;
    let o = written(
        app,
        main,
        seen,
        "backup-restore",
        input,
        &[&txt(&settings), &first, NO_AUTOMATIC],
        c.hidden,
    )?;
    note(&o);
    require(
        read(&settings) == Some(bytes("cleared")) && mode(&settings) == 0o644,
        format!("the settings after the restore: {:?}", read(&settings)),
    )?;
    require(
        diff(&first, &mut note)?["same"] == true,
        "the restored file differs from its backup",
    )?;

    // ---- foreign: an ID that is no manual backup, a restore onto another file
    let stranger = format!("b-{}", "0123456789abcdef".repeat(2));
    read_refused(
        main,
        seen,
        "backup-diff",
        json!({"backup": stranger}),
        "no manual backup",
    )?;
    let v = preview(&root_sel)?;
    let r = confirmed(
        app,
        main,
        seen,
        "backup-restore",
        json!({"backup": first, "targets": v["targets"]}),
        &[&first],
        c.hidden,
    )?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("backup of")),
        format!("a backup restored onto another file should be refused: {r}"),
    )?;
    require(!root_file.exists(), "the refused restore created a file")?;

    // ---- absence: a backup of a file that does not exist
    let absent = s(&create(&root_sel, &root_file, &mut note)?["backup"]);
    let l = list(&mut note)?;
    require(
        ids(&l) == vec![absent.clone(), first.clone()]
            && l[0]["files"][0]["exists"] == false
            && l[0]["files"][0]["size"] == 0
            && l[0]["files"][0]["selected"] == json!(txt(&root_link))
            && l[0]["files"][0]["target"] == json!(txt(&root_file)),
        format!("the list with an absent file's backup: {l:?}"),
    )?;
    // Applied, the root's file exists; the set's record names the library's own backups.
    let v = preview(&root_sel)?;
    let mut input = x["settings_input"].clone();
    input["targets"] = v["targets"].clone();
    input["key"] = json!(c.secret);
    written(
        app,
        main,
        seen,
        "apply",
        input,
        &[&txt(&root_link)],
        c.hidden,
    )?;
    require(
        read(&root_file) == Some(bytes("root_applied")),
        "the root's file after apply",
    )?;
    let library: Vec<String> = vault_backups(app)?
        .into_iter()
        .filter(|(_, label)| label.starts_with("tx "))
        .map(|(id, _)| id)
        .collect();
    require(
        !library.is_empty(),
        "the applied set's record names no backup in the vault",
    )?;
    let theirs = &library[0];
    read_refused(
        main,
        seen,
        "backup-diff",
        json!({"backup": theirs}),
        "no manual backup",
    )?;
    let r = confirmed(
        app,
        main,
        seen,
        "backup-delete",
        json!({"backup": theirs}),
        &[theirs],
        c.hidden,
    )?;
    require(
        r["ok"] == false
            && r["error"]
                .as_str()
                .is_some_and(|e| e.contains("no manual backup")),
        format!("deleting the library's backup should be refused: {r}"),
    )?;
    require(
        in_vault(app, theirs)?,
        "the library's backup left the vault",
    )?;

    // ---- retention: the eleventh Manual Backup deletes the oldest manual one, nothing else
    for _ in 0..KEEP - 2 {
        create(&file(&settings), &settings, &mut note)?;
    }
    let l = list(&mut note)?;
    require(
        l.len() == KEEP && ids(&l).last() == Some(&first),
        format!("the list with ten backups: {:?}", ids(&l)),
    )?;
    let o = create(&file(&settings), &settings, &mut note)?;
    require(
        o["deleted"] == json!([first]),
        format!("the eleventh Manual Backup: {o}"),
    )?;
    let l = list(&mut note)?;
    require(
        l.len() == KEEP && !ids(&l).contains(&first) && ids(&l).contains(&absent),
        format!("the list after retention: {:?}", ids(&l)),
    )?;
    require(
        !in_vault(app, &first)?,
        "the oldest backup is still in the vault",
    )?;
    for id in &library {
        require(
            in_vault(app, id)?,
            "retention deleted a backup the applied set's record names",
        )?;
    }

    // ---- absence restored: the file the apply created is removed again
    let d = diff(&absent, &mut note)?;
    require(
        d["same"] == false
            && d["files"][0]["exists"] == false
            && d["files"][0]["now_exists"] == true,
        format!("the diff of an absent file's backup: {d}"),
    )?;
    let v = preview(&root_sel)?;
    written(
        app,
        main,
        seen,
        "backup-restore",
        json!({"backup": absent, "targets": v["targets"]}),
        &[&txt(&root_file), &absent],
        c.hidden,
    )?;
    require(
        !root_file.exists(),
        "restoring an absent file's backup left it",
    )?;
    require(
        link(&root_link)? == (root_inode, PathBuf::from(&root_text)),
        "the root link changed",
    )?;
    // Restore Backup of the original (absent too) ends the set's record.
    let v = preview(&root_sel)?;
    written(
        app,
        main,
        seen,
        "restore",
        json!({"targets": v["targets"]}),
        &[&txt(&root_link)],
        c.hidden,
    )?;
    require(!root_file.exists(), "the original's restore made a file")?;

    // ---- delete: cancelled keeps it; confirmed removes it from the list and the vault
    cancelled("backup-delete", json!({"backup": absent}), &[&absent])?;
    require(
        ids(&list(&mut note)?).contains(&absent),
        "a cancelled delete removed the backup",
    )?;
    let o = written(
        app,
        main,
        seen,
        "backup-delete",
        json!({"backup": absent}),
        &[&absent],
        c.hidden,
    )?;
    note(&o);
    require(
        !ids(&list(&mut note)?).contains(&absent) && !in_vault(app, &absent)?,
        "the deleted backup is still kept",
    )?;

    // ---- own data: no value of the files in an answer or the tool's own data folder
    require(!answers.is_empty(), "the step saw no answer")?;
    for a in &answers {
        if let Some(v) = values.iter().find(|v| a.contains(v.as_str())) {
            return Err(format!("an answer carries a file's value {v:?}: {a}"));
        }
    }
    let own = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("tools")
        .join("pluxy")
        .join("claude-code");
    let own_files = files(&own);
    require(!own_files.is_empty(), "the tool keeps no data at all")?;
    for (f, data) in own_files {
        let text = String::from_utf8_lossy(&data);
        if let Some(v) = values.iter().find(|v| text.contains(v.as_str())) {
            return Err(format!("{} holds a file's value {v:?}", f.display()));
        }
    }
    Ok(())
}
