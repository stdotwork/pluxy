//! The pluxy desktop app: one window showing the UI kit (`../src`), a native tray icon, and the
//! connection to core in one of three modes (`conn.rs`). Closing the window asks, quits or
//! destroys it and its webviews, as the person chose (`window.rs`), so they hold no memory while
//! the app sits in the tray; the tray opens a new one.
//!
//! Every app command is declared in the app's command manifest (`build.rs`) and granted only to
//! the window's webview, labelled `main` (`capabilities/main.json`). Extension pages run in
//! webviews of their own (`pages.rs`), which no capability names.

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
mod key_entry;
pub mod managed;
#[cfg(feature = "native-test")]
mod native_test;
pub mod pages;
mod place;
pub mod recovery;
#[cfg(debug_assertions)]
pub mod release_config;
mod startup;
pub mod trust;
pub mod window;

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
    startup: Mutex<(startup::Store, startup::Preferences)>,
    conn: Mutex<Connection>,
    #[cfg(target_os = "linux")]
    confirmations: Mutex<confirmation::Confirmations>,
    /// The page webviews' own web context's folder: nothing shared with the window's.
    pages_dir: std::path::PathBuf,
    pages: Mutex<Pages>,
    status: Mutex<Option<MenuItem<Wry>>>,
    /// Start, Stop and Restart, in that order.
    core_actions: Mutex<Vec<MenuItem<Wry>>>,
    /// The newest view the tray shows. Only the main thread touches it.
    shown: Mutex<Shown>,
    /// A tray Start, Stop or Restart is running; the tray refuses another until it ends.
    busy: AtomicBool,
    /// What closing the window does, and its size and place (`window.rs`).
    window: Mutex<window::Store>,
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

/// The newest view given to the tray. Views come from several threads; one that arrives after
/// a newer one is dropped.
#[derive(Default)]
struct Shown {
    view: Option<View>,
}

impl Shown {
    fn offer(&mut self, v: View) -> bool {
        if self
            .view
            .as_ref()
            .is_some_and(|old| old.version > v.version)
        {
            return false;
        }
        self.view = Some(v);
        true
    }
}

const CORE_ACTIONS: [&str; 3] = ["core-start", "core-stop", "core-restart"];

/// The tray's status line, and whether Start, Stop and Restart are enabled. While a tray action
/// runs, all three are off.
fn tray_presentation(v: &View, busy: bool) -> (String, [bool; 3]) {
    let managed = v.mode == Some(Mode::Managed);
    if managed && busy {
        return ("Core working…".into(), [false; 3]);
    }
    let state = v.core.as_ref().map(|c| c.state);
    let running = state == Some("running");
    let recovering = state == Some("recovering");
    (
        status_text(v),
        [
            managed && !running,
            managed && (running || recovering),
            managed && running,
        ],
    )
}

/// Shows v in the tray, unless it already shows a newer view. The update runs on the main
/// thread, so two updates never interleave.
fn show_status(app: &AppHandle, v: &View) {
    let handle = app.clone();
    let v = v.clone();
    let _ = app.run_on_main_thread(move || {
        let st = handle.state::<App>();
        let mut shown = st.shown.lock().unwrap();
        if shown.offer(v) {
            apply_tray(&handle, &shown);
        }
    });
}

/// Shows the tray's newest view again, as the busy flag now says.
fn refresh_tray(app: &AppHandle) {
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        let st = handle.state::<App>();
        let shown = st.shown.lock().unwrap();
        apply_tray(&handle, &shown);
    });
}

fn apply_tray(app: &AppHandle, shown: &Shown) {
    let Some(v) = shown.view.as_ref() else {
        return;
    };
    let st = app.state::<App>();
    let (text, enabled) = tray_presentation(v, st.busy.load(Ordering::SeqCst));
    if let Some(item) = st.status.lock().unwrap().as_ref() {
        let _ = item.set_text(text);
    }
    for item in st.core_actions.lock().unwrap().iter() {
        if let Some(i) = CORE_ACTIONS.iter().position(|a| item.id().as_ref() == *a) {
            let _ = item.set_enabled(enabled[i]);
        }
    }
}

