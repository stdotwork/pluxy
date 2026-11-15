//! A page webview's frame navigations on Windows. WebView2 reports a navigation inside a frame
//! only as `FrameNavigationStarting`, which wry does not hook (it hooks the top document's
//! `NavigationStarting`), so the app hooks it here and holds the frame to the same rule as the
//! top: what the rule refuses is cancelled. WebKitGTK and WKWebView give frame navigations to
//! the webview's navigation handler already.
use std::sync::mpsc;
use std::time::Duration;
use tauri::{Url, Webview};
use webview2_com::{take_pwstr, NavigationStartingEventHandler};

/// Sends every frame navigation of webview to allow, and cancels those it refuses. Returns once
/// the hook is in place, or why it could not be placed.
pub fn watch(
    webview: &Webview,
    allow: impl Fn(&Url) -> bool + Send + 'static,
) -> Result<(), String> {
    let (tx, rx) = mpsc::channel();
    webview
        .with_webview(move |pw| {
            let hooked = (|| unsafe {
                let core = pw.controller().CoreWebView2()?;
                let mut token = Default::default();
                core.add_FrameNavigationStarting(
                    &NavigationStartingEventHandler::create(Box::new(move |_, args| {
                        let Some(args) = args else {
                            return Ok(());
                        };
                        let mut uri = Default::default();
                        args.Uri(&mut uri)?;
                        // An address that does not parse goes nowhere.
                        let allowed = Url::parse(&take_pwstr(uri)).is_ok_and(|u| allow(&u));
                        args.SetCancel(!allowed)?;
                        Ok(())
                    })),
                    &mut token,
                )
            })();
            let _ = tx.send(hooked.map_err(|e| e.to_string()));
        })
        .map_err(|e| e.to_string())?;
    // Run at once on the main thread, or by the event loop when called from another thread; a
    // webview already gone drops the closure unrun.
    match rx.recv_timeout(Duration::from_secs(10)) {
        Ok(r) => r.map_err(|e| format!("could not watch the page's frame: {e}")),
        Err(_) => Err("could not watch the page's frame".into()),
    }
}
