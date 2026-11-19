//! The native test's check of the read-only Codex session reader, pluxy/codex-sessions
//! (native-test builds with `artifact-test-trust` only). It drives the real app through the
//! window's own commands, the real native confirmations and real page webviews, with the reader
//! program and page `native/sessions.ts` packed and the Codex home it made in this run's HOME:
//!
//! - install and enable: the reader installs from the curated registry and is enabled through its
//!   native confirmation, which names `detail` as an operation that shows content;
//! - list: the reader's own package page (the relay page the test archive adds) lists the sessions
//!   exactly as `expect.json` says, by hand: the databases, rollouts and index merged, deduplicated
//!   and ordered, each page of the paging, the total, the page's active and archived counts and the
//!   broken database's warning; no confirmation shows for a list;
//! - detail: a detail call from the same page asks a native confirmation naming the session and
//!   "(shows content)"; cancelled, the page gets `forbidden` and nothing of the session; confirmed,
//!   the page gets the detail `expect.json` says. The next detail page asks again (never remembered);
//! - export: the page's export call of a session asks a native confirmation naming it and "(shows
//!   content)" too; cancelled, the page gets `forbidden` and no save dialog shows; confirmed, the
//!   real save dialog (GTK's, titled with the package) suggests `session-<id>-<UTC date>.jsonl`;
//!   cancelled, nothing is written and the page hears `cancelled`; saved at a path typed in the
//!   dialog, the file holds the rollout `expect.json` says, byte for byte, 0600, alone in its
//!   folder, and the page hears only `saved` and the size;
//! - open: the page's open call of a session asks the native confirmation of the launch it makes
//!   through pluxy/execution (installed and enabled first, the reader's optional import), whose
//!   text names the reader, execution, the rollout `expect.json` says (resolved), its content type,
//!   the sessions folder (never the Codex home) and the opener, and offers no Remember; cancelled,
//!   the page gets `forbidden` and the stand-in opener (the fixture program as `xdg-open`, which the
//!   test tells the reader's native-test build to use) never runs; confirmed, it runs once with
//!   exactly that file as its argument, in the file's folder, and the page hears only `opened`; a
//!   session with no rollout file or a bad ID is refused before any confirmation, and the opener
//!   ran once in all;
//! - idle: once the page closes, no reader process is left after the idle stop;
//! - the reader's own page: opened, it starts the reader with its list call;
//! - disable: the reader runs (a list from the window) right before it is disabled, and no reader
//!   process is left once it is;
//! - sources: every file in the Codex home has the same bytes, kind and modification time as
//!   before, and no file was added (no `-wal` or `-shm` beside the WAL database);
//! - audit: no canary (config.toml, auth.json, the rollout's text that only the export carries)
//!   reached a reply or any webview (DOM, storage, callbacks, messages); the runner checks the
//!   app's stdout and stderr for them too.
//!
//! One line on stderr: `native-test: sessions <JSON>`.

use super::admission::{dialog_text, ok, press, require, x};
use super::key_entry_test::{clean_webview, watch};
use super::program::processes;
use super::routing::{call, enable, install, showing, Page};
use super::{chooser, eval, log, wait_for};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

/// Every entry under root, by its path inside it: its kind, size, modification time (ns) and
/// SHA-256 or link target.
fn snapshot(root: &Path) -> Result<BTreeMap<String, String>, String> {
    fn walk(root: &Path, dir: &Path, out: &mut BTreeMap<String, String>) -> Result<(), String> {
        for e in std::fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
            let e = e.map_err(|e| e.to_string())?;
            let path = e.path();
            let m = std::fs::symlink_metadata(&path).map_err(|e| e.to_string())?;
            let name = path
                .strip_prefix(root)
                .map_err(|e| e.to_string())?
                .to_string_lossy()
                .into_owned();
            let mtime = m.mtime() as i128 * 1_000_000_000 + m.mtime_nsec() as i128;
            let what = if m.file_type().is_symlink() {
                let to = std::fs::read_link(&path).map_err(|e| e.to_string())?;
                format!("link {} {mtime}", to.display())
            } else if m.is_dir() {
                walk(root, &path, out)?;
                format!("folder {mtime}")
            } else {
                let bytes = std::fs::read(&path).map_err(|e| e.to_string())?;
                let sha: String = Sha256::digest(&bytes)
                    .iter()
                    .map(|b| format!("{b:02x}"))
                    .collect();
                format!("file {} {mtime} {sha}", m.len())
            };
            out.insert(name, what);
        }
        Ok(())
    }
    let mut out = BTreeMap::new();
    walk(root, root, &mut out)?;
    Ok(out)
}

