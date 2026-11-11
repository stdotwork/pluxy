//! For the native test only (`native/run.ts`, built with `--features native-test`). It drives the
//! real app as the person would and probes what each webview can call:
//!
//! 1. in the window's webview, calls each app command with no arguments, leaving out the two
//!    that take none and would act: each must get past the command check (and the ones that
//!    need arguments then fail on those); then gets core's status through the app;
//! 2. opens the extension page `PLUXY_APP_TEST_PAGE` (such as `ext/test/probe/probe`) and, at the
//!    top of that page's webview, calls every app command and some Tauri plugin commands: each
//!    must be refused by Tauri's command check;
//! 3. waits for the page's own report (it writes it into its settings through the channel);
//! 4. closes the window, reports which webviews are left, and quits (stopping the app's own core)
//!    once the file `PLUXY_APP_TEST_QUIT` exists.
//!
//! Each step prints one line on stderr: `native-test: <step> <JSON>`.

use crate::{App, COMMANDS, MAIN, WINDOW};
use serde_json::{json, Value};
use std::sync::mpsc;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

const WAIT: Duration = Duration::from_secs(60);

/// Tauri's own commands a page webview might try.
const PLUGINS: &[&str] = &[
    "plugin:event|listen",
    "plugin:event|emit",
    "plugin:window|close",
    "plugin:webview|create_webview",
    "plugin:app|version",
    "plugin:path|resolve_directory",
    "plugin:menu|new",
    "plugin:tray|new",
    "plugin:image|new",
];

/// Commands the window's probe leaves out: they take no arguments and would act.
const ACTING: &[&str] = &["sign_out", "core_restart", "confirmation_settings"];

pub(crate) fn log(step: &str, v: Value) {
    eprintln!("native-test: {step} {v}");
}

fn probe_js(calls: &[&str]) -> String {
    let calls = json!(calls);
    format!(
        r#"(async () => {{
  const inv = window.__TAURI_INTERNALS__?.invoke;
  const out = {{ internals: typeof inv === "function", calls: {{}} }};
  for (const cmd of {calls}) {{
    try {{
      const v = await Promise.race([
        inv(cmd, {{}}),
        new Promise((_, r) => setTimeout(() => r(new Error("timed out")), 3000)),
      ]);
      out.calls[cmd] = {{ ok: true, detail: String(JSON.stringify(v)).slice(0, 200) }};
    }} catch (e) {{
      out.calls[cmd] = {{ ok: false, detail: String(e).slice(0, 300) }};
    }}
  }}
  window.__pluxyProbe = out;
}})()"#
    )
}

