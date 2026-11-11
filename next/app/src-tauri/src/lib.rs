//! The pluxy desktop app: one window showing the UI kit (`../src`), a native tray icon, and the
//! connection to core in one of three modes (`conn.rs`). Closing the window destroys it and its
//! webviews, so they hold no memory while the app sits in the tray; the tray opens a new one.
//!
//! Every app command is declared in the app's command manifest (`build.rs`) and granted only to
//! the window's webview, labelled `main` (`capabilities/main.json`). Extension pages run in
//! webviews of their own (`pages.rs`), which no capability names.

pub mod client;
mod command_names;
#[cfg(target_os = "linux")]
pub mod confirmation;
pub mod conn;
pub mod managed;
#[cfg(feature = "native-test")]
mod native_test;
pub mod pages;
mod place;
pub mod recovery;

pub use command_names::COMMANDS;

use conn::{Change, Connection, Dirs, Mode, View};
use pages::{Binding, Pages};
use serde::Deserialize;
use serde_json::Value;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
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
    status: Mutex<Option<MenuItem<Wry>>>,
    core_actions: Mutex<Vec<MenuItem<Wry>>>,
    has_tray: AtomicBool,
    quitting: AtomicBool,
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
        (Some(Mode::Managed), _, Some(c)) if c.state == "paused" => {
            "Core recovery paused — Start core to try again".into()
        }
        (Some(Mode::Managed), _, Some(c)) if c.state == "recovering" => {
            "Core stopped — waiting to restart".into()
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
    let managed = v.mode == Some(Mode::Managed);
    let running = v.core.as_ref().is_some_and(|c| c.state == "running");
    let recovering = v.core.as_ref().is_some_and(|c| c.state == "recovering");
    for item in app.state::<App>().core_actions.lock().unwrap().iter() {
        let enabled = match item.id().as_ref() {
            "core-start" => managed && !running,
            "core-stop" => managed && (running || recovering),
            _ => managed,
        };
        let _ = item.set_enabled(enabled);
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
    for l in labels {
        place::forget(&l);
        if let Some(w) = app.get_webview(&l) {
            let _ = w.close();
        }
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
    let start = MenuItem::with_id(app, "core-start", "Start core", false, None::<&str>)?;
    let stop = MenuItem::with_id(app, "core-stop", "Stop core", false, None::<&str>)?;
    let restart = MenuItem::with_id(app, "core-restart", "Restart core", false, None::<&str>)?;
    let menu = Menu::with_items(
        app,
        &[&status, &start, &stop, &restart, &sep, &open, &access, &quit_item],
    )?;
    let mut tray = TrayIconBuilder::with_id("pluxy")
        .tooltip("pluxy")
        .menu(&menu)
        .on_menu_event(|app, e| match e.id().as_ref() {
            "open" => {
                let _ = open_window(app);
            }
            "core-start" | "core-stop" | "core-restart" => {
                let app = app.clone();
                let action = e.id().as_ref().to_string();
                tauri::async_runtime::spawn_blocking(move || {
                    let st = app.state::<App>();
                    let mut c = st.conn.lock().unwrap();
                    if st.quitting.load(Ordering::SeqCst) || c.mode() != Some(Mode::Managed) {
                        return;
                    }
                    match action.as_str() {
                        "core-start" => {
                            let _ = c.resume_core();
                        }
                        "core-stop" => c.stop_core(),
                        _ => {
                            let _ = c.restart_core();
                        }
                    }
                    let v = c.view();
                    drop(c);
                    show_status(&app, &v);
                    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-connection", ());
                });
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
    *app.state::<App>().core_actions.lock().unwrap() = vec![start, stop, restart];
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
    with_conn(&webview, move |c| c.change(change)).await
}

#[tauri::command]
async fn core_restart(webview: Webview) -> Result<View, String> {
    with_conn(&webview, |c| c.restart_core()).await
}

#[tauri::command]
async fn sign_in(webview: Webview, key: String, remember: bool) -> Result<View, String> {
    with_conn(&webview, move |c| c.sign_in(&key, remember)).await
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
    state: tauri::State<App>,
    extension: String,
    page: String,
    title: String,
    url: String,
    pages_origin: String,
    rect: Rect,
) -> Result<String, String> {
    trusted(&webview)?;
    let url = pages::page_url(&url, &pages_origin)?;
    let origin = url.origin();
    let label = state.pages.lock().unwrap().bind(Binding {
        extension,
        page,
        title,
        url,
        pages_origin,
    })?;
    let app_doc = page_scheme_url();
    let builder = WebviewBuilder::new(&label, WebviewUrl::CustomProtocol(app_doc.clone()))
        // A web context of their own, apart from the window's storage and cookies. Not
        // `incognito`: on Linux, Tauri then gives the webview no custom schemes at all.
        .data_directory(state.pages_dir.clone())
        .devtools(false)
        // The app's document at the top, the page's site in its frame; nowhere else.
        .on_navigation(move |u| *u == app_doc || u.origin() == origin)
        .on_new_window(|_, _| NewWindowResponse::Deny);
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
    if let Err(e) = placed {
        state.pages.lock().unwrap().unbind(&label);
        if let Some(w) = webview.app_handle().get_webview(&label) {
            let _ = w.close();
        }
        return Err(e);
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
    let w = page_webview(webview.app_handle(), &label);
    state.pages.lock().unwrap().unbind(&label);
    place::forget(&label);
    w?.close().map_err(|e| e.to_string())
}

/// Delivers a message of the window's channel to a page webview's app document.
#[tauri::command]
fn page_send(webview: Webview, label: String, msg: Value) -> Result<(), String> {
    trusted(&webview)?;
    let js = pages::delivery(&msg)?;
    page_webview(webview.app_handle(), &label)?
        .eval(js)
        .map_err(|e| e.to_string())
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
        let pages = st.pages.lock().unwrap();
        pages::serve(
            &pages,
            &label,
            request.method().as_str(),
            request.uri().path(),
            request.body(),
        )
    };
    if let Some(m) = msg {
        #[cfg(all(target_os = "linux", feature = "native-test"))]
        if m.kind == "call" && m.data.get("id").and_then(Value::as_str) == Some("spoof-confirm") {
            confirmation::native::PAGE_CLAIMS.fetch_add(1, Ordering::SeqCst);
        }
        let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-page", m);
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
    responder.respond(resp);
}

pub fn run() {
    let app = tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![
            connection_get,
            connection_set,
            core_restart,
            core_call,
            sign_in,
            sign_out,
            page_open,
            page_place,
            page_close,
            page_send,
            confirmation_request,
            confirmation_settings,
        ])
        .register_asynchronous_uri_scheme_protocol(pages::SCHEME, page_scheme)
        .setup(|app| {
            let config = app.path().app_config_dir()?;
            #[cfg(target_os = "linux")]
            let confirmations =
                confirmation::Confirmations::open(config.join("remembered-launches.json"))?;
            let core = app.path().app_data_dir()?.join("core");
            // When the app's core exits, the tray and the window show it. Off this thread: the
            // exit may come while a connection change holds the connection.
            let handle = app.handle().clone();
            let on_exit: managed::OnExit = std::sync::Arc::new(move || {
                let app = handle.clone();
                std::thread::spawn(move || {
                    if let Some(st) = app.try_state::<App>() {
                        let mut c = st.conn.lock().unwrap();
                        let plan = c.core_exited();
                        let v = c.view();
                        drop(c);
                        show_status(&app, &v);
                        recovery::schedule(app.clone(), plan);
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
                status: Mutex::new(None),
                core_actions: Mutex::new(Vec::new()),
                has_tray: AtomicBool::new(false),
                quitting: AtomicBool::new(false),
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
