//! Count-only native-test instrumentation. No request or reply payload is logged.
use super::{eval, log, wait_for};
use crate::App;
use serde_json::{json, Value};
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering::SeqCst};
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

static ARRIVED: AtomicUsize = AtomicUsize::new(0);
static ADMITTED: AtomicUsize = AtomicUsize::new(0);
static FORWARDED: AtomicUsize = AtomicUsize::new(0);
static OPERATIONS: AtomicUsize = AtomicUsize::new(0);
static REAL_SITE: AtomicUsize = AtomicUsize::new(0);
static ENABLED: AtomicBool = AtomicBool::new(false);

pub fn count(method: &str, path: &str, status: u16, query: Option<&str>) {
    if query == Some("pages_site_probe") {
        REAL_SITE.fetch_add(1, SeqCst);
    }
    if ENABLED.load(SeqCst) && method == "POST" && path == "/send" {
        ARRIVED.fetch_add(1, SeqCst);
        if status == 204 {
            ADMITTED.fetch_add(1, SeqCst);
        }
    }
}
pub fn forwarded() {
    if ENABLED.load(SeqCst) {
        FORWARDED.fetch_add(1, SeqCst);
    }
}
pub fn operation(method: &str, path: &str) {
    if ENABLED.load(SeqCst)
        && matches!(method, "GET" | "PATCH")
        && path == "/extensions/test/probe/config"
    {
        OPERATIONS.fetch_add(1, SeqCst);
    }
}
fn command(w: &Webview, name: &str, field: &str) -> Result<Value, String> {
    w.eval(format!("window.__ingressReport = null; document.querySelector('iframe').contentWindow.postMessage({{ingressCommand:{}}}, '*')", json!(name))).map_err(|e| e.to_string())?;
    wait_for(name, || {
        eval(w, "window.__ingressReport ?? null")
            .filter(|v| v.get(field).is_some() || v.get("error").is_some())
    })
}
fn require(ok: bool, reason: &str) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(format!("native ingress: {reason}"))
    }
}
pub fn run(app: &AppHandle, main: &Webview) -> Result<(), String> {
    require(
        REAL_SITE.load(SeqCst) == 0,
        "real pages-site probe unexpectedly reached the scheme; use a pages-site flood fixture",
    )?;
    log(
        "ingress_site",
        json!({"attempts":1,"arrivals":REAL_SITE.load(SeqCst),"blocked":true}),
    );
    main.eval("location.hash = '#/extensions'")
        .map_err(|e| e.to_string())?;
    wait_for("old page to close", || {
        app.state::<App>()
            .pages
            .lock()
            .unwrap()
            .labels()
            .is_empty()
            .then_some(())
    })?;
    app.state::<App>().pages.lock().unwrap().ingress_fixture = true;
    ENABLED.store(true, SeqCst);
    main.eval("location.hash = '#/ext/test/probe/probe'")
        .map_err(|e| e.to_string())?;
    let label = wait_for("ingress page", || {
        app.state::<App>()
            .pages
            .lock()
            .unwrap()
            .labels()
            .first()
            .cloned()
    })?;
    let w = wait_for("ingress webview", || app.get_webview(&label))?;
    wait_for("ingress welcome", || {
        eval(
            main,
            "document.querySelector('[data-part=extension-frame]')?.dataset.phase",
        )
        .filter(|v| v == "connected")
    })?;
    w.eval("window.addEventListener('message', e => { if(e.data?.ingress) window.__ingressReport = e.data.ingress; });").map_err(|e| e.to_string())?;
    let d = command(&w, "denials", "denials")?;
    for (name, status, code) in [
        ("unknown", 400, "unknown_method"),
        ("schema", 400, "bad_request"),
        ("params", 400, "bad_request"),
        ("large", 413, "too_large"),
        ("body", 413, "too_large"),
        ("generation", 409, "bad_request"),
        ("hello", 409, "bad_request"),
    ] {
        let r = &d["denials"][name];
        require(
            r["status"] == status,
            &format!("{name}: expected HTTP {status}, got {}", r["status"]),
        )?;
        if name != "hello" {
            let body: Value =
                serde_json::from_str(r["body"].as_str().unwrap_or("")).unwrap_or(Value::Null);
            require(
                body["error"]["code"] == code,
                &format!("{name}: missing matching channel error"),
            )?;
        }
    }
    require(
        d["denials"]["control"]["status"] == 204
            && d["denials"]["control"]["answer"]["result"].is_object(),
        "denial control failed",
    )?;
    std::thread::sleep(Duration::from_millis(1100));
    // Busy the actual window while the other webview sends directly to the scheme.
    main.eval("{ const until=performance.now()+1500; while(performance.now()<until) {} }")
        .map_err(|e| e.to_string())?;
    let f = command(&w, "flight", "flight")?;
    let flight = f["flight"].as_array().ok_or("no in-flight report")?;
    require(
        flight.iter().filter(|r| r["status"] == 204).count() == 8
            && flight.iter().filter(|r| r["status"] == 429).count() == 4,
        "in-flight flood did not admit exactly eight",
    )?;
    std::thread::sleep(Duration::from_millis(1800));
    let r = command(&w, "rate", "rate")?;
    let rate = r["rate"].as_array().ok_or("no rate report")?;
    require(
        r["elapsed"].as_f64().unwrap_or(f64::MAX) < 1000.0,
        "rate flood did not fit in one second",
    )?;
    require(
        rate.iter().filter(|r| r["status"] == 204).count() == 30
            && rate.iter().filter(|r| r["status"] == 429).count() == 10,
        "rate flood did not admit exactly thirty",
    )?;
    std::thread::sleep(Duration::from_millis(1100));
    let c = command(&w, "control", "control")?;
    require(
        c["control"]["status"] == 204 && c["answer"]["result"].is_object(),
        "final control failed",
    )?;
    let caller = app.state::<App>().conn.lock().unwrap().caller();
    require(
        caller
            .call("GET", "/extensions/test/probe/config", None)
            .body
            .as_ref()
            .is_some_and(|b| b["values"]["report"] == "native-ingress-control"),
        "control did not write real core settings",
    )?;
    require(
        OPERATIONS.load(SeqCst) == 10,
        "flood executed extra core operations",
    )?;
    require(
        ARRIVED.load(SeqCst) == 62,
        "not every hostile request reached the handler",
    )?;
    require(
        ADMITTED.load(SeqCst) == 41 && FORWARDED.load(SeqCst) == 41,
        "refusals reached the window",
    )?;
    log(
        "ingress",
        json!({"arrivals":ARRIVED.load(SeqCst),"admitted":ADMITTED.load(SeqCst),"forwarded":FORWARDED.load(SeqCst),"operations":OPERATIONS.load(SeqCst),"flight_admitted":8,"rate_admitted":30,"control":true}),
    );
    ENABLED.store(false, SeqCst);
    // Force exactly one real MessagePort delivery failure, then let the native error through.
    w.eval("{ const original=MessagePort.prototype.postMessage; let once=true; MessagePort.prototype.postMessage=function(m,...rest){if(once && m.id==='recover'){once=false;throw new Error('native test delivery failure');}return original.call(this,m,...rest);}; }").map_err(|e|e.to_string())?;
    let recovery = command(&w, "recovery", "recovery")?;
    require(
        recovery["answer"]["error"]["code"] == "unavailable",
        "failed delivery did not return a native error",
    )?;
    let control = command(&w, "control", "control")?;
    require(
        control["answer"]["result"].is_object(),
        "page stopped working after delivery error",
    )?;
    log(
        "ingress_recovery",
        json!({"error":"unavailable","control":true}),
    );
    // Suppress the wrapper's closed notification: native navigation alone must invalidate it.
    w.eval("{ const original=window.fetch; window.fetch=(url, options) => options?.body && JSON.parse(options.body).type === 'closed' ? Promise.resolve(new Response('',{status:204})) : original(url,options); }").map_err(|e| e.to_string())?;
    let nav = command(&w, "navigate", "navigated")?;
    require(
        nav["navigated"]["status"] == 409,
        "second frame document called before native closure",
    )?;
    log(
        "ingress_navigation",
        json!({"native_closed":true,"wrapper_closed_suppressed":true,"status":409}),
    );
    main.eval("location.hash='#/extensions'")
        .map_err(|e| e.to_string())?;
    wait_for("navigation page closed", || {
        app.state::<App>()
            .pages
            .lock()
            .unwrap()
            .labels()
            .is_empty()
            .then_some(())
    })?;
    main.eval("location.hash='#/ext/test/probe/probe'")
        .map_err(|e| e.to_string())?;
    let next = wait_for("closed control page", || {
        app.state::<App>()
            .pages
            .lock()
            .unwrap()
            .labels()
            .first()
            .cloned()
    })?;
    let w = wait_for("closed control webview", || app.get_webview(&next))?;
    wait_for("closed control welcome", || {
        eval(
            main,
            "document.querySelector('[data-part=extension-frame]')?.dataset.phase",
        )
        .filter(|v| v == "connected")
    })?;
    w.eval("window.addEventListener('message',e=>{if(e.data?.ingress)window.__ingressReport=e.data.ingress;});").map_err(|e|e.to_string())?;
    let closed = command(&w, "closed", "closed")?;
    require(
        closed["closed"]["status"] == 204 && closed["after"]["status"] == 409,
        "closed message did not stop subsequent calls",
    )?;
    log(
        "ingress_closed",
        json!({"closed":204,"subsequent_call":409}),
    );
    Ok(())
}
