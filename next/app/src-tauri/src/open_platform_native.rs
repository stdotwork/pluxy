//! The native test's check of opening a file in the person's own app, and an address in their
//! browser, on Windows and macOS (native-test builds with `artifact-test-trust` only; Linux runs
//! `open_native.rs`). It drives the real app, the real execution program and the real native
//! confirmations with the signed packages `native/open.ts` built: pluxy/execution (the real
//! execution program) and pluxy/open-relay, a stand-in for a tool such as the Codex session
//! reader: a fixture tool whose `relay` calls execution's `prepare` and `launch`, as on Linux.
//! The files are the session's own, in `open-files` in its home folder, which no one else may
//! write. The opener is the system's: on Windows the execution program in its `open` mode
//! (`ShellExecuteExW`), on macOS `/usr/bin/open`. Each refusal has a positive control on the same
//! route, a launch that differs from it only in what the rule judges, which is asked:
//!
//! - enable: both tools installed and enabled;
//! - prepare: the tool asks the execution program to prepare opening `notes.txt`: on Windows the
//!   execution program itself (the bytes the session built) with `open -- <file>`, on macOS
//!   `/usr/bin/open <file>`; the file's folder as the cwd; the file and the folder it may open
//!   files in named;
//! - cancel: the tool's launch of it asks a native confirmation whose first line names the file,
//!   its type by its extension, the folder, the system and the opener, with no Remember;
//!   cancelled, nothing opens. It is the control of the type, within and outside steps;
//! - type: the same text named `.html` is refused;
//! - own: a file in the folder is asked; once others may write it (Windows: an Everyone write
//!   entry; macOS: group write, then an access list entry) it is refused; put back, asked again;
//! - within: the home folder and the files' root as the folder are refused;
//! - outside: a file outside the folder, named directly or through a link (macOS) or junction
//!   (Windows) inside it, is refused; the execution program refuses to prepare it too; none of
//!   these opened anything;
//! - browser (on a GitHub-hosted runner only, failing loudly elsewhere): `updates::open_external`
//!   with the session's own local https address (`native/browser.ts`) reaches the default
//!   browser: on Windows a process started with the address on its command line, on macOS the
//!   session's server logs the browser's request for it. Then the step closes only the browser it
//!   started: on Windows `WM_CLOSE` to the windows of the processes with that address on their
//!   command line; on macOS it quits the default browser, which was not running before;
//! - open: confirmed, the system's app for `.txt` files opens exactly that file: on Windows
//!   Notepad, whose command line names it, closed with `WM_CLOSE`; on macOS TextEdit, one of
//!   whose windows shows it, closed, and TextEdit quit when the step started it. Its windows are
//!   read, closed and quit through System Events (`osascript`): a GitHub-hosted runner's image
//!   lets `osascript` send Apple events to System Events and use accessibility, but send none to
//!   TextEdit, so asking TextEdit itself waits for a consent no one gives;
//! - jsonl: on Windows (on a GitHub-hosted runner only, failing loudly elsewhere), with no app for
//!   `.jsonl` files the execution program refuses to prepare a session file; with a per-user
//!   `HKCU\Software\Classes` file type of the check's own whose `open` command runs the fixture
//!   program, the launch prepared and confirmed reaches it with exactly that file, in its folder;
//!   with the type removed it is refused again. On macOS, as the runner has it: refused when no app
//!   opens `.jsonl` files (as on GitHub's image), else opened in TextEdit as above.
//!
//! On Windows a confirmed open is a detached launch, which the app refuses while it runs in a job
//! that does not let what it starts leave (`owned::windows::NO_BREAKAWAY`). Every step of a
//! GitHub-hosted runner runs in such a job, so the session starts the app outside it
//! (`native/platform-tools.ts`, as for the launcher session); started inside one, the open and
//! jsonl steps fail, saying so.
//!
//! One line on stderr: `native-test: open <JSON>`.

use super::admission::{dialog_text, press, require};
use super::routing::{answer, clicked, enable, install, showing};
use super::vault::dialog_with;
use super::{log, wait_for, within};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Webview};

const EXECUTION: &str = "pluxy/execution";
const RELAY: &str = "pluxy/open-relay";
/// What the host and the execution program say of a file outside the folder a tool may open.
const INSIDE: &str = "must be inside its within folder";
/// What the host says of a file others may write.
const NOT_OWN: &str = "must be your own, writable by no one else";
/// What the host says of a within folder that is a root, the home folder or a folder above it.
const WITHIN: &str = "cannot be a root, the home folder or a folder above it";
/// What the host says of a file whose extension is not one it opens.
const TYPES: &str = "must be .txt, .json, .jsonl, .ndjson, .md or .log";
/// The system, as the confirmation names it.
const SYSTEM: &str = if cfg!(windows) { "Windows" } else { "macOS" };
/// What the execution program says when no app opens a type.
const NO_APP: &str = if cfg!(windows) {
    "no app opens .jsonl files on this computer"
} else {
    "no app opens .jsonl files on this Mac"
};

