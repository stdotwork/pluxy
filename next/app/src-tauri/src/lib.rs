//! The pluxy desktop app: one window showing the UI kit (`../src`), a native tray icon, and the
//! connection to core in one of three modes (`conn.rs`). Closing the window destroys it and its
//! webviews, so they hold no memory while the app sits in the tray; the tray opens a new one.
//!
//! Every app command is declared in the app's command manifest (`build.rs`) and granted only to
//! the window's webview, labelled `main` (`capabilities/main.json`). Extension pages run in
//! webviews of their own (`pages.rs`), which no capability names. Packages installed on this
//! device (`device.rs`) serve their pages' files to those webviews over the `pluxy-app:` scheme.

include!(concat!(env!("OUT_DIR"), "/build_receipt.rs"));
#[cfg(any(feature = "native-test", feature = "artifact-test-trust"))]
#[used]
#[no_mangle]
pub static PLUXY_TEST_BUILD: [u8; 31] = *b"PLUXY_TEST_BUILD_DO_NOT_RELEASE";

pub mod artifact;
pub mod client;
mod command_names;
#[cfg(target_os = "linux")]
pub mod confirmation;
pub mod conn;
pub mod device;
mod key_entry;
pub mod managed;
#[cfg(feature = "native-test")]
mod native_test;
pub mod pages;
mod place;
#[cfg(debug_assertions)]
pub mod release_config;
pub mod trust;

pub use command_names::COMMANDS;

