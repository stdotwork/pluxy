//! The native test's privacy mode checks. `native/privacy.ts` starts the app once per stage, with
//! `PLUXY_APP_TEST_PRIVACY=<stage>`, in one session (its HOME, X server and bus), in managed mode
//! against the bundled test packages:
//!
//! - `first`: adds accounts whose labels are hostile identities (`$&…`, `@x`, an e-mail address
//!   that the account ID holds in a file-name form); with privacy mode off the Accounts page
//!   shows them as they are; the eye toggle masks them in text, tooltips and accessible names;
//!   an extension page's webview gets `privacy: true` in its welcome, then a theme event with
//!   `false` and one with `true` again as the toggle changes; the app quits with it on.
//! - `second`: the next launch starts masked, before anything is clicked; the toggle shows
//!   everything again (the person's explicit reveal); the app quits with it off.
//! - `damaged`: native/privacy.ts damaged the saved preference: the app starts masked.
//!
//! The label's account key is a canary: it must never be in the window's document. Every check
//! prints `native-test: privacy_check {"ok":...,"what":...}`; a stage ends with
//! `native-test: privacy_report {"stage":...,"failed":...}` and quits. The page webview's
//! deliveries are recorded by `delivered` (called from the app's delivery to a page webview,
//! only in this build).

use super::{eval, log};
use crate::{App, MAIN};
use serde_json::{json, Value};
use std::sync::Mutex;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager};

pub const AMP: &str = "$&abcdefghijk-project-77";
pub const AT: &str = "@x";
pub const EMAIL: &str = "alice.smith@example.com";
pub const EMAIL_ID: &str = "alice_smith_example_com";
// Worked out by hand from the old app's mask rules, as in next/ui/e2e/privacy.spec.ts.
const AMP_MASKED: &str = "$&***77";
const EMAIL_MASKED: &str = "al***@e***.com";
/// What must never be shown, or be in a tooltip or an accessible name, while masking.
const RAW: &[&str] = &[
    "abcdefghijk",
    "alice.smith",
    "alice_smith",
    "example.com",
    AT,
];
const TOGGLE: &str = "[data-part=privacy-toggle]";
const PAGE: &str = "ext/test/probe/probe";

/// The privacy value of each welcome and theme event a page webview took, in order.
static DELIVERED: Mutex<Vec<Option<bool>>> = Mutex::new(Vec::new());

/// A message the window sent a page webview reached it (its wrapper took it).
pub fn delivered(msg: &Value) {
    let privacy = if msg["type"] == "welcome" {
        &msg["data"]["theme"]["privacy"]
    } else if msg["type"] == "message" && msg["data"]["event"] == "theme" {
        &msg["data"]["data"]["privacy"]
    } else {
        return;
    };
    DELIVERED.lock().unwrap().push(privacy.as_bool());
}

#[derive(Default)]
struct Checks {
    failed: usize,
}

