//! The native test's Setup checks. `native/setup.ts` starts the app once per stage, with
//! `PLUXY_APP_TEST_SETUP=<stage>`, in one session (its HOME, X server and bus), in managed mode on
//! a first run: the session's core has nothing installed and no accounts, its bundled registry
//! offers `pluxy/claude` as a pick, its HOME holds a Claude Code sign-in file, and a stand-in
//! `claude` that prints `2.4.1 (Claude Code)` comes first on the PATH core inherits from the app. A
//! curated device registry offers
//! `pluxy/claude-code`, the Claude Code page's package, so the app offers to install it.
//! The bundled test packages are not installed (`native_test.rs`).
//!
//! - `first`: the window opens on Setup by itself (`#/setup`, "Set up pluxy") with step 1, "Your AI
//!   accounts", open; the Claude kind is ticked, saying Claude Code is installed and signed in on
//!   this computer; "Add Claude" adds it (its row says Added, and `GET /packages` through the app's host lists it
//!   enabled); step 2, opened, has the app's one button on Claude Code's row: Install add-on, then
//!   Turn on add-on (answered in the native confirmation), then a "Set up" link to `#/claude-code`;
//!   pressing it opens the app's Claude Code page with "Setting up pluxy." and "Back to setup"
//!   above it, which goes back to `#/setup`; a later visit to the page by its address has no way
//!   back; "Leave setup for now" lands on Home and `GET /setup` has `first_run` false.
//! - `second`: the next launch opens on Home, not Setup; Setup, opened, is titled "Setup" with
//!   every step folded and no "Leave setup for now"; step 1, opened, says pluxy can connect to Claude; the stage
//!   stays on it, then switches GTK to its dark variant and stays again, so the session's last
//!   screenshot shows it dark.
//!
//! Every check prints `native-test: setup_check {"ok":...,"what":...}`; after holding a screen for
//! a screenshot it prints `native-test: setup_shot {"name":...}`; a stage ends with
//! `native-test: setup_report {"stage":...,"failed":...}` and quits.

use super::{eval, log};
use crate::MAIN;
use serde_json::{json, Value};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager};

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

/// An expression for Claude Code's row in step 2.
const TOOL_ROW: &str = "[...document.querySelectorAll('[data-part=tool-row]')].find((e) => e.textContent.includes('Claude Code'))";
/// An expression for the Claude kind's row in step 1.
const PICK_ROW: &str = "[...document.querySelectorAll('[data-part=setup-pick]')].find((e) => e.querySelector('strong')?.textContent === 'Claude')";
/// An expression for the open step's title, or null.
const OPEN_STEP: &str =
    "document.querySelector('[data-part=setup-step][data-state=open] h2')?.textContent ?? null";

/// Claude Code's row in step 2: each link's text and address, and each button's text.
fn tool_row(app: &AppHandle) -> Value {
    main_eval(
        app,
        &format!("(() => {{ const row = {TOOL_ROW}; return row ? {{ links: [...row.querySelectorAll('a')].map((a) => [a.textContent.trim(), a.getAttribute('href')]), buttons: [...row.querySelectorAll('button')].map((b) => b.textContent.trim()) }} : null; }})()"),
    )
    .unwrap_or(Value::Null)
}

/// Presses the button whose text is label inside the step whose title starts with step.
fn press(app: &AppHandle, step: &str, label: &str) -> bool {
    until(
        app,
        10,
        &format!(
            "(() => {{ const s = [...document.querySelectorAll('[data-part=setup-step]')].find((s) => s.querySelector('h2')?.textContent.startsWith({step})); const b = s && [...s.querySelectorAll('button')].find((b) => b.textContent.trim() === {label}); if (!b || b.disabled) return false; b.click(); return true; }})()",
            step = json!(step),
            label = json!(label),
        ),
    )
}

