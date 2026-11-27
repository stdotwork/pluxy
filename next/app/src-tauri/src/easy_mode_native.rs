//! The `easy` step of the Claude Code native check (`claude_code_native.rs`): Beginner Mode, the
//! guided setup (`src/easyMode.tsx`), driven through the window's own page as a person clicks it,
//! with the real native confirmations, the run's core and its bundled test/alpha extension. It runs
//! after the `uninstall` step, so pluxy/claude-code is not installed (the curated registry still
//! offers it) and pluxy/execution is installed and enabled; the key added in the `key` step is in
//! the vault. Each refusal has a positive control on the same route:
//!
//! - open: the window lists Beginner Mode and shows its two steps, Connect a model first;
//! - login: before any sign-in the guide counts core's accounts and, with none, keeps Next
//!   disabled; a sign-in with test/alpha's "Test API key" method in the guide's own Add an account
//!   (no account ID asked: core makes it) adds an account to core that was not there before (read
//!   back with `GET /accounts`), and the guide then counts it and enables Next;
//! - clients: Connect an agent lists Claude Code as offered by a registry (not installed) and
//!   pluxy/execution as enabled; Install installs pluxy/claude-code from the curated registry,
//!   asking nothing; Enable opens the native confirmation, which says "full access to your user
//!   account";
//! - native: while that confirmation shows, the guide offers no control of the package (every one
//!   of its buttons is disabled) and the package stays not enabled however long the window waits;
//!   the dialog's own Cancel, pressed natively, leaves it disabled; its Confirm, pressed natively,
//!   enables it;
//! - apply: the guide shows Claude Code's own page; on a selected file holding the person's
//!   settings, a cancelled apply leaves the file byte for byte as it was; a confirmed one, naming
//!   the file and the key's label and never the key, writes core's address, the key and the
//!   default model mapping and keeps the person's own setting;
//! - launch: Start Claude Code from the page in the guide, confirmed natively, runs the stand-in
//!   terminal with `-e <claude>` in the chosen folder;
//! - leave: going to another page and back writes nothing.

use super::admission::{press, require};
use super::claude_code::Seen;
use super::transactions::read;
use super::vault::dialog_with;
use super::{eval, wait_for};
use serde_json::{json, Value};
use std::path::{Path, PathBuf};
use std::time::Duration;
use tauri::{AppHandle, Webview};

const PACKAGE: &str = "pluxy/claude-code";
const EXECUTION: &str = "pluxy/execution";
const LABEL: &str = "fixture Claude Code key";
/// The page's one-press Connect.
const CONNECT: &str = "Connect Claude Code";
const FULL_ACCESS: &str = "full access to your user account";
/// The guide's own element.
const ROOT: &str = "[data-part=easy-mode]";
/// A native confirmation keeps Confirm disabled for a second after it shows.
const SETTLE: Duration = Duration::from_millis(1300);

/// What the step needs from the Claude Code check.
pub(super) struct Ctx<'a> {
    pub app: &'a AppHandle,
    pub main: &'a Webview,
    pub seen: &'a Seen,
    /// The key and the login canary.
    pub hidden: &'a [&'a str],
    /// expect.json.
    pub x: &'a Value,
    pub key: &'a str,
    /// The key's vault secret ID.
    pub secret: &'a str,
}

/// The value of the function body f in the window, as JSON.
fn js(main: &Webview, f: &str) -> Option<Value> {
    eval(main, &format!("(() => {{ {f} }})()"))
}

/// Waits until the function body f answers true in the window.
fn until(main: &Webview, what: &str, f: &str) -> Result<(), String> {
    wait_for(what, || (js(main, f) == Some(json!(true))).then_some(()))
}

/// The element in the guide that selector names, as JavaScript.
fn within(selector: &str) -> String {
    format!(
        "document.querySelector({})?.querySelector({})",
        json!(ROOT),
        json!(selector)
    )
}

/// Clicks the enabled button reading text inside the guide's element selector; waits for one.
fn click(main: &Webview, selector: &str, text: &str) -> Result<(), String> {
    until(
        main,
        &format!("an enabled {text:?} button in {selector}"),
        &format!(
            "const s = {}; const b = [...(s?.querySelectorAll('button') ?? [])].find((b) => b.textContent.trim() === {}); if (!b || b.disabled) return false; b.click(); return true;",
            within(selector),
            json!(text)
        ),
    )
}

