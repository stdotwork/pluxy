//! The native test's window checks. `native/window.ts` starts the app once per stage, with
//! `PLUXY_APP_TEST_WINDOW=<stage>`, in one session (its HOME, X server, bus and tray watcher):
//!
//! - `first`: the default size; resizing and moving; a pending login polls while the window
//!   shows, not while it is minimized (unmapped, as a window manager iconifies it), and again
//!   once it is restored, and its "Open sign-in page" opens the sign-in page in the browser, no window
//!   of the app's own; the default, Keep in the tray, closes the window without asking; with
//!   Ask saved, closing asks, Cancel keeps the window, Keep in the tray closes it and
//!   leaves no webview or web process; the real tray item reopens it at the size and place it
//!   had; Remember this choice keeps Keep in the tray, which then closes without asking; Quit,
//!   chosen in the settings, quits.
//! - `second`: the next launch opens at the size and place the window last had and shows the
//!   remembered choice; Ask, saved, asks again; Quit with Remember quits.
//! - `third`: the remembered Quit quits without asking.
//! - `no-tray-ask`: in a session with no tray watcher, with the default preferences, closing
//!   asks first (`__pluxyClosePrompt("no-tray")`), since quitting stops the app's own pluxy; a
//!   page that does not answer leaves the window open and pluxy running; the window's own prompt
//!   says "Close and stop pluxy?", and its Keep open keeps the window with pluxy running; Close
//!   and stop pluxy with "Don't ask again" quits.
//! - `no-tray`: in a session with no tray watcher and "Don't ask again" saved, closing quits
//!   without asking, whatever the saved choice.
//!
//! Every check prints `native-test: window_check {"ok":...,"what":...}`. Before its last close,
//! a stage prints `native-test: window_report {...}`; window.ts checks what the app leaves.
//! Each request the app makes to the window to ask what closing does prints
//! `native-test: window_prompt {}` (production code calls `prompted`).

use super::{eval, log};
use crate::{App, MAIN, WINDOW};
use gtk::prelude::GtkWindowExt;
use serde_json::{json, Value};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering::SeqCst};
use std::sync::mpsc;
use std::time::{Duration, Instant};
use tauri::{AppHandle, LogicalSize, Manager, PhysicalPosition};

static LOGIN_READS: AtomicUsize = AtomicUsize::new(0);
static PROMPTS: AtomicUsize = AtomicUsize::new(0);

/// Counts the kit's reads of a pending login (`GET /logins/<id>`): its poller.
pub fn core_call(method: &str, path: &str) {
    if method == "GET"
        && path
            .strip_prefix("/logins/")
            .is_some_and(|id| !id.is_empty() && !id.contains('/'))
    {
        LOGIN_READS.fetch_add(1, SeqCst);
    }
}

/// The app asks the window what closing should do.
pub fn prompted() {
    PROMPTS.fetch_add(1, SeqCst);
    log("window_prompt", json!({}));
}

/// The size (logical) and place (physical) the checks give the window.
type Geometry = (f64, f64, i32, i32);
const DEFAULT_SIZE: (f64, f64) = (1100.0, 760.0);
const MOVED: Geometry = (900.0, 640.0, 130, 90);
const MOVED_AGAIN: Geometry = (820.0, 600.0, 200, 150);

#[derive(Default)]
struct Checks {
    failed: usize,
}

