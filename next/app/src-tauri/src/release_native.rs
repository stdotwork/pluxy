//! The native test's release feed check, in native-test builds only. It drives Version Management
//! as the person would (real DOM clicks, real GTK dialogs through X input) against the runner's
//! fake https feed (`native/run.ts`), and reads what the window, the notes webview, the files the
//! app keeps and the runner's stand-in browser show. Expected versions come from the feed fixture
//! (`native/release-feed.json`), not from the app's parser.
use crate::confirmation::native::{key, window};
use crate::native_test::{eval, log, wait_for};
use gtk::prelude::*;
use serde_json::{json, Value};
use std::fs;
use std::path::PathBuf;
use std::sync::mpsc;
use std::time::Duration;
use tauri::{AppHandle, Manager, Webview};

const DIALOG: &str = "pluxy native confirmation";

fn env(name: &str) -> Result<String, String> {
    std::env::var(name).map_err(|_| format!("{name} is not set"))
}

/// Clicks the element selector names: a button, input or link itself, otherwise the button
/// inside it (the kit's buttons sit in a marked wrapper), once it is there and enabled. Fails
/// when there is still nothing to click.
pub(crate) fn click(main: &Webview, selector: &str) -> Result<(), String> {
    let js = format!(
        "(e => {{ const t = e?.matches('button, input, a') ? e : e?.querySelector('button'); \
         if (!t || t.disabled) return false; t.click(); return true; }})(document.querySelector({}))",
        json!(selector)
    );
    wait_for(&format!("something to click at {selector}"), || {
        eval(main, &js).filter(|v| v == true)
    })
    .map(|_| ())
}

/// A data attribute of the first element matching selector, once it equals want.
pub(crate) fn wait_attr(
    main: &Webview,
    what: &str,
    selector: &str,
    attr: &str,
    want: &str,
) -> Result<(), String> {
    let expr = format!(
        "document.querySelector({})?.dataset[{}] ?? null",
        json!(selector),
        json!(attr)
    );
    wait_for(what, || eval(main, &expr).filter(|v| v == want).map(|_| ()))
        .map_err(|e| format!("{e}: {selector} {attr} is {:?}", eval(main, &expr)))
}

pub(crate) fn attr(main: &Webview, selector: &str, attr: &str) -> Value {
    eval(
        main,
        &format!(
            "document.querySelector({})?.dataset[{}] ?? null",
            json!(selector),
            json!(attr)
        ),
    )
    .unwrap_or(Value::Null)
}

/// The text the native confirmation shows, read from its GTK labels.
pub(crate) fn dialog_text(app: &AppHandle) -> Result<String, String> {
    let (tx, rx) = mpsc::channel();
    app.run_on_main_thread(move || {
        fn labels(w: &gtk::Widget, out: &mut String) {
            if let Some(l) = w.downcast_ref::<gtk::Label>() {
                out.push_str(&l.text());
                out.push('\n');
            }
            if let Some(c) = w.downcast_ref::<gtk::Container>() {
                for child in c.children() {
                    labels(&child, out);
                }
            }
        }
        let mut out = String::new();
        for w in gtk::Window::list_toplevels() {
            if let Ok(d) = w.downcast::<gtk::Dialog>() {
                if d.title().as_deref() == Some(DIALOG) && d.is_visible() {
                    labels(d.upcast_ref(), &mut out);
                }
            }
        }
        let _ = tx.send(out);
    })
    .map_err(|e| e.to_string())?;
    rx.recv_timeout(Duration::from_secs(5))
        .map_err(|e| e.to_string())
}

/// Asks for a feed change in the window and waits for its native dialog.
fn ask_source(app: &AppHandle, main: &Webview, url: &str) -> Result<String, String> {
    main.eval(format!(
        "{{ const i = document.querySelector('[data-part=release-source-input]'); i.value = {}; \
         i.dispatchEvent(new Event('input', {{bubbles: true}})); }}",
        json!(url)
    ))
    .map_err(|e| e.to_string())?;
    std::thread::sleep(Duration::from_millis(200));
    click(main, "[data-part=release-source-change]")?;
    wait_for("the feed change's native confirmation", || window(DIALOG))?;
    let text = dialog_text(app)?;
    if !text.contains(url) {
        return Err(format!(
            "the native confirmation does not show {url}: {text}"
        ));
    }
    // Until it is settled, the window shows the change waiting and the feed unchanged.
    wait_attr(
        main,
        "the change shown waiting",
        "[data-part=release-source]",
        "changing",
        "true",
    )?;
    Ok(text)
}

