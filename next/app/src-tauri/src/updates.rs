//! The window's commands for the release feed (`release.rs`) and the release notes webviews
//! (`notes.rs`). Each refuses any caller but the window, as every app command does. Changing the
//! feed's address opens a native confirmation; only its confirmed, consumed token applies it.

use crate::notes::{Doc, Style};
use crate::release::{self, Channel, Product, View};
use crate::{trusted, App, Rect, MAIN};
use std::time::{Duration, Instant};
use tauri::webview::NewWindowResponse;
use tauri::{
    AppHandle, Emitter, EventTarget, LogicalPosition, LogicalSize, Manager, Url, Webview,
    WebviewBuilder, WebviewUrl, Window,
};

/// The certificates a feed's https is checked against: the public roots. A native-test build
/// takes the test feed's own authority from its runner instead; no release build has that code.
fn roots() -> release::Roots {
    #[cfg(feature = "native-test")]
    if let Ok(p) = std::env::var("PLUXY_APP_TEST_FEED_CA") {
        return std::fs::read(p)
            .ok()
            .and_then(|b| crate::client::certificates(&b).ok())
            .map(std::sync::Arc::new);
    }
    None
}

fn app_version(app: &AppHandle) -> String {
    app.package_info().version.to_string()
}

/// The view, with the version of the core the app is connected to, if it says one.
fn view(app: &AppHandle) -> View {
    let caller = app.state::<App>().conn.lock().unwrap().caller();
    let status = caller.call("GET", "/status", None);
    let core = release::core_version(status.body.as_ref().filter(|_| status.status == 200));
    app.state::<App>()
        .releases
        .lock()
        .unwrap()
        .view(&app_version(app), core)
}

async fn blocking<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> Result<T, String> {
    tauri::async_runtime::spawn_blocking(f)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn release_get(webview: Webview) -> Result<View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || view(&app)).await
}

/// "Check for Updates": reads the feed now. A failure keeps the last good copy, shown with it.
#[tauri::command]
pub async fn release_check(webview: Webview) -> Result<View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || {
        let check = app.state::<App>().releases.lock().unwrap().begin_check();
        let Some(check) = check else {
            return Err("No release feed is set.".to_string());
        };
        // Read without the lock; a check started later, or a feed change, wins over this one.
        let read = release::fetch(&check.source, &roots());
        app.state::<App>()
            .releases
            .lock()
            .unwrap()
            .checked(&check, read);
        Ok(view(&app))
    })
    .await?
}

#[tauri::command]
pub async fn release_channel_set(webview: Webview, channel: Channel) -> Result<View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    blocking(move || {
        app.state::<App>()
            .releases
            .lock()
            .unwrap()
            .set_channel(channel)?;
        Ok(view(&app))
    })
    .await?
}

/// Asks to change the feed's address (none: no feed). Returns once the native confirmation is
/// showing; the window hears `pluxy-release` when it is settled, with whether the feed changed
/// and, when a confirmed change could not be applied, why. Cancelling keeps the old feed.
#[tauri::command]
pub fn release_source_set(webview: Webview, source: Option<String>) -> Result<(), String> {
    trusted(&webview)?;
    #[cfg(not(target_os = "linux"))]
    {
        let _ = source;
        Err("changing the release feed needs a native confirmation, on Linux only for now".into())
    }
    #[cfg(target_os = "linux")]
    {
        use crate::confirmation::{self, Action, Package};
        let app = webview.app_handle().clone();
        let st = app.state::<App>();
        let (new, old, revision) = {
            let r = st.releases.lock().unwrap();
            let new = r.check_source(source.as_deref())?;
            (new, r.settings().source.clone(), r.revision())
        };
        let none = || "no feed".to_string();
        let app_package = Package {
            id: "pluxy app".into(),
            source: "the app's window".into(),
            digest: String::new(),
            signer: String::new(),
            contract: "release feed".into(),
            generation: revision,
        };
        let action = Action {
            what: format!(
                "Change the release feed from {} to {}",
                old.unwrap_or_else(none),
                new.clone().unwrap_or_else(none)
            ),
            chain: vec![app_package.clone()],
            executor: app_package,
            native_tool: false,
            files: vec![],
            launch: None,
        };
        let id = {
            let mut c = st.confirmations.lock().unwrap();
            let p = c.prepare_main(action)?;
            c.queue(p)?
        };
        st.releases.lock().unwrap().change = Some(release::SourceChange {
            action: id.clone(),
            source: new,
            revision,
        });
        if let Err(e) = confirmation::request(&app, id.clone()) {
            st.releases.lock().unwrap().change = None;
            return Err(e);
        }
        std::thread::spawn(move || settle_source(&app, &id));
        Ok(())
    }
}