impl Checks {
    fn check(&mut self, ok: bool, what: impl Into<String>) -> bool {
        let what = what.into();
        log("window_check", json!({"ok": ok, "what": what}));
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

fn sleep(ms: u64) {
    std::thread::sleep(Duration::from_millis(ms));
}

/// The value of expr in the window's webview, when there is one.
fn main_eval(app: &AppHandle, expr: &str) -> Option<Value> {
    eval(&app.get_webview(MAIN)?, expr)
}

/// Clicks the element selector names in the window's webview; whether there was one.
fn click(app: &AppHandle, selector: &str) -> bool {
    main_eval(
        app,
        &format!(
            "(() => {{ const e = document.querySelector({}); if (!e) return false; e.click(); return true; }})()",
            json!(selector)
        ),
    ) == Some(json!(true))
}

/// Clicks the button whose text is text, inside the element selector names.
fn click_button(app: &AppHandle, selector: &str, text: &str) -> bool {
    main_eval(
        app,
        &format!(
            "(() => {{ const b = [...(document.querySelector({})?.querySelectorAll('button') ?? [])].find((b) => b.textContent.trim() === {}); if (!b || b.disabled) return false; b.click(); return true; }})()",
            json!(selector),
            json!(text)
        ),
    ) == Some(json!(true))
}

fn shell_ready(app: &AppHandle, secs: u64) -> bool {
    within(secs, || {
        (main_eval(app, "document.querySelector('[data-part=nav]') !== null") == Some(json!(true)))
            .then_some(())
    })
    .is_some()
}

fn goto(app: &AppHandle, page: &str, ready: &str) -> bool {
    let Some(main) = app.get_webview(MAIN) else {
        return false;
    };
    let _ = main.eval(format!("location.hash = {}", json!(format!("#/{page}"))));
    within(10, || {
        (main_eval(
            app,
            &format!("document.querySelector({}) !== null", json!(ready)),
        ) == Some(json!(true)))
        .then_some(())
    })
    .is_some()
}

fn geometry(app: &AppHandle) -> Option<Geometry> {
    let w = app.get_window(WINDOW)?;
    let size = w
        .inner_size()
        .ok()?
        .to_logical::<f64>(w.scale_factor().ok()?);
    let place = w.outer_position().ok()?;
    Some((size.width.round(), size.height.round(), place.x, place.y))
}

fn near(a: Option<Geometry>, b: Geometry) -> bool {
    a.is_some_and(|a| {
        (a.0 - b.0).abs() <= 1.0
            && (a.1 - b.1).abs() <= 1.0
            && (a.2 - b.2).abs() <= 1
            && (a.3 - b.3).abs() <= 1
    })
}

/// Resizes and moves the window as a person (through the window manager) would.
fn place(app: &AppHandle, g: Geometry) -> bool {
    let Some(w) = app.get_window(WINDOW) else {
        return false;
    };
    let _ = w.set_size(LogicalSize::new(g.0, g.1));
    let _ = w.set_position(PhysicalPosition::new(g.2, g.3));
    within(5, || near(geometry(app), g).then_some(())).is_some()
}

/// The web processes below this app.
fn web_processes() -> usize {
    let me = std::process::id();
    let mut parent = std::collections::HashMap::new();
    let mut name = std::collections::HashMap::new();
    for e in std::fs::read_dir("/proc").into_iter().flatten().flatten() {
        let Ok(pid) = e.file_name().to_string_lossy().parse::<u32>() else {
            continue;
        };
        let Ok(stat) = std::fs::read_to_string(format!("/proc/{pid}/stat")) else {
            continue;
        };
        let (Some(open), Some(close)) = (stat.find('('), stat.rfind(')')) else {
            continue;
        };
        let ppid = stat[close + 2..]
            .split(' ')
            .nth(1)
            .and_then(|p| p.parse::<u32>().ok());
        name.insert(pid, stat[open + 1..close].to_string());
        if let Some(ppid) = ppid {
            parent.insert(pid, ppid);
        }
    }
    name.iter()
        .filter(|(_, comm)| comm.starts_with("WebKitWebProces"))
        .filter(|(pid, _)| {
            let mut p = parent.get(pid).copied();
            while let Some(q) = p.filter(|q| *q > 1) {
                if q == me {
                    return true;
                }
                p = parent.get(&q).copied();
            }
            false
        })
        .count()
}

fn no_web_processes(c: &mut Checks, after: &str) {
    let left = within(10, || (web_processes() == 0).then_some(())).is_some();
    c.check(left, format!("no web process is left after {after}"));
}

fn tool(cmd: &str, args: &[&str]) -> Result<String, String> {
    let out = Command::new(cmd)
        .args(args)
        .output()
        .map_err(|e| format!("{cmd}: {e}"))?;
    if !out.status.success() {
        return Err(format!(
            "{cmd} {args:?}: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        ));
    }
    Ok(String::from_utf8_lossy(&out.stdout).trim().into())
}

/// The real tray item, through the session's tray watcher and the item's menu (session_probe).
fn tray(action: &str) -> Result<String, String> {
    let probe = std::env::var("PLUXY_APP_TEST_PROBE").map_err(|_| "no PLUXY_APP_TEST_PROBE")?;
    tool(&probe, &["tray", &std::process::id().to_string(), action])
}

fn minimized(app: &AppHandle) -> bool {
    app.get_window(WINDOW)
        .and_then(|w| w.is_minimized().ok())
        .unwrap_or(false)
}

fn visibility(app: &AppHandle) -> Option<String> {
    main_eval(app, "document.visibilityState").and_then(|v| v.as_str().map(String::from))
}

/// Asks the window to close as its window manager's close button does (a GTK delete event).
fn request_close(app: &AppHandle) -> bool {
    let Some(w) = app.get_window(WINDOW) else {
        return false;
    };
    let (tx, rx) = mpsc::channel();
    let sent = app.run_on_main_thread(move || {
        let _ = tx.send(w.gtk_window().map(|g| g.close()).is_ok());
    });
    sent.is_ok() && rx.recv_timeout(Duration::from_secs(5)) == Ok(true)
}

fn prompt_open(app: &AppHandle) -> bool {
    main_eval(
        app,
        "document.querySelector('dialog[data-part=close-prompt]')?.open === true",
    ) == Some(json!(true))
}

fn closed(app: &AppHandle) -> bool {
    within(5, || app.get_window(WINDOW).is_none().then_some(())).is_some()
}

/// The saved close choice.
fn saved_choice(app: &AppHandle) -> Option<String> {
    let file = app.path().app_config_dir().ok()?.join("window.json");
    let v: Value = serde_json::from_slice(&std::fs::read(file).ok()?).ok()?;
    v["close_behavior"].as_str().map(String::from)
}

/// The choice the settings show.
fn shown_choice(app: &AppHandle) -> Option<String> {
    main_eval(
        app,
        "document.querySelector('[data-part=window-preferences] input[type=radio]:checked')?.value ?? null",
    )
    .and_then(|v| v.as_str().map(String::from))
}

/// Picks choice in the settings, which save when changed; whether the app then keeps it.
fn save_choice(app: &AppHandle, choice: &str) -> bool {
    goto(app, "connection", "[data-part=window-preferences]")
        && click(
            app,
            &format!("[data-part=window-preferences] input[type=radio][value={choice}]"),
        )
        && within(5, || {
            (saved_choice(app).as_deref() == Some(choice)).then_some(())
        })
        .is_some()
}

/// Closes the window and answers the prompt: whether it was asked.
fn answer(app: &AppHandle, choice: &str, remember: bool) -> bool {
    request_close(app);
    if within(5, || prompt_open(app).then_some(())).is_none() {
        return false;
    }
    if remember && !click(app, "dialog[data-part=close-prompt] input[role=switch]") {
        return false;
    }
    click(
        app,
        &format!("dialog[data-part=close-prompt] button[data-choice={choice}]"),
    )
}

fn reopen(c: &mut Checks, app: &AppHandle) -> bool {
    let clicked = tray("Open pluxy");
    let ok = c.check(
        clicked.is_ok(),
        format!("the tray's Open pluxy is clicked: {clicked:?}"),
    ) && within(10, || app.get_window(WINDOW)).is_some()
        && shell_ready(app, 60);
    c.check(ok, "Open pluxy in the tray opens the window again")
}

/// The window must be open for the next steps; a check that failed may have closed it.
fn ensure_open(c: &mut Checks, app: &AppHandle) {
    if app.get_window(WINDOW).is_none() {
        reopen(c, app);
    }
}

/// The stand-in browser's log (`native/feed.ts`, `PLUXY_APP_TEST_BROWSER_LOG`): one address a
/// line, each what the app handed the session's https handler. Empty without one.
fn browser_log() -> Vec<String> {
    std::env::var("PLUXY_APP_TEST_BROWSER_LOG")
        .ok()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .unwrap_or_default()
        .lines()
        .map(String::from)
        .collect()
}

/// A pending login polls while the window shows, not while it is minimized, and its "Open sign-in
/// page" goes to the person's browser, never to a window of the app's own.
fn polling(c: &mut Checks, app: &AppHandle) {
    // The page draws every provider from one reply, so once its tiles show, test/alpha's shows too (under
    // More when core lists more than four providers), if core runs test/alpha. A failure names the step
    // that did not happen.
    // The address test/alpha's browser sign-in gives the person (next/e2e/testext/main.go).
    const SIGN_IN_URL: &str = "https://login.example.test/authorize";
    // Picks test/alpha's tile, opening More first when the tile is not among the first four.
    let pick = "(() => { const t = document.querySelector('[data-part=provider-tile][data-extension=\"test/alpha\"]'); if (t) { t.click(); return true; } const more = document.querySelector('.provider-more'); if (more && more.getAttribute('aria-expanded') === 'false') more.click(); return false; })()";
    let tile = "[...document.querySelectorAll('[data-part=method]')].find((t) => t.closest('[data-extension]')?.dataset.extension === 'test/alpha' && t.textContent.includes('Test browser'))";
    let start = format!("(() => {{ const b = [...({tile})?.querySelectorAll('button') ?? []].find((b) => b.textContent.trim() === 'Start'); if (!b || b.disabled) return false; b.click(); return true; }})()");
    let missing = if !goto(app, "add-account", "[data-part=provider-tile]") {
        Some("Add an account shows no provider within 10 s")
    } else if within(10, || (main_eval(app, pick) == Some(json!(true))).then_some(())).is_none() {
        Some("Add an account has no test/alpha provider to pick")
    } else if within(10, || (main_eval(app, &start) == Some(json!(true))).then_some(())).is_none() {
        Some("Add an account has no enabled Start button for test/alpha's Test browser")
    } else if within(10, || {
        (main_eval(
            app,
            "[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Open sign-in page')",
        ) == Some(json!(true)))
        .then_some(())
    })
    .is_none()
    {
        Some("no Open sign-in page button within 10 s of pressing Start on Test browser")
    } else {
        None
    };
    let what = "a test/alpha browser sign-in is pending";
    if !c.check(
        missing.is_none(),
        missing.map_or(what.to_string(), |m| format!("{what}: {m}")),
    ) {
        return;
    }
    // Open sign-in page: the sign-in page's own address reaches the browser, and the window opens
    // nothing of its own.
    let link = Some(SIGN_IN_URL.to_string());
    let opened_before = browser_log().len();
    let webviews = app.webviews().len();
    let clicked = main_eval(
        app,
        "(() => { const b = [...document.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Open sign-in page'); if (!b) return false; b.click(); return true; })()",
    );
    let opened = within(10, || {
        let l = browser_log();
        (l.len() > opened_before).then_some(l)
    });
    c.check(
        clicked == Some(json!(true))
            && link.is_some()
            && opened
                .as_ref()
                .is_some_and(|l| l.last().is_some_and(|last| Some(last) == link.as_ref())),
        format!("Open sign-in page opens the sign-in page in the browser: {link:?} as {opened:?}"),
    );
    sleep(1000);
    let own = app.webviews().len() - webviews;
    c.check(
        own == 0,
        format!("Open sign-in page opens no window of the app's own ({own} webviews more)"),
    );
    let before = LOGIN_READS.load(SeqCst);
    sleep(5000);
    let shown = LOGIN_READS.load(SeqCst) - before;
    c.check(
        shown >= 2,
        format!("the pending sign-in polls while the window shows: {shown} reads in 5 s"),
    );
    let xid = tool("xdotool", &["search", "--onlyvisible", "--name", "^pluxy$"])
        .ok()
        .and_then(|s| s.lines().last().map(String::from));
    let Some(xid) = xid else {
        c.check(false, "xdotool finds the window");
        return;
    };
    let unmapped = tool("xdotool", &["windowunmap", "--sync", &xid]);
    c.check(
        unmapped.is_ok() && within(5, || minimized(app).then_some(())).is_some(),
        format!(
            "the window, unmapped as a window manager minimizes it, is minimized: {unmapped:?}"
        ),
    );
    let hidden = within(5, || {
        (visibility(app).as_deref() == Some("hidden")).then_some(())
    })
    .is_some();
    c.check(
        hidden,
        format!(
            "the minimized window's page is hidden: {:?}",
            visibility(app)
        ),
    );
    sleep(500);
    let before = LOGIN_READS.load(SeqCst);
    sleep(6000);
    let minimized_reads = LOGIN_READS.load(SeqCst) - before;
    c.check(
        minimized_reads == 0,
        format!("no polling while minimized: {minimized_reads} reads in 6 s"),
    );
    let mapped = tool("xdotool", &["windowmap", "--sync", &xid]);
    let before = LOGIN_READS.load(SeqCst);
    let resumed = mapped.is_ok()
        && within(5, || (!minimized(app)).then_some(())).is_some()
        && within(6, || (LOGIN_READS.load(SeqCst) > before).then_some(())).is_some();
    c.check(
        resumed,
        format!("polling resumes once the window is restored: {mapped:?}"),
    );
    click_button(app, "body", "Cancel");
}

/// Prints the stage's report, closes the window as the last step and expects the app to quit.
fn last_close(c: &mut Checks, app: &AppHandle, stage: &str, close: impl FnOnce() -> bool) {
    let core = app.state::<App>().conn.lock().unwrap().core_pid();
    log(
        "window_report",
        json!({"stage": stage, "failed": c.failed, "core_pid": core}),
    );
    let asked = close();
    // The app should be gone by now.
    sleep(10_000);
    c.check(
        false,
        format!("the app still runs 10 s after the last close (asked: {asked})"),
    );
    crate::quit(app);
}

fn first(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    let g = geometry(app);
    c.check(
        g.is_some_and(|g| (g.0, g.1) == DEFAULT_SIZE),
        format!("a first window has the default size 1100x760: {g:?}"),
    );
    c.check(
        web_processes() > 0,
        "web processes run while the window is open",
    );
    c.check(
        goto(app, "connection", "[data-part=window-preferences]")
            && within(5, || shown_choice(app)).as_deref() == Some("minimize-to-tray"),
        format!(
            "the close choice is Keep in the tray at first: {:?}",
            shown_choice(app)
        ),
    );
    c.check(
        place(app, MOVED),
        format!("the window is resized and moved: {:?}", geometry(app)),
    );
    polling(c, app);

    // The default keeps the app in the tray without asking.
    ensure_open(c, app);
    let prompts = PROMPTS.load(SeqCst);
    request_close(app);
    c.check(
        closed(app),
        "the default Keep in the tray closes the window",
    );
    c.check(
        PROMPTS.load(SeqCst) == prompts,
        "the default Keep in the tray does not ask",
    );
    no_web_processes(c, "the default Keep in the tray");
    reopen(c, app);

    // Ask, saved in the settings: Cancel keeps the window.
    ensure_open(c, app);
    c.check(
        save_choice(app, "ask"),
        format!("Ask is saved in the settings: {:?}", saved_choice(app)),
    );
    let asked = request_close(app) && within(5, || prompt_open(app).then_some(())).is_some();
    c.check(asked, "closing the window asks what to do");
    if asked {
        click(
            app,
            "dialog[data-part=close-prompt] button[data-choice=cancel]",
        );
        sleep(1000);
        c.check(
            app.get_window(WINDOW).is_some() && !prompt_open(app),
            "Cancel keeps the window open",
        );
    }

    // Keep in the tray, not remembered.
    ensure_open(c, app);
    let asked = answer(app, "minimize-to-tray", false);
    c.check(asked, "closing asks again, and Keep in the tray is chosen");
    c.check(closed(app), "Keep in the tray closes the window");
    c.check(
        app.webviews().is_empty(),
        format!(
            "no webview is left: {:?}",
            app.webviews().keys().collect::<Vec<_>>()
        ),
    );
    no_web_processes(c, "Keep in the tray");
    c.check(
        saved_choice(app).as_deref() == Some("ask"),
        format!("a choice not remembered keeps Ask: {:?}", saved_choice(app)),
    );
    let item = tray("inspect");
    c.check(
        item.as_ref().is_ok_and(|i| i.contains("Open pluxy=true")),
        format!("the app's own tray item offers Open pluxy: {item:?}"),
    );

    // The tray reopens the window where it was.
    if reopen(c, app) {
        c.check(
            near(geometry(app), MOVED),
            format!(
                "the window reopens at the size and place it had: {:?}",
                geometry(app)
            ),
        );
        c.check(
            web_processes() > 0,
            "the reopened window has a web process again",
        );
    }

    // Keep in the tray, remembered: the next close does not ask.
    ensure_open(c, app);
    c.check(
        answer(app, "minimize-to-tray", true),
        "closing asks, and Keep in the tray is chosen with Remember this choice",
    );
    c.check(closed(app), "Keep in the tray closes the window");
    no_web_processes(c, "Keep in the tray");
    c.check(
        within(5, || {
            (saved_choice(app).as_deref() == Some("minimize-to-tray")).then_some(())
        })
        .is_some(),
        format!(
            "Remember this choice keeps Keep in the tray: {:?}",
            saved_choice(app)
        ),
    );
    ensure_open(c, app);
    let prompts = PROMPTS.load(SeqCst);
    request_close(app);
    c.check(
        closed(app),
        "the remembered Keep in the tray closes the window",
    );
    c.check(
        PROMPTS.load(SeqCst) == prompts,
        "the remembered Keep in the tray does not ask",
    );
    c.check(
        app.state::<App>()
            .has_tray
            .load(std::sync::atomic::Ordering::SeqCst),
        "the app is still in the tray",
    );
    no_web_processes(c, "the remembered Keep in the tray");

    // Quit, chosen in the settings, quits; the window's size and place are kept.
    ensure_open(c, app);
    c.check(
        save_choice(app, "exit"),
        format!("Quit is saved in the settings: {:?}", saved_choice(app)),
    );
    c.check(
        place(app, MOVED_AGAIN),
        format!("the window is resized and moved again: {:?}", geometry(app)),
    );
    last_close(c, app, "first", || request_close(app));
}

fn second(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    c.check(
        near(geometry(app), MOVED_AGAIN),
        format!(
            "the next launch opens at the size and place the window had: {:?}",
            geometry(app)
        ),
    );
    c.check(
        goto(app, "connection", "[data-part=window-preferences]")
            && within(5, || shown_choice(app)).as_deref() == Some("exit"),
        format!(
            "the settings show the remembered Quit: {:?}",
            shown_choice(app)
        ),
    );
    c.check(
        save_choice(app, "ask"),
        format!("Ask is saved in the settings: {:?}", saved_choice(app)),
    );
    last_close(c, app, "second", || answer(app, "exit", true));
}

fn third(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    last_close(c, app, "third", || request_close(app));
}

/// What the open close prompt shows: its kind, title, buttons and switch; None when none is open.
fn prompt_shown(app: &AppHandle) -> Option<Value> {
    main_eval(
        app,
        "(() => { const d = document.querySelector('dialog[data-part=close-prompt]'); if (!d?.open) return null; return { kind: d.dataset.kind ?? null, title: d.querySelector('h2')?.textContent ?? null, buttons: [...d.querySelectorAll('button')].map((b) => b.textContent.trim()), switch: d.querySelector('label')?.textContent?.trim() ?? null }; })()",
    )
    .filter(|v| !v.is_null())
}

/// Clicks the open close prompt's button whose text is text, only when a person could: it is laid
/// out on the page and enabled.
fn click_shown(app: &AppHandle, text: &str) -> bool {
    main_eval(
        app,
        &format!(
            "(() => {{ const d = document.querySelector('dialog[data-part=close-prompt]'); if (!d?.open) return false; const b = [...d.querySelectorAll('button')].find((b) => b.textContent.trim() === {}); if (!b || b.disabled || b.getClientRects().length === 0) return false; b.click(); return true; }})()",
            json!(text)
        ),
    ) == Some(json!(true))
}

fn no_tray_ask(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    let core = || app.state::<App>().conn.lock().unwrap().core_pid();
    let pid = core();
    // A page that does not take the question (its prompt not in place, as while it loads): the
    // window stays open, and pluxy keeps running.
    let held = main_eval(
        app,
        "(() => { if (typeof window.__pluxyClosePrompt !== 'function') return false; window.__pluxyHeldPrompt = window.__pluxyClosePrompt; delete window.__pluxyClosePrompt; return true; })()",
    ) == Some(json!(true));
    let prompts = PROMPTS.load(SeqCst);
    request_close(app);
    sleep(5000);
    c.check(
        held && PROMPTS.load(SeqCst) == prompts + 1,
        format!(
            "with no tray, closing asks the window first ({} asks)",
            PROMPTS.load(SeqCst) - prompts
        ),
    );
    c.check(
        app.get_window(WINDOW).is_some() && shell_ready(app, 5) && pid.is_some() && core() == pid,
        format!(
            "a no-tray prompt the window does not answer keeps the window open and pluxy running ({pid:?}, now {:?})",
            core()
        ),
    );
    let restored = main_eval(
        app,
        "(() => { window.__pluxyClosePrompt = window.__pluxyHeldPrompt; return typeof window.__pluxyClosePrompt === 'function'; })()",
    ) == Some(json!(true));
    c.check(restored, "the window's own close prompt is back in place");

    // The window's own prompt: its words, and Keep open keeps the window with pluxy running.
    let prompts = PROMPTS.load(SeqCst);
    request_close(app);
    let shown = within(5, || prompt_shown(app));
    let want = json!({
        "kind": "no-tray",
        "title": "Close and stop pluxy?",
        "buttons": ["Keep open", "Close and stop pluxy"],
        "switch": "Don't ask again",
    });
    c.check(
        PROMPTS.load(SeqCst) == prompts + 1 && shown.as_ref() == Some(&want),
        format!("with no tray, closing shows Close and stop pluxy? with Keep open: {shown:?}"),
    );
    c.check(
        click_shown(app, "Keep open"),
        "the prompt's Keep open is shown and clicked",
    );
    // Longer than the three seconds an unanswered prompt is waited for.
    sleep(4000);
    c.check(
        prompt_shown(app).is_none()
            && app.get_window(WINDOW).is_some()
            && shell_ready(app, 5)
            && pid.is_some()
            && core() == pid,
        format!(
            "Keep open closes the prompt, and the window stays open with pluxy running ({pid:?}, now {:?})",
            core()
        ),
    );
    // Close and stop pluxy, with Don't ask again: it quits, and window.ts checks what it kept.
    last_close(c, app, "no-tray-ask", || {
        request_close(app);
        within(5, || prompt_shown(app)).is_some_and(|p| p == want)
            && click(app, "dialog[data-part=close-prompt] input[role=switch]")
            && click_shown(app, "Close and stop pluxy")
    });
}

fn no_tray(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    c.check(
        goto(app, "connection", "[data-part=window-preferences]")
            && within(5, || {
                (main_eval(
                    app,
                    "document.querySelector('[data-part=window-preferences]')?.innerText.includes('This desktop has no tray') === true",
                ) == Some(json!(true)))
                .then_some(())
            })
            .is_some(),
        "the settings say that with no system tray closing the window quits",
    );
    last_close(c, app, "no-tray", || request_close(app));
}

pub fn run(app: &AppHandle, stage: &str) -> Result<(), String> {
    let mut c = Checks::default();
    match stage {
        "first" => first(&mut c, app),
        "second" => second(&mut c, app),
        "third" => third(&mut c, app),
        "no-tray-ask" => no_tray_ask(&mut c, app),
        "no-tray" => no_tray(&mut c, app),
        _ => return Err(format!("no window stage {stage}")),
    }
    Ok(())
}