use conn::{Change, Connection, Dirs, Mode, View};
use pages::{Binding, Pages};
use serde::Deserialize;
use serde_json::Value;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{sync_channel, SyncSender, TrySendError};
use std::sync::{Mutex, OnceLock};
use tauri::http::{header, Response};
use tauri::menu::{Menu, MenuItem, PredefinedMenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::webview::{NewWindowResponse, PageLoadEvent};
use tauri::window::WindowBuilder;
use tauri::{
    AppHandle, Emitter, EventTarget, LogicalPosition, LogicalSize, Manager, RunEvent, Url, Webview,
    WebviewBuilder, WebviewUrl, Window, WindowEvent, Wry,
};

/// The window, and its webview: the only webview any capability names.
pub const WINDOW: &str = "main-window";
pub const MAIN: &str = "main";

struct App {
    conn: Mutex<Connection>,
    #[cfg(target_os = "linux")]
    confirmations: Mutex<confirmation::Confirmations>,
    /// The page webviews' own web context's folder: nothing shared with the window's.
    pages_dir: std::path::PathBuf,
    pages: Mutex<Pages>,
    /// Page webviews being closed, unbound already, until they are destroyed: one that could
    /// not be closed is closed again at the next close, so none is forgotten while it may run.
    closing: Mutex<Vec<String>>,
    /// This device's extension catalog. Installs check and stage a release without it.
    device: Mutex<device::Catalog>,
    device_dirs: device::Dirs,
    status: Mutex<Option<MenuItem<Wry>>>,
    has_tray: AtomicBool,
    quitting: AtomicBool,
    key_entry_open: AtomicBool,
    key_entry_after: Mutex<Option<std::time::Instant>>,
}

/// Every command checks its caller too: the capability already allows only the window.
fn trusted(w: &Webview) -> Result<(), String> {
    // The native test checks that no command is ever reached from another webview.
    #[cfg(feature = "native-test")]
    eprintln!(
        "native-test: reached {}",
        serde_json::json!({"webview": w.label()})
    );
    if w.label() == MAIN {
        Ok(())
    } else {
        Err("only the app's window may call this".into())
    }
}

fn status_text(v: &View) -> String {
    match (v.mode, &v.target, &v.core) {
        (Some(Mode::Managed), Some(t), Some(c)) if c.state == "running" => {
            format!("Core running at {t}")
        }
        (Some(Mode::Managed), _, _) => "Core stopped".into(),
        (Some(Mode::Local), Some(t), _) => format!("Connected to {t}"),
        (Some(Mode::Remote), Some(t), _) => format!("Remote core {t}"),
        _ => "Not connected".into(),
    }
}

fn show_status(app: &AppHandle, v: &View) {
    if let Some(item) = app.state::<App>().status.lock().unwrap().as_ref() {
        let _ = item.set_text(status_text(v));
    }
}

/// The app's own address, which the window may show; it navigates nowhere else.
fn is_app_url(u: &Url) -> bool {
    (u.scheme() == "tauri" && u.host_str() == Some("localhost"))
        || (matches!(u.scheme(), "http" | "https") && u.host_str() == Some("tauri.localhost"))
}

fn page_scheme_url() -> Url {
    #[cfg(any(windows, target_os = "android"))]
    let s = format!("http://{}.localhost/", pages::SCHEME);
    #[cfg(not(any(windows, target_os = "android")))]
    let s = format!("{}://localhost/", pages::SCHEME);
    Url::parse(&s).expect("the page scheme's URL")
}

/// Shows the window, making a new one if it was closed.
fn open_window(app: &AppHandle) -> tauri::Result<()> {
    if let Some(w) = app.get_window(WINDOW) {
        let _ = w.unminimize();
        w.show()?;
        return w.set_focus();
    }
    let w = WindowBuilder::new(app, WINDOW)
        .title("pluxy")
        .inner_size(1100.0, 760.0)
        .min_inner_size(640.0, 480.0)
        .build()?;
    let size = w.inner_size()?.to_logical::<f64>(w.scale_factor()?);
    let main = WebviewBuilder::new(MAIN, WebviewUrl::App("index.html".into()))
        .auto_resize()
        .on_navigation(is_app_url)
        .on_new_window(|_, _| NewWindowResponse::Deny)
        .on_page_load(|wv, p| {
            // A reload of the window's document leaves no page webview behind.
            if p.event() == PageLoadEvent::Started {
                close_pages(wv.app_handle());
            }
        });
    w.add_child(main, LogicalPosition::new(0.0, 0.0), size)?;
    Ok(())
}

fn close_pages(app: &AppHandle) {
    let labels = {
        let st = app.state::<App>();
        let mut pages = st.pages.lock().unwrap();
        let l = pages.labels();
        pages.clear();
        l
    };
    // Each one that cannot be closed stays in `closing`.
    let _ = close_webviews(app, labels);
}

/// Closes page webviews whose pages are unbound already, and those that could not be closed
/// before, and waits until they are destroyed. Each is tracked in `closing` from now until
/// then; one that cannot be closed stays tracked and is tried again at the next call. The
/// answer says which could not be closed.
///
/// The closes run on the main thread: there, tauri-runtime-wry 2.12.3 handles a webview's
/// close inline (`send_user_message`), removing the webview's last reference, whose drop
/// destroys the WebKit widget (wry 0.57.1, `InnerWebView::drop`), before `close` returns. From
/// another thread `close` only posts the message.
fn close_webviews(app: &AppHandle, labels: Vec<String>) -> Vec<String> {
    let todo: Vec<String> = {
        let st = app.state::<App>();
        let mut closing = st.closing.lock().unwrap();
        for l in labels {
            if !closing.contains(&l) {
                closing.push(l);
            }
        }
        closing.clone()
    };
    let (tx, rx) = std::sync::mpsc::channel();
    let a = app.clone();
    let sent = app.run_on_main_thread(move || {
        let mut left = Vec::new();
        for l in todo {
            place::forget(&l);
            let closed = match a.get_webview(&l) {
                None => Ok(()),
                Some(w) => w.close(),
            };
            match closed {
                Ok(()) => a.state::<App>().closing.lock().unwrap().retain(|x| *x != l),
                Err(e) => left.push(format!("page webview {l} could not be closed: {e}")),
            }
        }
        let _ = tx.send(left);
    });
    match sent {
        Ok(()) => rx.recv().unwrap_or_else(|_| {
            vec!["page webviews could not be closed: the main thread stopped".into()]
        }),
        Err(e) => vec![format!("page webviews could not be closed: {e}")],
    }
}

/// Stops the app's own core, if it runs one, and exits. A local or remote core keeps running.
fn quit(app: &AppHandle) {
    let st = app.state::<App>();
    st.quitting.store(true, Ordering::SeqCst);
    #[cfg(target_os = "linux")]
    st.confirmations.lock().unwrap().invalidate();
    st.conn.lock().unwrap().stop_core();
    app.exit(0);
}

fn build_tray(app: &AppHandle) -> tauri::Result<()> {
    let status = MenuItem::with_id(app, "status", "Not connected", false, None::<&str>)?;
    let open = MenuItem::with_id(app, "open", "Open pluxy", true, None::<&str>)?;
    let quit_item = MenuItem::with_id(app, "quit", "Quit pluxy", true, None::<&str>)?;
    let access = MenuItem::with_id(
        app,
        "launch-settings",
        "Remembered launches…",
        cfg!(target_os = "linux"),
        None::<&str>,
    )?;
    let sep = PredefinedMenuItem::separator(app)?;
    let menu = Menu::with_items(app, &[&status, &sep, &open, &access, &quit_item])?;
    let mut tray = TrayIconBuilder::with_id("pluxy")
        .tooltip("pluxy")
        .menu(&menu)
        .on_menu_event(|app, e| match e.id().as_ref() {
            "open" => {
                let _ = open_window(app);
            }
            "quit" => quit(app),
            #[cfg(target_os = "linux")]
            "launch-settings" => {
                let _ = confirmation::settings(app);
            }
            _ => {}
        })
        .on_tray_icon_event(|tray, e| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = e
            {
                let _ = open_window(tray.app_handle());
            }
        });
    if let Some(icon) = app.default_window_icon() {
        tray = tray.icon(icon.clone());
    }
    tray.build(app)?;
    *app.state::<App>().status.lock().unwrap() = Some(status);
    Ok(())
}

#[tauri::command]
fn connection_get(webview: Webview, state: tauri::State<App>) -> Result<View, String> {
    trusted(&webview)?;
    Ok(state.conn.lock().unwrap().view())
}

/// Runs f on the connection away from the window's thread, which starting a core would block,
/// and answers with the view, holding f's error if it failed.
async fn with_conn(
    webview: &Webview,
    f: impl FnOnce(&mut Connection) -> Result<(), String> + Send + 'static,
) -> Result<View, String> {
    trusted(webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let st = app.state::<App>();
        let mut c = st.conn.lock().unwrap();
        let r = f(&mut c);
        let mut v = c.view();
        if let Err(e) = r {
            v.error = Some(e);
        }
        drop(c);
        show_status(&app, &v);
        v
    })
    .await
    .map_err(|e| e.to_string())
}

