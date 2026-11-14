//! For the native test only (`native/run.ts`, built with `--features native-test`). It drives the
//! real app as the person would and probes what each webview can call:
//!
//! 0. with `PLUXY_APP_TEST_CORE_SOURCE`, a session runs only the core steps (`native_core.rs`):
//!    install, update, rollback, adopt, then quits;
//! 1. in the window's webview, calls each app command with no arguments, leaving out the two
//!    that take none and would act: each must get past the command check (and the ones that
//!    need arguments then fail on those); then gets core's status through the app;
//! 2. opens the extension page `PLUXY_APP_TEST_PAGE` (such as `ext/test/probe/probe`) and, at the
//!    top of that page's webview, calls every app command and some Tauri plugin commands: each
//!    must be refused by Tauri's command check;
//! 3. waits for the page's own report (it writes it into its settings through the channel);
//!    then native key entry and management-key rotation (`key_entry_test.rs`,
//!    `rotation_test.rs`) in local and remote mode, rotation alone in managed mode; in local and
//!    remote mode the window closes to the tray during a rotation and opens again;
//! 4. in managed mode, checks this device's extension catalog (`device_native.rs`);
//!    Access, the broker for files people grant to device packages, has stages of its own
//!    (`access_native.rs`, `PLUXY_APP_TEST_ACCESS_STAGE`);
//!    with `PLUXY_APP_TEST_ADMISSION`, a session checks only signed native-tier admission
//!    (`admission_native.rs`) and quits; with `PLUXY_APP_TEST_PROGRAM`, only the program host
//!    (`program_native.rs`, then `swap_native.rs`); with `PLUXY_APP_TEST_VAULT`, only the vault,
//!    secret delivery and references and journal recovery (`vault_native.rs`); with
//!    `PLUXY_APP_TEST_SESSIONS`, only the read-only Codex session reader (`sessions_native.rs`);
//!    with `PLUXY_APP_TEST_USAGE`, only the usage hub and its fixture parser (`usage_native.rs`);
//! 5. closes the window, reports which webviews are left, and quits (stopping the app's own core)
//!    once the file `PLUXY_APP_TEST_QUIT` exists.
//!
//! With `PLUXY_APP_TEST_UPDATE` it runs only the signed application update check
//! (`update_native.rs`).
//! With `PLUXY_APP_TEST_WINDOW` or `PLUXY_APP_TEST_PRIVACY` it runs that stage of the window or
//! privacy mode checks instead (`native_window.rs`, `native_privacy.rs`).
//! Before these, as the app starts, it reports its updater gate (`updater_gate`).
//!
//! With `PLUXY_APP_TEST_EARLY_EXIT` set it does none of that: the app's setup waits for its
//! core's exit callback before the app state exists, then the run waits for the app to start
//! that core again and quits.
//!
//! Each step prints one line on stderr: `native-test: <step> <JSON>`.

use crate::{App, COMMANDS, MAIN, WINDOW};
use serde_json::{json, Value};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

#[cfg(target_os = "linux")]
#[path = "access_native.rs"]
mod access;
#[cfg(target_os = "linux")]
#[path = "admission_native.rs"]
mod admission;
#[cfg(target_os = "linux")]
#[path = "native_core.rs"]
mod core_updates;
#[path = "device_native.rs"]
mod device;
#[path = "native_ingress.rs"]
mod ingress;
#[cfg(target_os = "linux")]
#[path = "jobs_native.rs"]
mod jobs;
#[cfg(target_os = "linux")]
#[path = "program_native.rs"]
mod program;
#[cfg(target_os = "linux")]
#[path = "routing_native.rs"]
mod routing;
#[cfg(target_os = "linux")]
#[path = "sessions_native.rs"]
mod sessions;
#[cfg(target_os = "linux")]
#[path = "swap_native.rs"]
mod swap;
#[cfg(target_os = "linux")]
#[path = "usage_native.rs"]
mod usage;
#[cfg(target_os = "linux")]
#[path = "vault_native.rs"]
mod vault;
pub use ingress::{
    count as ingress_count, forwarded as ingress_forwarded, operation as ingress_operation,
};
#[path = "native_privacy.rs"]
pub mod privacy;
#[cfg(target_os = "linux")]
#[path = "native_window.rs"]
pub mod window;

