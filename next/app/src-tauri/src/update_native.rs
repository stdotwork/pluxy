//! The native test's signed application update check, in native-test builds only, on Linux,
//! Windows and macOS (`native/update-cases.ts` holds the cases; `native/update-session.ts` runs
//! them on Linux, `native/platform.ts`'s update session on Windows and macOS). It drives Version
//! Management as the person would (real DOM clicks, the real native confirmation through the UI
//! driver, `ui_driver.rs`) against the session's fake https feed, whose releases the session
//! switches by the word this check writes into `<dir>/feed-variant`. `PLUXY_APP_TEST_UPDATE_CASE`
//! names the launch:
//!
//! - `no-roots`: the test package without its trust profile: Update Now is refused (and the
//!   first launch sets the feed, confirmed natively, for the later ones on the same profile);
//! - a crafted install the updater must refuse before any download, with how to update instead:
//!   `deb`, `rpm`, `unknown` (Linux: the program with the bundle type a .deb, an .rpm or no
//!   package gives it), `msi`, `nsis`, `shared` (Windows: an .msi- or setup-program-patched
//!   program, a folder others can change), `bare`, `group-writable` (macOS: the program outside
//!   its app bundle, a bundle others can change);
//! - `open` (macOS): the test package started by LaunchServices (`/usr/bin/open`): an update is
//!   offered, so its provenance holds for such a launch; then it quits;
//! - `update`: the test package itself. The app restarts itself on this route, so each process
//!   reads where it is from `<dir>/stage`: lower feed, bad signature, a signed version that is not
//!   the announced one and a cancelled download change nothing; an update whose new version does
//!   not accept itself (`<dir>/refuse-accept`) is undone; then v1→v2 for real, the new version
//!   holding its acceptance open (`<dir>/hold-gap`) while the session launches the app a third
//!   time; then a rollback refused for a version never installed and for a changed kept copy,
//!   cancelled in its native confirmation, and confirmed back to v1.
//!
//! Each step prints `native-test: update_<step> <JSON>`; the session checks them against what it
//! saw itself (its feed's requests, the installed copy's digest, the stand-ins' log).
use crate::native_test::ui::{self, Key};
use crate::native_test::{eval, log, wait_for};
use crate::release_native::{attr, click, dialog_text, set_source, wait_attr};
use crate::updater::{digest, installed_here, kept_path, Pinned};
use serde_json::{json, Value};
use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

const DIALOG: &str = "pluxy native confirmation";
const APP_CARD: &str = "[data-part=release-product][data-product=app]";
const UPDATE: &str = "[data-part=app-update]";

/// The installed copy this process runs from (the AppImage, the program or the app bundle).
fn installed(app: &AppHandle) -> Option<PathBuf> {
    installed_here(app)
}

/// The installed copy's digest as the updater pins it, or "" when there is none.
fn installed_sha(app: &AppHandle) -> String {
    installed(app)
        .and_then(|p| digest(&p).ok())
        .unwrap_or_default()
}

/// What is next to the installed copy, by name (what an install may leave there, and the copy).
fn beside(app: &AppHandle) -> Vec<String> {
    let Some(parent) = installed(app).and_then(|p| p.parent().map(Path::to_path_buf)) else {
        return vec![];
    };
    let mut names: Vec<String> = fs::read_dir(parent)
        .map(|d| {
            d.flatten()
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect()
        })
        .unwrap_or_default();
    names.sort();
    names
}

/// The result of invoking command with args from the window, as `{ok, error}`.
fn invoke(main: &Webview, command: &str, args: Value) -> Result<Value, String> {
    main.eval(format!(
        "window.__updateCall = null; window.__TAURI_INTERNALS__.invoke({}, {}).then(v => window.__updateCall = {{ok: true, value: v ?? null}}, e => window.__updateCall = {{ok: false, error: String(e)}})",
        json!(command),
        args
    ))
    .map_err(|e| e.to_string())?;
    wait_for(&format!("{command} to answer"), || {
        eval(main, "window.__updateCall").filter(|v| !v.is_null())
    })
}

