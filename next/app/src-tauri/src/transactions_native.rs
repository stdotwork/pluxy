//! The native test's check of the transaction library (next/app-tools/lib/transactions) through
//! its fixture writer, pluxy/fixture-writer (native-test builds with `artifact-test-trust` only).
//! It drives the real app through the window's own commands and the real native confirmations,
//! with the writer program `native/transactions.ts` packed and the stow and chezmoi layouts it made
//! in this run's HOME:
//!
//! - install and enable: the writer installs from the curated registry and is enabled through its
//!   native confirmation;
//! - file link: a preview of the selected stow link (a read, asked nothing) names the link's text
//!   and the file it resolves to; an apply asks a native confirmation naming both, then changes
//!   only that file, to the bytes `expect.json` says, keeping its mode, and the link keeps its
//!   identity and text;
//! - root link: the same for two files in the selected chezmoi root (one missing, so created),
//!   the root link kept; a file inside the root that is a link, or a path leaving it, is refused,
//!   and the link's target is untouched;
//! - retarget: after a preview, the link pointed elsewhere, or the file it resolves to replaced,
//!   makes the confirmed apply fail as changed, writing nothing anywhere;
//! - competing edit: an edit between preview and apply makes it fail as modified, the edit kept;
//!   clear after the person's later edits removes only the writer's untouched value and keeps
//!   theirs;
//! - restore: puts the original bytes back (and removes the file the writer created), through
//!   the same link;
//! - partial rollback: an apply to two files whose second cannot be written puts the first back
//!   and fails saying so; its journal ends rolled back;
//! - crash recovery: a writer that exits after writing one of two files has an unknown outcome;
//!   its next start rolls the written file back (journal reconciled as rolled back), keeps a
//!   competing edit made after the crash instead of rolling over it, and commits a write that
//!   finished before the crash (which clear then undoes);
//! - audit: no temporary file is left, the files no step names keep their bytes, and once
//!   disabled no writer process is left.
//!
//! One line on stderr: `native-test: transactions <JSON>`.

use super::admission::{dialog_text, ok, press, require, settle, start};
use super::program::processes;
use super::routing::{call, enable, install, showing};
use super::{log, wait_for};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

/// Every entry under root, by its path: a file's mode and SHA-256, a link's inode and text, or a
/// folder (folder times change as files are written in them, so they are not compared).
fn snapshot(root: &Path) -> Result<BTreeMap<String, String>, String> {
    fn walk(dir: &Path, out: &mut BTreeMap<String, String>) -> Result<(), String> {
        for e in std::fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
            let path = e.map_err(|e| e.to_string())?.path();
            let m = std::fs::symlink_metadata(&path).map_err(|e| e.to_string())?;
            let what = if m.file_type().is_symlink() {
                let to = std::fs::read_link(&path).map_err(|e| e.to_string())?;
                format!("link {} {}", m.ino(), to.display())
            } else if m.is_dir() {
                walk(&path, out)?;
                "folder".to_string()
            } else {
                let bytes = std::fs::read(&path).map_err(|e| e.to_string())?;
                let sha: String = Sha256::digest(&bytes)
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect();
                format!("file {:o} {sha}", m.mode() & 0o7777)
            };
            out.insert(path.display().to_string(), what);
        }
        Ok(())
    }
    let mut out = BTreeMap::new();
    walk(root, &mut out)?;
    Ok(out)
}

/// The paths whose entries differ between two snapshots.
fn changed(a: &BTreeMap<String, String>, b: &BTreeMap<String, String>) -> Vec<String> {
    let mut keys: Vec<&String> = a.keys().chain(b.keys()).collect();
    keys.sort();
    keys.dedup();
    keys.into_iter()
        .filter(|k| a.get(*k) != b.get(*k))
        .cloned()
        .collect()
}

fn read(p: &Path) -> Option<String> {
    std::fs::read_to_string(p).ok()
}

/// A link's inode and text.
fn link(p: &Path) -> Result<(u64, PathBuf), String> {
    let m = std::fs::symlink_metadata(p).map_err(|e| format!("{}: {e}", p.display()))?;
    require(
        m.file_type().is_symlink(),
        format!("{} is no longer a link", p.display()),
    )?;
    Ok((m.ino(), std::fs::read_link(p).map_err(|e| e.to_string())?))
}