#[tauri::command]
async fn connection_set(webview: Webview, change: Change) -> Result<View, String> {
    trusted(&webview)?;
    let app = webview.app_handle();
    if app.state::<App>().key_entry_open.load(Ordering::SeqCst) {
        return Err("finish native key entry first".into());
    }
    with_conn(&webview, move |c| c.change(change)).await
}

#[tauri::command]
async fn core_restart(webview: Webview) -> Result<View, String> {
    with_conn(&webview, |c| c.restart_core()).await
}

/// This command accepts connection metadata only; GTK supplies the secret on the native side.
#[derive(serde::Serialize)]
struct KeyEntryResult {
    status: &'static str,
    connection: View,
}

#[tauri::command]
async fn key_entry(webview: Webview, change: Option<Change>) -> Result<KeyEntryResult, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    if app
        .state::<App>()
        .key_entry_open
        .swap(true, Ordering::SeqCst)
    {
        return Err("native key entry is already open".into());
    }
    tauri::async_runtime::spawn_blocking(move || {
        struct Pending(AppHandle);
        impl Drop for Pending {
            fn drop(&mut self) {
                self.0
                    .state::<App>()
                    .key_entry_open
                    .store(false, Ordering::SeqCst);
            }
        }
        let _pending = Pending(app.clone());
        let st = app.state::<App>();
        let prepared = st.conn.lock().unwrap().prepare_key(change);
        let target = match prepared {
            Ok(target) => target,
            Err(reason) => {
                let mut connection = st.conn.lock().unwrap().view();
                connection.error = Some(reason);
                return KeyEntryResult {
                    status: "error",
                    connection,
                };
            }
        };
        if let Some(after) = *st.key_entry_after.lock().unwrap() {
            std::thread::sleep(after.saturating_duration_since(std::time::Instant::now()));
        }
        // GTK must remain free to handle IPC/window close while the dialog is open.
        let input = key_entry::request(&app, target.summary.clone());
        let mut c = st.conn.lock().unwrap();
        let mut error = None;
        let status = match input {
            Ok(None) => {
                *st.key_entry_after.lock().unwrap() =
                    Some(std::time::Instant::now() + std::time::Duration::from_secs(1));
                "cancelled"
            }
            Ok(Some(input)) => match c.finish_key(target, &input.key, input.remember) {
                Ok(()) => "signed_in",
                Err(e) if e == "core refused this key" => {
                    error = Some(e);
                    "wrong_key"
                }
                Err(reason) => {
                    error = Some(reason);
                    "error"
                }
            },
            Err(e) => {
                error = Some(e.into());
                "error"
            }
        };
        let mut connection = c.view();
        if error.is_some() {
            connection.error = error;
        }
        drop(c);
        show_status(&app, &connection);
        KeyEntryResult { status, connection }
    })
    .await
    .map_err(|_| "native key entry failed".into())
}

#[tauri::command]
async fn sign_out(webview: Webview) -> Result<View, String> {
    with_conn(&webview, |c| c.sign_out()).await
}

#[tauri::command]
async fn core_call(
    webview: Webview,
    state: tauri::State<'_, App>,
    method: String,
    path: String,
    body: Option<Value>,
) -> Result<client::Reply, String> {
    trusted(&webview)?;
    #[cfg(feature = "native-test")]
    native_test::ingress_operation(&method, &path);
    let caller = state.conn.lock().unwrap().caller();
    tauri::async_runtime::spawn_blocking(move || caller.call(&method, &path, body.as_ref()))
        .await
        .map_err(|e| e.to_string())
}

#[derive(Clone, Copy, Debug, Deserialize)]
struct Rect {
    x: f64,
    y: f64,
    width: f64,
    height: f64,
}

impl Rect {
    fn bounds(self) -> place::Bounds {
        place::Bounds {
            x: self.x,
            y: self.y,
            width: self.width,
            height: self.height,
        }
    }
}

/// Opens a webview for an extension page over rect of the window, and returns its label.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
fn page_open(
    webview: Webview,
    extension: String,
    page: String,
    title: String,
    url: String,
    pages_origin: String,
    rect: Rect,
) -> Result<String, String> {
    trusted(&webview)?;
    let url = pages::page_url(&url, &pages_origin)?;
    open_page(
        &webview,
        Binding {
            extension,
            page,
            title,
            url,
            pages_origin,
            device: None,
        },
        rect,
    )
}

/// Opens a webview for page of package id on this device, at the address its catalog gives.
#[tauri::command]
fn device_page_open(
    webview: Webview,
    state: tauri::State<App>,
    id: String,
    page: String,
    rect: Rect,
) -> Result<String, String> {
    trusted(&webview)?;
    // Bound while the catalog is locked: a change to the package (`device_changed`, under the
    // same lock) either finds this page bound and closes it, or comes before its address is
    // read.
    let (label, site) = {
        let catalog = state.device.lock().unwrap();
        let (url, folder, title) = catalog.page(&id, &page)?;
        let url = Url::parse(&url).map_err(|e| e.to_string())?;
        let binding = Binding {
            // Its own budget: a core package with the same ID is another package.
            extension: format!("device:{id}"),
            page,
            title,
            url,
            pages_origin: device::asset_base(),
            device: Some(folder),
        };
        let site = binding.clone();
        (state.pages.lock().unwrap().bind(binding)?, site)
    };
    add_page(&webview, label, site, rect)
}

