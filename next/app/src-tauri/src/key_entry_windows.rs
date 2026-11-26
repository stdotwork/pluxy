//! Native secret entry and the rotated key's window on Windows (`dialog_windows.rs`): a password
//! edit control, which shows no text, refuses copy and cut, and refuses other processes' reads.
use super::{text, Input};
use crate::dialog::{After, Spec};
use crate::dialog_windows as dw;
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
) -> Result<Option<Input>, &'static str> {
    let (tx, rx) = mpsc::channel();
    let handle = app.clone();
    app.run_on_main_thread(move || {
        // Modal to the app's window, as on Linux; without one there is no entry.
        let owner = handle.get_window(crate::WINDOW).and_then(|w| w.hwnd().ok());
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
        spec.buttons = vec![(CANCEL, text::CANCEL.into()), (OK, text::OK.into())];
        spec.default = Some(OK);
        spec.cancel = Some(CANCEL);
        if let Err(e) = dw::show(spec, Some(owner)) {
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
                    if let Err(e) = dw::copy_concealed(&copy) {
                        eprintln!("pluxy-app: could not copy the new key: {e}");
                    }
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
        match dw::show(spec, None) {
            Ok(h) => SHOWN.set(Some((n, h))),
            Err(e) => eprintln!("pluxy-app: {e}"),
        }
    });
}
