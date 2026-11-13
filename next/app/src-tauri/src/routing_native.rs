//! The native test's check of cross-package routing and import authorization (native-test builds
//! with `artifact-test-trust` only). It drives the real app through the window's own commands,
//! real page webviews and the real GTK native confirmations, with the signed tools and web
//! packages `native/routing.ts` built (their digests and the expected answers are in
//! `expect.json`, worked out there, not by the app). Each refusal has a positive control on the
//! same route, and the target's call count shows a refused call never reached it:
//!
//! - deps: a tool whose required import is not enabled, or needs an export contract no release
//!   has, is refused before any confirmation; once its target is enabled, its confirmation lists
//!   its imports and it is enabled;
//! - native-route: the window calls pluxy/relay, which calls pluxy/target through the host; the
//!   target sees the chain the host set (the window, then relay at its digest and generation);
//! - undeclared: relay calling an operation it does not import, or a package it does not
//!   import, is refused;
//! - forged: relay naming a caller chain or caller itself, or a host call that is not the one it
//!   serves, is refused;
//! - cyclic: a tool whose imports lead back to it is refused before any confirmation;
//! - web: a curated web package (like Easy mode) installs as curated; its page's call is refused
//!   until the person confirms its imports natively, then answered, with the page as the chain;
//!   its write-class call asks again (cancelled: never ran; confirmed: ran once); an Unverified
//!   package cannot get import consent and its page's call is refused; a window call cancelled
//!   while its write's confirmation is open closes the dialog and never runs; a native tool's own
//!   page calls its own operation;
//! - stale: once the target is updated (a swap, confirmed), the consent naming it is gone and the
//!   call is refused until consent is given again; native routes reach the new release;
//! - revoke: revoking the consent while a page's call runs delivers no result to it, and the
//!   next call is refused;
//! - launch: a launch-class call started by a native click asks, and with "remember" ticked, the
//!   next identical one from a native click runs without asking; the same launch through relay
//!   (another chain) asks again, as does one from the window without a native click; through a web
//!   package's import it asks each time and offers no "remember".
//!
//! One line on stderr: `native-test: routing <JSON>`.

/// The label of a launch confirmation's "remember" checkbox, as the dialog shows it.
const REMEMBER: &str = "Remember this exact executable, argv and cwd";

use super::admission::{dialog, dialog_text, ok, press, require, settle, start};
use super::{eval, log, wait_for};
use serde_json::{json, Value};
use std::path::PathBuf;
use std::sync::mpsc;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager, Webview};

const TARGET: &str = "pluxy/target";
const RELAY: &str = "pluxy/relay";
const GUIDE: &str = "pluxy/guide";
const VIEWER: &str = "example/hub-viewer";

/// Whether a native confirmation is showing now.
fn showing() -> bool {
    super::admission::x(&[
        "search",
        "--onlyvisible",
        "--name",
        "^pluxy native confirmation$",
    ])
    .is_ok_and(|s| !s.is_empty())
}

/// Starts cmd and requires it refused, saying says, before any native confirmation shows. A
/// confirmation that shows is cancelled and fails the check.
fn refused_unasked(main: &Webview, cmd: &str, args: Value, says: &str) -> Result<String, String> {
    let slot = start(main, cmd, args.clone())?;
    let deadline = Instant::now() + Duration::from_secs(20);
    loop {
        if let Some(r) = eval(
            main,
            &format!("window.__pluxyAdmission?.[{}] ?? null", json!(slot)),
        )
        .filter(|v| !v.is_null())
        {
            let error = r["error"].as_str().unwrap_or_default().to_string();
            require(
                r["ok"] == false && error.contains(says),
                format!("{cmd} {args} should be refused with {says:?}: {r}"),
            )?;
            return Ok(error);
        }
        if showing() {
            press("alt+c")?;
            let _ = settle(main, &slot);
            return Err(format!(
                "{cmd} {args} asked for a native confirmation; it should be refused with {says:?}"
            ));
        }
        if Instant::now() > deadline {
            return Err(format!("{cmd} {args}: no answer"));
        }
        std::thread::sleep(Duration::from_millis(200));
    }
}

/// Starts cmd, waits for its native confirmation and returns its text and the call's slot.
fn asked(
    app: &AppHandle,
    main: &Webview,
    cmd: &str,
    args: Value,
) -> Result<(String, String), String> {
    let slot = start(main, cmd, args)?;
    let text = dialog_text(app)?;
    Ok((text, slot))
}