fn open_page(webview: &Webview, binding: Binding, rect: Rect) -> Result<String, String> {
    let site = binding.clone();
    let label = webview.state::<App>().pages.lock().unwrap().bind(binding)?;
    add_page(webview, label, site, rect)
}

/// Makes the webview for page label, bound to site.
fn add_page(webview: &Webview, label: String, site: Binding, rect: Rect) -> Result<String, String> {
    let state = webview.state::<App>();
    let app_doc = page_scheme_url();
    let navigation_app = webview.app_handle().clone();
    let navigation_label = label.clone();
    let builder = WebviewBuilder::new(&label, WebviewUrl::CustomProtocol(app_doc.clone()))
        // A web context of their own, apart from the window's storage and cookies. Not
        // `incognito`: on Linux, Tauri then gives the webview no custom schemes at all.
        .data_directory(state.pages_dir.clone())
        .devtools(false)
        // The app's document at the top, the page's site in its frame; nowhere else.
        .on_navigation(move |u| {
            navigation_app
                .state::<App>()
                .pages
                .lock()
                .unwrap()
                .frame_navigation(&navigation_label, u);
            notify_page_closed(&navigation_app, &navigation_label);
            #[cfg(feature = "native-test")]
            if u.scheme() == pages::SCHEME
                && u.host_str() == Some("localhost")
                && u.path() == "/ingress-test"
            {
                return true;
            }
            *u == app_doc || site.on_site(u)
        })
        .on_new_window(|_, _| NewWindowResponse::Deny)
        .on_page_load(|wv, p| {
            if p.event() == PageLoadEvent::Started {
                wv.app_handle()
                    .state::<App>()
                    .pages
                    .lock()
                    .unwrap()
                    .begin_document(wv.label());
                notify_page_closed(wv.app_handle(), wv.label());
            }
        });
    let window: Window = webview.window();
    let b = rect.bounds();
    let placed = window
        .add_child(
            builder,
            LogicalPosition::new(b.x.max(0.0), b.y.max(0.0)),
            LogicalSize::new(b.width.max(1.0), b.height.max(1.0)),
        )
        .map_err(|e| e.to_string())
        .and_then(|_| place::adopt(&window, &label, b));
    // Unbound while it was made (its package changed, or the window closed its pages): the
    // webview must not stay.
    let placed = placed.and_then(|_| {
        if state.pages.lock().unwrap().get(&label).is_some() {
            Ok(())
        } else {
            Err("the page was closed while it opened".to_string())
        }
    });
    if let Err(e) = placed {
        state.pages.lock().unwrap().unbind(&label);
        let left = close_webviews(webview.app_handle(), vec![label]);
        return Err(std::iter::once(e)
            .chain(left)
            .collect::<Vec<_>>()
            .join("; "));
    }
    Ok(label)
}

/// A page webview of the window's, by label: never the window's own.
fn page_webview(app: &AppHandle, label: &str) -> Result<Webview, String> {
    if app
        .state::<App>()
        .pages
        .lock()
        .unwrap()
        .get(label)
        .is_none()
    {
        return Err(format!("no page webview {label}"));
    }
    app.get_webview(label)
        .ok_or_else(|| format!("no page webview {label}"))
}

#[tauri::command]
fn page_place(webview: Webview, label: String, rect: Rect) -> Result<(), String> {
    trusted(&webview)?;
    place::place(&page_webview(webview.app_handle(), &label)?, rect.bounds())
}

#[tauri::command]
fn page_close(webview: Webview, state: tauri::State<App>, label: String) -> Result<(), String> {
    trusted(&webview)?;
    page_webview(webview.app_handle(), &label)?;
    state.pages.lock().unwrap().unbind(&label);
    let left = close_webviews(webview.app_handle(), vec![label]);
    if left.is_empty() {
        Ok(())
    } else {
        Err(left.join("; "))
    }
}

/// Delivers a message of the window's channel to a page webview's app document.
#[tauri::command]
fn page_send(
    webview: Webview,
    label: String,
    generation: String,
    msg: Value,
) -> Result<(), String> {
    trusted(&webview)?;
    deliver_page(webview.app_handle(), &label, &generation, msg)
}

fn deliver_page(app: &AppHandle, label: &str, generation: &str, msg: Value) -> Result<(), String> {
    let target = page_webview(app, label)?;
    let prepared = app
        .state::<App>()
        .pages
        .lock()
        .unwrap()
        .prepare_delivery(label, generation, &msg)?;
    let Some((js, ticket)) = prepared else {
        return Ok(());
    };
    let callback_app = app.clone();
    let callback_label = label.to_string();
    let callback_generation = generation.to_string();
    let callback_msg = serde_json::json!({"type":msg["type"],"data":{"id":msg["data"]["id"],"event":msg["data"]["event"],"error":{"code":msg["data"]["error"]["code"]}}});
    let result = target
        .eval_with_callback(js, move |result| {
            finish_page_delivery(
                &callback_app,
                &callback_label,
                &callback_generation,
                &callback_msg,
                ticket,
                result == "true",
            );
        })
        .map_err(|e| e.to_string());
    if result.is_err() {
        finish_page_delivery(app, label, generation, &msg, ticket, false);
    }
    result
}

