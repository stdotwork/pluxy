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
//! - audit: neither canary of config.toml and auth.json reached a reply or any webview (DOM,
//!   storage, callbacks, messages); the runner checks the app's stdout and stderr for them too.
//!
//! One line on stderr: `native-test: sessions <JSON>`.

use super::admission::{dialog_text, ok, press, require};
use super::key_entry_test::{clean_webview, watch};
use super::program::processes;
use super::routing::{call, enable, install, showing, Page};
use super::{eval, log, wait_for};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::os::unix::fs::MetadataExt;
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
        "a canary of config.toml or auth.json reached the page",
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