pub(crate) fn wait_for<T>(what: &str, mut f: impl FnMut() -> Option<T>) -> Result<T, String> {
    let deadline = Instant::now() + WAIT;
    loop {
        if let Some(v) = f() {
            return Ok(v);
        }
        if Instant::now() > deadline {
            return Err(format!("timed out waiting for {what}"));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

/// The value of expr in w, as JSON.
pub(crate) fn eval(w: &Webview, expr: &str) -> Option<Value> {
    let (tx, rx) = mpsc::channel();
    w.eval_with_callback(format!("JSON.stringify({expr})"), move |raw| {
        let _ = tx.send(raw);
    })
    .ok()?;
    let raw = rx.recv_timeout(Duration::from_secs(5)).ok()?;
    // The callback gets the result serialised: here, a JSON string holding JSON.
    let text: String = serde_json::from_str(&raw).ok()?;
    serde_json::from_str(&text).ok()
}

fn probe(w: &Webview, calls: &[&str]) -> Result<Value, String> {
    w.eval(probe_js(calls)).map_err(|e| e.to_string())?;
    wait_for("the probe", || {
        eval(w, "window.__pluxyProbe ?? null").filter(|v| !v.is_null())
    })
}

fn run(app: &AppHandle) -> Result<(), String> {
    if let Ok(stage) = std::env::var("PLUXY_APP_TEST_STARTUP") {
        return startup(app, &stage);
    }
    let main = wait_for("the window", || app.get_webview(MAIN))?;
    wait_for("the window's shell", || {
        eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true)
    })?;
    let calls: Vec<&str> = COMMANDS
        .iter()
        .copied()
        .filter(|c| !ACTING.contains(c))
        .collect();
    log("main", probe(&main, &calls)?);
    // And one real call through the app's connection, as the Status page makes it.
    main.eval(
        "window.__TAURI_INTERNALS__.invoke('core_call', {method: 'GET', path: '/status', body: null})\
         .then((r) => { window.__pluxyStatus = {status: r.status}; }, (e) => { window.__pluxyStatus = {error: String(e)}; })",
    )
    .map_err(|e| e.to_string())?;
    log(
        "status",
        wait_for("the status call", || {
            eval(&main, "window.__pluxyStatus ?? null").filter(|v| !v.is_null())
        })?,
    );

    if let Ok(page) = std::env::var("PLUXY_APP_TEST_PAGE") {
        main.eval(format!("location.hash = {}", json!(format!("#/{page}"))))
            .map_err(|e| e.to_string())?;
        let label = wait_for("a page webview", || {
            app.state::<App>()
                .pages
                .lock()
                .unwrap()
                .labels()
                .first()
                .cloned()
        })?;
        let wv = wait_for("the page webview", || app.get_webview(&label))?;
        wait_for("the page's frame", || {
            eval(&wv, "document.querySelector('iframe')?.src ?? null").filter(|v| v.is_string())
        })?;
        let mut all: Vec<&str> = COMMANDS.to_vec();
        all.extend(PLUGINS);
        let mut r = probe(&wv, &all)?;
        r["label"] = json!(label);
        r["url"] = eval(&wv, "location.href").unwrap_or(Value::Null);
        // Where the page webview is, against the page area the window drew for it.
        r["area"] = eval(
            &main,
            "(() => { const r = document.querySelector('[data-part=extension-frame]').getBoundingClientRect(); \
             return {x: r.left, y: r.top, width: r.width, height: r.height}; })()",
        )
        .unwrap_or(Value::Null);
        let (tx, rx) = mpsc::channel();
        let l = label.clone();
        app.run_on_main_thread(move || {
            let _ = tx.send(crate::place::allocation(&l));
        })
        .map_err(|e| e.to_string())?;
        r["placed"] = json!(rx.recv_timeout(Duration::from_secs(5)).ok().flatten());
        log("page", r);
        #[cfg(target_os = "linux")]
        crate::confirmation::native::run(app, &main, &wv)?;

        // The page's own report, written through the channel into its settings.
        let ext = page
            .trim_start_matches("ext/")
            .rsplit_once('/')
            .map_or("", |(ext, _)| ext)
            .to_string();
        let caller = app.state::<App>().conn.lock().unwrap().caller();
        let report = wait_for("the page's report", || {
            let r = caller.call("GET", &format!("/extensions/{ext}/config"), None);
            r.body?["values"]["report"]
                .as_str()
                .filter(|s| !s.is_empty())
                .map(String::from)
        })?;
        log(
            "frame",
            serde_json::from_str(&report).unwrap_or(json!(report)),
        );
    }

    // For screenshots: keep the window open until this file exists.
    if let Ok(f) = std::env::var("PLUXY_APP_TEST_HOLD") {
        wait_for("the hold file", || {
            std::path::Path::new(&f).exists().then_some(())
        })?;
    }
    let window = app.get_window(WINDOW).ok_or("no window")?;
    window.close().map_err(|e| e.to_string())?;
    wait_for("the window to close", || {
        app.get_window(WINDOW).is_none().then_some(())
    })?;
    let left: Vec<String> = app.webviews().keys().cloned().collect();
    log(
        "closed",
        json!({
            "webviews": left,
            "tray": app.state::<App>().has_tray.load(std::sync::atomic::Ordering::SeqCst),
            "core_pid": app.state::<App>().conn.lock().unwrap().core_pid(),
        }),
    );

    if let Ok(f) = std::env::var("PLUXY_APP_TEST_QUIT") {
        wait_for("the quit file", || {
            std::path::Path::new(&f).exists().then_some(())
        })?;
    }
    log("quit", json!({}));
    crate::quit(app);
    Ok(())
}

fn startup(app: &AppHandle, stage: &str) -> Result<(), String> {
    if stage == "enable" || stage == "disable" {
        let main = wait_for("the startup settings window", || app.get_webview(MAIN))?;
        let enabled = stage == "enable";
        main.eval(format!(
            "window.__TAURI_INTERNALS__.invoke('startup_set', {{ preferences: {{ autostart_enabled: {enabled}, silent_start_enabled: false, start_core_on_launch: true }} }}).then((v) => {{ window.__startupResult = {{ ok: true, value: v }}; }}, (e) => {{ window.__startupResult = {{ error: String(e) }}; }})"
        )).map_err(|e| e.to_string())?;
        let result = wait_for("saving startup settings", || {
            eval(&main, "window.__startupResult ?? null").filter(|v| !v.is_null())
        })?;
        if result["ok"] != true {
            return Err(format!("startup settings command failed: {result}"));
        }
    }
    if let Some(main) = app.get_webview(MAIN) {
        wait_for("startup window shell", || eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true))?;
    }
    let connection = app.state::<App>().conn.lock().unwrap().view();
    log(
        "startup",
        json!({
            "app_pid": std::process::id(),
            "windows": app.windows().len(),
            "webviews": app.webviews().len(),
            "tray": app.state::<App>().has_tray.load(std::sync::atomic::Ordering::SeqCst),
            "core_pid": connection.core.and_then(|c| c.pid),
        }),
    );
    if let Ok(file) = std::env::var("PLUXY_APP_TEST_STARTUP_CONTINUE") {
        wait_for("startup process and tray inspection", || {
            std::path::Path::new(&file).exists().then_some(())
        })?;
    }
    crate::quit(app);
    Ok(())
}

pub fn start(app: AppHandle) {
    std::thread::spawn(move || {
        if let Err(e) = run(&app) {
            // What the window showed, to tell why.
            let shown = app.get_webview(MAIN).and_then(|m| {
                eval(
                    &m,
                    "({href: location.href, text: document.body.innerText.slice(0, 2000)})",
                )
            });
            let pages: Vec<Value> = app
                .webviews()
                .into_iter()
                .filter(|(l, _)| l != MAIN)
                .map(|(l, w)| {
                    json!({"label": l, "shown": eval(&w, "({href: location.href, html: document.documentElement.outerHTML.slice(0, 1000)})")})
                })
                .collect();
            log(
                "failed",
                json!({"error": e, "window": shown, "pages": pages}),
            );
            crate::quit(&app);
        }
    });
}
