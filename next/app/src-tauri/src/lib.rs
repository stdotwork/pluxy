//! The pluxy desktop app: one window showing the UI kit (`../src`), a native tray icon, and the
//! connection to core in one of three modes (`conn.rs`). Closing the window asks, quits or
//! destroys it and its webviews, as the person chose (`window.rs`), so they hold no memory while
//! the app sits in the tray; the tray opens a new one.
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

pub mod access;
pub mod artifact;
pub mod client;
mod command_names;
#[cfg(target_os = "linux")]
pub mod confirmation;
pub mod conn;
pub mod device;
pub mod instance;
mod key_entry;
pub mod managed;
#[cfg(feature = "native-test")]
mod native_test;
pub mod pages;
mod place;
mod privacy;
#[cfg(target_os = "linux")]
pub mod program;
pub mod recovery;
#[cfg(debug_assertions)]
pub mod release_config;
#[cfg(target_os = "linux")]
pub mod route;
mod startup;
pub mod trust;
#[cfg(target_os = "linux")]
pub mod vault;
pub mod window;

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
    startup: Mutex<(startup::Store, startup::Preferences)>,
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
    /// Access: approved signers and the files and folders granted to device packages. Lock it
    /// after `device`, never before.
    #[cfg(unix)]
    access: Mutex<access::Store>,
    /// Notified when a read through Access finishes, for a revocation waiting on it.
    #[cfg(unix)]
    access_done: std::sync::Condvar,
    /// The admitted native tools that run, on demand (`program.rs`).
    #[cfg(target_os = "linux")]
    programs: program::Host,
    /// Page calls of native operations in flight (`route.rs`).
    #[cfg(target_os = "linux")]
    routes: route::Routes,
    /// Secrets the person entered natively, references to them, and tools' backups and journals
    /// (`vault.rs`).
    #[cfg(target_os = "linux")]
    vault: std::sync::Arc<vault::Vault>,
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
    rotation_open: AtomicBool,
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
    // Their calls of native operations in flight are cancelled, their answers not delivered.
    #[cfg(target_os = "linux")]
    for l in &labels {
        app.state::<App>().routes.page_closed(l);
    }
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

/// Shows the window (a new one if it was closed), for a later launch of the app on this profile,
/// and says whether it did. On the window's own thread, where the tray's "Open pluxy" runs too.
fn raise_window(app: &AppHandle) -> bool {
    let (tx, rx) = std::sync::mpsc::channel();
    let a = app.clone();
    let queued = app.run_on_main_thread(move || {
        let _ = tx.send(open_window(&a).is_ok());
    });
    queued.is_ok() && rx.recv_timeout(std::time::Duration::from_secs(15)) == Ok(true)
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
        #[cfg(target_os = "linux")]
        app.state::<App>().programs.stop_all();
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

fn privacy_file(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    Ok(app
        .path()
        .app_config_dir()
        .map_err(|e| e.to_string())?
        .join(privacy::FILE))
}

/// Whether privacy mode is on (`privacy.rs`).
#[tauri::command]
async fn privacy_get(webview: Webview) -> Result<bool, String> {
    trusted(&webview)?;
    Ok(privacy::load(&privacy_file(webview.app_handle())?))
}

/// Saves privacy mode; only the window's own toggle calls it.
#[tauri::command]
async fn privacy_set(webview: Webview, on: bool) -> Result<bool, String> {
    trusted(&webview)?;
    // One write at a time, as window_set writes under its store's lock.
    static SAVING: Mutex<()> = Mutex::new(());
    let _saving = SAVING.lock().unwrap();
    privacy::save(&privacy_file(webview.app_handle())?, on)?;
    Ok(on)
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
    if client::native_only(&path) {
        return Ok(client::Reply::failed(
            403,
            "native_only",
            "the management key is rotated only by the app itself: use Rotate on the Config page",
        ));
    }
    let caller = state.conn.lock().unwrap().caller();
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        caller.call_or_retry(&method, &path, body.as_ref(), || {
            app.state::<App>().conn.lock().unwrap().caller()
        })
    })
    .await
    .map_err(|e| e.to_string())
}

/// Rotates the management key natively: the person confirms in the native confirmation, the app
/// calls core's rotation route itself, keeps the new key as the old one was kept and shows it
/// once in a native window. The window gets only how it went and how the key is kept: 200 with
/// `kept`, plus an error when it could not be saved; 204 when the person cancelled.
#[tauri::command]
async fn management_key_rotate(webview: Webview) -> Result<client::Reply, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    let st = app.state::<App>();
    if st.key_entry_open.load(Ordering::SeqCst) {
        return Err("finish native key entry first".into());
    }
    if st.rotation_open.swap(true, Ordering::SeqCst) {
        return Err("a rotation is already in progress".into());
    }
    let handle = app.clone();
    tauri::async_runtime::spawn_blocking(move || {
        struct Pending(AppHandle);
        impl Drop for Pending {
            fn drop(&mut self) {
                self.0
                    .state::<App>()
                    .rotation_open
                    .store(false, Ordering::SeqCst);
            }
        }
        let _pending = Pending(handle.clone());
        rotate(&handle)
    })
    .await
    .map_err(|_| "native rotation failed".into())
}