/// The app asked the window what closing should do.
pub fn window_prompted() {
    #[cfg(target_os = "linux")]
    window::prompted();
}

/// A management call from the window, for the window checks' count of polls.
pub fn window_call(method: &str, path: &str) {
    #[cfg(target_os = "linux")]
    window::core_call(method, path);
    #[cfg(not(target_os = "linux"))]
    let _ = (method, path);
}

const WAIT: Duration = Duration::from_secs(120);

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
const ACTING: &[&str] = &[
    "sign_out",
    "core_start",
    "core_restart",
    "core_adopt",
    "key_entry",
    "confirmation_settings",
    "management_key_rotate",
    "device_install_file",
    "app_update_start",
    "app_update_cancel",
];

#[path = "key_entry_test.rs"]
mod key_entry_test;
#[path = "rotation_test.rs"]
mod rotation_test;

/// Reports the updater gate (`trust::updater_gate`) with what it read: the compiled test marker,
/// identifier and product name. The app calls this first among the test's steps, right after the
/// single-instance check (a later launch ends there and reports nothing).
pub fn updater_gate(app: &AppHandle) {
    let config = app.config();
    let home = std::env::var_os("HOME").unwrap_or_default();
    let result = crate::trust::updater_gate(config, std::path::Path::new(&home));
    log(
        "updater_gate",
        json!({
            "passed": result.is_ok(),
            "marker": cfg!(any(feature = "native-test", feature = "artifact-test-trust")),
            "identifier": config.identifier,
            "product": config.product_name,
            "error": result.err(),
        }),
    );
}

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
      out.calls[cmd] = {{ ok: true, detail: String(JSON.stringify(v)).slice(0, 4000) }};
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

/// The page area the window drew for its page webview.
const AREA: &str = "(() => { const r = document.querySelector('[data-part=extension-frame]').getBoundingClientRect(); \
     return {x: r.left, y: r.top, width: r.width, height: r.height}; })()";

/// f's result, run on the main thread (GTK widgets live there).
fn on_main<T: Send + 'static>(
    app: &AppHandle,
    f: impl FnOnce() -> T + Send + 'static,
) -> Option<T> {
    let (tx, rx) = mpsc::channel();
    app.run_on_main_thread(move || {
        let _ = tx.send(f());
    })
    .ok()?;
    rx.recv_timeout(Duration::from_secs(5)).ok()
}

/// Whether two places agree, to the rounding of logical pixels to whole ones.
#[cfg(target_os = "linux")]
fn same_place(a: &Value, b: &Value) -> bool {
    ["x", "y", "width", "height"].iter().all(
        |k| matches!((a[k].as_f64(), b[k].as_f64()), (Some(a), Some(b)) if (a - b).abs() <= 2.0),
    )
}