/// Enables native tool id through its confirmation, confirmed; returns the confirmation's text.
fn enable(app: &AppHandle, main: &Webview, id: &str) -> Result<String, String> {
    let (text, slot) = asked(
        app,
        main,
        "device_enable",
        json!({"id": id, "enabled": true}),
    )?;
    press("alt+o")?;
    let r = settle(main, &slot)?;
    require(r["ok"] == true, format!("confirmed enable of {id}: {r}"))?;
    Ok(text)
}

fn install(main: &Webview, registry: &str, id: &str, version: &str) -> Result<(), String> {
    ok(
        main,
        "device_install",
        json!({"registry": registry, "id": id, "version": version}),
    )
    .map(|_| ())
}

/// The window's call of op of package with input: `{ok, value}` or `{ok: false, error}`.
fn call(main: &Webview, package: &str, op: &str, input: Value) -> Result<Value, String> {
    let slot = start(
        main,
        "program_call",
        json!({"package": package, "op": op, "input": input}),
    )?;
    settle(main, &slot)
}

/// Relay's answer for its call of op of package with input, the frame's extra fields and for.
fn relay(
    main: &Webview,
    package: &str,
    op: &str,
    input: Value,
    extra: Value,
) -> Result<Value, String> {
    let mut i = json!({"package": package, "op": op, "input": input});
    if let Some(m) = extra.as_object() {
        for (k, v) in m {
            i[k] = v.clone();
        }
    }
    let r = call(main, RELAY, "relay", i)?;
    require(r["ok"] == true, format!("relay's own call: {r}"))?;
    Ok(r["value"].clone())
}

fn package_view(main: &Webview, id: &str) -> Result<Value, String> {
    let list = ok(main, "device_list", json!({}))?;
    list["packages"]
        .as_array()
        .and_then(|a| a.iter().find(|p| p["id"] == id).cloned())
        .ok_or_else(|| format!("{id} is not listed: {list}"))
}

/// A page of package id, opened in the window, with its channel up. The window opens its webview
/// and answers its hello, as it does for the person; it shows one page at a time.
struct Page {
    app: AppHandle,
    label: String,
    wv: Webview,
    next: u64,
}

impl Page {
    fn open(app: &AppHandle, main: &Webview, id: &str) -> Result<Page, String> {
        let route = format!("device/{id}/page");
        wait_for(&format!("{route} in the navigation"), || {
            eval(
                main,
                &format!("document.querySelector('a[href=\"#/{route}\"]') !== null"),
            )
            .filter(|v| v == true)
        })?;
        main.eval(format!("location.hash = {}", json!(format!("#/{route}"))))
            .map_err(|e| e.to_string())?;
        let label = wait_for(&format!("a page webview showing {id}"), || {
            app.webviews().into_iter().find_map(|(label, w)| {
                (label != crate::MAIN
                    && eval(&w, "document.querySelector('iframe')?.src ?? null")
                        .and_then(|v| v.as_str().map(|s| s.contains(&format!("/{id}/"))))
                        == Some(true))
                .then_some(label)
            })
        })?;
        let wv = app
            .get_webview(&label)
            .ok_or_else(|| format!("no webview {label}"))?;
        wv.eval(
            "window.__route = {replies: {}, ready: false}; addEventListener('message', (e) => { \
             const d = e.data; if (d && d.routeReady) window.__route.ready = true; \
             if (d && d.routeReply && typeof d.routeReply.id === 'string') \
             window.__route.replies[d.routeReply.id] = d.routeReply; })",
        )
        .map_err(|e| e.to_string())?;
        wait_for("the page's channel", || {
            eval(&wv, "window.__route?.ready === true").filter(|v| v == true)
        })?;
        Ok(Page {
            app: app.clone(),
            label,
            wv,
            next: 0,
        })
    }

    /// Sends a native.call of op of package with input from the page, through its channel.
    fn send(&mut self, package: &str, op: &str, input: Value) -> Result<String, String> {
        self.next += 1;
        let id = format!("r{}", self.next);
        self.wv
            .eval(format!(
                "document.querySelector('iframe').contentWindow.postMessage({{routeCall: {{id: {}, method: 'native.call', params: {}}}}}, '*')",
                json!(id),
                json!({"package": package, "op": op, "input": input}),
            ))
            .map_err(|e| e.to_string())?;
        Ok(id)
    }