#[cfg(target_os = "linux")]
fn rotate(app: &AppHandle) -> client::Reply {
    let st = app.state::<App>();
    let prepared = st.conn.lock().unwrap().prepare_rotation();
    let target = match prepared {
        Ok(target) => target,
        Err(reply) => return reply,
    };
    match confirmation::confirm_app(app, target.summary.clone(), target.revision()) {
        Ok(true) => {}
        Ok(false) => {
            return client::Reply {
                status: 204,
                body: None,
                error: None,
            }
        }
        Err(e) => return client::Reply::failed(409, "not_confirmed", e),
    }
    let mut c = st.conn.lock().unwrap();
    let result = c.finish_rotation(target);
    let view = c.view();
    drop(c);
    show_status(app, &view);
    match result {
        Err(reply) => reply,
        Ok(rotation) => {
            let error = match &rotation.kept {
                conn::Kept::NotSaved(why) => Some(client::ApiError {
                    code: "not_saved".into(),
                    message: why.clone(),
                }),
                _ => None,
            };
            let body = serde_json::json!({"kept": rotation.kept.name()});
            key_entry::reveal(app, rotation.key, rotation.kept.notice());
            client::Reply {
                status: 200,
                body: Some(body),
                error,
            }
        }
    }
}

#[cfg(not(target_os = "linux"))]
fn rotate(_: &AppHandle) -> client::Reply {
    client::Reply::failed(
        501,
        "unsupported",
        "native key rotation is currently supported on Linux only",
    )
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
    #[cfg(feature = "native-test")]
    let sent = msg.clone();
    let result = target
        .eval_with_callback(js, move |result| {
            #[cfg(feature = "native-test")]
            if result == "true" {
                native_test::privacy::delivered(&sent);
            }
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
    Ok(device_view(&state, &state.device.lock().unwrap()))
}

/// The catalog as the window shows it: trust from the current roots, and each signer's
/// approval from Access.
fn device_view(st: &App, catalog: &device::Catalog) -> device::View {
    let roots = trust::TrustStore::current();
    #[cfg(unix)]
    {
        let access = st.access.lock().unwrap();
        catalog.view_with(&roots, &|s| access.approved(s))
    }
    #[cfg(not(unix))]
    {
        let _ = st;
        catalog.view(&roots)
    }
}

/// What the registries in `device-registries.json` offer this computer.
#[tauri::command]
async fn device_registries(webview: Webview) -> Result<Vec<device::RegistryView>, String> {
    trusted(&webview)?;
    let dirs = webview.state::<App>().device_dirs.clone();
    tauri::async_runtime::spawn_blocking(move || {
        device::offers(&dirs, &trust::TrustStore::current())
    })
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
    device_changed_as(app, id, f, true)
}

/// `device_changed`; with stop_tool false, it leaves the package's native tool to f (a swap).
fn device_changed_as(
    app: &AppHandle,
    id: &str,
    f: impl FnOnce(&mut device::Catalog) -> Result<(), String>,
    stop_tool: bool,
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
        (labels, device_view(&st, &catalog), left)
    };
    left.extend(close_webviews(app, labels));
    // A native tool runs only as the release it was admitted as: any change stops it, and its
    // next call starts it from its record then, if it still has one.
    #[cfg(target_os = "linux")]
    if stop_tool {
        st.programs.stop(id);
    }
    #[cfg(not(target_os = "linux"))]
    let _ = stop_tool;
    let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-device", ());
    if left.is_empty() {
        Ok(view)
    } else {
        Err(format!("{id} was changed, but {}", left.join("; ")))
    }
}

/// Commits a staged candidate and closes the package's open pages. An update of a native tool
/// the person enabled is a swap (`native_swap`).
fn device_commit(app: &AppHandle, c: device::Candidate) -> Result<device::View, String> {
    let id = c.id.clone();
    #[cfg(target_os = "linux")]
    if app
        .state::<App>()
        .device
        .lock()
        .unwrap()
        .swaps(&id, &c.release)
    {
        return native_swap(app, &id, |catalog, roots| catalog.swap_update(c, roots));
    }
    device_changed(app, &id, |catalog| catalog.commit(c))
}

/// Replaces enabled native tool id's active release by the one stage gives (an update, or the
/// last good release) while the tool serves on. The person confirms the new release in a native
/// confirmation; the program host starts and describes it, with no call; only then is it
/// committed and does it serve, while the old release's tool drains (`program::Host::switch`).
/// A cancel or a failure leaves the active release enabled and serving, as it was.
#[cfg(target_os = "linux")]
fn native_swap(
    app: &AppHandle,
    id: &str,
    stage: impl FnOnce(&mut device::Catalog, &trust::TrustStore) -> Result<device::Swap, String>,
) -> Result<device::View, String> {
    let st = app.state::<App>();
    let roots = trust::TrustStore::current();
    // The tool's stop count as it is staged: a change to the package since (disable, remove)
    // stopped it, and the swap's release does not start.
    let (swap, stops) = {
        let mut catalog = st.device.lock().unwrap();
        let swap = stage(&mut catalog, &roots)?;
        (swap, st.programs.stops(id))
    };
    let (from, to) = (
        swap.from_version().to_string(),
        swap.admitted().version().to_string(),
    );
    let stays = |e: String| format!("{e}; {id} {from} stays");
    let prepared = (|| {
        // Its files are hashed without holding the catalog, before and after the confirmation.
        swap.verify()?;
        let mut action = swap.admitted().confirmation();
        action.what = format!(
            "Update native tool {id} from {from} to {to}; {from} serves until {to} is ready. {}",
            action.what
        );
        if !ask_native(app, action, id)? {
            return Err("the update was cancelled".to_string());
        }
        swap.verify()?;
        // The package may have been disabled, removed or changed while the confirmation was open.
        st.device.lock().unwrap().swap_current(&swap, &roots)?;
        // On a thread of its own that ends with the start: the tool is held by the host's
        // supervisor thread, never by the thread that started it.
        std::thread::scope(|s| {
            std::thread::Builder::new()
                .name("pluxy-prog-swap".into())
                .spawn_scoped(s, || st.programs.prepare(swap.admitted(), stops))
                .map_err(|e| e.to_string())?
                .join()
                .map_err(|_| "the swap's thread failed".to_string())?
                .map_err(|e| e.message)
        })
    })();
    let prepared = match prepared {
        Ok(p) => p,
        Err(e) => {
            st.device.lock().unwrap().end_swap(swap);
            return Err(stays(e));
        }
    };
    device_changed_as(
        app,
        id,
        |catalog| {
            catalog.commit_swap(swap, &roots).map_err(stays)?;
            // Stopped since it was described: nothing of the old release may serve on.
            if st.programs.switch(prepared).is_err() {
                st.programs.stop(id);
            }
            Ok(())
        },
        false,
    )
}

/// Asks the person to confirm what in a native confirmation, which the window cannot answer
/// for them, and waits for the outcome: true once they confirmed (its single-use token spent),
/// false if they cancelled.
#[cfg(target_os = "linux")]
fn ask_native(app: &AppHandle, what: confirmation::Action, id: &str) -> Result<bool, String> {
    let st = app.state::<App>();
    let action = {
        let mut c = st.confirmations.lock().unwrap();
        let prepared = c.prepare_main(what)?;
        c.queue(prepared)?
    };
    confirmation::request(app, action.clone())?;
    // The dialog's own handler leaves the outcome for native code; it expires after a minute.
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(75);
    let outcome = loop {
        if let Some(r) = st.confirmations.lock().unwrap().take_result(&action) {
            break r;
        }
        if std::time::Instant::now() > deadline {
            return Err(format!("the native confirmation for {id} expired"));
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    };
    let Some(token) = outcome? else {
        return Ok(false);
    };
    // Spends the single-use token and checks the program file is the one shown.
    st.confirmations.lock().unwrap().consume(&action, &token)?;
    Ok(true)
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
        let c = device::stage_from_registry(
            &dirs,
            &trust::TrustStore::current(),
            &registry,
            &id,
            version.as_deref(),
        )?;
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
            &trust::TrustStore::current(),
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

/// Enables or disables package id. Enabling a native tool asks the person in a native
/// confirmation, which the window cannot answer for them, and waits for its outcome.
#[tauri::command]
async fn device_enable(
    webview: Webview,
    id: String,
    enabled: bool,
) -> Result<device::View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let native = enabled
            && app
                .state::<App>()
                .device
                .lock()
                .unwrap()
                .get(&id)
                .is_some_and(|p| p.active.tier == "native");
        if native {
            return native_enable(&app, &id);
        }
        device_changed(&app, &id, |c| {
            c.set_enabled(&id, enabled, &trust::TrustStore::current())
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Enables native tool id once the person confirmed it in a native confirmation that shows
/// its ID, source, digest, signer and full access to their user account. The catalog admits
/// it only if it is still the release that was shown.
#[cfg(target_os = "linux")]
fn native_enable(app: &AppHandle, id: &str) -> Result<device::View, String> {
    let st = app.state::<App>();
    // Its files are hashed without holding the catalog (`checked_unlocked`).
    let candidate =
        device::native_candidate_unlocked(&st.device, id, &trust::TrustStore::current())?;
    match ask_native(app, candidate.confirmation(), id) {
        Ok(true) => {}
        Ok(false) => return Err(format!("native enable cancelled; {id} stays disabled")),
        Err(e) => return Err(format!("{e}; {id} stays disabled")),
    }
    // Its files once more, then the record under the catalog's lock: still the one shown.
    let now = device::native_candidate_unlocked(&st.device, id, &trust::TrustStore::current())?;
    if now != candidate {
        return Err(format!(
            "{id} changed while it was being confirmed; enable it again"
        ));
    }
    device_changed(app, id, |c| {
        c.admit_checked(&candidate, &trust::TrustStore::current())
    })
}

#[cfg(not(target_os = "linux"))]
fn native_enable(_app: &AppHandle, id: &str) -> Result<device::View, String> {
    Err(format!(
        "{id} is a native tool; native confirmations are supported on Linux only"
    ))
}

/// Allows web package id's native imports, once the person confirmed them in a native
/// confirmation listing each operation and its effect, or revokes them, cancelling its pages'
/// calls in flight.
#[tauri::command]
async fn device_imports(
    webview: Webview,
    id: String,
    allowed: bool,
) -> Result<device::View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(target_os = "linux")]
        {
            let st = app.state::<App>();
            let roots = trust::TrustStore::current();
            if allowed {
                let (consent, action) = {
                    let c = st.device.lock().unwrap();
                    let consent = c.consent_candidate(&id, &roots)?;
                    let action = c.consent_confirmation(&id, &consent, &roots)?;
                    (consent, action)
                };
                let action = {
                    let mut c = st.confirmations.lock().unwrap();
                    let prepared = c.prepare_main(action)?;
                    c.queue(prepared)?
                };
                confirmation::request(&app, action.clone())?;
                route::confirmed(&app, &action, &program::Cancel::default())
                    .map_err(|e| format!("{e}; {id}'s native imports stay as they were"))?;
                st.device
                    .lock()
                    .unwrap()
                    .allow_imports(&id, &consent, &roots)?;
            } else {
                st.device.lock().unwrap().revoke_imports(&id)?;
                st.routes.revoked(&id);
            }
            let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-device", ());
            let view = st.device.lock().unwrap().view(&roots);
            Ok(view)
        }
        #[cfg(not(target_os = "linux"))]
        {
            let _ = (app, allowed);
            Err(format!("{id}: native imports are supported on Linux only"))
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
fn device_remove(webview: Webview, id: String) -> Result<device::View, String> {
    let view = device_change(&webview, &id, |c| c.remove(&id));
    // A removed package keeps no grants: installing it again starts with none.
    #[cfg(unix)]
    if webview
        .state::<App>()
        .device
        .lock()
        .unwrap()
        .get(&id)
        .is_none()
    {
        let st = webview.state::<App>();
        let removed = st.access.lock().unwrap().remove_package(&id);
        st.access_done.notify_all();
        removed.map_err(|e| format!("{id} was removed, but its grants could not be: {e}"))?;
    }
    view
}

// Access (`access.rs`): the window's commands, the native file chooser and confirmation for
// grants and signers, and the broker that answers device pages' `resources.*` calls. Grants
// start only here, from the window; pages can only read what was granted.

/// Asks the person to confirm action in a native confirmation. None: they cancelled.
#[cfg(target_os = "linux")]
fn access_confirm(
    app: &AppHandle,
    action: confirmation::Action,
) -> Result<Option<confirmation::Authorized>, String> {
    let st = app.state::<App>();
    let id = {
        let mut c = st.confirmations.lock().unwrap();
        let p = c.prepare_main(action)?;
        c.queue(p)?
    };
    confirmation::request(app, id.clone())?;
    // The dialog expires after a minute; this waits a little longer for its answer.
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(75);
    loop {
        let result = st.confirmations.lock().unwrap().take_result(&id);
        match result {
            Some(Ok(Some(token))) => {
                return st
                    .confirmations
                    .lock()
                    .unwrap()
                    .consume(&id, &token)
                    .map(Some)
            }
            Some(Ok(None)) => return Ok(None),
            Some(Err(e)) => return Err(e),
            None if std::time::Instant::now() > deadline => {
                return Err("the native confirmation did not answer".into())
            }
            None => std::thread::sleep(std::time::Duration::from_millis(100)),
        }
    }
}

/// Asks the person for a file (or a folder) in the native file chooser, titled title. None:
/// they cancelled. With GTK_USE_PORTAL=1, or inside a sandbox, it is the desktop portal's.
#[cfg(target_os = "linux")]
fn access_choose(
    app: &AppHandle,
    title: String,
    folder: bool,
) -> Result<Option<std::path::PathBuf>, String> {
    let (tx, rx) = std::sync::mpsc::channel();
    app.run_on_main_thread(move || {
        use gtk::prelude::*;
        let d = gtk::FileChooserNative::new(
            Some(&title),
            None::<&gtk::Window>,
            if folder {
                gtk::FileChooserAction::SelectFolder
            } else {
                gtk::FileChooserAction::Open
            },
            Some("_Choose"),
            Some("_Cancel"),
        );
        d.set_modal(true);
        d.set_local_only(true);
        // Kept until it answers: nothing else holds a native chooser.
        let keep = std::rc::Rc::new(std::cell::RefCell::new(Some(d.clone())));
        d.connect_response(move |d, r| {
            let Some(_kept) = keep.borrow_mut().take() else {
                return;
            };
            let chosen = (r == gtk::ResponseType::Accept)
                .then(|| d.filename())
                .flatten();
            let _ = tx.send(chosen);
        });
        d.show();
    })
    .map_err(|e| e.to_string())?;
    rx.recv().map_err(|_| "the file chooser closed".to_string())
}

fn access_facts(app: &AppHandle, id: &str) -> Result<access::Facts, String> {
    app.state::<App>()
        .device
        .lock()
        .unwrap()
        .facts(id, &trust::TrustStore::current())
        .ok_or_else(|| format!("{id} is not installed on this device"))
}

#[cfg(unix)]
fn grant_view(g: &access::Grant) -> Value {
    serde_json::json!({
        "id": g.id,
        "package": g.package,
        "resource": g.resource,
        "kind": g.kind,
        "operations": g.operations,
        "selected": g.selected,
        "target": g.target,
        "sha256": g.sha256,
        "session_only": g.session_only,
        "changed": g.changed,
        "granted_at": g.granted_at,
    })
}

/// Approved signers and every grant.
#[tauri::command]
fn access_list(webview: Webview) -> Result<Value, String> {
    trusted(&webview)?;
    #[cfg(unix)]
    {
        let a = webview.state::<App>();
        let a = a.access.lock().unwrap();
        Ok(serde_json::json!({
            "signers": a.signers(),
            "grants": a.grants().map(grant_view).collect::<Vec<_>>(),
            "warning": a.warning,
        }))
    }
    #[cfg(not(unix))]
    {
        Err("Access is supported on Linux only".into())
    }
}

/// Approves the signer of package id's active release, for its source, in a native
/// confirmation that shows the signer's full public key.
#[tauri::command]
async fn access_signer(webview: Webview, id: String) -> Result<Value, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(target_os = "linux")]
        {
            let f = access_facts(&app, &id)?;
            let s = f
                .signed
                .clone()
                .ok_or_else(|| format!("{id} is not signed: there is no signer to approve"))?;
            if f.source.as_deref() != Some(s.source.as_str()) {
                return Err(format!("{id}: its signature names another source"));
            }
            if app.state::<App>().access.lock().unwrap().approved(&s) {
                return Ok(serde_json::json!({"outcome": "approved"}));
            }
            let action = access::confirmation(
                &f,
                format!(
                    "Approve signer {} key {} for packages from {}.\nPublic key (Ed25519): {}\nPackages this key signs from this source may then ask you for files and folders; each grant still asks you. A signature says who signed a package, not that it is safe.",
                    s.publisher, s.key_id, s.source, s.public_key
                ),
                vec![],
            );
            if access_confirm(&app, action)?.is_none() {
                return Ok(serde_json::json!({"outcome": "cancelled"}));
            }
            if access_facts(&app, &id)?.signed.as_ref() != Some(&s) {
                return Err(format!("{id} changed during review; approve its signer again"));
            }
            app.state::<App>().access.lock().unwrap().approve(&s)?;
            let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-device", ());
            Ok(serde_json::json!({"outcome": "approved"}))
        }
        #[cfg(not(target_os = "linux"))]
        {
            let _ = (app, id);
            Err("Access is supported on Linux only".into())
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Grants package id's resource: the person chooses a file or folder in the native chooser
/// and confirms what it resolved to in a native confirmation.
#[tauri::command]
async fn access_grant(webview: Webview, id: String, resource: String) -> Result<Value, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(target_os = "linux")]
        {
            let f = access_facts(&app, &id)?;
            let d = app.state::<App>().access.lock().unwrap().may_grant(&f, &resource)?;
            let folder = d.kind == "subtree";
            let title = format!(
                "Choose a {} for {id}: {resource}",
                if folder { "folder" } else { "file" }
            );
            let Some(path) = access_choose(&app, title, folder)? else {
                return Ok(serde_json::json!({"outcome": "cancelled"}));
            };
            let r = access::resolve(&path, &d.kind)?;
            let ops: Vec<&str> = d
                .operations
                .iter()
                .map(String::as_str)
                .filter(|o| access::supported(&d.kind, o))
                .collect();
            let what = format!(
                "Grant {id} {} its resource {resource} ({}).\nChosen: {}\nRead from: {}{}\nIts pages can read {} and send what they read anywhere they can reach. You can revoke this on the Device extensions page.",
                ops.join(" and "),
                d.kind,
                r.selected.display(),
                r.target.display(),
                if r.portal {
                    "\nThe document portal offers this file: the grant lasts until the app quits."
                } else {
                    ""
                },
                if folder { "everything in this folder" } else { "this file" },
            );
            let action = access::confirmation(&f, what, vec![path.clone()]);
            if access_confirm(&app, action)?.is_none() {
                return Ok(serde_json::json!({"outcome": "cancelled"}));
            }
            // What was reviewed is what is granted: the package and the chosen path as they
            // are now. The catalog stays locked until the grant is saved, so the package
            // cannot be removed (and its grants with it) in between.
            let st = app.state::<App>();
            let catalog = st.device.lock().unwrap();
            let now = catalog
                .facts(&id, &trust::TrustStore::current())
                .ok_or_else(|| format!("{id} is not installed on this device"))?;
            if now.sha256 != f.sha256 || now.signed != f.signed || now.source != f.source {
                return Err(format!("{id} changed during review; grant it again"));
            }
            if access::resolve(&path, &d.kind)? != r {
                return Err("the chosen file changed during review; grant it again".into());
            }
            let g = st.access.lock().unwrap().add(&now, &d, &r)?;
            drop(catalog);
            let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-device", ());
            Ok(serde_json::json!({"outcome": "granted", "grant": grant_view(&g)}))
        }
        #[cfg(not(target_os = "linux"))]
        {
            let _ = (app, id, resource);
            Err("Access is supported on Linux only".into())
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Revokes a grant. Answers once the reads in flight under it have finished (they answer
/// `revoked`), saying how many there were.
#[tauri::command]
async fn access_revoke(webview: Webview, grant: String) -> Result<Value, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(unix)]
        {
            let st = app.state::<App>();
            let mut a = st.access.lock().unwrap();
            let finished = a.revoke(&grant)?;
            let deadline = std::time::Instant::now() + std::time::Duration::from_secs(30);
            while a.in_flight(&grant) > 0 {
                let left = deadline.saturating_duration_since(std::time::Instant::now());
                if left.is_zero() {
                    return Err(format!(
                        "revoked; {} reads under it have not finished yet",
                        a.in_flight(&grant)
                    ));
                }
                a = st.access_done.wait_timeout(a, left).unwrap().0;
            }
            drop(a);
            let _ = app.emit_to(EventTarget::webview(MAIN), "pluxy-device", ());
            Ok(serde_json::json!({"finished": finished}))
        }
        #[cfg(not(unix))]
        {
            let _ = (app, grant);
            Err("Access is supported on Linux only".into())
        }
    })
    .await
    .map_err(|e| e.to_string())?
}

/// For the native test: holds a read before its final check while the hold file exists.
#[cfg(all(unix, feature = "native-test"))]
fn access_hold() {
    let Ok(hold) = std::env::var("PLUXY_APP_TEST_ACCESS_HOLD") else {
        return;
    };
    let hold = std::path::PathBuf::from(hold);
    if !hold.exists() {
        return;
    }
    let _ = std::fs::write(hold.with_extension("held"), "");
    let until = std::time::Instant::now() + std::time::Duration::from_secs(30);
    while hold.exists() && std::time::Instant::now() < until {
        std::thread::sleep(std::time::Duration::from_millis(50));
    }
}

/// Answers a device page's `resources.*` call, admitted by the native ingress, off the
/// scheme's thread. The package and release come from the page webview's native binding,
/// never from the call. The reply goes back through the page's own delivery, so its bytes
/// never reach the window.
#[cfg(unix)]
fn access_call(app: AppHandle, label: String, generation: String, data: Value) {
    std::thread::spawn(move || {
        let id = data["id"].as_str().unwrap_or_default().to_string();
        let method = data["method"].as_str().unwrap_or_default().to_string();
        let st = app.state::<App>();
        let binding = st.pages.lock().unwrap().get(&label).cloned();
        // A begun call is in flight until its answer was handed to the page.
        let mut begun = None;
        let result = (|| -> Result<Value, access::Denial> {
            let (package, digest) = binding
                .and_then(|b| b.device_release())
                .ok_or(access::Denial::NotGranted)?;
            let facts = st
                .device
                .lock()
                .unwrap()
                .facts(&package, &trust::TrustStore::current())
                .ok_or(access::Denial::NotGranted)?;
            if method == "resources.status" {
                return Ok(st.access.lock().unwrap().status(&facts, &digest));
            }
            let b = begun.insert(st.access.lock().unwrap().begin(
                &facts,
                &digest,
                &method,
                &data["params"],
            )?);
            let (r, opened) = access::run(b);
            #[cfg(feature = "native-test")]
            access_hold();
            st.access.lock().unwrap().finish(b, r, opened)
        })();
        let data = match result {
            Ok(v) => serde_json::json!({"v": 0, "id": id, "result": v}),
            Err(d) => {
                serde_json::json!({"v": 0, "id": id, "error": {"code": d.code(), "message": d.message()}})
            }
        };
        let _ = deliver_page(
            &app,
            &label,
            &generation,
            serde_json::json!({"type": "message", "data": data}),
        );
        // A revocation answers only once what was read is with the page (or dropped).
        if let Some(b) = begun {
            st.access.lock().unwrap().delivered(&b);
            st.access_done.notify_all();
        }
    });
}

/// Makes package id's last good release active again. For a native tool the person enabled,
/// that is a swap (`native_swap`).
#[tauri::command]
async fn device_rollback(webview: Webview, id: String) -> Result<device::View, String> {
    trusted(&webview)?;
    let app = webview.app_handle().clone();
    tauri::async_runtime::spawn_blocking(move || {
        #[cfg(target_os = "linux")]
        {
            let swaps = {
                let st = app.state::<App>();
                let c = st.device.lock().unwrap();
                c.get(&id)
                    .and_then(|p| p.last_good.as_ref())
                    .is_some_and(|r| c.swaps(&id, r))
            };
            if swaps {
                return native_swap(&app, &id, |c, roots| c.swap_rollback(&id, roots));
            }
        }
        device_changed(&app, &id, |c| c.rollback(&id))
    })
    .await
    .map_err(|e| e.to_string())?
}

/// What the program host starts tool id from: its admitted record, checked now
/// (`Catalog::admitted`).
#[cfg(target_os = "linux")]
fn program_resolver(app: AppHandle) -> program::Resolve {
    Box::new(move |id| {
        // Its files are hashed without holding the catalog (`checked_unlocked`).
        device::admitted_unlocked(
            &app.state::<App>().device,
            id,
            &trust::TrustStore::current(),
        )
    })
}

/// Calls operation op of the native tool package with input, starting the tool if it is not
/// running, after a native confirmation for a write or launch (`route.rs`). With a ticket,
/// `program_cancel` cancels the call.
#[tauri::command]
async fn program_call(
    webview: Webview,
    package: String,
    op: String,
    input: Value,
    ticket: Option<String>,
) -> Result<Value, String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        let app = webview.app_handle().clone();
        // Registered now, before it can wait, so a cancel or the window's closing reaches it.
        let call = app
            .state::<App>()
            .programs
            .begin_call(ticket.as_deref())
            .map_err(|e| e.to_string())?;
        tauri::async_runtime::spawn_blocking(move || {
            // On a thread of its own that ends with the call: a tool this call starts is held by
            // the host's supervisor thread, never by the thread that started it.
            std::thread::Builder::new()
                .name("pluxy-prog-call".into())
                .spawn(move || {
                    route::call_main(&app, call, &package, &op, input, None)
                        .map_err(|e| e.to_string())
                })
                .map_err(|e| e.to_string())?
                .join()
                .map_err(|_| "the call's thread failed".to_string())?
        })
        .await
        .map_err(|e| e.to_string())?
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (package, op, input, ticket);
        Err("native tools run on Linux only for now".into())
    }
}

