//! The native test's check of the host's save-file service (native-test builds with
//! `artifact-test-trust` only). It drives the real app through the window's own commands, the real
//! native confirmations, the real native save dialog (GTK's, the app's own process: the run sets
//! `GTK_USE_PORTAL=0`) and a real page webview, with the signed fixture tool `native/save.ts` built:
//! pluxy/exporter, whose `export` (disclose) hands `content.bin`'s bytes to the host's save service
//! and whose `sneak` (read) tries the same without a confirmation. Every saved file is compared with
//! `content.bin` itself, never with what the app says it wrote.
//!
//! - sneak: a read operation's bytes are refused at the first append; no save dialog shows;
//! - path: after the confirmation, a save naming a path (`a/b.jsonl`) is refused, and so is one
//!   with a field beside `name` (`path`); no save dialog shows;
//! - limit: one byte over the limit is refused at the append that passes it, and a save after it
//!   is refused as well (the bytes so far were dropped); no dialog shows;
//! - cancel: the save dialog, titled with the package, suggests the caller's name; cancelled, the
//!   tool hears `cancelled` and the folder stays empty;
//! - saved: a name with control and format characters is suggested with them replaced; the file the
//!   person typed holds exactly `content.bin`, mode 0600, and nothing else is left in the folder; a
//!   second save for the same call is refused, with no dialog;
//! - overwrite: choosing an existing file asks GTK's own overwrite question and writes nothing
//!   while it shows; cancelling it and then the dialog leaves the file as it was; answering Replace
//!   replaces it with `content.bin` (a new file, with the old one's permissions);
//! - swap: while the service holds before its last check (the run's hold file), the chosen name
//!   becomes a link to another file: for a new file and for a confirmed overwrite, the save is
//!   refused, the link and the file it points to stay as they were, and no temporary file is left;
//! - page: from the exporter's own page, the same export asks the page's confirmation and the save
//!   dialog, and saves the file; the page's reply carries no content.
//!
//! The runner checks no line of the app's output shows a canary of `content.bin`.
//!
//! One line on stderr: `native-test: save <JSON>`.

