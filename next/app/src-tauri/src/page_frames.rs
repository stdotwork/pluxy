//! A page webview's frame navigations on Windows. WebView2 reports a navigation inside a frame
//! only as `FrameNavigationStarting`, which wry does not hook (it hooks the top document's
//! `NavigationStarting`), so the app hooks it here and holds the frame to the same rule as the
//! top: what the rule refuses is cancelled. WebKitGTK and WKWebView give frame navigations to
//! the webview's navigation handler already.
//!
//! A cancel does not always keep the frame's document: when the page's app document's policy
//! (`frame-src`) blocks the address too, WebView2 replaces the frame's document with its own error
//! page, and the navigation ends unsuccessfully without being cancelled. The page's document is
//! then gone, so the app is told (`replaced`), as of any frame navigation that ends that way.
use std::sync::mpsc;
use std::time::Duration;
use tauri::{Url, Webview};
use webview2_com::Microsoft::Web::WebView2::Win32::COREWEBVIEW2_WEB_ERROR_STATUS_OPERATION_CANCELED;
use webview2_com::{take_pwstr, NavigationCompletedEventHandler, NavigationStartingEventHandler};

/// Sends every frame navigation of webview to allow, and cancels those it refuses; calls replaced
/// with WebView2's error status when a frame navigation ends unsuccessfully but not cancelled.
/// Returns once both hooks are in place, or why they could not be placed.
pub fn watch(
    webview: &Webview,
    allow: impl Fn(&Url) -> bool + Send + 'static,
    replaced: impl Fn(i32) + Send + 'static,
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
                        // Cancelled unless its address is read, parses and is allowed.
                        let mut uri = Default::default();
                        let allowed = match args.Uri(&mut uri) {
                            Ok(()) => Url::parse(&take_pwstr(uri)).is_ok_and(|u| allow(&u)),
                            Err(_) => false,
                        };
                        args.SetCancel(!allowed)?;
                        Ok(())
                    })),
                    &mut token,
                )?;
                let mut token = Default::default();
                core.add_FrameNavigationCompleted(
                    &NavigationCompletedEventHandler::create(Box::new(move |_, args| {
                        let Some(args) = args else {
                            return Ok(());
                        };
                        // Replaced unless it is shown to have succeeded, or to have been cancelled.
                        let mut success = Default::default();
                        let succeeded = args.IsSuccess(&mut success).is_ok() && success.as_bool();
                        let mut status = Default::default();
                        let read = args.WebErrorStatus(&mut status).is_ok();
                        if !succeeded
                            && !(read && status == COREWEBVIEW2_WEB_ERROR_STATUS_OPERATION_CANCELED)
                        {
                            replaced(status.0);
                        }
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
