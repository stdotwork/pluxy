//! Native secret entry, and the native window showing a rotated key once. Only this module (and,
//! on Windows and macOS, its platform file) reads the hidden entry or shows a key; no key is
//! returned to IPC. Linux uses GTK here; Windows `key_entry_windows.rs`; macOS
//! `key_entry_macos.rs`.
use tauri::AppHandle;

pub(crate) struct Input {
    #[cfg(target_os = "linux")]
    pub key: gtk::glib::GString,
    /// Cleared from memory when dropped.
    #[cfg(not(target_os = "linux"))]
    pub key: zeroize::Zeroizing<String>,
    pub remember: bool,
}

/// The dialogs' fixed texts, in GTK's mnemonic form (`dialog.rs` renders them elsewhere).
pub(crate) mod text {
    pub(crate) const MANAGEMENT_TITLE: &str = "pluxy management key";
    pub(crate) const REVEAL_TITLE: &str = "pluxy new management key";
    pub(crate) const KEEP: &str = "_Keep the key on this device";
    pub(crate) const CANCEL: &str = "_Cancel";
    pub(crate) const OK: &str = "_OK";
    /// The confirm button when the window asks whether the person trusts the network: pressing it
    /// is their answer, so it is not the default and Enter does not press it.
    pub(crate) const TRUST_OK: &str = "_Trust this network and connect";
    pub(crate) const COPY: &str = "Co_py";
    pub(crate) const CLOSE: &str = "_Close";
}

#[cfg(windows)]
#[path = "key_entry_windows.rs"]
mod platform;
#[cfg(target_os = "macos")]
#[path = "key_entry_macos.rs"]
mod platform;
#[cfg(any(windows, target_os = "macos"))]
pub(crate) use platform::{request_as, reveal};

/// The management key's entry, with its choice to keep the key on this device; with trust, its
/// confirm button is the person's answer that they trust the network (`text::TRUST_OK`).
pub(crate) fn request(
    app: &AppHandle,
    summary: String,
    trust: bool,
) -> Result<Option<Input>, &'static str> {
    request_as(app, text::MANAGEMENT_TITLE, summary, true, trust)
}

/// A hidden entry titled title, saying summary; with keep, a choice (off at first) to keep the
/// key on this device; with trust, `text::TRUST_OK` as its confirm button, with no default.
#[cfg(target_os = "linux")]
pub(crate) fn request_as(
    app: &AppHandle,
    title: &'static str,
    summary: String,
    keep: bool,
    trust: bool,
) -> Result<Option<Input>, &'static str> {
    use gtk::prelude::*;
    use std::{cell::RefCell, rc::Rc, sync::mpsc};
    use tauri::Manager;
    let (tx, rx) = mpsc::channel();
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let parent = handle
            .get_window(crate::WINDOW)
            .and_then(|w| w.gtk_window().ok());
        let Some(parent) = parent else {
            let _ = tx.send(None);
            return;
        };
        let dialog = gtk::Dialog::with_buttons(
            Some(title),
            Some(&parent),
            gtk::DialogFlags::MODAL | gtk::DialogFlags::DESTROY_WITH_PARENT,
            &[
                (text::CANCEL, gtk::ResponseType::Cancel),
                (
                    if trust { text::TRUST_OK } else { text::OK },
                    gtk::ResponseType::Accept,
                ),
            ],
        );
        if !trust {
            dialog.set_default_response(gtk::ResponseType::Accept);
        }
        let content = dialog.content_area();
        content.set_border_width(16);
        content.set_spacing(12);
        let label = gtk::Label::new(Some(&summary));
        label.set_xalign(0.0);
        label.set_line_wrap(true);
        label.set_max_width_chars(60);
        content.add(&label);
        let entry = gtk::Entry::new();
        entry.set_visibility(false);
        entry.set_input_purpose(gtk::InputPurpose::Password);
        entry.set_activates_default(true);
        content.add(&entry);
        let keep_choice = gtk::CheckButton::with_mnemonic(text::KEEP);
        // Each entry starts session-only. Remembering requires an explicit native choice.
        keep_choice.set_active(false);
        if keep {
            content.add(&keep_choice);
        }
        let sender = Rc::new(RefCell::new(Some(tx)));
        let response_sender = sender.clone();
        let secret = entry.clone();
        dialog.connect_response(move |d, response| {
            let Some(tx) = response_sender.borrow_mut().take() else {
                return;
            };
            let input = (response == gtk::ResponseType::Accept).then(|| Input {
                key: secret.text(),
                remember: keep && keep_choice.is_active(),
            });
            secret.set_text("");
            d.hide();
            unsafe {
                d.destroy();
            }
            let _ = tx.send(input);
        });
        dialog.connect_destroy(move |_| {
            if let Some(tx) = sender.borrow_mut().take() {
                let _ = tx.send(None);
            }
        });
        dialog.show_all();
        entry.grab_focus();
    })
    .map_err(|_| "could not open native key entry")?;
    rx.recv()
        .map_err(|_| "native key entry closed unexpectedly")
}

