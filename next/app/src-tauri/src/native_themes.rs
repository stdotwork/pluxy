//! The native test's themes checks. `native/privacy.ts` starts the app once per stage, with
//! `PLUXY_APP_TEST_THEMES=<stage>`, after the privacy stages and in their session (its HOME, X
//! server and bus), in managed mode against the bundled test packages:
//!
//! - `add`: on the Themes page, the T3 Code theme file `ui/tests/fixtures/t3-docs-nightfall.json`,
//!   unchanged, is pasted into "Or paste a theme file's JSON" and added: the window paints in its
//!   canvas colour (`--px-color-bg` and `--app-theme-canvas` on the root element), the root has
//!   class `dark`, and the picker lists Nightfall, chosen; `GET /themes` through the app's host
//!   (the `core_call` command) has `t3/nightfall` as `current`. Then Export is pressed: the page
//!   says where the file was saved (in privacy mode only its folder and name), that is
//!   `nightfall.json` in the session's Downloads folder, and the file there, read from disk,
//!   holds the theme file's JSON. The app saves it
//!   itself (the `file_save` command), with no dialog to drive.
//! - `restart`: the next launch paints in that theme before anything is clicked, and its Themes
//!   page has Nightfall chosen; the stage stays on that page long enough for the session's last
//!   screenshot to show it.
//!
//! Every check prints `native-test: themes_check {"ok":...,"what":...}`; a stage ends with
//! `native-test: themes_report {"stage":...,"failed":...}` and quits.

use super::{eval, log};
use crate::MAIN;
use serde_json::{json, Value};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Manager};

const THEME: &str = include_str!("../../../ui/tests/fixtures/t3-docs-nightfall.json");
const ID: &str = "t3/nightfall";
const NAME: &str = "Nightfall";
/// The file's `canvas`, #1a1b26, as red, green and blue.
const CANVAS: [i64; 3] = [0x1a, 0x1b, 0x26];
const PASTE: &str = "Or paste a theme file's JSON";

#[derive(Default)]
struct Checks {
    failed: usize,
}

impl Checks {
    fn check(&mut self, ok: bool, what: impl Into<String>) -> bool {
        let what = what.into();
        log("themes_check", json!({"ok": ok, "what": what}));
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

/// The root's two variables as given, and each as the colour a canvas paints for it.
const PAINT: &str = r#"(() => {
  const style = getComputedStyle(document.documentElement);
  const ctx = document.createElement('canvas').getContext('2d');
  const paint = (name) => {
    const value = style.getPropertyValue(name).trim();
    ctx.clearRect(0, 0, 1, 1);
    ctx.fillStyle = '#000000';
    ctx.fillStyle = value;
    ctx.fillRect(0, 0, 1, 1);
    return { value, rgb: [...ctx.getImageData(0, 0, 1, 1).data.slice(0, 3)] };
  };
  return { bg: paint('--px-color-bg'), canvas: paint('--app-theme-canvas'), dark: document.documentElement.classList.contains('dark') };
})()"#;

/// Whether a painted colour is the file's canvas, to the rounding of a colour space conversion.
fn is_canvas(v: &Value) -> bool {
    let Some(rgb) = v["rgb"].as_array() else {
        return false;
    };
    rgb.len() == 3
        && rgb
            .iter()
            .zip(CANVAS)
            .all(|(got, want)| got.as_i64().is_some_and(|g| (g - want).abs() <= 1))
}

/// Waits for the window to paint in the theme, then checks it, saying what it saw.
fn painted(c: &mut Checks, app: &AppHandle, when: &str) {
    let seen = within(30, || {
        let v = main_eval(app, PAINT)?;
        (is_canvas(&v["bg"]) && is_canvas(&v["canvas"]) && v["dark"] == true).then_some(v)
    });
    let v = seen
        .clone()
        .or_else(|| main_eval(app, PAINT))
        .unwrap_or(Value::Null);
    c.check(
        is_canvas(&v["bg"]),
        format!(
            "{when}: --px-color-bg is the file's canvas #1a1b26 ({})",
            v["bg"]
        ),
    );
    c.check(
        is_canvas(&v["canvas"]),
        format!(
            "{when}: --app-theme-canvas is the file's canvas #1a1b26 ({})",
            v["canvas"]
        ),
    );
    c.check(
        v["dark"] == true,
        format!("{when}: the root has class dark"),
    );
}

