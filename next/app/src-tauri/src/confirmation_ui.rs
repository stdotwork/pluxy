use super::*;
use gtk::prelude::*;
use tauri::Manager;

fn text_view(text: &str) -> gtk::ScrolledWindow {
    text_view_of(text, 320)
}

/// text, read-only and selectable, in a scrolled area at least height pixels tall.
fn text_view_of(text: &str, height: i32) -> gtk::ScrolledWindow {
    let label = gtk::Label::new(Some(text));
    label.set_selectable(true);
    label.set_xalign(0.0);
    label.set_yalign(0.0);
    label.set_line_wrap(true);
    let scroll = gtk::ScrolledWindow::new(None::<&gtk::Adjustment>, None::<&gtk::Adjustment>);
    scroll.set_min_content_width(640);
    scroll.set_min_content_height(height);
    scroll.add(&label);
    scroll
}

/// A confirmation's plain words above its exact details: the words wrapped at the dialog's width,
/// then `text::DETAILS` and the details in a scrolled area of their own.
fn review_view(review: &str, details: &str) -> gtk::Box {
    let words = gtk::Label::new(Some(review));
    words.set_selectable(true);
    words.set_xalign(0.0);
    words.set_line_wrap(true);
    words.set_line_wrap_mode(gtk::pango::WrapMode::WordChar);
    words.set_width_chars(72);
    words.set_max_width_chars(72);
    let heading = gtk::Label::new(Some(text::DETAILS));
    heading.set_xalign(0.0);
    let b = gtk::Box::new(gtk::Orientation::Vertical, 8);
    b.set_border_width(8);
    b.pack_start(&words, false, false, 0);
    b.pack_start(&heading, false, false, 4);
    b.pack_start(&text_view_of(details, 200), true, true, 0);
    b
}

/// The notice's text: wrapped at a readable width, so the window is measured at that width (a
/// wrapped label with no width of its own asks for its height at one word a line, taller than any
/// screen), in an area as tall as its lines up to `NOTICE_MAX_HEIGHT`, scrolled past that. (A
/// scrolled area does not take its text's height here by itself, so the lines are counted.)
fn notice_body(text: &str) -> gtk::ScrolledWindow {
    let label = gtk::Label::new(Some(text));
    label.set_line_wrap(true);
    label.set_line_wrap_mode(gtk::pango::WrapMode::WordChar);
    label.set_width_chars(NOTICE_WIDTH_CHARS);
    label.set_max_width_chars(NOTICE_WIDTH_CHARS);
    label.set_xalign(0.0);
    label.set_yalign(0.0);
    label.set_selectable(true);
    let scroll = gtk::ScrolledWindow::new(None::<&gtk::Adjustment>, None::<&gtk::Adjustment>);
    scroll.set_policy(gtk::PolicyType::Never, gtk::PolicyType::Automatic);
    let line = label.create_pango_layout(Some("Xg")).pixel_size().1.max(1);
    // Lines a little shorter than the width, as wrapping breaks between words.
    let per_line = (NOTICE_WIDTH_CHARS - 5) as usize;
    let lines: usize = text
        .split('\n')
        .map(|l| l.chars().count().div_ceil(per_line).max(1))
        .sum();
    let height = i32::try_from(lines)
        .unwrap_or(i32::MAX)
        .saturating_mul(line);
    scroll.set_min_content_height(height.min(NOTICE_MAX_HEIGHT));
    scroll.set_max_content_height(NOTICE_MAX_HEIGHT);
    scroll.add(&label);
    scroll
}
/// The notice's text width, in characters.
const NOTICE_WIDTH_CHARS: i32 = 60;
/// The most the notice's text area grows to, in pixels; longer text scrolls.
const NOTICE_MAX_HEIGHT: i32 = 320;

/// Puts text in the notice window, in place of what it showed, with its Close button, which
/// hides it and is given the focus once it shows.
fn fill_notice(window: &gtk::Window, text: &str) -> gtk::Button {
    if let Some(child) = window.child() {
        window.remove(&child);
    }
    let close = gtk::Button::with_mnemonic(text::NOTICE_CLOSE);
    let weak = window.downgrade();
    close.connect_clicked(move |_| {
        if let Some(w) = weak.upgrade() {
            w.hide();
        }
    });
    let buttons = gtk::ButtonBox::new(gtk::Orientation::Horizontal);
    buttons.set_layout(gtk::ButtonBoxStyle::End);
    buttons.add(&close);
    let body = gtk::Box::new(gtk::Orientation::Vertical, 12);
    body.set_border_width(12);
    body.pack_start(&notice_body(text), true, true, 0);
    body.pack_start(&buttons, false, false, 0);
    window.add(&body);
    // Back to the size the new text asks for, not the last one's.
    window.resize(1, 1);
    close
}

/// Always host-owned, including the result of a refused page Start. Later ingress calls this
/// with the host-resolved requester/action; a page cannot provide the notice or outcome. It has
/// its own Close button, and Escape closes it, as a desktop without a window manager gives no
/// other way to.
pub(crate) fn notice(app: &tauri::AppHandle, text: String) {
    let _ = app.run_on_main_thread(move || {
        thread_local! { static NOTICE: std::cell::RefCell<Option<gtk::Window>> = const { std::cell::RefCell::new(None) }; }
        NOTICE.with(|slot| {
            let mut slot = slot.borrow_mut();
            let window = slot.get_or_insert_with(|| {
                let w = gtk::Window::new(gtk::WindowType::Toplevel);
                w.set_title(text::NOTICE_TITLE);
                w.set_position(gtk::WindowPosition::Center);
                w.connect_delete_event(|w, _| {
                    w.hide();
                    gtk::glib::Propagation::Stop
                });
                w.connect_key_press_event(|w, e| {
                    if e.keyval() == gtk::gdk::keys::constants::Escape {
                        w.hide();
                        return gtk::glib::Propagation::Stop;
                    }
                    gtk::glib::Propagation::Proceed
                });
                w
            });
            let close = fill_notice(window, &text);
            window.show_all();
            close.grab_focus();
        });
    });
}

pub(crate) fn request(app: &tauri::AppHandle, id: String) -> Result<(), String> {
    let Review {
        review,
        details,
        remember: remember_text,
        summary,
        page,
        quiet,
        confirm,
    } = begin(app, &id)?;
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let dialog = gtk::Dialog::with_buttons(
            Some(text::CONFIRM_TITLE),
            None::<&gtk::Window>,
            gtk::DialogFlags::MODAL,
            &[
                (text::CANCEL, gtk::ResponseType::Cancel),
                (confirm, gtk::ResponseType::Accept),
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
        // What will happen in plain words first, the exact details below it.
        match &details {
            Some(details) => {
                dialog
                    .content_area()
                    .pack_start(&review_view(&review, details), true, true, 8)
            }
            None => dialog
                .content_area()
                .pack_start(&text_view(&review), true, true, 8),
        }
        let remember = gtk::CheckButton::with_mnemonic(remember_text.as_deref().unwrap_or(""));
        if remember_text.is_some() {
            dialog.content_area().pack_start(&remember, false, false, 8);
        }
        let app = handle.clone();
        let expires = dialog.downgrade();
        let completed = std::rc::Rc::new(std::cell::Cell::new(false));
        let timed = completed.clone();
        // Closing/expiry cannot manufacture a confirm response. The engine checks its own clock.
        gtk::glib::timeout_add_local_once(SHOWN_LIFETIME, move || {
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
                quiet,
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