/// Sets the feed to url through its native confirmation, confirmed.
pub(crate) fn set_source(app: &AppHandle, main: &Webview, url: &str) -> Result<(), String> {
    wait_for("the Version Management page", || {
        eval(
            main,
            "document.querySelector('[data-part=release-source-input]') !== null",
        )
        .filter(|v| v == true)
    })?;
    ask_source(app, main, url)?;
    key(DIALOG, "alt+o")?;
    wait_attr(
        main,
        "the confirmed feed",
        "[data-part=release-source]",
        "source",
        url,
    )
}

fn browser_log(path: &str) -> Vec<String> {
    fs::read_to_string(path)
        .unwrap_or_default()
        .lines()
        .map(String::from)
        .collect()
}

/// A notes webview's document, as its own DOM has it.
const NOTES_DOM: &str = "({href: location.href, scripts: document.scripts.length, \
    images: document.images.length, embedded: document.querySelectorAll('iframe, object, embed, frame, form, input').length, \
    handlers: [...document.querySelectorAll('*')].filter(e => [...e.attributes].some(a => a.name.startsWith('on'))).length, \
    links: [...document.links].map(a => a.href), heading: document.querySelector('main h2')?.textContent ?? null, \
    text: document.body.innerText, ran: window.__hostileRan ?? null, \
    internals: typeof window.__TAURI_INTERNALS__?.invoke === 'function'})";

fn area(main: &Webview, product: &str) -> Value {
    eval(
        main,
        &format!(
            "(() => {{ const r = document.querySelector('[data-part=release-notes][data-product={product}]')?.getBoundingClientRect(); \
             return r ? {{x: r.left, y: r.top, width: r.width, height: r.height}} : null; }})()"
        ),
    )
    .unwrap_or(Value::Null)
}

fn placed(app: &AppHandle, label: &str) -> Result<Value, String> {
    let (tx, rx) = mpsc::channel();
    let l = label.to_string();
    app.run_on_main_thread(move || {
        let _ = tx.send(crate::place::allocation(&l));
    })
    .map_err(|e| e.to_string())?;
    Ok(json!(rx
        .recv_timeout(Duration::from_secs(5))
        .ok()
        .flatten()))
}

fn near(a: &Value, b: &Value) -> bool {
    ["x", "y", "width", "height"].iter().all(
        |k| matches!((a[k].as_f64(), b[k].as_f64()), (Some(x), Some(y)) if (x - y).abs() <= 2.0),
    )
}