/// Opens the Themes page and waits for its picker.
fn themes_page(c: &mut Checks, app: &AppHandle) -> bool {
    let _ = main_eval(app, "(location.hash = '#/themes', true)");
    c.check(
        until(
            app,
            30,
            "document.querySelector('[role=radiogroup] [data-part=card]') !== null",
        ),
        "the Themes page opens with its picker",
    )
}

/// Whether the picker lists Nightfall, chosen.
fn chosen(c: &mut Checks, app: &AppHandle, when: &str) {
    let expr = format!(
        "[...document.querySelectorAll('[role=radiogroup] [data-part=card]')].some((e) => e.querySelector('.theme-name')?.textContent === {} && e.querySelector('input[type=radio]')?.checked === true)",
        json!(NAME)
    );
    c.check(
        until(app, 15, &expr),
        format!("{when}: the picker lists {NAME}, chosen"),
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

/// Pastes the theme file into the Themes page's box and presses Add.
fn paste(c: &mut Checks, app: &AppHandle) -> bool {
    let fill = format!(
        r#"(() => {{
  const label = [...document.querySelectorAll('label')].find((l) => l.textContent.trim() === {paste});
  const box = label && document.getElementById(label.htmlFor);
  if (!box) return false;
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(box, {theme});
  box.dispatchEvent(new Event('input', {{ bubbles: true }}));
  return true;
}})()"#,
        paste = json!(PASTE),
        theme = json!(THEME),
    );
    if !c.check(
        main_eval(app, &fill) == Some(json!(true)),
        format!("the Themes page has \"{PASTE}\""),
    ) {
        return false;
    }
    let press = format!(
        r#"(() => {{
  const label = [...document.querySelectorAll('label')].find((l) => l.textContent.trim() === {paste});
  const add = label?.closest('form')?.querySelector('button[type=submit]');
  if (!add || add.disabled || add.textContent.trim() !== 'Add') return false;
  add.click();
  return true;
}})()"#,
        paste = json!(PASTE),
    );
    c.check(
        until(app, 10, &press),
        "Add is enabled with the file pasted, and pressed",
    )
}

/// `GET /themes` through the app's host, as the window's pages call core.
fn stored(app: &AppHandle) -> Option<Value> {
    let _ = main_eval(
        app,
        "(window.__pluxyThemes = null, window.__TAURI_INTERNALS__.invoke('core_call', { method: 'GET', path: '/themes', body: null }).then((r) => { window.__pluxyThemes = r; }, (e) => { window.__pluxyThemes = { error: String(e) }; }), true)",
    );
    within(10, || {
        main_eval(app, "window.__pluxyThemes").filter(|v| !v.is_null())
    })
}

fn add(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) || !themes_page(c, app) || !paste(c, app) {
        return;
    }
    let added = until(
        app,
        15,
        &format!(
            "document.body.innerText.includes({})",
            json!(format!("{NAME} was added and turned on."))
        ),
    );
    // What the page said instead, if anything.
    let notices = main_eval(
        app,
        "[...document.querySelectorAll('[role=alert], [role=status]')].map((e) => e.textContent).join(' | ')",
    )
    .unwrap_or(Value::Null);
    c.check(
        added,
        format!("the page says {NAME} was added and turned on (notices: {notices})"),
    );
    painted(c, app, "added");
    chosen(c, app, "added");
    let r = stored(app).unwrap_or(Value::Null);
    let has = r["body"]["themes"]
        .as_array()
        .is_some_and(|t| t.iter().any(|t| t["id"] == ID && t["name"] == NAME));
    c.check(
        r["status"] == 200 && r["body"]["current"] == ID && has,
        format!("GET /themes through the app's host has {ID} stored and current ({r})"),
    );
    export(c, app);
}

