use super::*;
use gtk::prelude::*;
use tauri::Manager;

fn text_view(text: &str) -> gtk::ScrolledWindow {
    let label = gtk::Label::new(Some(text));
    label.set_selectable(true);
    label.set_xalign(0.0);
    label.set_yalign(0.0);
    label.set_line_wrap(true);
    let scroll = gtk::ScrolledWindow::new(None::<&gtk::Adjustment>, None::<&gtk::Adjustment>);
    scroll.set_min_content_width(640);
    scroll.set_min_content_height(320);
    scroll.add(&label);
    scroll
}

/// Always host-owned, including the result of a refused page Start. Later ingress calls this
/// with the host-resolved requester/action; a page cannot provide the notice or outcome.
pub(crate) fn notice(app: &tauri::AppHandle, text: String) {
    let _ = app.run_on_main_thread(move || {
        thread_local! { static NOTICE: std::cell::RefCell<Option<gtk::Window>> = const { std::cell::RefCell::new(None) }; }
        NOTICE.with(|slot| {
            let mut slot = slot.borrow_mut();
            let window = slot.get_or_insert_with(|| {
                let w = gtk::Window::new(gtk::WindowType::Toplevel);
                w.set_title(text::NOTICE_TITLE);
                w.set_default_size(560, 100);
                w.connect_delete_event(|w, _| { w.hide(); gtk::glib::Propagation::Stop });
                w
            });
            if let Some(child) = window.child() { window.remove(&child); }
            let label = gtk::Label::new(Some(&text)); label.set_line_wrap(true);
            window.add(&label); window.show_all();
        });
    });
}

pub(crate) fn request(app: &tauri::AppHandle, id: String) -> Result<(), String> {
    let Review {
        review,
        remember: remember_text,
        summary,
        page,
        own,
    } = begin(app, &id)?;
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let dialog = gtk::Dialog::with_buttons(
            Some(text::CONFIRM_TITLE),
            None::<&gtk::Window>,
            gtk::DialogFlags::MODAL,
            &[
                (text::CANCEL, gtk::ResponseType::Cancel),
                (text::CONFIRM, gtk::ResponseType::Accept),
            ],
        );
        dialog.set_default_response(gtk::ResponseType::Cancel);
        if page {
            let ready = std::rc::Rc::new(std::cell::Cell::new(false));
            let gate = ready.clone();
            let confirm = dialog
                .widget_for_response(gtk::ResponseType::Accept)
                .unwrap();
            confirm.set_sensitive(false);
            dialog.connect_map(move |d| {
                if let Some(cancel) = d.widget_for_response(gtk::ResponseType::Cancel) {
                    cancel.grab_focus();
                }
                #[cfg(feature = "native-test")]
                super::native::PAGE_MAPPED_DISABLED
                    .store(!confirm.is_sensitive(), std::sync::atomic::Ordering::SeqCst);
                if !gate.replace(true) {
                    let weak = confirm.downgrade();
                    gtk::glib::timeout_add_local_once(Duration::from_secs(1), move || {
                        if let Some(button) = weak.upgrade() {
                            button.set_sensitive(true);
                        }
                    });
                }
            });
        }
        dialog
            .content_area()
            .pack_start(&text_view(&review), true, true, 8);
        let remember = gtk::CheckButton::with_mnemonic(remember_text.as_deref().unwrap_or(""));
        if remember_text.is_some() {
            dialog.content_area().pack_start(&remember, false, false, 8);
        }
        let app = handle.clone();
        let expires = dialog.downgrade();
        let completed = std::rc::Rc::new(std::cell::Cell::new(false));
        let timed = completed.clone();
        // Closing/expiry cannot manufacture a confirm response. The engine checks its own clock.
        gtk::glib::timeout_add_local_once(LIFETIME, move || {
            if timed.get() {
                return;
            }
            if let Some(d) = expires.upgrade() {
                d.response(gtk::ResponseType::Cancel);
            }
        });
        // Withdrawn (its caller's call was cancelled) or otherwise no longer pending: it closes,
        // as cancelled.
        let watched = dialog.downgrade();
        let watching = completed.clone();
        let (watch_app, watch_id) = (handle.clone(), id.clone());
        gtk::glib::timeout_add_local(Duration::from_millis(250), move || {
            if watching.get() {
                return gtk::glib::ControlFlow::Break;
            }
            if !withdrawn(&watch_app, &watch_id) {
                return gtk::glib::ControlFlow::Continue;
            }
            if let Some(d) = watched.upgrade() {
                d.response(gtk::ResponseType::Cancel);
            }
            gtk::glib::ControlFlow::Break
        });
        dialog.connect_response(move |d, response| {
            if completed.replace(true) {
                return;
            }
            respond(
                &app,
                &id,
                response == gtk::ResponseType::Accept,
                remember.is_active(),
                &summary,
                own,
            );
            d.hide();
            // No GTK state is queried after destroying this completed dialog.
            unsafe {
                d.destroy();
            }
        });
        dialog.show_all();
        if page {
            if let Some(cancel) = dialog.widget_for_response(gtk::ResponseType::Cancel) {
                cancel.grab_focus();
            }
        }
    })
    .map_err(|e| e.to_string())
}

/// Native settings keep revocation out of page messages and DOM events.
pub(crate) fn settings(app: &tauri::AppHandle) -> Result<(), String> {
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let dialog = gtk::Dialog::with_buttons(
            Some(text::SETTINGS_TITLE),
            None::<&gtk::Window>,
            gtk::DialogFlags::MODAL,
            &[(text::SETTINGS_CLOSE, gtk::ResponseType::Close)],
        );
        let approvals = handle
            .state::<crate::App>()
            .confirmations
            .lock()
            .unwrap()
            .approvals
            .clone();
        if approvals.is_empty() {
            dialog
                .content_area()
                .add(&gtk::Label::new(Some(text::NO_LAUNCHES)));
        }
        let rows = gtk::Box::new(gtk::Orientation::Vertical, 8);
        let scroll = gtk::ScrolledWindow::new(None::<&gtk::Adjustment>, None::<&gtk::Adjustment>);
        scroll.set_min_content_width(680);
        scroll.set_min_content_height(500);
        scroll.add(&rows);
        dialog.content_area().pack_start(&scroll, true, true, 4);
        for (index, approval) in approvals.into_iter().rev().enumerate() {
            let row = gtk::Box::new(gtk::Orientation::Vertical, 4);
            row.pack_start(&text_view(&approval_text(&approval)), true, true, 4);
            let revoke = if index == 0 {
                gtk::Button::with_mnemonic(text::REVOKE)
            } else {
                gtk::Button::with_label(&text::plain(text::REVOKE))
            };
            let app = handle.clone();
            revoke.connect_clicked(move |b| match revoke_approval(&app, &approval.id) {
                Ok(()) => {
                    b.set_label(text::REVOKED);
                    b.set_sensitive(false);
                }
                Err(e) => b.set_label(&format!("{}{e}", text::COULD_NOT_REVOKE)),
            });
            row.pack_start(&revoke, false, false, 4);
            rows.pack_start(&row, false, false, 4);
        }
        dialog.connect_response(|d, _| {
            d.hide();
            unsafe {
                d.destroy();
            }
        });
        dialog.show_all();
    })
    .map_err(|e| e.to_string())
}