/// The page's call's result, failing on an error, with no native confirmation showing meanwhile.
fn unasked(page: &mut Page, id: &str, op: &str, input: Value) -> Result<Value, String> {
    let call = page.send(id, op, input.clone())?;
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(r) = eval(
            page.webview(),
            &format!("window.__route.replies[{}] ?? null", json!(call)),
        )
        .filter(|v| !v.is_null())
        {
            require(
                r.get("result").is_some(),
                format!("the page's {op} {input}: {r}"),
            )?;
            return Ok(r["result"].clone());
        }
        if showing() {
            press("alt+c")?;
            return Err(format!(
                "the page's {op} {input} asked a native confirmation"
            ));
        }
        if Instant::now() > deadline {
            return Err(format!("the page's {op} {input}: no answer"));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

/// The page's detail call with input: its confirmation's text, after it was answered (accepted
/// when confirm), and the page's reply.
fn detail(
    page: &mut Page,
    app: &AppHandle,
    id: &str,
    input: &Value,
    confirm: bool,
) -> Result<(String, Value), String> {
    let call = page.send(id, "detail", input.clone())?;
    let text = dialog_text(app)?;
    // A page's confirmation takes its answer only a second after it shows.
    std::thread::sleep(Duration::from_millis(1100));
    press(if confirm { "alt+o" } else { "alt+c" })?;
    let r = page.reply(&call)?;
    // The page's calls are limited to one confirmation a second.
    std::thread::sleep(Duration::from_millis(1100));
    Ok((text, r))
}

/// The page's call of op with input, answered (a result or an error) with no native confirmation
/// showing meanwhile.
fn answered_unasked(page: &mut Page, id: &str, op: &str, input: Value) -> Result<Value, String> {
    let call = page.send(id, op, input.clone())?;
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(r) = eval(
            page.webview(),
            &format!("window.__route.replies[{}] ?? null", json!(call)),
        )
        .filter(|v| !v.is_null())
        {
            return Ok(r);
        }
        if showing() {
            press("alt+c")?;
            return Err(format!(
                "the page's {op} {input} asked a native confirmation"
            ));
        }
        if Instant::now() > deadline {
            return Err(format!("the page's {op} {input}: no answer"));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

/// What the stand-in opener ran with, a line each time it ran.
fn opener_ran(record: &Path) -> Vec<Value> {
    std::fs::read_to_string(record)
        .unwrap_or_default()
        .lines()
        .filter_map(|l| serde_json::from_str(l).ok())
        .collect()
}

/// The page's open call of session, its confirmation answered (accepted when confirm): the
/// confirmation's text and the call's ID.
fn open_asked(
    page: &mut Page,
    app: &AppHandle,
    id: &str,
    session: &str,
    confirm: bool,
) -> Result<(String, String), String> {
    let call = page.send(id, "open", json!({"id": session}))?;
    let text = dialog_text(app)?;
    // A confirmation of a tool chain takes its answer only a second after it shows.
    std::thread::sleep(Duration::from_millis(1300));
    press(if confirm { "alt+o" } else { "alt+c" })?;
    Ok((text, call))
}

/// The open of a session in the person's own app: cancelled, confirmed, and the sessions that are
/// refused before any confirmation. Gives the replies it got, and what to log.
fn opened(
    page: &mut Page,
    app: &AppHandle,
    id: &str,
    open: &Value,
) -> Result<(Vec<Value>, Value), String> {
    let text_of = |k: &str| open[k].as_str().unwrap_or_default().to_string();
    let (session, file, folder, within, opener) = (
        text_of("id"),
        text_of("file"),
        text_of("folder"),
        text_of("within"),
        text_of("opener"),
    );
    let record = PathBuf::from(text_of("record"));
    require(
        !session.is_empty() && !file.is_empty() && !opener.is_empty(),
        "expect.json has no open",
    )?;
    require(
        opener_ran(&record).is_empty(),
        "the stand-in opener ran before the open check",
    )?;
    let mut replies = Vec::new();
    let mut out = json!({});

    // A cancelled confirmation names the file, its type, the folder and the opener; nothing runs.
    let (text, call) = open_asked(page, app, id, &session, false)?;
    let head = format!("Opens the file {file} (");
    let tail = format!(", inside {within}) with {opener}.");
    require(
        [id, "pluxy/execution", head.as_str(), tail.as_str()]
            .iter()
            .all(|w| text.contains(w)),
        format!(
            "the open confirmation should name {id}, pluxy/execution, {head}<type>{tail}: {text}"
        ),
    )?;
    require(
        !text.contains("Remember"),
        "the open confirmation offers Remember",
    )?;
    out["type"] = json!(text
        .split(&head)
        .nth(1)
        .and_then(|t| t.split(", inside").next())
        .unwrap_or_default());
    let r = page.reply(&call)?;
    require(
        r["error"]["code"] == "forbidden" && r.get("result").is_none(),
        format!("the cancelled open should be refused as forbidden: {r}"),
    )?;
    replies.push(r);
    std::thread::sleep(Duration::from_millis(1500));
    require(
        opener_ran(&record).is_empty(),
        "the opener ran though the open was cancelled",
    )?;

    // Confirmed: the opener runs once, with exactly that file, in its folder.
    std::thread::sleep(Duration::from_millis(1100));
    let (_, call) = open_asked(page, app, id, &session, true)?;
    let r = page.reply(&call)?;
    require(
        r["result"] == json!({"outcome": "opened"}),
        format!("the confirmed open: {r}"),
    )?;
    replies.push(r);
    let ran = wait_for("the opener's record", || {
        opener_ran(&record).into_iter().next()
    })?;
    require(
        ran["argv"] == json!([opener, file]) && ran["cwd"] == json!(folder),
        format!("the opener ran with {ran}, not {opener} {file} in {folder}"),
    )?;
    out["opened"] = ran["argv"].clone();

    // Refused before any confirmation, the opener not run again.
    for (input, code) in open["refused"].as_array().into_iter().flatten().map(|c| {
        (
            c["id"].clone(),
            c["code"].as_str().unwrap_or_default().to_string(),
        )
    }) {
        let r = answered_unasked(page, id, "open", json!({"id": input}))?;
        require(
            r["error"]["code"] == json!(code) && r.get("result").is_none(),
            format!("the open of {input} should be refused as {code}: {r}"),
        )?;
        replies.push(r);
    }
    require(
        opener_ran(&record).len() == 1,
        format!(
            "the opener ran {} times, not once",
            opener_ran(&record).len()
        ),
    )?;
    Ok((replies, out))
}

/// The save dialog's title: it names the package that asks.
const SAVE_TITLE: &str = "Save a file from pluxy/codex-sessions";

/// The visible save dialog's X window, once it shows.
fn chooser() -> Result<String, String> {
    wait_for("the save dialog", || {
        x(&[
            "search",
            "--onlyvisible",
            "--name",
            &format!("^{SAVE_TITLE}$"),
        ])
        .ok()
        .and_then(|s| s.lines().last().map(String::from))
    })
}

fn chooser_showing() -> bool {
    x(&[
        "search",
        "--onlyvisible",
        "--name",
        &format!("^{SAVE_TITLE}$"),
    ])
    .is_ok_and(|s| !s.is_empty())
}

/// The name the visible save dialog suggests, read from GTK.
fn suggested(app: &AppHandle) -> Result<String, String> {
    use gtk::prelude::*;
    chooser()?;
    wait_for("the save dialog's name", || {
        let (tx, rx) = std::sync::mpsc::channel();
        app.run_on_main_thread(move || {
            let mut name = None;
            for w in gtk::Window::list_toplevels() {
                if let Some(d) = w.downcast_ref::<gtk::FileChooserDialog>() {
                    if d.is_visible() && d.title().as_deref() == Some(SAVE_TITLE) {
                        name = d.current_name().map(|n| n.to_string());
                    }
                }
            }
            let _ = tx.send(name);
        })
        .ok()?;
        rx.recv_timeout(Duration::from_secs(5)).ok().flatten()
    })
}

/// Enters path in the save dialog's name field and presses Return (`chooser::enter` pastes it).
fn choose(app: &AppHandle, path: &Path) -> Result<(), String> {
    let id = chooser()?;
    chooser::enter(app, &id, "ctrl+a", path.to_string_lossy().into_owned())
}

fn chooser_key(keys: &str) -> Result<(), String> {
    let id = chooser()?;
    x(&["windowfocus", "--sync", &id])?;
    x(&["key", "--clearmodifiers", keys])?;
    Ok(())
}

/// Today's date in UTC, from the system's `date`.
fn utc_date() -> Result<String, String> {
    let out = std::process::Command::new("date")
        .args(["-u", "+%F"])
        .output()
        .map_err(|e| e.to_string())?;
    Ok(String::from_utf8_lossy(&out.stdout).trim().into())
}

/// The names in dir, sorted.
fn listing(dir: &Path) -> Result<Vec<String>, String> {
    let mut names: Vec<String> = std::fs::read_dir(dir)
        .map_err(|e| e.to_string())?
        .map(|e| e.map(|e| e.file_name().to_string_lossy().into_owned()))
        .collect::<Result<_, _>>()
        .map_err(|e| e.to_string())?;
    names.sort();
    Ok(names)
}

/// The page's export call for session, with its confirmation answered (accepted when confirm):
/// the confirmation's text and the call's ID.
fn export_asked(
    page: &mut Page,
    app: &AppHandle,
    id: &str,
    session: &str,
    confirm: bool,
) -> Result<(String, String), String> {
    let call = page.send(id, "export", json!({"id": session}))?;
    let text = dialog_text(app)?;
    // A page's confirmation takes its answer only a second after it shows.
    std::thread::sleep(Duration::from_millis(1100));
    press(if confirm { "alt+o" } else { "alt+c" })?;
    Ok((text, call))
}

/// The export of a session through the real save dialog: refused, cancelled and saved, each a
/// call of the same page. Gives the replies it got, and what to log.
fn exported(
    page: &mut Page,
    app: &AppHandle,
    id: &str,
    expect: &Value,
) -> Result<(Vec<Value>, Value), String> {
    let session = expect["id"].as_str().unwrap_or_default();
    let want = expect["text"].as_str().unwrap_or_default().as_bytes();
    require(
        !session.is_empty() && !want.is_empty(),
        "expect.json has no export",
    )?;
    let folder = std::fs::canonicalize(expect["folder"].as_str().unwrap_or_default())
        .map_err(|e| e.to_string())?;
    require(
        listing(&folder)?.is_empty(),
        "the export folder is not empty",
    )?;
    let mut replies = Vec::new();

    // A cancelled confirmation: refused, and no save dialog.
    let (text, call) = export_asked(page, app, id, session, false)?;
    require(
        text.contains(&format!("{id} export")) && text.contains("(shows content)"),
        format!("the export confirmation should name {id} export (shows content): {text}"),
    )?;
    let r = page.reply(&call)?;
    require(
        r["error"]["code"] == "forbidden" && r.get("result").is_none(),
        format!("the cancelled export should be refused as forbidden: {r}"),
    )?;
    require(
        !chooser_showing(),
        "a save dialog showed for a refused export",
    )?;
    require(listing(&folder)?.is_empty(), "a refused export left a file")?;
    replies.push(r);
    std::thread::sleep(Duration::from_millis(1100));

    // A cancelled save dialog: nothing is written.
    let before = utc_date()?;
    let (text, call) = export_asked(page, app, id, session, true)?;
    require(
        text.contains("(shows content)"),
        format!("each export call asks: {text}"),
    )?;
    let name = suggested(app)?;
    let after = utc_date()?;
    require(
        [before, after]
            .iter()
            .any(|d| name == format!("session-{session}-{d}.jsonl")),
        format!("the dialog suggests {name:?}, not session-{session}-<today, UTC>.jsonl"),
    )?;
    chooser_key("Escape")?;
    let r = page.reply(&call)?;
    require(
        r["result"] == json!({"outcome": "cancelled"}),
        format!("the cancelled save dialog: {r}"),
    )?;
    require(listing(&folder)?.is_empty(), "a cancelled save left a file")?;
    replies.push(r);
    std::thread::sleep(Duration::from_millis(1100));

    // Saved where the person types: exactly the rollout's bytes.
    let (_, call) = export_asked(page, app, id, session, true)?;
    let path = folder.join("exported.jsonl");
    suggested(app)?;
    choose(app, &path)?;
    let r = page.reply(&call)?;
    require(
        r["result"] == json!({"outcome": "saved", "size": want.len()}),
        format!("the saved export: {r}"),
    )?;
    let got = std::fs::read(&path).map_err(|e| e.to_string())?;
    require(
        got == want,
        format!(
            "the saved file holds {} bytes, not the rollout's {} (or other bytes)",
            got.len(),
            want.len()
        ),
    )?;
    let mode = std::fs::metadata(&path)
        .map_err(|e| e.to_string())?
        .permissions()
        .mode()
        & 0o7777;
    require(
        mode == 0o600,
        format!("the saved file has mode {mode:o}, not 600"),
    )?;
    require(
        listing(&folder)? == ["exported.jsonl"],
        format!("the folder holds {:?}", listing(&folder)?),
    )?;
    replies.push(r);
    std::thread::sleep(Duration::from_millis(1100));
    Ok((replies, json!({"suggested": name, "size": want.len()})))
}

/// The list output without its warnings, and whether its warnings are the one about broken.
fn list_checked(got: &Value, want: &Value, broken: &str, what: &str) -> Result<(), String> {
    let mut rest = got.clone();
    let warnings = rest
        .as_object_mut()
        .and_then(|o| o.remove("warnings"))
        .unwrap_or(Value::Null);
    require(
        &rest == want,
        format!("{what}: the list\n  {rest}\nis not the expected\n  {want}"),
    )?;
    let w: Vec<&str> = warnings
        .as_array()
        .map(|a| a.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    require(
        w.len() == 1 && w[0].contains(broken),
        format!("{what}: the warnings should be one about {broken}: {warnings}"),
    )
}

/// The webview showing a page of package id, once one does.
fn page_webview(app: &AppHandle, id: &str) -> Result<Webview, String> {
    let label = wait_for(&format!("a page webview showing {id}"), || {
        app.webviews().into_iter().find_map(|(label, w)| {
            (label != crate::MAIN
                && eval(&w, "document.querySelector('iframe')?.src ?? null")
                    .and_then(|v| v.as_str().map(|s| s.contains(&format!("/{id}/"))))
                    == Some(true))
            .then_some(label)
        })
    })?;
    app.get_webview(&label)
        .ok_or_else(|| format!("no webview {label}"))
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let id = expect["id"].as_str().unwrap_or_default().to_string();
    let codex = PathBuf::from(expect["codex"].as_str().unwrap_or_default());
    let broken = expect["warning_names"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    let canaries: Vec<String> = expect["canaries"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(|c| c.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default();
    let canary_refs: Vec<&str> = canaries.iter().map(String::as_str).collect();
    let before = snapshot(&codex)?;
    let mut out = json!({"files": before.len()});
    watch(main)?;

    // pluxy/execution first, which the reader imports (optionally): the open check's launch.
    let execution = expect["execution"]["id"]
        .as_str()
        .unwrap_or_default()
        .to_string();
    install(
        main,
        "curated",
        &execution,
        expect["execution"]["version"].as_str().unwrap_or_default(),
    )?;
    enable(app, main, &execution)?;

    // Installed from the curated registry and enabled.
    install(
        main,
        "curated",
        &id,
        expect["version"].as_str().unwrap_or_default(),
    )?;
    let program = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("device-packages")
        .join("store")
        .join(expect["sha256"].as_str().unwrap_or_default())
        .join(expect["bin"].as_str().unwrap_or_default());
    require(
        program.is_file(),
        format!("{} is not installed", program.display()),
    )?;
    let enabled = enable(app, main, &id)?;
    require(
        enabled.contains("detail") && enabled.contains("shows content"),
        format!("the enable confirmation should name detail as showing content: {enabled}"),
    )?;
    out["program"] = json!(program);

    let mut replies = Vec::new();
    let mut page = Page::open(app, main, &id)?;
    watch(page.webview())?;
    // Each list page, as expected, with no confirmation.
    for (n, case) in expect["list"].as_array().into_iter().flatten().enumerate() {
        let got = unasked(&mut page, &id, "list", case["input"].clone())?;
        list_checked(
            &got,
            &case["output"],
            &broken,
            &format!("list {n} {}", case["input"]),
        )?;
        replies.push(got);
    }
    out["lists"] = json!(replies.len());

    // Detail: refused when its confirmation is cancelled, shown once confirmed, asked again for
    // the next page.
    let details = expect["detail"].as_array().cloned().unwrap_or_default();
    let first = details.first().ok_or("expect.json has no detail")?;
    let (text, r) = detail(&mut page, app, &id, &first["input"], false)?;
    require(
        text.contains(&format!("{id} detail")) && text.contains("(shows content)"),
        format!("the detail confirmation should name {id} detail (shows content): {text}"),
    )?;
    require(
        r["error"]["code"] == "forbidden" && r.get("result").is_none(),
        format!("the cancelled detail should be refused as forbidden: {r}"),
    )?;
    replies.push(r);
    let mut texts = vec![text];
    for case in &details {
        let (text, r) = detail(&mut page, app, &id, &case["input"], true)?;
        require(
            text.contains("(shows content)"),
            format!("each detail call asks again: {text}"),
        )?;
        require(
            r["result"] == case["output"],
            format!(
                "the confirmed detail {}\n  {}\nis not the expected\n  {}",
                case["input"], r["result"], case["output"]
            ),
        )?;
        texts.push(text);
        replies.push(r);
    }
    out["confirmations"] = json!(texts);
    // Export: refused, cancelled and saved through the real save dialog.
    let (more, shown) =
        exported(&mut page, app, &id, &expect["export"]).map_err(|e| format!("export: {e}"))?;
    replies.extend(more);
    out["export"] = shown;
    // The reader's native-test build takes the stand-in opener's folder from its data folder.
    let data = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("tools/pluxy/codex-sessions");
    let opener = PathBuf::from(expect["open"]["opener"].as_str().unwrap_or_default());
    std::fs::write(
        data.join("opener-folder"),
        opener
            .parent()
            .map(|p| p.to_string_lossy().into_owned())
            .unwrap_or_default(),
    )
    .map_err(|e| format!("the reader's data folder {}: {e}", data.display()))?;
    let (more, shown) =
        opened(&mut page, app, &id, &expect["open"]).map_err(|e| format!("open: {e}"))?;
    replies.extend(more);
    out["open"] = shown;
    let all = serde_json::to_string(&replies).map_err(|e| e.to_string())?;
    require(
        !canaries.iter().any(|c| all.contains(c.as_str())),
        "a canary of config.toml, auth.json or the rollout reached the page",
    )?;
    clean_webview(page.webview(), &canary_refs)?;
    page.close(main)?;

    // Idle: the reader stops on its own once nothing uses it.
    let t = Instant::now();
    wait_for("the reader to stop when idle", || {
        processes(&program).is_empty().then_some(())
    })?;
    out["idle_stop_secs"] = json!(t.elapsed().as_secs_f64());

    // The reader's own page starts it with its list call.
    let route = format!("device/{id}/sessions");
    wait_for(&format!("{route} in the navigation"), || {
        eval(
            main,
            &format!("document.querySelector('a[href=\"#/{route}\"]') !== null"),
        )
        .filter(|v| v == true)
    })?;
    main.eval(format!("location.hash = {}", json!(format!("#/{route}"))))
        .map_err(|e| e.to_string())?;
    let own = page_webview(app, &id)?;
    let label = own.label().to_string();
    wait_for("the reader's own page to start it", || {
        (!processes(&program).is_empty()).then_some(())
    })?;
    watch(&own)?;
    std::thread::sleep(Duration::from_secs(2));
    clean_webview(&own, &canary_refs)?;
    main.eval("location.hash = '#/device'")
        .map_err(|e| e.to_string())?;
    wait_for(&format!("{label} to close"), || {
        app.get_webview(&label).is_none().then_some(())
    })?;

    // Disabled: no reader process is left. A list from the window first, so it runs right before.
    let r = call(main, &id, "list", json!({}))?;
    require(
        r["ok"] == true,
        format!("the window's list before disabling: {r}"),
    )?;
    require(
        !processes(&program).is_empty(),
        "the reader is not running right before it is disabled",
    )?;
    let t = Instant::now();
    ok(main, "device_enable", json!({"id": id, "enabled": false}))?;
    wait_for("the reader to stop when disabled", || {
        processes(&program).is_empty().then_some(())
    })?;
    out["disable_stop_secs"] = json!(t.elapsed().as_secs_f64());

    // The sources are as they were, and nothing was added beside them.
    let after = snapshot(&codex)?;
    if after != before {
        let changed: Vec<&String> = after
            .iter()
            .filter(|(k, v)| before.get(*k) != Some(v))
            .map(|(k, _)| k)
            .chain(before.keys().filter(|k| !after.contains_key(*k)))
            .collect();
        return Err(format!("the Codex home changed: {changed:?}"));
    }
    clean_webview(main, &canary_refs)?;

    out["passed"] = json!(true);
    log("sessions", out);
    Ok(())
}
