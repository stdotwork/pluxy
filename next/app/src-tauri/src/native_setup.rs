//! The native test's Setup checks. `native/setup.ts` starts the app once per stage, with
//! `PLUXY_APP_TEST_SETUP=<stage>`, in one session (its HOME, X server and bus), in managed mode on
//! a first run: the session's core has nothing installed and no accounts, its bundled registry
//! offers `pluxy/claude` as a pick, and a stand-in `claude` that prints `2.4.1 (Claude Code)`
//! comes first on the PATH core inherits from the app. A curated device registry offers
//! `pluxy/claude-code`, the Claude Code page's package, so the app offers its "Set up" button.
//! The bundled test packages are not installed (`native_test.rs`).
//!
//! - `first`: the window opens on Setup by itself (`#/setup`, "Set up pluxy"); "Found on this
//!   machine" names Claude Code 2.4.1; the Claude pick is ticked, saying why; the Claude Code
//!   client row has "Set up" beside "Show what to paste"; Start installs the pick (its badge says
//!   Installed, and `GET /packages` through the app's host lists it enabled); Finish lands on the
//!   Dashboard and `GET /setup` has `first_run` false.
//! - `second`: the next launch opens on the Dashboard, not Setup; Setup, opened, says "Setup is
//!   finished." and has no Finish button; the stage stays on it, then switches GTK to its dark
//!   variant and stays again, so the session's last screenshot shows it dark.
//!
//! Every check prints `native-test: setup_check {"ok":...,"what":...}`; after holding a screen for
//! a screenshot it prints `native-test: setup_shot {"name":...}`; a stage ends with
//! `native-test: setup_report {"stage":...,"failed":...}` and quits.

use super::{eval, log};
use crate::MAIN;
use serde_json::{json, Value};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager};

/// What `GET /setup` names the stand-in's find, as the Found card shows it.
const FOUND: &str = "Claude Code 2.4.1";
const PICK: &str = "pluxy/claude";
/// session.ts captures the screen every 8 seconds: a screen held this long is in a capture.
const HOLD: Duration = Duration::from_secs(12);

#[derive(Default)]
struct Checks {
    failed: usize,
}

impl Checks {
    fn check(&mut self, ok: bool, what: impl Into<String>) -> bool {
        let what = what.into();
        log("setup_check", json!({"ok": ok, "what": what}));
        if !ok {
            self.failed += 1;
        }
        ok
    }
}

