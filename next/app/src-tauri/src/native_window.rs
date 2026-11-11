//! The native test's window checks. `native/window.ts` starts the app once per stage, with
//! `PLUXY_APP_TEST_WINDOW=<stage>`, in one session (its HOME, X server, bus and tray watcher):
//!
//! - `first`: the default size; resizing and moving; a pending login polls while the window
//!   shows, not while it is minimized (unmapped, as a window manager iconifies it), and again
//!   once it is restored; closing with Ask every time asks, Cancel keeps the window, Minimize to
//!   Tray closes it and leaves no webview or web process; the real tray item reopens it at the
//!   size and place it had; Remember this choice keeps Minimize to Tray, which then closes
//!   without asking; Exit App, chosen in the settings, quits.
//! - `second`: the next launch opens at the size and place the window last had and shows the
//!   remembered choice; Ask every time, saved, asks again; Exit App with Remember quits.
//! - `third`: the remembered Exit App quits without asking.
//! - `no-tray`: in a session with no tray watcher, closing quits, whatever the saved choice.
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

/// Picks choice in the settings and saves it; whether the app then keeps it.
fn save_choice(app: &AppHandle, choice: &str) -> bool {
    goto(app, "connection", "[data-part=window-preferences]")
        && click(
            app,
            &format!("[data-part=window-preferences] input[type=radio][value={choice}]"),
        )
        && click(app, "[data-part=window-preferences] button[type=submit]")
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

/// A pending login polls while the window shows, not while it is minimized.
fn polling(c: &mut Checks, app: &AppHandle) {
    let tile = "[...document.querySelectorAll('[data-part=method]')].find((t) => t.textContent.includes('test/alpha') && t.textContent.includes('Test browser'))";
    let started = goto(app, "add-account", "[data-part=method]")
        && main_eval(
            app,
            &format!("(() => {{ const b = [...({tile})?.querySelectorAll('button') ?? []].find((b) => b.textContent.trim() === 'Test browser'); if (!b) return false; b.click(); return true; }})()"),
        ) == Some(json!(true))
        && within(10, || {
            (main_eval(
                app,
                "[...document.querySelectorAll('button')].some((b) => b.textContent.trim() === 'Open Link')",
            ) == Some(json!(true)))
            .then_some(())
        })
        .is_some();
    if !c.check(started, "a test/alpha browser sign-in is pending") {
        return;
    }
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
            && within(5, || shown_choice(app)).as_deref() == Some("ask"),
        format!(
            "Close behavior is Ask every time at first: {:?}",
            shown_choice(app)
        ),
    );
    c.check(
        place(app, MOVED),
        format!("the window is resized and moved: {:?}", geometry(app)),
    );
    polling(c, app);

    // Ask every time: Cancel keeps the window.
    ensure_open(c, app);
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

    // Minimize to Tray, not remembered.
    ensure_open(c, app);
    let asked = answer(app, "minimize-to-tray", false);
    c.check(asked, "closing asks again, and Minimize to Tray is chosen");
    c.check(closed(app), "Minimize to Tray closes the window");
    c.check(
        app.webviews().is_empty(),
        format!(
            "no webview is left: {:?}",
            app.webviews().keys().collect::<Vec<_>>()
        ),
    );
    no_web_processes(c, "Minimize to Tray");
    c.check(
        saved_choice(app).as_deref() == Some("ask"),
        format!(
            "a choice not remembered keeps Ask every time: {:?}",
            saved_choice(app)
        ),
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

    // Minimize to Tray, remembered: the next close does not ask.
    ensure_open(c, app);
    c.check(
        answer(app, "minimize-to-tray", true),
        "closing asks, and Minimize to Tray is chosen with Remember this choice",
    );
    c.check(closed(app), "Minimize to Tray closes the window");
    no_web_processes(c, "Minimize to Tray");
    c.check(
        within(5, || {
            (saved_choice(app).as_deref() == Some("minimize-to-tray")).then_some(())
        })
        .is_some(),
        format!(
            "Remember this choice keeps Minimize to Tray: {:?}",
            saved_choice(app)
        ),
    );
    ensure_open(c, app);
    let prompts = PROMPTS.load(SeqCst);
    request_close(app);
    c.check(
        closed(app),
        "the remembered Minimize to Tray closes the window",
    );
    c.check(
        PROMPTS.load(SeqCst) == prompts,
        "the remembered Minimize to Tray does not ask",
    );
    c.check(
        app.state::<App>()
            .has_tray
            .load(std::sync::atomic::Ordering::SeqCst),
        "the app is still in the tray",
    );
    no_web_processes(c, "the remembered Minimize to Tray");

    // Exit App, chosen in the settings, quits; the window's size and place are kept.
    ensure_open(c, app);
    c.check(
        save_choice(app, "exit"),
        format!("Exit App is saved in the settings: {:?}", saved_choice(app)),
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
            "the settings show the remembered Exit App: {:?}",
            shown_choice(app)
        ),
    );
    c.check(
        save_choice(app, "ask"),
        format!(
            "Ask every time is saved in the settings: {:?}",
            saved_choice(app)
        ),
    );
    last_close(c, app, "second", || answer(app, "exit", true));
}

fn third(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    last_close(c, app, "third", || request_close(app));
}

fn no_tray(c: &mut Checks, app: &AppHandle) {
    c.check(shell_ready(app, 60), "the window shows the app's shell");
    c.check(
        goto(
            app,
            "connection",
            "[data-part=window-preferences] [data-part=no-tray]",
        ),
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
        "no-tray" => no_tray(&mut c, app),
        _ => return Err(format!("no window stage {stage}")),
    }
    Ok(())
}