/// Points the link at to, as a config manager would: a new link renamed over it.
fn relink(at: &Path, to: &Path) -> Result<(), String> {
    let tmp = at.with_extension("relink");
    std::os::unix::fs::symlink(to, &tmp).map_err(|e| e.to_string())?;
    std::fs::rename(&tmp, at).map_err(|e| e.to_string())
}

/// The writer's read operation op, failing on an error or on a native confirmation.
fn read_op(main: &Webview, w: &str, op: &str, input: Value) -> Result<Value, String> {
    let (r, asked) = unasked(|| call(main, w, op, input.clone()));
    let r = r?;
    require(!asked, format!("{op} {input} asked a native confirmation"))?;
    require(r["ok"] == true, format!("{op} {input}: {r}"))?;
    Ok(r["value"].clone())
}

/// A read operation that must fail saying says.
fn read_refused(main: &Webview, w: &str, op: &str, input: Value, says: &str) -> Result<(), String> {
    let (r, asked) = unasked(|| call(main, w, op, input.clone()));
    let r = r?;
    require(!asked, format!("{op} {input} asked a native confirmation"))?;
    require(
        r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains(says)),
        format!("{op} {input} should be refused with {says:?}: {r}"),
    )
}

/// f's result, and whether a native confirmation showed while it ran (each one is cancelled).
fn unasked<T>(f: impl FnOnce() -> T) -> (T, bool) {
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Arc;
    let (stop, seen) = (
        Arc::new(AtomicBool::new(false)),
        Arc::new(AtomicBool::new(false)),
    );
    let watcher = {
        let (stop, seen) = (Arc::clone(&stop), Arc::clone(&seen));
        std::thread::spawn(move || {
            while !stop.load(Ordering::SeqCst) {
                if showing() {
                    seen.store(true, Ordering::SeqCst);
                    let _ = press("alt+c");
                }
                std::thread::sleep(Duration::from_millis(100));
            }
        })
    };
    let r = f();
    stop.store(true, Ordering::SeqCst);
    let _ = watcher.join();
    (r, seen.load(Ordering::SeqCst))
}

/// The text of the native confirmation showing now that contains every one of needles.
fn dialog_with(app: &AppHandle, needles: &[&str]) -> Result<String, String> {
    let deadline = Instant::now() + Duration::from_secs(60);
    let mut last = String::new();
    while Instant::now() < deadline {
        if let Ok(t) = dialog_text(app) {
            if needles.iter().all(|n| t.contains(n)) {
                return Ok(t);
            }
            last = t;
        }
        std::thread::sleep(Duration::from_millis(200));
    }
    Err(format!(
        "no native confirmation naming {needles:?}; the last one said: {last}"
    ))
}

/// The writer's write operation op from the window, its confirmation (which must name needles)
/// accepted: the window's answer, `{"ok", "value" | "error"}`.
fn confirmed(
    app: &AppHandle,
    main: &Webview,
    w: &str,
    op: &str,
    input: Value,
    needles: &[&str],
) -> Result<Value, String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": w, "op": op, "input": input}),
    )?;
    let what = format!("{w} {op}");
    let mut all = vec![what.as_str()];
    all.extend_from_slice(needles);
    dialog_with(app, &all)?;
    press("alt+o")?;
    std::thread::sleep(Duration::from_millis(500));
    let (r, asked) = unasked(|| settle(main, &slot));
    require(!asked, format!("{op} asked a second native confirmation"))?;
    r
}

/// A confirmed write that must succeed: its output.
fn written(
    app: &AppHandle,
    main: &Webview,
    w: &str,
    op: &str,
    input: Value,
    needles: &[&str],
) -> Result<Value, String> {
    let r = confirmed(app, main, w, op, input.clone(), needles)?;
    require(r["ok"] == true, format!("{op} {input}: {r}"))?;
    Ok(r["value"].clone())
}

/// A confirmed write that must fail saying says.
fn write_refused(
    app: &AppHandle,
    main: &Webview,
    w: &str,
    op: &str,
    input: Value,
    says: &str,
) -> Result<String, String> {
    let r = confirmed(app, main, w, op, input.clone(), &[])?;
    let error = r["error"].as_str().unwrap_or_default().to_string();
    require(
        r["ok"] == false && error.contains(says),
        format!("{op} {input} should fail with {says:?}: {r}"),
    )?;
    Ok(error)
}

/// The writer's journals in the window's status.
fn journals(main: &Webview, w: &str) -> Result<Vec<Value>, String> {
    let status = ok(main, "program_status", json!({"package": w}))?;
    Ok(status["journals"].as_array().cloned().unwrap_or_default())
}

