//! The `tools` step of the Claude Code native check (`claude_code_native.rs`): the Tools page's Claude
//! Code row (`src/agentPages.ts`, `src/toolRows.tsx`), driven through the window's own page as a
//! person clicks it, with the real native confirmations, the run's core and its bundled test/alpha
//! extension. It runs after the `uninstall` step, so pluxy/claude-code is not installed (the curated
//! registry still offers it) and pluxy/execution is installed and enabled; the key added in the
//! `key` step is in the vault. Each refusal has a positive control on the same route:
//!
//! - open: Tools lists no Beginner Mode and no Manage Clients, and their old addresses go to Setup
//!   and to Tools; Claude Code's row offers Install add-on, nothing else;
//! - clients: Install add-on installs pluxy/claude-code from the curated registry, asking nothing,
//!   and the row then offers Turn on add-on;
//! - native: Turn on add-on opens the native confirmation, which says "full access to your user
//!   account"; while it shows, the row says to answer it and offers no live control, and the
//!   package stays not enabled however long the window waits; the dialog's own Cancel, pressed
//!   natively, leaves it off and the row says nothing of a failure; its Confirm, pressed natively,
//!   enables it and the row offers Set up;
//! - apply: Set up opens Claude Code's own page, with no way back to setup (it was opened from
//!   Tools); on a selected file holding the person's settings, a cancelled apply leaves the file
//!   byte for byte as it was; a confirmed one, naming the file and the key's label and never the
//!   key, writes core's address, the key and the default model mapping and keeps the person's own
//!   setting;
//! - launch: Start Claude Code from the page, confirmed natively, runs the stand-in terminal with
//!   `-e <claude>` in the chosen folder;
//! - leave: going to Tools and back writes nothing.

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
/// The row's add-on step: its button, and the line under the row.
const ADDON: &str = "[data-part=addon-action][data-row=claude-code]";
const NOTE: &str = "[data-part=addon-note]";
/// Claude Code's page.
const PAGE: &str = ".tool-page[data-tool=claude-code]";
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

/// The element in the window that selector names, as JavaScript.
fn within(selector: &str) -> String {
    format!("document.querySelector({})", json!(selector))
}

/// Clicks the enabled button reading text inside the window's element selector; waits for one.
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

/// Types value into the field labelled label inside the window's element selector, as typing does
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

/// Chooses the radio button of value inside the window's element selector; waits for it.
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

/// The text of the window's element selector, if it shows.
fn text(main: &Webview, selector: &str) -> Option<String> {
    js(
        main,
        &format!("return {}?.textContent ?? null;", within(selector)),
    )
    .and_then(|v| v.as_str().map(String::from))
}

/// The step the row's add-on button is for (install or enable), if the row has one.
fn step_of(main: &Webview) -> Option<String> {
    js(
        main,
        &format!(
            "return {}?.getAttribute('data-step') ?? null;",
            within(ADDON)
        ),
    )
    .and_then(|v| v.as_str().map(String::from))
}

fn wait_step(main: &Webview, want: &str) -> Result<(), String> {
    wait_for(&format!("the row to offer the {want} step"), || {
        (step_of(main).as_deref() == Some(want)).then_some(())
    })
    .map_err(|e| format!("{e} (it offers {:?})", step_of(main)))
}

/// Claude Code's row on the Tools page, as JavaScript.
const ROW: &str = "[...document.querySelectorAll('[data-part=tool-row]')].find((r) => r.querySelector('.tool-name')?.textContent.trim() === 'Claude Code')";

/// The labels of the row's buttons and the targets of its links, as JSON.
fn row_controls(main: &Webview) -> Option<Value> {
    js(
        main,
        &format!(
            "const r = {ROW}; if (!r) return null; return {{ buttons: [...r.querySelectorAll('button')].map((b) => b.textContent.trim()), links: [...r.querySelectorAll('a')].map((a) => [a.textContent.trim(), a.getAttribute('href')]) }};"
        ),
    )
    .filter(|v| !v.is_null())
}