/// Waits up to secs for f, without failing anything: None when it never came.
#[cfg(target_os = "linux")]
fn within<T>(secs: u64, mut f: impl FnMut() -> Option<T>) -> Option<T> {
    let deadline = Instant::now() + Duration::from_secs(secs);
    loop {
        if let Some(v) = f() {
            return Some(v);
        }
        if Instant::now() > deadline {
            return None;
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

/// The page webview is allocated exactly its page area, not the natural size GTK's overlay would
/// give it. WebKitGTK's natural height is its document's content height. The page document
/// fills its webview, so that is the height the webview last had, and WebKit does not ask GTK to
/// measure again when it changes: once the window shrinks, the natural height is still the old,
/// taller one. Checked after a real window resize, with the window at the top and partly
/// scrolled; then the window gets its size and scroll back. It never fails the run itself:
/// run.ts checks the report.
#[cfg(target_os = "linux")]
fn shrunk_page(app: &AppHandle, main: &Webview, label: &str) -> Value {
    let mut r = json!({});
    let size = app.get_window(WINDOW).and_then(|w| {
        let s = w.inner_size().ok()?;
        Some(s.to_logical::<f64>(w.scale_factor().ok()?))
    });
    let res = match size {
        Some(size) => shrunk_page_steps(app, main, label, size, &mut r),
        None => Err("no window size".into()),
    };
    let _ = main.eval(
        "{ document.getElementById('pluxy-test-spacer')?.remove(); \
         document.scrollingElement.scrollTop = 0; }",
    );
    if let (Some(size), Some(w)) = (size, app.get_window(WINDOW)) {
        let _ = w.set_size(size);
        let back = r["area_before"]["height"].as_f64();
        r["restored"] = json!(within(10, || {
            let h = eval(main, AREA)?["height"].as_f64()?;
            back.filter(|b| (b - h).abs() <= 2.0)
        })
        .is_some());
    }
    r["ok"] = json!(res.is_ok());
    if let Err(e) = res {
        r["error"] = json!(e);
    }
    r
}

#[cfg(target_os = "linux")]
fn shrunk_page_steps(
    app: &AppHandle,
    main: &Webview,
    label: &str,
    size: tauri::LogicalSize<f64>,
    r: &mut Value,
) -> Result<(), String> {
    let natural = |app: &AppHandle| {
        let l = label.to_string();
        on_main(app, move || crate::place::natural_height(&l)).flatten()
    };
    let placed = |app: &AppHandle| {
        let l = label.to_string();
        json!(on_main(app, move || crate::place::allocation(&l)).flatten())
    };
    // Where the page webview is against its area once the window placed it again (or the last
    // seen when it never matched).
    let settled = |r: &mut Value, key: &str| -> bool {
        let mut last = (Value::Null, Value::Null);
        let ok = within(10, || {
            let area = eval(main, AREA)?;
            last = (area.clone(), placed(app));
            same_place(&last.0, &last.1).then_some(())
        })
        .is_some();
        r[key] = json!({"area": last.0, "placed": last.1, "ok": ok});
        ok
    };

    let before = eval(main, AREA).ok_or("no page area")?;
    let height = before["height"].as_f64().ok_or("no page area height")?;
    r["area_before"] = before.clone();
    r["natural_before"] = json!(natural(app));
    r["window"] = json!([size.width, size.height]);

    // The window loses SHRINK pixels of height; the page area (100vh less the page around it,
    // above its minimum) loses as many.
    const SHRINK: f64 = 60.0;
    let w = app.get_window(WINDOW).ok_or("no window")?;
    w.set_size(tauri::LogicalSize::new(size.width, size.height - SHRINK))
        .map_err(|e| e.to_string())?;
    let shrunk = within(10, || {
        let a = eval(main, AREA)?;
        let h = a["height"].as_f64()?;
        ((height - SHRINK - h).abs() <= 2.0).then_some(a)
    })
    .ok_or("the page area did not shrink with the window")?;
    // The control: the natural height GTK had for it is taller than its new area, so a webview
    // given its natural height would not fit.
    let new_height = shrunk["height"].as_f64().unwrap_or(0.0);
    if !r["natural_before"]
        .as_f64()
        .is_some_and(|n| n > new_height + 2.0)
    {
        return Err(format!(
            "the natural height {} is not taller than the shrunk area {new_height}",
            r["natural_before"]
        ));
    }
    let top = settled(&mut *r, "top");
    r["natural_after"] = json!(natural(app));

    // Partly scrolled: the area moves up by half its distance from the top, still in view.
    let y = shrunk["y"].as_f64().unwrap_or(0.0);
    let scroll = (y / 2.0).floor();
    main.eval(format!(
        "{{ const s = document.createElement('div'); s.id = 'pluxy-test-spacer'; \
         s.style.height = (2 * innerHeight) + 'px'; \
         document.querySelector('[data-part=extension-frame]').after(s); \
         document.scrollingElement.scrollTop = {scroll}; }}"
    ))
    .map_err(|e| e.to_string())?;
    within(10, || {
        eval(main, AREA)?["y"]
            .as_f64()
            .filter(|now| (now - (y - scroll)).abs() <= 1.0)
    })
    .ok_or("the window did not scroll")?;
    r["scroll"] = json!(scroll);
    let scrolled = settled(&mut *r, "scrolled");
    if top && scrolled {
        Ok(())
    } else {
        Err("the page webview is not allocated its page area".into())
    }
}

fn run(app: &AppHandle) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    if let Ok(stage) = std::env::var("PLUXY_APP_TEST_ACCESS_STAGE") {
        return access::run(app, &stage);
    }
    #[cfg(target_os = "linux")]
    if let Ok(stage) = std::env::var("PLUXY_APP_TEST_WINDOW") {
        return window::run(app, &stage);
    }
    if let Ok(stage) = std::env::var("PLUXY_APP_TEST_PRIVACY") {
        return privacy::run(app, &stage);
    }
    if let Ok(stage) = std::env::var("PLUXY_APP_TEST_STARTUP") {
        return startup(app, &stage);
    }
    if std::env::var("PLUXY_APP_TEST_ROTATION_RESTART").is_ok() {
        return rotation_test::after_restart(app);
    }
    let main = wait_for("the window", || app.get_webview(MAIN))?;
    // The core steps (native_core.rs), in a session of their own. They come before the window's
    // shell: until a core is installed the window shows only the Connection page.
    #[cfg(target_os = "linux")]
    if std::env::var_os("PLUXY_APP_TEST_CORE_SOURCE").is_some() {
        wait_for("the window's IPC", || {
            eval(
                &main,
                "typeof window.__TAURI_INTERNALS__?.invoke === 'function'",
            )
            .filter(|v| v == true)
        })?;
        core_updates::run(app, &main)?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
    wait_for("the window's shell", || {
        eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true)
    })?;
    // The signed application update (update_native.rs), in a session of its own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_UPDATE") {
        return crate::update_native::run(app, &main, dir.into());
    }
    // Signed native-tier admission (admission_native.rs), in a session of its own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_ADMISSION") {
        admission::run(app, &main, dir.into())?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
    // Cross-package routing and import authorization (routing_native.rs), then declared jobs
    // (jobs_native.rs), in a session of their own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_ROUTING") {
        routing::run(app, &main, dir.clone().into())?;
        jobs::run(app, &main, dir.into())?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
    // The vault, secret delivery and references, and journal recovery (vault_native.rs), in a
    // session of its own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_VAULT") {
        vault::run(app, &main, dir.into())?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
    // The read-only Codex session reader (sessions_native.rs), in a session of its own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_SESSIONS") {
        sessions::run(app, &main, dir.into())?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
    // The usage hub and its fixture parser (usage_native.rs), in a session of their own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_USAGE") {
        usage::run(app, &main, dir.into())?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
    // The program host (program_native.rs), in a session of its own.
    #[cfg(target_os = "linux")]
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_PROGRAM") {
        program::run(app, &main, std::path::PathBuf::from(&dir))?;
        // Swaps, crashes and calls of unknown outcome (swap_native.rs), on from where it left.
        swap::run(app, &main, dir.into())?;
        log("quit", json!({}));
        crate::quit(app);
        return Ok(());
    }
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
        r["area"] = eval(&main, AREA).unwrap_or(Value::Null);
        let l = label.clone();
        r["placed"] = json!(on_main(app, move || crate::place::allocation(&l)).flatten());
        log("page", r);
        #[cfg(target_os = "linux")]
        log("page_allocation", shrunk_page(app, &main, &label));
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
        wv.eval("window.addEventListener('message',e=>{if(e.data?.nativeHashChanged)window.__nativeHashChanged=true;});document.querySelector('iframe').contentWindow.postMessage({nativeHash:true},'*')")
            .map_err(|e| e.to_string())?;
        wait_for("frame location hash change", || {
            eval(&wv, "window.__nativeHashChanged === true").filter(|v| v == true)
        })?;
        std::thread::sleep(Duration::from_millis(200));
        if app
            .state::<App>()
            .pages
            .lock()
            .unwrap()
            .document_closed(&label)
        {
            return Err("fragment navigation closed admission".into());
        }
        // The real HTTP pages-site frame navigates while wrapper closed is deliberately lost.
        // The native navigation callback must invalidate admission before its new document loads.
        wv.eval("{ const original=window.fetch; window.fetch=(u,o)=>o?.body && JSON.parse(o.body).type==='closed' ? Promise.resolve(new Response('',{status:204})) : original(u,o); const frame=document.querySelector('iframe');const next=new URL(frame.src);next.hash='';next.search='?native-second';frame.src=next.href; }").map_err(|e|e.to_string())?;
        wait_for("native HTTP frame navigation closure", || {
            app.state::<App>()
                .pages
                .lock()
                .unwrap()
                .document_closed(&label)
                .then_some(())
        })?;
        wv.eval("fetch('/frame').then(r=>r.json()).then(info=>fetch('/send',{method:'POST',body:JSON.stringify({generation:info.generation,type:'call',data:{v:0,id:'after-http-navigation',method:'theme.get',params:null}})})).then(r=>window.__navStatus=r.status)").map_err(|e|e.to_string())?;
        let status = wait_for("HTTP navigation refusal", || {
            eval(&wv, "window.__navStatus ?? null").filter(|v| v.is_number())
        })?;
        if status != 409 {
            return Err("HTTP frame navigation left native calls open".into());
        }
        wait_for("window closed state", || {
            eval(&main,"document.querySelector('[data-part=extension-frame]')?.dataset.phase === 'closed' && document.body.innerText.includes('Load the page again')").filter(|v|v==true)
        })?;
        log(
            "http_navigation",
            json!({"native_closed":true,"status":status,"wrapper_closed_suppressed":true,"window_closed":true,"fragment_open":true}),
        );
    }

    key_entry_test::check(app, &main)?;
    // In local and remote mode the window closes to the tray and opens again meanwhile.
    let main = rotation_test::check(app, &main)?;
    ingress::run(app, &main)?;
    #[cfg(target_os = "linux")]
    {
        let mut all: Vec<&str> = COMMANDS.to_vec();
        all.extend(PLUGINS);
        crate::release_native::run(app, &main, |w| probe(w, &all))?;
    }

    // This device's extension catalog (device_native.rs), with fixtures from native/device.ts.
    if let Ok(dir) = std::env::var("PLUXY_APP_TEST_DEVICE") {
        device::run(app, &main, dir.into())?;
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
        // The session's tray and recovery checks run now, through crash-loop delays.
        let deadline = Instant::now() + Duration::from_secs(300);
        loop {
            write_state(app);
            if std::path::Path::new(&f).exists() {
                break;
            }
            if Instant::now() > deadline {
                return Err("timed out waiting for the quit file".into());
            }
            std::thread::sleep(Duration::from_millis(200));
        }
    }
    log("quit", json!({}));
    crate::quit(app);
    Ok(())
}