/// Cancels the native tool call with ticket.
#[tauri::command]
fn program_cancel(webview: Webview, ticket: String) -> Result<(), String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        webview.state::<App>().programs.cancel(&ticket)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = ticket;
        Err("native tools run on Linux only for now".into())
    }
}

/// Whether the native tool package runs, and how it last stopped.
#[tauri::command]
fn program_status(webview: Webview, package: String) -> Result<Value, String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        let st = webview.state::<App>();
        let mut status =
            serde_json::to_value(st.programs.status(&package)).map_err(|e| e.to_string())?;
        // Its journals: open ones, and how recent ones ended.
        status["journals"] =
            serde_json::to_value(st.vault.journals(&package)).map_err(|e| e.to_string())?;
        Ok(status)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = package;
        Err("native tools run on Linux only for now".into())
    }
}

/// Asks for a secret in native key entry and keeps it in the vault as label; gives its ID. The
/// secret never passes through the window: GTK's hidden entry reads it.
#[tauri::command]
async fn vault_secret_add(webview: Webview, label: String) -> Result<Value, String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        let app = webview.app_handle().clone();
        if app
            .state::<App>()
            .key_entry_open
            .swap(true, Ordering::SeqCst)
        {
            return Err("native key entry is already open".into());
        }
        tauri::async_runtime::spawn_blocking(move || {
            let st = app.state::<App>();
            let input = key_entry::request_as(
                &app,
                "pluxy secret",
                format!(
                    "The secret to keep in this device's vault as \"{label}\". Native tools get it only after you confirm each use."
                ),
                false,
            );
            st.key_entry_open.store(false, Ordering::SeqCst);
            match input {
                Ok(Some(input)) => st
                    .vault
                    .add_secret(&label, input.key.as_str())
                    .map(|id| serde_json::json!({"id": id})),
                Ok(None) => Err("cancelled".into()),
                Err(e) => Err(e.into()),
            }
        })
        .await
        .map_err(|e| e.to_string())?
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = label;
        Err("the vault is on Linux only for now".into())
    }
}