    fn reply(&self, id: &str) -> Result<Value, String> {
        wait_for(&format!("the page's reply {id}"), || {
            eval(
                &self.wv,
                &format!("window.__route.replies[{}] ?? null", json!(id)),
            )
            .filter(|v| !v.is_null())
        })
    }

    fn call(&mut self, package: &str, op: &str, input: Value) -> Result<Value, String> {
        let id = self.send(package, op, input)?;
        self.reply(&id)
    }

    /// Leaves the page in the window, which closes its webview.
    fn close(self, main: &Webview) -> Result<(), String> {
        main.eval("location.hash = '#/device'")
            .map_err(|e| e.to_string())?;
        wait_for(&format!("{} to close", self.label), || {
            self.app.get_webview(&self.label).is_none().then_some(())
        })
    }
}

/// Starts the window's call of op of package with input from a native button's click, which
/// makes a native gesture; the answer comes on the receiver.
fn clicked(
    app: &AppHandle,
    package: &str,
    op: &str,
    input: Value,
) -> Result<mpsc::Receiver<Result<Value, crate::program::CallError>>, String> {
    let (tx, rx) = mpsc::channel();
    let (a, package, op) = (app.clone(), package.to_string(), op.to_string());
    crate::confirmation::native::click(app, "pluxy routing Start", move |gesture| {
        std::thread::spawn(move || {
            let r = a
                .state::<crate::App>()
                .programs
                .begin_call(None)
                .and_then(|call| {
                    crate::route::call_main(&a, call, &package, &op, input, Some(gesture))
                });
            let _ = tx.send(r);
        });
    })?;
    Ok(rx)
}

fn answer(
    rx: &mpsc::Receiver<Result<Value, crate::program::CallError>>,
) -> Result<Result<Value, crate::program::CallError>, String> {
    rx.recv_timeout(Duration::from_secs(90))
        .map_err(|_| "no answer to the clicked call".to_string())
}

fn forbidden(r: &Value, what: &str) -> Result<(), String> {
    require(
        r["error"]["code"] == "forbidden" && r.get("result").is_none(),
        format!("{what} should be refused as forbidden: {r}"),
    )
}