fn finish_page_delivery(
    app: &AppHandle,
    label: &str,
    generation: &str,
    msg: &Value,
    ticket: u64,
    delivered: bool,
) {
    let next = app
        .state::<App>()
        .pages
        .lock()
        .unwrap()
        .finish_delivery(label, generation, msg, ticket, delivered);
    notify_page_closed(app, label);
    if let Some(next) = next {
        let _ = deliver_page(app, label, generation, next);
    }
}

fn notify_page_closed(app: &AppHandle, label: &str) {
    let closed = app.state::<App>().pages.lock().unwrap().take_closed(label);
    if let Some(closed) = closed {
        let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-page", closed);
    }
}

/// Initiates review of a native-prepared action. No approval result, recipe or gesture input.
#[tauri::command]
fn confirmation_request(webview: Webview, action: String) -> Result<(), String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        confirmation::request(webview.app_handle(), action)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = action;
        Err("native confirmations are supported on Linux only".into())
    }
}
#[tauri::command]
fn confirmation_settings(webview: Webview) -> Result<(), String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        confirmation::settings(webview.app_handle())
    }
    #[cfg(not(target_os = "linux"))]
    {
        Err("native confirmations are supported on Linux only".into())
    }
}

/// This device's extension catalog, for the window.
#[tauri::command]
fn device_list(webview: Webview, state: tauri::State<App>) -> Result<device::View, String> {
    trusted(&webview)?;
    Ok(state.device.lock().unwrap().view())
}

/// What the registries in `device-registries.json` offer this computer.
#[tauri::command]
async fn device_registries(webview: Webview) -> Result<Vec<device::RegistryView>, String> {
    trusted(&webview)?;
    let dirs = webview.state::<App>().device_dirs.clone();
    tauri::async_runtime::spawn_blocking(move || device::offers(&dirs))
        .await
        .map_err(|e| e.to_string())?
}

/// Makes a change f to package id on this device, then closes the package's open pages and
/// tells the window its catalog changed: a page of a release that is no longer active never
/// stays open, even hidden. The pages are unbound under the catalog's lock, with the change
/// (see `device_page_open`). The change is made even when a page or a folder it left could not
/// be closed or deleted; the answer then says which.
fn device_changed(
    app: &AppHandle,
    id: &str,
    f: impl FnOnce(&mut device::Catalog) -> Result<(), String>,
) -> Result<device::View, String> {
    let st = app.state::<App>();
    let (labels, view, mut left) = {
        let mut catalog = st.device.lock().unwrap();
        f(&mut catalog)?;
        let mut pages = st.pages.lock().unwrap();
        let labels = pages.device_labels(id);
        for l in &labels {
            pages.unbind(l);
        }
        let left: Vec<String> = catalog.take_cleanup().into_iter().collect();
        (labels, catalog.view(), left)
    };
    left.extend(close_webviews(app, labels));
    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-device", ());
    if left.is_empty() {
        Ok(view)
    } else {
        Err(format!("{id} was changed, but {}", left.join("; ")))
    }
}

/// Commits a staged candidate and closes the package's open pages.
fn device_commit(app: &AppHandle, c: device::Candidate) -> Result<device::View, String> {
    let id = c.id.clone();
    device_changed(app, &id, |catalog| catalog.commit(c))
}

/// Installs or updates package id from a registry: version, or its newest release.
#[tauri::command]
async fn device_install(
    webview: Webview,
    registry: String,
    id: String,
    version: Option<String>,
) -> Result<device::View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let dirs = app.state::<App>().device_dirs.clone();
        let c = device::stage_from_registry(&dirs, &registry, &id, version.as_deref())?;
        device_commit(&app, c)
    })
    .await
    .map_err(|e| e.to_string())?
}

/// The native file chooser's title.
pub const CHOOSER: &str = "Install an extension package on this device";

/// Asks the person for a package archive in the native file chooser. None: they cancelled.
#[cfg(target_os = "linux")]
fn choose_archive(app: &AppHandle) -> Result<Option<std::path::PathBuf>, String> {
    let (tx, rx) = std::sync::mpsc::channel();
    app.run_on_main_thread(move || {
        use gtk::prelude::*;
        let d = gtk::FileChooserDialog::with_buttons(
            Some(CHOOSER),
            None::<&gtk::Window>,
            gtk::FileChooserAction::Open,
            &[
                ("_Cancel", gtk::ResponseType::Cancel),
                ("_Open", gtk::ResponseType::Accept),
            ],
        );
        d.set_default_response(gtk::ResponseType::Accept);
        d.set_local_only(true);
        d.set_modal(true);
        let f = gtk::FileFilter::new();
        f.set_name(Some("Extension packages (.tar.gz)"));
        f.add_pattern("*.tar.gz");
        f.add_pattern("*.tgz");
        d.add_filter(f);
        let done = std::cell::Cell::new(false);
        d.connect_response(move |d, r| {
            if done.replace(true) {
                return;
            }
            let chosen = (r == gtk::ResponseType::Accept)
                .then(|| d.filename())
                .flatten();
            let _ = tx.send(chosen);
            d.hide();
            // No GTK state is queried after destroying this finished chooser.
            unsafe {
                d.destroy();
            }
        });
        d.show_all();
    })
    .map_err(|e| e.to_string())?;
    rx.recv().map_err(|_| "the file chooser closed".to_string())
}