/// Presses Export and checks the file the page says it saved.
fn export(c: &mut Checks, app: &AppHandle) {
    let press = format!(
        "(() => {{ const b = document.querySelector({}); if (!b) return false; b.click(); return true; }})()",
        json!(format!(
            "button[aria-label=\"Export {NAME} as a theme file\"]"
        ))
    );
    if !c.check(
        main_eval(app, &press) == Some(json!(true)),
        format!("the Themes page has Export for {NAME}, pressed"),
    ) {
        return;
    }
    const SAVED: &str = "Saved to ";
    let said = within(15, || {
        main_eval(
            app,
            "[...document.querySelectorAll('[role=status]')].map((e) => e.textContent).find((t) => t.startsWith('Saved to ')) ?? null",
        )
        .and_then(|v| v.as_str().map(str::to_string))
    });
    let notices = main_eval(
        app,
        "[...document.querySelectorAll('[role=alert], [role=status]')].map((e) => e.textContent).join(' | ')",
    )
    .unwrap_or(Value::Null);
    if !c.check(
        said.is_some(),
        format!("the page says where {NAME} was saved (notices: {notices})"),
    ) {
        return;
    }
    let shown = said.unwrap_or_default()[SAVED.len()..].to_string();
    // A session with no Downloads folder set up gets the one at home.
    let downloads = app
        .path()
        .download_dir()
        .or_else(|_| app.path().home_dir().map(|home| home.join("Downloads")))
        .ok();
    let expected = downloads.as_ref().map(|d| d.join("nightfall.json"));
    // In privacy mode the page names the file and its folder only ("…/Downloads/nightfall.json"):
    // the folders above hold the user name. Otherwise it shows the whole path.
    let path = match shown.strip_prefix('…') {
        Some(short) => {
            let folder = downloads
                .as_ref()
                .and_then(|d| d.file_name())
                .and_then(|n| n.to_str())
                .unwrap_or_default();
            c.check(
                short.trim_start_matches(['/', '\\']).replace('\\', "/")
                    == format!("{folder}/nightfall.json"),
                format!(
                    "in privacy mode the page names only the folder and the file ({shown}, Downloads {downloads:?})"
                ),
            );
            expected.clone().unwrap_or_default()
        }
        None => std::path::PathBuf::from(&shown),
    };
    c.check(
        Some(&path) == expected.as_ref(),
        format!(
            "the saved file is nightfall.json in the Downloads folder ({}, Downloads {downloads:?})",
            path.display()
        ),
    );
    let held = std::fs::read(&path)
        .ok()
        .and_then(|bytes| serde_json::from_slice::<Value>(&bytes).ok());
    c.check(
        held.is_some() && held == serde_json::from_str::<Value>(THEME).ok(),
        format!("the file at {} holds the theme file's JSON", path.display()),
    );
}

fn restart(c: &mut Checks, app: &AppHandle) {
    if !shell(c, app) {
        return;
    }
    painted(c, app, "after a restart");
    if !themes_page(c, app) {
        return;
    }
    chosen(c, app, "after a restart");
    painted(c, app, "after a restart, on the Themes page");
    // The session's screenshot is taken every few seconds: let one show the page in the theme.
    std::thread::sleep(Duration::from_secs(10));
}

pub fn run(app: &AppHandle, stage: &str) -> Result<(), String> {
    let mut c = Checks::default();
    match stage {
        "add" => add(&mut c, app),
        "restart" => restart(&mut c, app),
        _ => return Err(format!("no themes stage {stage}")),
    }
    // Also after a stage stopped early, so privacy.ts says which checks failed.
    log("themes_report", json!({"stage": stage, "failed": c.failed}));
    crate::quit(app);
    Ok(())
}