fn wait_row(main: &Webview, buttons: &[&str], links: &[(&str, &str)]) -> Result<(), String> {
    let want = json!({ "buttons": buttons, "links": links.iter().map(|(t, h)| json!([t, h])).collect::<Vec<_>>() });
    wait_for(&format!("Claude Code's row to read {want}"), || {
        (row_controls(main).as_ref() == Some(&want)).then_some(())
    })
    .map_err(|e| format!("{e} (it reads {:?})", row_controls(main)))
}

/// The window's answer to cmd, which must succeed.
fn ok(main: &Webview, cmd: &str, args: Value) -> Result<Value, String> {
    super::admission::ok(main, cmd, args)
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
    let settings = p(&c.x["tools"]["settings"]);
    let original = s(&c.x["bytes"]["tools"]);
    let records = p(&c.x["records"]);
    let work = std::fs::canonicalize(p(&c.x["work"])).map_err(|e| e.to_string())?;
    let terminal = std::fs::canonicalize(p(&c.x["terminal"])).map_err(|e| e.to_string())?;
    let claude = std::fs::canonicalize(p(&c.x["claude"])).map_err(|e| e.to_string())?;
    let txt = |q: &Path| q.display().to_string();
    let step = |name: &str, r: Result<(), String>| r.map_err(|e| format!("{name}: {e}"));
    require(
        read(&settings).as_deref() == Some(original.as_str()),
        "the tools check's settings file is not as the fixture wrote it",
    )?;
    require(
        package(main, PACKAGE)?.is_null(),
        "pluxy/claude-code is still installed before the Tools check",
    )?;

    // ---- open: no Beginner Mode, no Manage Clients; Claude Code's row offers its add-on
    step(
        "open",
        (|| {
            main.eval("location.hash = '#/tools'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "Claude Code's row on Tools",
                &format!("return {ROW} !== undefined;"),
            )?;
            let old = js(
                main,
                "return [...document.querySelectorAll('a, option')].some((a) => ['#/easy-mode', '#/clients'].includes(a.getAttribute('href') ?? a.value)) || /Beginner Mode|Manage Clients/.test(document.body.textContent);",
            );
            require(
                old == Some(json!(false)),
                "Tools still names Beginner Mode or Manage Clients",
            )?;
            // The old addresses go to the pages that replaced them.
            for (from, to) in [("#/easy-mode", "#/setup"), ("#/clients", "#/tools")] {
                main.eval(format!("location.hash = '{from}'"))
                    .map_err(|e| e.to_string())?;
                until(
                    main,
                    &format!("{from} to go to {to}"),
                    &format!("return location.hash === '{to}';"),
                )?;
            }
            main.eval("location.hash = '#/tools'")
                .map_err(|e| e.to_string())?;
            // The add-on is offered by the curated registry and not installed: one quiet button.
            wait_row(main, &["Install add-on"], &[])
        })(),
    )?;

    // ---- clients: install asks nothing, and the row then offers to turn the add-on on
    step(
        "clients",
        (|| {
            wait_step(main, "install")?;
            let (r, asked) = super::transactions::unasked(|| -> Result<(), String> {
                click(main, ADDON, "Install add-on")?;
                wait_step(main, "enable")
            });
            r?;
            require(
                !asked,
                "installing from the row asked a native confirmation",
            )?;
            wait_row(main, &["Turn on add-on"], &[])?;
            let p = package(main, PACKAGE)?;
            require(
                p["enabled"] == false && p["source"]["name"] == "curated",
                format!("installed from the row: {p}"),
            )
        })(),
    )?;

    // ---- native: the row cannot answer the confirmation
    step(
        "native",
        (|| {
            click(main, ADDON, "Turn on add-on")?;
            let t = confirmation(c, &[PACKAGE, FULL_ACCESS])?;
            require(
                t.contains(FULL_ACCESS),
                format!("the turn-on confirmation: {t}"),
            )?;
            // While it shows: the row says to answer it, every control of the row is disabled and
            // clicks nothing through, and the package stays off.
            until(
                main,
                "the row to say to answer the confirmation window",
                &format!(
                    "return {}?.textContent.trim() === 'Answer the confirmation window to go on.';",
                    within(NOTE)
                ),
            )?;
            let controls = js(
                main,
                &format!(
                    "const b = [...({}?.querySelectorAll('button, input, select') ?? [])]; b.forEach((x) => x.click()); return b.length > 0 && b.every((x) => x.disabled);",
                    within(ADDON)
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
                step_of(main).as_deref() == Some("enable")
                    && package(main, PACKAGE)?["enabled"] == false,
                format!(
                    "the row shows {:?} and the package is on while the confirmation is open",
                    step_of(main)
                ),
            )?;
            let still = dialog_with(c.app, &[PACKAGE, FULL_ACCESS])?;
            require(!still.is_empty(), "the confirmation closed by itself")?;
            press("alt+c")?;
            wait_for("the row to settle the cancelled turn-on", || {
                js(
                    main,
                    &format!(
                        "return [...({}?.querySelectorAll('button') ?? [])].some((b) => b.textContent.trim() === 'Turn on add-on' && !b.disabled);",
                        within(ADDON)
                    ),
                )
                .filter(|v| v == true)
            })?;
            // A cancel is not a failure: the row says nothing of one.
            let said = js(
                main,
                &format!("return {ROW}?.textContent.includes('Could not') ?? null;"),
            );
            require(
                said == Some(json!(false)) && package(main, PACKAGE)?["enabled"] == false,
                format!("a cancelled turn-on turned the add-on on or said it failed ({said:?})"),
            )?;
            // Positive control: the dialog's own Confirm turns it on, and the row offers Set up.
            click(main, ADDON, "Turn on add-on")?;
            confirmation(c, &[PACKAGE, FULL_ACCESS])?;
            std::thread::sleep(SETTLE);
            press("alt+o")?;
            wait_row(main, &[], &[("Set up", "#/claude-code")])?;
            let p = package(main, PACKAGE)?;
            require(
                p["enabled"] == true && p["authorized"] == true,
                format!("confirmed turn-on: {p}"),
            )
        })(),
    )?;

    // ---- apply: Claude Code's own page; cancel keeps the file
    let page = PAGE;
    step(
        "apply",
        (|| {
            // The row's Set up opens the page; it was opened from Tools, so no way back to setup.
            until(
                main,
                "Set up on Claude Code's row",
                &format!(
                    "const a = {ROW}?.querySelector('a[data-row=claude-code]'); if (!a) return false; a.click(); return true;"
                ),
            )?;
            until(
                main,
                "Claude Code's page",
                &format!(
                    "return location.hash === '#/claude-code' && {} !== null && document.querySelector('[data-part=page-title]')?.textContent === 'Claude Code';",
                    within(page)
                ),
            )?;
            let back = js(
                main,
                "return document.querySelector('[data-part=setup-return]') !== null;",
            );
            require(
                back == Some(json!(false)),
                "a page opened from Tools shows the way back to setup",
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
                "a cancelled apply from the page changed the file",
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
                    "the file after the page's apply (core {target}) holds env {} and model {}",
                    redact(env, c.key),
                    v["model"]
                ),
            )
        })(),
    )?;

    // ---- launch: Start Claude Code from the page
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
            main.eval("location.hash = '#/tools'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "Claude Code's page to close",
                &format!("return {} === null;", within(page)),
            )?;
            main.eval("location.hash = '#/claude-code'")
                .map_err(|e| e.to_string())?;
            until(
                main,
                "Claude Code's page to open again",
                &format!("return {} !== null;", within(page)),
            )?;
            std::thread::sleep(Duration::from_millis(500));
            require(
                read(&settings) == before,
                "leaving and opening the page changed the file",
            )
        })(),
    )
}

/// env with the key's value replaced, for an error message.
fn redact(env: &Value, key: &str) -> String {
    env.to_string().replace(key, "<key>")
}