/// Waits for the confirmation's native result; only a confirmed, consumed token applies the change.
#[cfg(target_os = "linux")]
fn settle_source(app: &AppHandle, id: &str) {
    let st = app.state::<App>();
    // The dialog cancels itself after its lifetime; window close invalidates it sooner.
    let deadline = Instant::now() + Duration::from_secs(75);
    let result = loop {
        if let Some(r) = st.confirmations.lock().unwrap().take_result(id) {
            break Some(r);
        }
        if Instant::now() > deadline {
            break None;
        }
        std::thread::sleep(Duration::from_millis(100));
    };
    let authorized = match result {
        Some(Ok(Some(token))) => st.confirmations.lock().unwrap().consume(id, &token).is_ok(),
        _ => false,
    };
    let mut r = st.releases.lock().unwrap();
    let change = r.change.take_if(|c| c.action == id);
    // Confirmed but not applied (the settings changed meanwhile, or could not be saved) is an
    // error the window shows; cancelled, or not confirmed in time, keeps the feed quietly.
    let outcome = match change {
        Some(c) if authorized => match r.apply_source(c.source, c.revision) {
            Ok(()) => serde_json::json!({"changed": true, "error": null}),
            Err(e) => serde_json::json!({"changed": false, "error": e}),
        },
        _ => serde_json::json!({"changed": false, "error": null}),
    };
    drop(r);
    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-release", outcome);
}

fn notes_url() -> Url {
    #[cfg(any(windows, target_os = "android"))]
    let s = format!("http://{}.localhost/", crate::notes::SCHEME);
    #[cfg(not(any(windows, target_os = "android")))]
    let s = format!("{}://localhost/", crate::notes::SCHEME);
    Url::parse(&s).expect("the notes scheme's URL")
}

/// Opens an https address in the person's browser; any other address is refused here too,
/// whatever its caller checked.
fn open_external(app: &AppHandle, url: String) {
    let url = match release::https_url(&url) {
        Ok(u) => u,
        Err(e) => {
            eprintln!("pluxy-app: not opening {url:?}: {e}");
            return;
        }
    };
    #[cfg(target_os = "linux")]
    {
        let _ = app.run_on_main_thread(move || {
            use gtk::gio;
            if let Err(e) =
                gio::AppInfo::launch_default_for_uri(&url, None::<&gio::AppLaunchContext>)
            {
                eprintln!("pluxy-app: could not open {url}: {e}");
            }
        });
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = app;
        eprintln!("pluxy-app: opening {url} in the browser is supported on Linux only for now");
    }
}

/// Opens a webview over rect of the window (the part of the notes area that shows; none: hidden),
/// showing product's release notes on the current channel, and returns its label.
#[tauri::command]
pub fn release_notes_open(
    webview: Webview,
    state: tauri::State<App>,
    product: Product,
    rect: Option<Rect>,
    style: Style,
) -> Result<String, String> {
    trusted(&webview)?;
    let notes = state
        .releases
        .lock()
        .unwrap()
        .latest(product)
        .map(|r| r.notes.clone())
        .filter(|n| !n.trim().is_empty())
        .ok_or("this release has no notes")?;
    let label = state
        .notes
        .lock()
        .unwrap()
        .bind(Doc::new(product, &notes, &style))?;
    let doc = notes_url();
    let app = webview.app_handle().clone();
    let nav_label = label.clone();
    let builder = WebviewBuilder::new(&label, WebviewUrl::CustomProtocol(doc.clone()))
        .data_directory(state.notes_dir.clone())
        .devtools(false)
        .on_navigation(move |u| {
            if *u == doc {
                return true;
            }
            let open = app
                .state::<App>()
                .notes
                .lock()
                .unwrap()
                .external(&nav_label, u);
            if let Some(open) = open {
                open_external(&app, open);
            }
            false
        })
        .on_new_window(|_, _| NewWindowResponse::Deny)
        // Nothing the notes link to is saved: WebKit's "Download Linked File" included.
        .on_download(|_w, _| {
            #[cfg(feature = "native-test")]
            {
                _w.app_handle()
                    .state::<App>()
                    .notes
                    .lock()
                    .unwrap()
                    .downloads += 1;
            }
            false
        });
    let window: Window = webview.window();
    // No rect: none of the notes area shows, so the webview starts hidden.
    let b = rect.map(Rect::bounds);
    let at = b.unwrap_or(crate::place::Bounds {
        x: 0.0,
        y: 0.0,
        width: 1.0,
        height: 1.0,
    });
    let placed = window
        .add_child(
            builder,
            LogicalPosition::new(at.x.max(0.0), at.y.max(0.0)),
            LogicalSize::new(at.width.max(1.0), at.height.max(1.0)),
        )
        .map_err(|e| e.to_string())
        .and_then(|_| crate::place::adopt(&window, &label, b))
        .and_then(|_| no_context_menu(webview.app_handle(), &label));
    if let Err(e) = placed {
        state.notes.lock().unwrap().unbind(&label);
        if let Some(w) = webview.app_handle().get_webview(&label) {
            let _ = w.close();
        }
        return Err(e);
    }
    Ok(label)
}