pub(crate) fn run(
    app: &AppHandle,
    main: &Webview,
    probe: impl Fn(&Webview) -> Result<Value, String>,
) -> Result<(), String> {
    let feed = env("PLUXY_APP_TEST_FEED")?;
    let other = env("PLUXY_APP_TEST_FEED_OTHER")?;
    let base = feed.trim_end_matches("/feed.json").to_string();
    let browser = env("PLUXY_APP_TEST_BROWSER_LOG")?;
    let offline = env("PLUXY_APP_TEST_FEED_OFFLINE")?;
    let settings: PathBuf = app
        .path()
        .app_config_dir()
        .map_err(|e| e.to_string())?
        .join("release.json");
    let mut evidence = json!({});

    main.eval("location.hash = '#/versions'")
        .map_err(|e| e.to_string())?;
    wait_for("the Version Management page", || {
        eval(
            main,
            "document.querySelector('[data-part=release-check]') !== null",
        )
        .filter(|v| v == true)
    })?;
    // No built-in feed: nothing set, nothing kept.
    wait_attr(
        main,
        "no feed at first",
        "[data-part=release-source]",
        "source",
        "",
    )?;
    evidence["no_default"] = json!(!settings.exists());

    // A feed change opens the native confirmation; confirming applies it.
    ask_source(app, main, &feed)?;
    key(DIALOG, "alt+o")?;
    wait_attr(
        main,
        "the confirmed feed",
        "[data-part=release-source]",
        "source",
        &feed,
    )?;
    let kept: Value = serde_json::from_slice(&fs::read(&settings).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())?;
    evidence["confirmed"] = json!(kept["source"] == feed);

    // Check for Updates: the stable channel's releases.
    click(main, "[data-part=release-check]")?;
    let app_card = "[data-part=release-product][data-product=app]";
    let core_card = "[data-part=release-product][data-product=core]";
    wait_attr(
        main,
        "the app's stable release",
        app_card,
        "latest",
        "0.2.0",
    )?;
    wait_attr(main, "core's stable release", core_card, "latest", "1.4.0")?;
    evidence["stable"] = json!({
        "app": attr(main, app_card, "latest"), "core": attr(main, core_card, "latest"),
        "installed_app": attr(main, app_card, "installed"),
    });
    let checked = attr(main, "[data-part=release-checked]", "at");
    if checked.as_str().is_none_or(str::is_empty) {
        return Err("no check time shown".into());
    }

    // Cancelling another change keeps the old feed, its file and its last good copy.
    let before = fs::read(&settings).map_err(|e| e.to_string())?;
    ask_source(app, main, &other)?;
    key(DIALOG, "alt+c")?;
    wait_attr(
        main,
        "the cancelled change to settle",
        "[data-part=release-source]",
        "changing",
        "false",
    )?;
    evidence["cancel_kept"] = json!(
        attr(main, "[data-part=release-source]", "source") == feed
            && fs::read(&settings).map_err(|e| e.to_string())? == before
            && attr(main, app_card, "latest") == "0.2.0"
    );

    // While a change waits, Channel is disabled. A confirmed change that the settings outran
    // (here the channel, set by a direct call) is not applied, and the page says so.
    ask_source(app, main, &other)?;
    let channel_disabled = eval(
        main,
        "[...document.querySelectorAll('[data-part=release-channel] input')].every(i => i.matches(':disabled'))",
    );
    main.eval("window.__channelSet = null; window.__TAURI_INTERNALS__.invoke('release_channel_set', {channel: 'stable'}).then(() => window.__channelSet = true, e => window.__channelSet = String(e))")
        .map_err(|e| e.to_string())?;
    let channel_set = wait_for("the direct channel call", || {
        eval(main, "window.__channelSet").filter(|v| !v.is_null())
    })?;
    key(DIALOG, "alt+o")?;
    let voided = wait_for("the voided change shown as an error", || {
        eval(
            main,
            "document.querySelector('[data-part=release-error]')?.textContent ?? null",
        )
        .filter(|t| t.as_str().is_some_and(|t| t.contains("was not changed")))
    })?;
    evidence["voided"] = json!({
        "channel_disabled": channel_disabled, "channel_set": channel_set, "error": voided,
        "kept": attr(main, "[data-part=release-source]", "source") == feed
            && attr(main, "[data-part=release-source]", "changing") == "false",
    });

    // Preview (latest): the newest release on either channel, from the same copy.
    click(main, "[data-part=release-channel] input[value=preview]")?;
    wait_attr(
        main,
        "the app's preview release",
        app_card,
        "latest",
        "0.3.0-preview.1",
    )?;
    wait_attr(
        main,
        "core's preview release",
        core_card,
        "latest",
        "1.5.0-rc.1",
    )?;
    evidence["preview"] =
        json!({"app": attr(main, app_card, "latest"), "core": attr(main, core_card, "latest")});

    // The notes webview: hostile notes without their HTML, no grants, no way out but a rendered link.
    let notes_sel = "[data-part=release-notes][data-product=app]";
    wait_for("the app's notes webview", || {
        attr(main, notes_sel, "label")
            .as_str()
            .filter(|l| l.starts_with("notes-"))
            .and_then(|l| app.get_webview(l))
    })?;
    let label = attr(main, notes_sel, "label")
        .as_str()
        .unwrap_or_default()
        .to_string();
    let notes = app.get_webview(&label).ok_or("no notes webview")?;
    let dom = wait_for("the notes document", || {
        eval(&notes, NOTES_DOM).filter(|d| d["heading"].is_string())
    })?;
    let good = format!("{base}/notes/0.3.0-preview.1");
    let full = format!("{base}/releases/0.3.0-preview.1");
    evidence["notes"] = json!({
        "label": label, "href": dom["href"], "scripts": dom["scripts"], "images": dom["images"],
        "embedded": dom["embedded"], "handlers": dom["handlers"], "links": dom["links"],
        "heading": dom["heading"], "ran": dom["ran"], "internals": dom["internals"],
        "html_left_out": dom["text"].as_str().is_some_and(|t| t.contains("Bold")
            && !t.contains("__hostileRan") && !t.contains("<script") && !t.contains("<iframe")),
    });
    // The same command from the window (control) and from the notes webview (refused).
    main.eval("window.__releaseControl = null; window.__TAURI_INTERNALS__.invoke('release_get').then(v => window.__releaseControl = {ok: true, source: v.source}, e => window.__releaseControl = {ok: false, error: String(e)})")
        .map_err(|e| e.to_string())?;
    evidence["control"] = wait_for("the window's control call", || {
        eval(main, "window.__releaseControl").filter(|v| !v.is_null())
    })?;
    evidence["notes_probe"] = probe(&notes)?;

    // Navigation: an address the notes do not link to opens nothing; the rendered link opens
    // in the browser, and the notes webview stays on its document.
    notes
        .eval(format!(
            "location.href = {}",
            json!(format!("{base}/not-linked"))
        ))
        .map_err(|e| e.to_string())?;
    std::thread::sleep(Duration::from_millis(1500));
    let unlinked = browser_log(&browser);
    notes
        .eval("document.querySelector('main a').click()")
        .map_err(|e| e.to_string())?;
    let opened = wait_for("the browser to get the notes link", || {
        let l = browser_log(&browser);
        (!l.is_empty()).then_some(l)
    })?;
    std::thread::sleep(Duration::from_millis(1100));
    click(main, "[data-part=release-notes-full][data-product=app]")?;
    let full_opened = wait_for("the browser to get the full notes", || {
        let l = browser_log(&browser);
        (l.len() >= 2).then_some(l)
    })?;
    // A link with characters the renderer escapes (`[`, `]`, `|`): WebKit navigates to the
    // address as the document writes it, and that has to open in the browser too.
    std::thread::sleep(Duration::from_millis(1100));
    notes
        .eval("[...document.querySelectorAll('main a')].find(a => a.href.includes('/escaped/')).click()")
        .map_err(|e| e.to_string())?;
    let escaped_opened = wait_for(
        "the browser to get the link with escaped characters",
        || {
            let l = browser_log(&browser);
            (l.len() >= 3).then_some(l)
        },
    )?;
    evidence["navigation"] = json!({
        "unlinked_opened": unlinked, "opened": opened, "full": full_opened,
        "escaped": escaped_opened,
        "expected": [good, full],
        "stayed": eval(&notes, "location.href").unwrap_or(Value::Null),
    });

    // Nothing the notes link to is saved: WebKit's download of a link the notes webview can
    // load (its own style sheet) is refused, and right-clicking shows no menu to start one.
    let st = app.state::<crate::App>();
    let css = format!("{}notes.css", dom["href"].as_str().unwrap_or_default());
    notes
        .with_webview(move |pw| {
            use webkit2gtk::WebViewExt;
            let _ = pw.inner().download_uri(&css);
        })
        .map_err(|e| e.to_string())?;
    let downloads = wait_for("the notes download to be refused", || {
        let n = st.notes.lock().unwrap().downloads;
        (n > 0).then_some(n)
    })?;
    main.eval(format!(
        "document.querySelector({}).scrollIntoView({{block: 'center'}})",
        json!(notes_sel)
    ))
    .map_err(|e| e.to_string())?;
    let at = wait_for("the notes webview over its area", || {
        let p = placed(app, &label).ok()?;
        near(&area(main, "app"), &p).then_some(p)
    })?;
    let title = main.window().title().map_err(|e| e.to_string())?;
    let id = wait_for("the app's window", || window(&title))?;
    let (x, y) = (
        at["x"].as_f64().unwrap_or(0.0) + 20.0,
        at["y"].as_f64().unwrap_or(0.0) + 20.0,
    );
    let xdo = |args: &[&str]| {
        std::process::Command::new("xdotool")
            .args(args)
            .status()
            .map_err(|e| e.to_string())
            .and_then(|s| {
                s.success()
                    .then_some(())
                    .ok_or(format!("xdotool {args:?}: {s}"))
            })
    };
    xdo(&[
        "mousemove",
        "--window",
        &id,
        &format!("{x:.0}"),
        &format!("{y:.0}"),
    ])?;
    xdo(&["click", "3"])?;
    let menus = wait_for("the right click on the notes", || {
        let n = st.notes.lock().unwrap().menus;
        (n > 0).then_some(n)
    })?;
    std::thread::sleep(Duration::from_millis(500));
    let (tx, rx) = mpsc::channel();
    app.run_on_main_thread(move || {
        let shown = gtk::Window::list_toplevels().into_iter().any(|w| {
            w.is_visible()
                && w.downcast_ref::<gtk::Container>()
                    .is_some_and(|c| c.children().iter().any(|m| m.is::<gtk::Menu>()))
        });
        let _ = tx.send(shown);
    })
    .map_err(|e| e.to_string())?;
    let menu_shown = rx
        .recv_timeout(Duration::from_secs(5))
        .map_err(|e| e.to_string())?;
    let home = PathBuf::from(env("HOME")?);
    let saved = [
        gtk::glib::user_special_dir(gtk::glib::UserDirectory::Downloads),
        Some(home.join("Downloads")),
        Some(home),
    ]
    .into_iter()
    .flatten()
    .any(|d| d.join("notes.css").exists());
    evidence["downloads"] = json!({
        "refused": downloads, "saved": saved, "right_clicks": menus, "menu_shown": menu_shown,
    });

    // Expanded at first, the notes webview over the notes area; Collapse hides the notes and
    // closes their webview, Expand shows them again in a new one, over the area again.
    let toggle = "[data-part=release-notes-toggle][data-product=app]";
    let first = area(main, "app");
    let first_follows = near(&first, &placed(app, &label)?);
    click(main, toggle)?;
    wait_for("Collapse to close the notes webview", || {
        (app.get_webview(&label).is_none() && attr(main, notes_sel, "label").is_null())
            .then_some(())
    })?;
    click(main, toggle)?;
    let again = wait_for("Expand to open a notes webview", || {
        attr(main, notes_sel, "label")
            .as_str()
            .filter(|l| l.starts_with("notes-") && *l != label)
            .and_then(|l| app.get_webview(l).map(|_| l.to_string()))
    })?;
    std::thread::sleep(Duration::from_millis(500));
    evidence["expand"] = json!({
        "first_follows": first_follows,
        "collapse_closed": true,
        "expand_follows": near(&area(main, "app"), &placed(app, &again)?),
        "area": first,
    });

    // Scrolled: the notes webview covers only the part of its area that shows, below the
    // navigation bar pinned at the top, and hides once none of it does. The page gets room to
    // scroll, and the top navigation; unscrolled, the webview sits over its area (control).
    main.eval("window.__navWas = document.documentElement.dataset.nav; document.documentElement.dataset.nav = 'top'; document.querySelector('[data-part=main]').style.paddingBottom = '3000px'; window.scrollTo(0, 0)")
        .map_err(|e| e.to_string())?;
    let geometry = "(() => { const r = document.querySelector('[data-part=release-notes][data-product=app]').getBoundingClientRect(); \
        const n = document.querySelector('[data-part=nav]').getBoundingClientRect(); const v = document.documentElement; \
        return {left: r.left, top: r.top, right: r.right, bottom: r.bottom, nav: n.bottom, nav_top: n.top, \
        position: getComputedStyle(document.querySelector('[data-part=nav]')).position, \
        view_width: v.clientWidth, view_height: v.clientHeight, scroll: window.scrollY}; })()";
    // What should show of the area, from the page's own numbers.
    let shows = |g: &Value| -> Value {
        let f = |k: &str| g[k].as_f64().unwrap_or(f64::NAN);
        let (x, y) = (f("left").max(0.0), f("top").max(f("nav")));
        let (w, h) = (
            f("right").min(f("view_width")) - x,
            f("bottom").min(f("view_height")) - y,
        );
        if w >= 1.0 && h >= 1.0 {
            json!({"x": x, "y": y, "width": w, "height": h})
        } else {
            Value::Null
        }
    };
    // Scrolls the page to y, then waits until the webview is where the area shows (or hidden).
    let scrolled = |y: f64| -> Result<Value, String> {
        main.eval(format!("window.scrollTo(0, {y})"))
            .map_err(|e| e.to_string())?;
        std::thread::sleep(Duration::from_millis(300));
        let mut last = json!(null);
        wait_for(&format!("the notes webview placed at scroll {y}"), || {
            let g = eval(main, geometry)?;
            let want = shows(&g);
            let got = placed(app, &again).ok()?;
            let ok = if want.is_null() {
                got.is_null()
            } else {
                near(&want, &got)
            };
            last = json!({"geometry": g, "want": want, "placed": got});
            ok.then(|| last.clone())
        })
        .map_err(|e| format!("{e}: {last}"))
    };
    let unscrolled = scrolled(0.0)?;
    let top = unscrolled["geometry"]["top"].as_f64().unwrap_or(0.0);
    let nav = unscrolled["geometry"]["nav"].as_f64().unwrap_or(0.0);
    // Partly above the navigation bar, then wholly out of sight, then back.
    let partly = scrolled((top - nav + 100.0).max(0.0))?;
    let hidden = scrolled(top + 1000.0)?;
    let back = scrolled(0.0)?;
    main.eval("document.documentElement.dataset.nav = window.__navWas; document.querySelector('[data-part=main]').style.paddingBottom = ''; window.scrollTo(0, 0)")
        .map_err(|e| e.to_string())?;
    evidence["scrolled"] = json!({
        "unscrolled": unscrolled, "partly": partly, "hidden": hidden, "back": back,
    });

    // Offline: the runner stops the feed; the last good copy stays, with its time and an error.
    fs::write(&offline, b"").map_err(|e| e.to_string())?;
    wait_for("the feed to go offline", || {
        std::path::Path::new(&format!("{offline}-done"))
            .exists()
            .then_some(())
    })?;
    // The error notice appears above the notes area: no scroll, no resize of the window or of
    // the area, yet the area moves down, and the notes webview has to follow it.
    let shown = "(() => { const r = document.querySelector('[data-part=release-notes][data-product=app]').getBoundingClientRect(); \
        const v = document.documentElement; const x = Math.max(r.left, 0), y = Math.max(r.top, 0); \
        const w = Math.min(r.right, v.clientWidth) - x, h = Math.min(r.bottom, v.clientHeight) - y; \
        return w >= 1 && h >= 1 ? {x, y, width: w, height: h} : null; })()";
    let area_before = area(main, "app");
    click(main, "[data-part=release-check]")?;
    wait_attr(
        main,
        "the offline error",
        "[data-part=release-problem]",
        "offline",
        "true",
    )?;
    let mut last = json!(null);
    let under_notice = wait_for("the notes webview over its area under the notice", || {
        let want = eval(main, shown)?;
        let got = placed(app, &again).ok()?;
        last = json!({"want": want, "placed": got});
        near(&want, &got).then(|| last.clone())
    })
    .map_err(|e| format!("{e}: {last}"))?;
    let area_after = area(main, "app");
    evidence["offline"] = json!({
        "kept": attr(main, app_card, "latest") == "0.3.0-preview.1",
        "same_time": attr(main, "[data-part=release-checked]", "at") == checked,
        "message": eval(main, "document.querySelector('[data-part=release-problem]')?.textContent ?? null"),
        "area_moved": (area_after["y"].as_f64().unwrap_or(0.0) - area_before["y"].as_f64().unwrap_or(0.0)).abs() > 2.0,
        "area_before": area_before, "area_after": area_after, "webview": under_notice,
    });

    // Leaving the page closes its notes webviews.
    main.eval("location.hash = '#/status'")
        .map_err(|e| e.to_string())?;
    wait_for("the notes webviews to close", || {
        app.webviews()
            .keys()
            .all(|l| !l.starts_with("notes-"))
            .then_some(())
    })?;
    evidence["closed"] = json!(true);
    log("release", evidence);
    Ok(())
}