/// Waits up to secs for f; for what must already be there, so a missing feature fails fast.
fn soon<T>(what: &str, secs: u64, mut f: impl FnMut() -> Option<T>) -> Result<T, String> {
    let deadline = Instant::now() + Duration::from_secs(secs);
    loop {
        if let Some(v) = f() {
            return Ok(v);
        }
        if Instant::now() > deadline {
            return Err(format!("timed out waiting for {what}"));
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

fn update_attr(main: &Webview, name: &str) -> Value {
    attr(main, UPDATE, name)
}

/// Opens Version Management with the feed's `variant` releases checked, once the app's latest
/// version is `latest`.
fn check(main: &Webview, dir: &Path, variant: &str, latest: &str) -> Result<(), String> {
    fs::write(dir.join("feed-variant"), variant).map_err(|e| e.to_string())?;
    main.eval("location.hash = '#/versions'")
        .map_err(|e| e.to_string())?;
    wait_attr(
        main,
        "the feed",
        "[data-part=release-source]",
        "source",
        &feed()?,
    )?;
    let before = update_attr(main, "check");
    click(main, "[data-part=release-check]")?;
    wait_attr(
        main,
        &format!("the {variant} feed's app release"),
        APP_CARD,
        "latest",
        latest,
    )?;
    soon("the app's update controls", 15, || {
        eval(
            main,
            &format!("document.querySelector({}) !== null", json!(UPDATE)),
        )
        .filter(|v| v == true)
    })?;
    // What the updater says about this check, not the one before.
    wait_for("the updater's view of this check", || {
        (update_attr(main, "check") != before).then_some(())
    })?;
    Ok(())
}

fn feed() -> Result<String, String> {
    std::env::var("PLUXY_APP_TEST_FEED").map_err(|_| "PLUXY_APP_TEST_FEED is not set".into())
}

/// The phase once it is one of want.
fn phase(main: &Webview, what: &str, want: &[&str]) -> Result<String, String> {
    wait_for(what, || {
        update_attr(main, "phase")
            .as_str()
            .filter(|p| want.contains(p))
            .map(str::to_string)
    })
    .map_err(|e| {
        format!(
            "{e}: the update shows {:?}",
            eval(
                main,
                &format!("document.querySelector({})?.dataset ?? null", json!(UPDATE))
            )
        )
    })
}

/// The update's phase and error as the page shows them, read together: two reads could straddle
/// a new task (its phase from the last one, its error from the new one).
fn outcome_now(main: &Webview) -> Value {
    eval(
        main,
        &format!(
            "(d => d ? {{phase: d.phase, error: d.error}} : null)(document.querySelector({})?.dataset)",
            json!(UPDATE)
        ),
    )
    .unwrap_or(Value::Null)
}

/// Clicks Update Now and waits for that update's own outcome, one of want: a phase in want, read
/// with its error, that is not the outcome shown before the click (a step after a failed one
/// would otherwise take the leftover failure for its own); a failure must say why.
fn update_outcome(main: &Webview, what: &str, want: &[&str]) -> Result<Value, String> {
    let before = outcome_now(main);
    click(main, "[data-part=app-update-now]")?;
    wait_for(what, || {
        let now = outcome_now(main);
        let phase = now["phase"].as_str()?;
        let done = want.contains(&phase)
            && now != before
            && (phase != "failed" || now["error"].as_str().is_some_and(|e| !e.is_empty()));
        done.then_some(now)
    })
    .map_err(|e| {
        format!(
            "{e}: the update shows {}, before the click {before}",
            outcome_now(main)
        )
    })
}

/// A refused launch: Update Now is not offered, the page says why, and the command refuses too.
fn refused(app: &AppHandle, main: &Webview, dir: &Path, case: &str) -> Result<(), String> {
    check(main, dir, "good", "0.2.0")?;
    let reason = wait_for("the reason there is no update", || {
        (update_attr(main, "available") == "false")
            .then(|| update_attr(main, "reason"))
            .filter(|r| r.as_str().is_some_and(|s| !s.is_empty()))
    })?;
    let offered = eval(
        main,
        "(b => !!b && !b.disabled)(document.querySelector('[data-part=app-update-now] button'))",
    );
    let call = invoke(main, "app_update_start", json!({}))?;
    // A rollback is refused for the same reason, before it looks at any kept copy.
    let rollback = invoke(
        main,
        "app_update_rollback",
        json!({"version": "0.0.1", "sha256": "0".repeat(64)}),
    )?;
    log(
        &format!("update_{}", case.replace('-', "_")),
        json!({"reason": reason, "offered": offered, "call": call, "rollback": rollback,
               "installed_sha": installed_sha(app)}),
    );
    Ok(())
}

fn stage(dir: &Path) -> String {
    fs::read_to_string(dir.join("stage")).unwrap_or_default()
}

fn set_stage(dir: &Path, s: &str) -> Result<(), String> {
    fs::write(dir.join("stage"), s).map_err(|e| e.to_string())
}

/// What every process on the update route reports first: its version and process, the installed
/// copy's digest and what is next to it (once an earlier install's leftovers are gone, or after
/// 30 s), its own core (managed mode), what the page says about the last update and the versions
/// it can go back to.
fn report(app: &AppHandle, main: &Webview, dir: &Path, stage: &str) -> Result<Value, String> {
    check(main, dir, "good", "0.2.0")?;
    let core = if app.state::<crate::App>().conn.lock().unwrap().mode()
        == Some(crate::conn::Mode::Managed)
    {
        json!(wait_for("the app's own core", || {
            app.state::<crate::App>().conn.lock().unwrap().core_pid()
        })?)
    } else {
        Value::Null
    };
    let own = installed(app).and_then(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()));
    let _ = soon("only the installed copy next to it", 30, || {
        (beside(app) == own.iter().cloned().collect::<Vec<_>>()).then_some(())
    });
    let r = json!({
        "stage": stage,
        "version": app.package_info().version.to_string(),
        "pid": std::process::id(),
        "shown": attr(main, APP_CARD, "installed"),
        "installed": installed(app),
        "installed_sha": installed_sha(app),
        "beside": beside(app),
        "core_pid": core,
        "last": eval(main, "(e => e ? {result: e.dataset.result, detail: e.dataset.detail} : null)(document.querySelector('[data-part=app-update-last]'))"),
        "rollback": eval(main, "[...document.querySelectorAll('[data-part=app-rollback]')].map(e => ({version: e.dataset.version, sha256: e.dataset.sha256}))"),
    });
    log("update_stage", r.clone());
    Ok(r)
}

/// Starts the update the page offers and waits for this process to be replaced.
fn update_now(main: &Webview) -> Result<(), String> {
    click(main, "[data-part=app-update-now]")?;
    phase(main, "the update to install", &["restarting"])?;
    // The app quits and starts its new version: this process ends before the wait does.
    std::thread::sleep(Duration::from_secs(120));
    Err("the app did not restart after installing the update".into())
}

fn first(app: &AppHandle, main: &Webview, dir: &Path) -> Result<(), String> {
    report(app, main, dir, "first")?;
    let before = installed_sha(app);

    // A feed whose newest app release is not newer: nothing to install.
    check(main, dir, "lower", "0.1.0")?;
    let lower = json!({
        "available": update_attr(main, "available"),
        "offered": eval(main, "document.querySelector('[data-part=app-update-now]') !== null"),
        "call": invoke(main, "app_update_start", json!({}))?,
    });

    // Signed by a key the app does not trust (with the trusted key's ID): refused after download.
    check(main, dir, "badsig", "0.2.0")?;
    let o = update_outcome(main, "the bad signature refused", &["failed"])?;
    let badsig = json!({"error": o["error"], "installed_sha": installed_sha(app)});

    // Announced as 0.3.0 with 0.2.0's artifact and signature: the signed version differs.
    // The page still shows the bad signature's failure: this step waits for its own.
    check(main, dir, "mismatch", "0.3.0")?;
    let o = update_outcome(main, "the signed version refused", &["failed"])?;
    let mismatch = json!({"error": o["error"], "installed_sha": installed_sha(app)});

    // A slow download, cancelled part way.
    check(main, dir, "slow", "0.2.0")?;
    click(main, "[data-part=app-update-now]")?;
    wait_for("some of the download", || {
        (update_attr(main, "phase") == "downloading"
            && update_attr(main, "downloaded")
                .as_str()
                .and_then(|d| d.parse::<u64>().ok())
                .is_some_and(|d| d > 0))
        .then_some(())
    })?;
    click(main, "[data-part=app-update-cancel]")?;
    phase(main, "the cancelled download", &["cancelled"])?;
    let cancelled =
        json!({"installed_sha": installed_sha(app), "downloaded": update_attr(main, "downloaded")});

    // The window holds no updater permission: the plugin's own commands are refused to it.
    let plugin = invoke(main, "plugin:updater|check", json!({}))?;
    log(
        "update_refusals",
        json!({"before_sha": before, "lower": lower, "badsig": badsig, "mismatch": mismatch,
               "cancelled": cancelled, "plugin": plugin}),
    );

    // A valid update whose new version will not accept itself: the app puts this one back.
    fs::write(dir.join("refuse-accept"), "").map_err(|e| e.to_string())?;
    check(main, dir, "good", "0.2.0")?;
    set_stage(dir, "refused")?;
    update_now(main)
}

fn after_refused(app: &AppHandle, main: &Webview, dir: &Path) -> Result<(), String> {
    report(app, main, dir, "refused")?;
    log(
        "update_refused",
        json!({"hook_left": dir.join("refuse-accept").exists()}),
    );
    // The new version holds its acceptance open, for the session's third launch (`gap`).
    fs::write(dir.join("hold-gap"), "").map_err(|e| e.to_string())?;
    set_stage(dir, "updated")?;
    update_now(main)
}

/// While this new version holds its acceptance open (the earlier version still waiting): hides
/// the window, says so (the session then launches the app a third time on the same profile),
/// and reports whether that launch showed the window again; returns once the session has ended
/// the gap.
fn gap(app: &AppHandle, dir: &Path) -> Result<(), String> {
    if !dir.join("hold-gap").exists() {
        return Ok(());
    }
    let me = std::process::id().to_string();
    soon("this version to hold its acceptance", 30, || {
        fs::read_to_string(dir.join("gap"))
            .ok()
            .filter(|p| p.trim() == me)
    })?;
    let w = app.get_window(crate::WINDOW).ok_or("no window")?;
    w.hide().map_err(|e| e.to_string())?;
    soon("the window to hide", 15, || {
        (w.is_visible().ok() == Some(false)).then_some(())
    })?;
    log("update_gap_hidden", json!({"pid": std::process::id()}));
    let raised = soon("a later launch to show the window", 60, || {
        (w.is_visible().ok() == Some(true)).then_some(())
    })
    .is_ok();
    log(
        "update_gap_raised",
        json!({"pid": std::process::id(), "visible": raised}),
    );
    soon("the session to end the gap", 90, || {
        (!dir.join("hold-gap").exists()).then_some(())
    })?;
    // Then this version accepts itself (its record of the update written, its pending record
    // gone): what the page says about the last update is read after that. One that never accepts
    // shows in the report the session checks.
    let pending = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("updates")
        .join("pending.json");
    let _ = soon("this version to accept itself", 30, || {
        (!pending.exists()).then_some(())
    });
    Ok(())
}

/// Makes the kept copy at path differ from its pin (a file: one more byte; an app bundle: one
/// more file) and returns how to undo it.
fn tamper(path: &Path) -> Result<impl FnOnce() -> Result<(), String>, String> {
    let (file, original) = if path.is_dir() {
        (path.join("Contents").join("pluxy-tampered"), None)
    } else {
        let b = fs::read(path).map_err(|e| format!("{}: {e}", path.display()))?;
        (path.to_path_buf(), Some(b))
    };
    let mut changed = original.clone().unwrap_or_default();
    changed.push(0);
    fs::write(&file, &changed).map_err(|e| format!("{}: {e}", file.display()))?;
    Ok(move || {
        match original {
            Some(b) => fs::write(&file, b),
            None => fs::remove_file(&file),
        }
        .map_err(|e| format!("{}: {e}", file.display()))
    })
}

fn updated(app: &AppHandle, main: &Webview, dir: &Path) -> Result<(), String> {
    gap(app, dir)?;
    let r = report(app, main, dir, "updated")?;
    let v1 = fs::read_to_string(dir.join("v1.sha256")).map_err(|e| e.to_string())?;
    let v1 = v1.trim();
    // Refused without a confirmation: a version never installed here.
    let unknown = invoke(
        main,
        "app_update_rollback",
        json!({"version": "0.0.1", "sha256": "0".repeat(64)}),
    )?;
    let no_dialog = !ui::shown(app, DIALOG);
    // Refused without a confirmation: the kept copy no longer has its pinned digest.
    let kept = kept_path(
        &app.path()
            .app_data_dir()
            .map_err(|e| e.to_string())?
            .join("updates"),
        &Pinned {
            version: "0.1.0".into(),
            sha256: v1.into(),
        },
    );
    let kept_found = kept.exists();
    let undo = tamper(&kept)?;
    let tampered = invoke(
        main,
        "app_update_rollback",
        json!({"version": "0.1.0", "sha256": v1}),
    );
    let no_dialog_tampered = !ui::shown(app, DIALOG);
    undo()?;
    let tampered = tampered?;
    // Cancelled in the native confirmation: nothing changes.
    let button = "[data-part=app-rollback][data-version=\"0.1.0\"]";
    click(main, button)?;
    ui::wait_shown(app, DIALOG)?;
    let text = dialog_text(app)?;
    ui::press(app, DIALOG, Key::Access('c'))?;
    wait_for("the cancelled rollback to settle", || {
        (!ui::shown(app, DIALOG) && update_attr(main, "phase") != "restarting").then_some(())
    })?;
    std::thread::sleep(Duration::from_secs(1));
    let cancelled =
        json!({"installed_sha": installed_sha(app), "phase": update_attr(main, "phase")});
    log(
        "update_rollback_refusals",
        json!({"unknown": unknown, "no_dialog": no_dialog, "kept_found": kept_found,
               "tampered": tampered, "no_dialog_tampered": no_dialog_tampered, "dialog": text,
               "cancelled": cancelled, "v2_sha": r["installed_sha"]}),
    );
    // Confirmed: back to 0.1.0.
    set_stage(dir, "rolled-back")?;
    click(main, button)?;
    ui::wait_shown(app, DIALOG)?;
    ui::press(app, DIALOG, Key::Access('o'))?;
    phase(main, "the rollback to install", &["restarting"])?;
    std::thread::sleep(Duration::from_secs(120));
    Err("the app did not restart after the rollback".into())
}

pub(crate) fn run(app: &AppHandle, main: &Webview, dir: PathBuf) -> Result<(), String> {
    let case = std::env::var("PLUXY_APP_TEST_UPDATE_CASE").unwrap_or_default();
    match case.as_str() {
        "no-roots" => {
            // The first launch on this profile: the feed, confirmed natively.
            main.eval("location.hash = '#/versions'")
                .map_err(|e| e.to_string())?;
            set_source(app, main, &feed()?)?;
            refused(app, main, &dir, &case)?
        }
        "deb" | "rpm" | "unknown" | "msi" | "nsis" | "shared" | "bare" | "group-writable"
        | "linked" => refused(app, main, &dir, &case)?,
        "open" => {
            check(main, &dir, "good", "0.2.0")?;
            let available = wait_for("the updater's view", || {
                Some(update_attr(main, "available")).filter(|a| !a.is_null())
            })?;
            log(
                "update_open",
                json!({"available": available, "reason": update_attr(main, "reason"),
                       "installed": installed(app), "installed_sha": installed_sha(app),
                       "pid": std::process::id()}),
            );
        }
        "update" => match stage(&dir).as_str() {
            "" => return first(app, main, &dir),
            "refused" => return after_refused(app, main, &dir),
            "updated" => return updated(app, main, &dir),
            "rolled-back" => {
                report(app, main, &dir, "rolled-back")?;
                log("update_done", json!({}));
            }
            s => return Err(format!("unknown update stage {s:?}")),
        },
        c => return Err(format!("unknown update case {c:?}")),
    }
    log("quit", json!({}));
    crate::quit(app);
    Ok(())
}