/// path without Windows' `\\?\` prefix on a drive path, as a person would type it (what
/// `canonicalize` gives there); unchanged elsewhere.
fn plain(path: PathBuf) -> PathBuf {
    #[cfg(windows)]
    if let Some(rest) = path.to_str().and_then(|s| s.strip_prefix(r"\\?\")) {
        if rest.as_bytes().get(1) == Some(&b':') {
            return PathBuf::from(rest);
        }
    }
    path
}

fn canonical(path: &Path) -> Result<PathBuf, String> {
    std::fs::canonicalize(path)
        .map(plain)
        .map_err(|e| format!("{}: {e}", path.display()))
}

fn s(p: &Path) -> String {
    p.to_string_lossy().into_owned()
}

/// Refused, loudly, unless this is a GitHub-hosted runner, whose machine is thrown away after the
/// job: what changes the system outside the app's own folders (a file type, the browser) runs only
/// there, never on a person's computer.
fn hosted(what: &str) -> Result<(), String> {
    let actions = std::env::var("GITHUB_ACTIONS").unwrap_or_default();
    let runner = std::env::var("RUNNER_ENVIRONMENT").unwrap_or_default();
    require(
        actions == "true" && runner == "github-hosted",
        format!(
            "{what} changes this computer outside the app's own folders, so it runs only on a GitHub-hosted runner (GITHUB_ACTIONS=true, RUNNER_ENVIRONMENT=github-hosted); here GITHUB_ACTIONS={actions:?}, RUNNER_ENVIRONMENT={runner:?}"
        ),
    )
}

/// The tool's relayed call of op of the execution program with input, from a native click in the
/// window: the answer the tool got, `{"output": ...}` or `{"error": ...}`. A native confirmation
/// that shows meanwhile fails it (and is cancelled).
fn unasked(app: &AppHandle, op: &str, input: Value) -> Result<Value, String> {
    let rx = clicked(
        app,
        RELAY,
        "relay",
        json!({"package": EXECUTION, "op": op, "input": input}),
    )?;
    let deadline = Instant::now() + Duration::from_secs(30);
    loop {
        match rx.try_recv() {
            Ok(Ok(v)) => return Ok(v["answer"].clone()),
            Ok(Err(e)) => return Err(format!("the relay call failed: {e:?}")),
            Err(std::sync::mpsc::TryRecvError::Disconnected) => {
                return Err("the relay call gave no answer".into())
            }
            Err(std::sync::mpsc::TryRecvError::Empty) => {}
        }
        if showing() {
            press("alt+c")?;
            return Err(format!(
                "{op} {input} asked for a native confirmation; it should not"
            ));
        }
        require(Instant::now() < deadline, format!("{op}: no answer"))?;
        std::thread::sleep(Duration::from_millis(100));
    }
}

/// Requires the tool's relayed call of op with input refused before any confirmation, with an
/// error whose message contains says.
fn refused(app: &AppHandle, op: &str, input: Value, says: &str) -> Result<(), String> {
    // Confirmations from a tool's chain: one attempt per second.
    std::thread::sleep(Duration::from_millis(1100));
    let a = unasked(app, op, input.clone())?;
    let message = a["error"]["message"].as_str().unwrap_or_default();
    require(
        a.get("output").is_none() && message.contains(says),
        format!("{op} {input} should be refused saying {says:?}: {a}"),
    )
}

/// Requires the tool's launch of input asked: a native confirmation naming every one of needles
/// shows, is cancelled, and the launch then fails. The confirmation's text.
fn asked(app: &AppHandle, input: Value, needles: &[&str]) -> Result<String, String> {
    std::thread::sleep(Duration::from_millis(1100));
    let rx = clicked(
        app,
        RELAY,
        "relay",
        json!({"package": EXECUTION, "op": "launch", "input": input}),
    )?;
    let deadline = Instant::now() + Duration::from_secs(60);
    let mut last = String::new();
    let text = loop {
        if let Ok(v) = rx.try_recv() {
            return Err(format!(
                "the launch of {input} was answered with no confirmation: {v:?}"
            ));
        }
        if showing() {
            if let Ok(t) = dialog_text(app) {
                if needles.iter().all(|n| t.contains(n)) {
                    break t;
                }
                last = t;
            }
        }
        require(
            Instant::now() < deadline,
            format!("no native confirmation naming {needles:?} for {input}; the last said: {last}"),
        )?;
        std::thread::sleep(Duration::from_millis(200));
    };
    press("alt+c")?;
    let v = answer(&rx)?.map_err(|e| format!("the relay call failed: {e:?}"))?;
    require(
        v["answer"].get("error").is_some(),
        format!("the launch went ahead though cancelled: {v}"),
    )?;
    Ok(text)
}

/// The tool's launch of input, confirmed in its native confirmation, which names every one of
/// needles: required to start (detached, as every open is).
fn confirmed(app: &AppHandle, input: Value, needles: &[&str]) -> Result<(), String> {
    std::thread::sleep(Duration::from_millis(1100));
    let rx = clicked(
        app,
        RELAY,
        "relay",
        json!({"package": EXECUTION, "op": "launch", "input": input}),
    )?;
    dialog_with(app, needles)?;
    // A tool chain's dialog takes its Confirm only a second after it shows.
    std::thread::sleep(Duration::from_millis(1300));
    press("alt+o")?;
    let v = answer(&rx)?.map_err(|e| format!("the relay call failed: {e:?}"))?;
    #[cfg(windows)]
    if v.to_string().contains(
        crate::owned::windows::NO_BREAKAWAY
            .split(':')
            .next()
            .unwrap_or("-"),
    ) {
        return Err(format!(
            "the confirmed open did not start: the app runs in a job that keeps what it starts (a GitHub-hosted runner's steps all do), and refuses a detached launch there: {v}"
        ));
    }
    require(
        v["answer"]["output"]["lifetime"] == "detached",
        format!("the confirmed open: {v}"),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let execution_version =
        std::fs::read_to_string(fixtures.join("execution-version")).unwrap_or_default();
    let setup: Value = std::fs::read(fixtures.join("open.json"))
        .ok()
        .and_then(|b| serde_json::from_slice(&b).ok())
        .ok_or("the session wrote no open.json")?;
    let files = canonical(Path::new(setup["files"].as_str().unwrap_or_default()))?;
    // The execution program as the session built it, before it was packed.
    let built = PathBuf::from(setup["execution"].as_str().unwrap_or_default());
    let codex = files.join("codex");
    let folder = codex.join("sessions").join("2026").join("11").join("18");
    let notes = folder.join("notes.txt");
    let page = folder.join("page.html");
    let shared = folder.join("shared.txt");
    let rollout = folder.join("rollout-2026-11-18T03-00-00-session.jsonl");
    let private = files.join("outside").join("private.txt");
    // The file outside, named through a link (macOS) or a junction (Windows) inside the folder.
    let linked = if cfg!(windows) {
        folder.join("linked").join("private.txt")
    } else {
        folder.join("linked.txt")
    };
    let mut out = json!({"platform": std::env::consts::OS});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

    step(
        "enable",
        (|| {
            for (id, version) in [(EXECUTION, execution_version.trim()), (RELAY, "1.0.0")] {
                install(main, "curated", id, version)?;
                enable(app, main, id)?;
            }
            Ok(())
        })(),
    )?;

    // The opener's argv for file, as the design has it.
    let argv = |f: &Path| {
        if cfg!(windows) {
            json!(["open", "--", s(f)])
        } else {
            json!([s(f)])
        }
    };

    // ---- prepare: the system's opener, the file, its folder and the folder it may open files in
    let mut exe = String::new();
    step(
        "prepare",
        (|| {
            let a = unasked(
                app,
                "prepare",
                json!({"kind": "open", "file": s(&notes), "within": s(&codex)}),
            )?;
            let o = &a["output"];
            require(
                o["argv"] == argv(&notes)
                    && o["cwd"] == json!(s(&folder))
                    && o["file"] == json!(s(&notes))
                    && o["within"] == json!(s(&codex)),
                format!("prepared opening notes.txt: {a}"),
            )?;
            exe = o["executable"].as_str().unwrap_or_default().to_string();
            // Windows: the execution program itself, installed; macOS: the system's opener.
            #[cfg(windows)]
            require(
                std::fs::read(&exe)
                    .ok()
                    .is_some_and(|b| std::fs::read(&built).ok().as_deref() == Some(b.as_slice())),
                format!(
                    "the opener {exe} is not the execution program the session built ({})",
                    built.display()
                ),
            )?;
            #[cfg(target_os = "macos")]
            {
                let _ = &built;
                require(
                    exe == "/usr/bin/open",
                    format!("the opener is {exe}, not /usr/bin/open"),
                )?;
            }
            Ok(())
        })(),
    )?;

    // The tool's launch of file inside within, as the system opens it.
    let launch = |f: &Path, within: &Path| {
        json!({"executable": exe, "argv": argv(f), "cwd": s(&folder),
               "file": s(f), "within": s(within)})
    };
    // The confirmation's line for file of type kind (by its extension ext) inside the folder.
    let review = |f: &Path, kind: &str, ext: &str| {
        format!(
            "Opens the file {} ({kind}, inside {}) with the app {SYSTEM} opens .{ext} files with, through {exe}.",
            s(f),
            s(&codex)
        )
    };

    // ---- cancel: the confirmation names the file, its type, the folder and the opener
    step(
        "cancel",
        (|| {
            let line = review(&notes, "text/plain", "txt");
            let text = asked(app, launch(&notes, &codex), &[RELAY, EXECUTION, &line])?;
            require(
                !text.contains("Remember"),
                "a tool's open confirmation offers Remember",
            )?;
            std::thread::sleep(Duration::from_millis(1500));
            nothing_opened(&files)
        })(),
    )?;

    // ---- type: an extension that is not on the list
    step("type", refused(app, "launch", launch(&page, &codex), TYPES))?;

    // ---- own: a file others may write
    step(
        "own",
        (|| {
            let ask = || asked(app, launch(&shared, &codex), &[RELAY, &s(&shared)]);
            ask()?;
            #[cfg(windows)]
            {
                win::icacls(&shared, &["/grant", "*S-1-1-0:(W)"])?;
                let r = refused(app, "launch", launch(&shared, &codex), NOT_OWN);
                win::icacls(&shared, &["/remove:g", "*S-1-1-0"])?;
                r?;
            }
            #[cfg(target_os = "macos")]
            {
                use std::os::unix::fs::PermissionsExt;
                let mode = |m: u32| {
                    std::fs::set_permissions(&shared, std::fs::Permissions::from_mode(m))
                        .map_err(|e| format!("{}: {e}", shared.display()))
                };
                mode(0o620)?;
                let r = refused(app, "launch", launch(&shared, &codex), NOT_OWN);
                mode(0o600)?;
                r?;
                ask()?;
                mac::chmod(&["+a", "group:everyone allow write"], &shared)?;
                let r = refused(
                    app,
                    "launch",
                    launch(&shared, &codex),
                    "has an access list, which may let others write it",
                );
                mac::chmod(&["-N"], &shared)?;
                r?;
            }
            ask().map(|_| ())
        })(),
    )?;

    // ---- within: the home folder or a root as the folder the tool may open files in
    step(
        "within",
        (|| {
            let home = std::env::home_dir().ok_or("the app has no home folder")?;
            let home = canonical(&home)?;
            refused(app, "launch", launch(&notes, &home), WITHIN)?;
            let root = codex.ancestors().last().unwrap_or(&codex).to_path_buf();
            refused(app, "launch", launch(&notes, &root), WITHIN)?;
            out["within"] = json!({"home": s(&home), "root": s(&root)});
            Ok(())
        })(),
    )?;

    // ---- outside: files the tool may not name, refused before any confirmation
    step(
        "outside",
        (|| {
            refused(app, "launch", launch(&private, &codex), INSIDE)?;
            refused(app, "launch", launch(&linked, &codex), INSIDE)?;
            refused(
                app,
                "prepare",
                json!({"kind": "open", "file": s(&private), "within": s(&codex)}),
                INSIDE,
            )?;
            nothing_opened(&files)
        })(),
    )?;

    // The browser, open and jsonl steps each reach the system's own apps: each runs whether or not
    // the one before it failed (a confirmation it left showing is cancelled), and the check then
    // fails naming every one that did.
    let mut failed: Vec<String> = Vec::new();
    let mut each = |r: Result<(), String>| {
        if let Err(e) = r {
            failed.push(e);
            if showing() {
                let _ = press("alt+c");
            }
        }
    };

    // ---- browser: the app's own route to the person's browser, with the session's address
    each(step(
        "browser",
        (|| {
            hosted("opening the default browser")?;
            let b: Value = std::fs::read(fixtures.join("browser.json"))
                .ok()
                .and_then(|b| serde_json::from_slice(&b).ok())
                .ok_or("the session wrote no browser.json")?;
            let url = b["url"].as_str().unwrap_or_default().to_string();
            // The session's own address, never an outside site.
            let path = url
                .strip_prefix("https://127.0.0.1:")
                .and_then(|r| r.find('/').map(|i| r[i..].to_string()))
                .filter(|p| p.len() > 1)
                .ok_or(format!("{url:?} is not the session's local https address"))?;
            out["browser"] = browser(
                app,
                &url,
                &path,
                Path::new(b["log"].as_str().unwrap_or_default()),
            )?;
            Ok(())
        })(),
    ));

    // ---- open: confirmed, the system's app for .txt files opens exactly that file
    each(step(
        "open",
        (|| {
            let line = review(&notes, "text/plain", "txt");
            out["opened"] = opens(app, launch(&notes, &codex), &line, &notes)?;
            Ok(())
        })(),
    ));

    // ---- jsonl: a session file, as Codex's are
    each(step(
        "jsonl",
        (|| {
            let prep = json!({"kind": "open", "file": s(&rollout), "within": s(&codex)});
            let line = review(&rollout, "application/jsonl", "jsonl");
            #[cfg(windows)]
            {
                hosted("registering a file type for .jsonl files")?;
                refused(app, "prepare", prep.clone(), NO_APP)?;
                let bin = files.join("bin");
                let class = win::JsonlClass::register(&format!(
                    r#""{}" {OPENED} "%1""#,
                    s(&bin.join("handler.exe"))
                ))?;
                let a = unasked(app, "prepare", prep.clone())?;
                require(
                    a["output"] == launch(&rollout, &codex),
                    format!("prepared opening the session file: {a}"),
                )?;
                confirmed(app, launch(&rollout, &codex), &[RELAY, &line])?;
                let r = wait_for("the .jsonl app's record", || {
                    std::fs::read_to_string(bin.join("opened.jsonl"))
                        .ok()?
                        .lines()
                        .find_map(|l| serde_json::from_str::<Value>(l).ok())
                })?;
                require(
                    r["argv"].as_array().map(|a| a.get(1..).unwrap_or_default())
                        == Some(&[json!(OPENED), json!(s(&rollout))][..])
                        && r["cwd"] == json!(s(&folder)),
                    format!("the .jsonl app ran with {r}"),
                )?;
                drop(class);
                refused(app, "prepare", prep, NO_APP)?;
                out["jsonl"] = r["argv"].clone();
            }
            #[cfg(target_os = "macos")]
            {
                let a = unasked(app, "prepare", prep)?;
                if a.get("output").is_none() {
                    let message = a["error"]["message"].as_str().unwrap_or_default();
                    require(
                        message.contains(NO_APP),
                        format!("preparing the session file: {a}"),
                    )?;
                    out["jsonl"] = json!(NO_APP);
                } else {
                    require(
                        a["output"] == launch(&rollout, &codex),
                        format!("prepared opening the session file: {a}"),
                    )?;
                    out["jsonl"] = opens(app, launch(&rollout, &codex), &line, &rollout)?;
                }
            }
            Ok(())
        })(),
    ));
    require(failed.is_empty(), failed.join("; "))?;

    out["passed"] = json!(true);
    log("open", out);
    Ok(())
}

/// The fixture program's first argument as the stand-in app for `.jsonl` files
/// (`examples/program_fixture`, `OPENED`).
#[cfg(windows)]
const OPENED: &str = "pluxy-fixture-opened";

/// Requires that no app has any of the session's files open: on Windows no process names one on
/// its command line; on macOS TextEdit, if it runs, has none of them. On macOS it looks only when
/// TextEdit runs, so it proves little there; the refusals it backs are made before anything is
/// started, and the steps check those refusals themselves.
fn nothing_opened(files: &Path) -> Result<(), String> {
    #[cfg(windows)]
    let open: Vec<String> = win::naming(&s(files))?
        .into_iter()
        .map(|p| format!("{} {}", p.pid, p.command))
        .collect();
    #[cfg(target_os = "macos")]
    let open: Vec<String> = if mac::running(mac::TEXTEDIT)? {
        mac::textedit_documents()?
            .into_iter()
            .filter(|(_, d)| d.starts_with(files))
            .map(|(url, _)| url)
            .collect()
    } else {
        Vec::new()
    };
    require(
        open.is_empty(),
        format!("an app has the session's files open: {open:?}"),
    )
}

/// Confirms the tool's launch of input, which opens file, and requires the system's app for it
/// to have exactly that file open; then closes it. What opened it.
#[cfg(windows)]
fn opens(app: &AppHandle, input: Value, line: &str, file: &Path) -> Result<Value, String> {
    confirmed(app, input, &[RELAY, line])?;
    // The execution program's own `open -- <file>` names it too while it runs.
    let mut seen = Vec::new();
    let p = wait_for("Notepad started with the file", || {
        let now = win::naming(&s(file)).ok()?;
        let notepad = now.iter().position(|p| {
            p.path
                .to_ascii_lowercase()
                .ends_with(r"\system32\notepad.exe")
        });
        match notepad {
            Some(i) => now.into_iter().nth(i),
            None => {
                seen = now;
                None
            }
        }
    })
    .map_err(|e| format!("{e}; the processes naming the file: {seen:?}"))?;
    // Notepad, started for the file: its windows only, once it has one.
    wait_for("Notepad's window, to close it", || {
        (win::close_windows(p.pid) > 0).then_some(())
    })?;
    wait_for("Notepad to close", || {
        win::naming(&s(file)).ok().filter(|p| p.is_empty())
    })?;
    Ok(json!({"program": p.path, "command_line": p.command}))
}

/// Confirms the tool's launch of input, which opens file, and requires TextEdit (macOS' app for
/// text) to have exactly that file open; then closes it, and quits TextEdit when it was not
/// running before. What opened it.
#[cfg(target_os = "macos")]
fn opens(app: &AppHandle, input: Value, line: &str, file: &Path) -> Result<Value, String> {
    let was = mac::running(mac::TEXTEDIT)?;
    confirmed(app, input, &[RELAY, line])?;
    // What the last look found, for the timeout: whether TextEdit ran, and its windows' documents
    // or why they could not be read.
    let mut last = String::from("nothing yet");
    let docs = wait_for("TextEdit to have the file open", || {
        let d = match mac::running(mac::TEXTEDIT) {
            Ok(true) => mac::textedit_documents(),
            Ok(false) => Err("TextEdit does not run".into()),
            Err(e) => Err(e),
        };
        last = format!("{d:?}");
        let d = d.ok()?;
        d.iter().any(|(_, p)| p == file).then_some(d)
    })
    .map_err(|e| format!("{e}; the last look found {last}"))?;
    // Each look at the windows' documents decides: none shows the file (the proof that it closed),
    // or the windows that still do get a close click, and the look is repeated. A click can miss
    // (a window's index shifts as windows close or reorder), so one click is not the proof.
    let mut tried = String::from("no close click yet");
    wait_for("TextEdit to close the file", || {
        let d = match mac::textedit_documents() {
            Ok(d) => d,
            Err(e) => {
                tried = format!("the last look failed: {e}");
                return None;
            }
        };
        let shown: Vec<&str> = d
            .iter()
            .filter(|(_, p)| p == file)
            .map(|(u, _)| u.as_str())
            .collect();
        if shown.is_empty() {
            return Some(());
        }
        tried = shown
            .iter()
            .map(|u| match mac::textedit_close(u) {
                Ok(skipped) => format!("clicked, the script skipped: [{skipped}]"),
                Err(e) => format!("the close script failed: {e}"),
            })
            .collect::<Vec<_>>()
            .join("; ");
        None
    })
    .map_err(|e| format!("{e}; the last close: {tried}"))?;
    if !was {
        mac::textedit_quit()?;
        wait_for("TextEdit to quit", || {
            mac::running(mac::TEXTEDIT).ok().filter(|r| !r).map(|_| ())
        })?;
    }
    let docs: Vec<&str> = docs.iter().map(|(u, _)| u.as_str()).collect();
    Ok(json!({"app": "TextEdit", "documents": docs, "quit": !was}))
}

/// Windows: hands url to the app's browser route and requires a process started with it on its
/// command line (the path is the address's own, never given to anything else); then posts
/// `WM_CLOSE` to the windows of those processes, and reports whether they ended.
#[cfg(windows)]
fn browser(app: &AppHandle, url: &str, path: &str, _log: &Path) -> Result<Value, String> {
    require(
        win::naming(path)?.is_empty(),
        "a process has the session's address on its command line before the step",
    )?;
    crate::updates::open_external(app, url.to_string());
    let got = wait_for("a process started with the address", || {
        win::naming(path)
            .ok()
            .filter(|p| p.iter().any(|p| p.command.contains(url)))
    })
    .map_err(|e| {
        format!("{e} (is there a default browser, or did one already running take it?)")
    })?;
    // Only the processes started with this address: the step started them. Their windows, once
    // there are any.
    let closed = within(30, || {
        let n: usize = got.iter().map(|p| win::close_windows(p.pid)).sum();
        (n > 0).then_some(n)
    })
    .unwrap_or(0);
    let ended = within(30, || win::naming(path).ok().filter(|p| p.is_empty())).is_some();
    Ok(json!({
        "programs": got.iter().map(|p| p.path.clone()).collect::<Vec<_>>(),
        "command_line": got.iter().find(|p| p.command.contains(url)).map(|p| p.command.clone()),
        "windows_closed": closed,
        "ended": ended,
    }))
}

/// macOS: hands url to the app's browser route and requires the session's server to log the
/// default browser's request for its path; then quits the default browser, which the step
/// started (it was not running before).
#[cfg(target_os = "macos")]
fn browser(app: &AppHandle, url: &str, path: &str, log: &Path) -> Result<Value, String> {
    let bundle = mac::default_app(app, url).ok_or("no app opens https addresses on this Mac")?;
    let id = mac::bundle_id(&bundle)?;
    require(
        !mac::running(&id)?,
        format!("the default browser {id} runs already: this step quits only a browser it started"),
    )?;
    crate::updates::open_external(app, url.to_string());
    let request = wait_for("the browser's request for the address", || {
        std::fs::read_to_string(log)
            .ok()?
            .lines()
            .filter_map(|l| serde_json::from_str::<Value>(l).ok())
            .find(|r| r["method"] == "GET" && r["path"] == path)
    })?;
    require(
        mac::running(&id)?,
        format!("the default browser {id} does not run after the address was asked for"),
    )?;
    let quit = mac::quit(&id).and_then(|_| {
        within(30, || mac::running(&id).ok().filter(|r| !r).map(|_| ()))
            .ok_or_else(|| format!("{id} still runs 30 s after it was asked to quit"))
    });
    Ok(json!({
        "app": bundle,
        "id": id,
        "user_agent": request["user_agent"],
        "quit": quit.is_ok(),
        "quit_error": quit.err(),
    }))
}

#[cfg(windows)]
mod win {
    use std::path::{Path, PathBuf};
    use std::process::{Command, Stdio};

    /// A process, as Windows lists it.
    #[derive(Debug)]
    pub(super) struct Proc {
        pub pid: u32,
        pub path: String,
        pub command: String,
    }

    fn system32(name: &str) -> PathBuf {
        std::env::var_os("SystemRoot")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from(r"C:\Windows"))
            .join("System32")
            .join(name)
    }

    /// c's output, once it exited 0.
    fn output(mut c: Command) -> Result<String, String> {
        c.stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let o = crate::owned::windows::std_spawn(&mut c)
            .and_then(|c| c.wait_with_output())
            .map_err(|e| format!("{c:?}: {e}"))?;
        if !o.status.success() {
            return Err(format!(
                "{c:?} exited with {}: {}",
                o.status,
                String::from_utf8_lossy(&o.stderr)
            ));
        }
        Ok(String::from_utf8_lossy(&o.stdout).into_owned())
    }

    /// The processes whose command line holds needle (`Win32_Process`), which the query gets
    /// from its environment, not its own command line.
    pub(super) fn naming(needle: &str) -> Result<Vec<Proc>, String> {
        let mut c = Command::new(system32(r"WindowsPowerShell\v1.0\powershell.exe"));
        c.args([
            "-NoProfile",
            "-NonInteractive",
            "-Command",
            r#"$n = $env:PLUXY_TEST_NEEDLE; Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -and $_.CommandLine.Contains($n) } | ForEach-Object { "$($_.ProcessId)`t$($_.ExecutablePath)`t$($_.CommandLine)" }"#,
        ])
        .env("PLUXY_TEST_NEEDLE", needle);
        Ok(output(c)?
            .lines()
            .filter_map(|l| {
                let mut f = l.trim_end().splitn(3, '\t');
                Some(Proc {
                    pid: f.next()?.parse().ok()?,
                    path: f.next()?.to_string(),
                    command: f.next()?.to_string(),
                })
            })
            .collect())
    }

    /// Posts `WM_CLOSE` to each visible top-level window of process pid, as its close box does;
    /// how many.
    pub(super) fn close_windows(pid: u32) -> usize {
        use windows_sys::core::BOOL;
        use windows_sys::Win32::Foundation::{HWND, LPARAM};
        use windows_sys::Win32::UI::WindowsAndMessaging::{
            EnumWindows, GetWindow, GetWindowThreadProcessId, IsWindowVisible, PostMessageW,
            GW_OWNER, WM_CLOSE,
        };
        struct Find {
            pid: u32,
            found: Vec<HWND>,
        }
        unsafe extern "system" fn each(h: HWND, l: LPARAM) -> BOOL {
            // SAFETY: l is the Find EnumWindows was given, alive for its call.
            let f = unsafe { &mut *(l as *mut Find) };
            let mut owner = 0u32;
            // SAFETY: h is a window EnumWindows gave; owner is a u32 to fill.
            unsafe { GetWindowThreadProcessId(h, &mut owner) };
            if owner == f.pid
                && unsafe { IsWindowVisible(h) } != 0
                && unsafe { GetWindow(h, GW_OWNER) }.is_null()
            {
                f.found.push(h);
            }
            1
        }
        let mut f = Find {
            pid,
            found: Vec::new(),
        };
        // SAFETY: each only reads its window and writes f, which outlives the call.
        unsafe { EnumWindows(Some(each), &mut f as *mut Find as LPARAM) };
        f.found
            .into_iter()
            // SAFETY: posting a message to a window handle that has gone fails harmlessly.
            .filter(|h| unsafe { PostMessageW(*h, WM_CLOSE, 0, 0) } != 0)
            .count()
    }

    /// Changes path's access list with `icacls`.
    pub(super) fn icacls(path: &Path, args: &[&str]) -> Result<(), String> {
        let mut c = Command::new(system32("icacls.exe"));
        c.arg(path).args(args);
        output(c).map(|_| ())
    }

    fn wide(s: &str) -> Vec<u16> {
        s.encode_utf16().chain([0]).collect()
    }

    const JSONL: &str = r"Software\Classes\.jsonl";
    const CLASS: &str = r"Software\Classes\PluxyTest.jsonl";

    /// Whether the current user's registry has key.
    fn exists(key: &str) -> bool {
        use windows_sys::Win32::System::Registry::{
            RegCloseKey, RegOpenKeyExW, HKEY, HKEY_CURRENT_USER, KEY_READ,
        };
        let mut h: HKEY = std::ptr::null_mut();
        // SAFETY: the key name is NUL-terminated; h receives a key closed below.
        let r =
            unsafe { RegOpenKeyExW(HKEY_CURRENT_USER, wide(key).as_ptr(), 0, KEY_READ, &mut h) };
        if r == 0 {
            // SAFETY: h was opened above.
            unsafe { RegCloseKey(h) };
        }
        r == 0
    }

    /// Tells the shell the file types changed, as an installer does.
    fn changed() {
        use windows_sys::Win32::UI::Shell::{SHChangeNotify, SHCNE_ASSOCCHANGED, SHCNF_IDLIST};
        // SAFETY: SHCNE_ASSOCCHANGED takes no items.
        unsafe {
            SHChangeNotify(
                SHCNE_ASSOCCHANGED as i32,
                SHCNF_IDLIST,
                std::ptr::null(),
                std::ptr::null(),
            )
        };
    }

    /// A per-user file type for `.jsonl` while it lives: `HKCU\Software\Classes\.jsonl` naming a
    /// type of the check's own, whose `open` command is the command given. Both keys are removed
    /// when it is dropped (the session removes them too if the app ends first).
    pub(super) struct JsonlClass;

    impl JsonlClass {
        /// Refused when either key exists already: those would be the person's own file types.
        pub(super) fn register(command: &str) -> Result<JsonlClass, String> {
            use windows_sys::Win32::System::Registry::{
                RegSetKeyValueW, HKEY_CURRENT_USER, REG_SZ,
            };
            for key in [JSONL, CLASS] {
                if exists(key) {
                    return Err(format!(
                        r"HKCU\{key} exists already: this check registers .jsonl only where no file type of the user's names it"
                    ));
                }
            }
            // From here on, dropping it removes what it set.
            let class = JsonlClass;
            let set = |key: &str, value: &str| {
                let (k, v) = (wide(key), wide(value));
                // SAFETY: both strings are NUL-terminated; v's size is in bytes with its NUL.
                let r = unsafe {
                    RegSetKeyValueW(
                        HKEY_CURRENT_USER,
                        k.as_ptr(),
                        std::ptr::null(),
                        REG_SZ,
                        v.as_ptr().cast(),
                        (v.len() * 2) as u32,
                    )
                };
                if r == 0 {
                    Ok(())
                } else {
                    Err(format!(r"setting HKCU\{key}: error {r}"))
                }
            };
            set(JSONL, "PluxyTest.jsonl")?;
            set(&format!(r"{CLASS}\shell\open\command"), command)?;
            changed();
            Ok(class)
        }
    }

    impl Drop for JsonlClass {
        fn drop(&mut self) {
            use windows_sys::Win32::System::Registry::{RegDeleteTreeW, HKEY_CURRENT_USER};
            for key in [JSONL, CLASS] {
                // SAFETY: the key name is NUL-terminated.
                unsafe { RegDeleteTreeW(HKEY_CURRENT_USER, wide(key).as_ptr()) };
            }
            changed();
        }
    }
}

#[cfg(target_os = "macos")]
mod mac {
    use std::path::{Path, PathBuf};
    use std::process::{Command, Stdio};
    use std::time::{Duration, Instant};
    use tauri::AppHandle;

    pub(super) const TEXTEDIT: &str = "com.apple.TextEdit";

    /// `osascript` with script's lines and args (its `argv`): its output, once it exited 0. At
    /// most 30 s: an Apple event held for a person's consent would never return.
    fn osascript(script: &[&str], args: &[&str]) -> Result<String, String> {
        let mut c = Command::new("/usr/bin/osascript");
        for l in script {
            c.arg("-e").arg(l);
        }
        c.args(args)
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        let mut child = c.spawn().map_err(|e| format!("osascript: {e}"))?;
        let deadline = Instant::now() + Duration::from_secs(30);
        while child.try_wait().map_err(|e| e.to_string())?.is_none() {
            if Instant::now() > deadline {
                // The osascript this check started and holds.
                let _ = child.kill();
                let _ = child.wait();
                return Err(format!(
                    "osascript {script:?} did not answer within 30 s (a consent prompt?)"
                ));
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        let o = child.wait_with_output().map_err(|e| e.to_string())?;
        if !o.status.success() {
            return Err(format!(
                "osascript {script:?} exited with {}: {}",
                o.status,
                String::from_utf8_lossy(&o.stderr).trim()
            ));
        }
        Ok(String::from_utf8_lossy(&o.stdout).trim_end().to_string())
    }

    /// Whether the app with bundle ID id runs (which sends it nothing).
    pub(super) fn running(id: &str) -> Result<bool, String> {
        let r = osascript(
            &[
                "on run argv",
                "return (application id (item 1 of argv) is running) as text",
                "end run",
            ],
            &[id],
        )?;
        Ok(r == "true")
    }

    /// Asks the app with bundle ID id to quit, as its Quit command does.
    pub(super) fn quit(id: &str) -> Result<(), String> {
        osascript(
            &[
                "on run argv",
                "tell application id (item 1 of argv) to quit",
                "end run",
            ],
            &[id],
        )
        .map(|_| ())
    }

    /// The documents TextEdit's windows show, as their `file://` addresses and the files those
    /// name (resolved, as the session's own paths are), read through System Events' accessibility
    /// (each window's `AXDocument`): never an Apple event to TextEdit, which a runner refuses.
    pub(super) fn textedit_documents() -> Result<Vec<(String, PathBuf)>, String> {
        let out = osascript(
            &[
                "set out to \"\"",
                "tell application \"System Events\"",
                "repeat with p in (every process whose bundle identifier is \"com.apple.TextEdit\")",
                "repeat with w in (windows of p)",
                "try",
                "set out to out & (value of attribute \"AXDocument\" of w) & linefeed",
                "end try",
                "end repeat",
                "end repeat",
                "end tell",
                "return out",
            ],
            &[],
        )?;
        Ok(out
            .lines()
            .filter(|l| !l.is_empty())
            .map(|u| {
                let p = file_path(u);
                let p = std::fs::canonicalize(&p).unwrap_or(p);
                (u.to_string(), p)
            })
            .collect())
    }

    /// The file a `file://` address names (its percent escapes decoded); else the address as is.
    fn file_path(url: &str) -> PathBuf {
        let Some(rest) = url.strip_prefix("file://") else {
            return PathBuf::from(url);
        };
        let b = rest.as_bytes();
        let mut out = Vec::with_capacity(b.len());
        let mut i = 0;
        while i < b.len() {
            let hex = (b[i] == b'%')
                .then(|| b.get(i + 1..i + 3))
                .flatten()
                .and_then(|h| std::str::from_utf8(h).ok())
                .and_then(|h| u8::from_str_radix(h, 16).ok());
            match hex {
                Some(v) => {
                    out.push(v);
                    i += 3;
                }
                None => {
                    out.push(b[i]);
                    i += 1;
                }
            }
        }
        PathBuf::from(String::from_utf8_lossy(&out).into_owned())
    }

    /// Clicks the close button of each of TextEdit's windows that shows the document at url
    /// (unchanged, so none asks to save), through System Events' accessibility; returns what the
    /// script skipped. It does not prove a window closed: the caller looks at the windows' documents.
    ///
    /// A window's index in System Events' list shifts when windows close or come forward, so a
    /// `repeat with w in (windows of p)` reference (`item N of every window`) fails with -1719
    /// when its window went. The script instead walks the indexes from last to first (a close
    /// leaves the lower ones in place), reads and clicks each window inside a `try`, and
    /// returns the errors it skipped; a window it missed still shows the document, so the caller's
    /// next look finds it and clicks again.
    pub(super) fn textedit_close(url: &str) -> Result<String, String> {
        osascript(
            &[
                "on run argv",
                "set skipped to \"\"",
                "tell application \"System Events\"",
                "repeat with p in (every process whose bundle identifier is \"com.apple.TextEdit\")",
                "set n to 0",
                "try",
                "set n to count of windows of p",
                "on error m",
                "set skipped to skipped & m & linefeed",
                "end try",
                "repeat with i from n to 1 by -1",
                "try",
                "set w to window i of p",
                "set d to value of attribute \"AXDocument\" of w",
                "if d is (item 1 of argv) then click (first button of w whose subrole is \"AXCloseButton\")",
                "on error m",
                "set skipped to skipped & m & linefeed",
                "end try",
                "end repeat",
                "end repeat",
                "end tell",
                "return skipped",
                "end run",
            ],
            &[url],
        )
    }

    /// Quits TextEdit with its own Quit command (its application menu's "Quit TextEdit", as an
    /// English system names it), through System Events' accessibility.
    pub(super) fn textedit_quit() -> Result<(), String> {
        osascript(
            &[
                "tell application \"System Events\"",
                "repeat with p in (every process whose bundle identifier is \"com.apple.TextEdit\")",
                "click menu item \"Quit TextEdit\" of menu 1 of menu bar item \"TextEdit\" of menu bar 1 of p",
                "end repeat",
                "end tell",
            ],
            &[],
        )
        .map(|_| ())
    }

    /// Changes path's mode or access list with `chmod`.
    pub(super) fn chmod(args: &[&str], path: &Path) -> Result<(), String> {
        let o = Command::new("/bin/chmod")
            .args(args)
            .arg(path)
            .output()
            .map_err(|e| format!("chmod: {e}"))?;
        if !o.status.success() {
            return Err(format!(
                "chmod {args:?} {}: {}",
                path.display(),
                String::from_utf8_lossy(&o.stderr).trim()
            ));
        }
        Ok(())
    }

    /// The app LaunchServices opens url with (its bundle's path).
    pub(super) fn default_app(app: &AppHandle, url: &str) -> Option<String> {
        let url = url.to_string();
        crate::native_test::on_main(app, move || {
            use objc2_app_kit::NSWorkspace;
            use objc2_foundation::{NSString, NSURL};
            let u = NSURL::URLWithString(&NSString::from_str(&url))?;
            let a = NSWorkspace::sharedWorkspace().URLForApplicationToOpenURL(&u)?;
            a.path().map(|p| p.to_string())
        })
        .flatten()
    }

    /// The bundle ID of the app bundle at path, from its Info.plist.
    pub(super) fn bundle_id(path: &str) -> Result<String, String> {
        let info = Path::new(path).join("Contents").join("Info.plist");
        plist::Value::from_file(&info)
            .map_err(|e| format!("{}: {e}", info.display()))?
            .as_dictionary()
            .and_then(|d| d.get("CFBundleIdentifier"))
            .and_then(plist::Value::as_string)
            .map(String::from)
            .ok_or(format!("{} names no bundle ID", info.display()))
    }
}