/// The window as the instance scenario sees it.
fn window_state(app: &AppHandle) -> Value {
    match app.get_window(WINDOW) {
        Some(w) => json!({
            "exists": true,
            "visible": w.is_visible().unwrap_or(false),
            "minimized": w.is_minimized().unwrap_or(false),
            "focused": w.is_focused().unwrap_or(false),
        }),
        None => json!({"exists": false}),
    }
}

/// The single-instance scenario (`native/instances-session.ts`, which sets
/// `PLUXY_APP_TEST_INSTANCES`): reports once the window is up (`up`), then answers each line on
/// stdin, one command per line, with the window's state (`state`): `state`, `hide`, `close`, or
/// `quit`, which quits (stopping the app's own core) and answers nothing. If stdin closes, the
/// app quits.
fn instances(app: &AppHandle) -> Result<(), String> {
    wait_for("the window", || app.get_webview(MAIN))?;
    log(
        "up",
        json!({
            "window": window_state(app),
            "tray": app.state::<App>().has_tray.load(std::sync::atomic::Ordering::SeqCst),
            "core_pid": app.state::<App>().conn.lock().unwrap().core_pid(),
        }),
    );
    for line in std::io::stdin().lines() {
        let line = line.map_err(|e| e.to_string())?;
        match line.trim() {
            "state" => {}
            "hide" => app
                .get_window(WINDOW)
                .ok_or("no window")?
                .hide()
                .map_err(|e| e.to_string())?,
            "close" => {
                app.get_window(WINDOW)
                    .ok_or("no window")?
                    .close()
                    .map_err(|e| e.to_string())?;
                wait_for("the window to close", || {
                    app.get_window(WINDOW).is_none().then_some(())
                })?;
            }
            "quit" => {
                crate::quit(app);
                return Ok(());
            }
            other => return Err(format!("unknown command {other:?}")),
        }
        log("state", window_state(app));
    }
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
        wait_for("startup window shell", || {
            eval(&main, "document.querySelector('[data-part=nav]') !== null").filter(|v| v == true)
        })?;
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
        // The startup suite reads the connection from the state file meanwhile.
        wait_for("startup process and tray inspection", || {
            write_state(app);
            std::path::Path::new(&file).exists().then_some(())
        })?;
    }
    crate::quit(app);
    Ok(())
}