/// Turns off the notes webview's context menu: its entries (download, copy or open a link
/// elsewhere) would reach past the one way out, a person following a rendered link.
fn no_context_menu(app: &AppHandle, label: &str) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        let w = app
            .get_webview(label)
            .ok_or_else(|| format!("no notes webview {label}"))?;
        let _app = app.clone();
        w.with_webview(move |pw| {
            use webkit2gtk::WebViewExt;
            pw.inner().connect_context_menu(move |_, _, _, _| {
                #[cfg(feature = "native-test")]
                {
                    _app.state::<App>().notes.lock().unwrap().menus += 1;
                }
                true
            });
        })
        .map_err(|e| e.to_string())
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (app, label);
        Ok(())
    }
}

fn notes_webview(app: &AppHandle, label: &str) -> Result<Webview, String> {
    if !app.state::<App>().notes.lock().unwrap().has(label) {
        return Err(format!("no notes webview {label}"));
    }
    app.get_webview(label)
        .ok_or_else(|| format!("no notes webview {label}"))
}

#[tauri::command]
pub fn release_notes_place(
    webview: Webview,
    label: String,
    rect: Option<Rect>,
) -> Result<(), String> {
    trusted(&webview)?;
    crate::place::place(
        &notes_webview(webview.app_handle(), &label)?,
        rect.map(Rect::bounds),
    )
}

#[tauri::command]
pub fn release_notes_close(
    webview: Webview,
    state: tauri::State<App>,
    label: String,
) -> Result<(), String> {
    trusted(&webview)?;
    let w = notes_webview(webview.app_handle(), &label);
    state.notes.lock().unwrap().unbind(&label);
    crate::place::forget(&label);
    w?.close().map_err(|e| e.to_string())
}

/// "View full release notes": opens the release's notes address, from the feed, in the browser.
#[tauri::command]
pub fn release_notes_full(
    webview: Webview,
    state: tauri::State<App>,
    product: Product,
) -> Result<(), String> {
    trusted(&webview)?;
    let url = state
        .releases
        .lock()
        .unwrap()
        .latest(product)
        .and_then(|r| r.notes_url.clone())
        .ok_or("this release has no notes address")?;
    if !state.notes.lock().unwrap().rate() {
        return Err("one address a second".into());
    }
    open_external(webview.app_handle(), url);
    Ok(())
}

/// The `pluxy-notes:` scheme: each notes webview's own document and style sheet, nothing else.
pub fn notes_scheme(
    ctx: tauri::UriSchemeContext<'_, tauri::Wry>,
    request: tauri::http::Request<Vec<u8>>,
) -> tauri::http::Response<Vec<u8>> {
    use tauri::http::{header, Response};
    let served = ctx.app_handle().state::<App>().notes.lock().unwrap().serve(
        ctx.webview_label(),
        request.method().as_str(),
        request.uri().path(),
    );
    Response::builder()
        .status(served.status)
        .header(header::CONTENT_TYPE, served.kind)
        .header("Content-Security-Policy", crate::notes::CSP)
        .header(header::CACHE_CONTROL, "no-store")
        .header("X-Content-Type-Options", "nosniff")
        .header("Referrer-Policy", "no-referrer")
        .body(served.body)
        .expect("a response")
}