/// The vault's secrets: IDs, labels and when each was entered, never a value.
#[tauri::command]
fn vault_secrets(webview: Webview) -> Result<Value, String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        Ok(serde_json::json!({"secrets": webview.state::<App>().vault.secrets()}))
    }
    #[cfg(not(target_os = "linux"))]
    {
        Err("the vault is on Linux only for now".into())
    }
}

/// Removes secret id from the vault; references to it are refused from now on.
#[tauri::command]
fn vault_secret_remove(webview: Webview, id: String) -> Result<(), String> {
    trusted(&webview)?;
    #[cfg(target_os = "linux")]
    {
        webview.state::<App>().vault.remove_secret(&id)
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = id;
        Err("the vault is on Linux only for now".into())
    }
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
    // A page's call of a native operation is the router's, never the window's.
    let msg = msg.and_then(|m| {
        if m.kind != "call" || m.data["method"] != "native.call" {
            return Some(m);
        }
        let id = m.data["id"].as_str().unwrap_or_default().to_string();
        let app = app.clone();
        let spawned = std::thread::Builder::new()
            .name("pluxy-page-call".into())
            .spawn(move || {
                #[cfg(target_os = "linux")]
                route::call_page(&app, &m.label, &m.generation, &id, &m.data["params"]);
                #[cfg(not(target_os = "linux"))]
                let _ = deliver_page(
                    &app,
                    &m.label,
                    &m.generation,
                    serde_json::json!({"type": "message", "data": {"v": 0, "id": id,
                        "error": {"code": "unavailable", "message": "native tools run on Linux only for now"}}}),
                );
            });
        if let Err(e) = spawned {
            eprintln!("pluxy-app: a page's native call: {e}");
        }
        None
    });
    if let Some(m) = msg {
        #[cfg(all(target_os = "linux", feature = "native-test"))]
        if m.kind == "call" && m.data.get("id").and_then(Value::as_str) == Some("spoof-confirm") {
            confirmation::native::PAGE_CLAIMS.fetch_add(1, Ordering::SeqCst);
        }
        // Access calls go to the broker, never to the window.
        let access = m.kind == "call"
            && m.data["method"]
                .as_str()
                .is_some_and(|x| x.starts_with("resources."));
        if access {
            #[cfg(unix)]
            access_call(app.clone(), m.label, m.generation, m.data);
            // No broker off Unix: the call is answered, never left waiting.
            #[cfg(not(unix))]
            {
                let d = serde_json::json!({"v": 0, "id": m.data["id"], "error": {"code": "unavailable", "message": "Access is supported on Linux only"}});
                let _ = deliver_page(
                    &app,
                    &m.label,
                    &m.generation,
                    serde_json::json!({"type": "message", "data": d}),
                );
            }
        } else {
            let result = app.emit_to(EventTarget::webview(MAIN), "pluxy-page", m);
            #[cfg(feature = "native-test")]
            if result.is_ok() {
                native_test::ingress_forwarded();
            }
            let _ = result;
        }
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
            management_key_rotate,
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
            access_list,
            access_signer,
            access_grant,
            access_revoke,
            device_imports,
            program_call,
            program_cancel,
            program_status,
            vault_secret_add,
            vault_secrets,
            vault_secret_remove,
            window_get,
            window_set,
            window_close,
            privacy_get,
            privacy_set,
        ])
        .register_asynchronous_uri_scheme_protocol(pages::SCHEME, page_scheme)
        .register_asynchronous_uri_scheme_protocol(device::ASSET_SCHEME, asset_scheme)
        .setup(|app| {
            // First, before the core, the tray or any webview: one app per profile. A later launch
            // has the first show its window, and ends here, having started nothing.
            let instance =
                match instance::start(&app.path().app_data_dir()?.join("instance"), instance::WAIT)
                {
                    Ok(instance::Start::First(i)) => i,
                    Ok(instance::Start::Raised) => {
                        eprintln!("pluxy-app: already running; its window is shown");
                        std::process::exit(0);
                    }
                    Err(e) => {
                        eprintln!("pluxy-app: {e}");
                        std::process::exit(1);
                    }
                };
            let raise = app.handle().clone();
            if let Err(e) = instance.listen(&instance::socket_dirs(), move || raise_window(&raise))
            {
                // The lock still keeps a second app out; a later launch is told at once that this
                // window cannot be raised.
                eprintln!("pluxy-app: no way to raise this window from a later launch: {e}");
            }
            app.manage(instance);
            // Next, before any other step: the updater gate's report (it starts nothing; it reads
            // the compiled marker, identifier and product name). A later launch ended above.
            #[cfg(feature = "native-test")]
            native_test::updater_gate(app.handle());
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
            let device_dirs = device::Dirs {
                root: app.path().app_data_dir()?.join("device-packages"),
                registries: config.join("device-registries.json"),
            };
            // `--safe-mode`: no page of a package on this device is listed, opened or served.
            let safe_mode = std::env::args().skip(1).any(|a| a == "--safe-mode");
            let catalog = device::Catalog::open(
                device_dirs.clone(),
                safe_mode,
                &trust::TrustStore::current(),
            );
            if let Some(w) = &catalog.warning {
                eprintln!("pluxy-app: {w}");
            }
            #[cfg(unix)]
            let access_store = access::Store::open(&config);
            #[cfg(unix)]
            if let Some(w) = &access_store.warning {
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
            #[cfg(target_os = "linux")]
            let vault =
                std::sync::Arc::new(vault::Vault::open(app.path().app_data_dir()?.join("vault")));
            #[cfg(target_os = "linux")]
            if let Some(w) = vault.broken() {
                eprintln!("pluxy-app: the vault is not used: {w}");
            }
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
                closing: Mutex::new(Vec::new()),
                device: Mutex::new(catalog),
                device_dirs,
                #[cfg(unix)]
                access: Mutex::new(access_store),
                #[cfg(unix)]
                access_done: std::sync::Condvar::new(),
                #[cfg(target_os = "linux")]
                programs: program::Host::new(
                    program_resolver(app.handle().clone()),
                    Default::default(),
                ),
                #[cfg(target_os = "linux")]
                routes: route::Routes::default(),
                #[cfg(target_os = "linux")]
                vault: std::sync::Arc::clone(&vault),
                status: Mutex::new(None),
                core_actions: Mutex::new(Vec::new()),
                shown: Mutex::new(Shown::default()),
                busy: AtomicBool::new(false),
                window: Mutex::new(window_prefs),
                has_tray: AtomicBool::new(false),
                quitting: AtomicBool::new(false),
                key_entry_open: AtomicBool::new(false),
                key_entry_after: Mutex::new(None),
                rotation_open: AtomicBool::new(false),
            });
            let handle = app.handle().clone();
            // Tools' own calls of other packages' operations go through the router.
            #[cfg(target_os = "linux")]
            handle
                .state::<App>()
                .programs
                .set_route(route::route(handle.clone()));
            // And their vault frames, describe's journals and what is redacted, to the vault.
            #[cfg(target_os = "linux")]
            handle
                .state::<App>()
                .programs
                .set_private(vault as std::sync::Arc<dyn program::Private>);
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
                    // Its native tool calls end, so their tools stop when idle.
                    #[cfg(target_os = "linux")]
                    app.state::<App>().programs.cancel_all();
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
                #[cfg(target_os = "linux")]
                st.programs.stop_all();
                st.conn.lock().unwrap().stop_core();
            }
            if let Some(i) = app.try_state::<instance::Instance>() {
                i.release();
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