/// A tray Start, Stop or Restart, one at a time. It acts only if its item would be enabled now,
/// so a click on a disabled item (a desktop may still deliver one) changes nothing.
fn tray_action(app: &AppHandle, action: &str) {
    let Some(index) = CORE_ACTIONS.iter().position(|a| *a == action) else {
        return;
    };
    let st = app.state::<App>();
    if st
        .busy
        .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
        .is_err()
    {
        // Refused without reading the connection: this runs on the main thread, and the running
        // action may hold the connection for seconds.
        tray_refused(action, None, "busy");
        return;
    }
    refresh_tray(app);
    let app = app.clone();
    let action = action.to_string();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(feature = "native-test")]
        native_test::tray_hold(&action);
        let st = app.state::<App>();
        let mut c = st.conn.lock().unwrap();
        let before = c.view();
        if st.quitting.load(Ordering::SeqCst) {
            tray_refused(&action, before.mode, "quitting");
        } else if !tray_presentation(&before, false).1[index] {
            tray_refused(&action, before.mode, "disabled");
        } else {
            #[cfg(feature = "native-test")]
            native_test::log(
                "tray-action",
                serde_json::json!({"action": action, "mode": before.mode}),
            );
            match index {
                0 => {
                    let _ = c.resume_core();
                }
                1 => c.stop_core(),
                _ => {
                    let _ = c.restart_core();
                }
            }
        }
        let v = c.view();
        drop(c);
        st.busy.store(false, Ordering::SeqCst);
        show_status(&app, &v);
        // A newer view (from the core's exit, say) may have reached the tray while busy was set,
        // so v is dropped as older: draw the newest view again without the busy state.
        refresh_tray(&app);
        let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-connection", ());
    });
}

fn tray_refused(action: &str, mode: Option<Mode>, reason: &str) {
    #[cfg(feature = "native-test")]
    native_test::log(
        "tray-refused",
        serde_json::json!({"action": action, "mode": mode, "reason": reason}),
    );
    let _ = (action, mode, reason);
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
    let saved = app.state::<App>().window.lock().unwrap().prefs.geometry;
    let (width, height, place) = window::opening(app, saved);
    let mut builder = WindowBuilder::new(app, WINDOW)
        .title("pluxy")
        .inner_size(width, height)
        .min_inner_size(window::MIN_SIZE.0, window::MIN_SIZE.1);
    if let Some((x, y)) = place {
        builder = builder.position(x, y);
    }
    let w = builder.build()?;
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
    remember_window(app);
    #[cfg(target_os = "linux")]
    st.confirmations.lock().unwrap().invalidate();
    // Off the calling thread, which may be the main one: a recovery or a tray action can hold
    // the connection for seconds while a core starts or stops. Tray actions refuse to start now.
    let app = app.clone();
    tauri::async_runtime::spawn_blocking(move || {
        app.state::<App>().conn.lock().unwrap().stop_core();
        app.exit(0);
    });
}

/// Saves the window's size and place, as it is now if it is open.
fn remember_window(app: &AppHandle) {
    let now = app.get_window(WINDOW).and_then(|w| window::current(&w));
    let st = app.state::<App>();
    let mut store = st.window.lock().unwrap();
    if now.is_some() {
        store.prefs.geometry = now;
    }
    if let Err(e) = store.save() {
        eprintln!("pluxy-app: {e}");
    }
}

/// Whether closing the window can leave the app in the tray: it has a tray icon, and the desktop
/// a host that shows it.
fn usable_tray(app: &AppHandle) -> Result<(), String> {
    if !app.state::<App>().has_tray.load(Ordering::SeqCst) {
        return Err("the app has no tray icon".into());
    }
    window::tray_host()
}

/// Closes the window to the tray: the window and its webviews are destroyed, not hidden.
fn close_to_tray(app: &AppHandle) {
    remember_window(app);
    if let Some(w) = app.get_window(WINDOW) {
        let _ = w.destroy();
    }
}

/// The person closed the window: do what they chose. Off the window's thread, since asking the
/// desktop about its tray waits on D-Bus.
fn close_requested(app: &AppHandle) {
    let app = app.clone();
    std::thread::spawn(move || {
        let choice = app
            .state::<App>()
            .window
            .lock()
            .unwrap()
            .prefs
            .close_behavior;
        match (usable_tray(&app), choice) {
            (Err(e), _) => {
                eprintln!("pluxy-app: no usable tray ({e}): closing the window quits");
                quit(&app);
            }
            (Ok(()), window::Close::Exit) => quit(&app),
            (Ok(()), window::Close::MinimizeToTray) => close_to_tray(&app),
            (Ok(()), window::Close::Ask) => ask_on_close(&app),
        }
    });
}

