//! Native secret entry and the rotated key's window on macOS (`dialog_macos.rs`): an
//! `NSSecureTextField`, which shows no text, refuses copy and cut, and turns on Secure Event Input
//! while it has the focus (other processes' event taps do not see the keys typed).
use super::{text, Input};
use crate::dialog::{After, Spec};
use crate::dialog_macos as dw;
use std::cell::Cell;
use std::sync::mpsc;
use tauri::{AppHandle, Manager};

const CANCEL: u32 = 1;
const OK: u32 = 2;
const COPY: u32 = 3;
const CLOSE: u32 = 4;

pub(crate) fn request_as(
    app: &AppHandle,
    title: &'static str,
    summary: String,
    keep: bool,
    trust: bool,
) -> Result<Option<Input>, &'static str> {
    let (tx, rx) = mpsc::channel();
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let mtm =
            objc2::MainThreadMarker::new().expect("run_on_main_thread runs on the main thread");
        // A sheet on the app's window (modal to it), as on Linux; without one there is no entry.
        let owner = handle
            .get_window(crate::WINDOW)
            .and_then(|w| w.ns_window().ok())
            .and_then(|p| unsafe {
                objc2::rc::Retained::retain(p as *mut objc2_app_kit::NSWindow)
            });
        let Some(owner) = owner else {
            let _ = tx.send(None);
            return;
        };
        let mut tx = Some(tx);
        let mut spec = Spec::new(
            title,
            Box::new(move |c, id| {
                let secret = c.take_secret();
                let input = (id == OK).then(|| Input {
                    key: secret,
                    remember: keep && c.checked(),
                });
                if let Some(tx) = tx.take() {
                    let _ = tx.send(input);
                }
                After::Close
            }),
        );
        spec.text = Some(summary);
        spec.secret = true;
        if keep {
            // Each entry starts session-only. Remembering requires an explicit native choice.
            spec.check = Some(text::KEEP.into());
        }
        // With trust, pressing the confirm button is the answer that the person trusts the
        // network: it is not the default, so Enter does not press it.
        let ok = if trust { text::TRUST_OK } else { text::OK };
        spec.buttons = vec![(CANCEL, text::CANCEL.into()), (OK, ok.into())];
        spec.default = (!trust).then_some(OK);
        spec.cancel = Some(CANCEL);
        // The cancel answer once the window it is a sheet on is gone (closed to the tray, say):
        // AppKit may close a window without ending its sheet.
        let shown_on = objc2::rc::Retained::as_ptr(&owner) as usize;
        let window_app = handle.clone();
        spec.watch = Some(Box::new(move || {
            window_app
                .get_window(crate::WINDOW)
                .and_then(|w| w.ns_window().ok())
                .map(|p| p as usize)
                != Some(shown_on)
        }));
        if let Err(e) = dw::show(mtm, spec, Some(owner)) {
            // The handler, and with it the sender, is gone: the caller hears the entry closed.
            eprintln!("pluxy-app: {e}");
        }
    })
    .map_err(|_| "could not open native key entry")?;
    rx.recv()
        .map_err(|_| "native key entry closed unexpectedly")
}

pub(crate) fn reveal(app: &AppHandle, key: String, notice: String) {
    thread_local! {
        /// The rotated key's window that shows, and its number.
        static SHOWN: Cell<Option<(u64, dw::Handle)>> = const { Cell::new(None) };
        static NEXT: Cell<u64> = const { Cell::new(0) };
    }
    let _ = app.run_on_main_thread(move || {
        let mtm =
            objc2::MainThreadMarker::new().expect("run_on_main_thread runs on the main thread");
        if let Some((_, earlier)) = SHOWN.take() {
            dw::close(earlier);
        }
        let n = NEXT.with(|c| {
            c.set(c.get() + 1);
            c.get()
        });
        let copy = zeroize::Zeroizing::new(key.clone());
        let mut spec = Spec::new(
            text::REVEAL_TITLE,
            Box::new(move |_, id| {
                if id == COPY {
                    dw::copy_concealed(&copy);
                    return After::Stay;
                }
                if SHOWN.get().is_some_and(|(m, _)| m == n) {
                    SHOWN.set(None);
                }
                After::Close
            }),
        );
        spec.text = Some(notice);
        spec.value = Some(zeroize::Zeroizing::new(key));
        spec.buttons = vec![(COPY, text::COPY.into()), (CLOSE, text::CLOSE.into())];
        spec.default = Some(CLOSE);
        spec.cancel = Some(CLOSE);
        spec.focus = Some(CLOSE);
        // Not owned by the window: closing it to the tray must leave the only display of the key.
        match dw::show(mtm, spec, None) {
            Ok(h) => SHOWN.set(Some((n, h))),
            Err(e) => eprintln!("pluxy-app: {e}"),
        }
    });
}