/// Types value into the field labelled label inside the guide's element selector, as typing does
/// (the input event a page listens to); waits for the field. A field whose row names it (no label of
/// its own) is found by its accessible name.
fn fill(main: &Webview, selector: &str, label: &str, value: &str) -> Result<(), String> {
    until(
        main,
        &format!("the field {label:?} in {selector}"),
        &format!(
            "const s = {0}; const l = [...(s?.querySelectorAll('label') ?? [])].find((l) => l.querySelector('.label')?.textContent.trim() === {1}); \
             const i = l?.querySelector('input') ?? [...(s?.querySelectorAll('input[aria-label]') ?? [])].find((i) => i.getAttribute('aria-label') === {1}); \
             if (!i || i.disabled) return false; \
             Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, {2}); \
             i.dispatchEvent(new Event('input', {{ bubbles: true }})); return true;",
            within(selector),
            json!(label),
            json!(value)
        ),
    )
}

/// Chooses the radio button of value inside the guide's element selector; waits for it.
fn choose(main: &Webview, selector: &str, value: &str) -> Result<(), String> {
    until(
        main,
        &format!("the choice {value:?} in {selector}"),
        &format!(
            "const s = {}; const r = [...(s?.querySelectorAll('input[type=radio]') ?? [])].find((r) => r.value === {}); if (!r) return false; r.click(); return r.checked;",
            within(selector),
            json!(value)
        ),
    )
}

/// The text of the guide's element selector, if it shows.
fn text(main: &Webview, selector: &str) -> Option<String> {
    js(
        main,
        &format!("return {}?.textContent ?? null;", within(selector)),
    )
    .and_then(|v| v.as_str().map(String::from))
}

/// The row of package in the guide's lists.
fn row(package: &str) -> String {
    format!("[data-part=easy-package][data-package={}]", json!(package))
}

/// The state the guide shows for package: offered, installed, enabled.
fn state(main: &Webview, package: &str) -> Option<String> {
    js(
        main,
        &format!(
            "return {}?.getAttribute('data-state') ?? null;",
            within(&row(package))
        ),
    )
    .and_then(|v| v.as_str().map(String::from))
}

fn wait_state(main: &Webview, package: &str, want: &str) -> Result<(), String> {
    wait_for(&format!("the guide to show {package} {want}"), || {
        (state(main, package).as_deref() == Some(want)).then_some(())
    })
    .map_err(|e| format!("{e} (it shows {:?})", state(main, package)))
}

/// The window's answer to cmd, which must succeed.
fn ok(main: &Webview, cmd: &str, args: Value) -> Result<Value, String> {
    super::admission::ok(main, cmd, args)
}

/// Core's accounts, through the window's core call.
fn accounts(main: &Webview) -> Result<Vec<Value>, String> {
    let r = ok(
        main,
        "core_call",
        json!({"method": "GET", "path": "/accounts"}),
    )?;
    Ok(r["body"]["accounts"]
        .as_array()
        .cloned()
        .unwrap_or_default())
}

/// The device catalog's entry for package.
fn package(main: &Webview, id: &str) -> Result<Value, String> {
    let c = ok(main, "device_list", json!({}))?;
    Ok(c["packages"]
        .as_array()
        .and_then(|a| a.iter().find(|p| p["id"] == id).cloned())
        .unwrap_or(Value::Null))
}

/// The record a stand-in left: its arguments and folder.
fn stand_in(records: &Path, name: &str) -> Result<(Vec<String>, String), String> {
    wait_for(&format!("{name} to run"), || {
        records.join(format!("{name}.done")).exists().then_some(())
    })?;
    let r =
        |x: &str| std::fs::read_to_string(records.join(format!("{name}.{x}"))).unwrap_or_default();
    Ok((
        r("argv").lines().map(String::from).collect(),
        r("cwd").trim().to_string(),
    ))
}

/// The confirmation showing now, naming every one of needles and none of the hidden values.
fn confirmation(c: &Ctx, needles: &[&str]) -> Result<String, String> {
    let t = dialog_with(c.app, needles)?;
    c.seen.add(t.clone());
    if c.hidden.iter().any(|h| t.contains(h)) {
        std::thread::sleep(SETTLE);
        press("alt+c")?;
        return Err(format!(
            "a confirmation shows the key or the login canary: {t}"
        ));
    }
    Ok(t)
}