use super::admission::{dialog_text, press, require, settle, start, x};
use super::routing::{enable, install, showing, Page};
use super::{chooser, eval, log, on_main, wait_for};
use serde_json::{json, Value};
use std::os::unix::fs::{MetadataExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const EXPORTER: &str = "pluxy/exporter";
/// The save dialog's title: it names the package that asks.
const TITLE: &str = "Save a file from pluxy/exporter";

/// The visible save dialog's X window.
fn chooser() -> Result<String, String> {
    wait_for("the save dialog", || {
        x(&["search", "--onlyvisible", "--name", &format!("^{TITLE}$")])
            .ok()
            .and_then(|s| s.lines().last().map(String::from))
    })
}

fn chooser_showing() -> bool {
    x(&["search", "--onlyvisible", "--name", &format!("^{TITLE}$")]).is_ok_and(|s| !s.is_empty())
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
                    if d.is_visible() && d.title().as_deref() == Some(TITLE) {
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

/// The title the check gives GTK's overwrite question, which has none, to find its X window.
const QUESTION: &str = "pluxy save overwrite question";

/// What the check's GTK watcher last saw showing, for a timeout's message.
type Seen = std::sync::Arc<std::sync::Mutex<String>>;

/// Starts a GTK timer that titles GTK's overwrite question `QUESTION` once it shows (a visible
/// message dialog of the app's saying the file already exists) and keeps the list of the visible
/// toplevel windows. GTK runs the question in a main loop of its own until it is answered, where
/// the app's run_on_main_thread calls wait (the check cannot ask GTK then); a GLib timer still runs
/// there. It runs until the app quits.
fn watch_questions(app: &AppHandle) -> Result<Seen, String> {
    let seen = Seen::default();
    let shown = seen.clone();
    on_main(app, move || {
        use gtk::prelude::*;
        gtk::glib::timeout_add_local(Duration::from_millis(100), move || {
            let mut showing = Vec::new();
            for w in gtk::Window::list_toplevels() {
                let Some(win) = w.downcast_ref::<gtk::Window>() else {
                    continue;
                };
                if !win.is_visible() {
                    continue;
                }
                let text = w
                    .downcast_ref::<gtk::MessageDialog>()
                    .and_then(|d| d.text())
                    .map(|t| t.to_string());
                if text
                    .as_deref()
                    .is_some_and(|t| t.contains("already exists"))
                    && win.title().as_deref() != Some(QUESTION)
                {
                    win.set_title(QUESTION);
                }
                showing.push(format!(
                    "{} {:?} {:?}",
                    w.type_().name(),
                    win.title().map(|t| t.to_string()),
                    text
                ));
            }
            *shown.lock().unwrap() = showing.join("; ");
            gtk::glib::ControlFlow::Continue
        });
    })
    .ok_or("the main thread did not answer")?;
    Ok(seen)
}

/// GTK's overwrite question's X window, once it shows (titled by `watch_questions`). On a
/// timeout, says which toplevel windows were showing.
fn question(seen: &Seen) -> Result<String, String> {
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(id) = x(&[
            "search",
            "--onlyvisible",
            "--name",
            &format!("^{QUESTION}$"),
        ])
        .ok()
        .and_then(|s| s.lines().last().map(String::from))
        {
            return Ok(id);
        }
        if Instant::now() > deadline {
            return Err(format!(
                "no overwrite question showed within a minute; showing: {}",
                seen.lock().unwrap()
            ));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

fn question_key(id: &str, keys: &str) -> Result<(), String> {
    x(&["windowfocus", "--sync", id])?;
    x(&["key", "--clearmodifiers", keys])?;
    Ok(())
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

/// Starts the window's export (op) with input; once its native confirmation shows (naming the
/// package and "shows content", unless op asks none), confirms it. The call's slot.
fn exported(app: &AppHandle, main: &Webview, op: &str, input: Value) -> Result<String, String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": EXPORTER, "op": op, "input": input}),
    )?;
    let text = dialog_text(app)?;
    require(
        text.contains(EXPORTER) && text.contains("export") && text.contains("shows content"),
        format!("the export's confirmation: {text}"),
    )?;
    press("alt+o")?;
    Ok(slot)
}

/// The tool's output of a settled call, failing on an error.
fn output(main: &Webview, slot: &str) -> Result<Value, String> {
    let r = settle(main, slot)?;
    require(r["ok"] == true, format!("the export call: {r}"))?;
    Ok(r["value"].clone())
}

/// The call's answer while no save dialog shows (cancelling one that does): the tool's output.
fn undialogued(main: &Webview, slot: &str, what: &str) -> Result<Value, String> {
    let deadline = Instant::now() + Duration::from_secs(60);
    loop {
        if let Some(r) = eval(
            main,
            &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
        )
        .filter(|v| !v.is_null())
        {
            require(r["ok"] == true, format!("{what}: {r}"))?;
            return Ok(r["value"].clone());
        }
        if chooser_showing() {
            chooser_key("Escape")?;
            return Err(format!("{what}: a save dialog showed"));
        }
        if Instant::now() > deadline {
            return Err(format!("{what}: no answer"));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

fn code(answer: &Value) -> &str {
    answer["error"]["code"].as_str().unwrap_or_default()
}

fn mode(path: &Path) -> Result<u32, String> {
    std::fs::symlink_metadata(path)
        .map(|m| m.permissions().mode() & 0o7777)
        .map_err(|e| format!("{}: {e}", path.display()))
}

/// The file at path holds exactly want, as a regular file (not a link) of mode.
fn holds(path: &Path, want: &[u8], file_mode: u32) -> Result<(), String> {
    let m = std::fs::symlink_metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    require(
        m.file_type().is_file(),
        format!("{} is not a regular file", path.display()),
    )?;
    let got = std::fs::read(path).map_err(|e| e.to_string())?;
    require(
        got == want,
        format!(
            "{} holds {} bytes, not the {} expected",
            path.display(),
            got.len(),
            want.len()
        ),
    )?;
    require(
        mode(path)? == file_mode,
        format!(
            "{} has mode {:o}, not {file_mode:o}",
            path.display(),
            mode(path)?
        ),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let keys: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("keys.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let canaries: Vec<String> = keys["canaries"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(|c| c.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default();
    require(canaries.len() == 3, format!("keys.json: {keys}"))?;
    let content_file = fixtures.join("content.bin");
    let content = std::fs::read(&content_file).map_err(|e| e.to_string())?;
    let source = content_file.to_string_lossy().into_owned();
    let big = fixtures.join("big.bin").to_string_lossy().into_owned();
    let hold = fixtures.join("hold");
    let held = fixtures.join("hold.held");
    let out = fixtures.join("out");
    std::fs::create_dir_all(&out).map_err(|e| e.to_string())?;
    let out = std::fs::canonicalize(&out).map_err(|e| e.to_string())?;
    // Each step that passed, as it passes: a session that runs out of time still says how far it got.
    let passed = std::cell::RefCell::new(Vec::<String>::new());
    let step = |name: &str, r: Result<(), String>| -> Result<(), String> {
        r.map_err(|e| format!("{name}: {e}"))?;
        passed.borrow_mut().push(name.into());
        log("save_progress", json!({"passed": *passed.borrow()}));
        Ok(())
    };
    let seen = watch_questions(app)?;
    // A saved file's answer, and the content's canaries nowhere in what the tool heard.
    let saved = |r: &Value, what: &str| -> Result<(), String> {
        require(
            r["answer"]["output"]["outcome"] == "saved",
            format!("{what}: {r}"),
        )?;
        let text = r.to_string();
        require(
            !canaries.iter().any(|c| text.contains(c.as_str())),
            format!("{what}: the answer holds the content"),
        )
    };

    // ---- install and enable
    step(
        "install",
        (|| {
            install(main, "curated", EXPORTER, "1.0.0")?;
            let text = enable(app, main, EXPORTER)?;
            require(
                text.contains("export") && text.contains("shows content"),
                format!("the enable confirmation should name export as showing content: {text}"),
            )
        })(),
    )?;

    // ---- sneak: a read operation's bytes are refused at once, with no dialog
    step(
        "sneak",
        (|| {
            let slot = start(
                main,
                "program_call",
                json!({"package": EXPORTER, "op": "sneak",
                    "input": {"source": source, "name": "sneak.jsonl"}}),
            )?;
            let deadline = Instant::now() + Duration::from_secs(60);
            let r = loop {
                if showing() {
                    press("alt+c")?;
                    return Err("a read operation asked a native confirmation".into());
                }
                if let Some(r) = eval(
                    main,
                    &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
                )
                .filter(|v| !v.is_null())
                {
                    break r;
                }
                if chooser_showing() {
                    chooser_key("Escape")?;
                    return Err("a read operation's save showed a save dialog".into());
                }
                require(Instant::now() < deadline, "the sneak call: no answer")?;
                std::thread::sleep(Duration::from_millis(200));
            };
            require(r["ok"] == true, format!("the sneak call: {r}"))?;
            let appended = &r["value"]["appended"];
            require(
                appended.as_array().map(Vec::len) == Some(1)
                    && code(&appended[0]) == "permission"
                    && r["value"].get("answer").is_none(),
                format!("a read operation's append should be refused: {r}"),
            )?;
            require(listing(&out)?.is_empty(), "the sneak left a file")
        })(),
    )?;

    // ---- path: a caller-named path, or a field beside the name, is refused with no dialog
    step(
        "path",
        (|| {
            for (input, what) in [
                (
                    json!({"source": source, "name": "a/b.jsonl"}),
                    "a name with a path separator",
                ),
                (
                    json!({"source": source, "name": "c.jsonl",
                        "extra": {"path": out.join("c.jsonl").to_string_lossy()}}),
                    "a save naming a path",
                ),
            ] {
                let slot = exported(app, main, "export", input)?;
                let r = undialogued(main, &slot, what)?;
                require(
                    r["appended"].as_array().is_some_and(|a| {
                        !a.is_empty() && a.iter().all(|x| x.get("output").is_some())
                    }) && code(&r["answer"]) == "invalid_request",
                    format!("{what} should be refused: {r}"),
                )?;
                require(listing(&out)?.is_empty(), format!("{what} left a file"))?;
            }
            Ok(())
        })(),
    )?;

    // ---- limit: one byte over is refused at the append that passes it
    step(
        "limit",
        (|| {
            let slot = exported(
                app,
                main,
                "export",
                json!({"source": big, "name": "big.bin", "save_anyway": true}),
            )?;
            let r = undialogued(main, &slot, "the export over the limit")?;
            let appended = r["appended"].as_array().cloned().unwrap_or_default();
            let last = appended.last().cloned().unwrap_or(Value::Null);
            require(
                appended.len() > 1
                    && code(&last) == "invalid_request"
                    && appended[..appended.len() - 1]
                        .iter()
                        .all(|x| x.get("output").is_some())
                    && code(&r["answer"]) == "permission",
                format!(
                    "the append past the limit, and the save after it, should be refused: {} appends, last {last}, save {}",
                    appended.len(),
                    r["answer"]
                ),
            )?;
            require(
                listing(&out)?.is_empty(),
                "the export over the limit left a file",
            )
        })(),
    )?;

    // ---- cancel: the dialog names the package and suggests the name; cancel writes nothing
    step(
        "cancel",
        (|| {
            let slot = exported(
                app,
                main,
                "export",
                json!({"source": source, "name": "session.jsonl"}),
            )?;
            let name = suggested(app)?;
            require(
                name == "session.jsonl",
                format!("the dialog suggests {name:?}"),
            )?;
            chooser_key("Escape")?;
            let r = output(main, &slot)?;
            require(
                r["answer"]["output"]["outcome"] == "cancelled",
                format!("the cancelled save: {r}"),
            )?;
            require(listing(&out)?.is_empty(), "the cancelled save left a file")
        })(),
    )?;

    // ---- saved: exactly the bytes, 0600, nothing else left; a second save is refused
    step(
        "saved",
        (|| {
            let slot = exported(
                app,
                main,
                "export",
                json!({"source": source, "name": ".ses\u{202e}sion\u{7}.jsonl", "again": true}),
            )?;
            let name = suggested(app)?;
            require(
                name == "ses_sion_.jsonl",
                format!("the dialog suggests {name:?}, not the name with its control and format characters replaced"),
            )?;
            let path = out.join("new.jsonl");
            choose(app, &path)?;
            let r = output(main, &slot)?;
            saved(&r, "the confirmed save")?;
            require(
                code(&r["again"]) == "permission",
                format!("a second save for the same call should be refused: {r}"),
            )?;
            holds(&path, &content, 0o600)?;
            require(
                listing(&out)? == ["new.jsonl"],
                format!("the folder holds {:?}", listing(&out)?),
            )
        })(),
    )?;

    // ---- overwrite: GTK's own question; cancelled, nothing changes; Replace replaces
    step(
        "overwrite",
        (|| {
            let path = out.join("old.txt");
            std::fs::write(&path, b"the old bytes\n").map_err(|e| e.to_string())?;
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644))
                .map_err(|e| e.to_string())?;
            let inode = std::fs::metadata(&path).map_err(|e| e.to_string())?.ino();
            // Cancelled at the question, then in the dialog.
            let slot = exported(
                app,
                main,
                "export",
                json!({"source": source, "name": "old.txt"}),
            )?;
            chooser()?;
            choose(app, &path)?;
            // While the question shows, GTK holds the main thread, so the window cannot be asked
            // whether the call answered; the old bytes, and "cancelled" below, say it did not save.
            let q = question(&seen)?;
            holds(&path, b"the old bytes\n", 0o644)?;
            question_key(&q, "Escape")?;
            std::thread::sleep(Duration::from_millis(500));
            chooser_key("Escape")?;
            let r = output(main, &slot)?;
            require(
                r["answer"]["output"]["outcome"] == "cancelled",
                format!("the overwrite cancelled: {r}"),
            )?;
            holds(&path, b"the old bytes\n", 0o644)?;
            // Replace.
            let slot = exported(
                app,
                main,
                "export",
                json!({"source": source, "name": "old.txt"}),
            )?;
            chooser()?;
            choose(app, &path)?;
            let q = question(&seen)?;
            question_key(&q, "alt+r")?;
            let r = output(main, &slot)?;
            saved(&r, "the confirmed overwrite")?;
            holds(&path, &content, 0o644)?;
            require(
                std::fs::metadata(&path).map_err(|e| e.to_string())?.ino() != inode,
                "the overwrite wrote into the old file instead of replacing it",
            )?;
            require(
                listing(&out)? == ["new.jsonl", "old.txt"],
                format!("the folder holds {:?}", listing(&out)?),
            )
        })(),
    )?;

    // ---- swap: the chosen name becomes a link while the service holds; refused
    step(
        "swap",
        (|| {
            let victim = out.join("victim.txt");
            std::fs::write(&victim, b"the victim's bytes\n").map_err(|e| e.to_string())?;
            let target = out.join("target.txt");
            std::fs::write(&target, b"the target's bytes\n").map_err(|e| e.to_string())?;
            let mut want = vec![
                "new.jsonl".to_string(),
                "old.txt".into(),
                "target.txt".into(),
                "victim.txt".into(),
            ];
            for (name, existing) in [("swap.jsonl", false), ("target.txt", true)] {
                let path = out.join(name);
                std::fs::write(&hold, "").map_err(|e| e.to_string())?;
                let _ = std::fs::remove_file(&held);
                let slot = exported(app, main, "export", json!({"source": source, "name": name}))?;
                chooser()?;
                choose(app, &path)?;
                if existing {
                    let q = question(&seen)?;
                    question_key(&q, "alt+r")?;
                }
                wait_for("the save service's hold", || held.exists().then_some(()))?;
                let _ = std::fs::remove_file(&path);
                std::os::unix::fs::symlink(&victim, &path).map_err(|e| e.to_string())?;
                std::fs::remove_file(&hold).map_err(|e| e.to_string())?;
                let r = output(main, &slot)?;
                require(
                    code(&r["answer"]) == "permission",
                    format!("the save to {name}, a link swapped in, should be refused: {r}"),
                )?;
                require(
                    std::fs::read_link(&path).ok().as_deref() == Some(victim.as_path()),
                    format!("{name} is no longer the link swapped in"),
                )?;
                holds(&victim, b"the victim's bytes\n", mode(&victim)?)?;
                if !existing {
                    want.push(name.into());
                    want.sort();
                }
                require(
                    listing(&out)? == want,
                    format!("the folder holds {:?}, not {want:?}", listing(&out)?),
                )?;
            }
            Ok(())
        })(),
    )?;

    // ---- page: the exporter's own page exports the same way
    step(
        "page",
        (|| {
            let mut page = Page::open(app, main, EXPORTER)?;
            let path = out.join("from-page.jsonl");
            let id = page.send(
                EXPORTER,
                "export",
                json!({"source": source, "name": "from-page.jsonl"}),
            )?;
            let text = dialog_text(app)?;
            require(
                text.contains("export") && text.contains("shows content"),
                format!("the page's export confirmation: {text}"),
            )?;
            // A page's confirmation takes its answer only a second after it shows.
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+o")?;
            let name = suggested(app)?;
            require(
                name == "from-page.jsonl",
                format!("the dialog suggests {name:?}"),
            )?;
            choose(app, &path)?;
            let r = page.reply(&id)?;
            saved(&r["result"], "the page's save")?;
            holds(&path, &content, 0o600)?;
            page.close(main)
        })(),
    )?;

    log("save", json!({"passed": true}));
    Ok(())
}