/// Writes the app's connection, webviews and window to `PLUXY_APP_TEST_STATE`, for the session's
/// tray and recovery checks.
fn write_state(app: &AppHandle) {
    let Ok(path) = std::env::var("PLUXY_APP_TEST_STATE") else {
        return;
    };
    let view = app.state::<App>().conn.lock().unwrap().view();
    let snapshot = json!({"app_pid": std::process::id(), "view": view,
        "webviews": app.webviews().len(), "window": app.get_window(WINDOW).is_some()});
    crate::managed::write_private(
        std::path::Path::new(&path),
        &serde_json::to_vec(&snapshot).unwrap(),
    )
    .expect("write test state");
}

/// Holds a tray action while the session's hold file exists, so the test can see the tray
/// busy and click it again meanwhile.
pub(crate) fn tray_hold(action: &str) {
    let Ok(f) = std::env::var("PLUXY_APP_TEST_TRAY_HOLD") else {
        return;
    };
    let f = std::path::Path::new(&f);
    if !f.exists() {
        return;
    }
    log("tray-held", json!({"action": action}));
    let deadline = Instant::now() + WAIT;
    while f.exists() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// How many times the app's core exit callback has run.
static CORE_EXITS: AtomicUsize = AtomicUsize::new(0);

pub(crate) fn core_exit_seen() {
    CORE_EXITS.fetch_add(1, Ordering::SeqCst);
}

/// With `PLUXY_APP_TEST_EARLY_EXIT` set, holds the app's setup before its state exists until the
/// core's exit callback has run: the callback then finds no state to recover the core with.
pub(crate) fn hold_for_early_exit() {
    if std::env::var_os("PLUXY_APP_TEST_EARLY_EXIT").is_none() {
        return;
    }
    let seen = wait_for("the core's exit callback", || {
        (CORE_EXITS.load(Ordering::SeqCst) > 0).then_some(())
    });
    log("early_exit_held", json!({"callback_ran": seen.is_ok()}));
}

/// The early-exit run: the core exited before the app state existed. Waits past the first
/// recovery delay for the app to run its core again, then quits.
fn early_exit(app: &AppHandle) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(20);
    let view = loop {
        let v = app.state::<App>().conn.lock().unwrap().view();
        let running = v
            .core
            .as_ref()
            .is_some_and(|c| c.state == "running" && c.pid.is_some());
        if running || Instant::now() > deadline {
            break v;
        }
        std::thread::sleep(Duration::from_millis(200));
    };
    log("early_exit", json!({"view": view}));
    crate::quit(app);
    Ok(())
}

pub fn start(app: AppHandle) {
    std::thread::spawn(move || {
        let result = if std::env::var_os("PLUXY_APP_TEST_EARLY_EXIT").is_some() {
            early_exit(&app)
        } else if std::env::var_os("PLUXY_APP_TEST_INSTANCES").is_some() {
            instances(&app)
        } else {
            run(&app)
        };
        if let Err(e) = result {
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
