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
                w.set_title("pluxy launch notice");
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
    let (review, launch, summary, page, own, remember_text) = {
        let st = app.state::<crate::App>();
        let mut c = st.confirmations.lock().unwrap();
        let review = c.review(&id)?;
        if !c.showing.insert(id.clone()) {
            return Err("confirmation already showing".into());
        }
        let pending = c.pending.get(&id).unwrap();
        let summary = pending.prepared.notice();
        let (page, own) = (pending.page, pending.prepared.own);
        let remember_text = pending.prepared.remember_text();
        (review.0, review.1, summary, page, own, remember_text)
    };
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let dialog = gtk::Dialog::with_buttons(
            Some("pluxy native confirmation"),
            None::<&gtk::Window>,
            gtk::DialogFlags::MODAL,
            &[
                ("_Cancel", gtk::ResponseType::Cancel),
                ("C_onfirm", gtk::ResponseType::Accept),
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
        let remember = gtk::CheckButton::with_mnemonic(&remember_text);
        // Only a launch from the window, the tray or native UI may offer it: a page or import
        // chain never uses a remembered approval, so it would save one nothing can use.
        if launch && !page {
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
            let gone = !watch_app
                .state::<crate::App>()
                .confirmations
                .lock()
                .unwrap()
                .pending
                .contains_key(&watch_id);
            if !gone {
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
            let st = app.state::<crate::App>();
            let mut c = st.confirmations.lock().unwrap();
            let result = c.finish(
                &id,
                response == gtk::ResponseType::Accept,
                remember.is_active(),
            );
            c.showing.remove(&id);
            let outcome = match &result {
                Ok(Some(_)) => "confirmed",
                Ok(None) => "cancelled",
                Err(_) => "expired, changed or revoked",
            };
            let mut text = format!("{summary}\nOutcome: {outcome}");
            if let Some(warning) = c.saving_warning(&id) {
                text.push_str(&format!("\n{warning}"));
            }
            if c.results.len() >= 64 {
                c.results.clear();
            }
            c.results.insert(id.clone(), result);
            drop(c);
            // The app's own action shows its own outcome (such as the window with a new key).
            if !own {
                notice(&app, text);
            }
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

/// Reviews an app action started in the app's window, and waits for the person's answer: true
/// only after Confirm, with its single-use token spent here. Never call it on the GTK thread,
/// which shows the dialog.
pub(crate) fn confirm_app(
    app: &tauri::AppHandle,
    what: String,
    generation: u64,
) -> Result<bool, String> {
    let id = {
        let st = app.state::<crate::App>();
        let mut c = st.confirmations.lock().unwrap();
        let prepared = c.prepare_app(what, generation)?;
        c.queue(prepared)?
    };
    let st = app.state::<crate::App>();
    // Withdrawn on every way out that is not an answer, so a dialog that never showed does not
    // keep one of the pending places until its lifetime ends.
    if let Err(e) = request(app, id.clone()) {
        st.confirmations.lock().unwrap().withdraw(&id);
        return Err(e);
    }
    // The dialog cancels itself when its lifetime ends; this is only a backstop.
    wait_app(
        &st.confirmations,
        &id,
        Instant::now() + LIFETIME + Duration::from_secs(10),
    )
}

/// Waits for the result of app confirmation id until deadline: true after Confirm, with its
/// single-use token spent. One that does not finish by then is withdrawn.
pub(super) fn wait_app(
    confirmations: &std::sync::Mutex<Confirmations>,
    id: &str,
    deadline: Instant,
) -> Result<bool, String> {
    loop {
        let mut c = confirmations.lock().unwrap();
        match c.take_result(id) {
            Some(Ok(Some(token))) => {
                c.consume(id, &token)?;
                return Ok(true);
            }
            Some(Ok(None)) => return Ok(false),
            Some(Err(e)) => return Err(e),
            None if Instant::now() > deadline => {
                c.withdraw(id);
                return Err("the confirmation did not finish".into());
            }
            None => {}
        }
        drop(c);
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// Native settings keep revocation out of page messages and DOM events.
pub(crate) fn settings(app: &tauri::AppHandle) -> Result<(), String> {
    let handle = app.clone();
    app.run_on_main_thread(move || {
        let dialog = gtk::Dialog::with_buttons(
            Some("pluxy remembered launches"),
            None::<&gtk::Window>,
            gtk::DialogFlags::MODAL,
            &[("Close", gtk::ResponseType::Close)],
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
                .add(&gtk::Label::new(Some("No remembered launches.")));
        }
        let rows = gtk::Box::new(gtk::Orientation::Vertical, 8);
        let scroll = gtk::ScrolledWindow::new(None::<&gtk::Adjustment>, None::<&gtk::Adjustment>);
        scroll.set_min_content_width(680);
        scroll.set_min_content_height(500);
        scroll.add(&rows);
        dialog.content_area().pack_start(&scroll, true, true, 4);
        for (index, approval) in approvals.into_iter().rev().enumerate() {
            let row = gtk::Box::new(gtk::Orientation::Vertical, 4);
            row.pack_start(
                &text_view(&review_text(&approval.exact).unwrap_or_else(std::convert::identity)),
                true,
                true,
                4,
            );
            let revoke = if index == 0 {
                gtk::Button::with_mnemonic("_Revoke remembered launch")
            } else {
                gtk::Button::with_label("Revoke remembered launch")
            };
            let app = handle.clone();
            revoke.connect_clicked(move |b| {
                let result = app
                    .state::<crate::App>()
                    .confirmations
                    .lock()
                    .unwrap()
                    .revoke(&approval.id);
                match result {
                    Ok(()) => {
                        b.set_label("Revoked");
                        b.set_sensitive(false);
                    }
                    Err(e) => b.set_label(&format!("Could not revoke: {e}")),
                }
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

/// A host-resolved page Start always reviews, even when an identical approval exists.
pub fn page_start(app: &tauri::AppHandle, prepared: Prepared) -> Result<String, String> {
    if prepared.origin != Origin::Page {
        return Err("page Start requires a page-origin preparation".into());
    }
    let review = prepared.notice();
    let result = app
        .state::<crate::App>()
        .confirmations
        .lock()
        .unwrap()
        .queue(prepared);
    let outcome = match &result {
        Ok(_) => "awaiting native confirmation".to_string(),
        Err(e) => e.clone(),
    };
    notice(app, format!("{review}\nOutcome: {outcome}"));
    let id = result?;
    request(app, id.clone())?;
    Ok(id)
}