/// Installs or updates a package from a local archive the person chooses in the native file
/// chooser; the window names no path.
#[tauri::command]
async fn device_install_file(webview: Webview) -> Result<device::View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(target_os = "linux")]
        let path = choose_archive(&app)?.ok_or("no file chosen")?;
        #[cfg(not(target_os = "linux"))]
        let path: std::path::PathBuf =
            return Err("installing from a file is supported on Linux only".into());
        let name = path
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default();
        let bytes = device::read_archive(&path)?;
        let dirs = app.state::<App>().device_dirs.clone();
        let c = device::stage(
            &dirs,
            &bytes,
            device::Source {
                kind: "local".into(),
                name,
                index: None,
            },
            None,
        )?;
        device_commit(&app, c)
    })
    .await
    .map_err(|e| e.to_string())?
}

fn device_change(
    webview: &Webview,
    id: &str,
    f: impl FnOnce(&mut device::Catalog) -> Result<(), String>,
) -> Result<device::View, String> {
    trusted(webview)?;
    device_changed(webview.app_handle(), id, f)
}

#[tauri::command]
fn device_enable(webview: Webview, id: String, enabled: bool) -> Result<device::View, String> {
    device_change(&webview, &id, |c| c.set_enabled(&id, enabled))
}

#[tauri::command]
fn device_remove(webview: Webview, id: String) -> Result<device::View, String> {
    device_change(&webview, &id, |c| c.remove(&id))
}

/// Makes package id's last good release active again.
#[tauri::command]
fn device_rollback(webview: Webview, id: String) -> Result<device::View, String> {
    device_change(&webview, &id, |c| c.rollback(&id))
}

/// The `pluxy-app:` scheme: a device page's files, served only to the page webview bound to
/// that page's release folder, while the release is active and each file is unchanged.
fn asset_scheme(
    ctx: tauri::UriSchemeContext<'_, Wry>,
    request: tauri::http::Request<Vec<u8>>,
    responder: tauri::UriSchemeResponder,
) {
    let app = ctx.app_handle().clone();
    let label = ctx.webview_label().to_string();
    let path = request.uri().path().to_string();
    // Which file, under the locks; reading and hashing it, without them.
    let asset = (request.method() == "GET")
        .then(|| {
            let folder = app
                .state::<App>()
                .pages
                .lock()
                .unwrap()
                .get(&label)
                .and_then(|b| b.device.clone())?;
            let (id, digest, file) = device::asset_request(&folder, &path)?;
            app.state::<App>()
                .device
                .lock()
                .unwrap()
                .asset_file(&id, &digest, &file)
        })
        .flatten();
    let job = AssetJob {
        asset,
        label,
        path,
        responder,
    };
    // Past the queue's bound, a request is answered at once rather than waited on.
    if let Err(TrySendError::Full(job) | TrySendError::Disconnected(job)) =
        asset_queue().try_send(job)
    {
        respond_asset(job, 503, None);
    }
}

/// Asset requests waiting for a reader at most; they hold no file.
const ASSET_QUEUE: usize = 1024;
/// Threads that read asset files: at most this many files are read and hashed at a time.
const ASSET_READERS: usize = 2;

/// An asset request, its file found under the locks, waiting to be read.
struct AssetJob {
    asset: Option<device::Asset>,
    label: String,
    path: String,
    responder: tauri::UriSchemeResponder,
}

fn asset_queue() -> &'static SyncSender<AssetJob> {
    static QUEUE: OnceLock<SyncSender<AssetJob>> = OnceLock::new();
    QUEUE.get_or_init(|| {
        let (tx, rx) = sync_channel::<AssetJob>(ASSET_QUEUE);
        let rx = std::sync::Arc::new(Mutex::new(rx));
        for _ in 0..ASSET_READERS {
            let rx = rx.clone();
            std::thread::spawn(move || loop {
                let job = rx.lock().unwrap_or_else(|e| e.into_inner()).recv();
                let Ok(mut job) = job else { return };
                let found = job.asset.take().and_then(|a| Some((a.read()?, a.kind)));
                let status = if found.is_some() { 200 } else { 404 };
                respond_asset(job, status, found);
            });
        }
        tx
    })
}