/// Presses the page's button whose text is label, wherever it is.
fn press_any(app: &AppHandle, label: &str) -> bool {
    until(
        app,
        10,
        &format!(
            "(() => {{ const b = [...document.querySelectorAll('main button')].find((b) => b.textContent.trim() === {label}); if (!b || b.disabled) return false; b.click(); return true; }})()",
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
        r["status"] == 200
            && r["body"]["packages"]
                .as_array()
                .is_some_and(|p| p.is_empty()),
        format!("GET /packages through the app's host lists nothing installed at first ({r})"),
    );
    let open = until(app, 15, &format!("({OPEN_STEP}) === 'Your AI accounts'"));
    c.check(
        open,
        format!(
            "step 1, \"Your AI accounts\", is open ({})",
            main_eval(app, OPEN_STEP).unwrap_or(Value::Null)
        ),
    );
    let ticked = until(
        app,
        30,
        &format!("{PICK_ROW}?.querySelector('input[type=checkbox]')?.checked === true"),
    );
    c.check(ticked, "the Claude kind is ticked");
    c.check(
        until(
            app,
            5,
            &format!("{PICK_ROW}?.textContent.includes('Claude Code is installed and signed in on this computer') === true"),
        ),
        "the Claude kind says Claude Code is installed and signed in on this computer",
    );
    shot("found");

    if !c.check(
        press(app, "Your AI accounts", "Add Claude"),
        "\"Add Claude\" is there, enabled, and pressed",
    ) {
        return;
    }
    let added = until(
        app,
        90,
        "document.body.innerText.includes('pluxy can now connect to Claude.')",
    );
    let notices = main_eval(
        app,
        "[...document.querySelectorAll('[role=alert], [role=status]')].map((e) => e.textContent).join(' | ')",
    )
    .unwrap_or(Value::Null);
    c.check(
        added,
        format!("after Add, step 1 says \"pluxy can now connect to Claude.\" (notices: {notices})"),
    );
    let r = core(app, "GET", "/packages");
    let listed = r["body"]["packages"]
        .as_array()
        .is_some_and(|p| p.iter().any(|p| p["id"] == PICK && p["enabled"] == true));
    c.check(
        r["status"] == 200 && listed,
        format!("GET /packages through the app's host lists {PICK}, enabled ({r})"),
    );
    shot("added");

    if !c.check(
        press(app, "Your coding tool", "Open"),
        "step 2's Open is there and pressed",
    ) {
        return;
    }
    // The add-on is offered by the curated registry and not installed: the row's one button
    // installs it (asking nothing), turns it on (the native confirmation), and only then offers
    // the page. The buttons come once the device's catalog and the registries' offers have loaded.
    let wait_row = |buttons: Value, links: Value, secs: u64| {
        let want = json!({ "links": links, "buttons": buttons });
        let row = within(secs, || {
            let r = tool_row(app);
            (r == want).then_some(r)
        })
        .unwrap_or_else(|| tool_row(app));
        (row == want, row)
    };
    let (ok, row) = wait_row(json!(["Install add-on"]), json!([]), 20);
    c.check(
        ok,
        format!("Claude Code's row in step 2 offers Install add-on and nothing else ({row})"),
    );
    shot("tool");
    let press_row = |label: &str| {
        until(
            app,
            10,
            &format!("(() => {{ const b = [...({TOOL_ROW}?.querySelectorAll('button') ?? [])].find((b) => b.textContent.trim() === {label}); if (!b || b.disabled) return false; b.click(); return true; }})()", label = json!(label)),
        )
    };
    c.check(press_row("Install add-on"), "Install add-on is pressed");
    let (ok, row) = wait_row(json!(["Turn on add-on"]), json!([]), 60);
    c.check(
        ok,
        format!("once installed, Claude Code's row offers Turn on add-on ({row})"),
    );
    c.check(press_row("Turn on add-on"), "Turn on add-on is pressed");
    let confirmed = super::vault::dialog_with(
        app,
        &["pluxy/claude-code", "full access to your user account"],
    )
    .and_then(|_| {
        std::thread::sleep(Duration::from_millis(1300));
        super::admission::press("alt+o")
    });
    c.check(
        confirmed.is_ok(),
        format!("the confirmation names pluxy/claude-code and is answered ({confirmed:?})"),
    );
    let (ok, row) = wait_row(json!([]), json!([["Set up", "#/claude-code"]]), 30);
    c.check(
        ok,
        format!("once turned on, Claude Code's row has the app's \"Set up\" link to #/claude-code and no other control ({row})"),
    );
    shot("tool-on");

    // The link goes to the app's own page for the tool, with the way back to setup above it.
    let followed = until(
        app,
        10,
        &format!("(() => {{ const a = {TOOL_ROW}?.querySelector('a[href=\"#/claude-code\"]'); if (!a) return false; a.click(); return true; }})()"),
    );
    let tool_page = followed
        && until(
            app,
            30,
            "location.hash === '#/claude-code' && document.querySelector('[data-tool=claude-code]') !== null && document.querySelector('[data-part=page-title]')?.textContent === 'Claude Code'",
        );
    c.check(
        tool_page,
        format!(
            "pressing \"Set up\" opens the Claude Code page ({})",
            page(app)
        ),
    );
    c.check(
        until(
            app,
            10,
            "(() => { const s = document.querySelector('[data-part=setup-return]'); const a = s?.querySelector('a'); return s?.textContent.startsWith('Setting up pluxy.') === true && a?.textContent === 'Back to setup' && a.getAttribute('href') === '#/setup'; })()",
        ),
        "the page opened from step 2 says \"Setting up pluxy.\" with a \"Back to setup\" link to #/setup",
    );
    shot("back");
    let _ = main_eval(
        app,
        "(document.querySelector('[data-part=setup-return] a')?.click(), true)",
    );
    c.check(
        until(
            app,
            30,
            "location.hash === '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Set up pluxy'",
        ),
        format!("back on #/setup, Setup is still \"Set up pluxy\" ({})", page(app)),
    );
    // The way back is forgotten once the page is left: the page opened by its address has none.
    let _ = main_eval(app, "(location.hash = '#/claude-code', true)");
    c.check(
        until(
            app,
            30,
            "location.hash === '#/claude-code' && document.querySelector('[data-tool=claude-code]') !== null && document.querySelector('[data-part=setup-return]') === null",
        ),
        format!("the Claude Code page opened by its address has no way back to setup ({})", page(app)),
    );
    let _ = main_eval(app, "(location.hash = '#/setup', true)");
    c.check(
        until(
            app,
            30,
            "location.hash === '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Set up pluxy'",
        ),
        format!("Setup opens again ({})", page(app)),
    );

    if !c.check(
        press_any(app, "Leave setup for now"),
        "\"Leave setup for now\" is there, enabled, and pressed",
    ) {
        return;
    }
    c.check(
        until(
            app,
            30,
            "location.hash !== '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Home'",
        ),
        format!("Leave setup for now lands on Home ({})", page(app)),
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
        "document.querySelector('[data-part=page-title]')?.textContent === 'Home'",
    );
    std::thread::sleep(Duration::from_secs(3));
    let stayed = main_eval(
        app,
        "location.hash !== '#/setup' && document.querySelector('[data-part=page-title]')?.textContent === 'Home'",
    ) == Some(json!(true));
    c.check(
        dashboard && stayed,
        format!("the next launch opens on Home, not Setup ({})", page(app)),
    );

    let _ = main_eval(app, "(location.hash = '#/setup', true)");
    c.check(
        until(
            app,
            30,
            "document.querySelector('[data-part=page-title]')?.textContent === 'Setup' && document.body.innerText.includes('Review or change your accounts and coding tools.')",
        ),
        format!("Setup, opened, is titled \"Setup\" and reads as a summary ({})", page(app)),
    );
    let steps = main_eval(
        app,
        "[...document.querySelectorAll('[data-part=setup-step]')].map((s) => s.getAttribute('data-state'))",
    )
    .unwrap_or(Value::Null);
    c.check(
        steps
            .as_array()
            .is_some_and(|s| s.len() == 3 && !s.contains(&json!("open"))),
        format!("Setup has three steps, every one folded ({steps})"),
    );
    let later = main_eval(
        app,
        "[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Leave setup for now')",
    );
    c.check(
        later == Some(json!(false)),
        format!("Setup has no \"Leave setup for now\" button ({later:?})"),
    );
    shot("finished");
    c.check(
        press(app, "Your AI accounts", "Open")
            && until(
                app,
                15,
                "document.body.innerText.includes('pluxy can now connect to Claude.')",
            ),
        "step 1, opened, says \"pluxy can now connect to Claude.\"",
    );

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
