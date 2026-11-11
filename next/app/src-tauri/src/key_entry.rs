//! Native secret entry. Only this module reads GTK's hidden entry; no key is returned to IPC.
use tauri::AppHandle;

pub(crate) struct Input {
    pub key: String,
    pub remember: bool,
}

#[cfg(target_os = "linux")]
pub(crate) fn request(app: &AppHandle) -> Result<Option<Input>, &'static str> {
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
            Some("pluxy management key"),
            Some(&parent),
            gtk::DialogFlags::MODAL | gtk::DialogFlags::DESTROY_WITH_PARENT,
            &[
                ("_Cancel", gtk::ResponseType::Cancel),
                ("_OK", gtk::ResponseType::Accept),
            ],
        );
        dialog.set_default_response(gtk::ResponseType::Accept);
        let content = dialog.content_area();
        content.set_border_width(16);
        content.set_spacing(12);
        let label = gtk::Label::new(Some("Management key"));
        label.set_xalign(0.0);
        content.add(&label);
        let entry = gtk::Entry::new();
        entry.set_visibility(false);
        entry.set_input_purpose(gtk::InputPurpose::Password);
        entry.set_activates_default(true);
        content.add(&entry);
        let keep = gtk::CheckButton::with_mnemonic("_Keep the key on this device");
        // Each entry starts session-only. Remembering requires an explicit native choice.
        keep.set_active(false);
        content.add(&keep);
        let sender = Rc::new(RefCell::new(Some(tx)));
        let response_sender = sender.clone();
        let secret = entry.clone();
        dialog.connect_response(move |d, response| {
            let Some(tx) = response_sender.borrow_mut().take() else {
                return;
            };
            let input = (response == gtk::ResponseType::Accept).then(|| Input {
                key: secret.text().to_string(),
                remember: keep.is_active(),
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

#[cfg(not(target_os = "linux"))]
pub(crate) fn request(_: &AppHandle) -> Result<Option<Input>, &'static str> {
    Err("native key entry is currently supported on Linux only")
}