fn respond_asset(job: AssetJob, status: u16, found: Option<(Vec<u8>, &'static str)>) {
    #[cfg(feature = "native-test")]
    native_test::log(
        "asset",
        serde_json::json!({"label": job.label, "path": job.path, "status": status}),
    );
    #[cfg(not(feature = "native-test"))]
    let _ = (&job.label, &job.path);
    let (body, kind) = found.unwrap_or((Vec::new(), "text/plain"));
    // As core's pages site: sandboxed with scripts, loading only from the asset scheme (where
    // this webview gets only its own folder), and no requests from script, frames or forms.
    let s = device::ASSET_SCHEME;
    let csp = if status == 200 {
        format!("sandbox allow-scripts; default-src 'none'; script-src {s}:; style-src {s}:; img-src {s}: data: blob:; font-src {s}: data:; media-src {s}: data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; form-action 'none'; base-uri 'none'")
    } else {
        "sandbox; default-src 'none'".to_string()
    };
    let resp = Response::builder()
        .status(status)
        .header(header::CONTENT_TYPE, kind)
        .header("Content-Security-Policy", csp)
        // A sandboxed page has an opaque origin; its module scripts and fonts load with CORS.
        .header("Access-Control-Allow-Origin", "*")
        .header(header::CACHE_CONTROL, "no-store")
        .header("X-Content-Type-Options", "nosniff")
        .header("Referrer-Policy", "no-referrer")
        .body(body)
        .expect("a response");
    job.responder.respond(resp);
}

/// The `pluxy-page:` scheme: the page webviews' app document and their way to the window.
fn page_scheme(
    ctx: tauri::UriSchemeContext<'_, Wry>,
    request: tauri::http::Request<Vec<u8>>,
    responder: tauri::UriSchemeResponder,
) {
    let app = ctx.app_handle().clone();
    let label = ctx.webview_label().to_string();
    let (served, msg) = {
        let st = app.state::<App>();
        let mut pages = st.pages.lock().unwrap();
        pages::serve(
            &mut pages,
            &label,
            request.method().as_str(),
            request.uri().path(),
            request.body(),
        )
    };
    #[cfg(feature = "native-test")]
    native_test::ingress_count(
        request.method().as_str(),
        request.uri().path(),
        served.status,
        request.uri().query(),
    );
    if let Some(m) = msg {
        #[cfg(all(target_os = "linux", feature = "native-test"))]
        if m.kind == "call" && m.data.get("id").and_then(Value::as_str) == Some("spoof-confirm") {
            confirmation::native::PAGE_CLAIMS.fetch_add(1, Ordering::SeqCst);
        }
        let result = app.emit_to(EventTarget::webview(MAIN), "pluxy-page", m);
        #[cfg(feature = "native-test")]
        if result.is_ok() {
            native_test::ingress_forwarded();
        }
        let _ = result;
    }
    let resp = Response::builder()
        .status(served.status)
        .header(header::CONTENT_TYPE, served.kind)
        .header("Content-Security-Policy", served.csp)
        .header(header::CACHE_CONTROL, "no-store")
        .header("X-Content-Type-Options", "nosniff")
        .header("Referrer-Policy", "no-referrer")
        .body(served.body)
        .expect("a response");
    #[cfg(feature = "native-test")]
    let resp = {
        let mut resp = resp;
        resp.headers_mut().insert(
            "Access-Control-Allow-Origin",
            tauri::http::HeaderValue::from_static("*"),
        );
        resp
    };
    responder.respond(resp);
}

pub fn run() {
    std::hint::black_box(&PLUXY_BUILD_RECEIPT);
    #[cfg(any(feature = "native-test", feature = "artifact-test-trust"))]
    std::hint::black_box(&PLUXY_TEST_BUILD);
    let app = tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            connection_get,
            connection_set,
            core_restart,
            core_call,
            key_entry,
            sign_out,
            page_open,
            page_place,
            page_close,
            page_send,
            confirmation_request,
            confirmation_settings,
            device_list,
            device_registries,
            device_install,
            device_install_file,
            device_enable,
            device_remove,
            device_rollback,
            device_page_open,
        ])
        .register_asynchronous_uri_scheme_protocol(pages::SCHEME, page_scheme)
        .register_asynchronous_uri_scheme_protocol(device::ASSET_SCHEME, asset_scheme)
        .setup(|app| {
            let config = app.path().app_config_dir()?;
            #[cfg(target_os = "linux")]
            let confirmations =
                confirmation::Confirmations::open(config.join("remembered-launches.json"))?;
            let device_dirs = device::Dirs {
                root: app.path().app_data_dir()?.join("device-packages"),
                registries: config.join("device-registries.json"),
            };
            // `--safe-mode`: no page of a package on this device is listed, opened or served.
            let safe_mode = std::env::args().skip(1).any(|a| a == "--safe-mode");
            let catalog = device::Catalog::open(device_dirs.clone(), safe_mode);
            if let Some(w) = &catalog.warning {
                eprintln!("pluxy-app: {w}");
            }
            let core = app.path().app_data_dir()?.join("core");
            // When the app's core exits, the tray and the window show it. Off this thread: the
            // exit may come while a connection change holds the connection.
            let handle = app.handle().clone();
            let on_exit: managed::OnExit = std::sync::Arc::new(move || {
                let app = handle.clone();
                std::thread::spawn(move || {
                    if let Some(st) = app.try_state::<App>() {
                        let v = st.conn.lock().unwrap().view();
                        show_status(&app, &v);
                    }
                    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-connection", ());
                });
            });
            let conn = Connection::open(Dirs { config, core }, Some(on_exit));
            // The page webviews keep nothing from one run to the next.
            let pages_dir = app.path().app_cache_dir()?.join("pages");
            let _ = std::fs::remove_dir_all(&pages_dir);
            app.manage(App {
                conn: Mutex::new(conn),
                #[cfg(target_os = "linux")]
                confirmations: Mutex::new(confirmations),
                pages_dir,
                pages: Mutex::new(Pages::default()),
                closing: Mutex::new(Vec::new()),
                device: Mutex::new(catalog),
                device_dirs,
                status: Mutex::new(None),
                has_tray: AtomicBool::new(false),
                quitting: AtomicBool::new(false),
                key_entry_open: AtomicBool::new(false),
                key_entry_after: Mutex::new(None),
            });
            let handle = app.handle().clone();
            #[cfg(target_os = "linux")]
            if let Some(message) = handle
                .state::<App>()
                .confirmations
                .lock()
                .unwrap()
                .startup_warning
                .take()
            {
                confirmation::ui_notice(&handle, message);
            }
            match build_tray(&handle) {
                Ok(()) => handle.state::<App>().has_tray.store(true, Ordering::SeqCst),
                // No tray: closing the window quits the app.
                Err(e) => eprintln!("pluxy-app: no tray icon: {e}"),
            }
            let v = handle.state::<App>().conn.lock().unwrap().view();
            show_status(&handle, &v);
            if let Some(pid) = handle.state::<App>().conn.lock().unwrap().core_pid() {
                eprintln!("pluxy-app: core started, pid {pid}");
            }
            open_window(&handle)?;
            #[cfg(feature = "native-test")]
            native_test::start(handle);
            Ok(())
        })
        .on_window_event(|window, event| {
            if window.label() != WINDOW {
                return;
            }
            let app = window.app_handle();
            match event {
                WindowEvent::CloseRequested { .. }
                    if !app.state::<App>().has_tray.load(Ordering::SeqCst) =>
                {
                    quit(app);
                }
                WindowEvent::Destroyed => {
                    #[cfg(target_os = "linux")]
                    app.state::<App>()
                        .confirmations
                        .lock()
                        .unwrap()
                        .invalidate();
                    // Its webviews went with it.
                    app.state::<App>().pages.lock().unwrap().clear();
                    place::forget_all();
                    eprintln!("pluxy-app: window closed");
                }
                _ => {}
            }
        })
        .build(tauri::generate_context!())
        .expect("building the app");
    app.run(|app, event| match event {
        RunEvent::ExitRequested {
            code: None, api, ..
        } => {
            let st = app.state::<App>();
            // The window closed: stay in the tray.
            if st.has_tray.load(Ordering::SeqCst) && !st.quitting.load(Ordering::SeqCst) {
                api.prevent_exit();
            }
        }
        RunEvent::Exit => {
            if let Some(st) = app.try_state::<App>() {
                st.conn.lock().unwrap().stop_core();
            }
        }
        _ => {}
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Every command the app handles is in the manifest, and the window's capability grants
    /// each to the window's webview only: no window-wide grant (which would cover the page
    /// webviews inside the window), no remote address, nothing for any other webview.
    #[test]
    fn every_command_is_granted_to_the_window_only() {
        let cap: Value = serde_json::from_str(include_str!("../capabilities/main.json")).unwrap();
        assert_eq!(cap["webviews"], serde_json::json!([MAIN]));
        assert!(
            cap.get("windows").is_none(),
            "a window-wide grant covers page webviews"
        );
        assert!(cap.get("remote").is_none(), "remote content gets nothing");
        assert_eq!(cap["local"], true);
        let perms: Vec<&str> = cap["permissions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|p| p.as_str().unwrap())
            .collect();
        let mut want: Vec<String> = COMMANDS
            .iter()
            .map(|c| format!("allow-{}", c.replace('_', "-")))
            .collect();
        want.extend([
            "core:event:allow-listen".into(),
            "core:event:allow-unlisten".into(),
        ]);
        let mut got: Vec<String> = perms.iter().map(|p| p.to_string()).collect();
        want.sort();
        got.sort();
        assert_eq!(got, want);

        let conf: Value = serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        assert_eq!(
            conf["app"]["security"]["capabilities"],
            serde_json::json!(["main"])
        );
        assert_eq!(conf["app"]["withGlobalTauri"], false);
        let dir = std::fs::read_dir(concat!(env!("CARGO_MANIFEST_DIR"), "/capabilities")).unwrap();
        assert_eq!(dir.count(), 1, "one capability file, for the window");
    }

    #[test]
    fn the_command_list_matches_the_handler() {
        let src = include_str!("lib.rs");
        let start = src.find("generate_handler![").unwrap();
        let end = start + src[start..].find(']').unwrap();
        let mut handled: Vec<&str> = src[start + "generate_handler![".len()..end]
            .split(',')
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .collect();
        let mut listed = COMMANDS.to_vec();
        handled.sort();
        listed.sort();
        assert_eq!(handled, listed);
    }

    #[test]
    fn the_window_stays_on_the_app() {
        assert!(is_app_url(
            &Url::parse("tauri://localhost/index.html").unwrap()
        ));
        assert!(is_app_url(&Url::parse("http://tauri.localhost/").unwrap()));
        assert!(!is_app_url(
            &Url::parse("http://127.0.0.1:8400/panel/").unwrap()
        ));
        assert!(!is_app_url(&Url::parse("pluxy-page://localhost/").unwrap()));
    }
}