/// Asks the window what closing should do (src/window.tsx answers with `window_close`). A
/// window that cannot answer (its page is still loading, or busy) closes to the tray, from where
/// it can be opened again.
fn ask_on_close(app: &AppHandle) {
    #[cfg(feature = "native-test")]
    native_test::window_prompted();
    let (tx, rx) = std::sync::mpsc::channel();
    let asked = app.get_webview(MAIN).is_some_and(|main| {
        main.eval_with_callback("window.__pluxyClosePrompt?.() === true", move |r| {
            let _ = tx.send(r == "true");
        })
        .is_ok()
    }) && rx.recv_timeout(std::time::Duration::from_secs(3)) == Ok(true);
    if !asked {
        eprintln!("pluxy-app: the window did not answer the close request: closing to the tray");
        close_to_tray(app);
    }
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
        &[
            &status, &start, &stop, &restart, &sep, &open, &access, &quit_item,
        ],
    )?;
    let mut tray = TrayIconBuilder::with_id("pluxy")
        .tooltip("pluxy")
        .menu(&menu)
        .on_menu_event(|app, e| match e.id().as_ref() {
            "open" => {
                let _ = open_window(app);
            }
            "core-start" | "core-stop" | "core-restart" => tray_action(app, e.id().as_ref()),
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

fn window_view(app: &AppHandle) -> window::View {
    window::View {
        close_behavior: app
            .state::<App>()
            .window
            .lock()
            .unwrap()
            .prefs
            .close_behavior,
        tray: usable_tray(app).is_ok(),
    }
}

#[tauri::command]
async fn window_get(webview: Webview) -> Result<window::View, String> {
    trusted(&webview)?;
    Ok(window_view(webview.app_handle()))
}

#[tauri::command]
async fn window_set(
    webview: Webview,
    close_behavior: window::Close,
) -> Result<window::View, String> {
    trusted(&webview)?;
    let app = webview.app_handle();
    {
        let st = app.state::<App>();
        let mut store = st.window.lock().unwrap();
        let before = store.prefs.close_behavior;
        store.prefs.close_behavior = close_behavior;
        if let Err(e) = store.save() {
            store.prefs.close_behavior = before;
            return Err(e);
        }
    }
    Ok(window_view(app))
}

/// The window's answer to "What would you like to do?": Exit App or Minimize to Tray, and
/// whether to remember it.
#[tauri::command]
async fn window_close(
    webview: Webview,
    action: window::Close,
    remember: bool,
) -> Result<(), String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    match action {
        window::Close::Ask => return Err("choose Exit App or Minimize to Tray".into()),
        window::Close::MinimizeToTray => usable_tray(&app).map_err(|e| {
            format!("The system tray is unavailable ({e}); keep the window open or exit the app")
        })?,
        window::Close::Exit => {}
    }
    if remember {
        let st = app.state::<App>();
        let mut store = st.window.lock().unwrap();
        let before = store.prefs.close_behavior;
        store.prefs.close_behavior = action;
        if let Err(e) = store.save() {
            store.prefs.close_behavior = before;
            return Err(e);
        }
    }
    if action == window::Close::Exit {
        std::thread::spawn(move || quit(&app));
    } else {
        close_to_tray(&app);
    }
    Ok(())
}

#[tauri::command]
fn connection_get(webview: Webview, state: tauri::State<App>) -> Result<View, String> {
    trusted(&webview)?;
    Ok(state.conn.lock().unwrap().view())
}

#[tauri::command]
fn startup_get(webview: Webview, state: tauri::State<App>) -> Result<startup::View, String> {
    trusted(&webview)?;
    let startup = state.startup.lock().unwrap();
    startup.0.view(startup.1)
}

#[tauri::command]
fn startup_set(
    webview: Webview,
    state: tauri::State<App>,
    preferences: startup::Input,
) -> Result<startup::View, String> {
    trusted(&webview)?;
    let mut startup = state.startup.lock().unwrap();
    let preferences = preferences.into();
    let view = startup.0.save(preferences, || {
        #[cfg(target_os = "linux")]
        let appimage = webview.app_handle().env().appimage;
        #[cfg(not(target_os = "linux"))]
        let appimage = None;
        startup::executable(appimage, std::env::current_exe)
    })?;
    startup.1 = view.preferences;
    Ok(view)
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
        // Checked under the lock, as tray actions do: once Quit has begun, nothing the window
        // asks for may start a core that would outlive the app.
        let r = if st.quitting.load(Ordering::SeqCst) {
            Err("the app is quitting".to_string())
        } else {
            f(&mut c)
        };
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

/// Starts the app's own core only if it is not running, as the tray's Start does: a click that
/// races a recovery leaves the recovered core alone.
#[tauri::command]
async fn core_start(webview: Webview) -> Result<View, String> {
    with_conn(&webview, |c| c.resume_core()).await
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
    #[cfg(feature = "native-test")]
    native_test::window_call(&method, &path);
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
            *u == app_doc || u.origin() == origin
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
            startup_get,
            startup_set,
            connection_get,
            connection_set,
            core_start,
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
            window_get,
            window_set,
            window_close,
        ])
        .register_asynchronous_uri_scheme_protocol(pages::SCHEME, page_scheme)
        .setup(|app| {
            let config = app.path().app_config_dir()?;
            let startup_store = startup::Store::new(
                config.clone(),
                app.path().config_dir()?,
                app.config().identifier.clone(),
            );
            let preferences = startup_store.load();
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
                    #[cfg(feature = "native-test")]
                    native_test::core_exit_seen();
                    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-connection", ());
                });
            });
            let window_prefs = window::Store::open(config.join("window.json"));
            let conn = Connection::open_at_startup(
                Dirs { config, core },
                Some(on_exit),
                preferences.start_core_on_launch,
            );
            #[cfg(feature = "native-test")]
            native_test::hold_for_early_exit();
            // The page webviews keep nothing from one run to the next.
            let pages_dir = app.path().app_cache_dir()?.join("pages");
            let _ = std::fs::remove_dir_all(&pages_dir);
            app.manage(App {
                startup: Mutex::new((startup_store, preferences)),
                conn: Mutex::new(conn),
                #[cfg(target_os = "linux")]
                confirmations: Mutex::new(confirmations),
                pages_dir,
                pages: Mutex::new(Pages::default()),
                status: Mutex::new(None),
                core_actions: Mutex::new(Vec::new()),
                shown: Mutex::new(Shown::default()),
                busy: AtomicBool::new(false),
                window: Mutex::new(window_prefs),
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
            // A core that exited before the app state existed had no one to recover it.
            let (plan, v) = {
                let st = handle.state::<App>();
                let mut c = st.conn.lock().unwrap();
                (c.core_exited(), c.view())
            };
            show_status(&handle, &v);
            recovery::schedule(handle.clone(), plan);
            if let Some(pid) = handle.state::<App>().conn.lock().unwrap().core_pid() {
                eprintln!("pluxy-app: core started, pid {pid}");
            }
            let managed = handle.state::<App>().conn.lock().unwrap().mode() == Some(Mode::Managed);
            let silent = startup::wants_silent(
                preferences,
                std::env::args_os().any(|a| a == "--autostart"),
                managed,
            );
            let usable_tray = silent
                && handle.state::<App>().has_tray.load(Ordering::SeqCst)
                && match startup::tray_host() {
                    Ok(()) => true,
                    Err(e) => {
                        eprintln!("pluxy-app: opening startup window: {e}");
                        false
                    }
                };
            if !silent || !usable_tray {
                open_window(&handle)?;
            }
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
                WindowEvent::CloseRequested { api, .. }
                    if !app.state::<App>().quitting.load(Ordering::SeqCst) =>
                {
                    api.prevent_close();
                    close_requested(app);
                }
                WindowEvent::Moved(_) | WindowEvent::Resized(_) => {
                    if let Some(g) = window::current(window) {
                        app.state::<App>().window.lock().unwrap().prefs.geometry = Some(g);
                    }
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

    fn managed(state: &'static str, version: u64) -> View {
        View {
            mode: Some(Mode::Managed),
            core: Some(conn::CoreView {
                state,
                pid: None,
                detail: None,
            }),
            version,
            ..View::default()
        }
    }

    /// Start, Stop and Restart as the old app enabled them: Restart only while running, none
    /// while a tray action runs, none outside managed mode.
    #[test]
    fn tray_actions_follow_the_core_state() {
        for (state, want) in [
            ("running", [false, true, true]),
            ("stopped", [true, false, false]),
            ("recovering", [true, true, false]),
            ("paused", [true, false, false]),
        ] {
            assert_eq!(
                tray_presentation(&managed(state, 1), false).1,
                want,
                "{state}"
            );
            assert_eq!(
                tray_presentation(&managed(state, 1), true),
                ("Core working…".to_string(), [false; 3]),
                "{state} while busy"
            );
        }
        for mode in [Mode::Local, Mode::Remote] {
            let v = View {
                mode: Some(mode),
                ..View::default()
            };
            assert_eq!(tray_presentation(&v, false).1, [false; 3]);
            assert_eq!(tray_presentation(&v, true).1, [false; 3]);
        }
    }

    #[test]
    fn the_tray_drops_a_view_older_than_the_one_it_shows() {
        let mut shown = Shown::default();
        assert!(shown.offer(managed("running", 2)));
        assert!(!shown.offer(managed("stopped", 1)));
        assert_eq!(
            shown.view.as_ref().unwrap().core.as_ref().unwrap().state,
            "running"
        );
        assert!(shown.offer(managed("stopped", 3)));
        assert_eq!(
            shown.view.as_ref().unwrap().core.as_ref().unwrap().state,
            "stopped"
        );
    }

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