fn within<T>(secs: u64, mut f: impl FnMut() -> Option<T>) -> Option<T> {
    let deadline = Instant::now() + Duration::from_secs(secs);
    loop {
        if let Some(v) = f() {
            return Some(v);
        }
        if Instant::now() > deadline {
            return None;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

fn main_eval(app: &AppHandle, expr: &str) -> Option<Value> {
    eval(&app.get_webview(MAIN)?, expr)
}

fn until(app: &AppHandle, secs: u64, expr: &str) -> bool {
    within(secs, || {
        (main_eval(app, expr) == Some(json!(true))).then_some(())
    })
    .is_some()
}

/// Holds the screen so the session's next capture shows it, then says so.
fn shot(name: &str) {
    std::thread::sleep(HOLD);
    log("setup_shot", json!({ "name": name }));
}

/// A call to core through the app's host, as the window's pages make it (`core_call`).
fn core(app: &AppHandle, method: &str, path: &str) -> Value {
    let _ = main_eval(
        app,
        &format!(
            "(window.__pluxySetup = null, window.__TAURI_INTERNALS__.invoke('core_call', {{ method: {}, path: {}, body: null }}).then((r) => {{ window.__pluxySetup = r; }}, (e) => {{ window.__pluxySetup = {{ error: String(e) }}; }}), true)",
            json!(method),
            json!(path)
        ),
    );
    within(15, || {
        main_eval(app, "window.__pluxySetup").filter(|v| !v.is_null())
    })
    .unwrap_or(Value::Null)
}

/// The page's title, its address and its text, for a check that says what it saw instead.
fn page(app: &AppHandle) -> Value {
    main_eval(
        app,
        "({ hash: location.hash, title: document.querySelector('[data-part=page-title]')?.textContent ?? null })",
    )
    .unwrap_or(Value::Null)
}

/// An expression for the client row of Claude Code.
const CLIENT: &str = "[...document.querySelectorAll('[data-part=setup-client]')].find((e) => e.querySelector('strong')?.textContent.startsWith('Claude Code'))";
/// An expression for the Claude pick's row.
const PICK_ROW: &str = "[...document.querySelectorAll('[data-part=setup-pick]')].find((e) => e.querySelector('strong')?.textContent === 'Claude')";

/// The text of the buttons in the Claude Code client row, in order.
fn client_buttons(app: &AppHandle) -> Value {
    main_eval(
        app,
        &format!("(() => {{ const row = {CLIENT}; return row ? [...row.querySelectorAll('.actions button')].map((b) => b.textContent.trim()) : null; }})()"),
    )
    .unwrap_or(Value::Null)
}

/// Presses the button whose text is label inside the card titled card.
fn press(app: &AppHandle, card: &str, label: &str) -> bool {
    until(
        app,
        10,
        &format!(
            "(() => {{ const b = [...document.querySelectorAll('section[aria-label=' + JSON.stringify({card}) + '] button')].find((b) => b.textContent.trim() === {label}); if (!b || b.disabled) return false; b.click(); return true; }})()",
            card = json!(card),
            label = json!(label),
        ),
    )
}

fn shell(c: &mut Checks, app: &AppHandle) -> bool {
    c.check(
        until(
            app,
            60,
            "document.querySelector('[data-part=nav]') !== null",
        ),
        "the window shows the app's shell",
    )
}

fn first(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) {
        return;
    }
    let opened = until(
        app,
        30,
        "location.hash === '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Set up pluxy'",
    );
    c.check(
        opened,
        format!(
            "the window opens on Setup by itself: #/setup, \"Set up pluxy\" ({})",
            page(app)
        ),
    );
    if !opened {
        return;
    }
    let r = core(app, "GET", "/packages");
    c.check(
        r["status"] == 200 && r["body"]["packages"].as_array().is_some_and(|p| p.is_empty()),
        format!("GET /packages through the app's host lists nothing installed at first ({r})"),
    );
    let found = until(
        app,
        30,
        &format!(
            "[...document.querySelectorAll('section[aria-label=\"Found on this machine\"] [data-part=setup-found] strong')].some((e) => e.textContent === {})",
            json!(FOUND)
        ),
    );
    let shown = main_eval(
        app,
        "document.querySelector('section[aria-label=\"Found on this machine\"]')?.innerText ?? null",
    )
    .unwrap_or(Value::Null);
    c.check(
        found,
        format!("\"Found on this machine\" names {FOUND} ({shown})"),
    );
    let ticked = until(
        app,
        15,
        &format!("{PICK_ROW}?.querySelector('input[type=checkbox]')?.checked === true"),
    );
    c.check(ticked, "the Claude pick is ticked");
    c.check(
        until(
            app,
            5,
            &format!("{PICK_ROW}?.textContent.includes('Claude Code is installed on this machine') === true"),
        ),
        "the Claude pick says Claude Code is installed on this machine",
    );
    // The Set up button comes once the device's catalog and the registries' offers have loaded.
    let buttons = within(20, || {
        let b = client_buttons(app);
        (b == json!(["Set up", "Show what to paste"])).then_some(b)
    })
    .unwrap_or_else(|| client_buttons(app));
    c.check(
        buttons == json!(["Set up", "Show what to paste"]),
        format!("the Claude Code client row has \"Set up\" beside \"Show what to paste\" ({buttons})"),
    );
    shot("found");

    if !c.check(press(app, "Start", "Start"), "Start is there, enabled, and pressed") {
        return;
    }
    let installed = until(
        app,
        90,
        &format!("{PICK_ROW}?.querySelector('[data-part=badge]')?.textContent === 'Installed'"),
    );
    let notices = main_eval(
        app,
        "[...document.querySelectorAll('[role=alert], [role=status]')].map((e) => e.textContent).join(' | ')",
    )
    .unwrap_or(Value::Null);
    c.check(
        installed,
        format!("after Start the Claude pick says Installed (notices: {notices})"),
    );
    let r = core(app, "GET", "/packages");
    let listed = r["body"]["packages"]
        .as_array()
        .is_some_and(|p| p.iter().any(|p| p["id"] == PICK && p["enabled"] == true));
    c.check(
        r["status"] == 200 && listed,
        format!("GET /packages through the app's host lists {PICK}, enabled ({r})"),
    );
    shot("started");

    if !c.check(press(app, "Finish", "Finish"), "Finish is there, enabled, and pressed") {
        return;
    }
    c.check(
        until(
            app,
            30,
            "location.hash !== '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Dashboard'",
        ),
        format!("Finish lands on the Dashboard ({})", page(app)),
    );
    let r = core(app, "GET", "/setup");
    c.check(
        r["status"] == 200 && r["body"]["first_run"] == false,
        format!(
            "GET /setup through the app's host has first_run false ({})",
            r["body"]["first_run"]
        ),
    );
}

fn second(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) {
        return;
    }
    let r = core(app, "GET", "/setup");
    c.check(
        r["status"] == 200 && r["body"]["first_run"] == false,
        format!(
            "GET /setup through the app's host still has first_run false ({})",
            r["body"]["first_run"]
        ),
    );
    // The shell asks core about Setup once signed in; give it time to have opened Setup.
    let dashboard = until(
        app,
        30,
        "document.querySelector('[data-part=page-title]')?.textContent === 'Dashboard'",
    );
    std::thread::sleep(Duration::from_secs(3));
    let stayed = main_eval(
        app,
        "location.hash !== '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Dashboard'",
    ) == Some(json!(true));
    c.check(
        dashboard && stayed,
        format!("the next launch opens on the Dashboard, not Setup ({})", page(app)),
    );

    let _ = main_eval(app, "(location.hash = '#/setup', true)");
    c.check(
        until(
            app,
            30,
            "document.querySelector('[data-part=page-title]')?.textContent === 'Set up pluxy' && document.body.innerText.includes('Setup is finished.')",
        ),
        format!("Setup, opened, says \"Setup is finished.\" ({})", page(app)),
    );
    c.check(
        until(
            app,
            15,
            &format!("{PICK_ROW}?.querySelector('[data-part=badge]')?.textContent === 'Installed'"),
        ),
        "Setup lists the Claude pick as Installed",
    );
    let finish = main_eval(
        app,
        "[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Finish')",
    );
    c.check(
        finish == Some(json!(false)),
        format!("Setup has no Finish button ({finish:?})"),
    );
    shot("finished");

    // The same page in GTK's dark variant, which WebKitGTK reports as prefers-color-scheme: dark
    // and the window's theme state follows.
    let _ = app.run_on_main_thread(|| {
        use gtk::prelude::GtkSettingsExt;
        if let Some(s) = gtk::Settings::default() {
            s.set_gtk_application_prefer_dark_theme(true);
        }
    });
    let dark = until(
        app,
        15,
        "matchMedia('(prefers-color-scheme: dark)').matches && document.documentElement.classList.contains('dark')",
    );
    c.check(
        dark,
        "with GTK's dark variant the window paints dark (prefers-color-scheme and the root's dark class)",
    );
    shot("finished-dark");
}

pub fn run(app: &AppHandle, stage: &str) -> Result<(), String> {
    let mut c = Checks::default();
    match stage {
        "first" => first(&mut c, app),
        "second" => second(&mut c, app),
        _ => return Err(format!("no setup stage {stage}")),
    }
    // Also after a stage stopped early, so setup.ts says which checks failed.
    log("setup_report", json!({"stage": stage, "failed": c.failed}));
    crate::quit(app);
    Ok(())
}