impl Checks {
    fn check(&mut self, ok: bool, what: impl Into<String>) -> bool {
        let what = what.into();
        log("privacy_check", json!({"ok": ok, "what": what}));
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

/// The window's text with every tooltip and accessible name.
fn shown(app: &AppHandle) -> String {
    main_eval(
        app,
        "document.body.innerText + '\\n' + [...document.querySelectorAll('[title], [aria-label]')].map((e) => (e.getAttribute('title') ?? '') + '\\n' + (e.getAttribute('aria-label') ?? '')).join('\\n')",
    )
    .and_then(|v| v.as_str().map(String::from))
    .unwrap_or_default()
}

fn pressed(app: &AppHandle) -> Option<String> {
    main_eval(
        app,
        &format!(
            "document.querySelector({})?.getAttribute('aria-pressed') ?? null",
            json!(TOGGLE)
        ),
    )
    .and_then(|v| v.as_str().map(String::from))
}

fn toggle(c: &mut Checks, app: &AppHandle, to: bool) {
    let clicked = main_eval(
        app,
        &format!(
            "(() => {{ const e = document.querySelector({}); if (!e) return false; e.click(); return true; }})()",
            json!(TOGGLE)
        ),
    ) == Some(json!(true));
    c.check(clicked, "the navigation has the privacy mode toggle");
    let want = if to { "true" } else { "false" };
    c.check(
        within(10, || (pressed(app).as_deref() == Some(want)).then_some(())).is_some(),
        format!(
            "the toggle shows privacy mode {}",
            if to { "on" } else { "off" }
        ),
    );
}

/// Opens the Accounts page and waits for the hostile accounts' rows.
fn accounts(c: &mut Checks, app: &AppHandle) {
    let _ = main_eval(app, "(location.hash = '#/accounts', true)");
    c.check(
        until(
            app,
            30,
            "[...document.querySelectorAll('[data-part=item]')].some((e) => e.textContent.includes('privacy-amp'))",
        ),
        "the Accounts page lists the hostile accounts",
    );
}

fn raw_shown(c: &mut Checks, app: &AppHandle, key: &str) {
    let all = shown(app);
    for raw in [AMP, AT, EMAIL, EMAIL_ID] {
        c.check(all.contains(raw), format!("privacy mode off shows {raw}"));
    }
    c.check(!all.contains(key), "the account's key is not in the window");
}

fn masked(c: &mut Checks, app: &AppHandle, key: Option<&str>, when: &str) {
    // The page draws again after the toggle: wait for the masked form first.
    c.check(
        within(10, || shown(app).contains(AMP_MASKED).then_some(())).is_some(),
        format!("{when}: the $& label shows as {AMP_MASKED}"),
    );
    let all = shown(app);
    c.check(
        all.contains(EMAIL_MASKED),
        format!("{when}: the e-mail label and ID show as {EMAIL_MASKED}"),
    );
    for raw in RAW {
        c.check(
            !all.contains(raw),
            format!("{when}: {raw} is not shown, nor in a tooltip or name"),
        );
    }
    if let Some(key) = key {
        c.check(
            !all.contains(key),
            format!("{when}: the account's key is not in the window"),
        );
    }
}

fn add(c: &mut Checks, app: &AppHandle, id: &str, label: &str, key: &str) {
    let caller = app.state::<App>().conn.lock().unwrap().caller();
    let body = json!({"id": id, "extension": "test/probe", "method": "api_key", "input": {"api_key": key, "label": label}});
    let r = caller.call("POST", "/accounts", Some(&body));
    c.check(
        r.status == 202,
        format!("core takes account {id} ({})", r.status),
    );
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

/// Waits for the page webview to have taken n welcome or theme messages; their privacy values.
fn deliveries(n: usize) -> Vec<Option<bool>> {
    within(20, || {
        let d = DELIVERED.lock().unwrap();
        (d.len() >= n).then(|| d.clone())
    })
    .unwrap_or_else(|| DELIVERED.lock().unwrap().clone())
}

fn first(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) {
        return;
    }
    let key = crate::managed::random_key();
    add(c, app, "privacy-amp", AMP, &key);
    add(c, app, "privacy-at", AT, &key);
    add(c, app, EMAIL_ID, EMAIL, &key);
    let caller = app.state::<App>().conn.lock().unwrap().caller();
    let labelled = within(30, || {
        let r = caller.call("GET", "/accounts", None);
        let accounts = r.body?["accounts"].as_array()?.clone();
        let label = |id: &str| {
            accounts
                .iter()
                .find(|a| a["id"] == id)
                .and_then(|a| a["label"].as_str().map(String::from))
        };
        (label("privacy-amp")? == AMP && label("privacy-at")? == AT && label(EMAIL_ID)? == EMAIL)
            .then_some(())
    });
    c.check(
        labelled.is_some(),
        "core lists the accounts with their labels",
    );
    accounts(c, app);
    c.check(
        pressed(app).as_deref() == Some("false"),
        "privacy mode starts off",
    );
    raw_shown(c, app, &key);
    toggle(c, app, true);
    masked(c, app, Some(&key), "privacy mode on");

    // An extension page in its own webview: told in its welcome, then by theme events.
    let _ = main_eval(
        app,
        &format!("(location.hash = {}, true)", json!(format!("#/{PAGE}"))),
    );
    let open = within(30, || {
        app.state::<App>()
            .pages
            .lock()
            .unwrap()
            .labels()
            .first()
            .cloned()
    });
    c.check(
        open.is_some(),
        "the extension page opens in a webview of its own",
    );
    let d = deliveries(1);
    c.check(
        d.first() == Some(&Some(true)),
        format!("the page's welcome says privacy is on ({d:?})"),
    );
    toggle(c, app, false);
    let d = deliveries(2);
    c.check(
        d.last() == Some(&Some(false)),
        format!("a theme event tells the page it is off ({d:?})"),
    );
    toggle(c, app, true);
    let d = deliveries(3);
    c.check(
        d.last() == Some(&Some(true)),
        format!("a theme event tells the page it is on ({d:?})"),
    );
}

fn second(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) {
        return;
    }
    c.check(
        pressed(app).as_deref() == Some("true"),
        "after a restart privacy mode is on before anything is clicked",
    );
    accounts(c, app);
    masked(c, app, None, "after a restart");
    // The person's explicit reveal: the toggle in the app's own window.
    toggle(c, app, false);
    c.check(
        within(10, || shown(app).contains(AMP).then_some(())).is_some(),
        "turned off, the $& label shows as it is",
    );
    let all = shown(app);
    for raw in [AT, EMAIL, EMAIL_ID] {
        c.check(all.contains(raw), format!("turned off, {raw} shows again"));
    }
}

fn damaged(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) {
        return;
    }
    c.check(
        pressed(app).as_deref() == Some("true"),
        "a damaged saved preference starts with privacy mode on",
    );
    accounts(c, app);
    masked(c, app, None, "with a damaged preference");
}

pub fn run(app: &AppHandle, stage: &str) -> Result<(), String> {
    let mut c = Checks::default();
    match stage {
        "first" => first(&mut c, app),
        "second" => second(&mut c, app),
        "damaged" => damaged(&mut c, app),
        _ => return Err(format!("no privacy stage {stage}")),
    }
    // Also after a stage stopped early, so privacy.ts says which checks failed.
    log(
        "privacy_report",
        json!({"stage": stage, "failed": c.failed}),
    );
    crate::quit(app);
    Ok(())
}