/// Waits out the writer's crash backoff, once its status says it crashed.
fn after_backoff(main: &Webview, w: &str) -> Result<(), String> {
    let wait = wait_for("the writer's crash in its status", || {
        let status = ok(main, "program_status", json!({"package": w})).ok()?;
        (status["state"] == "crashed").then(|| status["retry_in_ms"].as_u64().unwrap_or(0))
    })?;
    std::thread::sleep(Duration::from_millis(wait + 300));
    Ok(())
}

/// Temporary files left anywhere under root.
fn leftovers(root: &Path) -> Result<Vec<String>, String> {
    Ok(snapshot(root)?
        .into_keys()
        .filter(|k| {
            Path::new(k)
                .file_name()
                .and_then(|n| n.to_str())
                .is_some_and(|n| n.contains(".pluxy-") || n.ends_with(".tmp"))
        })
        .collect())
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let x: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let s = |v: &Value| v.as_str().unwrap_or_default().to_string();
    let w = s(&x["id"]);
    let w = w.as_str();
    let home = PathBuf::from(s(&x["home"]));
    let stow_link = PathBuf::from(s(&x["stow"]["link"]));
    let stow_text = s(&x["stow"]["text"]);
    let stow_target = PathBuf::from(s(&x["stow"]["target"]));
    let stow_folder = PathBuf::from(s(&x["stow"]["folder"]));
    let alt = PathBuf::from(s(&x["stow"]["alt"]));
    let root_link = PathBuf::from(s(&x["root"]["link"]));
    let root_text = s(&x["root"]["text"]);
    let a = PathBuf::from(s(&x["root"]["a"]));
    let b = PathBuf::from(s(&x["root"]["b"]));
    let outside = PathBuf::from(s(&x["outside"]));
    let bytes = |k: &str| s(&x["bytes"][k]);
    let (sl, st, rl, ap, bp) = (
        stow_link.display().to_string(),
        stow_target.display().to_string(),
        root_link.display().to_string(),
        a.display().to_string(),
        b.display().to_string(),
    );
    let dotfiles = home.join("dotfiles");
    let chezmoi = home.join(".local/share/chezmoi");
    let config = home.join(".config");
    let all = || -> Result<BTreeMap<String, String>, String> {
        let mut m = snapshot(&dotfiles)?;
        m.extend(snapshot(&chezmoi)?);
        m.extend(snapshot(&config)?);
        Ok(m)
    };
    let file = |p: &Path| json!({"file": p});
    let in_root = |name: &str| json!({"root": root_link, "path": name});
    let mut out = json!({"steps": []});
    let mut passed = |name: &str| {
        out["steps"].as_array_mut().unwrap().push(json!(name));
    };
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

    // ---- install and enable
    step(
        "install",
        (|| {
            install(main, "curated", w, &s(&x["version"]))?;
            enable(app, main, w)?;
            Ok(())
        })(),
    )?;
    passed("install");
    let program = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store")
        .join(s(&x["sha256"]))
        .join(s(&x["bin"]));
    require(
        program.is_file(),
        format!("{} is not installed", program.display()),
    )?;

    // ---- file link: the selected stow link
    step(
        "file link",
        (|| {
            let before = all()?;
            let link_before = link(&stow_link)?;
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "file", "targets": [file(&stow_link)]}),
            )?;
            let pin = &p["targets"][0];
            require(
                pin["selected"] == json!(stow_link)
                    && pin["link"] == json!(stow_text)
                    && pin["target"] == json!(stow_target),
                format!("the preview of the stow link: {p}"),
            )?;
            let o = written(
                app,
                main,
                w,
                "apply",
                json!({"set": "file", "targets": p["targets"],
                    "values": {"model": "fixture-model", "proxy": "http://127.0.0.1:9"}}),
                &[sl.as_str(), st.as_str()],
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&stow_target) == Some(bytes("settingsApplied")),
                format!("the stow target after apply: {:?}", read(&stow_target)),
            )?;
            let mode = std::fs::metadata(&stow_target)
                .map_err(|e| e.to_string())?
                .permissions()
                .mode()
                & 0o7777;
            require(mode == 0o640, format!("the stow target's mode is {mode:o}"))?;
            require(
                link(&stow_link)? == link_before,
                "the stow link's identity or text changed",
            )?;
            let diff = changed(&before, &all()?);
            require(
                diff == vec![stow_target.display().to_string()],
                format!("the apply changed more than its target: {diff:?}"),
            )
        })(),
    )?;
    passed("file link");

    // ---- root link: two files in the selected chezmoi root
    step(
        "root link",
        (|| {
            let before = all()?;
            let link_before = link(&root_link)?;
            require(
                link_before.1 == Path::new(&root_text),
                "the root link's text is not the fixture's",
            )?;
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "root", "targets": [in_root("a.json"), in_root("b.json")]}),
            )?;
            require(
                p["targets"][0]["link"] == json!(root_text)
                    && p["targets"][0]["target"] == json!(a)
                    && p["targets"][1]["target"] == json!(b),
                format!("the preview of the root: {p}"),
            )?;
            let o = written(
                app,
                main,
                w,
                "apply",
                json!({"set": "root", "targets": p["targets"], "values": {"managed": true}}),
                &[rl.as_str(), ap.as_str()],
            )?;
            require(o["outcome"] == "applied", format!("the apply: {o}"))?;
            require(
                read(&a) == Some(bytes("aApplied")) && read(&b) == Some(bytes("bApplied")),
                format!("the root files after apply: {:?} {:?}", read(&a), read(&b)),
            )?;
            let bmode = std::fs::metadata(&b)
                .map_err(|e| e.to_string())?
                .permissions()
                .mode()
                & 0o7777;
            require(
                bmode == 0o600,
                format!("the created file's mode is {bmode:o}"),
            )?;
            require(
                link(&root_link)? == link_before,
                "the root link's identity or text changed",
            )?;
            let mut diff = changed(&before, &all()?);
            diff.sort();
            let mut want = vec![a.display().to_string(), b.display().to_string()];
            want.sort();
            require(diff == want, format!("the apply changed {diff:?}"))?;
            // Links inside the root and paths leaving it are refused, before any write.
            read_refused(
                main,
                w,
                "preview",
                json!({"set": "root", "targets": [in_root("inner-link.json")]}),
                "link",
            )?;
            read_refused(
                main,
                w,
                "preview",
                json!({"set": "root", "targets": [in_root("../outside.json")]}),
                "path",
            )?;
            require(
                read(&outside) == Some(bytes("outside")),
                "the inner link's target changed",
            )
        })(),
    )?;
    passed("root link");

    // ---- retarget: the link pointed elsewhere, or its target replaced, after the preview
    step(
        "retarget",
        (|| {
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "file", "targets": [file(&stow_link)]}),
            )?;
            relink(&stow_link, &alt)?;
            let before = all()?;
            write_refused(
                app,
                main,
                w,
                "apply",
                json!({"set": "file", "targets": p["targets"], "values": {"model": "retarget"}}),
                "changed",
            )?;
            let diff = changed(&before, &all()?);
            require(
                diff.is_empty(),
                format!("a retargeted apply changed {diff:?}"),
            )?;
            require(
                read(&alt) == Some(bytes("alt"))
                    && read(&stow_target) == Some(bytes("settingsApplied")),
                "a retargeted apply wrote a file",
            )?;
            relink(&stow_link, Path::new(&stow_text))?;
            // The target replaced as editors save, by rename, with the same bytes.
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "file", "targets": [file(&stow_link)]}),
            )?;
            let tmp = stow_folder.join("settings.json.editor");
            std::fs::write(&tmp, bytes("settingsApplied")).map_err(|e| e.to_string())?;
            std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o640))
                .map_err(|e| e.to_string())?;
            std::fs::rename(&tmp, &stow_target).map_err(|e| e.to_string())?;
            let before = all()?;
            write_refused(
                app,
                main,
                w,
                "apply",
                json!({"set": "file", "targets": p["targets"], "values": {"model": "replaced"}}),
                "changed",
            )?;
            let diff = changed(&before, &all()?);
            require(
                diff.is_empty(),
                format!("an apply to a replaced target changed {diff:?}"),
            )
        })(),
    )?;
    passed("retarget");

    // ---- competing edit: between preview and apply, and before clear
    step(
        "competing edit",
        (|| {
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "file", "targets": [file(&stow_link)]}),
            )?;
            std::fs::write(&stow_target, bytes("settingsEdited")).map_err(|e| e.to_string())?;
            write_refused(
                app,
                main,
                w,
                "apply",
                json!({"set": "file", "targets": p["targets"], "values": {"model": "late"}}),
                "modified",
            )?;
            require(
                read(&stow_target) == Some(bytes("settingsEdited")),
                "the edit made before the apply was not kept",
            )?;
            std::fs::write(&stow_target, bytes("settingsEditedAgain"))
                .map_err(|e| e.to_string())?;
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "file", "targets": [file(&stow_link)]}),
            )?;
            require(
                p["applied"] == true && p["edited"] == json!([true]),
                format!("the preview should say the applied file was edited: {p}"),
            )?;
            let link_before = link(&stow_link)?;
            let before = all()?;
            let o = written(
                app,
                main,
                w,
                "clear",
                json!({"set": "file", "targets": p["targets"]}),
                &[st.as_str()],
            )?;
            require(o["outcome"] == "cleared", format!("the clear: {o}"))?;
            require(
                read(&stow_target) == Some(bytes("settingsCleared")),
                format!("the stow target after clear: {:?}", read(&stow_target)),
            )?;
            require(
                link(&stow_link)? == link_before,
                "clear changed the stow link",
            )?;
            let diff = changed(&before, &all()?);
            require(
                diff == vec![stow_target.display().to_string()],
                format!("the clear changed {diff:?}"),
            )
        })(),
    )?;
    passed("competing edit");

    // ---- restore: the original bytes back through the same root link
    step(
        "restore",
        (|| {
            std::fs::write(&a, bytes("aEdited")).map_err(|e| e.to_string())?;
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "root", "targets": [in_root("a.json"), in_root("b.json")]}),
            )?;
            require(
                p["applied"] == true && p["edited"] == json!([true, false]),
                format!("the restore preview should say a.json was edited: {p}"),
            )?;
            let link_before = link(&root_link)?;
            let o = written(
                app,
                main,
                w,
                "restore",
                json!({"set": "root", "targets": p["targets"]}),
                &[ap.as_str()],
            )?;
            require(o["outcome"] == "restored", format!("the restore: {o}"))?;
            require(
                read(&a) == Some(bytes("a")) && !b.exists(),
                format!(
                    "after restore: a.json {:?}, b.json exists {}",
                    read(&a),
                    b.exists()
                ),
            )?;
            require(
                link(&root_link)? == link_before,
                "restore changed the root link",
            )
        })(),
    )?;
    passed("restore");

    // ---- partial rollback: the second file cannot be written
    step(
        "partial rollback",
        (|| {
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "pair", "targets": [in_root("a.json"), file(&stow_link)]}),
            )?;
            let before = all()?;
            std::fs::set_permissions(&stow_folder, std::fs::Permissions::from_mode(0o555))
                .map_err(|e| e.to_string())?;
            let r = write_refused(
                app,
                main,
                w,
                "apply",
                json!({"set": "pair", "targets": p["targets"], "values": {"pair": 1}}),
                "rolled back",
            );
            std::fs::set_permissions(&stow_folder, std::fs::Permissions::from_mode(0o755))
                .map_err(|e| e.to_string())?;
            r?;
            let diff = changed(&before, &all()?);
            require(
                diff.is_empty(),
                format!("a rolled-back apply left {diff:?}"),
            )?;
            require(
                read(&a) == Some(bytes("a"))
                    && read(&stow_target) == Some(bytes("settingsCleared")),
                "a rolled-back apply left a write",
            )?;
            let last = journals(main, w)?.last().cloned().unwrap_or_default();
            require(
                last["outcome"] == "rolled_back",
                format!("the rolled-back apply's journal: {last}"),
            )
        })(),
    )?;
    passed("partial rollback");

    // ---- crash recovery
    step(
        "crash rolled back",
        (|| {
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "crash1", "targets": [in_root("a.json"), in_root("b.json")]}),
            )?;
            let r = confirmed(
                app,
                main,
                w,
                "apply",
                json!({"set": "crash1", "targets": p["targets"], "values": {"crash": 1}, "crash_after": 1}),
                &[],
            )?;
            require(
                r["ok"] == false
                    && r["error"]
                        .as_str()
                        .is_some_and(|e| e.contains("outcome is unknown")),
                format!("the crashed apply: {r}"),
            )?;
            require(
                read(&a) != Some(bytes("a")),
                "the crashed apply did not write its first file",
            )?;
            let open: Vec<Value> = journals(main, w)?
                .into_iter()
                .filter(|j| j["outcome"].is_null())
                .collect();
            require(
                open.len() == 1,
                format!("open journals after the crash: {open:?}"),
            )?;
            let journal = open[0]["id"].clone();
            after_backoff(main, w)?;
            let st = read_op(main, w, "status", json!({}))?;
            let report = st["recovered"]
                .as_array()
                .and_then(|a| a.last())
                .cloned()
                .unwrap_or_default();
            require(
                report["journal"] == journal
                    && report["outcome"] == "rolled_back"
                    && report["restored"] == json!([a])
                    && report["conflicts"] == json!([]),
                format!("the recovery report: {st}"),
            )?;
            require(
                read(&a) == Some(bytes("a")) && !b.exists(),
                "the crashed apply was not rolled back",
            )?;
            let j = journals(main, w)?
                .into_iter()
                .find(|j| j["id"] == journal)
                .unwrap_or_default();
            require(
                j["outcome"] == "rolled_back" && j["reconciled"] == true,
                format!("the reconciled journal in the window's status: {j}"),
            )
        })(),
    )?;
    passed("crash rolled back");

    step(
        "crash competing edit",
        (|| {
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "crash2", "targets": [in_root("a.json"), file(&stow_link)]}),
            )?;
            let r = confirmed(
                app,
                main,
                w,
                "apply",
                json!({"set": "crash2", "targets": p["targets"], "values": {"crash": 2}, "crash_after": 1}),
                &[],
            )?;
            require(r["ok"] == false, format!("the crashed apply: {r}"))?;
            // Another program writes the file the writer wrote, before the writer starts again.
            std::fs::write(&a, bytes("competing")).map_err(|e| e.to_string())?;
            after_backoff(main, w)?;
            let st = read_op(main, w, "status", json!({}))?;
            let report = st["recovered"]
                .as_array()
                .and_then(|a| a.last())
                .cloned()
                .unwrap_or_default();
            require(
                report["outcome"] == "rolled_back"
                    && report["conflicts"] == json!([a])
                    && report["restored"] == json!([]),
                format!("the recovery report: {st}"),
            )?;
            require(
                read(&a) == Some(bytes("competing"))
                    && read(&stow_target) == Some(bytes("settingsCleared")),
                "recovery rolled over the competing edit or wrote the other file",
            )
        })(),
    )?;
    passed("crash competing edit");

    step(
        "crash committed",
        (|| {
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "crash3", "targets": [in_root("b.json")]}),
            )?;
            let r = confirmed(
                app,
                main,
                w,
                "apply",
                json!({"set": "crash3", "targets": p["targets"], "values": {"crash": 3}, "crash_after": 1}),
                &[],
            )?;
            require(r["ok"] == false, format!("the crashed apply: {r}"))?;
            after_backoff(main, w)?;
            let st = read_op(main, w, "status", json!({}))?;
            let report = st["recovered"]
                .as_array()
                .and_then(|a| a.last())
                .cloned()
                .unwrap_or_default();
            require(
                report["outcome"] == "committed" && st["sets"]["crash3"].is_object(),
                format!("the recovery report: {st}"),
            )?;
            require(
                read(&b) == Some(bytes("bCrash")),
                "the finished write was lost",
            )?;
            // The recovered state undoes it: clear removes the file the apply created.
            let p = read_op(
                main,
                w,
                "preview",
                json!({"set": "crash3", "targets": [in_root("b.json")]}),
            )?;
            let o = written(
                app,
                main,
                w,
                "clear",
                json!({"set": "crash3", "targets": p["targets"]}),
                &[bp.as_str()],
            )?;
            require(
                o["outcome"] == "cleared" && !b.exists(),
                format!("clear after recovery: {o}"),
            )
        })(),
    )?;
    passed("crash committed");

    // ---- audit
    step(
        "audit",
        (|| {
            let mut left = leftovers(&dotfiles)?;
            left.extend(leftovers(&chezmoi)?);
            left.extend(leftovers(&config)?);
            require(left.is_empty(), format!("temporary files left: {left:?}"))?;
            require(
                read(&outside) == Some(bytes("outside"))
                    && read(&alt) == Some(bytes("alt"))
                    && read(&stow_folder.join("other.json")) == Some(bytes("other")),
                "a file no step names changed",
            )?;
            require(
                link(&stow_link)?.1 == Path::new(&stow_text)
                    && link(&root_link)?.1 == Path::new(&root_text),
                "a link's text changed",
            )?;
            ok(main, "device_enable", json!({"id": w, "enabled": false}))?;
            wait_for("no writer process once disabled", || {
                processes(&program).is_empty().then_some(())
            })?;
            Ok(())
        })(),
    )?;
    passed("audit");

    out["passed"] = json!(true);
    log("transactions", out);
    Ok(())
}
