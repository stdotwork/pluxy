//! The native test's signed application update check, in native-test builds only
//! (`native/update.ts` runs it, `native/update-session.ts` launches the app). It drives Version
//! Management as the person would (real DOM clicks, the real GTK confirmation through X input)
//! against the session's fake https feed, whose releases the session switches by the word this
//! check writes into `<dir>/feed-variant`. `PLUXY_APP_TEST_UPDATE_CASE` names the launch:
//!
//! - `no-roots`: the test package without its trust profile: Update Now is refused (and the
//!   first launch sets the feed, confirmed natively, for the later ones on the same profile);
//! - `deb`, `rpm`, `unknown`: the test package's program, its bundle type patched as a .deb, an
//!   .rpm or no package would have it: refused before any download, with how to update instead;
//! - `update`: the test package itself, in managed mode. The app restarts itself on this route,
//!   so each process reads where it is from `<dir>/stage`: lower feed, bad signature, a signed
//!   version that is not the announced one and a cancelled download change nothing; an update
//!   whose new version does not accept itself (`<dir>/refuse-accept`) is undone; then v1→v2 for
//!   real; then a rollback refused for a version never installed and for a changed kept copy,
//!   cancelled in its native confirmation, and confirmed back to v1.
//!
//! Each step prints `native-test: update_<step> <JSON>`; the session checks them against what it
//! saw itself (its feed's requests, the installed file's digest, the privileged-tool log).
use crate::confirmation::native::{key, window};
use crate::native_test::{eval, log, wait_for};
use crate::release_native::{attr, click, dialog_text, set_source, wait_attr};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::fs;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

const DIALOG: &str = "pluxy native confirmation";
const APP_CARD: &str = "[data-part=release-product][data-product=app]";
const UPDATE: &str = "[data-part=app-update]";

fn file_sha(path: &Path) -> String {
    fs::read(path)
        .map(|b| {
            Sha256::digest(&b)
                .iter()
                .map(|x| format!("{x:02x}"))
                .collect()
        })
        .unwrap_or_default()
}

fn installed() -> PathBuf {
    std::env::var_os("APPIMAGE")
        .map(PathBuf::from)
        .unwrap_or_default()
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

/// A refused launch: Update Now is not offered, the page says why, and the command refuses too.
fn refused(main: &Webview, dir: &Path, case: &str) -> Result<(), String> {
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
    log(
        &format!("update_{}", case.replace('-', "_")),
        json!({"reason": reason, "offered": offered, "call": call,
               "installed_sha": file_sha(&installed())}),
    );
    Ok(())
}

fn stage(dir: &Path) -> String {
    fs::read_to_string(dir.join("stage")).unwrap_or_default()
}

fn set_stage(dir: &Path, s: &str) -> Result<(), String> {
    fs::write(dir.join("stage"), s).map_err(|e| e.to_string())
}

/// What every process on the update route reports first: its version, the installed file's
/// digest, its own core, what the page says about the last update and the versions it can go
/// back to.
fn report(app: &AppHandle, main: &Webview, dir: &Path, stage: &str) -> Result<Value, String> {
    check(main, dir, "good", "0.2.0")?;
    let core = wait_for("the app's own core", || {
        app.state::<crate::App>().conn.lock().unwrap().core_pid()
    })?;
    let r = json!({
        "stage": stage,
        "version": app.package_info().version.to_string(),
        "shown": attr(main, APP_CARD, "installed"),
        "installed_sha": file_sha(&installed()),
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
    let before = file_sha(&installed());

    // A feed whose newest app release is not newer: nothing to install.
    check(main, dir, "lower", "0.1.0")?;
    let lower = json!({
        "available": update_attr(main, "available"),
        "offered": eval(main, "document.querySelector('[data-part=app-update-now]') !== null"),
        "call": invoke(main, "app_update_start", json!({}))?,
    });

    // Signed by a key the app does not trust (with the trusted key's ID): refused after download.
    check(main, dir, "badsig", "0.2.0")?;
    click(main, "[data-part=app-update-now]")?;
    phase(main, "the bad signature refused", &["failed"])?;
    let badsig =
        json!({"error": update_attr(main, "error"), "installed_sha": file_sha(&installed())});

    // Announced as 0.3.0 with 0.2.0's artifact and signature: the signed version differs.
    check(main, dir, "mismatch", "0.3.0")?;
    click(main, "[data-part=app-update-now]")?;
    phase(main, "the signed version refused", &["failed"])?;
    let mismatch =
        json!({"error": update_attr(main, "error"), "installed_sha": file_sha(&installed())});

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
    let cancelled = json!({"installed_sha": file_sha(&installed()), "downloaded": update_attr(main, "downloaded")});

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
    set_stage(dir, "updated")?;
    update_now(main)
}

fn updated(app: &AppHandle, main: &Webview, dir: &Path) -> Result<(), String> {
    let r = report(app, main, dir, "updated")?;
    let v1 = fs::read_to_string(dir.join("v1.sha256")).map_err(|e| e.to_string())?;
    let v1 = v1.trim();
    // Refused without a confirmation: a version never installed here.
    let unknown = invoke(
        main,
        "app_update_rollback",
        json!({"version": "0.0.1", "sha256": "0".repeat(64)}),
    )?;
    let no_dialog = window(DIALOG).is_none();
    // Refused without a confirmation: the kept copy no longer has its pinned digest.
    let kept = app
        .path()
        .app_data_dir()
        .map_err(|e| e.to_string())?
        .join("updates")
        .join("kept")
        .join(format!("0.1.0-{v1}.AppImage"));
    let original = fs::read(&kept).map_err(|e| format!("{}: {e}", kept.display()))?;
    let mut changed = original.clone();
    changed.push(0);
    fs::write(&kept, &changed).map_err(|e| e.to_string())?;
    let tampered = invoke(
        main,
        "app_update_rollback",
        json!({"version": "0.1.0", "sha256": v1}),
    )?;
    let no_dialog_tampered = window(DIALOG).is_none();
    fs::write(&kept, &original).map_err(|e| e.to_string())?;
    // Cancelled in the native confirmation: nothing changes.
    let button = "[data-part=app-rollback][data-version=\"0.1.0\"]";
    click(main, button)?;
    wait_for("the rollback's native confirmation", || window(DIALOG))?;
    let text = dialog_text(app)?;
    key(DIALOG, "alt+c")?;
    wait_for("the cancelled rollback to settle", || {
        (window(DIALOG).is_none() && update_attr(main, "phase") != "restarting").then_some(())
    })?;
    std::thread::sleep(Duration::from_secs(1));
    let cancelled =
        json!({"installed_sha": file_sha(&installed()), "phase": update_attr(main, "phase")});
    log(
        "update_rollback_refusals",
        json!({"unknown": unknown, "no_dialog": no_dialog, "tampered": tampered,
               "no_dialog_tampered": no_dialog_tampered, "dialog": text,
               "cancelled": cancelled, "v2_sha": r["installed_sha"]}),
    );
    // Confirmed: back to 0.1.0.
    set_stage(dir, "rolled-back")?;
    click(main, button)?;
    wait_for("the rollback's native confirmation", || window(DIALOG))?;
    key(DIALOG, "alt+o")?;
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
            refused(main, &dir, &case)?
        }
        "deb" | "rpm" | "unknown" => refused(main, &dir, &case)?,
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