pub(crate) fn run(app: &AppHandle, main: &Webview, fixtures: PathBuf) -> Result<(), String> {
    let expect: Value = serde_json::from_slice(
        &std::fs::read(fixtures.join("expect.json")).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    let sha = |id: &str, version: &str| expect["sha256"][format!("{id} {version}")].clone();
    let items = expect["items"].clone();
    let summary_ok =
        |out: &Value| out["count"] == expect["count"] && out["longest"] == expect["longest"];
    let mut out = json!({});
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));

    // ---- deps
    step(
        "deps",
        (|| {
            for id in [
                TARGET,
                RELAY,
                "pluxy/needs-v2",
                "pluxy/cycle-a",
                "pluxy/cycle-b",
            ] {
                install(main, "curated", id, "1.0.0")?;
            }
            refused_unasked(
                main,
                "device_enable",
                json!({"id": RELAY, "enabled": true}),
                TARGET,
            )?;
            enable(app, main, TARGET)?;
            refused_unasked(
                main,
                "device_enable",
                json!({"id": "pluxy/needs-v2", "enabled": true}),
                "contract",
            )?;
            let text = enable(app, main, RELAY)?;
            require(
                text.contains(TARGET) && text.contains("summary") && text.contains("launch"),
                format!("relay's confirmation does not list its imports: {text}"),
            )
        })(),
    )?;

    // ---- native-route
    let mut calls = 0u64;
    step(
        "native-route",
        (|| {
            let a = relay(
                main,
                TARGET,
                "summary",
                json!({"items": items}),
                Value::Null,
            )?;
            let o = &a["answer"]["output"];
            require(summary_ok(o), format!("relay's answer: {a}"))?;
            require(
                a["chain"] == json!([{"kind": "main"}]),
                format!("relay's own chain: {a}"),
            )?;
            let admitted = app
                .state::<crate::App>()
                .device
                .lock()
                .unwrap()
                .admitted(RELAY, &crate::trust::TrustStore::current())?;
            let want = json!([{"kind": "main"},
            {"kind": "package", "id": RELAY, "sha256": sha(RELAY, "1.0.0"), "generation": admitted.installed_at()}]);
            require(
                o["chain"] == want,
                format!("the target saw chain {}, not {want}", o["chain"]),
            )?;
            calls = o["calls"].as_u64().unwrap_or_default();
            out["chain"] = o["chain"].clone();
            Ok(())
        })(),
    )?;

    // ---- undeclared and forged: refused, and the target never saw them
    let refused = |a: &Value, what: &str| {
        require(
            a["answer"]["error"]["code"] == "permission" && a["answer"].get("output").is_none(),
            format!("{what} should be refused: {a}"),
        )
    };
    step(
        "undeclared",
        (|| {
            let a = relay(main, TARGET, "store", json!({"value": "x"}), Value::Null)?;
            refused(&a, "relay calling target's store, which it does not import")?;
            let a = relay(main, "pluxy/cycle-a", "relay", json!({}), Value::Null)?;
            refused(&a, "relay calling a package it does not import")
        })(),
    )?;
    step(
        "forged",
        (|| {
            let a = relay(
                main,
                TARGET,
                "summary",
                json!({"items": items}),
                json!({"extra": {"chain": [{"kind": "page", "id": GUIDE}]}}),
            )?;
            refused(&a, "relay naming a chain")?;
            let a = relay(
                main,
                TARGET,
                "summary",
                json!({"items": items}),
                json!({"extra": {"caller": GUIDE}}),
            )?;
            refused(&a, "relay naming a caller")?;
            let a = relay(
                main,
                TARGET,
                "summary",
                json!({"items": items}),
                json!({"for": "c-1"}),
            )?;
            refused(&a, "relay naming a host call it does not serve")?;
            // The control: the same call as relay serves it reaches the target, which saw none of
            // the refused ones.
            let a = relay(
                main,
                TARGET,
                "summary",
                json!({"items": items}),
                Value::Null,
            )?;
            require(
                summary_ok(&a["answer"]["output"]) && a["answer"]["output"]["calls"] == calls + 1,
                format!("the control after the refusals: {a} (the target had {calls} calls)"),
            )
        })(),
    )?;

    // ---- cyclic
    step(
        "cyclic",
        (|| {
            enable(app, main, "pluxy/cycle-a")?;
            refused_unasked(
                main,
                "device_enable",
                json!({"id": "pluxy/cycle-b", "enabled": true}),
                "cycle",
            )
            .map(|_| ())
        })(),
    )?;

    // ---- web
    let mut guide: Option<Page> = None;
    step(
        "web",
        (|| {
            install(main, "curated", GUIDE, "1.0.0")?;
            install(main, "untrusted", VIEWER, "1.0.0")?;
            let g = package_view(main, GUIDE)?;
            require(
                g["trust"] == "curated" && g["enabled"] == true,
                format!("the guide: {g}"),
            )?;
            let v = package_view(main, VIEWER)?;
            require(v["trust"] == "unverified", format!("the viewer: {v}"))?;
            let mut page = Page::open(app, main, GUIDE)?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            forbidden(&r, "the guide's call before consent")?;
            let (text, slot) = asked(
                app,
                main,
                "device_imports",
                json!({"id": GUIDE, "allowed": true}),
            )?;
            let digest = sha(GUIDE, "1.0.0");
            let digest = digest.as_str().unwrap_or("?");
            for want in [GUIDE, digest, TARGET, "summary", "store", "write", "launch"] {
                require(
                    text.contains(want),
                    format!("the import confirmation lacks {want}: {text}"),
                )?;
            }
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("confirmed imports: {r}"))?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            require(
                summary_ok(&r["result"]),
                format!("the guide's summary: {r}"),
            )?;
            require(
                r["result"]["chain"]
                    == json!([{"kind": "page", "id": GUIDE, "sha256": sha(GUIDE, "1.0.0"), "page": "page"}]),
                format!("the target saw chain {}", r["result"]["chain"]),
            )?;
            let before = r["result"]["calls"].as_u64().unwrap_or_default();
            // A write-class call asks, each time, in a page-started confirmation; cancelled, it
            // never runs.
            let id = page.send(TARGET, "store", json!({"value": "from the guide"}))?;
            let text = dialog_text(app)?;
            require(
                text.contains("store") && text.contains(GUIDE),
                format!("the store confirmation: {text}"),
            )?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+c")?;
            forbidden(&page.reply(&id)?, "the cancelled store")?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            require(
                r["result"]["calls"] == before + 1,
                format!("the summary after the cancelled store: {r} (had {before})"),
            )?;
            // The control on the same route: confirmed, the write runs, once.
            std::thread::sleep(Duration::from_millis(1100));
            let id = page.send(TARGET, "store", json!({"value": "confirmed"}))?;
            let text = dialog_text(app)?;
            require(
                text.contains("store") && text.contains("confirmed"),
                format!("the second store confirmation: {text}"),
            )?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+o")?;
            let r = page.reply(&id)?;
            require(
                r["result"]["stored"] == "confirmed" && r["result"]["calls"] == before + 2,
                format!("the confirmed store: {r} (had {before})"),
            )?;
            // Unverified: no consent, and its page's call refused; the guide's on the same route is
            // the control.
            refused_unasked(
                main,
                "device_imports",
                json!({"id": VIEWER, "allowed": true}),
                "Unverified",
            )?;
            let mut viewer = Page::open(app, main, VIEWER)?;
            forbidden(
                &viewer.call(TARGET, "summary", json!({"items": items}))?,
                "the Unverified viewer's call",
            )?;
            viewer.close(main)?;
            // The window shows one page at a time: the guide's again.
            let mut page = Page::open(app, main, GUIDE)?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            require(
                summary_ok(&r["result"]),
                format!("the control after the viewer: {r}"),
            )?;
            // A window call cancelled while its write's confirmation is open: the dialog closes
            // and the write never runs.
            let calls = r["result"]["calls"].as_u64().unwrap_or_default();
            let slot = start(
                main,
                "program_call",
                json!({"package": TARGET, "op": "store", "input": {"value": "withdrawn"},
                    "ticket": "routing-withdrawn"}),
            )?;
            dialog_text(app)?;
            ok(
                main,
                "program_cancel",
                json!({"ticket": "routing-withdrawn"}),
            )?;
            let r = settle(main, &slot)?;
            require(
                r["ok"] == false && r["error"].as_str().is_some_and(|e| e.contains("cancelled")),
                format!("the cancelled call with its confirmation open: {r}"),
            )?;
            wait_for("the withdrawn confirmation to close", || {
                app.state::<crate::App>()
                    .confirmations
                    .lock()
                    .unwrap()
                    .showing
                    .is_empty()
                    .then_some(())
            })?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            require(
                r["result"]["calls"] == calls + 1,
                format!("the target after the withdrawn write: {r} (had {calls})"),
            )?;
            // A native tool's own page calls its own operation.
            let mut own = Page::open(app, main, TARGET)?;
            let r = own.call(TARGET, "summary", json!({"items": items}))?;
            require(
                summary_ok(&r["result"]),
                format!("the target's own page: {r}"),
            )?;
            own.close(main)?;
            guide = Some(Page::open(app, main, GUIDE)?);
            Ok(())
        })(),
    )?;
    let mut page = guide.ok_or("no guide page")?;

    // ---- stale
    step(
        "stale",
        (|| {
            // The target is enabled, so its update is a swap with its own native confirmation;
            // confirmed, 2.0.0 serves, enabled and admitted.
            let (text, slot) = asked(
                app,
                main,
                "device_install",
                json!({"registry": "curated", "id": TARGET, "version": "2.0.0"}),
            )?;
            require(
                text.contains(TARGET) && text.contains("2.0.0"),
                format!("the update's confirmation: {text}"),
            )?;
            press("alt+o")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == true, format!("the confirmed update: {r}"))?;
            let t = package_view(main, TARGET)?;
            require(
                t["active"]["version"] == "2.0.0"
                    && t["enabled"] == true
                    && t["authorized"] == true,
                format!("the target after its update: {t}"),
            )?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            forbidden(&r, "the guide's call after the target changed")?;
            let a = relay(
                main,
                TARGET,
                "summary",
                json!({"items": items}),
                Value::Null,
            )?;
            require(
                summary_ok(&a["answer"]["output"]),
                format!("relay to the new target: {a}"),
            )?;
            let (_, slot) = asked(
                app,
                main,
                "device_imports",
                json!({"id": GUIDE, "allowed": true}),
            )?;
            press("alt+o")?;
            require(
                settle(main, &slot)?["ok"] == true,
                "consent for the new target",
            )?;
            let r = page.call(TARGET, "summary", json!({"items": items}))?;
            require(
                summary_ok(&r["result"]),
                format!("the guide's call after consent again: {r}"),
            )
        })(),
    )?;

    // ---- revoke
    step(
        "revoke",
        (|| {
            let id = page.send(TARGET, "wait", json!({}))?;
            wait_for("the target to run the guide's wait", || {
                ok(main, "program_status", json!({"package": TARGET}))
                    .ok()
                    .filter(|s| s["state"] == "active")
            })?;
            ok(
                main,
                "device_imports",
                json!({"id": GUIDE, "allowed": false}),
            )?;
            let r = page.reply(&id)?;
            require(
                r.get("error").is_some() && r.get("result").is_none(),
                format!("the wait after revoke: {r}"),
            )?;
            forbidden(
                &page.call(TARGET, "summary", json!({"items": items}))?,
                "the guide's call after revoke",
            )?;
            let (_, slot) = asked(
                app,
                main,
                "device_imports",
                json!({"id": GUIDE, "allowed": true}),
            )?;
            press("alt+o")?;
            require(
                settle(main, &slot)?["ok"] == true,
                "consent again after revoke",
            )
        })(),
    )?;

    // ---- launch
    step(
        "launch",
        (|| {
            let launch = expect["launch"].clone();
            let argv = launch["argv"].clone();
            // Started by a native click: asks the first time; remembered, the next runs unasked.
            let rx = clicked(app, TARGET, "launch", launch.clone())?;
            let text = dialog_text(app)?;
            require(
                text.contains(launch["executable"].as_str().unwrap_or("?"))
                    && text.contains("main window")
                    && text.contains(REMEMBER),
                format!("the clicked launch's confirmation: {text}"),
            )?;
            press("alt+r")?;
            press("alt+o")?;
            let r = answer(&rx)?.map_err(|e| format!("the confirmed clicked launch: {e}"))?;
            require(r["launched"] == argv, format!("the clicked launch: {r}"))?;
            let rx = clicked(app, TARGET, "launch", launch.clone())?;
            let r = answer(&rx)?.map_err(|e| format!("the remembered launch: {e}"))?;
            require(
                r["launched"] == argv && !showing(),
                format!("the remembered launch ran without asking: {r}"),
            )?;
            // The same launch through relay is another chain: it asks again.
            let rx = clicked(
                app,
                RELAY,
                "relay",
                json!({"package": TARGET, "op": "launch", "input": launch}),
            )?;
            let text = dialog_text(app)?;
            require(
                text.contains(RELAY),
                format!("the relayed launch's confirmation: {text}"),
            )?;
            press("alt+c")?;
            let r = answer(&rx)?.map_err(|e| format!("relay's own call: {e}"))?;
            require(
                r["answer"]["error"]["code"] == "cancelled",
                format!("the relayed launch, cancelled: {r}"),
            )?;
            // The window's call without a native gesture asks.
            let slot = start(
                main,
                "program_call",
                json!({"package": TARGET, "op": "launch", "input": launch}),
            )?;
            let text = dialog_text(app)?;
            require(
                text.contains(launch["executable"].as_str().unwrap_or("?"))
                    && text.contains("--routing-check"),
                format!("the launch confirmation: {text}"),
            )?;
            press("alt+c")?;
            let r = settle(main, &slot)?;
            require(r["ok"] == false, format!("the cancelled launch: {r}"))?;
            // Through the guide's import: asks each time, and offers no "remember", which a page
            // chain could never use.
            let id = page.send(TARGET, "launch", launch.clone())?;
            let text = dialog_text(app)?;
            require(
                !text.contains(REMEMBER),
                format!("the guide's launch confirmation offers to remember: {text}"),
            )?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+o")?;
            let r = page.reply(&id)?;
            require(
                r["result"]["launched"] == argv,
                format!("the guide's confirmed launch: {r}"),
            )?;
            std::thread::sleep(Duration::from_millis(1100));
            let id = page.send(TARGET, "launch", launch.clone())?;
            dialog()?;
            std::thread::sleep(Duration::from_millis(1100));
            press("alt+c")?;
            forbidden(&page.reply(&id)?, "the guide's second launch, cancelled")
        })(),
    )?;
    page.close(main)?;

    out["passed"] = json!(true);
    log("routing", out);
    Ok(())
}