pub(super) fn run(c: &Ctx) -> Result<(), String> {
    let main = c.main;
    let s = |v: &Value| v.as_str().unwrap_or_default().to_string();
    let p = |v: &Value| PathBuf::from(s(v));
    let e = &c.x["easy"];
    let settings = p(&e["settings"]);
    let original = s(&c.x["bytes"]["easy"]);
    let records = p(&c.x["records"]);
    let work = std::fs::canonicalize(p(&c.x["work"])).map_err(|e| e.to_string())?;
    let terminal = std::fs::canonicalize(p(&c.x["terminal"])).map_err(|e| e.to_string())?;
    let claude = std::fs::canonicalize(p(&c.x["claude"])).map_err(|e| e.to_string())?;
    let txt = |q: &Path| q.display().to_string();
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    require(
        read(&settings).as_deref() == Some(original.as_str()),
        "the guide's settings file is not as the fixture wrote it",
    )?;
    require(
        package(main, PACKAGE)?.is_null(),
        "pluxy/claude-code is still installed before the guide",
    )?;

    // ---- open: listed among the topics of Tools, two steps, the first shown
    step(
        "open",
        (|| {
            main.eval("location.hash = '#/tools'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "Beginner Mode among the topics of Tools",
                "return [...document.querySelectorAll('[data-part=topics] a, [data-part=topics-select] option')].some((a) => (a.getAttribute('href') ?? a.value) === '#/easy-mode');",
            )
            .map_err(|_| "the window lists no Beginner Mode page (#/easy-mode)".to_string())?;
            main.eval("location.hash = '#/easy-mode'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "Beginner Mode's first step",
                &format!(
                    "return {} !== null && {} === null;",
                    within("[data-part=easy-step][data-step='1']"),
                    within("[data-part=easy-step][data-step='2']")
                ),
            )
        })(),
    )?;

    // ---- login: the guide's Add an account signs in with test/alpha's API-key method
    step(
        "login",
        (|| {
            let known = accounts(main)?;
            let before = known.len();
            let step1 = "[data-part=easy-step][data-step='1']";
            let sources = "[data-part=easy-sources]";
            wait_for("the guide's source count", || {
                text(main, sources).filter(|t| {
                    if before == 0 {
                        t.contains("No source connected yet")
                    } else {
                        t.contains(&format!("{before} connected source"))
                    }
                })
            })?;
            let next_enabled = || {
                js(
                    main,
                    &format!(
                        "const b = [...({}?.querySelectorAll('button') ?? [])].find((b) => b.textContent.trim() === 'Next: connect an agent'); return b ? !b.disabled : null;",
                        within(sources)
                    ),
                )
            };
            require(
                next_enabled() == Some(json!(before > 0)),
                format!("with {before} accounts, Next is {:?}", next_enabled()),
            )?;
            // test/alpha's API-key method.
            until(
                main,
                "test/alpha's Test API key method",
                &format!(
                    "const t = [...({}?.querySelectorAll('[data-part=method]') ?? [])].find((t) => t.closest('[data-extension]')?.dataset.extension === 'test/alpha' && t.textContent.includes('Test API key')); \
                     const b = t && [...t.querySelectorAll('button')].find((b) => b.textContent.trim() === 'Start'); if (!b || b.disabled) return false; b.click(); return true;",
                    within(step1)
                ),
            )?;
            fill(main, step1, "API key", &s(&e["api_key"]))?;
            click(main, step1, "Continue")?;
            wait_for("the sign-in to succeed", || {
                text(main, step1).filter(|t| t.contains("Added "))
            })?;
            let listed = accounts(main)?;
            require(
                listed.len() == before + 1
                    && listed
                        .iter()
                        .any(|a| !known.iter().any(|k| k["id"] == a["id"])),
                format!("core's accounts hold no new account: {known:?} then {listed:?}"),
            )?;
            wait_for("the guide to count the new account", || {
                text(main, sources)
                    .filter(|t| t.contains(&format!("{} connected source", listed.len())))
            })?;
            require(
                next_enabled() == Some(json!(true)),
                "Next stays disabled after the sign-in",
            )?;
            click(main, sources, "Next: connect an agent")?;
            until(
                main,
                "Connect an agent",
                &format!(
                    "return {} !== null;",
                    within("[data-part=easy-step][data-step='2']")
                ),
            )
        })(),
    )?;

    // ---- clients: offered and enabled packages; install asks nothing; enable asks natively
    step(
        "clients",
        (|| {
            wait_state(main, PACKAGE, "offered")?;
            wait_state(main, EXECUTION, "enabled")?;
            let (r, asked) = super::transactions::unasked(|| -> Result<(), String> {
                click(main, &row(PACKAGE), "Install")?;
                wait_state(main, PACKAGE, "installed")
            });
            r?;
            require(
                !asked,
                "installing from the guide asked a native confirmation",
            )?;
            let p = package(main, PACKAGE)?;
            require(
                p["enabled"] == false && p["source"]["name"] == "curated",
                format!("installed from the guide: {p}"),
            )
        })(),
    )?;

    // ---- native: the guide cannot answer the enable confirmation
    step(
        "native",
        (|| {
            click(main, &row(PACKAGE), "Enable")?;
            let t = confirmation(c, &[PACKAGE, FULL_ACCESS])?;
            require(
                t.contains(FULL_ACCESS),
                format!("the enable confirmation: {t}"),
            )?;
            // While it shows: every control of the row is disabled, the guide clicks nothing
            // through, and the package stays not enabled.
            let controls = js(
                main,
                &format!(
                    "const b = [...({}?.querySelectorAll('button, input, select') ?? [])]; b.forEach((x) => x.click()); return b.length > 0 && b.every((x) => x.disabled);",
                    within(&row(PACKAGE))
                ),
            );
            require(
                controls == Some(json!(true)),
                format!(
                    "a control of the row is enabled while the confirmation shows: {controls:?}"
                ),
            )?;
            std::thread::sleep(Duration::from_millis(1500));
            require(
                state(main, PACKAGE).as_deref() == Some("installed"),
                format!(
                    "the guide shows {:?} while the confirmation is open",
                    state(main, PACKAGE)
                ),
            )?;
            let still = dialog_with(c.app, &[PACKAGE, FULL_ACCESS])?;
            require(!still.is_empty(), "the confirmation closed by itself")?;
            press("alt+c")?;
            wait_for("the guide to settle the cancelled enable", || {
                js(
                    main,
                    &format!(
                        "return [...({}?.querySelectorAll('button') ?? [])].some((b) => b.textContent.trim() === 'Enable' && !b.disabled);",
                        within(&row(PACKAGE))
                    ),
                )
                .filter(|v| v == true)
            })?;
            require(
                state(main, PACKAGE).as_deref() == Some("installed")
                    && package(main, PACKAGE)?["enabled"] == false,
                "a cancelled enable enabled the package",
            )?;
            // Positive control: the dialog's own Confirm enables it.
            click(main, &row(PACKAGE), "Enable")?;
            confirmation(c, &[PACKAGE, FULL_ACCESS])?;
            std::thread::sleep(SETTLE);
            press("alt+o")?;
            wait_state(main, PACKAGE, "enabled")?;
            let p = package(main, PACKAGE)?;
            require(
                p["enabled"] == true && p["authorized"] == true,
                format!("confirmed enable: {p}"),
            )
        })(),
    )?;

    // ---- apply: Claude Code's own page in the guide; cancel keeps the file
    let page = "[data-part=easy-client-page]";
    step(
        "apply",
        (|| {
            click(main, &row(PACKAGE), "Set up")?;
            until(
                main,
                "Claude Code's page in the guide",
                &format!(
                    "return {}?.querySelector('[data-part=page-title]')?.textContent === 'Claude Code';",
                    within(page)
                ),
            )?;
            // The page reads Claude Code's own file as it opens; the check names another one under
            // "Settings file, key and backups" and reads that.
            choose(main, page, "file")?;
            fill(main, page, "File", &txt(&settings))?;
            click(main, page, "Read this file")?;
            wait_for("the page to read the file", || {
                text(main, &format!("{page} .tool-facts")).filter(|t| t.contains(&txt(&settings)))
            })
            .map_err(|e| format!("{e}: {:?}", text(main, page)))?;
            // The key the check entered: Connect then makes none.
            choose(main, page, c.secret)?;
            let needles = [PACKAGE, "apply", &txt(&settings), LABEL];
            // Cancelled: the file stays as it was.
            click(main, page, CONNECT)?;
            confirmation(c, &needles)?;
            std::thread::sleep(SETTLE);
            press("alt+c")?;
            wait_for("the page to settle the cancelled apply", || {
                js(
                    main,
                    &format!(
                        "return [...({}?.querySelectorAll('button') ?? [])].some((b) => b.textContent.trim() === {} && !b.disabled);",
                        within(page),
                        json!(CONNECT)
                    ),
                )
                .filter(|v| v == true)
            })?;
            std::thread::sleep(Duration::from_millis(500));
            require(
                read(&settings).as_deref() == Some(original.as_str()),
                "a cancelled apply from the guide changed the file",
            )?;
            // Confirmed: core's address, the key and the default mapping; the person's own value
            // stays.
            click(main, page, CONNECT)?;
            confirmation(c, &needles)?;
            std::thread::sleep(SETTLE);
            press("alt+o")?;
            wait_for("the page to show Claude Code connected", || {
                text(main, &format!("{page} .tool-head .badge")).filter(|t| t == "Connected")
            })
            .map_err(|e| format!("{e}: {:?}", text(main, page)))?;
            let target = s(&ok(main, "connection_get", json!({}))?["target"]);
            // Core's accounts serve none of Claude Code's own model names, so the page started
            // the three models with one core serves, not with claude-opus-5-5 and the others.
            let served: Vec<String> = ok(
                main,
                "core_call",
                json!({"method": "GET", "path": "/models"}),
            )?["body"]["models"]
                .as_array()
                .map(|m| m.iter().map(|x| s(&x["id"])).collect())
                .unwrap_or_default();
            let v: Value = serde_json::from_str(&read(&settings).unwrap_or_default())
                .map_err(|e| format!("the applied file is not JSON: {e}"))?;
            let env = &v["env"];
            require(
                !target.is_empty()
                    && env["ANTHROPIC_BASE_URL"] == target.trim_end_matches('/')
                    && env["ANTHROPIC_AUTH_TOKEN"] == c.key
                    && env["API_TIMEOUT_MS"] == "1"
                    && served.contains(&s(&env["ANTHROPIC_DEFAULT_OPUS_MODEL"]))
                    && env["ANTHROPIC_DEFAULT_SONNET_MODEL"] == env["ANTHROPIC_DEFAULT_OPUS_MODEL"]
                    && env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] == env["ANTHROPIC_DEFAULT_OPUS_MODEL"]
                    && v["model"] == "opus",
                format!(
                    "the file after the guide's apply (core {target}) holds env {} and model {}",
                    redact(env, c.key),
                    v["model"]
                ),
            )
        })(),
    )?;

    // ---- launch: Start Claude Code from the page in the guide
    step(
        "launch",
        (|| {
            for name in ["terminal", "claude"] {
                for x in ["argv", "cwd", "env", "done"] {
                    let _ = std::fs::remove_file(records.join(format!("{name}.{x}")));
                }
            }
            fill(main, page, "Folder", &txt(&work))?;
            click(main, page, "Start Claude Code")?;
            confirmation(
                c,
                &[
                    EXECUTION,
                    "launch",
                    &txt(&terminal),
                    &txt(&claude),
                    &txt(&work),
                ],
            )?;
            std::thread::sleep(SETTLE);
            press("alt+o")?;
            let (argv, cwd) = stand_in(&records, "terminal")?;
            require(
                argv == vec!["-e".to_string(), txt(&claude)] && cwd == txt(&work),
                format!("the terminal ran with {argv:?} in {cwd}"),
            )?;
            let (argv, cwd) = stand_in(&records, "claude")?;
            require(
                argv.is_empty() && cwd == txt(&work),
                format!("claude ran with {argv:?} in {cwd}"),
            )
        })(),
    )?;

    // ---- leave: another page and back writes nothing
    step(
        "leave",
        (|| {
            let before = read(&settings);
            main.eval("location.hash = '#/status'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "the guide to close",
                &format!("return document.querySelector({}) === null;", json!(ROOT)),
            )?;
            main.eval("location.hash = '#/easy-mode'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "the guide to open again",
                &format!("return document.querySelector({}) !== null;", json!(ROOT)),
            )?;
            std::thread::sleep(Duration::from_millis(500));
            require(
                read(&settings) == before,
                "leaving and opening the guide changed the file",
            )
        })(),
    )
}

/// env with the key's value replaced, for an error message.
fn redact(env: &Value, key: &str) -> String {
    env.to_string().replace(key, "<key>")
}