/// Shows a rotated management key once, with notice saying how the app keeps it, a Copy button
/// (to the clipboard) and Close. Returns once the window is asked for; it does not wait. The
/// window stays open when the main window closes to the tray, and replaces the window of an
/// earlier rotation, whose key no longer works.
#[cfg(target_os = "linux")]
pub(crate) fn reveal(app: &AppHandle, key: String, notice: String) {
    use gtk::prelude::*;
    use tauri::Manager;
    thread_local! {
        static SHOWN: std::cell::RefCell<Option<gtk::Dialog>> = const { std::cell::RefCell::new(None) };
    }
    let handle = app.clone();
    let _ = app.run_on_main_thread(move || {
        if let Some(earlier) = SHOWN.with(|s| s.borrow_mut().take()) {
            // SAFETY: on the GTK thread; nothing uses the earlier window afterwards.
            unsafe {
                earlier.destroy();
            }
        }
        let parent = handle
            .get_window(crate::WINDOW)
            .and_then(|w| w.gtk_window().ok());
        // Not destroyed with its parent: closing the main window to the tray must leave the
        // only display of the new key open.
        let dialog = gtk::Dialog::with_buttons(
            Some(text::REVEAL_TITLE),
            parent.as_ref(),
            gtk::DialogFlags::empty(),
            &[
                (text::COPY, gtk::ResponseType::Apply),
                (text::CLOSE, gtk::ResponseType::Close),
            ],
        );
        dialog.set_default_response(gtk::ResponseType::Close);
        let content = dialog.content_area();
        content.set_border_width(16);
        content.set_spacing(12);
        let label = gtk::Label::new(Some(&notice));
        label.set_xalign(0.0);
        label.set_line_wrap(true);
        label.set_max_width_chars(60);
        content.add(&label);
        let value = gtk::Label::new(Some(&key));
        value.set_xalign(0.0);
        value.set_selectable(true);
        value.style_context().add_class("monospace");
        content.add(&value);
        dialog.connect_response(move |d, response| {
            if response == gtk::ResponseType::Apply {
                gtk::Clipboard::get(&gtk::gdk::SELECTION_CLIPBOARD).set_text(&key);
                return;
            }
            SHOWN.with(|s| {
                let mut s = s.borrow_mut();
                if s.as_ref() == Some(d) {
                    *s = None;
                }
            });
            d.hide();
            unsafe {
                d.destroy();
            }
        });
        dialog.show_all();
        dialog.present();
        SHOWN.with(|s| *s.borrow_mut() = Some(dialog));
    });
}

#[cfg(not(any(target_os = "linux", windows, target_os = "macos")))]
pub(crate) fn request_as(
    _: &AppHandle,
    _: &'static str,
    _: String,
    _: bool,
    _: bool,
) -> Result<Option<Input>, &'static str> {
    Err("native key entry is supported on Linux, Windows and macOS only")
}
